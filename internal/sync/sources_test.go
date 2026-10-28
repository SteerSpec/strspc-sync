package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

func TestResolveSource(t *testing.T) {
	s := New(makeTestConfig(), makeTestClient(newMockRepoService(), newMockPRService()), "testorg", "central")
	tmpl := config.TemplateConfig{
		ID:     "claude-md",
		Source: "templates/CLAUDE.md",
		Sources: map[string]string{
			"private":  "testorg/private-src:templates/CLAUDE.private.mustache",
			"internal": "templates/CLAUDE.internal.mustache",
		},
	}
	for _, tc := range []struct {
		visibility string
		want       templateSource
	}{
		{"private", templateSource{"testorg", "private-src", "templates/CLAUDE.private.mustache"}},
		{"internal", templateSource{"testorg", "central", "templates/CLAUDE.internal.mustache"}},
		{"public", templateSource{"testorg", "central", "templates/CLAUDE.md"}},
		{"", templateSource{"testorg", "central", "templates/CLAUDE.md"}},
	} {
		got, err := s.resolveSource(tmpl, tc.visibility)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.visibility, got, err, tc.want)
		}
	}

	noFallback := config.TemplateConfig{ID: "x", Sources: map[string]string{"private": "p"}}
	if _, err := s.resolveSource(noFallback, "public"); err == nil {
		t.Error("a visibility with no source and no fallback must error")
	}
}

// GH #80: each target receives the source for its own visibility, and a
// private source is read from the repo it names, not the public central repo.
func TestSyncUsesSourceForTargetVisibility(t *testing.T) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "pub", FullName: "testorg/pub", DefaultBranch: "main", Visibility: "public"},
		{Owner: "testorg", Name: "priv", FullName: "testorg/priv", DefaultBranch: "main", Visibility: "private"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("public text")
	repoSvc.files["testorg/hidden/templates/CLAUDE.private.md/"] = []byte("private text")

	cfg := makeTestConfig()
	cfg.Templates[0].Sources = map[string]string{"private": "testorg/hidden:templates/CLAUDE.private.md"}
	syncer := New(cfg, makeTestClient(repoSvc, prSvc), "testorg", "central")
	result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PRsCreated != 2 || result.Errors != 0 {
		t.Fatalf("want 2 PRs and no errors, got %+v", result)
	}
	for repo, want := range map[string]string{"pub": "public text", "priv": "private text"} {
		got := string(repoSvc.files["testorg/"+repo+"/CLAUDE.md/steerspec-sync/claude-md/1.0.0"])
		if got != want {
			t.Errorf("%s got %q, want %q", repo, got, want)
		}
	}
	linked := 0
	for _, pr := range prSvc.created {
		if strings.Contains(pr.Body, "testorg/hidden") {
			linked++
			if !strings.Contains(pr.Body, "CLAUDE.private.md") {
				t.Errorf("PR body links the private repo without its path: %s", pr.Body)
			}
		}
	}
	if linked != 1 {
		t.Errorf("exactly the private target's PR may link the private source, %d did", linked)
	}
}
