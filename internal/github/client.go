package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RepoService provides access to repository-related GitHub API endpoints.
type RepoService interface {
	ListByOrg(ctx context.Context, org string) ([]*Repository, error)
	ListByTopic(ctx context.Context, topic string) ([]*Repository, error)
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, string, error) // content, sha, error
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch string, content []byte, sha, message string) error
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
	Method         string // "github-app", "pat", "github-token"
	AppID          string
	PrivateKey     string
	InstallationID string
	Org            string // required for github-app; used to discover installation ID
	Token          string
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
	mu         sync.Mutex
	remaining  int // -1 means unknown
	resetAt    time.Time
	threshold  int // proactive sleep when remaining < threshold
}

// loadPrivateKey returns PEM bytes from either an inline PEM string or a file path.
func loadPrivateKey(value string) ([]byte, error) {
	if strings.HasPrefix(strings.TrimSpace(value), "-----BEGIN") {
		return []byte(value), nil
	}
	return os.ReadFile(value)
}

// generateJWT creates a signed RS256 JWT for GitHub App authentication.
func generateJWT(appID string, key *rsa.PrivateKey) (string, error) {
	encode := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}

	header, err := encode(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}

	now := time.Now().Unix()
	payload, err := encode(map[string]any{"iss": appID, "iat": now - 60, "exp": now + 600})
	if err != nil {
		return "", err
	}

	sigInput := header + "." + payload
	h := sha256.Sum256([]byte(sigInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	return sigInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// exchangeInstallationToken trades a GitHub App JWT for an installation access token.
// If installationID is empty, the installation is auto-discovered from the org name.
func exchangeInstallationToken(ctx context.Context, jwt, baseURL, org, installationID string) (string, error) {
	hc := &http.Client{Timeout: 30 * time.Second}

	makeReq := func(method, url string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, http.NoBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		return req, nil
	}

	if installationID == "" {
		req, err := makeReq(http.MethodGet, baseURL+"/app/installations")
		if err != nil {
			return "", fmt.Errorf("building installations request: %w", err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("listing installations: %w", err)
		}
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("listing installations: status %d: %s", resp.StatusCode, string(data))
		}

		var installations []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&installations); err != nil {
			return "", fmt.Errorf("decoding installations: %w", err)
		}

		for _, inst := range installations {
			if strings.EqualFold(inst.Account.Login, org) {
				installationID = strconv.FormatInt(inst.ID, 10)
				break
			}
		}
		if installationID == "" {
			return "", fmt.Errorf("no GitHub App installation found for org %q", org)
		}
	}

	req, err := makeReq(http.MethodPost, baseURL+"/app/installations/"+installationID+"/access_tokens")
	if err != nil {
		return "", fmt.Errorf("building access_tokens request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging installation token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("exchanging installation token: status %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding installation token: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty token in installation token response")
	}
	return result.Token, nil
}

// NewClient creates a new GitHub API client with the given auth configuration.
func NewClient(auth AuthConfig) (*Client, error) {
	baseURL := "https://api.github.com"
	if u := os.Getenv("GITHUB_API_URL"); u != "" {
		baseURL = u
	}

	var token string
	switch auth.Method {
	case "pat", "github-token":
		if auth.Token == "" {
			return nil, fmt.Errorf("token is required for auth method %q", auth.Method)
		}
		token = auth.Token
	case "github-app":
		if auth.AppID == "" || auth.PrivateKey == "" {
			return nil, fmt.Errorf("app-id and private-key are required for github-app auth")
		}
		pemBytes, err := loadPrivateKey(auth.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("loading private key: %w", err)
		}
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("failed to decode PEM block from private key")
		}
		rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Try PKCS8 (e.g. keys generated by newer tooling)
			key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err2 != nil {
				return nil, fmt.Errorf("parsing private key: %w", err)
			}
			var ok bool
			rsaKey, ok = key.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("private key is not an RSA key")
			}
		}
		jwt, err := generateJWT(auth.AppID, rsaKey)
		if err != nil {
			return nil, fmt.Errorf("generating JWT: %w", err)
		}
		token, err = exchangeInstallationToken(context.Background(), jwt, baseURL, auth.Org, auth.InstallationID)
		if err != nil {
			return nil, fmt.Errorf("github-app auth: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported auth method: %q", auth.Method)
	}

	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    baseURL,
		token:      token,
		rateLimiter: &rateLimiter{
			maxRetries: 5,
			baseDelay:  time.Second,
			remaining:  -1,
			threshold:  50,
		},
	}
	c.Repos = &repoService{client: c}
	c.PullRequests = &pullRequestService{client: c}
	c.Issues = &issueService{client: c}
	return c, nil
}

// updateRateLimitState reads X-RateLimit-Remaining and X-RateLimit-Reset headers
// from a response and updates the rate limiter's shared state.
func (rl *rateLimiter) updateRateLimitState(resp *http.Response) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rl.remaining = n
		}
	}

	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			rl.resetAt = time.Unix(ts, 0)
		}
	}
}

// waitIfBelowThreshold sleeps until the rate limit window resets when remaining
// requests are below the configured threshold. It returns early if the context
// is cancelled. If the remaining count is unknown (-1), it does not block.
func (rl *rateLimiter) waitIfBelowThreshold(ctx context.Context) error {
	rl.mu.Lock()
	remaining := rl.remaining
	resetAt := rl.resetAt
	threshold := rl.threshold
	rl.mu.Unlock()

	if remaining >= 0 && remaining < threshold && time.Now().Before(resetAt) {
		sleepDuration := time.Until(resetAt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDuration):
		}
	}

	return nil
}

func (rl *rateLimiter) do(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= rl.maxRetries; attempt++ {
		// Proactively wait if we know we're close to the rate limit.
		if err := rl.waitIfBelowThreshold(ctx); err != nil {
			return nil, err
		}

		resp, err = fn()
		if err != nil {
			return nil, err
		}

		// Always update rate limit state from response headers.
		rl.updateRateLimitState(resp)

		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}

		// Check if it's a rate limit response
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") != "0" {
			return resp, nil
		}

		resp.Body.Close() //nolint:errcheck // drain body before retry

		if attempt == rl.maxRetries {
			return nil, fmt.Errorf("rate limit exceeded after %d retries", rl.maxRetries)
		}

		// Prefer Retry-After header (secondary rate limits) over exponential backoff.
		delay := rl.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
				delay = time.Duration(seconds) * time.Second
			}
		}

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
	defer resp.Body.Close() //nolint:errcheck // error response body close is best-effort
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
