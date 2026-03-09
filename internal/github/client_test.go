package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := NewClient(AuthConfig{Method: "pat", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.rateLimiter.baseDelay = 10 * time.Millisecond // speed up tests
	return c
}

// generateTestRSAKey returns a fresh RSA key and its inline PEM string.
func generateTestRSAKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, string(pemBytes)
}

func TestNewClient_AuthValidation(t *testing.T) {
	_, err := NewClient(AuthConfig{Method: "pat", Token: ""})
	if err == nil {
		t.Fatal("expected error for empty token")
	}

	_, err = NewClient(AuthConfig{Method: "unknown", Token: "x"})
	if err == nil {
		t.Fatal("expected error for unknown method")
	}

	c, err := NewClient(AuthConfig{Method: "pat", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "tok" {
		t.Fatalf("expected token 'tok', got %q", c.token)
	}

	_, err = NewClient(AuthConfig{Method: "github-app", AppID: "", PrivateKey: "somekey"})
	if err == nil {
		t.Fatal("expected error for github-app with empty AppID")
	}
}

func TestLoadPrivateKey(t *testing.T) {
	_, pemStr := generateTestRSAKey(t)

	// Inline PEM returns bytes directly
	got, err := loadPrivateKey(pemStr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pemStr {
		t.Fatal("expected inline PEM to be returned as-is")
	}

	// File path reads the file
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, []byte(pemStr), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = loadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pemStr {
		t.Fatal("expected file content to match PEM")
	}
}

func TestGenerateJWT(t *testing.T) {
	_, pemStr := generateTestRSAKey(t)

	pemBytes := []byte(pemStr)
	block, _ := pem.Decode(pemBytes)
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	token, err := generateJWT("app-123", rsaKey)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	// Verify header claims alg + typ
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal("decoding header:", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatal("unmarshaling header:", err)
	}
	if header["alg"] != "RS256" {
		t.Fatalf("expected alg RS256, got %q", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Fatalf("expected typ JWT, got %q", header["typ"])
	}

	// Verify payload claims
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("decoding payload:", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatal("unmarshaling payload:", err)
	}
	if claims["iss"] != "app-123" {
		t.Fatalf("expected iss 'app-123', got %v", claims["iss"])
	}
	iat, ok := claims["iat"].(float64)
	if !ok || iat <= 0 {
		t.Fatalf("expected positive iat, got %v", claims["iat"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= iat {
		t.Fatalf("expected exp > iat, got exp=%v iat=%v", exp, iat)
	}
}

func TestNewClient_GithubApp_WithDiscovery(t *testing.T) {
	_, pemStr := generateTestRSAKey(t)

	var discoveryCallCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		discoveryCallCount.Add(1)
		// Verify JWT bearer token format (3 dot-separated base64url parts)
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if len(strings.Split(token, ".")) != 3 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
			{"id": 42, "account": map[string]string{"login": "myorg"}},
		})
	})
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "ghs_discovered_token"}) //nolint:errcheck
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)

	c, err := NewClient(AuthConfig{
		Method:     "github-app",
		AppID:      "12345",
		PrivateKey: pemStr,
		Org:        "myorg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "ghs_discovered_token" {
		t.Fatalf("expected 'ghs_discovered_token', got %q", c.token)
	}
	if discoveryCallCount.Load() != 1 {
		t.Fatalf("expected exactly 1 discovery call, got %d", discoveryCallCount.Load())
	}
}

func TestNewClient_GithubApp_InstallationIDShortcut(t *testing.T) {
	_, pemStr := generateTestRSAKey(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, _ *http.Request) {
		// Should never be called when InstallationID is set
		t.Error("discovery endpoint called unexpectedly")
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/app/installations/99/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "ghs_shortcut_token"}) //nolint:errcheck
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)

	c, err := NewClient(AuthConfig{
		Method:         "github-app",
		AppID:          "12345",
		PrivateKey:     pemStr,
		InstallationID: "99",
		Org:            "myorg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "ghs_shortcut_token" {
		t.Fatalf("expected 'ghs_shortcut_token', got %q", c.token)
	}
}

