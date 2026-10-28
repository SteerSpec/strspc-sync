//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

const (
	visibilityTemplateShortName = "sync-visibility"
	visibilityDestination       = ".steerspec-e2e/visibility.md"
	developBranch               = "develop"
)

// branchExists reports whether repo has a branch of that name.
func branchExists(t *testing.T, env *harness.Env, repo, branch string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err := env.Client.Repos.GetBranchSHA(ctx, env.Org, repo, branch)
	if err == nil {
		return true
	}
	if harness.IsNotFound(err) {
		return false
	}
	t.Fatalf("looking up branch %s of %s: %v", branch, repo, err)
	return false
}

// isSymlink reports whether path on repo's default branch is a git symlink (mode 120000).
func isSymlink(t *testing.T, env *harness.Env, repo, path string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	link, err := env.Client.Repos.IsSymlink(ctx, env.Org, repo, path, "")
	if err != nil {
		t.Fatalf("IsSymlink %s/%s: %v", repo, path, err)
	}
	return link
}

// TestSyncSkipsSymlinkedClaudeMd: a destination that is a symlink on the default branch is skipped with a reason, no
// branch or PR is created, and the link is left intact (#82).
func TestSyncSkipsSymlinkedClaudeMd(t *testing.T) {
	env := harness.New(t)
	repo := harness.RepoSymlink
	if !isSymlink(t, env, repo, basicDestination) {
		t.Fatalf("fixture: %s/%s is not a symlink (mode 120000); the owner must pre-seed it", repo, basicDestination)
	}

	seedSources(t, env)
	res := env.Run(t, "sync", "--force",
		"--template-filter", env.TemplateID(basicTemplateShortName),
		"--target-filter", env.Org+"/"+repo)
	requireSyncOK(t, res)

	status, errText, number := resultFor(t, env, res, repo)
	if status != "skipped" {
		t.Errorf("status = %q, want skipped (error %q)", status, errText)
	}
	containsAll(t, "skip reason", errText, "is a symlink")
	if number != 0 {
		t.Errorf("PR number = %d, want 0", number)
	}
	if res.Sync.PRsCreated != 0 || res.Sync.PRsUpdated != 0 {
		t.Errorf("PRsCreated=%d PRsUpdated=%d, want 0/0", res.Sync.PRsCreated, res.Sync.PRsUpdated)
	}

	if prs := env.PRsWithPrefixState(t, repo, "all", "steerspec-sync/"); len(prs) != 0 {
		t.Errorf("%d sync PRs in %s, want none: %+v", len(prs), repo, prs)
	}
	if branchExists(t, env, repo, branchFor(env)) {
		t.Errorf("branch %s exists in %s, want none", branchFor(env), repo)
	}
	if !isSymlink(t, env, repo, basicDestination) {
		t.Errorf("%s/%s is no longer a symlink", repo, basicDestination)
	}
}

// TestSyncUsesNonMainDefaultBranch: for a repo whose default branch is develop, the PR targets develop and the sync
// branch carries the rendered file.
func TestSyncUsesNonMainDefaultBranch(t *testing.T) {
	env := harness.New(t)
	repo := harness.RepoDevelop

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	def, err := env.Client.Repos.GetDefaultBranch(ctx, env.Org, repo)
	if err != nil {
		t.Fatalf("default branch of %s: %v", repo, err)
	}
	if def != developBranch {
		t.Fatalf("fixture: default branch of %s is %q, want %q", repo, def, developBranch)
	}
	tipBefore, err := env.Client.Repos.GetBranchSHA(ctx, env.Org, repo, developBranch)
	if err != nil {
		t.Fatalf("tip of %s/%s: %v", repo, developBranch, err)
	}

	seedSources(t, env)
	res := env.Run(t, "sync", "--force",
		"--template-filter", env.TemplateID(basicTemplateShortName),
		"--target-filter", env.Org+"/"+repo)
	requireSyncOK(t, res)

	status, errText, number := resultFor(t, env, res, repo)
	if status != "created" || errText != "" || number == 0 {
		t.Fatalf("status=%q error=%q pr=%d, want created/empty/non-zero", status, errText, number)
	}

	prs := basicPRs(t, env, repo)
	if len(prs) != 1 {
		t.Fatalf("%d open sync-basic PRs in %s, want 1", len(prs), repo)
	}
	pr := prs[0]
	if pr.Number != number {
		t.Errorf("PR #%d, summary says #%d", pr.Number, number)
	}
	if pr.Base != developBranch {
		t.Errorf("PR base = %q, want %q", pr.Base, developBranch)
	}
	if pr.Head != branchFor(env) {
		t.Errorf("PR head = %q, want %q", pr.Head, branchFor(env))
	}
	onBranch := string(env.ReadFileAt(t, repo, basicDestination, pr.Head))
	containsAll(t, "CLAUDE.md on "+pr.Head, onBranch, "BASIC-SOURCE")
	if want := string(renderBasic(t, env, repo)); onBranch != want {
		t.Errorf("branch content differs from in-process render:\n got: %q\nwant: %q", onBranch, want)
	}

	// Nothing is merged and nothing else is pushed to develop; the sync branch is the only new ref.
	tipAfter, err := env.Client.Repos.GetBranchSHA(ctx, env.Org, repo, developBranch)
	if err != nil {
		t.Fatalf("tip of %s/%s after sync: %v", repo, developBranch, err)
	}
	if tipAfter != tipBefore {
		t.Errorf("%s tip moved from %s to %s", developBranch, tipBefore, tipAfter)
	}
}

