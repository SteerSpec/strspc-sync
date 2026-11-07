package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

// GH #133: with a sections hash recorded, only the managed sections count. A repo's own content outside them is
// not drift; an edit inside them is, and the issue reports the sections hashes.
func TestDriftComparesManagedSectionsOnly(t *testing.T) {
	const managed = "<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->\n"
	deployed := []byte("# App\n" + managed)
	sections, ok, err := template.SectionsHash(deployed, []string{"org"})
	if err != nil || !ok {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		content string
		drifted bool
	}{
		{"repo adds its own sections", "# App\n\n## Build\n\nmake build\n\n" + managed + "\n## Architecture\n", false},
		{"managed section edited", "# App\n<!-- STEERSPEC:BEGIN:org -->\nLocal rules.\n<!-- STEERSPEC:END:org -->\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoSvc := &mockRepoService{files: map[string][]byte{"org/app/CLAUDE.md": []byte(tt.content)}}
			issueSvc := newMockIssueService()
			ds := makeState(map[string]map[string]state.TemplateState{
				"org/app": {"claude-md": {
					Version: "1.0", Hash: hash.HashBytes(deployed), Sections: []string{"org"}, SectionsHash: sections, Timestamp: time.Now(),
				}},
			})

			res, err := New(testConfig(), makeClient(repoSvc, issueSvc), "test", "central").
				Run(context.Background(), ds, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.ReposDrifted == 1; got != tt.drifted {
				t.Fatalf("drifted = %v, want %v: %+v", got, tt.drifted, res)
			}
			if tt.drifted && res.DriftEntries[0].ExpectedHash != sections {
				t.Errorf("expected hash = %s, want the sections hash %s", res.DriftEntries[0].ExpectedHash, sections)
			}
			if !tt.drifted && res.ReposInSync != 1 {
				t.Errorf("ReposInSync = %d, want 1", res.ReposInSync)
			}
		})
	}
}
