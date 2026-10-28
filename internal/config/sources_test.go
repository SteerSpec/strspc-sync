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
version: "1.0"
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
