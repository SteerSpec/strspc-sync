package sync

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

// bundlePRService is mockPRService with what repeated runs need: Close marks the PR closed, and CreateBranch reports
// an existing branch with GitHub's 422, as the real API does.
type bundlePRService struct {
	*mockPRService
	existing map[string]bool
}

func newBundlePRService() *bundlePRService {
	return &bundlePRService{mockPRService: newMockPRService(), existing: map[string]bool{}}
}

func (m *bundlePRService) Close(ctx context.Context, owner, repo string, number int) error {
	if err := m.mockPRService.Close(ctx, owner, repo, number); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pr := range m.prs[owner+"/"+repo] {
		if pr.Number == number {
			pr.State = "closed"
		}
	}
	return nil
}

func (m *bundlePRService) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	key := owner + "/" + repo + ":" + branch
	m.mu.Lock()
	exists := m.existing[key]
	m.existing[key] = true
	m.mu.Unlock()
	if exists {
		return &gh.APIError{StatusCode: 422, Message: "Reference already exists"}
	}
	return m.mockPRService.CreateBranch(ctx, owner, repo, branch, baseSHA)
}

// bundleConfig has three templates: two marker templates sharing CLAUDE.md, and a settings file.
func bundleConfig(grouping string) *config.SyncConfig {
	cfg := makeTestConfig()
	cfg.Sync.PRGrouping = grouping
	cfg.Templates = []config.TemplateConfig{
		{ID: "org", Version: "1.0.0", Source: "templates/org.md", Destination: "CLAUDE.md", Strategy: "marker"},
		{ID: "team", Version: "1.0.0", Source: "templates/team.md", Destination: "CLAUDE.md", Strategy: "marker"},
		{
			ID: "settings", Version: "1.0.0", Source: "templates/settings.json", Destination: ".claude/settings.json",
			Strategy: "full-replace",
		},
	}
	return cfg
}

func setupBundleRepos(repoSvc *mockRepoService) {
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/org.md/"] = []byte("<!-- STEERSPEC:BEGIN:org -->\norg v1\n<!-- STEERSPEC:END:org -->\n")
	repoSvc.files["testorg/central/templates/team.md/"] = []byte("<!-- STEERSPEC:BEGIN:team -->\nteam v1\n<!-- STEERSPEC:END:team -->\n")
	repoSvc.files["testorg/central/templates/settings.json/"] = []byte(`{"v":1}`)
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = []byte("# Repo notes\n")
}

