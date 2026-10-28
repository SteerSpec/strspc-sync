//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/internal/conflict"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// mcMonitor runs `strspc monitor` restricted to one repo (monitor matches --target-filter exactly, not as a glob)
// and requires a parsed summary and a zero exit.
func mcMonitor(t *testing.T, env *harness.Env, repo string) *harness.Result {
	t.Helper()
	r := env.Run(t, "monitor", "--target-filter", env.Org+"/"+repo)
	if r.ParseErr != nil || r.Monitor == nil {
		t.Fatalf("monitor: no summary (exit %d, %v)\nstderr: %s", r.ExitCode, r.ParseErr, r.Stderr)
	}
	if r.ExitCode != 0 {
		t.Fatalf("monitor exit = %d, want 0\nstderr: %s", r.ExitCode, r.Stderr)
	}
	return r
}

// mcConflict runs `strspc conflict --tiers tiers` and requires a parsed report and the wanted exit code. The
// conflict command has no per-repo filter (it scans every repo in the deployment state), so scenarios keep the state
// down to the repos they use.
func mcConflict(t *testing.T, env *harness.Env, tiers string, wantExit int) *conflict.ConflictReport {
	t.Helper()
	r := env.Run(t, "conflict", "--tiers", tiers)
	if r.ParseErr != nil || r.Conflict == nil {
		t.Fatalf("conflict: no report (exit %d, %v)\nstderr: %s", r.ExitCode, r.ParseErr, r.Stderr)
	}
	if r.ExitCode != wantExit {
		t.Fatalf("conflict exit = %d, want %d\nstderr: %s", r.ExitCode, wantExit, r.Stderr)
	}
	return r.Conflict
}

// mcEntryTypes returns the conflict types of the report's entries with the given severity, counted.
func mcEntryTypes(rep *conflict.ConflictReport, sev conflict.Severity) map[conflict.ConflictType]int {
	out := make(map[conflict.ConflictType]int)
	for _, e := range rep.Entries {
		if e.Severity == sev {
			out[e.Type]++
		}
	}
	return out
}

// TestMonitorDriftLifecycle: in sync, no issue; drift opens one issue in the central repo; a rerun adds none; the
// revert closes it.
func TestMonitorDriftLifecycle(t *testing.T) {
	env := harness.New(t)
	repo := harness.RepoPublic
	full := env.Org + "/" + repo
	tmpl := env.TemplateID("sync-basic")
	wantTitle := "[SteerSpec Drift] " + full + ": " + tmpl
	synced := mcSyncedContent(env.Org)

	mcSeedSynced(t, env, repo)

	// 1. In sync: nothing drifted, no issue.
	r := mcMonitor(t, env, repo)
	if m := r.Monitor; m.ReposInSync != 1 || m.ReposDrifted != 0 || m.IssuesCreated != 0 || m.IssuesClosed != 0 ||
		len(m.DriftEntries) != 0 {
		t.Fatalf("synced monitor = %+v, want repos_in_sync 1 and nothing else", m)
	}
	mcWaitIssues(t, env, harness.RepoCentral, mcDriftLabel, tmpl, 0)

	// 2. Drift: append a comment line to the managed file.
	drifted := append(append([]byte{}, synced...), []byte("<!-- e2e: local edit -->\n")...)
	mcWriteAndWait(t, env, repo, mcClaudePath, drifted)

	r = mcMonitor(t, env, repo)
	m := r.Monitor
	if m.ReposDrifted != 1 || m.ReposInSync != 0 || m.IssuesCreated != 1 || m.IssuesClosed != 0 {
		t.Fatalf("drift monitor = %+v, want repos_drifted 1, issues_created 1", m)
	}
	if len(m.DriftEntries) != 1 {
		t.Fatalf("drift entries = %d, want 1: %+v", len(m.DriftEntries), m.DriftEntries)
	}
	de := m.DriftEntries[0]
	if de.Repo != full || de.TemplateID != tmpl || de.ExpectedVersion != "1.0.0" || de.IssueNumber == 0 {
		t.Errorf("drift entry = %+v, want repo %s template %s version 1.0.0 and an issue number", de, full, tmpl)
	}
	if de.ActualHash == "" || de.ActualHash == de.ExpectedHash {
		t.Errorf("drift entry hashes expected=%q actual=%q, want different non-empty actual", de.ExpectedHash, de.ActualHash)
	}
	issues := mcWaitIssues(t, env, harness.RepoCentral, mcDriftLabel, tmpl, 1)
	if issues[0].Title != wantTitle {
		t.Errorf("drift issue title = %q, want %q", issues[0].Title, wantTitle)
	}
	if issues[0].Number != de.IssueNumber {
		t.Errorf("drift issue #%d, entry says #%d", issues[0].Number, de.IssueNumber)
	}
	if !strings.Contains(issues[0].Body, "steerspec-fingerprint") {
		t.Errorf("drift issue body has no fingerprint marker:\n%s", issues[0].Body)
	}

	// 3. Rerun, nothing changed: still drifted, same issue, none created (fingerprint idempotency).
	r = mcMonitor(t, env, repo)
	m = r.Monitor
	if m.ReposDrifted != 1 || m.IssuesCreated != 0 || m.IssuesClosed != 0 {
		t.Fatalf("rerun monitor = %+v, want repos_drifted 1, issues_created 0", m)
	}
	if len(m.DriftEntries) != 1 || m.DriftEntries[0].IssueNumber != issues[0].Number {
		t.Errorf("rerun entries = %+v, want the existing issue #%d", m.DriftEntries, issues[0].Number)
	}
	mcWaitIssues(t, env, harness.RepoCentral, mcDriftLabel, tmpl, 1)

	// 4. Revert: back in sync, the issue is closed.
	mcWriteAndWait(t, env, repo, mcClaudePath, synced)
	r = mcMonitor(t, env, repo)
	m = r.Monitor
	if m.ReposInSync != 1 || m.ReposDrifted != 0 || m.IssuesCreated != 0 || m.IssuesClosed != 1 {
		t.Fatalf("revert monitor = %+v, want repos_in_sync 1, issues_closed 1", m)
	}
	mcWaitIssues(t, env, harness.RepoCentral, mcDriftLabel, tmpl, 0)
}

