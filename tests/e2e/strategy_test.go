//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/template"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// strategyScenario writes a template source into the central repo and an existing destination into the public
// target, registers the template under shortName with strategy, and runs a forced sync of that template on that
// repo. It returns the content of the destination on the sync PR's branch. Both files are restored by Cleanup.
func strategyScenario(t *testing.T, env *harness.Env, shortName, strategy, srcPath, dest string, src, existing []byte) string {
	t.Helper()
	repo := harness.RepoPublic
	mcWriteAndWait(t, env, harness.RepoCentral, srcPath, src)
	mcWriteAndWait(t, env, repo, dest, existing)
	mcAddTemplates(t, env, config.TemplateConfig{
		ID:          env.TemplateID(shortName),
		Version:     syncVersion,
		Type:        "custom",
		Strategy:    strategy,
		Source:      srcPath,
		Destination: dest,
	})

	res := env.Run(t, "sync", "--force",
		"--template-filter", env.TemplateID(shortName),
		"--target-filter", env.Org+"/"+repo)
	requireSyncOK(t, res)
	rr, ok := res.RepoResultFor(repo, env.TemplateID(shortName))
	if !ok || rr.Status != "created" || rr.Error != "" {
		t.Fatalf("%s sync on %s: result %+v (found=%v), want created", strategy, repo, rr, ok)
	}
	pr := onlyPR(t, env, repo, shortName)
	if bad := env.UnverifiedPRCommits(t, repo, pr.Number); len(bad) > 0 {
		t.Errorf("PR #%d has unsigned commits: %v", pr.Number, bad)
	}
	return string(env.ReadFileAt(t, repo, dest, pr.Head))
}

// TestSyncMarkerKeepsRepoContent: the marker strategy replaces only the managed section and leaves the repo's own
// content above and below it untouched, byte for byte. A managed section the file lacks is appended.
func TestSyncMarkerKeepsRepoContent(t *testing.T) {
	env := harness.New(t)
	src := []byte("<!-- STEERSPEC:BEGIN:e2e-rules -->\nMARKER-SOURCE: managed rules v2.\n<!-- STEERSPEC:END:e2e-rules -->\n" +
		"\n<!-- STEERSPEC:BEGIN:e2e-extra -->\nMARKER-SOURCE: new section.\n<!-- STEERSPEC:END:e2e-extra -->\n")
	existing := []byte("# Repo notes\n\nREPO-OWNED: top, written by the repo team.\n\n" +
		"<!-- STEERSPEC:BEGIN:e2e-rules -->\nmanaged rules v1, to be replaced.\n<!-- STEERSPEC:END:e2e-rules -->\n\n" +
		"REPO-OWNED: bottom, also kept.\n")
	want := "# Repo notes\n\nREPO-OWNED: top, written by the repo team.\n\n" +
		"<!-- STEERSPEC:BEGIN:e2e-rules -->\nMARKER-SOURCE: managed rules v2.\n<!-- STEERSPEC:END:e2e-rules -->\n\n" +
		"REPO-OWNED: bottom, also kept.\n" +
		"\n<!-- STEERSPEC:BEGIN:e2e-extra -->\nMARKER-SOURCE: new section.\n<!-- STEERSPEC:END:e2e-extra -->"

	got := strategyScenario(t, env, "sync-marker", "marker", "templates/e2e-marker.md", ".steerspec-e2e/marker.md",
		src, existing)
	if got != want {
		t.Errorf("marker result:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "to be replaced") {
		t.Errorf("old managed section survived:\n%s", got)
	}
	if rendered := string(renderTemplate(t, env, harness.RepoPublic, "sync-marker", src, existing)); got != rendered {
		t.Errorf("branch content differs from in-process render:\n got: %q\nwant: %q", got, rendered)
	}

	// Drift checks compare only the template's own sections (GH #133), so the state records their names and hash.
	names := []string{"e2e-extra", "e2e-rules"}
	sections, ok, err := template.SectionsHash([]byte(want), names)
	if err != nil || !ok {
		t.Fatalf("SectionsHash of the expected render: %v, %v", ok, err)
	}
	ts := env.ReadState(t).GetTemplateState(env.Org+"/"+harness.RepoPublic, env.TemplateID("sync-marker"))
	if ts == nil || !slices.Equal(ts.Sections, names) || ts.SectionsHash != sections ||
		ts.Hash != hash.HashBytes([]byte(want)) {
		t.Errorf("state = %+v, want sections %v, sections_hash %s and the whole-file hash", ts, names, sections)
	}
}

// TestSyncFullReplaceOverwrites: the full-replace strategy writes the template with {{var}} substitution and
// keeps nothing of the existing file.
func TestSyncFullReplaceOverwrites(t *testing.T) {
	env := harness.New(t)
	src := []byte("FULL-SOURCE: whole file owned by sync for {{org_name}} ({{e2e_marker}}).\n")
	existing := []byte("REPO-OWNED: this file was edited by hand.\n")
	want := "FULL-SOURCE: whole file owned by sync for " + env.Org + " (strspc-e2e).\n"

	got := strategyScenario(t, env, "sync-full", "full-replace", "templates/e2e-full.md", ".steerspec-e2e/full.md",
		src, existing)
	if got != want {
		t.Errorf("full-replace result:\n got: %q\nwant: %q", got, want)
	}
	if rendered := string(renderTemplate(t, env, harness.RepoPublic, "sync-full", src, existing)); got != rendered {
		t.Errorf("branch content differs from in-process render:\n got: %q\nwant: %q", got, rendered)
	}
}
