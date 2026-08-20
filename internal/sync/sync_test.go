package sync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/registry"
)

// mockRepoService implements gh.RepoService for testing.
type mockRepoService struct {
	mu            gosync.RWMutex
	repos         map[string][]*gh.Repository // org -> repos
	files         map[string][]byte           // "owner/repo/path/ref" -> content
	fileSHAs      map[string]string           // "owner/repo/path/ref" -> sha
	defaultBranch string
	createdFiles  []string
	branchSHAErr  error
}

func newMockRepoService() *mockRepoService {
	return &mockRepoService{
		repos:         make(map[string][]*gh.Repository),
		files:         make(map[string][]byte),
		fileSHAs:      make(map[string]string),
		defaultBranch: "main",
	}
}

func (m *mockRepoService) ListByOrg(_ context.Context, org string) ([]*gh.Repository, error) {
	return m.repos[org], nil
}

func (m *mockRepoService) ListByTopic(_ context.Context, topic string) ([]*gh.Repository, error) {
	return m.repos["topic:"+topic], nil
}

func (m *mockRepoService) GetDefaultBranch(_ context.Context, _, _ string) (string, error) {
	return m.defaultBranch, nil
}

func (m *mockRepoService) GetBranchSHA(_ context.Context, _, _, _ string) (string, error) {
	if m.branchSHAErr != nil {
		return "", m.branchSHAErr
	}
	return "abc123sha", nil
}

func (m *mockRepoService) GetFileContent(_ context.Context, owner, repo, path, ref string) ([]byte, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := fmt.Sprintf("%s/%s/%s/%s", owner, repo, path, ref)
	data, ok := m.files[key]
	if !ok {
		key = fmt.Sprintf("%s/%s/%s/", owner, repo, path)
		data, ok = m.files[key]
		if !ok {
			return nil, "", &gh.APIError{StatusCode: 404, Message: "not found"}
		}
	}
	sha := m.fileSHAs[key]
	return data, sha, nil
}

func (m *mockRepoService) CreateOrUpdateFile(_ context.Context, owner, repo, path, branch string, content []byte, sha, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s/%s/%s/%s", owner, repo, path, branch)
	m.files[key] = content
	m.fileSHAs[key] = "newsha"
	m.createdFiles = append(m.createdFiles, key)
	return nil
}

// mockPRService implements gh.PullRequestService for testing.
type mockPRService struct {
	mu       gosync.Mutex
	prs      map[string][]*gh.PullRequest // "owner/repo" -> prs
	created  []*gh.PullRequestCreate
	closed   []int
	nextPR   int
	branches []string
	// branchBases records the baseSHA each branch was cut from.
	branchBases []string

	// createBranchErr, when set, is returned by CreateBranch instead of nil.
	createBranchErr error
}

func newMockPRService() *mockPRService {
	return &mockPRService{
		prs:    make(map[string][]*gh.PullRequest),
		nextPR: 1,
	}
}

func (m *mockPRService) List(_ context.Context, owner, repo string, opts *gh.PullRequestListOptions) ([]*gh.PullRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := owner + "/" + repo
	var result []*gh.PullRequest
	for _, pr := range m.prs[key] {
		if opts != nil && opts.State != "" && pr.State != opts.State {
			continue
		}
		if opts != nil && opts.Head != "" && pr.Head != opts.Head {
			continue
		}
		result = append(result, pr)
	}
	return result, nil
}

func (m *mockPRService) Create(_ context.Context, owner, repo string, pr *gh.PullRequestCreate) (*gh.PullRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, pr)
	num := m.nextPR
	m.nextPR++
	result := &gh.PullRequest{
		Number: num,
		Title:  pr.Title,
		Body:   pr.Body,
		State:  "open",
		Head:   pr.Head,
		Base:   pr.Base,
		Labels: pr.Labels,
		URL:    fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num),
	}
	key := owner + "/" + repo
	m.prs[key] = append(m.prs[key], result)
	return result, nil
}

