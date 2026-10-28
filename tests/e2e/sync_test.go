//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// TestSyncForcedOpensPRs: a forced sync opens one PR per target, on steerspec-sync/<id>/1.0.0, carrying the rendered
// file, a versioned body, and a deployment-state entry.
func TestSyncForcedOpensPRs(t *testing.T) {
	env := harness.New(t)
	res := seedAndForceSync(t, env)

	tmplID := env.TemplateID(basicTemplateShortName)
	if res.Sync.ReposTargeted != len(syncTargets) {
		t.Errorf("ReposTargeted = %d, want %d", res.Sync.ReposTargeted, len(syncTargets))
	}
	ds := env.ReadState(t)
	for _, repo := range syncTargets {
		status, errText, number := resultFor(t, env, res, repo)
		if status != "created" || errText != "" || number == 0 {
			t.Errorf("%s: status=%q error=%q pr=%d, want created/empty/non-zero", repo, status, errText, number)
		}

		prs := basicPRs(t, env, repo)
		if len(prs) != 1 {
			t.Fatalf("%s: %d open sync-basic PRs, want 1", repo, len(prs))
		}
		pr := prs[0]
		if pr.Head != branchFor(env) {
			t.Errorf("%s: head = %q, want %q", repo, pr.Head, branchFor(env))
		}
		if pr.Number != number {
			t.Errorf("%s: PR #%d, summary says #%d", repo, pr.Number, number)
		}
		if strings.TrimSpace(pr.Title) == "" || strings.TrimSpace(pr.Body) == "" {
			t.Errorf("%s: empty PR title %q or body", repo, pr.Title)
		}
		containsAll(t, repo+" PR title", pr.Title, tmplID, syncVersion)
		containsAll(t, repo+" PR body", pr.Body,
			"| Previous version | none (first deployment) |", "| New version | `"+syncVersion+"` |")

		onBranch := string(env.ReadFileAt(t, repo, basicDestination, pr.Head))
		if !strings.Contains(onBranch, "BASIC-SOURCE") {
			t.Errorf("%s: %s on %s lacks BASIC-SOURCE:\n%s", repo, basicDestination, pr.Head, onBranch)
		}
		want := renderBasic(t, env, repo)
		if onBranch != string(want) {
			t.Errorf("%s: branch content differs from in-process render:\n got: %q\nwant: %q", repo, onBranch, want)
		}

		ts := ds.GetTemplateState(env.Org+"/"+repo, tmplID)
		if ts == nil {
			t.Errorf("%s: no deployment state entry for %s", repo, tmplID)
			continue
		}
		if ts.Hash != hash.HashBytes(want) || ts.Version != syncVersion || ts.PRNumber != number || ts.PRStatus != "open" {
			t.Errorf("%s: state = %+v, want hash %s version %s pr #%d open", repo, *ts, hash.HashBytes(want), syncVersion, number)
		}
	}
}

// TestSyncRerunOpensNothing: after a forced sync, a plain rerun skips every target, opens nothing, and does not
// touch the state file (#94, #100).
func TestSyncRerunOpensNothing(t *testing.T) {
	env := harness.New(t)
	seedAndForceSync(t, env)
	prsBefore := map[string]int{}
	for _, repo := range syncTargets {
		prsBefore[repo] = len(env.ListPRsState(t, repo, "all"))
	}
	_, shaBefore, err := env.GetFile(t.Context(), harness.RepoCentral, harness.StatePath, "")
	if err != nil {
		t.Fatalf("reading state sha: %v", err)
	}

	res := runBasicSync(t, env)
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != 0 || res.Sync.PRsUpdated != 0 {
		t.Errorf("created=%d updated=%d, want 0/0", res.Sync.PRsCreated, res.Sync.PRsUpdated)
	}
	if res.Sync.ReposSkipped != len(syncTargets) {
		t.Errorf("ReposSkipped = %d, want %d: %+v", res.Sync.ReposSkipped, len(syncTargets), res.Sync.RepoResults)
	}
	for _, repo := range syncTargets {
		if status, errText, _ := resultFor(t, env, res, repo); status != "skipped" || errText != "" {
			t.Errorf("%s: status=%q error=%q, want skipped with no error", repo, status, errText)
		}
		if n := len(env.ListPRsState(t, repo, "all")); n != prsBefore[repo] {
			t.Errorf("%s: %d PRs after rerun, %d before", repo, n, prsBefore[repo])
		}
	}

	_, shaAfter, err := env.GetFile(t.Context(), harness.RepoCentral, harness.StatePath, "")
	if err != nil {
		t.Fatalf("reading state sha: %v", err)
	}
	if shaAfter != shaBefore {
		t.Errorf("state file blob changed on a no-op rerun: %s -> %s (#100)", shaBefore, shaAfter)
	}
}

