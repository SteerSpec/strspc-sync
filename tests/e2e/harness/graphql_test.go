//go:build e2e

package harness

import (
	"strings"
	"testing"
)

func TestGraphQLError(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"success", `{"data":{"createCommitOnBranch":{"commit":{"oid":"abc"}}}}`, ""},
		{"one error", `{"errors":[{"message":"Expected head oid mismatch"}]}`, "Expected head oid mismatch"},
		{"several errors", `{"errors":[{"message":"a"},{"message":"b"}]}`, "a; b"},
		{"not json", `<html>`, "decoding GraphQL response"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := graphqlError("deleting x", []byte(c.body))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want it to contain %q", err, c.wantErr)
			}
		})
	}
}
