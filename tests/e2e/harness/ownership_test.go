//go:build e2e

package harness

import (
	"testing"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

func TestOwnedByRun(t *testing.T) {
	marker := templateID("7-1", "")
	for _, tc := range []struct {
		name  string
		issue gh.Issue
		want  bool
	}{
		{"marker in title", gh.Issue{Title: "drift e2e-7-1-sync-basic"}, true},
		{"marker in body", gh.Issue{Body: "template e2e-7-1-sync-basic"}, true},
		{"drift label", gh.Issue{Title: "CLAUDE.md drifted", Labels: []string{"steerspec-drift"}}, true},
		{"conflict label without marker", gh.Issue{Title: "duplicate skill", Labels: []string{"steerspec-conflict"}}, true},
		{"other run, no label", gh.Issue{Title: "e2e-8-1-sync-basic", Labels: []string{"bug"}}, false},
		{"unrelated", gh.Issue{Title: "question"}, false},
	} {
		if got := ownedByRun(&tc.issue, marker); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
