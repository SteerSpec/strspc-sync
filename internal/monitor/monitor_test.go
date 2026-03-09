package monitor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

// --- mock services ---

type mockRepoService struct {
	files map[string][]byte // "owner/repo/path" -> content
}

func (m *mockRepoService) GetFileContent(_ context.Context, owner, repo, path, _ string) ([]byte, string, error) {
	key := owner + "/" + repo + "/" + path
	content, ok := m.files[key]
	if !ok {
		return nil, "", fmt.Errorf("file not found: %s", key)
	}
	return content, "sha", nil
}

func (m *mockRepoService) ListByOrg(_ context.Context, _ string) ([]*gh.Repository, error) {
	return nil, nil
}

func (m *mockRepoService) ListByTopic(_ context.Context, _ string) ([]*gh.Repository, error) {
	return nil, nil
}

func (m *mockRepoService) GetDefaultBranch(_ context.Context, _, _ string) (string, error) {
	return "main", nil
}

func (m *mockRepoService) CreateOrUpdateFile(_ context.Context, _, _, _, _ string, _ []byte, _, _ string) error {
	return nil
}

type mockIssueService struct {
	issues  []*gh.Issue
	created []*gh.Issue
	updated map[int]*gh.IssueUpdate
	closed  []int
	nextNum int
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		updated: make(map[int]*gh.IssueUpdate),
		nextNum: 100,
	}
}

func (m *mockIssueService) List(_ context.Context, _, _ string, opts *gh.IssueListOptions) ([]*gh.Issue, error) {
	var result []*gh.Issue
	for _, iss := range m.issues {
		if opts != nil && opts.State != "" && iss.State != opts.State {
			continue
		}
		if opts != nil && len(opts.Labels) > 0 {
			labelSet := make(map[string]bool)
			for _, l := range iss.Labels {
				labelSet[l] = true
			}
			match := true
			for _, want := range opts.Labels {
				if !labelSet[want] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, iss)
	}
	return result, nil
}

func (m *mockIssueService) Create(_ context.Context, _, _ string, ic *gh.IssueCreate) (*gh.Issue, error) {
	m.nextNum++
	iss := &gh.Issue{
		Number: m.nextNum,
		Title:  ic.Title,
		Body:   ic.Body,
		State:  "open",
		Labels: ic.Labels,
		URL:    fmt.Sprintf("https://github.com/test/central/issues/%d", m.nextNum),
	}
	m.issues = append(m.issues, iss)
	m.created = append(m.created, iss)
	return iss, nil
}

func (m *mockIssueService) Update(_ context.Context, _, _ string, number int, iu *gh.IssueUpdate) error {
	m.updated[number] = iu
	return nil
}

func (m *mockIssueService) Close(_ context.Context, _, _ string, number int) error {
	m.closed = append(m.closed, number)
	// Remove from issues list.
	for i, iss := range m.issues {
		if iss.Number == number {
			m.issues = append(m.issues[:i], m.issues[i+1:]...)
			break
		}
	}
	return nil
}

// --- helpers ---

func testConfig() *config.SyncConfig {
	t := true
	return &config.SyncConfig{
		Templates: []config.TemplateConfig{
			{ID: "claude-md", Destination: "CLAUDE.md"},
			{ID: "ci-config", Destination: ".github/workflows/ci.yml"},
		},
		Monitor: config.MonitorBehavior{
			IssueLabel: "steerspec-drift",
			AutoClose:  &t,
		},
	}
}

func makeClient(repos *mockRepoService, issues *mockIssueService) *gh.Client {
	return &gh.Client{
		Repos:  repos,
		Issues: issues,
	}
}

func makeState(repos map[string]map[string]state.TemplateState) *state.DeploymentState {
	return &state.DeploymentState{
		Repositories: repos,
		UpdatedAt:    time.Now(),
	}
}

// --- tests ---

func TestNoDrift(t *testing.T) {
	content := []byte("hello world")
	h := hash.HashBytes(content)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": content,
		},
	}
	issueSvc := newMockIssueService()
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: h, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ReposInSync != 1 {
		t.Errorf("expected 1 repo in sync, got %d", res.ReposInSync)
	}
	if res.ReposDrifted != 0 {
		t.Errorf("expected 0 repos drifted, got %d", res.ReposDrifted)
	}
	if res.IssuesCreated != 0 {
		t.Errorf("expected 0 issues created, got %d", res.IssuesCreated)
	}
	if len(issueSvc.created) != 0 {
		t.Errorf("expected no issues created in mock, got %d", len(issueSvc.created))
	}
}

