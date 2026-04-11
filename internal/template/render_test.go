package template

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderMustache(t *testing.T) {
	tmpl := []byte("Hello {{name}}, welcome to {{org}}!")
	vars := map[string]string{
		"name": "Alice",
		"org":  "Acme",
	}

	out, err := Render(StrategyMustache, tmpl, nil, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Hello Alice, welcome to Acme!"
	if string(out) != expected {
		t.Errorf("got %q, want %q", string(out), expected)
	}
}

func TestRenderMarkerReplacesSection(t *testing.T) {
	existing := []byte(strings.Join([]string{
		"# My File",
		"Some intro text",
		"<!-- STEERSPEC:BEGIN:standards -->",
		"old standards content",
		"<!-- STEERSPEC:END:standards -->",
		"Footer text",
	}, "\n"))

	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:standards -->",
		"new standards content",
		"line two",
		"<!-- STEERSPEC:END:standards -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := string(out)
	if !strings.Contains(result, "new standards content") {
		t.Error("expected new standards content")
	}
	if !strings.Contains(result, "line two") {
		t.Error("expected line two in output")
	}
	if strings.Contains(result, "old standards content") {
		t.Error("old standards content should be replaced")
	}
	if !strings.Contains(result, "Some intro text") {
		t.Error("unmanaged content should be preserved")
	}
	if !strings.Contains(result, "Footer text") {
		t.Error("footer should be preserved")
	}
}

func TestRenderMarkerNoExisting(t *testing.T) {
	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:standards -->",
		"content here",
		"<!-- STEERSPEC:END:standards -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Equal(out, template) {
		t.Errorf("got %q, want %q", string(out), string(template))
	}
}

