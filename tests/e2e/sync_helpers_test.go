//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/template"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

const (
	basicTemplatePath      = "templates/basic.mustache"
	publicTemplatePath     = "templates/visibility.public.mustache"
	privateTemplatePath    = "templates/visibility.private.mustache"
	basicDestination       = "CLAUDE.md"
	syncVersion            = "1.0.0"
	basicTemplateShortName = "sync-basic"
)

// syncTargets are the repositories the sync scenarios touch. The symlink and develop repos belong to the edge-case
// scenarios and are never targeted here.
var syncTargets = []string{harness.RepoPublic, harness.RepoPrivate}

// fixture reads a file below tests/e2e/fixtures (go test runs in the package directory).
func fixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("fixtures", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", rel, err)
	}
	return b
}

// seedSources makes the central repo and the second source repo hold the fixture templates the sync reads.
func seedSources(t *testing.T, env *harness.Env) {
	t.Helper()
	env.ResetFile(t, harness.RepoCentral, basicTemplatePath, fixture(t, "templates/basic.mustache"))
	env.ResetFile(t, harness.RepoCentral, publicTemplatePath, fixture(t, "templates/visibility.public.mustache"))
	env.ResetFile(t, harness.RepoSrc, privateTemplatePath, fixture(t, "src-repo/templates/visibility.private.mustache"))
}

// targetFilter matches exactly the sync targets (strspc-e2e-public and strspc-e2e-private). It is a path.Match glob
// over org/repo, and "p*" excludes strspc-e2e-symlink and strspc-e2e-develop.
func targetFilter(env *harness.Env) string { return env.Org + "/strspc-e2e-p*" }

// runBasicSync runs `sync` for template sync-basic on the sync targets only.
func runBasicSync(t *testing.T, env *harness.Env, extra ...string) *harness.Result {
	t.Helper()
	args := append([]string{
		"--template-filter", env.TemplateID(basicTemplateShortName),
		"--target-filter", targetFilter(env),
	}, extra...)
	return env.Run(t, "sync", args...)
}

// seedAndForceSync seeds the sources and runs the first forced sync, requiring it to succeed and open a PR per target.
func seedAndForceSync(t *testing.T, env *harness.Env) *harness.Result {
	t.Helper()
	seedSources(t, env)
	res := runBasicSync(t, env, "--force")
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != len(syncTargets) {
		t.Fatalf("PRsCreated = %d, want %d; results: %+v", res.Sync.PRsCreated, len(syncTargets), res.Sync.RepoResults)
	}
	return res
}

// requireSyncOK fails t unless the run exited 0 with a parsed summary and no errors.
func requireSyncOK(t *testing.T, res *harness.Result) {
	t.Helper()
	if res.ParseErr != nil {
		t.Fatalf("no sync summary: %v\nstdout: %s\nstderr: %s", res.ParseErr, res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 || res.Sync.Errors != 0 {
		t.Fatalf("sync exit=%d errors=%d, want 0/0: %+v", res.ExitCode, res.Sync.Errors, res.Sync.RepoResults)
	}
}

// branchFor is the head branch of the sync PR for the sync-basic template.
func branchFor(env *harness.Env) string {
	return "steerspec-sync/" + env.TemplateID(basicTemplateShortName) + "/" + syncVersion
}

// basicPRs returns the open sync-basic PRs of repo.
func basicPRs(t *testing.T, env *harness.Env, repo string) []*gh.PullRequest {
	t.Helper()
	return env.PRsWithPrefix(t, repo, "steerspec-sync/"+env.TemplateID(basicTemplateShortName)+"/")
}

// renderBasic renders the sync-basic template for repo exactly as the binary does: global variables, then the
// template's variables, then the built-ins.
func renderBasic(t *testing.T, env *harness.Env, repo string) []byte {
	t.Helper()
	cfg, err := config.Load(env.ConfigPath)
	if err != nil {
		t.Fatalf("loading run config: %v", err)
	}
	id := env.TemplateID(basicTemplateShortName)
	vars := map[string]string{}
	var strategy string
	for k, v := range cfg.Variables {
		vars[k] = v
	}
	found := false
	for _, tm := range cfg.Templates {
		if tm.ID != id {
			continue
		}
		found = true
		strategy = tm.Strategy
		for k, v := range tm.Variables {
			vars[k] = v
		}
	}
	if !found {
		t.Fatalf("template %s not in run config", id)
	}
	vars["repo_name"] = repo
	vars["repo_owner"] = env.Org
	vars["repo_full_name"] = env.Org + "/" + repo
	out, err := template.Render(template.Strategy(strategy), fixture(t, "templates/basic.mustache"), nil, vars)
	if err != nil {
		t.Fatalf("rendering basic template: %v", err)
	}
	return out
}

// closePR closes a pull request without merging it.
func closePR(t *testing.T, env *harness.Env, repo string, number int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := env.Client.PullRequests.Close(ctx, env.Org, repo, number); err != nil {
		t.Fatalf("closing %s#%d: %v", repo, number, err)
	}
}

// resultFor returns the sync result of repo for the sync-basic template, failing t when absent.
func resultFor(t *testing.T, env *harness.Env, res *harness.Result, repo string) (status, errText string, pr int) {
	t.Helper()
	rr, ok := res.RepoResultFor(repo, env.TemplateID(basicTemplateShortName))
	if !ok {
		t.Fatalf("no sync result for %s: %+v", repo, res.Sync.RepoResults)
	}
	return rr.Status, rr.Error, rr.PRNumber
}

func containsAll(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s does not contain %q:\n%s", what, sub, s)
		}
	}
}
