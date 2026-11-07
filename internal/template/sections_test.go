package template

import (
	"slices"
	"testing"
)

func TestSectionNames(t *testing.T) {
	got, err := SectionNames([]byte("<!-- STEERSPEC:BEGIN:b -->\nb\n<!-- STEERSPEC:END:b -->\n" +
		"<!-- STEERSPEC:BEGIN:a -->\na\n<!-- STEERSPEC:END:a -->\n"))
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("SectionNames = %v, %v; want [a b]", got, err)
	}
	if got, err := SectionNames([]byte("no sections\n")); err != nil || len(got) != 0 {
		t.Errorf("SectionNames without sections = %v, %v; want none", got, err)
	}
	if _, err := SectionNames([]byte("<!-- STEERSPEC:BEGIN:a -->\n")); err == nil {
		t.Error("want an error for an unclosed section")
	}
}

func TestSectionsHash(t *testing.T) {
	const managed = "<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->"
	names := []string{"org"}
	base, ok, err := SectionsHash([]byte("# Repo\n\n"+managed+"\n"), names)
	if err != nil || !ok {
		t.Fatalf("SectionsHash = %q, %v, %v", base, ok, err)
	}

	tests := []struct {
		name    string
		content string
		same    bool
	}{
		{"content outside the section changes", "# Repo\n\nBuild with make.\n\n" + managed + "\n\nMore local notes.\n", true},
		{"CRLF line endings", "# Repo\r\n\r\n<!-- STEERSPEC:BEGIN:org -->\r\nOrg rules.\r\n<!-- STEERSPEC:END:org -->\r\n", true},
		{
			"another template's or the repo's own section", "# Repo\n\n" + managed +
				"\n<!-- STEERSPEC:BEGIN:local -->\nrepo section\n<!-- STEERSPEC:END:local -->\n",
			true,
		},
		{"section text changes", "# Repo\n\n<!-- STEERSPEC:BEGIN:org -->\nOther rules.\n<!-- STEERSPEC:END:org -->\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := SectionsHash([]byte(tt.content), names)
			if err != nil || !ok {
				t.Fatalf("SectionsHash = %q, %v, %v", got, ok, err)
			}
			if (got == base) != tt.same {
				t.Errorf("hash equal to base = %v, want %v", got == base, tt.same)
			}
		})
	}

	t.Run("section order does not matter", func(t *testing.T) {
		a := "<!-- STEERSPEC:BEGIN:a -->\na\n<!-- STEERSPEC:END:a -->\n<!-- STEERSPEC:BEGIN:b -->\nb\n<!-- STEERSPEC:END:b -->\n"
		b := "<!-- STEERSPEC:BEGIN:b -->\nb\n<!-- STEERSPEC:END:b -->\nlocal\n<!-- STEERSPEC:BEGIN:a -->\na\n<!-- STEERSPEC:END:a -->\n"
		ha, _, _ := SectionsHash([]byte(a), []string{"a", "b"})
		hb, _, _ := SectionsHash([]byte(b), []string{"b", "a"})
		if ha == "" || ha != hb {
			t.Errorf("reordered sections hash differently: %q vs %q", ha, hb)
		}
	})

	t.Run("named section missing", func(t *testing.T) {
		got, ok, err := SectionsHash([]byte("# Repo\n\n"+managed+"\n"), []string{"org", "gone"})
		if ok || err != nil || got != "" {
			t.Errorf("SectionsHash = %q, %v, %v; want empty, false, nil", got, ok, err)
		}
	})

	t.Run("no names", func(t *testing.T) {
		if got, ok, err := SectionsHash([]byte(managed), nil); ok || err != nil || got != "" {
			t.Errorf("SectionsHash = %q, %v, %v; want empty, false, nil", got, ok, err)
		}
	})

	t.Run("broken marker", func(t *testing.T) {
		if _, _, err := SectionsHash([]byte("<!-- STEERSPEC:BEGIN:org -->\nno end\n"), names); err == nil {
			t.Error("want an error for an unclosed section")
		}
	})
}
