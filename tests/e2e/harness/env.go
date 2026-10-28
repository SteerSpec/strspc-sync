//go:build e2e

// Package harness is the support library for the end-to-end suite in tests/e2e. It runs the real strspc binary
// against the testbed organization and inspects the outcome through the real GitHub client in internal/github.
package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

// Testbed repository names. Every one lives in Env.Org.
const (
	RepoCentral = "strspc-e2e-central" // templates, config, deployment state
	RepoPublic  = "strspc-e2e-public"
	RepoPrivate = "strspc-e2e-private"
	RepoSrc     = "strspc-e2e-src" // private second source repo for owner/repo:path templates
	RepoSymlink = "strspc-e2e-symlink"
	RepoDevelop = "strspc-e2e-develop" // default branch is develop

	// DefaultOrg is the organization used when E2E_ORG is unset.
	DefaultOrg = "testbed-multi-org"

	// StatePath is the deployment-state file in the central repo.
	StatePath = ".steerspec/deployment-state.json"

	// DefaultConfigRel is the testbed config relative to the repository root.
	DefaultConfigRel = "tests/e2e/fixtures/steerspec-sync.yml"
)

// TargetRepos are the repositories that receive sync PRs and drift/conflict issues.
var TargetRepos = []string{RepoPublic, RepoPrivate, RepoSymlink, RepoDevelop}

// Env is one e2e run: credentials, identity and an authenticated client.
type Env struct {
	Org        string // E2E_ORG
	RunID      string // E2E_RUN_ID
	Bin        string // STRSPC_BIN
	ConfigPath string // E2E_CONFIG, default <repo root>/tests/e2e/fixtures/steerspec-sync.yml
	Client     *gh.Client

	mu        sync.Mutex
	baselines map[string]baseline // files changed by this run, restored by Cleanup

	appID      string
	privateKey string
	baseURL    string
	token      string
	http       *http.Client
}

// New builds the Env for t. It skips the test when STEERSPEC_APP_ID or STEERSPEC_PRIVATE_KEY is unset, fails it when
// the App token cannot be minted, registers Cleanup to run when t finishes, and resets the deployment state so the
// scenario starts clean. CleanupRun does not reset anything.
func New(t *testing.T) *Env {
	t.Helper()
	e := newEnv(t)
	// Each test gets its own template-id prefix (e2e-<run>-<test>-), so closed PRs and branches left by one scenario
	// never show up in another's listings. It still starts with the run prefix, which TestCleanupRun sweeps.
	e.RunID += "-" + testSlug(t.Name())
	var err error
	if e.ConfigPath, err = e.writeRunConfig(t.TempDir()); err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	t.Cleanup(func() { e.Cleanup(t) })
	e.ResetState(t)
	return e
}

// testSlug turns a test name into a short, branch-safe suffix: "TestSyncForcedOpensPRs" becomes "syncforcedopensprs".
func testSlug(name string) string {
	name = strings.TrimPrefix(name, "Test")
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}

// CleanupRun removes everything the run left behind without registering anything else. A workflow step runs it as
// `go test -tags e2e -run TestCleanup ./tests/e2e/...`, so a test named TestCleanup calling CleanupRun(t) covers an
// aborted run.
func CleanupRun(t *testing.T) {
	t.Helper()
	e := newEnv(t)
	e.Cleanup(t)
}

func newEnv(t *testing.T) *Env {
	t.Helper()
	appID, key := os.Getenv("STEERSPEC_APP_ID"), os.Getenv("STEERSPEC_PRIVATE_KEY")
	if appID == "" || key == "" {
		t.Skip("STEERSPEC_APP_ID / STEERSPEC_PRIVATE_KEY not set; skipping e2e")
	}
	e, err := buildEnv(t.Context(), appID, key)
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	return e
}

func buildEnv(ctx context.Context, appID, key string) (*Env, error) {
	e := &Env{
		Org:        getenv("E2E_ORG", DefaultOrg),
		RunID:      getenv("E2E_RUN_ID", time.Now().UTC().Format("20060102t150405")),
		Bin:        os.Getenv("STRSPC_BIN"),
		ConfigPath: os.Getenv("E2E_CONFIG"),
		appID:      appID,
		privateKey: key,
		baseURL:    getenv("GITHUB_API_URL", "https://api.github.com"),
		http:       &http.Client{Timeout: 30 * time.Second},
	}
	if e.ConfigPath == "" {
		e.ConfigPath = filepath.Join(repoRoot(), filepath.FromSlash(DefaultConfigRel))
	}
	tok, err := mintInstallationToken(ctx, e.baseURL, appID, key, e.Org)
	if err != nil {
		return nil, fmt.Errorf("app token: %w", err)
	}
	e.token = tok
	e.Client, err = gh.NewClient(gh.AuthConfig{Method: "pat", Token: tok})
	if err != nil {
		return nil, fmt.Errorf("github client: %w", err)
	}
	return e, nil
}

// TemplateID returns the run-scoped template id e2e-<RunID>-<name>. Config fixtures use the same shape so cleanup
// can find every branch and issue by prefix.
func (e *Env) TemplateID(name string) string { return templateID(e.RunID, name) }

func templateID(runID, name string) string { return "e2e-" + runID + "-" + name }

// BranchPrefix is the head-branch prefix of every sync PR opened by this run.
func (e *Env) BranchPrefix() string { return branchPrefix(e.RunID) }

func branchPrefix(runID string) string { return "steerspec-sync/e2e-" + runID + "-" }

// Central returns the full name (org/repo) of the central repository.
func (e *Env) Central() string { return e.Org + "/" + RepoCentral }

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// repoRoot locates the repository root from this source file, independent of the test's working directory.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// <root>/tests/e2e/harness/env.go
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
