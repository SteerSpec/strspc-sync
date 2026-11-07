//go:build e2e

package e2e

import (
	"slices"
	"sort"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// targetTopic is the topic carried by strspc-e2e-develop only (one-time testbed setup, tests/e2e/README.md).
const targetTopic = "strspc-e2e-topic"

// TestSyncTargetSelection: target resolution from the config alone (no --target-filter). An include glob and an
// explicit include pick public, private and symlink; the topic adds develop; an exclude glob removes symlink.
// Overrides then shape what each target gets: private skips sync-visibility and renders with an override variable,
// develop takes only sync-visibility.
func TestSyncTargetSelection(t *testing.T) {
	env := harness.New(t)
	if !slices.Contains(env.RepoTopics(t, harness.RepoDevelop), targetTopic) {
		t.Fatalf("testbed setup: %s must carry the topic %q (tests/e2e/README.md, one-time setup)",
			harness.RepoDevelop, targetTopic)
	}
	seedSources(t, env)

	full := func(repo string) string { return env.Org + "/" + repo }
	basicID, visID := env.TemplateID(basicTemplateShortName), env.TemplateID(visibilityTemplateShortName)
	editConfig(t, env, func(cfg *config.SyncConfig) {
		cfg.Targets = config.TargetsConfig{
			Include: []string{env.Org + "/strspc-e2e-p*", full(harness.RepoSymlink)},
			Topics:  []string{targetTopic},
			Exclude: []string{env.Org + "/strspc-e2e-sym*"},
			Overrides: []config.TargetOverride{
				{
					Repo:             full(harness.RepoPrivate),
					Variables:        map[string]string{"e2e_marker": "override-marker"},
					ExcludeTemplates: []string{visID},
				},
				{Repo: full(harness.RepoDevelop), IncludeTemplates: []string{visID}},
			},
		}
	})

	res := env.Run(t, "sync")
	requireSyncOK(t, res)

	var got []string
	for _, rr := range res.Sync.RepoResults {
		got = append(got, rr.Repo+" "+rr.TemplateID)
		if rr.Status != "created" || rr.Error != "" {
			t.Errorf("%s %s: status=%q error=%q, want created", rr.Repo, rr.TemplateID, rr.Status, rr.Error)
		}
	}
	sort.Strings(got)
	want := []string{
		full(harness.RepoDevelop) + " " + visID,
		full(harness.RepoPrivate) + " " + basicID,
		full(harness.RepoPublic) + " " + basicID,
		full(harness.RepoPublic) + " " + visID,
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("synced (repo, template) pairs:\n got: %v\nwant: %v", got, want)
	}
	if res.Sync.ReposTargeted != 3 {
		t.Errorf("ReposTargeted = %d, want 3 (public, private, develop)", res.Sync.ReposTargeted)
	}

	// The override variable wins over the global one, for that target only.
	priv := onlyPR(t, env, harness.RepoPrivate, basicTemplateShortName)
	containsAll(t, "private CLAUDE.md", string(env.ReadFileAt(t, harness.RepoPrivate, basicDestination, priv.Head)),
		"BASIC-SOURCE", "(override-marker)")
	pub := onlyPR(t, env, harness.RepoPublic, basicTemplateShortName)
	pubGot := string(env.ReadFileAt(t, harness.RepoPublic, basicDestination, pub.Head))
	if pubWant := string(renderBasic(t, env, harness.RepoPublic)); pubGot != pubWant {
		t.Errorf("public CLAUDE.md:\n got: %q\nwant: %q", pubGot, pubWant)
	}

	// The topic-selected repo gets its private-visibility source on its own default branch.
	dev := onlyPR(t, env, harness.RepoDevelop, visibilityTemplateShortName)
	if dev.Base != developBranch {
		t.Errorf("develop PR base = %q, want %q", dev.Base, developBranch)
	}
	containsAll(t, "develop visibility.md", string(env.ReadFileAt(t, harness.RepoDevelop, visibilityDestination, dev.Head)),
		"PRIVATE-SOURCE", "(strspc-e2e)")

	if n := len(env.PRsWithPrefix(t, harness.RepoSymlink, env.BranchPrefix())); n != 0 {
		t.Errorf("%s is excluded but has %d sync PRs", harness.RepoSymlink, n)
	}
}