func runSync(t *testing.T, cfg *config.SyncConfig, repoSvc *mockRepoService, prSvc gh.PullRequestService,
	opts Options,
) *Result {
	t.Helper()
	if opts.Trigger == "" {
		opts.Trigger = TriggerManual
	}
	s := New(cfg, &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	result, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func savedState(t *testing.T, repoSvc *mockRepoService) *state.DeploymentState {
	t.Helper()
	data, ok := repoSvc.files["testorg/central/.steerspec/deployment-state.json/"]
	if !ok {
		t.Fatal("no deployment state was saved")
	}
	ds, err := state.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// SYNCPR-008: the hash input is the sorted entries joined by "\n", and the branch takes 8 lowercase hex characters.
func TestBundleBranch(t *testing.T) {
	// A single empty entry hashes the empty string; af1349b9... is BLAKE3's published test vector for it.
	if got := bundleBranch([]string{""}); got != "steerspec-sync/repo/af1349b9" {
		t.Errorf("bundleBranch([\"\"]) = %s, want steerspec-sync/repo/af1349b9", got)
	}
	a := bundleBranch([]string{"settings@1.0.0", "org@1.0.0"})
	if b := bundleBranch([]string{"org@1.0.0", "settings@1.0.0"}); a != b {
		t.Errorf("order changes the branch: %s vs %s", a, b)
	}
	want := "steerspec-sync/repo/" + strings.TrimPrefix(hash.HashString("org@1.0.0\nsettings@1.0.0"), "b3_")[:8]
	if a != want {
		t.Errorf("bundleBranch = %s, want %s", a, want)
	}
	if bundleBranch([]string{"org@1.0.1", "settings@1.0.0"}) == a {
		t.Error("a version change must change the branch")
	}
	if !isBundleBranch(a) {
		t.Errorf("isBundleBranch(%s) = false", a)
	}
}

func TestIsBundleBranch(t *testing.T) {
	for head, want := range map[string]bool{
		"steerspec-sync/repo/0123abcd":  true,
		"steerspec-sync/repo/0123ABCD":  false,
		"steerspec-sync/repo/0123abc":   false,
		"steerspec-sync/repo/0123abcde": false,
		"steerspec-sync/repo/1.0.0":     false, // template "repo", version 1.0.0
		"steerspec-sync/org/1.0.0":      false,
		"feature/repo/0123abcd":         false,
	} {
		if got := isBundleBranch(head); got != want {
			t.Errorf("isBundleBranch(%q) = %v, want %v", head, got, want)
		}
	}
	// A template named "repo" claims its own versions, not bundles.
	if !isSyncBranchForTemplate("steerspec-sync/repo/1.0.0", "repo") {
		t.Error("template repo must claim steerspec-sync/repo/1.0.0")
	}
	if isSyncBranchForTemplate("steerspec-sync/repo/0123abcd", "repo") {
		t.Error("template repo must not claim a bundle branch")
	}
}

// SYNCCFG-031, SYNCPR-011: one PR per repository, one commit with every changed file, one body section per template.
func TestBundleOpensOnePRWithOneCommit(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()

	result := runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{})
	if result.PRsCreated != 1 {
		t.Errorf("PRsCreated = %d, want 1", result.PRsCreated)
	}
	if len(prSvc.created) != 1 {
		t.Fatalf("PRs opened = %d, want 1", len(prSvc.created))
	}
	pr := prSvc.created[0]
	wantBranch := bundleBranch([]string{"org@1.0.0", "team@1.0.0", "settings@1.0.0"})
	if pr.Head != wantBranch {
		t.Errorf("head = %s, want %s", pr.Head, wantBranch)
	}
	if pr.Title != "chore(steerspec-sync): update 3 templates" {
		t.Errorf("title = %q", pr.Title)
	}
	for _, id := range []string{"`org`", "`team`", "`settings`"} {
		if !strings.Contains(pr.Body, "### "+id) {
			t.Errorf("body has no section for %s:\n%s", id, pr.Body)
		}
	}
	if n := strings.Count(pr.Body, "| Previous version |"); n != 3 {
		t.Errorf("body has %d version tables, want 3", n)
	}

	if len(repoSvc.commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(repoSvc.commits))
	}
	c := repoSvc.commits[0]
	if !slices.Equal(c.paths, []string{"CLAUDE.md", ".claude/settings.json"}) {
		t.Errorf("committed paths = %v, want CLAUDE.md and .claude/settings.json once each", c.paths)
	}
	if !strings.HasPrefix(c.message, "chore(steerspec-sync): update 3 templates\n\n- update org to 1.0.0") {
		t.Errorf("commit message = %q", c.message)
	}
	// Both marker templates rendered onto one CLAUDE.md, around the repository's own notes.
	claude := string(repoSvc.files["testorg/repo-a/CLAUDE.md/"+wantBranch])
	for _, want := range []string{"# Repo notes", "org v1", "team v1"} {
		if !strings.Contains(claude, want) {
			t.Errorf("CLAUDE.md on the bundle branch lacks %q:\n%s", want, claude)
		}
	}

	ds := savedState(t, repoSvc)
	for _, id := range []string{"org", "team", "settings"} {
		ts := ds.GetTemplateState("testorg/repo-a", id)
		if ts == nil || ts.PRNumber != 1 || ts.PRStatus != "open" {
			t.Errorf("state of %s = %+v, want PR #1 open", id, ts)
		}
	}
	for _, rr := range result.RepoResults {
		if rr.Status != "created" || rr.PRNumber != 1 {
			t.Errorf("result %s = %s #%d, want created #1", rr.TemplateID, rr.Status, rr.PRNumber)
		}
	}
}

// A bundle of one template takes that template's title and commit message.
func TestBundleOfOneTemplateUsesItsTitle(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)
	cfg.Templates = cfg.Templates[2:]

	runSync(t, cfg, repoSvc, prSvc, Options{})
	if len(prSvc.created) != 1 || prSvc.created[0].Title != "chore(steerspec-sync): update settings to 1.0.0" {
		t.Fatalf("created = %+v, want one PR titled for settings", prSvc.created)
	}
	if got := repoSvc.commits[0].message; got != "chore(steerspec-sync): update settings to 1.0.0" {
		t.Errorf("commit message = %q", got)
	}
}

