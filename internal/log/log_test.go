package log

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"", slog.LevelInfo, false},
		{"bogus", slog.LevelInfo, true},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseLevel(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"auto", FormatAuto, false},
		{"JSON", FormatJSON, false},
		{"text", FormatText, false},
		{"", FormatAuto, false},
		{"yaml", FormatAuto, true},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseFormat(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseFormat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestInit_JSONHandler(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, slog.LevelInfo, FormatJSON)
	L().Info("hello", "repo", "acme/foo", "count", 3)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", buf.String(), err)
	}
	if record["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", record["msg"])
	}
	if record["repo"] != "acme/foo" {
		t.Errorf("repo = %v, want acme/foo", record["repo"])
	}
	if record["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", record["level"])
	}
}

func TestInit_TextHandler(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, slog.LevelInfo, FormatText)
	L().Info("hello", "repo", "acme/foo")

	out := buf.String()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "repo=acme/foo") {
		t.Errorf("text handler output missing expected fields: %q", out)
	}
	// Text handler output is not valid JSON.
	if json.Valid(buf.Bytes()) {
		t.Errorf("expected text output, got JSON: %q", out)
	}
}

func TestInit_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, slog.LevelWarn, FormatText)
	L().Info("should be filtered")
	L().Warn("should appear")

	out := buf.String()
	if strings.Contains(out, "should be filtered") {
		t.Errorf("info-level line leaked past warn threshold: %q", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("warn-level line missing: %q", out)
	}
}

// TestInit_FormatAutoInCI locks in that FormatAuto selects the JSON handler
// when GITHUB_ACTIONS=true, so GitHub Actions runners get machine-parseable
// stderr without callers having to pass --log-format=json.
func TestInit_FormatAutoInCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var buf bytes.Buffer
	InitTo(&buf, slog.LevelInfo, FormatAuto)
	L().Info("hello", "repo", "acme/foo")

	if !json.Valid(buf.Bytes()) {
		t.Errorf("expected JSON handler under GITHUB_ACTIONS=true, got %q", buf.String())
	}
}

// TestInit_FormatAutoLocal locks in that FormatAuto falls back to the text
// handler when GITHUB_ACTIONS is unset, giving human-readable output during
// local development.
func TestInit_FormatAutoLocal(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	var buf bytes.Buffer
	InitTo(&buf, slog.LevelInfo, FormatAuto)
	L().Info("hello", "repo", "acme/foo")

	if json.Valid(buf.Bytes()) {
		t.Errorf("expected text handler with GITHUB_ACTIONS unset, got JSON %q", buf.String())
	}
	if !strings.Contains(buf.String(), "repo=acme/foo") {
		t.Errorf("text handler output missing repo field: %q", buf.String())
	}
}
