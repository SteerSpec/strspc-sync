package sync

import (
	"context"
	"slices"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

const markerTemplate = "<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->\n"

func markerSyncConfig() *config.SyncConfig {
	cfg := makeTestConfig()
	cfg.Templates[0].Strategy = "marker"
	return cfg
}

func savedTemplateState(t *testing.T, repoSvc *mockRepoService, repo string) *state.TemplateState {
	t.Helper()
	ds, err := state.Load(repoSvc.files["testorg/central/.steerspec/deployment-state.json/"])
	if err != nil {
		t.Fatal(err)
	}
	ts := ds.GetTemplateState(repo, "claude-md")
	if ts == nil {
		t.Fatalf("no state recorded for %s", repo)
	}
	return ts
}

func wantSectionsHash(t *testing.T, content []byte) string {
	t.Helper()
	sum, ok := template.SectionsHash(content, []string{"org"})
	if !ok {
		t.Fatal("SectionsHash: no sections")
	}
	return sum
}

// GH #133: a marker-strategy PR records the hash of the managed sections next to the whole-file hash.
func TestMarkerSyncRecordsSectionsHash(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte(markerTemplate)
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = []byte("# Repo A\n\nLocal notes.\n")

	syncer := New(markerSyncConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PRsCreated != 1 {
		t.Fatalf("PRsCreated = %d, want 1: %+v", result.PRsCreated, result.RepoResults)
	}
	ts := savedTemplateState(t, repoSvc, "testorg/repo-a")
	if want := wantSectionsHash(t, []byte(markerTemplate)); ts.SectionsHash != want {
		t.Errorf("sections_hash = %q, want %q", ts.SectionsHash, want)
	}
	if !slices.Equal(ts.Sections, []string{"org"}) {
		t.Errorf("sections = %v, want [org]", ts.Sections)
	}
	if ts.Hash == ts.SectionsHash {
		t.Error("hash must stay the whole-file hash")
	}
}

// GH #133: a record written before sections_hash existed gains it on the next run that finds the repo in sync, even
// though the whole-file hash and version are unchanged.
func TestMarkerSyncBackfillsSectionsHash(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte(markerTemplate)
	current := []byte("# Repo A\n\n" + markerTemplate + "\nLocal notes.\n")
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = current
	repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(`{
	  "repositories": {"testorg/repo-a": {"claude-md": {"version": "1.0.0", "hash": "` + hash.HashBytes(current) + `"}}}
	}`)

	syncer := New(markerSyncConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReposSkipped != 1 || result.PRsCreated != 0 {
		t.Fatalf("want the in-sync repo skipped with no PR: %+v", result)
	}
	ts := savedTemplateState(t, repoSvc, "testorg/repo-a")
	if want := wantSectionsHash(t, current); ts.SectionsHash != want {
		t.Errorf("sections_hash = %q, want %q", ts.SectionsHash, want)
	}
}

// Other strategies own the whole file and record no sections; a marker template without sections records none
// either. Only the template's own sections are hashed, not others the file holds. A template section the render
// holds unreadably, or a malformed template, is an error rather than a silent whole-file fallback.
func TestMarkerSections(t *testing.T) {
	full := config.TemplateConfig{Strategy: "full-replace"}
	if got, err := markerSections(full, []byte(markerTemplate), []byte(markerTemplate)); err != nil ||
		got.hash != "" || got.names != nil {
		t.Errorf("full-replace sections = %+v, %v; want none", got, err)
	}
	marker := config.TemplateConfig{Strategy: "marker", Destination: "CLAUDE.md"}
	if got, err := markerSections(marker, []byte("no sections\n"), []byte("no sections\n")); err != nil || got.hash != "" {
		t.Errorf("marker template without sections = %+v, %v; want none", got, err)
	}
	withOther := []byte(markerTemplate + "<!-- STEERSPEC:BEGIN:local -->\nrepo\n<!-- STEERSPEC:END:local -->\n")
	got, err := markerSections(marker, []byte(markerTemplate), withOther)
	if err != nil || !slices.Equal(got.names, []string{"org"}) || got.hash != wantSectionsHash(t, []byte(markerTemplate)) {
		t.Errorf("sections = %+v, %v; want only org", got, err)
	}
	if _, err := markerSections(marker, []byte(markerTemplate), []byte(markerTemplate+markerTemplate)); err == nil {
		t.Error("a duplicated managed section in the render must be an error")
	}
	if _, err := markerSections(marker, []byte("<!-- STEERSPEC:BEGIN:org -->\n"), []byte("x")); err == nil {
		t.Error("a malformed template must be an error")
	}
}

// A repo whose file holds a managed section twice is reported, not recorded with a whole-file fallback.
func TestMarkerSyncReportsDuplicatedSection(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte(markerTemplate)
	repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = []byte(markerTemplate + "\n" + markerTemplate)

	result, err := New(markerSyncConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 1 || result.PRsCreated != 0 {
		t.Fatalf("want one error and no PR: %+v", result.RepoResults)
	}
}
