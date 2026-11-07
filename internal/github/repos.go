package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrSymlink is returned by GetFileContent when the path is a symlink.
var ErrSymlink = errors.New("path is a symlink")

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

// ListByTopic finds repositories carrying a topic within a single organization.
// The org qualifier is not optional: a bare "topic:x" search returns every repo
// the token can see in any organization, which for a token-based auth method
// means sync could target repositories the config never named (GH #41).
func (s *repoService) ListByTopic(ctx context.Context, topic, org string) ([]*Repository, error) {
	// Refuse rather than emit "topic:x org:", which GitHub may treat as an
	// unscoped search — the exact failure this parameter exists to prevent.
	if org == "" {
		return nil, fmt.Errorf("list repos by topic %q: org must not be empty", topic)
	}

	var all []*Repository
	page := 1

	for {
		q := url.QueryEscape(fmt.Sprintf("topic:%s org:%s", topic, org))
		u := fmt.Sprintf("%s/search/repositories?q=%s&per_page=100&page=%d", s.client.baseURL, q, page)
		resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			return nil, fmt.Errorf("list repos by topic %q in org %q: %w", topic, org, err)
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

// GetBranchSHA resolves a branch name to the commit SHA it points at. Callers
// that need a base for a new ref must use this rather than the branch name —
// the git/refs endpoint takes a SHA and rejects a name.
func (s *repoService) GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s",
		s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if err := checkResponse(resp); err != nil {
		return "", fmt.Errorf("get branch ref %s in %s/%s: %w", branch, owner, repo, err)
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := decodeBody(resp, &ref); err != nil {
		return "", err
	}
	if ref.Object.SHA == "" {
		return "", fmt.Errorf("get branch ref %s in %s/%s: empty sha in response", branch, owner, repo)
	}
	return ref.Object.SHA, nil
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
		Type     string `json:"type"`
		Content  string `json:"content"`
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
	}
	if err := decodeBody(resp, &content); err != nil {
		return nil, "", err
	}
	// A symlink has no content, only a target path. Writing to it through the
	// Contents API stores the new bytes as the target (GH #82).
	if content.Type == "symlink" {
		return nil, content.SHA, fmt.Errorf("get file %s/%s/%s: %w", owner, repo, path, ErrSymlink)
	}

	decoded, err := base64.StdEncoding.DecodeString(content.Content)
	if err != nil {
		return nil, "", fmt.Errorf("decode file content: %w", err)
	}
	return decoded, content.SHA, nil
}

// IsSymlink reads the git tree of the parent directory and checks the entry's mode (120000 is a symlink).
// Neither Contents API response shows it: a single-file GET follows a working link and returns the target as a
// regular file, and the directory listing reports the link itself with type "file". A missing directory or
// entry is not a symlink; a truncated tree cannot decide, so it is an error.
func (s *repoService) IsSymlink(ctx context.Context, owner, repo, path, ref string) (bool, error) {
	dir, name := "", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir, name = path[:i], path[i+1:]
	}
	if ref == "" {
		ref = "HEAD"
	}
	treeish := ref
	if dir != "" {
		treeish += ":" + dir
	}
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s", s.client.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(treeish))
	resp, err := s.client.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close() //nolint:errcheck // not-found body close is best-effort
		return false, nil
	}
	if err := checkResponse(resp); err != nil {
		return false, fmt.Errorf("read tree %s/%s@%s: %w", owner, repo, treeish, err)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := decodeBody(resp, &tree); err != nil {
		return false, err
	}
	for _, e := range tree.Tree {
		if e.Path == name {
			return e.Mode == "120000", nil
		}
	}
	if tree.Truncated {
		return false, fmt.Errorf("tree %s/%s@%s is truncated: cannot tell whether %s is a symlink", owner, repo, treeish, path)
	}
	return false, nil
}

// CreateOrUpdateFile commits content to path on branch, or on the default branch when branch is empty. path is a
// repository path, not a URL segment. When sha is non-empty it is the blob the caller read, and the write fails
// if the file at the branch head has changed since, as the Contents API did with a stale sha.
//
// The commit goes through GraphQL createCommitOnBranch rather than the Contents API: GitHub signs it whatever the
// token type, so it passes Signed Commit rulesets (GH #126). The file is written with mode 100644.
func (s *repoService) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch string, content []byte, sha, message string) error {
	wrap := func(err error) error { return fmt.Errorf("create/update file %s/%s/%s: %w", owner, repo, path, err) }

	// The mutation has no default branch, and the deployment state passes none (GH #43).
	if branch == "" {
		b, err := s.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			return wrap(err)
		}
		branch = b
	}
	head, err := s.GetBranchSHA(ctx, owner, repo, branch)
	if err != nil {
		return wrap(err)
	}
	if sha != "" {
		// Read at the head the commit is based on: a change after this check moves the head, and the mutation's
		// expectedHeadOid then refuses the commit.
		_, current, err := s.GetFileContent(ctx, owner, repo, escapeRepoPath(path), head)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			current, err = "", nil
		}
		if err != nil {
			return wrap(err)
		}
		if current != sha {
			if current == "" {
				current = "none"
			}
			return wrap(fmt.Errorf("%w (blob %s, expected %s)", errFileChanged, current, sha))
		}
	}
	if err := s.commitFile(ctx, owner, repo, path, branch, head, content, message); err != nil {
		return wrap(err)
	}
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
	Private       bool                   `json:"private"`
	Visibility    string                 `json:"visibility"`
}

func (r *apiRepo) toDomain() *Repository {
	return &Repository{
		Owner:         r.Owner.Login,
		Name:          r.Name,
		FullName:      r.FullName,
		DefaultBranch: r.DefaultBranch,
		Topics:        r.Topics,
		Archived:      r.Archived,
		Visibility:    r.visibility(),
	}
}

// visibility returns "public", "internal" or "private". The API's visibility
// field wins; the private flag covers responses that omit it.
func (r *apiRepo) visibility() string {
	if r.Visibility != "" {
		return r.Visibility
	}
	if r.Private {
		return "private"
	}
	return "public"
}

func decodeBody(resp *http.Response, v any) error {
	defer resp.Body.Close() //nolint:errcheck // response body close is best-effort
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
