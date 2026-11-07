//go:build e2e

package e2e

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// revisedBasic is the sync-basic fixture with one more line, standing in for a template edit in the central repo.
func revisedBasic(t *testing.T) []byte {
	t.Helper()
	return append(fixture(t, "templates/basic.mustache"), []byte("\nREVISED-SOURCE: second revision.\n")...)
}

// TestSyncTemplateEditUpdatesOpenPR: when the template changes while its sync PR is still open and the version
// stays the same, a plain run pushes the new content to the same branch and updates that PR instead of opening
// another one.
func TestSyncTemplateEditUpdatesOpenPR(t *testing.T) {
	env := harness.New(t)
	first := seedAndForceSync(t, env)

	revised := revisedBasic(t)
	mcWriteAndWait(t, env, harness.RepoCentral, basicTemplatePath, revised)

	res := runBasicSync(t, env)
	requireSyncOK(t, res)
	if res.Sync.PRsUpdated != len(syncTargets) || res.Sync.PRsCreated != 0 {
		t.Errorf("updated=%d created=%d, want %d/0: %+v",
			res.Sync.PRsUpdated, res.Sync.PRsCreated, len(syncTargets), res.Sync.RepoResults)
	}
	ds := env.ReadState(t)
	for _, repo := range syncTargets {
		_, _, firstPR := resultFor(t, env, first, repo)
		status, errText, number := resultFor(t, env, res, repo)
		if status != "updated" || errText != "" || number != firstPR {
			t.Errorf("%s: status=%q error=%q pr=%d, want updated/empty/#%d", repo, status, errText, number, firstPR)
		}

		pr := onlyPR(t, env, repo, basicTemplateShortName)
		if pr.Number != firstPR || pr.Head != branchFor(env) {
			t.Errorf("%s: open PR #%d on %s, want #%d on %s", repo, pr.Number, pr.Head, firstPR, branchFor(env))
		}
		want := renderTemplate(t, env, repo, basicTemplateShortName, revised, nil)
		if got := env.ReadFileAt(t, repo, basicDestination, pr.Head); !bytes.Equal(got, want) {
			t.Errorf("%s: branch content not updated:\n got: %q\nwant: %q", repo, got, want)
		}
		containsAll(t, repo+" PR body", pr.Body, hash.HashBytes(want))
		if bad := env.UnverifiedPRCommits(t, repo, pr.Number); len(bad) > 0 {
			t.Errorf("%s: PR #%d has unsigned commits: %v", repo, pr.Number, bad)
		}

		ts := ds.GetTemplateState(env.Org+"/"+repo, env.TemplateID(basicTemplateShortName))
		if ts == nil || ts.Hash != hash.HashBytes(want) || ts.PRNumber != firstPR || ts.PRStatus != "open" {
			t.Errorf("%s: state = %+v, want hash %s, PR #%d open", repo, ts, hash.HashBytes(want), firstPR)
		}
	}
}

// TestSyncVersionBumpClosesStalePR: a new template version with new content opens a PR on the new version's branch
// and, with close-stale-prs (the default), closes the open PR of the previous version. The new PR reports the
// previous version.
func TestSyncVersionBumpClosesStalePR(t *testing.T) {
	env := harness.New(t)
	first := seedAndForceSync(t, env)

	const bumped = "1.1.0"
	revised := revisedBasic(t)
	mcWriteAndWait(t, env, harness.RepoCentral, basicTemplatePath, revised)
	setTemplate(t, env, basicTemplateShortName, func(tm *config.TemplateConfig) { tm.Version = bumped })

	res := runBasicSync(t, env)
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != len(syncTargets) {
		t.Errorf("PRsCreated = %d, want %d: %+v", res.Sync.PRsCreated, len(syncTargets), res.Sync.RepoResults)
	}
	newBranch := "steerspec-sync/" + env.TemplateID(basicTemplateShortName) + "/" + bumped
	ds := env.ReadState(t)
	for _, repo := range syncTargets {
		_, _, oldPR := resultFor(t, env, first, repo)
		status, errText, number := resultFor(t, env, res, repo)
		if status != "created" || errText != "" || number == 0 || number == oldPR {
			t.Errorf("%s: status=%q error=%q pr=%d, want created with a PR other than #%d",
				repo, status, errText, number, oldPR)
		}

		pr := onlyPR(t, env, repo, basicTemplateShortName)
		if pr.Number != number || pr.Head != newBranch {
			t.Errorf("%s: open PR #%d on %s, want #%d on %s", repo, pr.Number, pr.Head, number, newBranch)
		}
		containsAll(t, repo+" PR body", pr.Body,
			"| Previous version | `"+syncVersion+"` |", "| New version | `"+bumped+"` |")
		want := renderTemplate(t, env, repo, basicTemplateShortName, revised, nil)
		if got := env.ReadFileAt(t, repo, basicDestination, pr.Head); !bytes.Equal(got, want) {
			t.Errorf("%s: %s content:\n got: %q\nwant: %q", repo, newBranch, got, want)
		}

		closed := false
		for _, c := range env.PRsWithPrefixState(t, repo, "closed", "steerspec-sync/"+env.TemplateID(basicTemplateShortName)+"/") {
			if c.Number == oldPR {
				closed = true
				if c.Merged {
					t.Errorf("%s: stale PR #%d was merged, want closed without merging", repo, oldPR)
				}
			}
		}
		if !closed {
			t.Errorf("%s: stale %s PR #%d is not closed", repo, syncVersion, oldPR)
		}

		ts := ds.GetTemplateState(env.Org+"/"+repo, env.TemplateID(basicTemplateShortName))
		if ts == nil || ts.Version != bumped || ts.PreviousVersion != syncVersion || ts.PRNumber != number {
			t.Errorf("%s: state = %+v, want version %s (previous %s), PR #%d", repo, ts, bumped, syncVersion, number)
		}
	}
}

