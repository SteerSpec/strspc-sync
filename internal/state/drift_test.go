package state

import (
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

func TestTemplateStateDrift(t *testing.T) {
	rendered := []byte("# Repo\n<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->\n")
	names := []string{"org"}
	sections, ok := template.SectionsHash(rendered, names)
	if !ok {
		t.Fatal("SectionsHash: no sections")
	}
	full := hash.HashBytes(rendered)
	marker := TemplateState{Hash: full, Sections: names, SectionsHash: sections}
	localEdit := append([]byte("Local build notes.\n"), rendered...)
	otherSection := append(append([]byte{}, rendered...),
		[]byte("<!-- STEERSPEC:BEGIN:local -->\nrepo's own\n<!-- STEERSPEC:END:local -->\n")...)
	sectionEdit := []byte("# Repo\n<!-- STEERSPEC:BEGIN:org -->\nEdited.\n<!-- STEERSPEC:END:org -->\n")

	tests := []struct {
		name    string
		ts      TemplateState
		content []byte
		drifted bool
	}{
		{"whole file: unchanged", TemplateState{Hash: full}, rendered, false},
		{"whole file: edit outside sections", TemplateState{Hash: full}, localEdit, true},
		{"sections: unchanged", marker, rendered, false},
		{"sections: edit outside sections", marker, localEdit, false},
		{"sections: another section added", marker, otherSection, false},
		{"sections: broken marker of another name", marker, append(append([]byte{}, rendered...),
			[]byte("<!-- STEERSPEC:BEGIN:local -->\nunclosed\n")...), false},
		{"sections: edit inside a section", marker, sectionEdit, true},
		{"sections: section removed", marker, []byte("# Repo\n"), true},
		{"sections: broken marker", marker, []byte("<!-- STEERSPEC:BEGIN:org -->\n"), true},
		{"sections hash without names falls back to the whole file", TemplateState{Hash: full, SectionsHash: sections}, localEdit, true},
	}
	if got := marker.ExpectedHash(); got != sections {
		t.Errorf("marker ExpectedHash = %s, want the sections hash %s", got, sections)
	}
	if got := (TemplateState{Hash: full}).ExpectedHash(); got != full {
		t.Errorf("whole-file ExpectedHash = %s, want %s", got, full)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drifted, expected, actual := tt.ts.Drift(tt.content)
			if expected != tt.ts.ExpectedHash() {
				t.Errorf("Drift compared against %s, ExpectedHash says %s", expected, tt.ts.ExpectedHash())
			}
			if drifted != tt.drifted {
				t.Errorf("drifted = %v (expected %s, actual %s), want %v", drifted, expected, actual, tt.drifted)
			}
		})
	}
}
