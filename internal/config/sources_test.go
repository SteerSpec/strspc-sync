package config

import (
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	for _, tc := range []struct {
		src, repo, path string
		wantErr         bool
	}{
		{src: "templates/CLAUDE.md", path: "templates/CLAUDE.md"},
		{src: "SteerSpec/.github-private:templates/a.mustache", repo: "SteerSpec/.github-private", path: "templates/a.mustache"},
		{src: "", wantErr: true},
		{src: "not-a-repo:path", wantErr: true},
		{src: "a/b:", wantErr: true},
		{src: ".claude/skills/x/SKILL.md", path: ".claude/skills/x/SKILL.md"},
		{src: "templates/..hidden/a.md", path: "templates/..hidden/a.md"},
		// TMPLT-004: a source path is relative to its repository, bare or after owner/repo:.
		{src: "/etc/passwd", wantErr: true},
		{src: "../other/a.md", wantErr: true},
		{src: "templates/../../a.md", wantErr: true},
		{src: "./templates/a.md", wantErr: true},
		{src: "templates//a.md", wantErr: true},
		{src: "templates/", wantErr: true},
		{src: `templates\a.md`, wantErr: true},
		{src: "templates/a\x00.md", wantErr: true},
		{src: "a/b:/abs.md", wantErr: true},
		{src: "a/b:../up.md", wantErr: true},
		{src: "a/b:t/./x.md", wantErr: true},
	} {
		t.Run(tc.src, func(t *testing.T) {
			repo, path, err := ParseSource(tc.src)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if repo != tc.repo || path != tc.path {
				t.Errorf("got (%q, %q), want (%q, %q)", repo, path, tc.repo, tc.path)
			}
		})
	}
}

func TestValidationSources(t *testing.T) {
	tmpl := func(extra string) string {
		return `
version: "1.0.0"
auth:
  method: pat
  token: "t"
templates:
  - id: t1
    version: "1.0.0"
    type: claude-md
    destination: d
    strategy: mustache
` + extra + `
targets:
  include: ["org/*"]
`
	}
	for _, tc := range []struct {
		name, extra, wantErr string
	}{
		{name: "sources only", extra: "    sources:\n      private: org/priv:t/p.mustache"},
		{name: "source and sources", extra: "    source: t/s\n    sources:\n      public: t/pub"},
		{name: "neither", extra: "", wantErr: "needs source or sources"},
		{name: "bad visibility", extra: "    source: t/s\n    sources:\n      secret: t/x", wantErr: "must be one of"},
		{name: "bad fallback ref", extra: "    source: bad:x", wantErr: "owner/repo:path"},
		{name: "bad repo ref", extra: "    source: t/s\n    sources:\n      private: bad:x", wantErr: "owner/repo:path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tmpl(tc.extra)))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TMPLT-005: a destination is required and is a path relative to the target repository's root.
func TestValidationDestination(t *testing.T) {
	cfg := func(dest string) string {
		return `
version: "1.0.0"
auth:
  method: pat
  token: "t"
templates:
  - id: t1
    version: "1.0.0"
    type: claude-md
    source: t/s
    strategy: mustache
` + dest + `
targets:
  include: ["org/*"]
`
	}
	for _, tc := range []struct {
		name, dest, wantErr string
	}{
		{name: "file", dest: "    destination: CLAUDE.md"},
		{name: "claude tree", dest: "    destination: .claude/skills/commit/SKILL.md"},
		{name: "missing", dest: "", wantErr: "destination is required"},
		{name: "absolute", dest: "    destination: /CLAUDE.md", wantErr: "not absolute"},
		{name: "escaping", dest: "    destination: ../CLAUDE.md", wantErr: `".." segment`},
		{name: "inner escape", dest: "    destination: .claude/../../x", wantErr: `".." segment`},
		{name: "dot", dest: "    destination: ./CLAUDE.md", wantErr: `"." segment`},
		{name: "trailing slash", dest: "    destination: .claude/", wantErr: "empty segment"},
		{name: "backslash", dest: `    destination: '.claude\x.md'`, wantErr: "separator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, cfg(tc.dest)))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
