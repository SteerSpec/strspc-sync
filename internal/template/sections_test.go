package template

import "testing"

func TestSectionsHash(t *testing.T) {
	const managed = "<!-- STEERSPEC:BEGIN:org -->\nOrg rules.\n<!-- STEERSPEC:END:org -->"
	base, ok, err := SectionsHash([]byte("# Repo\n\n" + managed + "\n"))
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
		{"section text changes", "# Repo\n\n<!-- STEERSPEC:BEGIN:org -->\nOther rules.\n<!-- STEERSPEC:END:org -->\n", false},
		{"second section added", "# Repo\n\n" + managed + "\n<!-- STEERSPEC:BEGIN:x -->\nx\n<!-- STEERSPEC:END:x -->\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := SectionsHash([]byte(tt.content))
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
		ha, _, _ := SectionsHash([]byte(a))
		hb, _, _ := SectionsHash([]byte(b))
		if ha != hb {
			t.Errorf("reordered sections hash differently: %s vs %s", ha, hb)
		}
	})

	t.Run("no section", func(t *testing.T) {
		if got, ok, err := SectionsHash([]byte("# Repo only\n")); ok || err != nil || got != "" {
			t.Errorf("SectionsHash = %q, %v, %v; want empty, false, nil", got, ok, err)
		}
	})

	t.Run("broken marker", func(t *testing.T) {
		if _, _, err := SectionsHash([]byte("<!-- STEERSPEC:BEGIN:org -->\nno end\n")); err == nil {
			t.Error("want an error for an unclosed section")
		}
	})
}
