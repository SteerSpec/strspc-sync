package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `
version: "1.0.0"
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
	if cfg.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", cfg.Version)
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
	if cfg.Monitor.IssueLabel != "steerspec-drift" {
		t.Errorf("expected monitor issue-label steerspec-drift, got %s", cfg.Monitor.IssueLabel)
	}
	if cfg.Conflicts.IssueLabel != "steerspec-conflict" {
		t.Errorf("expected conflicts issue-label steerspec-conflict, got %s", cfg.Conflicts.IssueLabel)
	}
	if len(cfg.Conflicts.Tiers) != 2 || cfg.Conflicts.Tiers[0] != 1 || cfg.Conflicts.Tiers[1] != 2 {
		t.Errorf("expected default tiers [1,2], got %v", cfg.Conflicts.Tiers)
	}
	if cfg.Conflicts.SeverityThreshold != "warning" {
		t.Errorf("expected conflicts severity-threshold warning, got %q", cfg.Conflicts.SeverityThreshold)
	}
}

// A `schedule` key in the monitor or conflicts section is ignored (SYNCCFG-028 and -029, which supersede
// SYNCCFG-019 and -022): configs that still set it load, and the other settings of those sections apply.
func TestScheduleKeyIgnored(t *testing.T) {
	cfg := validConfig + `
monitor:
  schedule: "0 8 * * 1"
  issue-label: drift
conflicts:
  schedule: "not a cron expression"
  tiers: [1]
`
	got, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatalf("config with schedule keys must load: %v", err)
	}
	if got.Monitor.IssueLabel != "drift" {
		t.Errorf("expected monitor issue-label drift, got %s", got.Monitor.IssueLabel)
	}
	if len(got.Conflicts.Tiers) != 1 || got.Conflicts.Tiers[0] != 1 {
		t.Errorf("expected conflicts tiers [1], got %v", got.Conflicts.Tiers)
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
version: "1.0.0"
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

// TestValidationGithubAppFieldsOptional: a github-app config may omit app-id and private-key, which main reads from
// STEERSPEC_APP_ID / STEERSPEC_PRIVATE_KEY after Load; github.NewClient rejects them if still empty (see
// TestNewClient_AuthValidation in internal/github). This test used to expect an error and only got one from the missing template
// version (GH #175).
func TestValidationGithubAppFieldsOptional(t *testing.T) {
	cfg := `
version: "1.0.0"
auth:
  method: github-app
templates:
  - id: t1
    version: "1.0.0"
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
	path := writeConfig(t, cfg)
	if _, err := Load(path); err != nil {
		t.Fatalf("github-app without app-id in the config: %v", err)
	}
}

func TestValidationVersionSemver(t *testing.T) {
	for _, tt := range []struct {
		version string
		ok      bool
	}{
		{"1.0.0", true},
		{"2.10.3", true},
		{"0.0.0", true},
		{"10.20.30", true},
		{"1.0", false},
		{"1", false},
		{"v1.0.0", false},
		{"latest", false},
		// SemVer 2.0.0 §2: version numbers MUST NOT contain leading zeroes (GH #210).
		{"01.0.0", false},
		{"1.02.0", false},
		{"1.0.00", false},
		// The rules say MAJOR.MINOR.PATCH, so pre-release and build suffixes are rejected.
		{"1.0.0-rc.1", false},
		{"1.0.0+build", false},
	} {
		cfg := `
version: "` + tt.version + `"
auth:
  method: github-token
templates:
  - id: t1
    version: "1.0.0"
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
		_, err := Load(writeConfig(t, cfg))
		if (err == nil) != tt.ok {
			t.Errorf("version %q: err = %v, want ok=%v", tt.version, err, tt.ok)
		}
	}
}

func TestValidationPatRequiresToken(t *testing.T) {
	cfg := `
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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
version: "1.0.0"
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

// TestTMPLTVRS001_TemplateVersionSemver: a template version is MAJOR.MINOR.PATCH without leading zeros (GH #210).
func TestTMPLTVRS001_TemplateVersionSemver(t *testing.T) {
	for _, tt := range []struct {
		version string
		ok      bool
	}{
		{"1.0.0", true},
		{"0.10.0", true},
		{"01.0.0", false},
		{"1.00.0", false},
		{"1.0.01", false},
		{"1.0.0-beta", false},
		{"1.0.0+build", false},
	} {
		cfg := `
version: "1.0.0"
auth:
  method: github-token
templates:
  - id: t1
    version: "` + tt.version + `"
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
`
		_, err := Load(writeConfig(t, cfg))
		if (err == nil) != tt.ok {
			t.Errorf("template version %q: err = %v, want ok=%v", tt.version, err, tt.ok)
		}
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

// TestShippedExampleConfigsLoad guards the configs that ship with the repo.
// The quickstart config is the first thing the README points a new user at, and
// it silently stopped parsing once: a template carried a YAML list under
// `variables` (a map[string]string) and none of the templates declared the
// `version` field Validate requires. Loading them here means CI catches that
// rather than the user.
func TestShippedExampleConfigsLoad(t *testing.T) {
	paths := []string{
		"../../docs/quickstart/steerspec-sync.yml",
		"../../tests/fixtures/steerspec-sync.yml",
	}

	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("shipped config %s failed to load: %v", path, err)
			}
			if len(cfg.Templates) == 0 {
				t.Errorf("shipped config %s has no templates", path)
			}
		})
	}
}