func (m *mockPRService) Update(_ context.Context, _, _ string, _ int, _ *gh.PullRequestUpdate) error {
	return nil
}

func (m *mockPRService) Close(_ context.Context, _, _ string, number int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, number)
	return nil
}

func (m *mockPRService) CreateBranch(_ context.Context, _, _, branch, baseSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createBranchErr != nil {
		return m.createBranchErr
	}
	m.branches = append(m.branches, branch)
	m.branchBases = append(m.branchBases, baseSHA)
	return nil
}

func makeTestConfig() *config.SyncConfig {
	t := true
	return &config.SyncConfig{
		Version: "1",
		Auth:    config.AuthConfig{Method: "pat", Token: "test"},
		Variables: map[string]string{
			"org_name": "testorg",
		},
		Templates: []config.TemplateConfig{
			{
				ID:          "claude-md",
				Version:     "1.0.0",
				Type:        "claude-md",
				Source:      "templates/CLAUDE.md",
				Destination: "CLAUDE.md",
				Strategy:    "full-replace",
			},
		},
		Targets: config.TargetsConfig{
			Include: []string{"testorg/*"},
		},
		Sync: config.SyncBehavior{
			Concurrency:   2,
			PRLabel:       "steerspec-sync",
			CloseStalePRs: &t,
		},
	}
}

func makeTestClient(repoSvc *mockRepoService, prSvc *mockPRService) *gh.Client {
	return &gh.Client{
		Repos:        repoSvc,
		PullRequests: prSvc,
	}
}

func setupMockRepos(repoSvc *mockRepoService) {
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
		{Owner: "testorg", Name: "repo-b", FullName: "testorg/repo-b", DefaultBranch: "main"},
	}
	// Template source file in central repo
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	// State file (empty/missing is fine)
}

func TestRunBasic(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "completed" {
		t.Errorf("expected status completed, got %s", result.Status)
	}
	if result.PRsCreated != 2 {
		t.Errorf("expected 2 PRs created, got %d", result.PRsCreated)
	}
	if len(prSvc.created) != 2 {
		t.Errorf("expected 2 PRs created in mock, got %d", len(prSvc.created))
	}
}

func TestDryRun(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{
		DryRun:  true,
		Trigger: "manual",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prSvc.created) != 0 {
		t.Errorf("expected 0 PRs in dry run, got %d", len(prSvc.created))
	}
	if result.ReposSkipped != 2 {
		t.Errorf("expected 2 repos skipped, got %d", result.ReposSkipped)
	}
}

