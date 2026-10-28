//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

const opTimeout = 60 * time.Second

// IsNotFound reports whether err is a GitHub 404.
func IsNotFound(err error) bool {
	var ae *gh.APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// api performs a raw authenticated request for endpoints the client does not wrap. It returns the status and body;
// a transport failure is the only error.
func (e *Env) api(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encoding body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: reading body: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// GetFile reads path from repo at ref ("" is the default branch). It returns the content and blob sha; a missing
// file satisfies IsNotFound.
func (e *Env) GetFile(ctx context.Context, repo, path, ref string) ([]byte, string, error) {
	content, sha, err := e.Client.Repos.GetFileContent(ctx, e.Org, repo, escapePath(path), ref)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s/%s: %w", repo, path, err)
	}
	return content, sha, nil
}

// ReadFile returns the content of path on repo's default branch and fails t if it cannot be read.
func (e *Env) ReadFile(t *testing.T, repo, path string) []byte {
	t.Helper()
	return e.ReadFileAt(t, repo, path, "")
}

// ReadFileAt is ReadFile at a branch, tag or commit.
func (e *Env) ReadFileAt(t *testing.T, repo, path, ref string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	content, _, err := e.GetFile(ctx, repo, path, ref)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return content
}

// FileExists reports whether path exists on repo's default branch.
func (e *Env) FileExists(t *testing.T, repo, path string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	_, _, err := e.GetFile(ctx, repo, path, "")
	if err == nil {
		return true
	}
	if IsNotFound(err) {
		return false
	}
	t.Fatalf("FileExists: %v", err)
	return false
}

// PutFile creates or updates path on the default branch, looking up the current sha when the file exists.
func (e *Env) PutFile(ctx context.Context, repo, path string, content []byte, message string) error {
	_, sha, err := e.GetFile(ctx, repo, path, "")
	if err != nil && !IsNotFound(err) {
		return err
	}
	if err := e.Client.Repos.CreateOrUpdateFile(ctx, e.Org, repo, escapePath(path), "", content, sha, message); err != nil {
		return fmt.Errorf("writing %s/%s: %w", repo, path, err)
	}
	return nil
}

// WriteFile creates or updates path on the default branch of repo (the App commit is signed by GitHub).
func (e *Env) WriteFile(t *testing.T, repo, path string, content []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	if err := e.PutFile(ctx, repo, path, content, "test(e2e): write "+path+" ["+e.RunID+"]"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// ResetFile makes path hold exactly content, writing only when it differs so a no-op reset leaves no commit.
func (e *Env) ResetFile(t *testing.T, repo, path string, content []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	if err := e.resetFile(ctx, repo, path, content); err != nil {
		t.Fatalf("ResetFile: %v", err)
	}
}

func (e *Env) resetFile(ctx context.Context, repo, path string, content []byte) error {
	cur, sha, err := e.GetFile(ctx, repo, path, "")
	if err != nil && !IsNotFound(err) {
		return err
	}
	if err == nil && bytes.Equal(cur, content) {
		return nil
	}
	if err := e.Client.Repos.CreateOrUpdateFile(ctx, e.Org, repo, escapePath(path), "", content, sha,
		"test(e2e): reset "+path+" ["+e.RunID+"]"); err != nil {
		return fmt.Errorf("resetting %s/%s: %w", repo, path, err)
	}
	return nil
}

// DeleteFile removes path from repo's default branch; a file that is already gone is not an error.
func (e *Env) DeleteFile(t *testing.T, repo, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	_, sha, err := e.GetFile(ctx, repo, path, "")
	if IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	status, body, err := e.api(ctx, http.MethodDelete,
		"/repos/"+url.PathEscape(e.Org)+"/"+url.PathEscape(repo)+"/contents/"+escapePath(path),
		map[string]string{"message": "test(e2e): delete " + path + " [" + e.RunID + "]", "sha": sha})
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("DeleteFile %s/%s: status %d: %s", repo, path, status, body)
	}
}
