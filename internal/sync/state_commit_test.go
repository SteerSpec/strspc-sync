package sync

import (
	"context"
	"strings"
	"testing"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
)

func stateWrites(repoSvc *mockRepoService) int {
	n := 0
	for _, f := range repoSvc.createdFiles {
		if strings.Contains(f, ".steerspec/deployment-state.json") {
			n++
		}
	}
	return n
}

// GH #100: a run that records nothing new must not commit the state file.
func TestSaveStateSkipsUnchangedState(t *testing.T) {
	rendered := []byte("# Hello testorg")
	currentState := `{"repositories": {"testorg/repo-a": {"claude-md": {"version": "1.0.0", "hash": "` +
		hash.HashBytes(rendered) + `", "timestamp": "2026-10-01T00:00:00Z"}}}}`

	for _, tc := range []struct {
		name       string
		stateFile  string // empty means no state file in the central repo
		destBody   []byte
		wantWrites int
	}{
		{name: "nothing changed", stateFile: currentState, destBody: rendered, wantWrites: 0},
		{name: "new deployment recorded", stateFile: `{"repositories": {}}`, destBody: []byte("stale"), wantWrites: 1},
		{name: "in-sync target not yet recorded", stateFile: `{"repositories": {}}`, destBody: rendered, wantWrites: 1},
		{name: "no state file yet", stateFile: "", destBody: rendered, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoSvc := newMockRepoService()
			prSvc := newMockPRService()
			repoSvc.repos["testorg"] = []*gh.Repository{
				{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
			}
			repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
			repoSvc.files["testorg/repo-a/CLAUDE.md/main"] = tc.destBody
			if tc.stateFile != "" {
				repoSvc.files["testorg/central/.steerspec/deployment-state.json/"] = []byte(tc.stateFile)
			}

			cfg := makeTestConfig()
			cfg.Templates[0].Strategy = "mustache"
			syncer := New(cfg, makeTestClient(repoSvc, prSvc), "testorg", "central")
			result, err := syncer.Run(context.Background(), Options{Trigger: "manual"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Errors != 0 {
				t.Fatalf("unexpected errors: %+v", result)
			}
			if got := stateWrites(repoSvc); got != tc.wantWrites {
				t.Errorf("state file written %d times, want %d", got, tc.wantWrites)
			}
		})
	}
}