// A second run with nothing new opens nothing and adds no commit.
func TestBundleRerunIsSkipped(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)

	runSync(t, cfg, repoSvc, prSvc, Options{})
	result := runSync(t, cfg, repoSvc, prSvc, Options{})
	if result.PRsCreated != 0 || result.PRsUpdated != 0 || len(prSvc.created) != 1 || len(repoSvc.commits) != 1 {
		t.Errorf("rerun: created %d updated %d, %d PRs, %d commits; want nothing new",
			result.PRsCreated, result.PRsUpdated, len(prSvc.created), len(repoSvc.commits))
	}
	for _, rr := range result.RepoResults {
		if rr.Status != "skipped" || rr.PRNumber != 1 {
			t.Errorf("rerun result %s = %s #%d, want skipped #1", rr.TemplateID, rr.Status, rr.PRNumber)
		}
	}
}

// SYNCPR-009: new content at the same versions adds one commit to the open bundle, with only the changed file.
func TestBundleSameVersionsUpdatesOpenPR(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)

	runSync(t, cfg, repoSvc, prSvc, Options{})
	repoSvc.files["testorg/central/templates/settings.json/"] = []byte(`{"v":2}`)
	result := runSync(t, cfg, repoSvc, prSvc, Options{})

	if len(prSvc.created) != 1 || result.PRsUpdated != 1 {
		t.Fatalf("PRs opened = %d, updated = %d; want the open bundle updated", len(prSvc.created), result.PRsUpdated)
	}
	if len(repoSvc.commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(repoSvc.commits))
	}
	if got := repoSvc.commits[1].paths; !slices.Equal(got, []string{".claude/settings.json"}) {
		t.Errorf("second commit paths = %v, want only .claude/settings.json", got)
	}
	if len(prSvc.closed) != 0 {
		t.Errorf("closed = %v, want none", prSvc.closed)
	}
}

// SYNCPR-010: a version change opens a new bundle, which still carries the unchanged templates, and closes the old.
func TestBundleVersionChangeReplacesOldBundle(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)

	runSync(t, cfg, repoSvc, prSvc, Options{})
	cfg.Templates[2].Version = "1.1.0"
	repoSvc.files["testorg/central/templates/settings.json/"] = []byte(`{"v":2}`)
	runSync(t, cfg, repoSvc, prSvc, Options{})

	if len(prSvc.created) != 2 {
		t.Fatalf("PRs opened = %d, want 2", len(prSvc.created))
	}
	if !slices.Equal(prSvc.closed, []int{1}) {
		t.Errorf("closed = %v, want [1]", prSvc.closed)
	}
	second := prSvc.created[1]
	if second.Head != bundleBranch([]string{"org@1.0.0", "team@1.0.0", "settings@1.1.0"}) {
		t.Errorf("new bundle head = %s", second.Head)
	}
	// The templates that did not change are carried again, or closing #1 would drop them.
	if got := repoSvc.commits[1].paths; !slices.Equal(got, []string{"CLAUDE.md", ".claude/settings.json"}) {
		t.Errorf("new bundle paths = %v, want both files", got)
	}
	ds := savedState(t, repoSvc)
	for _, id := range []string{"org", "team", "settings"} {
		if ts := ds.GetTemplateState("testorg/repo-a", id); ts == nil || ts.PRNumber != 2 || ts.PRStatus != "open" {
			t.Errorf("state of %s = %+v, want PR #2 open", id, ts)
		}
	}
}

