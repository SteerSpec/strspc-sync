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

type pullRequestService struct {
	client *Client
}

func (s *pullRequestService) List(ctx context.Context, owner, repo string, opts *PullRequestListOptions) ([]*PullRequest, error) {
	var all []*PullRequest
	page := 1

	for {
		params := url.Values{"per_page": {"100"}, "page": {fmt.Sprintf("%d", page)}}
		if opts != nil {
			if opts.State != "" {
				params.Set("state", opts.State)
			}
			if opts.Head != "" {
				params.Set("head", opts.Head)
			}
		}

		u := fmt.Sprintf("%s/repos/%s/%s/pulls?%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), params.Encode())
		resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			return nil, fmt.Errorf("list PRs %s/%s: %w", owner, repo, err)
		}

		var prs []apiPullRequest
		if err := decodeBody(resp, &prs); err != nil {
			return nil, err
		}

		for i := range prs {
			pr := prs[i].toDomain()
			if opts != nil && len(opts.Labels) > 0 {
				if !hasAllLabels(pr.Labels, opts.Labels) {
					continue
				}
			}
			all = append(all, pr)
		}
		if len(prs) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (s *pullRequestService) Create(ctx context.Context, owner, repo string, pr *PullRequestCreate) (*PullRequest, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/pulls", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	body := map[string]any{
		"title": pr.Title,
		"body":  pr.Body,
		"head":  pr.Head,
		"base":  pr.Base,
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
		return nil, fmt.Errorf("create PR %s/%s: %w", owner, repo, err)
	}

	var apiPR apiPullRequest
	if err := decodeBody(resp, &apiPR); err != nil {
		return nil, err
	}

	created := apiPR.toDomain()

	// Add labels if specified
	if len(pr.Labels) > 0 {
		labelsURL := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), created.Number)
		labelsPayload, _ := json.Marshal(pr.Labels)
		labelsResp, err := s.client.doRequest(ctx, http.MethodPost, labelsURL, bytes.NewReader(labelsPayload))
		if err != nil {
			return created, err
		}
		labelsResp.Body.Close() //nolint:errcheck // label response body close is best-effort
		created.Labels = pr.Labels
	}

	return created, nil
}

func (s *pullRequestService) Update(ctx context.Context, owner, repo string, number int, pr *PullRequestUpdate) error {
	u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), number)

	body := make(map[string]any)
	if pr.Title != nil {
		body["title"] = *pr.Title
	}
	if pr.Body != nil {
		body["body"] = *pr.Body
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
		return fmt.Errorf("update PR %s/%s#%d: %w", owner, repo, number, err)
	}
	resp.Body.Close()
	return nil
}

func (s *pullRequestService) Close(ctx context.Context, owner, repo string, number int) error {
	u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), number)

	payload, _ := json.Marshal(map[string]string{"state": "closed"})
	resp, err := s.client.doRequest(ctx, http.MethodPatch, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return fmt.Errorf("close PR %s/%s#%d: %w", owner, repo, number, err)
	}
	resp.Body.Close()
	return nil
}

func (s *pullRequestService) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	u := fmt.Sprintf("%s/repos/%s/%s/git/refs", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	payload, _ := json.Marshal(map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": baseSHA,
	})
	resp, err := s.client.doRequest(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return fmt.Errorf("create branch %s in %s/%s: %w", branch, owner, repo, err)
	}
	resp.Body.Close()
	return nil
}

type apiPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	HTMLURL string `json:"html_url"`
}

func (p *apiPullRequest) toDomain() *PullRequest {
	labels := make([]string, len(p.Labels))
	for i, l := range p.Labels {
		labels[i] = l.Name
	}
	return &PullRequest{
		Number: p.Number,
		Title:  p.Title,
		Body:   p.Body,
		State:  p.State,
		Head:   p.Head.Ref,
		Base:   p.Base.Ref,
		Labels: labels,
		URL:    p.HTMLURL,
	}
}

func hasAllLabels(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, l := range have {
		set[strings.ToLower(l)] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[strings.ToLower(w)]; !ok {
			return false
		}
	}
	return true
}