// TestSyncPerVisibilitySources: public targets render the central source, private targets the second source repo,
// and only the private PR links that repo (#80).
func TestSyncPerVisibilitySources(t *testing.T) {
	env := harness.New(t)
	seedSources(t, env)

	tmplID := env.TemplateID(visibilityTemplateShortName)
	res := env.Run(t, "sync", "--force",
		"--template-filter", tmplID,
		"--target-filter", targetFilter(env))
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != len(syncTargets) {
		t.Fatalf("PRsCreated = %d, want %d: %+v", res.Sync.PRsCreated, len(syncTargets), res.Sync.RepoResults)
	}

	ds := env.ReadState(t)
	hashes := map[string]string{}
	cases := []struct {
		repo         string
		want, reject string
	}{
		{harness.RepoPublic, "PUBLIC-SOURCE", "PRIVATE-SOURCE"},
		{harness.RepoPrivate, "PRIVATE-SOURCE", "PUBLIC-SOURCE"},
	}
	for _, c := range cases {
		rr, ok := res.RepoResultFor(c.repo, tmplID)
		if !ok || rr.Status != "created" || rr.Error != "" || rr.PRNumber == 0 {
			t.Fatalf("%s: result %+v (found=%v), want created without error", c.repo, rr, ok)
		}
		prs := env.PRsWithPrefix(t, c.repo, "steerspec-sync/"+tmplID+"/")
		if len(prs) != 1 {
			t.Fatalf("%s: %d open sync-visibility PRs, want 1", c.repo, len(prs))
		}
		pr := prs[0]

		content := string(env.ReadFileAt(t, c.repo, visibilityDestination, pr.Head))
		containsAll(t, c.repo+" "+visibilityDestination, content, c.want)
		if strings.Contains(content, c.reject) {
			t.Errorf("%s: %s contains %q:\n%s", c.repo, visibilityDestination, c.reject, content)
		}

		switch c.repo {
		case harness.RepoPrivate:
			containsAll(t, c.repo+" PR body", pr.Body, harness.RepoSrc, "templates/visibility.private.mustache")
		default:
			containsAll(t, c.repo+" PR body", pr.Body, harness.RepoCentral, "templates/visibility.public.mustache")
			if strings.Contains(pr.Body, harness.RepoSrc) {
				t.Errorf("%s: PR body mentions %s:\n%s", c.repo, harness.RepoSrc, pr.Body)
			}
		}

		ts := ds.GetTemplateState(env.Org+"/"+c.repo, tmplID)
		if ts == nil {
			t.Errorf("%s: no deployment state entry for %s", c.repo, tmplID)
			continue
		}
		if want := hash.HashBytes([]byte(content)); ts.Hash != want {
			t.Errorf("%s: state hash %s, want hash of branch content %s", c.repo, ts.Hash, want)
		}
		hashes[c.repo] = ts.Hash
	}
	if len(hashes) == len(cases) && hashes[harness.RepoPublic] == hashes[harness.RepoPrivate] {
		t.Errorf("public and private state hashes are both %s, want different", hashes[harness.RepoPublic])
	}
}
