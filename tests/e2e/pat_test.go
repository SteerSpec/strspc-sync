//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// TestSyncPATCommitsSigned (GH #162): a sync that authenticates with auth.method github-token and a user's PAT, not
// the App, opens its PRs and writes the deployment state with commits GitHub verifies, so it passes the Signed Commit
// ruleset on the testbed. It runs only when E2E_PAT_TOKEN holds a token of a user with write access to the sync
// targets and the central repo; the harness still uses the App token for setup, assertions and cleanup.
func TestSyncPATCommitsSigned(t *testing.T) {
	token := os.Getenv("E2E_PAT_TOKEN")
	if token == "" {
		t.Skip("E2E_PAT_TOKEN not set; skipping the PAT sync scenario")
	}
	env := harness.New(t)
	seedSources(t, env)
	editConfig(t, env, func(c *config.SyncConfig) { c.Auth = config.AuthConfig{Method: "github-token"} })

	res := env.RunWithToken(t, token, "sync",
		"--template-filter", env.TemplateID(basicTemplateShortName),
		"--target-filter", targetFilter(env),
		"--force")
	if res.ParseErr == nil && res.Sync.Errors > 0 {
		t.Logf("a 401, 403 or 404 means the token cannot reach %s or lacks Contents and Pull requests write", env.Org)
	}
	requireSyncOK(t, res)
	if res.Sync.PRsCreated != len(syncTargets) {
		t.Fatalf("PRsCreated = %d, want %d; results: %+v", res.Sync.PRsCreated, len(syncTargets), res.Sync.RepoResults)
	}

	var user string
	for _, repo := range syncTargets {
		pr := onlyPR(t, env, repo, basicTemplateShortName)
		author := env.PRAuthor(t, repo, pr.Number)
		if author == "" {
			t.Fatalf("%s: PR #%d has no linked author", repo, pr.Number)
		}
		if strings.HasSuffix(author, "[bot]") {
			t.Errorf("%s: PR #%d opened by %s, want the PAT's user, not an App", repo, pr.Number, author)
		}
		if user == "" {
			user = author
		} else if author != user {
			t.Errorf("%s: PR #%d opened by %s, want %s", repo, pr.Number, author, user)
		}
		if bad := env.UnverifiedPRCommits(t, repo, pr.Number); len(bad) > 0 {
			t.Errorf("%s: PR #%d has unsigned commits: %v", repo, pr.Number, bad)
		}
	}

	ds := env.ReadState(t)
	for _, repo := range syncTargets {
		if ds.GetTemplateState(env.Org+"/"+repo, env.TemplateID(basicTemplateShortName)) == nil {
			t.Errorf("%s: no deployment state entry after the PAT sync", repo)
		}
	}
	c := env.LatestCommit(t, harness.RepoCentral, harness.StatePath)
	if c.Author != user {
		t.Errorf("state commit %s authored by %q, want the PAT's user %q", c.SHA, c.Author, user)
	}
	if !c.Verified {
		t.Errorf("state commit %s is not verified: %s", c.SHA, c.Reason)
	}
}