// TestConflictTier1Lifecycle: tier 1 finds version drift and a broken cross-reference in a target, opens one issue
// per finding in that target, exits 1 (critical), does not duplicate on rerun, and closes the issues once fixed.
func TestConflictTier1Lifecycle(t *testing.T) {
	env := harness.New(t)
	repo := harness.RepoPublic
	full := env.Org + "/" + repo
	synced := mcSyncedContent(env.Org)
	const (
		driftTitle = "[SteerSpec Conflict] version-drift: CLAUDE.md"
		refPath    = ".claude/agents/e2e-missing.md"
		refTitle   = "[SteerSpec Conflict] cross-reference-broken: " + refPath
	)

	mcSeedSynced(t, env, repo)

	// 1. In sync: no findings, exit 0.
	rep := mcConflict(t, env, "1", 0)
	if rep.SeveritySummary.Critical != 0 || rep.SeveritySummary.Warning != 0 || rep.IssuesCreated != 0 ||
		rep.IssuesClosed != 0 {
		t.Fatalf("synced report = %+v, want no critical/warning findings and no issues", rep)
	}

	// 2. Version drift: the file no longer matches the recorded hash.
	drifted := append(append([]byte{}, synced...), []byte("<!-- e2e: local edit -->\n")...)
	mcWriteAndWait(t, env, repo, mcClaudePath, drifted)

	rep = mcConflict(t, env, "1", 1)
	if rep.SeveritySummary.Critical != 1 || rep.IssuesCreated != 1 || rep.IssuesClosed != 0 {
		t.Fatalf("drift report = %+v, want critical 1, issues_created 1", rep)
	}
	if got := mcEntryTypes(rep, conflict.SeverityCritical); got[conflict.TypeVersionDrift] != 1 {
		t.Fatalf("critical entries = %v, want one version-drift", got)
	}
	for _, e := range rep.Entries {
		if e.Type == conflict.TypeVersionDrift && (e.Repo != full || e.FilePath != mcClaudePath) {
			t.Errorf("version-drift entry = %+v, want repo %s file %s", e, full, mcClaudePath)
		}
	}
	issues := mcWaitIssues(t, env, repo, mcConflictLabel, driftTitle, 1)
	if !strings.Contains(issues[0].Body, "steerspec-conflict-type: version-drift") {
		t.Errorf("conflict issue body lacks the type marker:\n%s", issues[0].Body)
	}

	// 3. Rerun: still critical (exit 1) but no second issue.
	rep = mcConflict(t, env, "1", 1)
	if rep.SeveritySummary.Critical != 1 || rep.IssuesCreated != 0 || rep.IssuesClosed != 0 {
		t.Fatalf("rerun report = %+v, want critical 1, issues_created 0, issues_closed 0", rep)
	}
	mcWaitIssues(t, env, repo, mcConflictLabel, driftTitle, 1)

	// 4. A reference to a missing agent file adds a second tier 1 finding on top of the drift.
	broken := append(append([]byte{}, synced...), []byte("\nSee agents/e2e-missing.md for details.\n")...)
	mcWriteAndWait(t, env, repo, mcClaudePath, broken)

	rep = mcConflict(t, env, "1", 1)
	got := mcEntryTypes(rep, conflict.SeverityCritical)
	if rep.SeveritySummary.Critical != 2 || got[conflict.TypeVersionDrift] != 1 || got[conflict.TypeCrossReferenceBroken] != 1 ||
		rep.IssuesCreated != 1 || rep.IssuesClosed != 0 {
		t.Fatalf("cross-ref report = %+v (critical types %v), want drift + cross-reference-broken, issues_created 1", rep, got)
	}
	for _, e := range rep.Entries {
		if e.Type == conflict.TypeCrossReferenceBroken && (e.Repo != full || e.FilePath != refPath) {
			t.Errorf("cross-reference entry = %+v, want repo %s file %s", e, full, refPath)
		}
	}
	mcWaitIssues(t, env, repo, mcConflictLabel, driftTitle, 1)
	mcWaitIssues(t, env, repo, mcConflictLabel, refTitle, 1)

	// 5. Fix: restore the managed content. Both findings go away, both issues close, exit 0.
	mcWriteAndWait(t, env, repo, mcClaudePath, synced)
	rep = mcConflict(t, env, "1", 0)
	if rep.SeveritySummary.Critical != 0 || rep.IssuesCreated != 0 || rep.IssuesClosed != 2 {
		t.Fatalf("fixed report = %+v, want critical 0, issues_closed 2", rep)
	}
	mcWaitIssues(t, env, repo, mcConflictLabel, "[SteerSpec Conflict]", 0)
}

