package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	c, err = NewClient(AuthConfig{Method: "github-app", Token: "inst-tok"})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "inst-tok" {
		t.Fatalf("expected token 'inst-tok', got %q", c.token)
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
			fmt.Fprint(w, `{"message":"rate limit exceeded"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	})

	c := testClient(t, mux)
	resp, err := c.doRequest(context.Background(), http.MethodGet, c.baseURL+"/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
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
		fmt.Fprint(w, `{"message":"rate limit exceeded"}`)
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
		json.NewEncoder(w).Encode(repos)
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
			json.NewEncoder(w).Encode(page2)
			return
		}
		json.NewEncoder(w).Encode(page1)
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
		json.NewEncoder(w).Encode(result)
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
		json.NewEncoder(w).Encode(map[string]string{
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
		fmt.Fprint(w, `{"content":{"sha":"newsha"}}`)
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
		json.NewEncoder(w).Encode(map[string]any{
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
		json.NewEncoder(w).Encode(apiPullRequest{Number: 1, Title: "Updated", State: "open"})
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
		json.NewEncoder(w).Encode(apiPullRequest{Number: 1, State: "closed"})
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
		fmt.Fprint(w, `{"ref":"refs/heads/new-branch"}`)
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
		json.NewEncoder(w).Encode(map[string]any{
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
		fmt.Fprint(w, `{"message":"Not Found"}`)
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
		fmt.Fprint(w, `{"message":"Repository access blocked"}`)
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
		fmt.Fprint(w, `{}`)
	})

	c := testClient(t, mux)
	resp, err := c.doRequest(context.Background(), http.MethodGet, c.baseURL+"/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
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
		fmt.Fprint(w, `[{"name":"steerspec-sync"}]`)
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
