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
	"github.com/SteerSpec/strspc-sync/internal/hash"
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
	// createFileErr maps "owner/repo/path/branch" to an error to return.
	createFileErr map[string]error
	// commitMessages records, per CreateOrUpdateFile call, the "owner/repo/path/branch"
	// key and the commit message used.
	commitMessages map[string]string
}

func newMockRepoService() *mockRepoService {
	return &mockRepoService{
		repos:          make(map[string][]*gh.Repository),
		files:          make(map[string][]byte),
		fileSHAs:       make(map[string]string),
		commitMessages: make(map[string]string),
		defaultBranch:  "main",
	}
}

func (m *mockRepoService) ListByOrg(_ context.Context, org string) ([]*gh.Repository, error) {
	return m.repos[org], nil
}

func (m *mockRepoService) ListByTopic(_ context.Context, topic, _ string) ([]*gh.Repository, error) {
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
	if err, ok := m.createFileErr[key]; ok {
		return err
	}
	m.files[key] = content
	m.fileSHAs[key] = "newsha"
	m.createdFiles = append(m.createdFiles, key)
	m.commitMessages[key] = message
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

func (m *mockPRService) Get(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pr := range m.prs[owner+"/"+repo] {
		if pr.Number == number {
			return pr, nil
		}
	}
	return nil, fmt.Errorf("pull request %s/%s#%d not found", owner, repo, number)
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

func (f *failingPRService) Get(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	return f.inner.Get(ctx, owner, repo, number)
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

func (s *slowPRService) Get(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	return s.inner.Get(ctx, owner, repo, number)
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

// Sync commits and PR titles are conventional commits, so target repos gated by
// commitlint or action-semantic-pull-request can merge them.
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

	const want = "chore(steerspec-sync): update claude-md to 1.0.0"
	if len(prSvc.created) == 0 {
		t.Fatal("expected PRs to be created")
	}
	for _, pr := range prSvc.created {
		if pr.Title != want {
			t.Errorf("PR title = %q, want %q", pr.Title, want)
		}
		if pr.Head != "steerspec-sync/claude-md/1.0.0" {
			t.Errorf("branch should be 'steerspec-sync/claude-md/1.0.0', got: %s", pr.Head)
		}
	}
	for key, msg := range repoSvc.commitMessages {
		if strings.Contains(key, "deployment-state.json") {
			continue
		}
		if msg != want {
			t.Errorf("commit message for %s = %q, want %q", key, msg, want)
		}
	}
}

// sync.commit-prefix and sync.pr-title-prefix replace the type(scope) part of
// the message; the ": update <id> to <version>" tail is fixed.
func TestConfigurablePrefixes(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	cfg := makeTestConfig()
	cfg.Sync.CommitPrefix = "chore(ai-config)"
	cfg.Sync.PRTitlePrefix = "ci(steerspec)"
	client := makeTestClient(repoSvc, prSvc)

	syncer := New(cfg, client, "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "push"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, pr := range prSvc.created {
		if pr.Title != "ci(steerspec): update claude-md to 1.0.0" {
			t.Errorf("PR title = %q, want the configured pr-title-prefix", pr.Title)
		}
	}
	for key, msg := range repoSvc.commitMessages {
		want := "chore(ai-config): update claude-md to 1.0.0"
		if strings.Contains(key, "deployment-state.json") {
			want = "chore(ai-config): update deployment state"
		}
		if msg != want {
			t.Errorf("commit message for %s = %q, want %q", key, msg, want)
		}
	}
}

// The deployment-state commit lands directly on the central repo's default
// branch, so it has to pass that repo's commitlint too.
func TestStateCommitMessageIsConventional(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "push"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg, ok := repoSvc.commitMessages["testorg/central/.steerspec/deployment-state.json/"]
	if !ok {
		t.Fatalf("deployment state was not written; commits: %v", repoSvc.commitMessages)
	}
	if msg != "chore(steerspec-sync): update deployment state" {
		t.Errorf("state commit message = %q", msg)
	}
}

func TestUpdateMessageAndMatching(t *testing.T) {
	if got := updateMessage("chore(steerspec-sync)", "claude-md", "1.2.0"); got != "chore(steerspec-sync): update claude-md to 1.2.0" {
		t.Errorf("updateMessage = %q", got)
	}

	tests := []struct {
		title string
		want  bool
	}{
		{"chore(steerspec-sync): update claude-md to 1.0.0", true},
		{"chore(steerspec-sync): update claude-md to 0.9.0", true},
		{"[SteerSpec] Update claude-md", true},
		{"[SteerSpec] Update claude-md (old v1)", true},
		{"chore(steerspec-sync): update claude-md-extra to 1.0.0", false},
		{"chore(steerspec-sync): update claude-md", false},
		{"ci(other): update claude-md to 1.0.0", false},
		{"fix: unrelated", false},
	}
	for _, tt := range tests {
		if got := isSyncPRForTemplate(tt.title, "chore(steerspec-sync)", "claude-md"); got != tt.want {
			t.Errorf("isSyncPRForTemplate(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

// trackingPRService wraps mockPRService to track Update calls.
type trackingPRService struct {
	*mockPRService
	updateCalls int
	lastUpdate  *gh.PullRequestUpdate
}

func (tp *trackingPRService) Update(ctx context.Context, owner, repo string, number int, pr *gh.PullRequestUpdate) error {
	tp.updateCalls++
	tp.lastUpdate = pr
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

	// Pre-populate stale PRs: two in the legacy "[SteerSpec] Update" format an
	// older release opened, one in the current conventional format for an
	// earlier version, and one current-format PR for a *different* template
	// whose ID shares the prefix, which must stay open.
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
		{
			Number: 12,
			Title:  "chore(steerspec-sync): update claude-md to 0.9.0",
			State:  "open",
			Head:   "steerspec-sync/claude-md/0.9.0",
			Labels: []string{"steerspec-sync"},
		},
		{
			Number: 13,
			Title:  "chore(steerspec-sync): update claude-md-extra to 0.9.0",
			State:  "open",
			Head:   "steerspec-sync/claude-md-extra/0.9.0",
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

	// The 3 stale PRs for claude-md should be closed, in both title formats.
	if len(prSvc.closed) != 3 {
		t.Errorf("SYNCPR-006: expected 3 stale PRs closed, got %d: %v", len(prSvc.closed), prSvc.closed)
	}
	for _, n := range prSvc.closed {
		if n == 13 {
			t.Errorf("SYNCPR-006: PR #13 belongs to template claude-md-extra and must not be closed")
		}
	}
}

// SYNCPR-003: the body carries previous version, new version, a changelog and
// a diff link. On a first deployment there is no previous version to report.
func TestSYNCPR003_BodyFirstDeployment(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.com/")
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "push"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prSvc.created) == 0 {
		t.Fatal("expected PRs to be created")
	}

	body := prSvc.created[0].Body
	for _, want := range []string{
		"| Previous version | none (first deployment) |",
		"| New version | `1.0.0` |",
		"**Changelog:**",
		"https://ghe.example.com/testorg/central/commits/HEAD/templates/CLAUDE.md",
		"**Diff:**",
		"https://ghe.example.com/testorg/central/blob/HEAD/templates/CLAUDE.md",
		"`CLAUDE.md`",
		"`claude-md`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SYNCPR-003: body missing %q\n%s", want, body)
		}
	}
}

// SYNCPR-003: when deployment state records an earlier version, the body names
// it, including under --force, which skips the hash comparison.
func TestSYNCPR003_BodyPreviousVersion(t *testing.T) {
	repoSvc := newMockRepoService()
	basePR := newMockPRService()
	prSvc := &trackingPRService{mockPRService: basePR}

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(`{
	  "repositories": {"testorg/repo-a": {"claude-md": {"version": "0.9.0", "hash": "blake3:stale"}}}
	}`)
	// An open PR on the branch, so the update path is exercised too.
	basePR.prs["testorg/repo-a"] = []*gh.PullRequest{{
		Number: 7,
		Title:  "chore(steerspec-sync): update claude-md to 1.0.0",
		State:  "open",
		Head:   "steerspec-sync/claude-md/1.0.0",
		Labels: []string{"steerspec-sync"},
	}}

	cfg := makeTestConfig()
	syncer := New(cfg, &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual", Force: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prSvc.lastUpdate == nil || prSvc.lastUpdate.Body == nil {
		t.Fatal("expected the existing PR to be updated with a body")
	}
	body := *prSvc.lastUpdate.Body
	for _, want := range []string{
		"| Previous version | `0.9.0` |",
		"| New version | `1.0.0` |",
		"https://github.com/testorg/central/commits/HEAD/templates/CLAUDE.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SYNCPR-003: body missing %q\n%s", want, body)
		}
	}
	if prSvc.lastUpdate.Title == nil || *prSvc.lastUpdate.Title != "chore(steerspec-sync): update claude-md to 1.0.0" {
		t.Errorf("updated PR title = %v", prSvc.lastUpdate.Title)
	}
}

// GH #56: once the target is in sync, a pr_status recorded as "open" at
// creation time is brought up to date from the PR itself.
func TestSkippedRepoRefreshesPRStatus(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	// Rendered content for org_name=testorg, so the hash matches the state.
	rendered := []byte("# Hello testorg")
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = rendered
	repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(`{
	  "repositories": {"testorg/repo-a": {"claude-md": {"version": "1.0.0", "hash": "` + hash.HashBytes(rendered) + `", "pr_number": 7, "pr_status": "open"}}}
	}`)
	prSvc.prs["testorg/repo-a"] = []*gh.PullRequest{{Number: 7, State: "closed", Merged: true, Head: "steerspec-sync/claude-md/1.0.0"}}

	syncer := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ReposSkipped != 1 {
		t.Fatalf("expected the in-sync repo to be skipped, got %+v", result)
	}
	saved := string(repoSvc.files["testorg/central/.steerspec/deployment-state.json/"])
	if !strings.Contains(saved, `"pr_status": "merged"`) && !strings.Contains(saved, `"pr_status":"merged"`) {
		t.Errorf("saved state should record pr_status merged, got:\n%s", saved)
	}
}

// GH #56: "closed" is not terminal. A PR closed unmerged, reopened and
// merged ends up recorded as merged across successive no-op syncs.
func TestPRStatusFollowsCloseReopenMerge(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	rendered := []byte("# Hello testorg")
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = rendered
	repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(`{
	  "repositories": {"testorg/repo-a": {"claude-md": {"version": "1.0.0", "hash": "` + hash.HashBytes(rendered) + `", "pr_number": 7, "pr_status": "open"}}}
	}`)
	pr := &gh.PullRequest{Number: 7, State: "closed", Merged: false, Head: "steerspec-sync/claude-md/1.0.0"}
	prSvc.prs["testorg/repo-a"] = []*gh.PullRequest{pr}
	syncer := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")

	run := func(want string) {
		t.Helper()
		if _, err := syncer.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		saved := string(repoSvc.files["testorg/central/.steerspec/deployment-state.json/"])
		if !strings.Contains(saved, `"pr_status": "`+want+`"`) && !strings.Contains(saved, `"pr_status":"`+want+`"`) {
			t.Fatalf("after PR state %s/merged=%v, saved pr_status should be %q, got:\n%s", pr.State, pr.Merged, want, saved)
		}
	}
	run("closed")
	pr.State = "open"
	run("open")
	pr.State, pr.Merged = "closed", true
	run("merged")
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

// A sync that cannot record its results has violated SYNCOP-008. It used to be
// logged and dropped, so the run reported errors:0 while writing nothing.
func TestSaveStateFailureIsCounted(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	setupMockRepos(repoSvc)
	repoSvc.createFileErr = map[string]error{
		"testorg/central/.steerspec/deployment-state.json/": &gh.APIError{
			StatusCode: http.StatusNotFound, Message: "Branch  not found",
		},
	}

	syncer := New(makeTestConfig(), makeTestClient(repoSvc, prSvc), "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The PRs still happened and must still be reported.
	if result.PRsCreated != 2 {
		t.Errorf("expected 2 PRs created, got %d", result.PRsCreated)
	}
	// But the run must not claim success.
	if result.Errors != 1 {
		t.Errorf("expected the state-save failure counted as 1 error, got %d", result.Errors)
	}
}

// SYNCPR-003: a forced re-run of an open PR keeps reporting the version that
// was deployed before the PR, not the version the PR proposes. The first run
// records 1.0.0 (previous 0.9.0) in state; the forced second run must not turn
// the body into "1.0.0 → 1.0.0".
func TestSYNCPR003_ForceRerunKeepsBaseline(t *testing.T) {
	repoSvc := newMockRepoService()
	basePR := newMockPRService()
	prSvc := &trackingPRService{mockPRService: basePR}

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(`{
	  "repositories": {"testorg/repo-a": {"claude-md": {"version": "0.9.0", "hash": "blake3:stale"}}}
	}`)

	cfg := makeTestConfig()
	syncer := New(cfg, &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")

	// First run creates the PR and records the proposed version in state.
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(basePR.created) != 1 {
		t.Fatalf("expected 1 PR created, got %d", len(basePR.created))
	}
	if !strings.Contains(basePR.created[0].Body, "| Previous version | `0.9.0` |") {
		t.Errorf("first body should report 0.9.0 as previous:\n%s", basePR.created[0].Body)
	}
	ts := syncer.state.GetTemplateState("testorg/repo-a", "claude-md")
	if ts == nil || ts.Version != "1.0.0" || ts.PreviousVersion != "0.9.0" {
		t.Fatalf("state after first run = %+v, want version 1.0.0 previous 0.9.0", ts)
	}

	// Make the PR the first run created visible to the update path.
	basePR.prs["testorg/repo-a"] = []*gh.PullRequest{{
		Number: 1, // the mock numbers created PRs from 1
		Title:  basePR.created[0].Title,
		State:  "open",
		Head:   "steerspec-sync/claude-md/1.0.0",
		Labels: []string{"steerspec-sync"},
	}}

	// Forced second run updates the same PR.
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual", Force: true}); err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if prSvc.lastUpdate == nil || prSvc.lastUpdate.Body == nil {
		t.Fatal("expected the open PR to be updated")
	}
	if !strings.Contains(*prSvc.lastUpdate.Body, "| Previous version | `0.9.0` |") {
		t.Errorf("forced re-run must keep the 0.9.0 baseline:\n%s", *prSvc.lastUpdate.Body)
	}
	ts = syncer.state.GetTemplateState("testorg/repo-a", "claude-md")
	if ts == nil || ts.PreviousVersion != "0.9.0" {
		t.Errorf("state after forced run = %+v, want previous 0.9.0", ts)
	}
}

// SYNCPR-003: link targets are URL-encoded per path segment so spaces and
// reserved characters in a template source do not break the Markdown links
// or get read as a fragment or query.
func TestPRBody_EscapesSourcePath(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	s := New(makeTestConfig(), makeTestClient(newMockRepoService(), newMockPRService()), "testorg", "central")
	tmpl := config.TemplateConfig{
		ID:          "guide",
		Source:      "templates/Claude Guide#1.md?v2",
		Destination: "CLAUDE.md",
		Version:     "1.0.0",
	}
	body := s.prBody(tmpl, "", "blake3:abc")
	for _, want := range []string{
		"https://github.com/testorg/central/commits/HEAD/templates/Claude%20Guide%231.md%3Fv2",
		"https://github.com/testorg/central/blob/HEAD/templates/Claude%20Guide%231.md%3Fv2",
		"`templates/Claude Guide#1.md?v2`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "HEAD/templates/Claude Guide") {
		t.Errorf("body contains an unescaped link target\n%s", body)
	}
}

// SYNCPR-006: a PR opened under an earlier pr-title-prefix is still stale
// once a newer version supersedes it; the head branch identifies it.
func TestSYNCPR006_CloseStalePRs_AcrossPrefixChange(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	prSvc.prs["testorg/repo-a"] = []*gh.PullRequest{
		{
			Number: 20,
			Title:  "ci(steerspec): update claude-md to 0.9.0", // opened under the old prefix
			State:  "open",
			Head:   "steerspec-sync/claude-md/0.9.0",
			Labels: []string{"steerspec-sync"},
		},
		{
			Number: 21,
			Title:  "ci(steerspec): update claude-md-extra to 0.9.0",
			State:  "open",
			Head:   "steerspec-sync/claude-md-extra/0.9.0",
			Labels: []string{"steerspec-sync"},
		},
	}

	cfg := makeTestConfig()
	cfg.Sync.PRTitlePrefix = "chore(steerspec-sync)"
	syncer := New(cfg, makeTestClient(repoSvc, prSvc), "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prSvc.closed) != 1 || prSvc.closed[0] != 20 {
		t.Errorf("SYNCPR-006: expected only PR #20 closed, got %v", prSvc.closed)
	}
}

// SYNCPR-006: template IDs may contain "/", so "guide" must not close the
// open PR of template "guide/team" even though its branch starts with
// "steerspec-sync/guide/".
func TestSYNCPR006_CloseStalePRs_SlashInTemplateID(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()

	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	prSvc.prs["testorg/repo-a"] = []*gh.PullRequest{
		{
			Number: 30,
			Title:  "ci(steerspec): update guide to 0.9.0",
			State:  "open",
			Head:   "steerspec-sync/guide/0.9.0",
			Labels: []string{"steerspec-sync"},
		},
		{
			Number: 31,
			Title:  "ci(steerspec): update guide/team to 0.9.0",
			State:  "open",
			Head:   "steerspec-sync/guide/team/0.9.0",
			Labels: []string{"steerspec-sync"},
		},
	}

	cfg := makeTestConfig()
	cfg.Templates[0].ID = "guide"
	syncer := New(cfg, makeTestClient(repoSvc, prSvc), "testorg", "central")
	if _, err := syncer.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prSvc.closed) != 1 || prSvc.closed[0] != 30 {
		t.Errorf("SYNCPR-006: expected only PR #30 closed, got %v", prSvc.closed)
	}
}

func TestIsSyncBranchForTemplate(t *testing.T) {
	cases := []struct {
		head, id string
		want     bool
	}{
		{"steerspec-sync/guide/1.0.0", "guide", true},
		{"steerspec-sync/guide/team/1.0.0", "guide", false},
		{"steerspec-sync/guide/team/1.0.0", "guide/team", true},
		{"steerspec-sync/guide-extra/1.0.0", "guide", false},
		{"steerspec-sync/guide/", "guide", false},
		{"feature/guide/1.0.0", "guide", false},
	}
	for _, c := range cases {
		if got := isSyncBranchForTemplate(c.head, c.id); got != c.want {
			t.Errorf("isSyncBranchForTemplate(%q, %q) = %v, want %v", c.head, c.id, got, c.want)
		}
	}
}
