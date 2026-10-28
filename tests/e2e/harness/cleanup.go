//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const cleanupTimeout = 5 * time.Minute

// Cleanup removes what this run created in every e2e repo; see CleanupAll. It logs and fails t (without aborting)
// if anything could not be cleaned, and uses its own context so it still works after t's context is cancelled.
func (e *Env) Cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
	defer cancel()
	if err := CleanupAll(ctx, e); err != nil {
		t.Errorf("cleanup: %v", err)
	}
}

// CleanupAll closes the open pull requests whose head branch starts with steerspec-sync/e2e-<RunID>-, deletes those
// branches, and closes the open issues mentioning the run's template prefix (e2e-<RunID>-) in the title or body,
// across the central repo and all target repos. It keeps going after a failure and returns the joined errors.
func CleanupAll(ctx context.Context, e *Env) error {
	var errs []error
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
		if !strings.Contains(is.Title, marker) && !strings.Contains(is.Body, marker) {
			continue
		}
		if err := e.Client.Issues.Close(ctx, e.Org, repo, is.Number); err != nil {
			errs = append(errs, fmt.Errorf("closing issue #%d: %w", is.Number, err))
		}
	}
	return errors.Join(errs...)
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
