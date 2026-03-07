package template

import (
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

	if string(out) != string(template) {
		t.Errorf("got %q, want %q", string(out), string(template))
	}
}

func TestRenderMarkerEmptyExisting(t *testing.T) {
	template := []byte("<!-- STEERSPEC:BEGIN:s -->\ncontent\n<!-- STEERSPEC:END:s -->")

	out, err := Render(StrategyMarker, template, []byte{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(out) != string(template) {
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

	if string(out) != string(tmpl) {
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

	_, err := Render(StrategyMarker, tmpl, nil, nil)
	// With no existing content, the template is returned as-is,
	// but the extractSections should still catch unclosed markers
	// since we need to parse template sections
	if err != nil {
		// extractSections is only called when existingContent is non-empty
		// for the template path; with empty existing, template is returned directly
	}

	// Test with existing content to trigger template parsing
	_, err = Render(StrategyMarker, tmpl, []byte("existing"), nil)
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