func TestRenderMarkerEmptyExisting(t *testing.T) {
	template := []byte("<!-- STEERSPEC:BEGIN:s -->\ncontent\n<!-- STEERSPEC:END:s -->")

	out, err := Render(StrategyMarker, template, []byte{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Equal(out, template) {
		t.Errorf("got %q, want %q", string(out), string(template))
	}
}

// TestRenderMarkerPreservesCRLF verifies that when the existing target file
// uses CRLF line endings, the rendered output preserves CRLF throughout —
// including across the section we replaced, the unmanaged preamble/footer,
// and any newly appended sections. The template itself is authored with LF
// (the realistic case — templates are stored in the central repo) and must
// be normalized up to CRLF so marker extraction still finds its sections.
func TestRenderMarkerPreservesCRLF(t *testing.T) {
	existing := []byte(strings.Join([]string{
		"# My File",
		"Some intro text",
		"<!-- STEERSPEC:BEGIN:standards -->",
		"old standards content",
		"<!-- STEERSPEC:END:standards -->",
		"Footer text",
	}, "\r\n"))

	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:standards -->",
		"new standards content",
		"line two",
		"<!-- STEERSPEC:END:standards -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The replaced section's inner content must be present and CRLF-joined.
	if !bytes.Contains(out, []byte("new standards content\r\nline two")) {
		t.Errorf("expected CRLF-joined new section body, got %q", string(out))
	}
	// Unmanaged preamble and footer must still be CRLF-terminated.
	if !bytes.Contains(out, []byte("# My File\r\nSome intro text\r\n")) {
		t.Errorf("preamble lost CRLF, got %q", string(out))
	}
	if !bytes.Contains(out, []byte("<!-- STEERSPEC:END:standards -->\r\nFooter text")) {
		t.Errorf("footer lost CRLF, got %q", string(out))
	}
	// No bare LF should have crept in.
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Errorf("output contains stray LF after stripping CRLF: %q", string(out))
	}
	if bytes.Contains(out, []byte("old standards content")) {
		t.Error("old standards content should be replaced")
	}
}

// TestRenderMarkerAppendsWithCRLF covers the "section not present in existing"
// branch at marker.go's append path: the new section must be separated from
// the existing content with CRLF, not a bare LF.
func TestRenderMarkerAppendsWithCRLF(t *testing.T) {
	existing := []byte("# Header\r\nbody line\r\n")
	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:new -->",
		"brand new content",
		"<!-- STEERSPEC:END:new -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The separator between existing and appended section must be CRLF.
	if !bytes.Contains(out, []byte("body line\r\n\r\n<!-- STEERSPEC:BEGIN:new -->")) {
		t.Errorf("appended section not separated by CRLF: %q", string(out))
	}
	if !bytes.Contains(out, []byte("<!-- STEERSPEC:BEGIN:new -->\r\nbrand new content\r\n<!-- STEERSPEC:END:new -->")) {
		t.Errorf("appended section body not CRLF-joined: %q", string(out))
	}
}

// TestRenderMarkerMixedEndingsPrefersDominant asserts that when existing
// content has mixed line endings, the dominant style wins.
func TestRenderMarkerMixedEndingsPrefersDominant(t *testing.T) {
	// 3 CRLFs vs 1 LF — CRLF wins.
	existing := []byte("# H\r\na\r\nb\r\n<!-- STEERSPEC:BEGIN:s -->\nold\n<!-- STEERSPEC:END:s -->\r\nfooter")
	template := []byte("<!-- STEERSPEC:BEGIN:s -->\nnew\n<!-- STEERSPEC:END:s -->")

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(out, []byte("<!-- STEERSPEC:BEGIN:s -->\r\nnew\r\n<!-- STEERSPEC:END:s -->")) {
		t.Errorf("expected CRLF-joined replacement under dominant CRLF, got %q", string(out))
	}
}

// TestRenderMarkerLFRegression locks in that pure-LF existing content keeps
// LF endings end-to-end (no regression from the CRLF fix).
func TestRenderMarkerLFRegression(t *testing.T) {
	existing := []byte("# Header\nbody\n<!-- STEERSPEC:BEGIN:s -->\nold\n<!-- STEERSPEC:END:s -->\nfooter\n")
	template := []byte("<!-- STEERSPEC:BEGIN:s -->\nnew\n<!-- STEERSPEC:END:s -->")

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bytes.Contains(out, []byte("\r\n")) {
		t.Errorf("LF-only input should produce LF-only output, got %q", string(out))
	}
	if !bytes.Contains(out, []byte("<!-- STEERSPEC:BEGIN:s -->\nnew\n<!-- STEERSPEC:END:s -->")) {
		t.Errorf("LF replacement missing, got %q", string(out))
	}
}

func TestRenderFullReplace(t *testing.T) {
	tmpl := []byte("This is the template content")
	existing := []byte("This is existing content that should be ignored")

	out, err := Render(StrategyFullReplace, tmpl, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Equal(out, tmpl) {
		t.Errorf("got %q, want %q", string(out), string(tmpl))
	}
}

func TestRenderFullReplaceWithVariables(t *testing.T) {
	tmpl := []byte("Project: {{name}}, Type: {{type}}")
	vars := map[string]string{"name": "api", "type": "service"}

	out, err := Render(StrategyFullReplace, tmpl, nil, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Project: api, Type: service"
	if string(out) != expected {
		t.Errorf("got %q, want %q", string(out), expected)
	}
}

func TestRenderInvalidStrategy(t *testing.T) {
	_, err := Render("bogus", []byte("content"), nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid strategy")
	}
	if !strings.Contains(err.Error(), "unknown strategy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRenderEmptyTemplate(t *testing.T) {
	out, err := Render(StrategyFullReplace, []byte{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %q", string(out))
	}
}

func TestRenderMarkerMissingEndMarker(t *testing.T) {
	tmpl := []byte("<!-- STEERSPEC:BEGIN:test -->\ncontent")

	// With no existing content the template is returned as-is; no error expected.
	_, _ = Render(StrategyMarker, tmpl, nil, nil)

	// Test with existing content to trigger template parsing
	_, err := Render(StrategyMarker, tmpl, []byte("existing"), nil)
	if err == nil {
		t.Fatal("expected error for unclosed marker")
	}
}

func TestRenderMarkerMismatchedNames(t *testing.T) {
	tmpl := []byte("<!-- STEERSPEC:BEGIN:foo -->\ncontent\n<!-- STEERSPEC:END:bar -->")

	_, err := Render(StrategyMarker, tmpl, []byte("existing"), nil)
	if err == nil {
		t.Fatal("expected error for mismatched marker names")
	}
}

func TestRenderMarkerMultipleSections(t *testing.T) {
	existing := []byte(strings.Join([]string{
		"# My Project",
		"Some intro text",
		"<!-- STEERSPEC:BEGIN:standards -->",
		"old standards",
		"<!-- STEERSPEC:END:standards -->",
		"",
		"Unmanaged paragraph between sections",
		"",
		"<!-- STEERSPEC:BEGIN:tools -->",
		"old tools list",
		"<!-- STEERSPEC:END:tools -->",
		"Footer text",
	}, "\n"))

	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:standards -->",
		"new standards content",
		"updated line",
		"<!-- STEERSPEC:END:standards -->",
		"<!-- STEERSPEC:BEGIN:tools -->",
		"new tools list",
		"<!-- STEERSPEC:END:tools -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := string(out)
	// Both sections should be replaced
	if !strings.Contains(result, "new standards content") {
		t.Error("expected new standards content")
	}
	if !strings.Contains(result, "updated line") {
		t.Error("expected updated line in standards section")
	}
	if !strings.Contains(result, "new tools list") {
		t.Error("expected new tools list")
	}
	// Old content should be gone
	if strings.Contains(result, "old standards") {
		t.Error("old standards should be replaced")
	}
	if strings.Contains(result, "old tools list") {
		t.Error("old tools list should be replaced")
	}
	// Unmanaged content should be preserved
	if !strings.Contains(result, "Some intro text") {
		t.Error("intro text should be preserved")
	}
	if !strings.Contains(result, "Unmanaged paragraph between sections") {
		t.Error("unmanaged paragraph between sections should be preserved")
	}
	if !strings.Contains(result, "Footer text") {
		t.Error("footer should be preserved")
	}
}

func TestRenderMustacheUnicode(t *testing.T) {
	tmpl := []byte("Project: {{name}}")
	vars := map[string]string{
		"name": "日本語テスト 🚀",
	}

	out, err := Render(StrategyMustache, tmpl, nil, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Project: 日本語テスト 🚀"
	if string(out) != expected {
		t.Errorf("got %q, want %q", string(out), expected)
	}
}

func TestRenderMarkerUnicode(t *testing.T) {
	existing := []byte(strings.Join([]string{
		"# ドキュメント",
		"<!-- STEERSPEC:BEGIN:rules -->",
		"古いルール",
		"<!-- STEERSPEC:END:rules -->",
	}, "\n"))

	template := []byte(strings.Join([]string{
		"<!-- STEERSPEC:BEGIN:rules -->",
		"新しいルール 📝",
		"ルール二行目",
		"<!-- STEERSPEC:END:rules -->",
	}, "\n"))

	out, err := Render(StrategyMarker, template, existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := string(out)
	if !strings.Contains(result, "新しいルール 📝") {
		t.Error("expected new Unicode content")
	}
	if !strings.Contains(result, "ルール二行目") {
		t.Error("expected second line of Unicode content")
	}
	if strings.Contains(result, "古いルール") {
		t.Error("old Unicode content should be replaced")
	}
	if !strings.Contains(result, "# ドキュメント") {
		t.Error("Unicode header should be preserved")
	}
}