// TestConflictTier2Lifecycle: tier 2 duplicate-skill (same skill file, different content in two targets; issue in the
// central repo) and section overlap (one STEERSPEC marker id in two marker-managed files of a target; issue in the
// target). Both are warnings, so the command exits 0. The fixture has no skill or marker templates, so the scenario
// adds them to its own copy of the run config.
func TestConflictTier2Lifecycle(t *testing.T) {
	env := harness.New(t)
	pub, priv := harness.RepoPublic, harness.RepoPrivate
	const (
		skillPath  = ".claude/skills/e2e-dup-skill.md"
		skillFile  = "e2e-dup-skill.md"
		markerA    = ".steerspec-e2e/marker-a.md"
		markerB    = ".steerspec-e2e/marker-b.md"
		dupTitle   = "[SteerSpec Conflict] duplicate-skill: " + skillFile
		overTitle  = "[SteerSpec Conflict] manual-override: " + markerB
		markerText = "<!-- STEERSPEC:e2e-shared -->\nshared section\n<!-- /STEERSPEC:e2e-shared -->\n"
	)
	mcAddTemplates(t, env,
		config.TemplateConfig{
			ID: env.TemplateID("skill-dup"), Version: "1.0.0", Type: "skill", Strategy: "full-replace",
			Source: "templates/basic.mustache", Destination: skillPath,
		},
		config.TemplateConfig{
			ID: env.TemplateID("marker-a"), Version: "1.0.0", Type: "custom", Strategy: "marker",
			Source: "templates/basic.mustache", Destination: markerA,
		},
		config.TemplateConfig{
			ID: env.TemplateID("marker-b"), Version: "1.0.0", Type: "custom", Strategy: "marker",
			Source: "templates/basic.mustache", Destination: markerB,
		},
	)
	mcSeedSynced(t, env, pub, priv)

	// 1. Duplicate skill: same file name, different content in two targets.
	mcWriteAndWait(t, env, pub, skillPath, []byte("# skill\nversion A\n"))
	mcWriteAndWait(t, env, priv, skillPath, []byte("# skill\nversion B\n"))

	rep := mcConflict(t, env, "2", 0)
	if got := mcEntryTypes(rep, conflict.SeverityWarning); got[conflict.TypeDuplicateSkill] != 1 || len(got) != 1 {
		t.Fatalf("warning entries = %v, want exactly one duplicate-skill\n%+v", got, rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.FilePath != skillFile || !strings.Contains(e.Repo, env.Org+"/"+pub) || !strings.Contains(e.Repo, env.Org+"/"+priv) {
			t.Errorf("duplicate-skill entry = %+v, want file %s naming both repos", e, skillFile)
		}
	}
	if rep.SeveritySummary.Critical != 0 || rep.IssuesCreated != 1 || rep.IssuesClosed != 0 {
		t.Fatalf("duplicate report = %+v, want critical 0, issues_created 1", rep)
	}
	// Multi-repo findings are filed in the central repo.
	mcWaitIssues(t, env, harness.RepoCentral, mcConflictLabel, dupTitle, 1)

	rep = mcConflict(t, env, "2", 0)
	if rep.IssuesCreated != 0 || rep.IssuesClosed != 0 {
		t.Fatalf("duplicate rerun = %+v, want no issues created or closed", rep)
	}
	mcWaitIssues(t, env, harness.RepoCentral, mcConflictLabel, dupTitle, 1)

	// Fix: make the skill identical everywhere.
	mcWriteAndWait(t, env, priv, skillPath, []byte("# skill\nversion A\n"))
	rep = mcConflict(t, env, "2", 0)
	if rep.IssuesCreated != 0 || rep.IssuesClosed != 1 || len(rep.Entries) != 0 {
		t.Fatalf("duplicate fixed = %+v, want no entries and issues_closed 1", rep)
	}
	mcWaitIssues(t, env, harness.RepoCentral, mcConflictLabel, dupTitle, 0)

	// 2. Section overlap: the same STEERSPEC marker id in two marker-managed files of one target.
	mcWriteAndWait(t, env, pub, markerA, []byte(markerText))
	mcWriteAndWait(t, env, pub, markerB, []byte(markerText))

	rep = mcConflict(t, env, "2", 0)
	if got := mcEntryTypes(rep, conflict.SeverityWarning); got[conflict.TypeManualOverride] != 1 || len(got) != 1 {
		t.Fatalf("warning entries = %v, want exactly one manual-override\n%+v", got, rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Repo != env.Org+"/"+pub || e.FilePath != markerB {
			t.Errorf("manual-override entry = %+v, want repo %s file %s", e, env.Org+"/"+pub, markerB)
		}
	}
	if rep.IssuesCreated != 1 || rep.IssuesClosed != 0 {
		t.Fatalf("overlap report = %+v, want issues_created 1", rep)
	}
	mcWaitIssues(t, env, pub, mcConflictLabel, overTitle, 1)

	rep = mcConflict(t, env, "2", 0)
	if rep.IssuesCreated != 0 || rep.IssuesClosed != 0 {
		t.Fatalf("overlap rerun = %+v, want no issues created or closed", rep)
	}
	mcWaitIssues(t, env, pub, mcConflictLabel, overTitle, 1)

	// Fix: drop the duplicate section from the second file.
	mcWriteAndWait(t, env, pub, markerB, []byte("no markers here\n"))
	rep = mcConflict(t, env, "2", 0)
	if rep.IssuesCreated != 0 || rep.IssuesClosed != 1 || len(rep.Entries) != 0 {
		t.Fatalf("overlap fixed = %+v, want no entries and issues_closed 1", rep)
	}
	mcWaitIssues(t, env, pub, mcConflictLabel, overTitle, 0)
}
