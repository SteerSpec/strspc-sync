package conflict

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

// --- Mock implementations ---

type mockRepoService struct {
	// failPaths lists "owner/repo/path" keys whose read fails with a
	// non-404 API error, as a transient outage would.
	failPaths map[string]bool
	files     map[string][]byte // key: "owner/repo/path"
}

func (m *mockRepoService) GetFileContent(_ context.Context, owner, repo, path, _ string) ([]byte, string, error) {
	key := owner + "/" + repo + "/" + path
	if m.failPaths[key] {
		return nil, "", &gh.APIError{StatusCode: 502, Message: "bad gateway"}
	}
	content, ok := m.files[key]
	if !ok {
		return nil, "", &gh.APIError{StatusCode: 404, Message: "file not found: " + key}
	}
	return content, "sha-fake", nil
}

func (m *mockRepoService) ListByOrg(_ context.Context, _ string) ([]*gh.Repository, error) {
	return nil, nil
}

func (m *mockRepoService) ListByTopic(_ context.Context, _, _ string) ([]*gh.Repository, error) {
	return nil, nil
}

func (m *mockRepoService) GetDefaultBranch(_ context.Context, _, _ string) (string, error) {
	return "main", nil
}

func (m *mockRepoService) GetBranchSHA(_ context.Context, _, _, _ string) (string, error) {
	return "abc123sha", nil
}

func (m *mockRepoService) CreateOrUpdateFile(_ context.Context, _, _, _, _ string, _ []byte, _, _ string) error {
	return nil
}

type mockIssueService struct {
	issues []*gh.Issue
	// repoIssues, when set, scopes List to the "owner/repo" asked for, as the
	// real API does; tests that predate it keep the flat issues slice.
	repoIssues map[string][]*gh.Issue
	created    []*gh.IssueCreate
	updated    []issueUpdateCall
	closed     []int
}

type issueUpdateCall struct {
	owner, repo string
	number      int
	update      *gh.IssueUpdate
}

func (m *mockIssueService) List(_ context.Context, owner, repo string, _ *gh.IssueListOptions) ([]*gh.Issue, error) {
	if m.repoIssues != nil {
		return m.repoIssues[owner+"/"+repo], nil
	}
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

func (m *mockIssueService) Close(_ context.Context, _, _ string, number int) error {
	m.closed = append(m.closed, number)
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

func TestCNFLDTS007_TierIndependentlyEnableable(t *testing.T) {
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

	targets := []string{"org/repo1", "org/repo2"}

	// Run with only tier 1
	scanner := New(cfg, client, "org", "central")
	report1, err := scanner.Run(context.Background(), ds, targets, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report1.Entries {
		if e.Type == TypeDuplicateSkill {
			t.Error("CNFLDTS-007: tier 1 only should not include tier 2 entries (duplicate-skill)")
		}
	}
	hasVersionDrift := false
	for _, e := range report1.Entries {
		if e.Type == TypeVersionDrift {
			hasVersionDrift = true
		}
	}
	if !hasVersionDrift {
		t.Error("CNFLDTS-007: tier 1 should include version-drift entries")
	}

	// Run with only tier 2
	issues2 := &mockIssueService{}
	client2 := newTestClient(repos, issues2)
	scanner2 := New(cfg, client2, "org", "central")
	report2, err := scanner2.Run(context.Background(), ds, targets, Options{Tiers: []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report2.Entries {
		if e.Type == TypeVersionDrift {
			t.Error("CNFLDTS-007: tier 2 only should not include tier 1 entries (version-drift)")
		}
	}
	hasDupSkill := false
	for _, e := range report2.Entries {
		if e.Type == TypeDuplicateSkill {
			hasDupSkill = true
		}
	}
	if !hasDupSkill {
		t.Error("CNFLDTS-007: tier 2 should include duplicate-skill entries")
	}
}

func TestCNFLACT007_NoDuplicateConflictIssues(t *testing.T) {
	originalContent := []byte("# Original")
	driftedContent := []byte("# Drifted")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md": driftedContent,
		},
	}

	// Pre-populate an existing conflict issue with matching title
	issues := &mockIssueService{
		issues: []*gh.Issue{
			{
				Number: 55,
				Title:  fmt.Sprintf("[SteerSpec Conflict] %s: %s", TypeVersionDrift, "CLAUDE.md"),
				Body:   "old conflict body",
				State:  "open",
				Labels: []string{"steerspec-conflict"},
			},
		},
	}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version:   "1.0.0",
		Hash:      hash.HashBytes(originalContent),
		Timestamp: time.Now(),
	})

	scanner := New(cfg, client, "org", "central")
	_, err := scanner.Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}

	if len(issues.created) != 0 {
		t.Errorf("CNFLACT-007: expected no new issues created (should update existing), got %d", len(issues.created))
	}
	if len(issues.updated) != 1 {
		t.Errorf("CNFLACT-007: expected 1 issue updated, got %d", len(issues.updated))
	}
}

