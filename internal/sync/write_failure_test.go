package sync

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

func singleRepoSetup() (*mockRepoService, *mockPRService) {
	repoSvc := newMockRepoService()
	prSvc := newMockPRService()
	repoSvc.repos["testorg"] = []*gh.Repository{
		{Owner: "testorg", Name: "repo-a", FullName: "testorg/repo-a", DefaultBranch: "main"},
	}
	repoSvc.files["testorg/central/templates/CLAUDE.md/"] = []byte("# Hello {{org_name}}")
	return repoSvc, prSvc
}

const syncBranch = "steerspec-sync/claude-md/1.0.0"

// GH #150: a branch this run created is deleted when the file write fails, so it is not left with no commit of its
// own.
func TestFailedWriteDeletesCreatedBranch(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	repoSvc.createFileErr = map[string]error{
		"testorg/repo-a/CLAUDE.md/" + syncBranch: errors.New("graphql: Repository rule violations found"),
	}

	result, err := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 1 || result.PRsCreated != 0 {
		t.Fatalf("want one error and no PR: %+v", result)
	}
	if !slices.Equal(prSvc.deleted, []string{syncBranch}) {
		t.Errorf("deleted branches = %v, want [%s]", prSvc.deleted, syncBranch)
	}
}

// The read of the file on the new branch failing (other than 404) also stops before the write, so the branch is
// deleted too.
func TestFailedBranchReadDeletesCreatedBranch(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	repoSvc.getFileErr = map[string]error{
		"testorg/repo-a/CLAUDE.md@" + syncBranch: &gh.APIError{StatusCode: 502, Message: "bad gateway"},
	}

	result, err := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 1 {
		t.Fatalf("want one error: %+v", result)
	}
	if !slices.Equal(prSvc.deleted, []string{syncBranch}) {
		t.Errorf("deleted branches = %v, want [%s]", prSvc.deleted, syncBranch)
	}
}

// A concurrent run that found the branch already there may have committed to it since this run created it. A
// branch whose head moved off the base it was created at is not deleted.
func TestFailedWriteKeepsBranchThatMoved(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	repoSvc.branchSHAs = map[string]string{syncBranch: "other-run-commit"}
	repoSvc.createFileErr = map[string]error{
		"testorg/repo-a/CLAUDE.md/" + syncBranch: errors.New("bad gateway"),
	}

	if _, err := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	if len(prSvc.deleted) != 0 {
		t.Errorf("deleted branches = %v, want none", prSvc.deleted)
	}
}

// A branch that already existed may carry an open sync PR: a failed write must not delete it.
func TestFailedWriteKeepsExistingBranch(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	prSvc.createBranchErr = &gh.APIError{StatusCode: 422, Message: "Reference already exists"}
	repoSvc.createFileErr = map[string]error{
		"testorg/repo-a/CLAUDE.md/" + syncBranch: errors.New("bad gateway"),
	}

	result, err := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 1 {
		t.Fatalf("want one error: %+v", result)
	}
	if len(prSvc.deleted) != 0 {
		t.Errorf("deleted branches = %v, want none", prSvc.deleted)
	}
}

// A successful write keeps the branch.
func TestSuccessfulWriteKeepsBranch(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	if _, err := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central").
		Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	if len(prSvc.deleted) != 0 {
		t.Errorf("deleted branches = %v, want none", prSvc.deleted)
	}
}

// GH #151: a ruleset rejecting the deployment-state commit is reported with the fix, not only the raw API error.
func TestStateWriteRuleRejectionExplained(t *testing.T) {
	repoSvc, prSvc := singleRepoSetup()
	raw := errors.New("create/update file testorg/central/.steerspec/deployment-state.json: graphql: " +
		"Repository rule violations found\n\nChanges must be made through a pull request.")
	repoSvc.createFileErr = map[string]error{"testorg/central/.steerspec/deployment-state.json/": raw}

	s := New(makeTestConfig(), &gh.Client{Repos: repoSvc, PullRequests: prSvc}, "testorg", "central")
	if _, err := s.Run(context.Background(), Options{Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	err := s.saveState(context.Background())
	if err == nil {
		t.Fatal("want the state write to fail")
	}
	for _, want := range []string{"deployment state not saved", "testorg/central", "bypass", "Writing the deployment state"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !errors.Is(err, raw) {
		t.Error("the API error must stay wrapped")
	}
}

func TestIsRuleRejection(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"graphql: Repository rule violations found", true},
		{"409: Repository rule violations found for refs/heads/main", true},
		{"Protected branch update failed for refs/heads/main.", true},
		{"graphql: Expected branch to point to abc", false},
		{"bad gateway", false},
	}
	for _, tt := range tests {
		if got := isRuleRejection(errors.New(tt.msg)); got != tt.want {
			t.Errorf("isRuleRejection(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}