// SYNCPR-011 partial failure: a template that cannot render is reported, the others ship, and its state is kept.
func TestBundleShipsTheRestWhenOneFails(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	repoSvc.getFileErr = map[string]error{"testorg/central/templates/settings.json": fmt.Errorf("boom")}
	prSvc := newBundlePRService()

	result := runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{})
	if len(prSvc.created) != 1 {
		t.Fatalf("PRs opened = %d, want 1", len(prSvc.created))
	}
	if got := prSvc.created[0].Head; got != bundleBranch([]string{"org@1.0.0", "team@1.0.0"}) {
		t.Errorf("head = %s, want the bundle of org and team", got)
	}
	if result.Errors != 1 || result.Status != StatusCompleted {
		t.Errorf("errors = %d status = %s, want 1 error, completed", result.Errors, result.Status)
	}
	if ts := savedState(t, repoSvc).GetTemplateState("testorg/repo-a", "settings"); ts != nil {
		t.Errorf("settings state = %+v, want none", ts)
	}
}

// Switching to repo mode closes the per-template PRs of the templates a bundle carries (SYNCPR-010).
func TestBundleClosesPerTemplatePRs(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()

	runSync(t, bundleConfig(config.PRGroupingTemplate), repoSvc, prSvc, Options{})
	if len(prSvc.created) != 3 {
		t.Fatalf("template mode opened %d PRs, want 3", len(prSvc.created))
	}
	runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{})
	if len(prSvc.created) != 4 || !isBundleBranch(prSvc.created[3].Head) {
		t.Fatalf("repo mode: created = %d, want one bundle more", len(prSvc.created))
	}
	closed := slices.Clone(prSvc.closed)
	slices.Sort(closed)
	if !slices.Equal(closed, []int{1, 2, 3}) {
		t.Errorf("closed = %v, want [1 2 3]", closed)
	}
}

// Switching back to template mode: a per-template PR closes the bundle, and the bundle's other templates are marked
// superseded, so the next run proposes them instead of reporting a PR closed without merging.
func TestTemplateModeSupersedesBundle(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)
	runSync(t, cfg, repoSvc, prSvc, Options{})

	// Only settings changes; template mode opens its own PR and closes bundle #1.
	cfg = bundleConfig(config.PRGroupingTemplate)
	cfg.Templates[2].Version = "1.1.0"
	repoSvc.files["testorg/central/templates/settings.json/"] = []byte(`{"v":2}`)
	runSync(t, cfg, repoSvc, prSvc, Options{TemplateFilter: "settings"})
	if !slices.Equal(prSvc.closed, []int{1}) {
		t.Fatalf("closed = %v, want bundle [1]", prSvc.closed)
	}
	ds := savedState(t, repoSvc)
	for _, id := range []string{"org", "team"} {
		if ts := ds.GetTemplateState("testorg/repo-a", id); ts == nil || ts.PRStatus != prSuperseded {
			t.Errorf("state of %s = %+v, want superseded", id, ts)
		}
	}

	result := runSync(t, cfg, repoSvc, prSvc, Options{})
	for _, rr := range result.RepoResults {
		if strings.Contains(rr.Error, "closed without merging") {
			t.Errorf("%s: %s", rr.TemplateID, rr.Error)
		}
	}
	if len(prSvc.created) != 4 {
		t.Errorf("PRs opened = %d, want org and team proposed again (4 in all)", len(prSvc.created))
	}
}

