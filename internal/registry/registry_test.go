package registry

import (
	"context"
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/config"
)

type mockLister struct {
	orgRepos   map[string][]*ResolvedRepo
	topicRepos map[string][]*ResolvedRepo
}

func (m *mockLister) ListByOrg(_ context.Context, org string) ([]*ResolvedRepo, error) {
	return m.orgRepos[org], nil
}

func (m *mockLister) ListByTopic(_ context.Context, topic string) ([]*ResolvedRepo, error) {
	return m.topicRepos[topic], nil
}

func newMockLister() *mockLister {
	return &mockLister{
		orgRepos: map[string][]*ResolvedRepo{
			"acme-corp": {
				{Owner: "acme-corp", Name: "api", FullName: "acme-corp/api", DefaultBranch: "main"},
				{Owner: "acme-corp", Name: "frontend", FullName: "acme-corp/frontend", DefaultBranch: "main"},
				{Owner: "acme-corp", Name: "archived-old", FullName: "acme-corp/archived-old", DefaultBranch: "main"},
				{Owner: "acme-corp", Name: ".github", FullName: "acme-corp/.github", DefaultBranch: "main"},
			},
		},
		topicRepos: map[string][]*ResolvedRepo{
			"claude-managed": {
				{Owner: "other-org", Name: "tool", FullName: "other-org/tool", DefaultBranch: "main", Topics: []string{"claude-managed"}},
			},
		},
	}
}

func TestResolveGlobPattern(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 4 {
		t.Fatalf("expected 4 repos, got %d", len(results))
	}
}

func TestResolveExclusion(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
		Exclude: []string{"acme-corp/archived-*", "acme-corp/.github"},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(results))
	}

	for _, r := range results {
		if r.Repo == "acme-corp/archived-old" || r.Repo == "acme-corp/.github" {
			t.Errorf("excluded repo %q should not be in results", r.Repo)
		}
	}
}

func TestResolveTopics(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Topics: []string{"claude-managed"},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(results))
	}
	if results[0].Repo != "other-org/tool" {
		t.Errorf("expected other-org/tool, got %s", results[0].Repo)
	}
}

func TestResolveDeduplication(t *testing.T) {
	lister := &mockLister{
		orgRepos: map[string][]*ResolvedRepo{
			"acme-corp": {
				{Owner: "acme-corp", Name: "api", FullName: "acme-corp/api", DefaultBranch: "main"},
			},
		},
		topicRepos: map[string][]*ResolvedRepo{
			"managed": {
				{Owner: "acme-corp", Name: "api", FullName: "acme-corp/api", DefaultBranch: "main"},
			},
		},
	}

	reg := New(lister)
	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
		Topics:  []string{"managed"},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 repo (deduplicated), got %d", len(results))
	}
}

func TestResolveOverrides(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
		Exclude: []string{"acme-corp/archived-*", "acme-corp/.github"},
		Overrides: []config.TargetOverride{
			{
				Repo:             "acme-corp/api",
				DefaultBranch:    "develop",
				Variables:        map[string]string{"project_type": "api"},
				ExcludeTemplates: []string{"claude-settings"},
			},
		},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var apiTarget *ResolvedTarget
	for i := range results {
		if results[i].Repo == "acme-corp/api" {
			apiTarget = &results[i]
			break
		}
	}

	if apiTarget == nil {
		t.Fatal("acme-corp/api not found in results")
	}
	if apiTarget.DefaultBranch != "develop" {
		t.Errorf("expected develop branch, got %s", apiTarget.DefaultBranch)
	}
	if apiTarget.Variables["project_type"] != "api" {
		t.Errorf("expected project_type=api, got %s", apiTarget.Variables["project_type"])
	}
	if len(apiTarget.ExcludeTemplates) != 1 || apiTarget.ExcludeTemplates[0] != "claude-settings" {
		t.Errorf("expected exclude_templates=[claude-settings], got %v", apiTarget.ExcludeTemplates)
	}
}

func TestResolveVariableMerging(t *testing.T) {
	reg := New(newMockLister())

	globalVars := map[string]string{
		"org_name":      "acme-corp",
		"default_model": "claude-sonnet",
	}

	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
		Exclude: []string{"acme-corp/archived-*", "acme-corp/.github"},
		Overrides: []config.TargetOverride{
			{
				Repo:      "acme-corp/frontend",
				Variables: map[string]string{"default_model": "claude-opus"},
			},
		},
	}

	results, err := reg.Resolve(context.Background(), targets, globalVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if r.Variables["org_name"] != "acme-corp" {
			t.Errorf("%s: expected org_name=acme-corp, got %s", r.Repo, r.Variables["org_name"])
		}
	}

	var frontend *ResolvedTarget
	var api *ResolvedTarget
	for i := range results {
		switch results[i].Repo {
		case "acme-corp/frontend":
			frontend = &results[i]
		case "acme-corp/api":
			api = &results[i]
		}
	}

	if frontend == nil || api == nil {
		t.Fatal("expected both frontend and api targets")
	}

	// Target override should take precedence
	if frontend.Variables["default_model"] != "claude-opus" {
		t.Errorf("frontend: expected default_model=claude-opus, got %s", frontend.Variables["default_model"])
	}
	// Global var should be preserved when no override
	if api.Variables["default_model"] != "claude-sonnet" {
		t.Errorf("api: expected default_model=claude-sonnet, got %s", api.Variables["default_model"])
	}
}

func TestResolveSorted(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Include: []string{"acme-corp/*"},
	}

	results, err := reg.Resolve(context.Background(), targets, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Repo < results[i-1].Repo {
			t.Errorf("results not sorted: %s comes after %s", results[i].Repo, results[i-1].Repo)
		}
	}
}

func TestResolveInvalidPattern(t *testing.T) {
	reg := New(newMockLister())

	targets := config.TargetsConfig{
		Include: []string{"no-slash"},
	}

	_, err := reg.Resolve(context.Background(), targets, nil)
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}