// TestSyncMergedPRIsRecorded: once a sync PR is merged, the next plain run records the merge, opens nothing, and a
// forced run opens nothing either, because the default branch already holds the content.
func TestSyncMergedPRIsRecorded(t *testing.T) {
	env := harness.New(t)
	// The merge changes the default branch, and Cleanup restores it only from memory. A run-scoped marker keeps
	// the merged content distinct from every other run's render, so a run killed before Cleanup cannot leave a
	// CLAUDE.md that makes later forced syncs see the repo as already current.
	setTemplate(t, env, basicTemplateShortName, func(tm *config.TemplateConfig) {
		if tm.Variables == nil {
			tm.Variables = map[string]string{}
		}
		tm.Variables["e2e_marker"] = "merged-" + env.RunID
	})
	seedAndForceSync(t, env)

	repo := harness.RepoPublic
	pr := onlyPR(t, env, repo, basicTemplateShortName)
	want := renderBasic(t, env, repo)
	containsAll(t, "merged render", string(want), "merged-"+env.RunID)
	env.Track(t, repo, basicDestination)
	env.MergePR(t, repo, pr.Number)
	harness.Eventually(t, mcWait, func() bool {
		got, _, err := env.GetFile(t.Context(), repo, basicDestination, "")
		return err == nil && bytes.Equal(got, want)
	}, repo+" default branch to hold the merged content")

	for _, force := range []bool{false, true} {
		var extra []string
		if force {
			extra = append(extra, "--force")
		}
		res := runBasicSync(t, env, extra...)
		requireSyncOK(t, res)
		run := "plain run"
		if force {
			run = "forced run"
		}
		status, errText, _ := resultFor(t, env, res, repo)
		if status != "skipped" || errText != "" {
			t.Errorf("%s: %s: status=%q error=%q, want skipped with no error", repo, run, status, errText)
		}
		if res.Sync.PRsCreated != 0 {
			t.Errorf("%s: PRsCreated = %d, want 0: %+v", run, res.Sync.PRsCreated, res.Sync.RepoResults)
		}
		if n := len(prsFor(t, env, repo, basicTemplateShortName)); n != 0 {
			t.Errorf("%s: %s: %d open sync-basic PRs, want 0", repo, run, n)
		}

		ts := env.ReadState(t).GetTemplateState(env.Org+"/"+repo, env.TemplateID(basicTemplateShortName))
		if ts == nil || ts.PRStatus != "merged" || ts.Hash != hash.HashBytes(want) || ts.Version != syncVersion {
			t.Errorf("%s: %s: state = %+v, want merged, hash %s, version %s",
				repo, run, ts, hash.HashBytes(want), syncVersion)
		}
	}

	// The other target's PR was not merged and stays open.
	if prs := prsFor(t, env, harness.RepoPrivate, basicTemplateShortName); len(prs) != 1 ||
		!strings.HasSuffix(prs[0].Head, "/"+syncVersion) {
		t.Errorf("%s: open sync-basic PRs = %d, want the original one", harness.RepoPrivate, len(prs))
	}
}
