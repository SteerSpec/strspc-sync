package conflict

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

// --- Mock implementations ---

type mockRepoService struct {
	files map[string][]byte // key: "owner/repo/path"
}

func (m *mockRepoService) GetFileContent(_ context.Context, owner, repo, path, _ string) ([]byte, string, error) {
	key := owner + "/" + repo + "/" + path
	content, ok := m.files[key]
	if !ok {
		return nil, "", fmt.Errorf("file not found: %s", key)
	}
	return content, "sha-fake", nil
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
	created []*gh.IssueCreate
	updated []issueUpdateCall
}

type issueUpdateCall struct {
	owner, repo string
	number      int
	update      *gh.IssueUpdate
}

func (m *mockIssueService) List(_ context.Context, _, _ string, _ *gh.IssueListOptions) ([]*gh.Issue, error) {
	return m.issues, nil
}

func (m *mockIssueService) Create(_ context.Context, _, _ string, issue *gh.IssueCreate) (*gh.Issue, error) {
	m.created = append(m.created, issue)
	return &gh.Issue{Number: len(m.created), Title: issue.Title}, nil
}

func (m *mockIssueService) Update(_ context.Context, owner, repo string, number int, issue *gh.IssueUpdate) error {
	m.updated = append(m.updated, issueUpdateCall{owner, repo, number, issue})
	return nil
}

func (m *mockIssueService) Close(_ context.Context, _, _ string, _ int) error {
	return nil
}

type mockPRService struct{}

func (m *mockPRService) List(_ context.Context, _, _ string, _ *gh.PullRequestListOptions) ([]*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Create(_ context.Context, _, _ string, _ *gh.PullRequestCreate) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Update(_ context.Context, _, _ string, _ int, _ *gh.PullRequestUpdate) error {
	return nil
}
func (m *mockPRService) Close(_ context.Context, _, _ string, _ int) error { return nil }
func (m *mockPRService) CreateBranch(_ context.Context, _, _, _, _ string) error {
	return nil
}

func newTestClient(repos *mockRepoService, issues *mockIssueService) *gh.Client {
	return &gh.Client{
		Repos:        repos,
		Issues:       issues,
		PullRequests: &mockPRService{},
	}
}

func testConfig() *config.SyncConfig {
	return &config.SyncConfig{
		Version: "1",
		Templates: []config.TemplateConfig{
			{ID: "claude-md", Type: "claude-md", Source: "templates/CLAUDE.md", Destination: "CLAUDE.md", Strategy: "marker"},
			{ID: "skill-test", Type: "skill", Source: "templates/skills/test.md", Destination: ".claude/skills/test.md", Strategy: "full-replace"},
		},
		Conflicts: config.ConflictBehavior{
			Tiers:      []int{1, 2},
			IssueLabel: "steerspec-conflict",
		},
	}
}

// --- Tests ---

func TestTier1_VersionDrift(t *testing.T) {
	originalContent := []byte("# Original content")
	driftedContent := []byte("# Modified content")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md": driftedContent,
		},
	}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version:   "1.0.0",
		Hash:      hash.HashBytes(originalContent),
		Timestamp: time.Now(),
	})

	scanner := New(cfg, client, "org", "central")
	entries, err := scanner.runTier1(context.Background(), ds, []string{"org/repo1"})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range entries {
		if e.Type == TypeVersionDrift && e.Repo == "org/repo1" {
			found = true
			if e.Severity != SeverityCritical {
				t.Errorf("expected critical severity, got %s", e.Severity)
			}
		}
	}
	if !found {
		t.Error("expected version drift entry, got none")
	}
}

func TestTier1_CrossReferenceBroken(t *testing.T) {
	claudeContent := []byte("# Project\nSee agents/helper.md for agent docs.\nAlso check skills/deploy.md.")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md": claudeContent,
			// .claude/agents/helper.md does NOT exist
			// .claude/skills/deploy.md does NOT exist
		},
	}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	scanner := New(cfg, client, "org", "central")
	entries, err := scanner.runTier1(context.Background(), ds, []string{"org/repo1"})
	if err != nil {
		t.Fatal(err)
	}

	brokenCount := 0
	for _, e := range entries {
		if e.Type == TypeCrossReferenceBroken {
			brokenCount++
		}
	}
	if brokenCount != 2 {
		t.Errorf("expected 2 broken cross-references, got %d", brokenCount)
	}
}

