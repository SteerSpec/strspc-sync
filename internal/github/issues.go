package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type issueService struct {
	client *Client
}

func (s *issueService) List(ctx context.Context, owner, repo string, opts *IssueListOptions) ([]*Issue, error) {
	var all []*Issue
	page := 1

	for {
		params := url.Values{"per_page": {"100"}, "page": {fmt.Sprintf("%d", page)}}
		if opts != nil {
			if opts.State != "" {
				params.Set("state", opts.State)
			}
			if len(opts.Labels) > 0 {
				params.Set("labels", strings.Join(opts.Labels, ","))
			}
		}

		u := fmt.Sprintf("%s/repos/%s/%s/issues?%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), params.Encode())
		resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			return nil, fmt.Errorf("list issues %s/%s: %w", owner, repo, err)
		}

		var issues []apiIssue
		if err := decodeBody(resp, &issues); err != nil {
			return nil, err
		}

		for i := range issues {
			// Skip pull requests (GitHub API returns PRs in issue listings)
			if issues[i].PullRequest != nil {
				continue
			}
			all = append(all, issues[i].toDomain())
		}
		if len(issues) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (s *issueService) Create(ctx context.Context, owner, repo string, issue *IssueCreate) (*Issue, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/issues", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	body := map[string]any{
		"title": issue.Title,
		"body":  issue.Body,
	}
	if len(issue.Labels) > 0 {
		body["labels"] = issue.Labels
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.doRequest(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, fmt.Errorf("create issue %s/%s: %w", owner, repo, err)
	}

	var apiIss apiIssue
	if err := decodeBody(resp, &apiIss); err != nil {
		return nil, err
	}
	return apiIss.toDomain(), nil
}

func (s *issueService) Update(ctx context.Context, owner, repo string, number int, issue *IssueUpdate) error {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%d", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), number)

	body := make(map[string]any)
	if issue.Title != nil {
		body["title"] = *issue.Title
	}
	if issue.Body != nil {
		body["body"] = *issue.Body
	}
	if issue.State != nil {
		body["state"] = *issue.State
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := s.client.doRequest(ctx, http.MethodPatch, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return fmt.Errorf("update issue %s/%s#%d: %w", owner, repo, number, err)
	}
	resp.Body.Close() //nolint:errcheck // success response body close is best-effort
	return nil
}

func (s *issueService) Close(ctx context.Context, owner, repo string, number int) error {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%d", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), number)

	payload, _ := json.Marshal(map[string]string{"state": "closed"})
	resp, err := s.client.doRequest(ctx, http.MethodPatch, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return fmt.Errorf("close issue %s/%s#%d: %w", owner, repo, number, err)
	}
	resp.Body.Close() //nolint:errcheck // success response body close is best-effort
	return nil
}

type apiIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	HTMLURL     string `json:"html_url"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (i *apiIssue) toDomain() *Issue {
	labels := make([]string, len(i.Labels))
	for idx, l := range i.Labels {
		labels[idx] = l.Name
	}
	return &Issue{
		Number: i.Number,
		Title:  i.Title,
		Body:   i.Body,
		State:  i.State,
		Labels: labels,
		URL:    i.HTMLURL,
	}
}