// TestSyncClosedPRIsReported: a sync PR closed without merging is reported on the next plain run, and no new PR is
// opened (#91).
func TestSyncClosedPRIsReported(t *testing.T) {
	env := harness.New(t)
	seedAndForceSync(t, env)

	closedRepo := harness.RepoPublic
	prs := basicPRs(t, env, closedRepo)
	if len(prs) != 1 {
		t.Fatalf("%s: %d open sync-basic PRs, want 1", closedRepo, len(prs))
	}
	closed := prs[0].Number
	closePR(t, env, closedRepo, closed)

	res := runBasicSync(t, env)
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != 0 || res.Sync.PRsUpdated != 0 {
		t.Errorf("created=%d updated=%d, want 0/0", res.Sync.PRsCreated, res.Sync.PRsUpdated)
	}
	status, errText, number := resultFor(t, env, res, closedRepo)
	if status != "skipped" || number != closed {
		t.Errorf("%s: status=%q pr=%d, want skipped with PR #%d", closedRepo, status, number, closed)
	}
	containsAll(t, "closed-PR report", errText, "closed without merging", "force")

	// The other target's PR is still open, so it is skipped silently.
	if status, errText, _ := resultFor(t, env, res, harness.RepoPrivate); status != "skipped" || errText != "" {
		t.Errorf("%s: status=%q error=%q, want skipped with no error", harness.RepoPrivate, status, errText)
	}
	if n := len(basicPRs(t, env, closedRepo)); n != 0 {
		t.Errorf("%s: %d open sync-basic PRs after rerun, want 0 (closed PR must not be reopened)", closedRepo, n)
	}
	if n := len(basicPRs(t, env, harness.RepoPrivate)); n != 1 {
		t.Errorf("%s: %d open sync-basic PRs, want the original 1", harness.RepoPrivate, n)
	}
	ts := env.ReadState(t).GetTemplateState(env.Org+"/"+closedRepo, env.TemplateID(basicTemplateShortName))
	if ts == nil || ts.PRStatus != "closed" {
		t.Errorf("state for %s = %+v, want pr_status closed", closedRepo, ts)
	}
}

// TestSyncAlreadyCurrentOpensNothing: when the default branch already holds the rendered content, even a forced sync
// opens no PR (#94).
func TestSyncAlreadyCurrentOpensNothing(t *testing.T) {
	env := harness.New(t)
	seedSources(t, env)

	repo := harness.RepoPublic
	want := renderBasic(t, env, repo)
	env.WriteFile(t, repo, basicDestination, want)

	res := env.Run(t, "sync", "--force",
		"--template-filter", env.TemplateID(basicTemplateShortName),
		"--target-filter", env.Org+"/"+repo)
	requireSyncOK(t, res)
	if res.Sync.ReposTargeted != 1 {
		t.Fatalf("ReposTargeted = %d, want 1", res.Sync.ReposTargeted)
	}
	status, errText, number := resultFor(t, env, res, repo)
	if status != "skipped" || errText != "" || number != 0 {
		t.Errorf("%s: status=%q error=%q pr=%d, want skipped/empty/0", repo, status, errText, number)
	}
	if res.Sync.PRsCreated != 0 || res.Sync.PRsUpdated != 0 {
		t.Errorf("created=%d updated=%d, want 0/0", res.Sync.PRsCreated, res.Sync.PRsUpdated)
	}
	if n := len(basicPRs(t, env, repo)); n != 0 {
		t.Errorf("%s: %d open sync-basic PRs, want 0", repo, n)
	}
	if n := len(env.PRsWithPrefixState(t, repo, "closed", env.BranchPrefix())); n != 0 {
		t.Errorf("%s: %d closed sync PRs, want 0", repo, n)
	}

	// The sync records the repo as in sync, with no PR.
	ts := env.ReadState(t).GetTemplateState(env.Org+"/"+repo, env.TemplateID(basicTemplateShortName))
	if ts == nil || ts.Hash != hash.HashBytes(want) || ts.PRNumber != 0 {
		t.Errorf("state for %s = %+v, want hash %s and no PR", repo, ts, hash.HashBytes(want))
	}
}