func TestTier1_UnmanagedFile(t *testing.T) {
	repos := &mockRepoService{
		files: map[string][]byte{},
	}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	// Target has NO deployment state entries at all
	ds := state.NewDeploymentState()
	scanner := New(cfg, client, "org", "central")
	entries, err := scanner.runTier1(context.Background(), ds, []string{"org/repo1"})
	if err != nil {
		t.Fatal(err)
	}

	unmanagedCount := 0
	for _, e := range entries {
		if e.Type == TypeUnmanagedFile {
			unmanagedCount++
		}
	}
	// Should flag CLAUDE.md and the two template destinations as unmanaged
	if unmanagedCount == 0 {
		t.Error("expected unmanaged file entries, got none")
	}
}

func TestTier2_DuplicateSkill(t *testing.T) {
	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/.claude/skills/test.md": []byte("# Skill v1"),
			"org/repo2/.claude/skills/test.md": []byte("# Skill v2 - different"),
		},
	}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	scanner := New(cfg, client, "org", "central")
	entries, err := scanner.runTier2(context.Background(), []string{"org/repo1", "org/repo2"})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range entries {
		if e.Type == TypeDuplicateSkill {
			found = true
			if e.Severity != SeverityWarning {
				t.Errorf("expected warning severity, got %s", e.Severity)
			}
		}
	}
	if !found {
		t.Error("expected duplicate skill entry, got none")
	}
}

func TestTier3_NotImplemented(t *testing.T) {
	repos := &mockRepoService{files: map[string][]byte{}}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	scanner := New(cfg, client, "org", "central")
	_, err := scanner.runTier3(context.Background(), []string{"org/repo1"})
	if err == nil {
		t.Fatal("expected error for tier 3, got nil")
	}
}

func TestReportSeveritySummary(t *testing.T) {
	report := &ConflictReport{}
	report.AddEntry(ConflictEntry{Severity: SeverityCritical, Type: TypeVersionDrift})
	report.AddEntry(ConflictEntry{Severity: SeverityCritical, Type: TypeCrossReferenceBroken})
	report.AddEntry(ConflictEntry{Severity: SeverityWarning, Type: TypeDuplicateSkill})
	report.AddEntry(ConflictEntry{Severity: SeverityInfo, Type: TypeUnmanagedFile})

	report.ComputeSummary()

	if report.SeveritySummary.Critical != 2 {
		t.Errorf("expected 2 critical, got %d", report.SeveritySummary.Critical)
	}
	if report.SeveritySummary.Warning != 1 {
		t.Errorf("expected 1 warning, got %d", report.SeveritySummary.Warning)
	}
	if report.SeveritySummary.Info != 1 {
		t.Errorf("expected 1 info, got %d", report.SeveritySummary.Info)
	}
}

func TestFullScan_Tiers1And2(t *testing.T) {
	originalContent := []byte("# Original")
	driftedContent := []byte("# Drifted")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md":              driftedContent,
			"org/repo1/.claude/skills/test.md": []byte("# Skill v1"),
			"org/repo2/.claude/skills/test.md": []byte("# Skill v2"),
		},
	}
	issues := &mockIssueService{}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version:   "1.0.0",
		Hash:      hash.HashBytes(originalContent),
		Timestamp: time.Now(),
	})

	scanner := New(cfg, client, "org", "central")
	report, err := scanner.Run(context.Background(), ds, []string{"org/repo1", "org/repo2"}, Options{
		Tiers: []int{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.ID == "" {
		t.Error("expected report to have an ID")
	}
	if report.Timestamp.IsZero() {
		t.Error("expected report to have a timestamp")
	}
	if len(report.Entries) == 0 {
		t.Error("expected conflict entries, got none")
	}

	// Should have at least a version drift and a duplicate skill
	hasVersionDrift := false
	hasDuplicateSkill := false
	for _, e := range report.Entries {
		if e.Type == TypeVersionDrift {
			hasVersionDrift = true
		}
		if e.Type == TypeDuplicateSkill {
			hasDuplicateSkill = true
		}
	}
	if !hasVersionDrift {
		t.Error("expected version drift entry in combined report")
	}
	if !hasDuplicateSkill {
		t.Error("expected duplicate skill entry in combined report")
	}

	// Issues should have been created for critical/warning entries
	if len(issues.created) == 0 {
		t.Error("expected GitHub issues to be created for conflicts")
	}
}
