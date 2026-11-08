//go:build e2e

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// UnverifiedPRCommits returns the commits of PR number in repo whose signature GitHub does not verify, as
// "sha: reason". Sync commits through createCommitOnBranch, which GitHub signs (GH #126).
func (e *Env) UnverifiedPRCommits(t *testing.T, repo string, number int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits", url.PathEscape(e.Org), url.PathEscape(repo), number)
	status, body, err := e.api(ctx, http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("UnverifiedPRCommits: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("UnverifiedPRCommits %s#%d: status %d: %s", repo, number, status, body)
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
			} `json:"verification"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		t.Fatalf("UnverifiedPRCommits: decoding: %v", err)
	}
	if len(commits) == 0 {
		t.Fatalf("UnverifiedPRCommits %s#%d: no commits", repo, number)
	}
	var out []string
	for _, c := range commits {
		if !c.Commit.Verification.Verified {
			out = append(out, c.SHA+": "+c.Commit.Verification.Reason)
		}
	}
	return out
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

// MergePR squash-merges pull request number of repo. GitHub signs the squash commit, so it passes the testbed's
// Signed Commit ruleset. The merge changes the default branch, so call Track on every file the PR touches first:
// Cleanup then puts those files back.
func (e *Env) MergePR(t *testing.T, repo string, number int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", url.PathEscape(e.Org), url.PathEscape(repo), number)
	status, body, err := e.api(ctx, http.MethodPut, path, map[string]string{"merge_method": "squash"})
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("MergePR %s#%d: status %d: %s", repo, number, status, body)
	}
}

// RepoTopics returns the topics of repo.
func (e *Env) RepoTopics(t *testing.T, repo string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	path := fmt.Sprintf("/repos/%s/%s/topics", url.PathEscape(e.Org), url.PathEscape(repo))
	status, body, err := e.api(ctx, http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("RepoTopics: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("RepoTopics %s: status %d: %s", repo, status, body)
	}
	var resp struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("RepoTopics: decoding: %v", err)
	}
	return resp.Names
}

// PRAuthor returns the login of the user who opened PR number in repo.
func (e *Env) PRAuthor(t *testing.T, repo string, number int) string {
	t.Helper()
	var pr struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	e.getJSON(t, fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(e.Org), url.PathEscape(repo), number), &pr)
	return pr.User.Login
}

// Commit is the author and signature status of one commit.
type Commit struct {
	SHA      string
	// Author is the login GitHub links to the commit's author (the API's top-level author), "" when it links none.
	// createCommitOnBranch always authors as the authenticated user, so a sync commit links to the token's user;
	// the committer is GitHub itself (web-flow), so it is not a fallback.
	Author   string
	Verified bool
	Reason   string // verification reason, such as "valid" or "unsigned"
}

// LatestCommit returns the newest commit on the default branch of repo that touched path.
func (e *Env) LatestCommit(t *testing.T, repo, path string) Commit {
	t.Helper()
	var commits []struct {
		SHA    string `json:"sha"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
			} `json:"verification"`
		} `json:"commit"`
	}
	e.getJSON(t, fmt.Sprintf("/repos/%s/%s/commits?per_page=1&path=%s",
		url.PathEscape(e.Org), url.PathEscape(repo), url.QueryEscape(path)), &commits)
	if len(commits) == 0 {
		t.Fatalf("LatestCommit %s:%s: no commits", repo, path)
	}
	c := commits[0]
	out := Commit{SHA: c.SHA, Verified: c.Commit.Verification.Verified, Reason: c.Commit.Verification.Reason}
	if c.Author != nil {
		out.Author = c.Author.Login
	}
	return out
}

// getJSON GETs path with the harness token and decodes the 200 response into out, failing t otherwise.
func (e *Env) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	status, body, err := e.api(ctx, http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("GET %s: decoding: %v", path, err)
	}
}
