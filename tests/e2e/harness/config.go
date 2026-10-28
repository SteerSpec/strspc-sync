//go:build e2e

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/SteerSpec/strspc-sync/internal/config"
)

// RunConfig loads the fixture config at path and returns it rewritten for one run: every template id becomes
// e2e-<runID>-<id>, and org replaces DefaultOrg in the central repo, the target patterns and the owner/repo:path
// sources. The fixture itself is never modified.
func RunConfig(path, runID, org string) ([]byte, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading fixture config: %w", err)
	}
	cfg.CentralRepo = strings.ReplaceAll(cfg.CentralRepo, DefaultOrg+"/", org+"/")
	for i := range cfg.Targets.Include {
		cfg.Targets.Include[i] = strings.ReplaceAll(cfg.Targets.Include[i], DefaultOrg+"/", org+"/")
	}
	for i := range cfg.Templates {
		t := &cfg.Templates[i]
		t.ID = templateID(runID, t.ID)
		t.Source = strings.ReplaceAll(t.Source, DefaultOrg+"/", org+"/")
		for vis, src := range t.Sources {
			t.Sources[vis] = strings.ReplaceAll(src, DefaultOrg+"/", org+"/")
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encoding run config: %w", err)
	}
	return data, nil
}

// writeRunConfig writes the run config into dir and returns its path.
func (e *Env) writeRunConfig(dir string) (string, error) {
	data, err := RunConfig(e.ConfigPath, e.RunID, e.Org)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "steerspec-sync.yml")
	if err := os.WriteFile(out, data, 0o600); err != nil {
		return "", fmt.Errorf("writing run config: %w", err)
	}
	return out, nil
}