// TestQuickstartTemplateSourcesExist keeps the quickstart honest. The quickstart
// directory models a central repo root: sync reads each template's `source`
// from the root of the central repo (internal/sync/sync.go, GetFileContent on
// tmpl.Source), so every `source` must exist relative to the directory that
// holds steerspec-sync.yml. The quickstart once referenced two template files
// that were never committed, and nothing caught it.
func TestQuickstartTemplateSourcesExist(t *testing.T) {
	configPath := "../../docs/quickstart/steerspec-sync.yml"
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("quickstart config failed to load: %v", err)
	}

	root := filepath.Dir(configPath)
	for _, tmpl := range cfg.Templates {
		t.Run(tmpl.ID, func(t *testing.T) {
			src := filepath.Join(root, tmpl.Source)
			info, err := os.Stat(src)
			if err != nil {
				t.Fatalf("template %q source %q does not exist under %s: %v", tmpl.ID, tmpl.Source, root, err)
			}
			if info.IsDir() {
				t.Fatalf("template %q source %q is a directory, want a file", tmpl.ID, tmpl.Source)
			}
			if tmpl.Type == "skill" && filepath.Base(tmpl.Destination) != "SKILL.md" {
				t.Errorf("template %q is a skill but destination %q is not a .claude/skills/<name>/SKILL.md path",
					tmpl.ID, tmpl.Destination)
			}
			if strings.Contains(tmpl.Destination, ".claude/settings/") {
				t.Errorf("template %q destination %q uses .claude/settings/; Claude Code reads .claude/settings.json",
					tmpl.ID, tmpl.Destination)
			}
		})
	}
}

// CNFLACT-006: conflicts.severity-threshold is critical, warning or info.
func TestConflictSeverityThreshold(t *testing.T) {
	const base = `
version: "1.0.0"
auth:
  method: pat
  token: "test"
templates:
  - id: t1
    version: "1.0.0"
    type: claude-md
    source: s
    destination: d
    strategy: mustache
targets:
  include: ["org/*"]
conflicts:
  severity-threshold: `
	for _, tc := range []struct {
		value   string
		wantErr bool
	}{{"critical", false}, {"warning", false}, {"info", false}, {"Critical", true}, {"error", true}} {
		cfg, err := Load(writeConfig(t, base+tc.value+"\n"))
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "severity-threshold") {
				t.Errorf("%q: got %v, want a severity-threshold error", tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.value, err)
			continue
		}
		if cfg.Conflicts.SeverityThreshold != tc.value {
			t.Errorf("%q: got %q", tc.value, cfg.Conflicts.SeverityThreshold)
		}
	}
}

// SYNCCFG-031: sync.pr-grouping is "template" (the default) or "repo".
func TestSyncPRGrouping(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.PRGrouping != PRGroupingTemplate {
		t.Errorf("default pr-grouping = %q, want %q", cfg.Sync.PRGrouping, PRGroupingTemplate)
	}
	for _, tc := range []struct {
		value   string
		wantErr bool
	}{{"template", false}, {"repo", false}, {"Repo", true}, {"org", true}} {
		cfg, err := Load(writeConfig(t, validConfig+"sync:\n  pr-grouping: "+tc.value+"\n"))
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "pr-grouping") {
				t.Errorf("%q: got %v, want a pr-grouping error", tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.value, err)
			continue
		}
		if cfg.Sync.PRGrouping != tc.value {
			t.Errorf("%q: got %q", tc.value, cfg.Sync.PRGrouping)
		}
	}
}

// A template id is part of its sync branch name (SYNCPR-002, SYNCPR-008), so it must be valid in a git ref.
func TestValidationTemplateIDIsRefSafe(t *testing.T) {
	for _, tc := range []struct {
		id      string
		wantErr bool
	}{
		{"claude-md", false},
		{"guide/team", false},
		{"repo", false},
		{"v1.2_x@y", false},
		{"two words", true},
		{"line\nbreak", true},
		{"a..b", true},
		{"a@{b", true},
		{"/lead", true},
		{"trail/", true},
		{"a//b", true},
		{".hidden", true},
		{"a/.hidden", true},
		{"x.lock", true},
		{"a:b", true},
		{"a~b", true},
		{"a^b", true},
		{"a?b", true},
		{"a*b", true},
		{"a[b", true},
		{`a\b`, true},
	} {
		err := checkTemplateID(tc.id)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkTemplateID(%q) = %v, wantErr %v", tc.id, err, tc.wantErr)
		}
	}
	bad := strings.Replace(validConfig, "id: claude-md", `id: "a b"`, 1)
	if _, err := Load(writeConfig(t, bad)); err == nil || !strings.Contains(err.Error(), "templates[0].id") {
		t.Errorf("Load with id %q: got %v, want a templates[0].id error", "a b", err)
	}
}