func TestDriftDetected_NewIssue(t *testing.T) {
	expectedContent := []byte("expected content")
	actualContent := []byte("actual content (modified)")
	expectedHash := hash.HashBytes(expectedContent)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": actualContent,
		},
	}
	issueSvc := newMockIssueService()
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: expectedHash, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ReposDrifted != 1 {
		t.Errorf("expected 1 repo drifted, got %d", res.ReposDrifted)
	}
	if res.IssuesCreated != 1 {
		t.Errorf("expected 1 issue created, got %d", res.IssuesCreated)
	}
	if len(res.DriftEntries) != 1 {
		t.Fatalf("expected 1 drift entry, got %d", len(res.DriftEntries))
	}
	entry := res.DriftEntries[0]
	if entry.Repo != "org/app" {
		t.Errorf("expected repo org/app, got %s", entry.Repo)
	}
	if entry.TemplateID != "claude-md" {
		t.Errorf("expected template claude-md, got %s", entry.TemplateID)
	}
	if entry.IssueNumber == 0 {
		t.Error("expected issue number to be set")
	}

	// Verify issue title format.
	if len(issueSvc.created) != 1 {
		t.Fatalf("expected 1 issue in mock, got %d", len(issueSvc.created))
	}
	if !strings.Contains(issueSvc.created[0].Title, "[SteerSpec Drift]") {
		t.Errorf("issue title missing prefix: %s", issueSvc.created[0].Title)
	}
}

func TestDriftDetected_ExistingIssueUpdated(t *testing.T) {
	expectedContent := []byte("expected")
	actualContent := []byte("drifted")
	expectedHash := hash.HashBytes(expectedContent)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": actualContent,
		},
	}
	issueSvc := newMockIssueService()
	// Pre-populate an existing drift issue.
	issueSvc.issues = []*gh.Issue{
		{
			Number: 42,
			Title:  "[SteerSpec Drift] org/app: claude-md",
			Body:   "old body",
			State:  "open",
			Labels: []string{"steerspec-drift"},
			URL:    "https://github.com/test/central/issues/42",
		},
	}
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: expectedHash, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IssuesCreated != 0 {
		t.Errorf("expected 0 new issues (should update existing), got %d", res.IssuesCreated)
	}
	if _, ok := issueSvc.updated[42]; !ok {
		t.Error("expected existing issue #42 to be updated")
	}
	if len(res.DriftEntries) != 1 {
		t.Fatalf("expected 1 drift entry, got %d", len(res.DriftEntries))
	}
	if res.DriftEntries[0].IssueNumber != 42 {
		t.Errorf("expected issue number 42, got %d", res.DriftEntries[0].IssueNumber)
	}
}

func TestDriftResolved_IssueAutoClosed(t *testing.T) {
	content := []byte("matching content")
	h := hash.HashBytes(content)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": content,
		},
	}
	issueSvc := newMockIssueService()
	issueSvc.issues = []*gh.Issue{
		{
			Number: 50,
			Title:  "[SteerSpec Drift] org/app: claude-md",
			Body:   "drift body",
			State:  "open",
			Labels: []string{"steerspec-drift"},
			URL:    "https://github.com/test/central/issues/50",
		},
	}
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: h, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IssuesClosed != 1 {
		t.Errorf("expected 1 issue closed, got %d", res.IssuesClosed)
	}
	if len(issueSvc.closed) != 1 || issueSvc.closed[0] != 50 {
		t.Errorf("expected issue #50 closed, got %v", issueSvc.closed)
	}
	if res.ReposInSync != 1 {
		t.Errorf("expected 1 repo in sync, got %d", res.ReposInSync)
	}
}

