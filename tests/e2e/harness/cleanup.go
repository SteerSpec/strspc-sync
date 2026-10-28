//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

const cleanupTimeout = 5 * time.Minute

// Cleanup removes what this run created in every e2e repo; see CleanupAll. It logs and fails t (without aborting)
// if anything could not be cleaned, and uses its own context so it still works after t's context is cancelled.
func (e *Env) Cleanup(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_KEEP") == "true" {
		t.Logf("E2E_KEEP=true: leaving run %s in place", e.RunID)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
	defer cancel()
	if err := CleanupAll(ctx, e); err != nil {
		t.Errorf("cleanup: %v", err)
	}
}

// CleanupAll first restores every file this Env changed to its pre-run content (or removes it if it did not exist),
// then closes the open pull requests whose head branch starts with steerspec-sync/e2e-<RunID>-, deletes those
// branches, and closes the open issues mentioning the run's template prefix (e2e-<RunID>-) in the title or body,
// across the central repo and all target repos. It keeps going after a failure and returns the joined errors.
func CleanupAll(ctx context.Context, e *Env) error {
	var errs []error
	if err := e.restoreBaselines(ctx); err != nil {
		errs = append(errs, err)
	}
	// strspc-e2e-central is dedicated to the suite, so its baseline state is empty; resetting also works in the
	// cleanup-only process, which has no in-memory snapshot.
	if err := e.resetFile(ctx, RepoCentral, StatePath, []byte(EmptyState)); err != nil {
		errs = append(errs, err)
	}
	for _, repo := range append([]string{RepoCentral}, TargetRepos...) {
		if err := e.cleanupRepo(ctx, repo); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Env) cleanupRepo(ctx context.Context, repo string) error {
	var errs []error
	prefix := e.BranchPrefix()

	prs, err := e.listPRs(ctx, repo, "open")
	if err != nil {
		errs = append(errs, err)
	}
	for _, pr := range filterPRs(prs, prefix) {
		if err := e.Client.PullRequests.Close(ctx, e.Org, repo, pr.Number); err != nil {
			errs = append(errs, fmt.Errorf("closing PR #%d: %w", pr.Number, err))
		}
	}

	branches, err := e.branchesWithPrefix(ctx, repo, prefix)
	if err != nil {
		errs = append(errs, err)
	}
	for _, b := range branches {
		if err := e.deleteBranch(ctx, repo, b); err != nil {
			errs = append(errs, err)
		}
	}

	issues, err := e.listIssues(ctx, repo, "open")
	if err != nil {
		errs = append(errs, err)
	}
	marker := templateID(e.RunID, "")
	for _, is := range issues {
		if !ownedByRun(is, marker) {
			continue
		}
		if err := e.Client.Issues.Close(ctx, e.Org, repo, is.Number); err != nil {
			errs = append(errs, fmt.Errorf("closing issue #%d: %w", is.Number, err))
		}
	}
	return errors.Join(errs...)
}

// runIssueLabels are the labels strspc gives drift and conflict issues. Conflict issues are titled by conflict type and
// path, not by template id, so they carry no run marker; in the dedicated, serialized testbed any open issue with one
// of these labels is a leftover of a test run.
var runIssueLabels = []string{"steerspec-drift", "steerspec-conflict"}

// ownedByRun reports whether cleanup should close is: it mentions the run's template prefix, or carries a drift or
// conflict label.
func ownedByRun(is *gh.Issue, marker string) bool {
	if strings.Contains(is.Title, marker) || strings.Contains(is.Body, marker) {
		return true
	}
	for _, l := range is.Labels {
		if slices.Contains(runIssueLabels, l) {
			return true
		}
	}
	return false
}

// branchesWithPrefix lists branch names in repo starting with prefix.
func (e *Env) branchesWithPrefix(ctx context.Context, repo, prefix string) ([]string, error) {
	path := "/repos/" + url.PathEscape(e.Org) + "/" + url.PathEscape(repo) + "/git/matching-refs/heads/" + escapePath(prefix)
	status, body, err := e.api(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing branches: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("listing branches: status %d: %s", status, body)
	}
	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &refs); err != nil {
		return nil, fmt.Errorf("decoding branches: %w", err)
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, strings.TrimPrefix(r.Ref, "refs/heads/"))
	}
	return names, nil
}

func (e *Env) deleteBranch(ctx context.Context, repo, branch string) error {
	path := "/repos/" + url.PathEscape(e.Org) + "/" + url.PathEscape(repo) + "/git/refs/heads/" + escapePath(branch)
	status, body, err := e.api(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("deleting branch %s: %w", branch, err)
	}
	// 422 means the ref is already gone.
	if status != http.StatusNoContent && status != http.StatusUnprocessableEntity && status != http.StatusNotFound {
		return fmt.Errorf("deleting branch %s: status %d: %s", branch, status, body)
	}
	return nil
}