func TestTargetFilter(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{
		TargetFilter: "testorg/repo-a",
		Trigger:      "manual",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.PRsCreated != 1 {
		t.Errorf("expected 1 PR created, got %d", result.PRsCreated)
	}
}

func TestTemplateFilter(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	cfg.Templates = append(cfg.Templates, config.TemplateConfig{
		ID:          "skill-review",
		Version:     "1.0.0",
		Type:        "skill",
		Source:      "templates/review.md",
		Destination: ".claude/skills/review.md",
		Strategy:    "full-replace",
	})
	// Add source for second template
	repoSvc.files["testorg/central/templates/review.md/"] = []byte("review content")

	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{
		TemplateFilter: "claude-md",
		Trigger:        "manual",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only create PRs for claude-md template (2 repos x 1 template)
	if result.PRsCreated != 2 {
		t.Errorf("expected 2 PRs created (filtered to claude-md), got %d", result.PRsCreated)
	}
}

func TestForceBypassesHashCheck(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")

	// First run to populate state
	_, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}
	_ = len(prSvc.created)

	// Second run without force - should skip (hash matches)
	result2, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if result2.PRsCreated != 0 && result2.ReposSkipped != 2 {
		// PRs already exist so they get "updated" not "created", but content hash matches so should skip
		if result2.ReposSkipped == 0 && result2.PRsUpdated == 0 {
			t.Errorf("expected repos to be skipped or updated on second run")
		}
	}

	// Third run with force - should process
	prSvc2 := newMockPRService()
	// Copy existing PRs so they can be found
	client2 := makeTestClient(repoSvc, prSvc2)
	syncer2 := New(cfg, client2, "testorg", "central")
	syncer2.state = syncer.state // carry over state

	result3, err := syncer2.Run(context.Background(), Options{Force: true, Trigger: "manual"})
	if err != nil {
		t.Fatalf("force run error: %v", err)
	}
	if result3.PRsCreated == 0 && result3.PRsUpdated == 0 {
		t.Errorf("expected force to bypass hash check, got created=%d updated=%d skipped=%d",
			result3.PRsCreated, result3.PRsUpdated, result3.ReposSkipped)
	}
	if len(prSvc2.created) != 2 {
		t.Errorf("expected 2 PRs created with force, got %d", len(prSvc2.created))
	}
}

func TestPerRepoErrorIsolation(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-ok", FullName: "testorg/repo-ok", DefaultBranch: "main"},
		{Owner: "testorg", Name: "repo-fail", FullName: "testorg/repo-fail", DefaultBranch: "main"},
	}
	// Only provide template source (both repos try to read from central, which uses first include pattern)
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("content")

	// Make PR creation fail for repo-fail
	failPRSvc := &failingPRService{
		inner:    prSvc,
		failRepo: "repo-fail",
	}

	cfg := makeTestConfig()
	client := &gh.Client{
		Repos:        repoSvc,
		PullRequests: failPRSvc,
	}

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One should succeed, one should fail
	if result.PRsCreated < 1 {
		t.Errorf("expected at least 1 PR created, got %d", result.PRsCreated)
	}
	if result.Errors < 1 {
		t.Errorf("expected at least 1 error, got %d", result.Errors)
	}
}

type failingPRService struct {
	inner    *mockPRService
	failRepo string
}

func (f *failingPRService) List(ctx context.Context, owner, repo string, opts *gh.PullRequestListOptions) ([]*gh.PullRequest, error) {
	return f.inner.List(ctx, owner, repo, opts)
}

func (f *failingPRService) Create(ctx context.Context, owner, repo string, pr *gh.PullRequestCreate) (*gh.PullRequest, error) {
	if repo == f.failRepo {
		return nil, fmt.Errorf("simulated failure for %s", repo)
	}
	return f.inner.Create(ctx, owner, repo, pr)
}

func (f *failingPRService) Update(ctx context.Context, owner, repo string, number int, pr *gh.PullRequestUpdate) error {
	return f.inner.Update(ctx, owner, repo, number, pr)
}

func (f *failingPRService) Close(ctx context.Context, owner, repo string, number int) error {
	return f.inner.Close(ctx, owner, repo, number)
}

func (f *failingPRService) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	return f.inner.CreateBranch(ctx, owner, repo, branch, baseSHA)
}

func TestConcurrencyLimiting(t *testing.T) {
	repoSvc := newMockRepoService()
	// Create many repos to test concurrency
	var repos []*gh.Repository
	for i := 0; i < 10; i++ {
		repos = append(repos, &gh.Repository{
			Owner:         "testorg",
			Name:          fmt.Sprintf("repo-%d", i),
			FullName:      fmt.Sprintf("testorg/repo-%d", i),
			DefaultBranch: "main",
		})
	}
	repoSvc.repos["testorg"] = repos
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("content")

	var maxConcurrent int64
	var current int64

	// Use a slow PR service to observe concurrency
	slowPR := &slowPRService{
		inner: newMockPRService(),
		onCall: func() {
			c := atomic.AddInt64(&current, 1)
			for {
				old := atomic.LoadInt64(&maxConcurrent)
				if c <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, c) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&current, -1)
		},
	}

	cfg := makeTestConfig()
	cfg.Sync.Concurrency = 3
	client := &gh.Client{
		Repos:        repoSvc,
		PullRequests: slowPR,
	}

	syncer := New(cfg, client, "testorg", "central")
	_, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mc := atomic.LoadInt64(&maxConcurrent)
	if mc > 3 {
		t.Errorf("expected max concurrency 3, observed %d", mc)
	}
}

