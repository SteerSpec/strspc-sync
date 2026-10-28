//go:build e2e

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/template"
)

// The scenarios assert on these markers, so each fixture template must render to exactly its own marker.
func TestFixtureTemplatesRenderTheirMarkers(t *testing.T) {
	vars := map[string]string{"org_name": DefaultOrg, "e2e_marker": "strspc-e2e", "template_name": "sync-x"}
	for file, marker := range map[string]string{
		filepath.Join("..", "fixtures", "templates", "basic.mustache"):                          "BASIC-SOURCE",
		filepath.Join("..", "fixtures", "templates", "visibility.public.mustache"):              "PUBLIC-SOURCE",
		filepath.Join("..", "fixtures", "src-repo", "templates", "visibility.private.mustache"): "PRIVATE-SOURCE",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		out, err := template.Render(template.StrategyMustache, src, nil, vars)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if !strings.Contains(string(out), marker) || strings.Contains(string(out), "{{") {
			t.Errorf("%s rendered %q, want marker %s and no unresolved variables", file, out, marker)
		}
		for _, other := range []string{"BASIC-SOURCE", "PUBLIC-SOURCE", "PRIVATE-SOURCE"} {
			if other != marker && strings.Contains(string(out), other) {
				t.Errorf("%s also contains %s", file, other)
			}
		}
	}
}
