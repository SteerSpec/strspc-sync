//go:build e2e

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
)

func TestRunConfigRewritesFixture(t *testing.T) {
	fixture := filepath.Join("..", "fixtures", "steerspec-sync.yml")
	data, err := RunConfig(fixture, "42-1", "other-org")
	if err != nil {
		t.Fatalf("RunConfig: %v", err)
	}
	out := filepath.Join(t.TempDir(), "run.yml")
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("rewritten config must load: %v", err)
	}
	if cfg.CentralRepo != "other-org/strspc-e2e-central" {
		t.Errorf("central repo = %q", cfg.CentralRepo)
	}
	for _, inc := range cfg.Targets.Include {
		if !strings.HasPrefix(inc, "other-org/") {
			t.Errorf("include %q not rewritten", inc)
		}
	}
	ids := map[string]bool{}
	for _, tmpl := range cfg.Templates {
		ids[tmpl.ID] = true
		if strings.Contains(tmpl.Sources["private"], DefaultOrg) {
			t.Errorf("private source %q still names %s", tmpl.Sources["private"], DefaultOrg)
		}
	}
	for _, want := range []string{"e2e-42-1-sync-basic", "e2e-42-1-sync-visibility"} {
		if !ids[want] {
			t.Errorf("template id %q missing, got %v", want, ids)
		}
	}
}
