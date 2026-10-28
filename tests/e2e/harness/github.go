//go:build e2e

package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

// ListPRs returns the open pull requests of repo.
func (e *Env) ListPRs(t *testing.T, repo string) []*gh.PullRequest {
	t.Helper()
	return e.ListPRsState(t, repo, "open")
}

// ListPRsState lists pull requests in the given state: "open", "closed" or "all".
func (e *Env) ListPRsState(t *testing.T, repo, state string) []*gh.PullRequest {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	prs, err := e.listPRs(ctx, repo, state)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	return prs
}

func (e *Env) listPRs(ctx context.Context, repo, state string) ([]*gh.PullRequest, error) {
	prs, err := e.Client.PullRequests.List(ctx, e.Org, repo, &gh.PullRequestListOptions{State: state})
	if err != nil {
		return nil, fmt.Errorf("listing PRs of %s: %w", repo, err)
	}
	return prs, nil
}

// PRsWithPrefix returns the open pull requests of repo whose head branch starts with branchPrefix, for example
// env.BranchPrefix() or "steerspec-sync/"+env.TemplateID("claude")+"/".
func (e *Env) PRsWithPrefix(t *testing.T, repo, branchPrefix string) []*gh.PullRequest {
	t.Helper()
	return filterPRs(e.ListPRs(t, repo), branchPrefix)
}

// PRsWithPrefixState is PRsWithPrefix for any state ("open", "closed", "all").
func (e *Env) PRsWithPrefixState(t *testing.T, repo, state, branchPrefix string) []*gh.PullRequest {
	t.Helper()
	return filterPRs(e.ListPRsState(t, repo, state), branchPrefix)
}

func filterPRs(prs []*gh.PullRequest, prefix string) []*gh.PullRequest {
	var out []*gh.PullRequest
	for _, pr := range prs {
		if strings.HasPrefix(pr.Head, prefix) {
			out = append(out, pr)
		}
	}
	return out
}

// ListIssues returns the open issues of repo (pull requests excluded). A non-empty filter keeps only issues that
// carry the label filter or whose title starts with it; "" keeps all.
func (e *Env) ListIssues(t *testing.T, repo, labelOrTitlePrefix string) []*gh.Issue {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	issues, err := e.listIssues(ctx, repo, "open")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	return filterIssues(issues, labelOrTitlePrefix)
}

func (e *Env) listIssues(ctx context.Context, repo, state string) ([]*gh.Issue, error) {
	issues, err := e.Client.Issues.List(ctx, e.Org, repo, &gh.IssueListOptions{State: state})
	if err != nil {
		return nil, fmt.Errorf("listing issues of %s: %w", repo, err)
	}
	return issues, nil
}

func filterIssues(issues []*gh.Issue, labelOrTitlePrefix string) []*gh.Issue {
	if labelOrTitlePrefix == "" {
		return issues
	}
	var out []*gh.Issue
	for _, is := range issues {
		match := strings.HasPrefix(is.Title, labelOrTitlePrefix)
		for _, l := range is.Labels {
			if l == labelOrTitlePrefix {
				match = true
			}
		}
		if match {
			out = append(out, is)
		}
	}
	return out
}
