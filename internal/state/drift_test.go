package state

import (
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

func TestTemplateStateDrift(t *testing.T) {
	rendered := []byte("# Repo\n<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->\n")
	sections, ok, err := template.SectionsHash(rendered)
	if err != nil || !ok {
		t.Fatal(err)
	}
	full := hash.HashBytes(rendered)
	localEdit := append([]byte("Local build notes.\n"), rendered...)
	sectionEdit := []byte("# Repo\n<!-- STEERSPEC:BEGIN:org -->\nEdited.\n<!-- STEERSPEC:END:org -->\n")

	tests := []struct {
		name    string
		ts      TemplateState
		content []byte
		drifted bool
	}{
		{"whole file: unchanged", TemplateState{Hash: full}, rendered, false},
		{"whole file: edit outside sections", TemplateState{Hash: full}, localEdit, true},
		{"sections: unchanged", TemplateState{Hash: full, SectionsHash: sections}, rendered, false},
		{"sections: edit outside sections", TemplateState{Hash: full, SectionsHash: sections}, localEdit, false},
		{"sections: edit inside a section", TemplateState{Hash: full, SectionsHash: sections}, sectionEdit, true},
		{"sections: section removed", TemplateState{Hash: full, SectionsHash: sections}, []byte("# Repo\n"), true},
		{"sections: broken marker", TemplateState{Hash: full, SectionsHash: sections}, []byte("<!-- STEERSPEC:BEGIN:org -->\n"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drifted, expected, actual := tt.ts.Drift(tt.content)
			if drifted != tt.drifted {
				t.Errorf("drifted = %v (expected %s, actual %s), want %v", drifted, expected, actual, tt.drifted)
			}
		})
	}
}
