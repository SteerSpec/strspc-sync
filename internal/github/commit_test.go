package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// commitServer fakes the endpoints CreateOrUpdateFile uses: the repository (default branch), branch refs, the
// file at the branch head, and the GraphQL createCommitOnBranch mutation (GH #126).
type commitServer struct {
	defaultBranch string
	heads         map[string]string // branch -> head oid
	blobSHA       string            // blob sha of the file at the head; "" means the file does not exist
	contentRefs   []string          // ref query of each contents GET
	contentPaths  []string          // escaped URL path of each contents GET
	graphqlStatus int
	graphqlBody   string
	mutations     []map[string]any // decoded "input" variable of each GraphQL call
}

func newCommitServer() *commitServer {
	return &commitServer{
		defaultBranch: "main",
		heads:         map[string]string{"main": "headmain", "steerspec-sync/claude-md/1.0.0": "headsync"},
		graphqlStatus: http.StatusOK,
		graphqlBody:   `{"data":{"createCommitOnBranch":{"commit":{"oid":"newoid"}}}}`,
	}
}

func (s *commitServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"default_branch":%q}`, s.defaultBranch) //nolint:errcheck // test handler write
	})
	mux.HandleFunc("/repos/o/r/git/ref/heads/", func(w http.ResponseWriter, r *http.Request) {
		branch := strings.TrimPrefix(r.URL.Path, "/repos/o/r/git/ref/heads/")
		head, ok := s.heads[branch]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"object":{"sha":%q}}`, head) //nolint:errcheck // test handler write
	})
	mux.HandleFunc("/repos/o/r/contents/", func(w http.ResponseWriter, r *http.Request) {
		s.contentRefs = append(s.contentRefs, r.URL.Query().Get("ref"))
		s.contentPaths = append(s.contentPaths, r.URL.EscapedPath())
		if s.blobSHA == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"type":"file","sha":%q,"encoding":"base64","content":""}`, s.blobSHA) //nolint:errcheck // test
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("GraphQL method = %s, want POST", r.Method)
		}
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding GraphQL request: %v", err)
		}
		if !strings.Contains(req.Query, "createCommitOnBranch") {
			t.Errorf("query does not call createCommitOnBranch: %s", req.Query)
		}
		s.mutations = append(s.mutations, req.Variables.Input)
		w.WriteHeader(s.graphqlStatus)
		fmt.Fprint(w, s.graphqlBody) //nolint:errcheck // test handler write
	})
	return mux
}

// field walks a decoded JSON object along keys and returns the value found there.
func field(t *testing.T, v any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %q: %v is not an object", k, v)
		}
		v = m[k]
	}
	return v
}

func TestCreateOrUpdateFileCommitsThroughGraphQL(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		wantBranch string
		wantHead   string
	}{
		{"named branch", "steerspec-sync/claude-md/1.0.0", "steerspec-sync/claude-md/1.0.0", "headsync"},
		// The deployment state passes no branch and relies on the default (GH #43); the mutation has no default.
		{"empty branch resolves the default branch", "", "main", "headmain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCommitServer()
			c := testClient(t, srv.handler(t))

			err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", ".claude/a b.md", tt.branch,
				[]byte("hello"), "", "chore(sync): update claude-md to 1.0.0")
			if err != nil {
				t.Fatal(err)
			}
			if len(srv.mutations) != 1 {
				t.Fatalf("GraphQL calls = %d, want 1", len(srv.mutations))
			}
			in := srv.mutations[0]
			if got := field(t, in, "branch", "repositoryNameWithOwner"); got != "o/r" {
				t.Errorf("repositoryNameWithOwner = %v, want o/r", got)
			}
			if got := field(t, in, "branch", "branchName"); got != tt.wantBranch {
				t.Errorf("branchName = %v, want %s", got, tt.wantBranch)
			}
			if got := field(t, in, "expectedHeadOid"); got != tt.wantHead {
				t.Errorf("expectedHeadOid = %v, want %s", got, tt.wantHead)
			}
			if got := field(t, in, "message", "headline"); got != "chore(sync): update claude-md to 1.0.0" {
				t.Errorf("headline = %v", got)
			}
			if body, ok := field(t, in, "message").(map[string]any)["body"]; ok {
				t.Errorf("single-line message sent a body: %v", body)
			}
			adds, ok := field(t, in, "fileChanges", "additions").([]any)
			if !ok || len(adds) != 1 {
				t.Fatalf("additions = %v, want one entry", field(t, in, "fileChanges", "additions"))
			}
			// The path is a repository path, not a URL segment: it must not be escaped.
			if got := field(t, adds[0], "path"); got != ".claude/a b.md" {
				t.Errorf("path = %v, want .claude/a b.md", got)
			}
			if got := field(t, adds[0], "contents"); got != base64.StdEncoding.EncodeToString([]byte("hello")) {
				t.Errorf("contents = %v, want base64 of hello", got)
			}
		})
	}
}

func TestCreateOrUpdateFileSplitsMultilineMessage(t *testing.T) {
	srv := newCommitServer()
	c := testClient(t, srv.handler(t))

	err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "f.txt", "main", []byte("x"), "",
		"chore: subject\n\nfirst line\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	msg := field(t, srv.mutations[0], "message")
	if got := field(t, msg, "headline"); got != "chore: subject" {
		t.Errorf("headline = %q", got)
	}
	if got := field(t, msg, "body"); got != "first line\nsecond line" {
		t.Errorf("body = %q", got)
	}
}

// A non-empty sha is the blob the caller read; the REST call refused a stale one with 409, and the deployment
// state relies on that to notice a concurrent run.
func TestCreateOrUpdateFileChecksExpectedSHA(t *testing.T) {
	tests := []struct {
		name    string
		blobSHA string
		sha     string
		wantErr string
	}{
		{"matching sha commits", "blob1", "blob1", ""},
		{"stale sha is refused", "blob2", "blob1", "changed since it was read"},
		{"deleted file is refused", "", "blob1", "changed since it was read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCommitServer()
			srv.blobSHA = tt.blobSHA
			c := testClient(t, srv.handler(t))

			err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "f.txt", "main", []byte("x"), tt.sha, "m")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(srv.mutations) != 1 {
					t.Errorf("GraphQL calls = %d, want 1", len(srv.mutations))
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if len(srv.mutations) != 0 {
					t.Errorf("committed despite the sha mismatch")
				}
			}
			// The file is read at the head the commit is based on, not at the branch name.
			if len(srv.contentRefs) != 1 || srv.contentRefs[0] != "headmain" {
				t.Errorf("contents read at %v, want [headmain]", srv.contentRefs)
			}
		})
	}
}

// The sha check reads the file over REST, so the repository path must be escaped there even though the
// mutation takes it raw.
func TestCreateOrUpdateFileEscapesPathForSHACheck(t *testing.T) {
	srv := newCommitServer()
	srv.blobSHA = "blob1"
	c := testClient(t, srv.handler(t))
	if err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "dir/a b.txt", "main", []byte("x"), "blob1", "m"); err != nil {
		t.Fatal(err)
	}
	if want := "/repos/o/r/contents/dir/a%20b.txt"; len(srv.contentPaths) != 1 || srv.contentPaths[0] != want {
		t.Errorf("contents GET paths = %v, want [%s]", srv.contentPaths, want)
	}
	if got := field(t, srv.mutations[0], "fileChanges", "additions").([]any)[0]; field(t, got, "path") != "dir/a b.txt" {
		t.Errorf("mutation path = %v, want dir/a b.txt", field(t, got, "path"))
	}
}

func TestCreateOrUpdateFileSkipsSHACheckWhenEmpty(t *testing.T) {
	srv := newCommitServer()
	c := testClient(t, srv.handler(t))
	if err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "f.txt", "main", []byte("x"), "", "m"); err != nil {
		t.Fatal(err)
	}
	if len(srv.contentRefs) != 0 {
		t.Errorf("contents read %d times, want 0", len(srv.contentRefs))
	}
}

func TestCreateOrUpdateFileGraphQLFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			"GraphQL errors", http.StatusOK,
			`{"data":null,"errors":[{"message":"Expected branch to point to \"abc\""}]}`,
			`Expected branch to point to "abc"`,
		},
		{"HTTP failure", http.StatusBadGateway, `{"message":"Bad Gateway"}`, "502"},
		{"no commit returned", http.StatusOK, `{"data":{"createCommitOnBranch":{"commit":null}}}`, "no commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCommitServer()
			srv.graphqlStatus, srv.graphqlBody = tt.status, tt.body
			c := testClient(t, srv.handler(t))
			c.rateLimiter.maxRetries = 0

			err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "f.txt", "main", []byte("x"), "", "m")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "o/r/f.txt") {
				t.Errorf("err = %v, want it to name the file", err)
			}
		})
	}
}

func TestCreateOrUpdateFileMissingBranch(t *testing.T) {
	srv := newCommitServer()
	c := testClient(t, srv.handler(t))
	err := c.Repos.CreateOrUpdateFile(context.Background(), "o", "r", "f.txt", "nope", []byte("x"), "", "m")
	if err == nil {
		t.Fatal("want an error for a branch that does not exist")
	}
	if len(srv.mutations) != 0 {
		t.Error("committed to a branch that does not exist")
	}
}

func TestGraphQLEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		override string
		want     string
	}{
		{"github.com", "https://api.github.com", "", "https://api.github.com/graphql"},
		{"github.com trailing slash", "https://api.github.com/", "", "https://api.github.com/graphql"},
		{"GHES", "https://ghe.example.com/api/v3", "", "https://ghe.example.com/api/graphql"},
		{"GHES trailing slash", "https://ghe.example.com/api/v3/", "", "https://ghe.example.com/api/graphql"},
		{"explicit URL wins", "https://api.github.com", "https://proxy.example.com/gql", "https://proxy.example.com/gql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{baseURL: tt.baseURL, graphqlURL: tt.override}
			if got := c.graphqlEndpoint(); got != tt.want {
				t.Errorf("graphqlEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClientReadsGraphQLURL(t *testing.T) {
	t.Setenv("GITHUB_GRAPHQL_URL", "https://ghe.example.com/api/graphql")
	c, err := NewClient(AuthConfig{Method: "pat", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if c.graphqlURL != "https://ghe.example.com/api/graphql" {
		t.Errorf("graphqlURL = %q", c.graphqlURL)
	}
}