// A bundle PR closed by a person is left closed: the run reports it and waits for --force, as for one template.
func TestBundleClosedWithoutMergingNeedsForce(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)
	runSync(t, cfg, repoSvc, prSvc, Options{})
	prSvc.prs["testorg/repo-a"][0].State = "closed"

	result := runSync(t, cfg, repoSvc, prSvc, Options{})
	if len(prSvc.created) != 1 {
		t.Fatalf("PRs opened = %d, want none new", len(prSvc.created))
	}
	for _, rr := range result.RepoResults {
		if !strings.Contains(rr.Error, "closed without merging") {
			t.Errorf("%s: error %q, want closed without merging", rr.TemplateID, rr.Error)
		}
	}

	runSync(t, cfg, repoSvc, prSvc, Options{Force: true})
	if len(prSvc.created) != 2 {
		t.Errorf("forced run opened %d PRs in all, want 2", len(prSvc.created))
	}
}

// Dry run in repo mode writes nothing.
func TestBundleDryRun(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()

	result := runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{DryRun: true})
	if len(prSvc.created) != 0 || len(repoSvc.commits) != 0 || len(prSvc.branches) != 0 {
		t.Errorf("dry run wrote: %d PRs, %d commits, %d branches", len(prSvc.created), len(repoSvc.commits),
			len(prSvc.branches))
	}
	if len(result.RepoResults) != 3 {
		t.Errorf("results = %d, want 3", len(result.RepoResults))
	}
}

// --template-filter narrows the bundle to the templates it matches.
func TestBundleTemplateFilter(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()

	runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{TemplateFilter: "settings"})
	if len(prSvc.created) != 1 || prSvc.created[0].Head != bundleBranch([]string{"settings@1.0.0"}) {
		t.Fatalf("created = %+v, want one bundle of settings", prSvc.created)
	}
}

// A failed commit on a branch this run created deletes the branch and opens no PR.
func TestBundleCommitFailureDropsBranch(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	branch := bundleBranch([]string{"org@1.0.0", "team@1.0.0", "settings@1.0.0"})
	repoSvc.createFileErr = map[string]error{"testorg/repo-a/CLAUDE.md/" + branch: fmt.Errorf("rejected")}
	prSvc := newBundlePRService()

	result := runSync(t, bundleConfig(config.PRGroupingRepo), repoSvc, prSvc, Options{})
	if len(prSvc.created) != 0 {
		t.Errorf("PRs opened = %d, want 0", len(prSvc.created))
	}
	if !slices.Equal(prSvc.deleted, []string{branch}) {
		t.Errorf("deleted = %v, want [%s]", prSvc.deleted, branch)
	}
	if result.Errors != 3 || result.Status != StatusFailed {
		t.Errorf("errors = %d status = %s, want 3 errors, failed", result.Errors, result.Status)
	}
}

// SYNCOP-009: a dry run over a bundle that has nothing new looks the PR up but records nothing, not even in memory.
func TestBundleDryRunRecordsNoPRStatus(t *testing.T) {
	repoSvc := newMockRepoService()
	setupBundleRepos(repoSvc)
	prSvc := newBundlePRService()
	cfg := bundleConfig(config.PRGroupingRepo)
	runSync(t, cfg, repoSvc, prSvc, Options{})
	prSvc.prs["testorg/repo-a"][0].State = "closed"

	s := New(cfg, &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	result, err := s.Run(context.Background(), Options{Trigger: TriggerManual, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"org", "team", "settings"} {
		if ts := s.state.GetTemplateState("testorg/repo-a", id); ts == nil || ts.PRStatus != "open" {
			t.Errorf("dry run changed the record of %s: %+v", id, ts)
		}
	}
	// The run still reports what a real run would.
	for _, rr := range result.RepoResults {
		if !strings.Contains(rr.Error, "closed without merging") {
			t.Errorf("%s: error %q, want closed without merging", rr.TemplateID, rr.Error)
		}
	}
}
