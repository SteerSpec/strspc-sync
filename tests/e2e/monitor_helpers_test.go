//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

const (
	mcDriftLabel    = "steerspec-drift"
	mcConflictLabel = "steerspec-conflict"
	mcClaudePath    = "CLAUDE.md"
	mcWait          = 45 * time.Second
)

// mcSyncedContent is what sync renders for sync-basic (fixtures/templates/basic.mustache with org_name,
// e2e_marker and template_name filled in).
func mcSyncedContent(org string) []byte {
	return []byte("# " + org + " e2e CLAUDE.md\n\n" +
		"BASIC-SOURCE: managed by SteerSpec Sync template sync-basic (strspc-e2e).\n")
}

// mcSeedSynced puts every repo in a known-synced state without a sync PR: the rendered content is written to the
// repo's default branch and the deployment state in the central repo records its hash for the run's sync-basic
// template. Monitor and conflict then see the repos as in sync.
func mcSeedSynced(t *testing.T, env *harness.Env, repos ...string) {
	t.Helper()
	content := mcSyncedContent(env.Org)
	ds := state.NewDeploymentState()
	for _, repo := range repos {
		mcWriteAndWait(t, env, repo, mcClaudePath, content)
		ds.SetTemplateState(env.Org+"/"+repo, env.TemplateID("sync-basic"), state.TemplateState{
			Version:   "1.0.0",
			Hash:      hash.HashBytes(content),
			Timestamp: time.Now().UTC(),
			PRStatus:  "merged",
		})
	}
	data, err := ds.Save()
	if err != nil {
		t.Fatalf("encoding deployment state: %v", err)
	}
	env.WriteFile(t, harness.RepoCentral, harness.StatePath, data)
}

// mcWriteAndWait writes path and waits until a read returns the new content, so the binary that runs next does
// not see a stale copy.
func mcWriteAndWait(t *testing.T, env *harness.Env, repo, path string, content []byte) {
	t.Helper()
	env.WriteFile(t, repo, path, content)
	harness.Eventually(t, mcWait, func() bool {
		got, _, err := env.GetFile(t.Context(), repo, path, "")
		return err == nil && bytes.Equal(got, content)
	}, repo+"/"+path+" to show the written content")
}

// mcIssues returns the open issues of repo that carry label and whose title contains titleSub.
func mcIssues(t *testing.T, env *harness.Env, repo, label, titleSub string) []*gh.Issue {
	t.Helper()
	var out []*gh.Issue
	for _, is := range env.ListIssues(t, repo, label) {
		if strings.Contains(is.Title, titleSub) {
			out = append(out, is)
		}
	}
	return out
}

// mcWaitIssues waits until exactly want matching open issues exist and returns them.
func mcWaitIssues(t *testing.T, env *harness.Env, repo, label, titleSub string, want int) []*gh.Issue {
	t.Helper()
	var got []*gh.Issue
	harness.Eventually(t, mcWait, func() bool {
		got = mcIssues(t, env, repo, label, titleSub)
		return len(got) == want
	}, "open "+label+" issues in "+repo+" matching "+titleSub+" to number "+strconv.Itoa(want))
	return got
}

// mcAddTemplates rewrites the run config of env with extra templates appended, so scenarios can exercise features
// the shared fixture does not configure (skill and marker templates for conflict tier 2).
func mcAddTemplates(t *testing.T, env *harness.Env, extra ...config.TemplateConfig) {
	t.Helper()
	cfg, err := config.Load(env.ConfigPath)
	if err != nil {
		t.Fatalf("loading run config: %v", err)
	}
	cfg.Templates = append(cfg.Templates, extra...)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("encoding config: %v", err)
	}
	out := filepath.Join(t.TempDir(), "steerspec-sync.yml")
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	env.ConfigPath = out
}
