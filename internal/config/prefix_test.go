package config

import (
	"strings"
	"testing"
)

// sync.commit-prefix and sync.pr-title-prefix default to a conventional-commit
// type(scope) so sync commits and PR titles pass commitlint and
// action-semantic-pull-request in target repos.
func TestPrefixDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sync.CommitPrefix != "chore(steerspec-sync)" {
		t.Errorf("commit-prefix default = %q", cfg.Sync.CommitPrefix)
	}
	if cfg.Sync.PRTitlePrefix != "chore(steerspec-sync)" {
		t.Errorf("pr-title-prefix default = %q", cfg.Sync.PRTitlePrefix)
	}
}

func TestPrefixOverrides(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig+`
sync:
  commit-prefix: "chore(ai-config)"
  pr-title-prefix: "ci(steerspec)"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sync.CommitPrefix != "chore(ai-config)" {
		t.Errorf("commit-prefix = %q", cfg.Sync.CommitPrefix)
	}
	if cfg.Sync.PRTitlePrefix != "ci(steerspec)" {
		t.Errorf("pr-title-prefix = %q", cfg.Sync.PRTitlePrefix)
	}
}

func TestPrefixRejectsMultilineOrPadded(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"newline", "sync:\n  commit-prefix: \"chore\\nfix\"\n"},
		{"padded", "sync:\n  pr-title-prefix: \" chore(x)\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, validConfig+tc.yaml))
			if err == nil || !strings.Contains(err.Error(), "prefix") {
				t.Fatalf("expected a prefix validation error, got %v", err)
			}
		})
	}
}
