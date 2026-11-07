package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// graphqlEndpoint returns the GraphQL URL: GITHUB_GRAPHQL_URL when set, otherwise one derived from the REST base
// URL. github.com serves both under api.github.com; GHES serves REST at /api/v3 and GraphQL at /api/graphql.
func (c *Client) graphqlEndpoint() string {
	if c.graphqlURL != "" {
		return c.graphqlURL
	}
	base := strings.TrimSuffix(c.baseURL, "/")
	if strings.HasSuffix(base, "/api/v3") {
		return strings.TrimSuffix(base, "/v3") + "/graphql"
	}
	return base + "/graphql"
}

// doGraphQL posts a query and decodes its data into out. A non-2xx status and a 200 that carries GraphQL errors
// are both errors: GitHub reports most mutation failures the second way.
func (c *Client) doGraphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	resp, err := c.doRequest(ctx, http.MethodPost, c.graphqlEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return err
	}
	var body struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := decodeBody(resp, &body); err != nil {
		return err
	}
	if len(body.Errors) > 0 {
		msgs := make([]string, len(body.Errors))
		for i, e := range body.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
	}
	if out == nil || len(body.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(body.Data, out); err != nil {
		return fmt.Errorf("decode graphql data: %w", err)
	}
	return nil
}

const createCommitOnBranchMutation = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) { commit { oid } }
}`

// errFileChanged reports that the file no longer has the blob sha the caller read.
var errFileChanged = errors.New("file changed since it was read")

// escapeRepoPath escapes each segment of a repository path for use in a REST URL, keeping the separators.
func escapeRepoPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// splitCommitMessage separates a git commit message into the headline and body that createCommitOnBranch takes.
func splitCommitMessage(message string) (headline, body string) {
	headline, body, _ = strings.Cut(message, "\n")
	return headline, strings.TrimLeft(body, "\n")
}

// commitFile commits content to path on branch with createCommitOnBranch. GitHub signs those commits for every
// token type, so they pass Signed Commit rulesets; Contents API commits made with a PAT are unsigned (GH #126).
func (s *repoService) commitFile(ctx context.Context, owner, repo, path, branch, head string, content []byte,
	message string,
) error {
	headline, body := splitCommitMessage(message)
	msg := map[string]string{"headline": headline}
	if body != "" {
		msg["body"] = body
	}
	input := map[string]any{
		"branch": map[string]string{
			"repositoryNameWithOwner": owner + "/" + repo,
			"branchName":              branch,
		},
		"message":         msg,
		"expectedHeadOid": head,
		"fileChanges": map[string]any{
			"additions": []map[string]string{{"path": path, "contents": base64.StdEncoding.EncodeToString(content)}},
		},
	}
	var out struct {
		CreateCommitOnBranch struct {
			Commit *struct {
				OID string `json:"oid"`
			} `json:"commit"`
		} `json:"createCommitOnBranch"`
	}
	if err := s.client.doGraphQL(ctx, createCommitOnBranchMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if c := out.CreateCommitOnBranch.Commit; c == nil || c.OID == "" {
		return errors.New("createCommitOnBranch returned no commit")
	}
	return nil
}