func TestAutoCloseDisabled_IssueStaysOpen(t *testing.T) {
	content := []byte("matching content")
	h := hash.HashBytes(content)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": content,
		},
	}
	issueSvc := newMockIssueService()
	issueSvc.issues = []*gh.Issue{
		{
			Number: 60,
			Title:  "[SteerSpec Drift] org/app: claude-md",
			Body:   "drift body",
			State:  "open",
			Labels: []string{"steerspec-drift"},
			URL:    "https://github.com/test/central/issues/60",
		},
	}
	client := makeClient(repoSvc, issueSvc)

	cfg := testConfig()
	f := false
	cfg.Monitor.AutoClose = &f

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: h, Timestamp: time.Now()},
		},
	})

	mon := New(cfg, client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IssuesClosed != 0 {
		t.Errorf("expected 0 issues closed (auto-close disabled), got %d", res.IssuesClosed)
	}
	if len(issueSvc.closed) != 0 {
		t.Errorf("expected no issues closed in mock, got %v", issueSvc.closed)
	}
}

func TestTargetFilter(t *testing.T) {
	content := []byte("some content")
	driftedContent := []byte("drifted")
	h := hash.HashBytes(content)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app1/CLAUDE.md": content,
			"org/app2/CLAUDE.md": driftedContent,
		},
	}
	issueSvc := newMockIssueService()
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app1": {
			"claude-md": {Version: "1.0", Hash: h, Timestamp: time.Now()},
		},
		"org/app2": {
			"claude-md": {Version: "1.0", Hash: h, Timestamp: time.Now()},
		},
	})

	// Filter to only app1 (which is in sync), so app2 drift should be ignored.
	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{TargetFilter: "org/app1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ReposInSync != 1 {
		t.Errorf("expected 1 repo in sync, got %d", res.ReposInSync)
	}
	if res.ReposDrifted != 0 {
		t.Errorf("expected 0 repos drifted (filtered), got %d", res.ReposDrifted)
	}
	if res.IssuesCreated != 0 {
		t.Errorf("expected 0 issues created, got %d", res.IssuesCreated)
	}
}

func TestMNRACT005_HashBasedDriftDetection(t *testing.T) {
	// Scenario: deployed version string is the same, but file content (and thus hash) differs.
	// Proves drift detection is hash-based, not version-string-based.
	deployedContent := []byte("# Standards v1.0\nOriginal rules here")
	modifiedContent := []byte("# Standards v1.0\nModified rules with same version header")
	deployedHash := hash.HashBytes(deployedContent)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": modifiedContent,
		},
	}
	issueSvc := newMockIssueService()
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: deployedHash, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")
	res, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ReposDrifted != 1 {
		t.Errorf("MNRACT-005: expected 1 repo drifted (hash-based detection), got %d", res.ReposDrifted)
	}
	if res.IssuesCreated != 1 {
		t.Errorf("MNRACT-005: expected 1 drift issue created, got %d", res.IssuesCreated)
	}
}

func TestMNRACT008_NoDuplicateDriftIssues_TwoPassScenario(t *testing.T) {
	expectedContent := []byte("expected")
	actualContent := []byte("drifted")
	expectedHash := hash.HashBytes(expectedContent)

	repoSvc := &mockRepoService{
		files: map[string][]byte{
			"org/app/CLAUDE.md": actualContent,
		},
	}
	issueSvc := newMockIssueService()
	client := makeClient(repoSvc, issueSvc)

	ds := makeState(map[string]map[string]state.TemplateState{
		"org/app": {
			"claude-md": {Version: "1.0", Hash: expectedHash, Timestamp: time.Now()},
		},
	})

	mon := New(testConfig(), client, "test", "central")

	// First run: creates a drift issue
	res1, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}
	if res1.IssuesCreated != 1 {
		t.Fatalf("MNRACT-008: first run expected 1 issue created, got %d", res1.IssuesCreated)
	}

	// Second run with same drift: should update existing issue, not create new one
	res2, err := mon.Run(context.Background(), ds, Options{})
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if res2.IssuesCreated != 0 {
		t.Errorf("MNRACT-008: second run expected 0 new issues (should update existing), got %d", res2.IssuesCreated)
	}
	// Total issues created across both runs should be 1
	totalCreated := len(issueSvc.created)
	if totalCreated != 1 {
		t.Errorf("MNRACT-008: expected 1 total issue created across both runs, got %d", totalCreated)
	}
}