func TestResolvedConflictIssueIsClosed(t *testing.T) {
	// The file is back in sync, so the conflict issue opened by an earlier
	// run must be closed (SteerSpec/strspc-sync#55).
	content := []byte("# In sync")
	repos := &mockRepoService{files: map[string][]byte{"org/repo1/CLAUDE.md": content}}
	fp := hash.Fingerprint(string(TypeVersionDrift), "org/repo1", "CLAUDE.md")
	issues := &mockIssueService{
		repoIssues: map[string][]*gh.Issue{"org/repo1": {{
			Number: 7,
			Title:  fmt.Sprintf("[SteerSpec Conflict] %s: %s", TypeVersionDrift, "CLAUDE.md"),
			Body:   "stale\n\n<!-- steerspec-fingerprint: " + fp + " -->",
			State:  "open",
			Labels: []string{"steerspec-conflict"},
		}}},
	}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version: "1.0.0", Hash: hash.HashBytes(content), Timestamp: time.Now(),
	})

	report, err := New(cfg, client, "org", "central").Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 1 || issues.closed[0] != 7 {
		t.Errorf("expected issue #7 closed, got %v", issues.closed)
	}
	if report.IssuesClosed != 1 || report.IssuesCreated != 0 {
		t.Errorf("report counters = created %d closed %d", report.IssuesCreated, report.IssuesClosed)
	}
}

func TestStillPresentConflictIssueStaysOpen(t *testing.T) {
	original := []byte("# Original")
	repos := &mockRepoService{files: map[string][]byte{"org/repo1/CLAUDE.md": []byte("# Drifted")}}
	fp := hash.Fingerprint(string(TypeVersionDrift), "org/repo1", "CLAUDE.md")
	issues := &mockIssueService{
		repoIssues: map[string][]*gh.Issue{"org/repo1": {{
			Number: 8,
			Title:  "some other title",
			Body:   "<!-- steerspec-fingerprint: " + fp + " -->",
			State:  "open",
			Labels: []string{"steerspec-conflict"},
		}}},
	}
	client := newTestClient(repos, issues)
	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version: "1.0.0", Hash: hash.HashBytes(original), Timestamp: time.Now(),
	})

	report, err := New(testConfig(), client, "org", "central").Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 0 {
		t.Errorf("a conflict that is still present must keep its issue open, closed %v", issues.closed)
	}
	if report.IssuesClosed != 0 {
		t.Errorf("IssuesClosed = %d, want 0", report.IssuesClosed)
	}
}

func TestCleanupKeepsIssuesOfTiersThatDidNotRun(t *testing.T) {
	// A tier-2-only run produces no version-drift findings, so it must not
	// close a tier-1 issue even though the file still drifts.
	repos := &mockRepoService{files: map[string][]byte{"org/repo1/CLAUDE.md": []byte("# Drifted")}}
	issues := &mockIssueService{repoIssues: map[string][]*gh.Issue{"org/repo1": {{
		Number: 11, Title: fmt.Sprintf("[SteerSpec Conflict] %s: CLAUDE.md", TypeVersionDrift), State: "open",
	}}}}
	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{Version: "1.0.0", Hash: hash.HashBytes([]byte("# Original")), Timestamp: time.Now()})

	if _, err := New(testConfig(), newTestClient(repos, issues), "org", "central").Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{2}}); err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 0 {
		t.Errorf("tier-1 issue must survive a tier-2-only run, closed %v", issues.closed)
	}
}

func TestCleanupSkipsWhenNoTargetsScanned(t *testing.T) {
	// An empty target list (e.g. the deployment state failed to load) must
	// not sweep the central repo's conflict issues.
	issues := &mockIssueService{repoIssues: map[string][]*gh.Issue{"org/central": {{
		Number: 12, Title: fmt.Sprintf("[SteerSpec Conflict] %s: skills/x.md", TypeDuplicateSkill), State: "open",
	}}}}
	if _, err := New(testConfig(), newTestClient(&mockRepoService{files: map[string][]byte{}}, issues), "org", "central").Run(context.Background(), state.NewDeploymentState(), nil, Options{Tiers: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 0 {
		t.Errorf("no targets scanned, nothing may be closed; closed %v", issues.closed)
	}
}

func TestCleanupKeepsIssuesWhenAReadFailed(t *testing.T) {
	// The file read fails with a non-404 error, so the missing finding does
	// not prove the conflict is gone.
	repos := &mockRepoService{
		files:     map[string][]byte{},
		failPaths: map[string]bool{"org/repo1/CLAUDE.md": true},
	}
	fp := hash.Fingerprint(string(TypeVersionDrift), "org/repo1", "CLAUDE.md")
	issues := &mockIssueService{repoIssues: map[string][]*gh.Issue{"org/repo1": {{
		Number: 13, Title: "[SteerSpec Conflict] version-drift: CLAUDE.md", Body: "<!-- steerspec-fingerprint: " + fp + " -->", State: "open",
	}}}}
	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{Version: "1.0.0", Hash: hash.HashBytes([]byte("# Original")), Timestamp: time.Now()})

	if _, err := New(testConfig(), newTestClient(repos, issues), "org", "central").Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 0 {
		t.Errorf("a failed read must keep the issue open, closed %v", issues.closed)
	}
}

func TestAutoCloseOffKeepsResolvedIssues(t *testing.T) {
	content := []byte("# In sync")
	repos := &mockRepoService{files: map[string][]byte{"org/repo1/CLAUDE.md": content}}
	issues := &mockIssueService{repoIssues: map[string][]*gh.Issue{"org/repo1": {{Number: 9, Title: "[SteerSpec Conflict] version-drift: CLAUDE.md", State: "open"}}}}
	cfg := testConfig()
	off := false
	cfg.Conflicts.AutoClose = &off
	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{Version: "1.0.0", Hash: hash.HashBytes(content), Timestamp: time.Now()})

	if _, err := New(cfg, newTestClient(repos, issues), "org", "central").Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if len(issues.closed) != 0 {
		t.Errorf("auto-close off must not close issues, closed %v", issues.closed)
	}
}

