package main

import (
	"os"
	"strings"
	"testing"
)

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
