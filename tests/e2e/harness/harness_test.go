//go:build e2e

package harness

import (
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

func TestTemplateID(t *testing.T) {
	tests := []struct{ run, name, want string }{
		{"123", "claude", "e2e-123-claude"},
		{"20260101t000000", "skill", "e2e-20260101t000000-skill"},
	}
	for _, tc := range tests {
		e := &Env{RunID: tc.run}
		if got := e.TemplateID(tc.name); got != tc.want {
			t.Errorf("TemplateID(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	e := &Env{RunID: "9"}
	if got, want := e.BranchPrefix(), "steerspec-sync/e2e-9-"; got != want {
		t.Errorf("BranchPrefix = %q, want %q", got, want)
	}
}

func TestPoll(t *testing.T) {
	var calls atomic.Int32
	if !poll(time.Second, time.Millisecond, func() bool { return calls.Add(1) >= 3 }) {
		t.Fatal("poll should succeed on third call")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	start := time.Now()
	if poll(30*time.Millisecond, 5*time.Millisecond, func() bool { return false }) {
		t.Fatal("poll should time out")
	}
	if time.Since(start) > time.Second {
		t.Error("poll overran its timeout")
	}
}

func TestParseResult(t *testing.T) {
	tests := []struct {
		name, cmd, stdout string
		exit              int
		check             func(t *testing.T, r *Result)
	}{
		{
			name: "sync", cmd: "sync", exit: 0,
			stdout: `{"id":"s1","status":"completed","repos_targeted":2,"prs_created":1,"prs_updated":0,"repos_skipped":1,"errors":0,
"repo_results":[{"repo":"o/strspc-e2e-public","status":"pr_created","pr_number":7,"template_id":"e2e-1-a"}]}`,
			check: func(t *testing.T, r *Result) {
				if r.Sync == nil || r.Sync.PRsCreated != 1 || r.Sync.ReposSkipped != 1 {
					t.Fatalf("sync = %+v", r.Sync)
				}
				rr, ok := r.RepoResultFor("strspc-e2e-public", "e2e-1-a")
				if !ok || rr.PRNumber != 7 {
					t.Errorf("RepoResultFor = %+v, %v", rr, ok)
				}
				if _, ok := r.RepoResultFor("strspc-e2e-public", "other"); ok {
					t.Error("matched wrong template")
				}
			},
		},
		{
			name: "monitor", cmd: "monitor", exit: 0,
			stdout: `{"repos_in_sync":3,"repos_drifted":1,"issues_created":1,"issues_closed":0,
"drift_entries":[{"repo":"o/r","template_id":"t","issue_number":4}]}`,
			check: func(t *testing.T, r *Result) {
				if r.Monitor == nil || r.Monitor.ReposDrifted != 1 || r.Monitor.DriftEntries[0].IssueNumber != 4 {
					t.Fatalf("monitor = %+v", r.Monitor)
				}
			},
		},
		{
			name: "conflict", cmd: "conflict", exit: 1,
			stdout: `{"id":"c1","entries":[{"severity":"critical","repo":"o/r"}],"severity_summary":{"critical":1,"warning":0,"info":0},
"issues_created":1}`,
			check: func(t *testing.T, r *Result) {
				if r.Conflict == nil || r.Conflict.SeveritySummary.Critical != 1 || len(r.Conflict.Entries) != 1 {
					t.Fatalf("conflict = %+v", r.Conflict)
				}
			},
		},
		{
			name: "garbage", cmd: "sync", exit: 1, stdout: "error: boom",
			check: func(t *testing.T, r *Result) {
				if r.ParseErr == nil || r.Sync != nil {
					t.Errorf("want ParseErr and nil Sync, got %v / %v", r.ParseErr, r.Sync)
				}
			},
		},
		{
			name: "unknown command", cmd: "version", stdout: "{}",
			check: func(t *testing.T, r *Result) {
				if r.ParseErr == nil {
					t.Error("want ParseErr")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseResult(tc.cmd, tc.exit, tc.stdout, "")
			if r.ExitCode != tc.exit {
				t.Errorf("ExitCode = %d", r.ExitCode)
			}
			tc.check(t, r)
		})
	}
}

func TestFilters(t *testing.T) {
	prs := []*gh.PullRequest{{Number: 1, Head: "steerspec-sync/e2e-1-a/v1"}, {Number: 2, Head: "feature/x"}}
	if got := filterPRs(prs, "steerspec-sync/e2e-1-"); len(got) != 1 || got[0].Number != 1 {
		t.Errorf("filterPRs = %+v", got)
	}
	issues := []*gh.Issue{
		{Number: 1, Title: "Drift: e2e-1-a", Labels: []string{"steerspec-drift"}},
		{Number: 2, Title: "other", Labels: []string{"bug"}},
	}
	tests := []struct {
		filter string
		want   int
	}{{"", 2}, {"steerspec-drift", 1}, {"Drift:", 1}, {"nomatch", 0}}
	for _, tc := range tests {
		if got := filterIssues(issues, tc.filter); len(got) != tc.want {
			t.Errorf("filterIssues(%q) = %d, want %d", tc.filter, len(got), tc.want)
		}
	}
}

func TestRunEnv(t *testing.T) {
	e := &Env{Org: "o", RunID: "1", appID: "42", privateKey: "k"}
	got := e.runEnv([]string{"PATH=/bin", "GITHUB_REPOSITORY=x/y", "GITHUB_OUTPUT=/f", "STEERSPEC_APP_ID=old"})
	want := map[string]bool{
		"PATH=/bin": true, "GITHUB_REPOSITORY=o/strspc-e2e-central": true, "STEERSPEC_APP_ID=42": true,
		"STEERSPEC_PRIVATE_KEY=k": true, "E2E_ORG=o": true, "E2E_RUN_ID=1": true,
	}
	if len(got) != len(want) {
		t.Fatalf("env = %v", got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Errorf("unexpected env entry %q", kv)
		}
	}
}

func TestEscapePath(t *testing.T) {
	if got, want := escapePath(".steerspec/a b.json"), ".steerspec/a%20b.json"; got != want {
		t.Errorf("escapePath = %q, want %q", got, want)
	}
}
