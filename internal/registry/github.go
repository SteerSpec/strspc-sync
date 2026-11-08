package registry

import (
	"context"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

// GitHubLister adapts gh.RepoService to RepoLister, so sync and conflict resolve targets the same way.
type GitHubLister struct {
	Repos gh.RepoService
}

// ListByOrg lists the repositories of org.
func (a *GitHubLister) ListByOrg(ctx context.Context, org string) ([]*ResolvedRepo, error) {
	repos, err := a.Repos.ListByOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	return toResolved(repos), nil
}

// ListByTopic lists the repositories of org that carry topic.
func (a *GitHubLister) ListByTopic(ctx context.Context, topic, org string) ([]*ResolvedRepo, error) {
	repos, err := a.Repos.ListByTopic(ctx, topic, org)
	if err != nil {
		return nil, err
	}
	return toResolved(repos), nil
}

func toResolved(repos []*gh.Repository) []*ResolvedRepo {
	var out []*ResolvedRepo
	for _, r := range repos {
		out = append(out, &ResolvedRepo{
			Owner:         r.Owner,
			Name:          r.Name,
			FullName:      r.FullName,
			DefaultBranch: r.DefaultBranch,
			Topics:        r.Topics,
			Visibility:    r.Visibility,
		})
	}
	return out
}