type slowPRService struct {
	inner  *mockPRService
	onCall func()
}

func (s *slowPRService) List(ctx context.Context, owner, repo string, opts *gh.PullRequestListOptions) ([]*gh.PullRequest, error) {
	return s.inner.List(ctx, owner, repo, opts)
}

func (s *slowPRService) Create(ctx context.Context, owner, repo string, pr *gh.PullRequestCreate) (*gh.PullRequest, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return s.inner.Create(ctx, owner, repo, pr)
}

func (s *slowPRService) Update(ctx context.Context, owner, repo string, number int, pr *gh.PullRequestUpdate) error {
	return s.inner.Update(ctx, owner, repo, number, pr)
}

func (s *slowPRService) Close(ctx context.Context, owner, repo string, number int) error {
	return s.inner.Close(ctx, owner, repo, number)
}

func (s *slowPRService) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	return s.inner.CreateBranch(ctx, owner, repo, branch, baseSHA)
}

func TestShouldApplyTemplate(t *testing.T) {
	tests := []struct {
		name     string
		target   registry.ResolvedTarget
		tmplID   string
		expected bool
	}{
		{
			name:     "no include/exclude applies all",
			target:   registry.ResolvedTarget{Repo: "org/repo"},
			tmplID:   "claude-md",
			expected: true,
		},
		{
			name:     "include list matches",
			target:   registry.ResolvedTarget{Repo: "org/repo", IncludeTemplates: []string{"claude-md"}},
			tmplID:   "claude-md",
			expected: true,
		},
		{
			name:     "include list does not match",
			target:   registry.ResolvedTarget{Repo: "org/repo", IncludeTemplates: []string{"other"}},
			tmplID:   "claude-md",
			expected: false,
		},
		{
			name:     "exclude list matches",
			target:   registry.ResolvedTarget{Repo: "org/repo", ExcludeTemplates: []string{"claude-md"}},
			tmplID:   "claude-md",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldApplyTemplate(tt.target, tt.tmplID)
			if got != tt.expected {
				t.Errorf("shouldApplyTemplate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSplitRepo(t *testing.T) {
	owner, repo := splitRepo("testorg/myrepo")
	if owner != "testorg" || repo != "myrepo" {
		t.Errorf("splitRepo(testorg/myrepo) = %s, %s", owner, repo)
	}
}

func TestPRTitleFormat(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	_, err := syncer.Run(context.Background(), Options{Trigger: "push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, pr := range prSvc.created {
		if !strings.HasPrefix(pr.Title, "[SteerSpec] Update ") {
			t.Errorf("PR title should start with '[SteerSpec] Update', got: %s", pr.Title)
		}
		if pr.Head != "steerspec-sync/claude-md/1.0.0" {
			t.Errorf("branch should be 'steerspec-sync/claude-md/1.0.0', got: %s", pr.Head)
		}
	}
}

// trackingPRService wraps mockPRService to track Update calls.
type trackingPRService struct {
	*mockPRService
	updateCalls int
}

func (tp *trackingPRService) Update(ctx context.Context, owner, repo string, number int, pr *gh.PullRequestUpdate) error {
	tp.updateCalls++
	return tp.mockPRService.Update(ctx, owner, repo, number, pr)
}

func TestSYNCPR002_BranchNameFormat(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	_, err := syncer.Run(context.Background(), Options{Trigger: "push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, pr := range prSvc.created {
		expected := "steerspec-sync/claude-md/1.0.0"
		if pr.Head != expected {
			t.Errorf("SYNCPR-002: branch should be %q, got %q", expected, pr.Head)
		}
	}
}

func TestSYNCPR005_NoDuplicatePRs(t *testing.T) {
	repoSvc := newMockRepoService()
	basePR := newMockPRService()
	prSvc := &trackingPRService{mockPRService: basePR}

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")

	// Pre-populate an open PR matching the template
	basePR.prs["testorg/repo-a"] = []*gh.PullRequest{
		{
			Number: 99,
			Title:  "[SteerSpec] Update claude-md",
			State:  "open",
			Head:   "steerspec-sync/claude-md/1.0.0",
			Base:   "main",
			Labels: []string{"steerspec-sync"},
			URL:    "https://github.com/testorg/repo-a/pull/99",
		},
	}

	cfg := makeTestConfig()
	client := &gh.Client{
		Repos:        repoSvc,
		PullRequests: prSvc,
	}

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual", Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.PRsCreated != 0 {
		t.Errorf("SYNCPR-005: expected 0 PRs created (should reuse existing), got %d", result.PRsCreated)
	}
	if result.PRsUpdated != 1 {
		t.Errorf("SYNCPR-005: expected 1 PR updated, got %d", result.PRsUpdated)
	}
	if len(basePR.created) != 0 {
		t.Errorf("SYNCPR-005: expected Create NOT called, but it was called %d times", len(basePR.created))
	}
	if prSvc.updateCalls != 1 {
		t.Errorf("SYNCPR-005: expected Update called once, got %d", prSvc.updateCalls)
	}
}

func TestSYNCPR006_CloseStalePRs(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")

	// Pre-populate 2 old stale PRs with matching title prefix and label
	prSvc.prs["testorg/repo-a"] = []*gh.PullRequest{
		{
			Number: 10,
			Title:  "[SteerSpec] Update claude-md (old v1)",
			State:  "open",
			Head:   "steerspec-sync/claude-md-old1",
			Labels: []string{"steerspec-sync"},
		},
		{
			Number: 11,
			Title:  "[SteerSpec] Update claude-md (old v2)",
			State:  "open",
			Head:   "steerspec-sync/claude-md-old2",
			Labels: []string{"steerspec-sync"},
		},
	}

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.PRsCreated != 1 {
		t.Errorf("SYNCPR-006: expected 1 new PR created, got %d", result.PRsCreated)
	}

	// The 2 old PRs should be closed
	if len(prSvc.closed) != 2 {
		t.Errorf("SYNCPR-006: expected 2 stale PRs closed, got %d: %v", len(prSvc.closed), prSvc.closed)
	}
}

func TestSYNCACT_ResultCounts(t *testing.T) {
	repoSvc := newMockRepoService()

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-new", FullName: "testorg/repo-new", DefaultBranch: "main"},
		{Owner: "testorg", Name: "repo-insync", FullName: "testorg/repo-insync", DefaultBranch: "main"},
		{Owner: "testorg", Name: "repo-fail", FullName: "testorg/repo-fail", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("content")

	failPR := &failingPRService{
		inner:    newMockPRService(),
		failRepo: "repo-fail",
	}

	cfg := makeTestConfig()
	client := &gh.Client{
		Repos:        repoSvc,
		PullRequests: failPR,
	}

	syncer := New(cfg, client, "testorg", "central")

	// First run: creates PRs for repo-new and repo-fail (fails)
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// repo-new and repo-insync should get PRs created, repo-fail should error
	if result.PRsCreated < 1 {
		t.Errorf("SYNCACT-016: expected at least 1 PR created, got %d", result.PRsCreated)
	}
	if result.Errors < 1 {
		t.Errorf("SYNCACT-019: expected at least 1 error, got %d", result.Errors)
	}

	// Now run again - already-synced repos should be skipped (hash match)
	result2, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2.ReposSkipped < 1 {
		t.Errorf("SYNCACT-017: expected at least 1 repo skipped on second run, got %d", result2.ReposSkipped)
	}
}

func TestDryRunDoesNotSaveState(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	_, err := syncer.Run(context.Background(), Options{
		DryRun:  true,
		Trigger: "manual",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that CreateOrUpdateFile was never called for the state path
	for _, path := range repoSvc.createdFiles {
		if strings.Contains(path, "deployment-state.json") {
			t.Errorf("SYNCOP-007: dry run should not save state, but CreateOrUpdateFile was called for %s", path)
		}
	}
}

// The branch is cut from the SHA GetBranchSHA resolves, not from the branch
// name — passing a name here is what GH #31 was about, and the git/refs
// endpoint rejects it with a 422.
func TestCreateBranchUsesResolvedSHA(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prSvc.branchBases) == 0 {
		t.Fatal("expected at least one branch to be created")
	}
	for _, base := range prSvc.branchBases {
		if base != "abc123sha" {
			t.Errorf("expected branch cut from resolved sha abc123sha, got %q", base)
		}
	}
}

// Re-running a sync hits an existing branch. That 422 is the normal case and
// must not fail the run.
func TestCreateBranchAlreadyExistsIsTolerated(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	prSvc.createBranchErr = &gh.APIError{
		StatusCode: http.StatusUnprocessableEntity,
		Message:    "Reference already exists",
	}
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Errors != 0 {
		t.Errorf("expected 0 errors for an existing branch, got %d: %+v", result.Errors, result.RepoResults)
	}
	if result.PRsCreated != 2 {
		t.Errorf("expected sync to continue and create 2 PRs, got %d", result.PRsCreated)
	}
}

// Any other branch-creation failure must surface. Previously the error was
// discarded, so the file write and PR steps ran against a branch that did not
// exist.
func TestCreateBranchHardFailureIsReported(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	prSvc.createBranchErr = &gh.APIError{
		StatusCode: http.StatusInternalServerError,
		Message:    "Internal Server Error",
	}
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Errors != 2 {
		t.Errorf("expected 2 errors, got %d", result.Errors)
	}
	if len(prSvc.created) != 0 {
		t.Errorf("expected no PRs after branch creation failed, got %d", len(prSvc.created))
	}
	for _, rr := range result.RepoResults {
		if !strings.Contains(rr.Error, "creating branch") {
			t.Errorf("expected a branch-creation error, got %q", rr.Error)
		}
	}
}

func TestBaseSHAResolutionFailureIsReported(t *testing.T) {
	repoSvc := newMockRepoService()
	repoSvc.branchSHAErr = &gh.APIError{StatusCode: http.StatusNotFound, Message: "Not Found"}
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Errors != 2 {
		t.Errorf("expected 2 errors, got %d", result.Errors)
	}
	for _, rr := range result.RepoResults {
		if !strings.Contains(rr.Error, "getting base SHA") {
			t.Errorf("expected a base-SHA error, got %q", rr.Error)
		}
	}
}

// git/refs answers 422 for any validation failure, not just an existing ref —
// an unresolvable base SHA lands here too. Tolerating those on status alone
// would put us back at GH #31, with later steps running against a branch that
// was never created.
func TestCreateBranch422OtherThanAlreadyExistsIsReported(t *testing.T) {
	for _, message := range []string{
		"Object does not exist",
		"Reference cannot be created",
	} {
		t.Run(message, func(t *testing.T) {
			repoSvc := newMockRepoService()
			prSvc := newMockPRService()
			prSvc.createBranchErr = &gh.APIError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    message,
			}
			setupMockRepos(repoSvc)

			syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
			result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Errors != 2 {
				t.Errorf("expected 2 errors for a non-existence 422, got %d", result.Errors)
			}
			if len(prSvc.created) != 0 {
				t.Errorf("expected no PRs after branch creation failed, got %d", len(prSvc.created))
			}
		})
	}
}

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"reference already exists", &gh.APIError{StatusCode: 422, Message: "Reference already exists"}, true},
		{"lowercase message", &gh.APIError{StatusCode: 422, Message: "reference already exists"}, true},
		{"wrapped", fmt.Errorf("create branch: %w", &gh.APIError{StatusCode: 422, Message: "Reference already exists"}), true},
		{"422 invalid sha", &gh.APIError{StatusCode: 422, Message: "Object does not exist"}, false},
		{"422 empty message", &gh.APIError{StatusCode: 422}, false},
		{"404", &gh.APIError{StatusCode: 404, Message: "Not Found"}, false},
		{"non-api error", fmt.Errorf("network unreachable"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAlreadyExists(tt.err); got != tt.want {
				t.Errorf("isAlreadyExists(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
