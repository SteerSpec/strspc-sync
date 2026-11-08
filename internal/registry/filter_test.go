package registry

import "testing"

func TestFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		input  string
		want   bool
	}{
		{"empty matches all", "", "org/repo", true},
		{"only commas matches all", " , ", "org/repo", true},
		{"exact name", "org/repo", "org/repo", true},
		{"exact name other", "org/repo", "org/other", false},
		{"glob", "org/api-*", "org/api-gw", true},
		{"glob does not cross slash", "*", "org/repo", false},
		{"list first", "claude-md,agents", "claude-md", true},
		{"list second with spaces", "claude-md , agents", "agents", true},
		{"list none", "claude-md,agents", "skills", false},
		{"list of globs", "org/api-*,org/web-*", "org/web-app", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := ParseFilter(tt.filter)
			if err != nil {
				t.Fatalf("ParseFilter(%q): %v", tt.filter, err)
			}
			if got := f.Match(tt.input); got != tt.want {
				t.Errorf("ParseFilter(%q).Match(%q) = %v, want %v", tt.filter, tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFilter_Invalid(t *testing.T) {
	for _, s := range []string{"[", "ok,org/[a-"} {
		if _, err := ParseFilter(s); err == nil {
			t.Errorf("ParseFilter(%q) = nil error, want one", s)
		}
	}
}
