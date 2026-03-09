package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validConfig = `
version: "1.0"
auth:
  method: pat
  token: "ghp_test123"
templates:
  - id: claude-md
    version: "1.0.0"
    type: claude-md
    source: templates/CLAUDE.md
    destination: CLAUDE.md
    strategy: mustache
targets:
  include:
    - "myorg/*"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", cfg.Version)
	}
	if cfg.Auth.Method != "pat" {
		t.Errorf("expected auth method pat, got %s", cfg.Auth.Method)
	}
	if len(cfg.Templates) != 1 {
		t.Errorf("expected 1 template, got %d", len(cfg.Templates))
	}
}

func TestDefaultsApplied(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sync.Concurrency != 5 {
		t.Errorf("expected concurrency 5, got %d", cfg.Sync.Concurrency)
	}
	if cfg.Sync.PRLabel != "steerspec-sync" {
		t.Errorf("expected pr-label steerspec-sync, got %s", cfg.Sync.PRLabel)
	}
	if cfg.Sync.CloseStalePRs == nil || !*cfg.Sync.CloseStalePRs {
		t.Error("expected close-stale-prs to default to true")
	}
	if cfg.Monitor.Schedule != "0 8 * * 1" {
		t.Errorf("expected monitor schedule '0 8 * * 1', got %s", cfg.Monitor.Schedule)
	}
	if cfg.Monitor.IssueLabel != "steerspec-drift" {
		t.Errorf("expected monitor issue-label steerspec-drift, got %s", cfg.Monitor.IssueLabel)
	}
	if cfg.Conflicts.IssueLabel != "steerspec-conflict" {
		t.Errorf("expected conflicts issue-label steerspec-conflict, got %s", cfg.Conflicts.IssueLabel)
	}
	if len(cfg.Conflicts.Tiers) != 2 || cfg.Conflicts.Tiers[0] != 1 || cfg.Conflicts.Tiers[1] != 2 {
		t.Errorf("expected default tiers [1,2], got %v", cfg.Conflicts.Tiers)
	}
}

func TestValidationMissingVersion(t *testing.T) {
	cfg := `
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestValidationInvalidAuthMethod(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: invalid
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid auth method")
	}
}

func TestValidationGithubAppRequiresFields(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: github-app
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for github-app without app-id")
	}
}

func TestValidationPatRequiresToken(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for pat without token")
	}
}

func TestValidationNoTemplates(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates: []
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty templates")
	}
}

func TestValidationDuplicateTemplateID(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
  - id: t1
    type: skill
    source: s2
    destination: d2
    strategy: marker
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate template id")
	}
}

func TestValidationInvalidTemplateType(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: invalid-type
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid template type")
	}
}

func TestValidationInvalidStrategy(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: bad-strategy
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid strategy")
	}
}

func TestValidationEmptyIncludes(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: []
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty includes")
	}
}

func TestValidationInvalidConflictTier(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
conflicts:
  tiers: [1, 4]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid conflict tier")
	}
}

func TestValidationTemplateVersionRequired(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing template version")
	}
}

func TestValidationTemplateVersionInvalidFormat(t *testing.T) {
	cfg := `
version: "1.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    version: "not-semver"
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid template version format")
	}
}

func TestTemplateVersionField(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Templates[0].Version != "1.0.0" {
		t.Errorf("expected template version 1.0.0, got %s", cfg.Templates[0].Version)
	}
}