func TestRateLimiterRetry(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"rate limit exceeded"}`) //nolint:errcheck // test handler write
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	resp, err := c.doRequest(context.Background(), http.MethodGet, c.baseURL+"/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", calls.Load())
	}
}

func TestRateLimiterExhausted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"rate limit exceeded"}`) //nolint:errcheck
	})

	c := testClient(t, mux)
	c.rateLimiter.maxRetries = 1
	_, err := c.doRequest(context.Background(), http.MethodGet, c.baseURL+"/test", nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestRepoListByOrg(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/myorg/repos", func(w http.ResponseWriter, r *http.Request) {
		repos := []apiRepo{
			{Name: "repo1", FullName: "myorg/repo1", DefaultBranch: "main", Owner: struct{ Login string }{Login: "myorg"}},
			{Name: "repo2", FullName: "myorg/repo2", DefaultBranch: "master", Owner: struct{ Login string }{Login: "myorg"}, Archived: true},
		}
		json.NewEncoder(w).Encode(repos) //nolint:errcheck
	})

	c := testClient(t, mux)
	repos, err := c.Repos.ListByOrg(context.Background(), "myorg")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].Name != "repo1" || repos[0].Owner != "myorg" {
		t.Fatalf("unexpected repo: %+v", repos[0])
	}
	if !repos[1].Archived {
		t.Fatal("expected repo2 to be archived")
	}
}

func TestRepoListByOrgPagination(t *testing.T) {
	page1 := make([]apiRepo, 100)
	for i := range page1 {
		page1[i] = apiRepo{Name: fmt.Sprintf("repo-%d", i), FullName: fmt.Sprintf("org/repo-%d", i), Owner: struct{ Login string }{Login: "org"}}
	}
	page2 := []apiRepo{
		{Name: "repo-100", FullName: "org/repo-100", Owner: struct{ Login string }{Login: "org"}},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/org/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(page2) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(page1) //nolint:errcheck
	})

	c := testClient(t, mux)
	repos, err := c.Repos.ListByOrg(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 101 {
		t.Fatalf("expected 101 repos, got %d", len(repos))
	}
}

func TestRepoListByTopic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/repositories", func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Items []apiRepo `json:"items"`
		}{
			Items: []apiRepo{
				{Name: "r1", FullName: "o/r1", Owner: struct{ Login string }{Login: "o"}, Topics: []string{"steerspec"}},
			},
		}
		json.NewEncoder(w).Encode(result) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	repos, err := c.Repos.ListByTopic(context.Background(), "steerspec")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "r1" {
		t.Fatalf("unexpected repos: %+v", repos)
	}
}

func TestRepoGetFileContent(t *testing.T) {
	content := "hello world"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/contents/path/to/file", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // test handler write
			"content":  encoded,
			"sha":      "abc123",
			"encoding": "base64",
		})
	})

	c := testClient(t, mux)
	data, sha, err := c.Repos.GetFileContent(context.Background(), "o", "r", "path/to/file", "main")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("expected %q, got %q", content, string(data))
	}
	if sha != "abc123" {
		t.Fatalf("expected sha abc123, got %q", sha)
	}
}

func TestRepoCreateOrUpdateFile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/contents/file.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"content":{"sha":"newsha"}}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "file.txt", "main", []byte("data"), "oldsha", "update file")
	if err != nil {
		t.Fatal(err)
	}
}

func TestPRCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test handler write
			"number":   42,
			"title":    "Test PR",
			"state":    "open",
			"head":     map[string]string{"ref": "feature"},
			"base":     map[string]string{"ref": "main"},
			"html_url": "https://github.com/o/r/pull/42",
		})
	})

	c := testClient(t, mux)
	pr, err := c.PullRequests.Create(context.Background(), "o", "r", &PullRequestCreate{
		Title: "Test PR",
		Body:  "body",
		Head:  "feature",
		Base:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 {
		t.Fatalf("expected PR #42, got #%d", pr.Number)
	}
}

func TestPRUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(apiPullRequest{Number: 1, Title: "Updated", State: "open"}) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	title := "Updated"
	err := c.PullRequests.Update(context.Background(), "o", "r", 1, &PullRequestUpdate{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPRClose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(apiPullRequest{Number: 1, State: "closed"}) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	err := c.PullRequests.Close(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPRCreateBranch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/git/refs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ref":"refs/heads/new-branch"}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	err := c.PullRequests.CreateBranch(context.Background(), "o", "r", "new-branch", "abc123")
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssueCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test handler write
			"number":   10,
			"title":    "Bug",
			"state":    "open",
			"html_url": "https://github.com/o/r/issues/10",
			"labels":   []map[string]string{{"name": "bug"}},
		})
	})

	c := testClient(t, mux)
	issue, err := c.Issues.Create(context.Background(), "o", "r", &IssueCreate{
		Title:  "Bug",
		Body:   "body",
		Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 10 {
		t.Fatalf("expected issue #10, got #%d", issue.Number)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Fatalf("unexpected labels: %v", issue.Labels)
	}
}

func TestIssueUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(apiIssue{Number: 10, Title: "Updated", State: "open"})
	})

	c := testClient(t, mux)
	title := "Updated"
	err := c.Issues.Update(context.Background(), "o", "r", 10, &IssueUpdate{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssueClose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(apiIssue{Number: 10, State: "closed"})
	})

	c := testClient(t, mux)
	err := c.Issues.Close(context.Background(), "o", "r", 10)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssueListFiltersPRs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		issues := []map[string]any{
			{"number": 1, "title": "Real issue", "state": "open"},
			{"number": 2, "title": "Actually a PR", "state": "open", "pull_request": map[string]string{"url": "https://api.github.com/repos/o/r/pulls/2"}},
		}
		json.NewEncoder(w).Encode(issues)
	})

	c := testClient(t, mux)
	issues, err := c.Issues.List(context.Background(), "o", "r", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (PR filtered), got %d", len(issues))
	}
	if issues[0].Number != 1 {
		t.Fatalf("expected issue #1, got #%d", issues[0].Number)
	}
}

func TestErrorHandling404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	_, err := c.Repos.GetDefaultBranch(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		// wrapped error
		return
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("expected status 404, got %d", apiErr.StatusCode)
	}
}

func TestErrorHandling403NonRateLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Repository access blocked"}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	_, err := c.Repos.GetDefaultBranch(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestAuthHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		accept := r.Header.Get("Accept")
		if accept != "application/vnd.github+json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	resp, err := c.doRequest(context.Background(), http.MethodGet, c.baseURL+"/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (auth or accept header mismatch)", resp.StatusCode)
	}
}

func TestPRCreateRequestBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if body["title"] != "Test Title" {
			t.Errorf("expected title 'Test Title', got %v", body["title"])
		}
		if body["body"] != "Test Body" {
			t.Errorf("expected body 'Test Body', got %v", body["body"])
		}
		if body["head"] != "feature-branch" {
			t.Errorf("expected head 'feature-branch', got %v", body["head"])
		}
		if body["base"] != "main" {
			t.Errorf("expected base 'main', got %v", body["base"])
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   1,
			"title":    "Test Title",
			"state":    "open",
			"head":     map[string]string{"ref": "feature-branch"},
			"base":     map[string]string{"ref": "main"},
			"html_url": "https://github.com/o/r/pull/1",
		})
	})
	// Handle label addition (PR create adds labels separately)
	mux.HandleFunc("/repos/o/r/issues/1/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[{"name":"steerspec-sync"}]`) //nolint:errcheck // test handler write
	})

	c := testClient(t, mux)
	pr, err := c.PullRequests.Create(context.Background(), "o", "r", &PullRequestCreate{
		Title:  "Test Title",
		Body:   "Test Body",
		Head:   "feature-branch",
		Base:   "main",
		Labels: []string{"steerspec-sync"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 1 {
		t.Errorf("expected PR #1, got #%d", pr.Number)
	}
}

func TestIssueCreateLabels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		labels, ok := body["labels"].([]any)
		if !ok {
			t.Error("expected labels array in request body")
		} else if len(labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(labels))
		} else {
			if labels[0] != "steerspec-drift" {
				t.Errorf("expected first label 'steerspec-drift', got %v", labels[0])
			}
			if labels[1] != "automated" {
				t.Errorf("expected second label 'automated', got %v", labels[1])
			}
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   5,
			"title":    body["title"],
			"state":    "open",
			"html_url": "https://github.com/o/r/issues/5",
			"labels":   []map[string]string{{"name": "steerspec-drift"}, {"name": "automated"}},
		})
	})

	c := testClient(t, mux)
	issue, err := c.Issues.Create(context.Background(), "o", "r", &IssueCreate{
		Title:  "Drift detected",
		Body:   "drift body",
		Labels: []string{"steerspec-drift", "automated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 5 {
		t.Errorf("expected issue #5, got #%d", issue.Number)
	}
	if len(issue.Labels) != 2 {
		t.Errorf("expected 2 labels on created issue, got %d", len(issue.Labels))
	}
}