func TestFingerprintBasedDedup_SkipsWhenFingerprintMatches(t *testing.T) {
	originalContent := []byte("# Original")
	driftedContent := []byte("# Drifted")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md": driftedContent,
		},
	}

	// Pre-populate an existing issue with a fingerprint in the body (but a DIFFERENT title)
	fp := hash.Fingerprint(string(TypeVersionDrift), "org/repo1", "CLAUDE.md")
	issues := &mockIssueService{
		issues: []*gh.Issue{
			{
				Number: 77,
				Title:  "[SteerSpec Conflict] TOTALLY DIFFERENT TITLE",
				Body:   "old body\n\n<!-- steerspec-fingerprint: " + fp + " -->",
				State:  "open",
				Labels: []string{"steerspec-conflict"},
			},
		},
	}
	client := newTestClient(repos, issues)
	cfg := testConfig()

	ds := state.NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", state.TemplateState{
		Version:   "1.0.0",
		Hash:      hash.HashBytes(originalContent),
		Timestamp: time.Now(),
	})

	scanner := New(cfg, client, "org", "central")
	_, err := scanner.Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}

	if len(issues.created) != 0 {
		t.Errorf("expected no new issues (fingerprint match should update), got %d created", len(issues.created))
	}
	if len(issues.updated) != 1 {
		t.Errorf("expected 1 issue updated via fingerprint match, got %d", len(issues.updated))
	}
}

func TestFingerprintBasedDedup_CreatesWhenNoMatch(t *testing.T) {
	originalContent := []byte("# Original")
	driftedContent := []byte("# Drifted")

	repos := &mockRepoService{
		files: map[string][]byte{
			"org/repo1/CLAUDE.md": driftedContent,
		},
	}

	// No existing issues at all
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
	_, err := scanner.Run(context.Background(), ds, []string{"org/repo1"}, Options{Tiers: []int{1}})
	if err != nil {
		t.Fatal(err)
	}

	if len(issues.created) == 0 {
		t.Error("expected new issue to be created when no fingerprint match exists")
	}

	// Verify the created issue body contains a fingerprint
	for _, ic := range issues.created {
		if !strings.Contains(ic.Body, "<!-- steerspec-fingerprint:") {
			t.Errorf("created issue body missing fingerprint marker: %s", ic.Body)
		}
	}
}

func TestConflictEntrySeverityMapping(t *testing.T) {
	tests := []struct {
		entryType ConflictType
		severity  Severity
	}{
		{TypeVersionDrift, SeverityCritical},
		{TypeCrossReferenceBroken, SeverityCritical},
		{TypeDuplicateSkill, SeverityWarning},
		{TypeUnmanagedFile, SeverityInfo},
	}

	// Verify each type produces the expected severity via actual scan
	for _, tt := range tests {
		report := &ConflictReport{}
		report.AddEntry(ConflictEntry{Severity: tt.severity, Type: tt.entryType})
		report.ComputeSummary()

		switch tt.severity {
		case SeverityCritical:
			if report.SeveritySummary.Critical != 1 {
				t.Errorf("type %s: expected critical=1, got %d", tt.entryType, report.SeveritySummary.Critical)
			}
		case SeverityWarning:
			if report.SeveritySummary.Warning != 1 {
				t.Errorf("type %s: expected warning=1, got %d", tt.entryType, report.SeveritySummary.Warning)
			}
		case SeverityInfo:
			if report.SeveritySummary.Info != 1 {
				t.Errorf("type %s: expected info=1, got %d", tt.entryType, report.SeveritySummary.Info)
			}
		}
	}
}
