//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/conflict"
	"github.com/SteerSpec/strspc-sync/internal/monitor"
	"github.com/SteerSpec/strspc-sync/internal/sync"
)

// runTimeout bounds one binary invocation.
const runTimeout = 10 * time.Minute

// Result is the outcome of one strspc invocation. Exactly one of Sync, Monitor and Conflict is set, matching the
// command, when stdout held a JSON summary; ParseErr says why not otherwise (for example a config error).
type Result struct {
	Cmd      string
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string

	Sync     *sync.Result
	Monitor  *monitor.Result
	Conflict *conflict.ConflictReport
	ParseErr error
}

// parse decodes the stdout JSON of cmd ("sync", "monitor" or "conflict") into r.
func (r *Result) parse(cmd, stdout string) {
	var err error
	switch cmd {
	case "sync":
		r.Sync = new(sync.Result)
		err = json.Unmarshal([]byte(stdout), r.Sync)
	case "monitor":
		r.Monitor = new(monitor.Result)
		err = json.Unmarshal([]byte(stdout), r.Monitor)
	case "conflict":
		r.Conflict = new(conflict.ConflictReport)
		err = json.Unmarshal([]byte(stdout), r.Conflict)
	default:
		err = fmt.Errorf("no summary for command %q", cmd)
	}
	if err != nil {
		r.Sync, r.Monitor, r.Conflict = nil, nil, nil
		r.ParseErr = fmt.Errorf("parsing %s output: %w", cmd, err)
	}
}

// ParseResult builds a Result from captured output; Run uses it and tests can feed it fixture JSON.
func ParseResult(cmd string, exitCode int, stdout, stderr string) *Result {
	r := &Result{Cmd: cmd, ExitCode: exitCode, Stdout: stdout, Stderr: stderr}
	r.parse(cmd, stdout)
	return r
}

// RepoResultFor returns the sync result of repo for templateID ("" matches any template). Repo may be the bare
// name or org/name.
func (r *Result) RepoResultFor(repo, templateID string) (sync.RepoResult, bool) {
	if r.Sync == nil {
		return sync.RepoResult{}, false
	}
	for _, rr := range r.Sync.RepoResults {
		if (rr.Repo == repo || strings.HasSuffix(rr.Repo, "/"+repo)) && (templateID == "" || rr.TemplateID == templateID) {
			return rr, true
		}
	}
	return sync.RepoResult{}, false
}

// Run executes the strspc binary (STRSPC_BIN) as `strspc <cmd> --config <ConfigPath> <args...>` with
// GITHUB_REPOSITORY pointing at the central repo and the App credentials in the environment. A non-zero exit is
// reported in Result.ExitCode, not as a test failure: sync exits 1 on errors and conflict on critical findings,
// which scenarios assert on. Failing to start the binary does fail t.
func (e *Env) Run(t *testing.T, cmd string, args ...string) *Result {
	t.Helper()
	if e.Bin == "" {
		t.Fatal("STRSPC_BIN is not set")
	}
	full := append([]string{cmd, "--config", e.ConfigPath}, args...)
	ctx, cancel := context.WithTimeout(t.Context(), runTimeout)
	defer cancel()

	c := exec.CommandContext(ctx, e.Bin, full...)
	c.Env = e.runEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	exit := 0
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running %s %s: %v", e.Bin, strings.Join(full, " "), err)
		}
		exit = ee.ExitCode()
	}
	t.Logf("strspc %s exit=%d\n%s", cmd, exit, stderr.String())

	r := ParseResult(cmd, exit, stdout.String(), stderr.String())
	r.Args = full
	return r
}

// runEnv returns base with the variables the binary needs set and GitHub Actions leftovers removed.
func (e *Env) runEnv(base []string) []string {
	drop := map[string]bool{
		"GITHUB_REPOSITORY": true, "GITHUB_OUTPUT": true, "GITHUB_EVENT_NAME": true,
		"STEERSPEC_APP_ID": true, "STEERSPEC_PRIVATE_KEY": true,
	}
	out := make([]string, 0, len(base)+6)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return append(out,
		"GITHUB_REPOSITORY="+e.Central(),
		"STEERSPEC_APP_ID="+e.appID,
		"STEERSPEC_PRIVATE_KEY="+e.privateKey,
		"E2E_ORG="+e.Org,
		"E2E_RUN_ID="+e.RunID,
	)
}
