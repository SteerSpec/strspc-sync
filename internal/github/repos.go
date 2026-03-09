package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type repoService struct {
	client *Client
}

func (s *repoService) ListByOrg(ctx context.Context, org string) ([]*Repository, error) {
	var all []*Repository
	page := 1

	for {
		u := fmt.Sprintf("%s/orgs/%s/repos?per_page=100&page=%d", s.client.baseURL, url.PathEscape(org), page)
		resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			return nil, fmt.Errorf("list repos by org %q: %w", org, err)
		}

		var repos []apiRepo
		if err := decodeBody(resp, &repos); err != nil {
			return nil, err
		}
		for _, r := range repos {
			all = append(all, r.toDomain())
		}
		if len(repos) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (s *repoService) ListByTopic(ctx context.Context, topic string) ([]*Repository, error) {
	var all []*Repository
	page := 1

	for {
		q := url.QueryEscape(fmt.Sprintf("topic:%s", topic))
		u := fmt.Sprintf("%s/search/repositories?q=%s&per_page=100&page=%d", s.client.baseURL, q, page)
		resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			return nil, fmt.Errorf("list repos by topic %q: %w", topic, err)
		}

		var result struct {
			Items []apiRepo `json:"items"`
		}
		if err := decodeBody(resp, &result); err != nil {
			return nil, err
		}
		for _, r := range result.Items {
			all = append(all, r.toDomain())
		}
		if len(result.Items) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (s *repoService) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo))
	resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if err := checkResponse(resp); err != nil {
		return "", fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}

	var r apiRepo
	if err := decodeBody(resp, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

func (s *repoService) GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), path)
	if ref != "" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	if err := checkResponse(resp); err != nil {
		return nil, "", fmt.Errorf("get file %s/%s/%s: %w", owner, repo, path, err)
	}

	var content struct {
		Content  string `json:"content"`
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
	}
	if err := decodeBody(resp, &content); err != nil {
		return nil, "", err
	}

	decoded, err := base64.StdEncoding.DecodeString(content.Content)
	if err != nil {
		return nil, "", fmt.Errorf("decode file content: %w", err)
	}
	return decoded, content.SHA, nil
}

func (s *repoService) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch string, content []byte, sha, message string) error {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), path)

	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	if sha != "" {
		body["sha"] = sha
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := s.client.doRequest(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return fmt.Errorf("create/update file %s/%s/%s: %w", owner, repo, path, err)
	}
	resp.Body.Close()
	return nil
}

// apiRepo is the JSON shape returned by the GitHub API.
type apiRepo struct {
	Owner         struct{ Login string } `json:"owner"`
	Name          string                 `json:"name"`
	FullName      string                 `json:"full_name"`
	DefaultBranch string                 `json:"default_branch"`
	Topics        []string               `json:"topics"`
	Archived      bool                   `json:"archived"`
}

func (r *apiRepo) toDomain() *Repository {
	return &Repository{
		Owner:         r.Owner.Login,
		Name:          r.Name,
		FullName:      r.FullName,
		DefaultBranch: r.DefaultBranch,
		Topics:        r.Topics,
		Archived:      r.Archived,
	}
}

func decodeBody(resp *http.Response, v any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
