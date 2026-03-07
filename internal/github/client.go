package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// RepoService provides access to repository-related GitHub API endpoints.
type RepoService interface {
	ListByOrg(ctx context.Context, org string) ([]*Repository, error)
	ListByTopic(ctx context.Context, topic string) ([]*Repository, error)
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, string, error) // content, sha, error
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch string, content []byte, sha string, message string) error
}

// PullRequestService provides access to pull-request-related GitHub API endpoints.
type PullRequestService interface {
	List(ctx context.Context, owner, repo string, opts *PullRequestListOptions) ([]*PullRequest, error)
	Create(ctx context.Context, owner, repo string, pr *PullRequestCreate) (*PullRequest, error)
	Update(ctx context.Context, owner, repo string, number int, pr *PullRequestUpdate) error
	Close(ctx context.Context, owner, repo string, number int) error
	CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error
}

// IssueService provides access to issue-related GitHub API endpoints.
type IssueService interface {
	List(ctx context.Context, owner, repo string, opts *IssueListOptions) ([]*Issue, error)
	Create(ctx context.Context, owner, repo string, issue *IssueCreate) (*Issue, error)
	Update(ctx context.Context, owner, repo string, number int, issue *IssueUpdate) error
	Close(ctx context.Context, owner, repo string, number int) error
}

type Repository struct {
	Owner         string
	Name          string
	FullName      string
	DefaultBranch string
	Topics        []string
	Archived      bool
}

type PullRequest struct {
	Number int
	Title  string
	Body   string
	State  string
	Head   string
	Base   string
	Labels []string
	URL    string
}

type PullRequestCreate struct {
	Title  string
	Body   string
	Head   string
	Base   string
	Labels []string
}

type PullRequestUpdate struct {
	Title *string
	Body  *string
}

type PullRequestListOptions struct {
	State  string
	Head   string
	Labels []string
}

type Issue struct {
	Number int
	Title  string
	Body   string
	State  string
	Labels []string
	URL    string
}

type IssueCreate struct {
	Title  string
	Body   string
	Labels []string
}

type IssueUpdate struct {
	Title *string
	Body  *string
	State *string
}

type IssueListOptions struct {
	State  string
	Labels []string
}

type AuthConfig struct {
	Method     string // "github-app", "pat", "github-token"
	AppID      string
	PrivateKey string
	Token      string
}

// Client is the GitHub API client.
type Client struct {
	Repos        RepoService
	PullRequests PullRequestService
	Issues       IssueService
	httpClient   *http.Client
	baseURL      string
	token        string
	rateLimiter  *rateLimiter
}

type rateLimiter struct {
	maxRetries int
	baseDelay  time.Duration
}

// NewClient creates a new GitHub API client with the given auth configuration.
func NewClient(auth AuthConfig) (*Client, error) {
	var token string
	switch auth.Method {
	case "pat", "github-token":
		if auth.Token == "" {
			return nil, fmt.Errorf("token is required for auth method %q", auth.Method)
		}
		token = auth.Token
	case "github-app":
		// TODO: Implement full GitHub App JWT / installation token exchange.
		// For now, accept a pre-generated installation token via auth.Token.
		if auth.Token == "" {
			return nil, fmt.Errorf("pre-generated installation token is required for github-app auth (full JWT auth not yet implemented)")
		}
		token = auth.Token
	default:
		return nil, fmt.Errorf("unsupported auth method: %q", auth.Method)
	}

	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.github.com",
		token:      token,
		rateLimiter: &rateLimiter{
			maxRetries: 5,
			baseDelay:  time.Second,
		},
	}
	c.Repos = &repoService{client: c}
	c.PullRequests = &pullRequestService{client: c}
	c.Issues = &issueService{client: c}
	return c, nil
}

func (rl *rateLimiter) do(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= rl.maxRetries; attempt++ {
		resp, err = fn()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}

		// Check if it's a rate limit response
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") != "0" {
			return resp, nil
		}

		resp.Body.Close()

		if attempt == rl.maxRetries {
			return nil, fmt.Errorf("rate limit exceeded after %d retries", rl.maxRetries)
		}

		delay := rl.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return resp, nil
}

// doRequest performs an authenticated HTTP request through the rate limiter.
func (c *Client) doRequest(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	return c.rateLimiter.do(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.httpClient.Do(req)
	})
}

// APIError represents an error response from the GitHub API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github API error (status %d): %s", e.StatusCode, e.Message)
}

// checkResponse returns an error if the response status code indicates failure.
func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	msg := string(data)
	var apiResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &apiResp) == nil && apiResp.Message != "" {
		msg = apiResp.Message
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}
