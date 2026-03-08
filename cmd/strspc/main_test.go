package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalConfig is a valid config that passes validation but has no real targets.
// newGHClient will fail because GITHUB_TOKEN is empty, so commands requiring
// a GitHub client will return exit code 1 without making any API calls.
const minimalConfig = `version: "1"
auth:
  method: github-token
templates:
  - id: test-template
    type: custom
    strategy: full-replace
    source: nonexistent.txt
    destination: nonexistent.txt
targets:
  include:
    - org/placeholder
`

func writeTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "steerspec-sync.yml")
	if err := os.WriteFile(path, []byte(minimalConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- run() dispatch tests ----

func TestRunNoArgs(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for no args, got %d", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("expected usage in stderr, got %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Errorf("expected exit 0 for version, got %d", code)
	}
	if !strings.Contains(out.String(), "strspc") {
		t.Errorf("expected 'strspc' in version output, got %q", out.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			var out, errOut strings.Builder
			code := run([]string{arg}, &out, &errOut)
			if code != 0 {
				t.Errorf("expected exit 0 for %s, got %d", arg, code)
			}
			if !strings.Contains(errOut.String(), "Usage:") {
				t.Errorf("expected usage in stderr for %s, got %q", arg, errOut.String())
			}
		})
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{"notacommand"}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for unknown command, got %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("expected 'unknown command' in stderr, got %q", errOut.String())
	}
}

// ---- run() with sub-commands: fail cleanly on missing GitHub token ----

func TestRunSyncMissingToken(t *testing.T) {
	cfg := writeTempConfig(t)
	t.Setenv("GITHUB_TOKEN", "")
	var out, errOut strings.Builder
	code := run([]string{"sync", "--config", cfg, "--dry-run"}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for sync with no token, got %d", code)
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Errorf("expected token error in stderr, got %q", errOut.String())
	}
}

func TestRunMonitorMissingToken(t *testing.T) {
	cfg := writeTempConfig(t)
	t.Setenv("GITHUB_TOKEN", "")
	var out, errOut strings.Builder
	code := run([]string{"monitor", "--config", cfg}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for monitor with no token, got %d", code)
	}
}

func TestRunConflictMissingToken(t *testing.T) {
	cfg := writeTempConfig(t)
	t.Setenv("GITHUB_TOKEN", "")
	var out, errOut strings.Builder
	code := run([]string{"conflict", "--config", cfg}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for conflict with no token, got %d", code)
	}
}

func TestRunSyncMissingConfig(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{"sync", "--config", "/nonexistent/path.yml"}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for missing config, got %d", code)
	}
	if !strings.Contains(errOut.String(), "error loading config") {
		t.Errorf("expected config error in stderr, got %q", errOut.String())
	}
}

func TestRunConflictInvalidTier(t *testing.T) {
	cfg := writeTempConfig(t)
	t.Setenv("GITHUB_TOKEN", "")
	var out, errOut strings.Builder
	code := run([]string{"conflict", "--config", cfg, "--tiers", "notanumber"}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected exit 1 for invalid tier, got %d", code)
	}
}

// ---- lower-level unit tests ----

func TestParseCommonFlags_Defaults(t *testing.T) {
	f, remaining := parseCommonFlags([]string{})
	if f.configPath != "steerspec-sync.yml" {
		t.Errorf("expected default configPath steerspec-sync.yml, got %s", f.configPath)
	}
	if f.targetFilter != "" {
		t.Errorf("expected empty targetFilter, got %s", f.targetFilter)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
}

func TestParseCommonFlags_CustomConfig(t *testing.T) {
	f, _ := parseCommonFlags([]string{"--config", "custom.yml"})
	if f.configPath != "custom.yml" {
		t.Errorf("expected configPath custom.yml, got %s", f.configPath)
	}
}

func TestParseCommonFlags_TargetFilter(t *testing.T) {
	f, _ := parseCommonFlags([]string{"--target-filter", "org/*"})
	if f.targetFilter != "org/*" {
		t.Errorf("expected targetFilter org/*, got %s", f.targetFilter)
	}
}

func TestParseCommonFlags_UnknownFlagsPassThrough(t *testing.T) {
	f, remaining := parseCommonFlags([]string{"--dry-run", "--force"})
	if f.configPath != "steerspec-sync.yml" {
		t.Errorf("expected default configPath, got %s", f.configPath)
	}
	if len(remaining) != 2 || remaining[0] != "--dry-run" || remaining[1] != "--force" {
		t.Errorf("expected remaining [--dry-run --force], got %v", remaining)
	}
}

func TestDetectTrigger_Push(t *testing.T) {
	t.Setenv("GITHUB_EVENT_NAME", "push")
	if got := detectTrigger(); got != "push" {
		t.Errorf("expected push, got %s", got)
	}
}

func TestDetectTrigger_Schedule(t *testing.T) {
	t.Setenv("GITHUB_EVENT_NAME", "schedule")
	if got := detectTrigger(); got != "schedule" {
		t.Errorf("expected schedule, got %s", got)
	}
}

func TestDetectTrigger_WorkflowDispatch(t *testing.T) {
	t.Setenv("GITHUB_EVENT_NAME", "workflow_dispatch")
	if got := detectTrigger(); got != "manual" {
		t.Errorf("expected manual, got %s", got)
	}
}

func TestDetectTrigger_Default(t *testing.T) {
	t.Setenv("GITHUB_EVENT_NAME", "")
	if got := detectTrigger(); got != "manual" {
		t.Errorf("expected manual, got %s", got)
	}
}

func TestSetOutput(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "github-output-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	t.Setenv("GITHUB_OUTPUT", tmp.Name())
	setOutput("prs-created", "3")

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "prs-created=3") {
		t.Errorf("expected prs-created=3 in output, got %q", string(data))
	}
}

func TestDetectCentralRepo_Success(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "myorg/myrepo")
	owner, repo, err := detectCentralRepo(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" || repo != "myrepo" {
		t.Errorf("expected myorg/myrepo, got %s/%s", owner, repo)
	}
}

func TestDetectCentralRepo_Missing(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	_, _, err := detectCentralRepo(nil)
	if err == nil {
		t.Error("expected error when GITHUB_REPOSITORY is empty")
	}
}

func TestPrintJSON(t *testing.T) {
	var buf strings.Builder
	printJSON(map[string]int{"count": 42}, &buf)
	if !strings.Contains(buf.String(), `"count"`) {
		t.Errorf("expected JSON output with 'count', got %q", buf.String())
	}
}

func TestLoadConfig_Missing(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yml")
	if err == nil {
		t.Error("expected error for missing config")
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	path := writeTempConfig(t)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Auth.Method != "github-token" {
		t.Errorf("expected github-token auth, got %s", cfg.Auth.Method)
	}
}
