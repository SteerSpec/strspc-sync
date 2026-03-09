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
