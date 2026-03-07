package registry

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/SteerSpec/strspc-sync/internal/config"
)

// ResolvedRepo represents a repository resolved from the GitHub API.
type ResolvedRepo struct {
	Owner         string
	Name          string
	FullName      string // "owner/repo"
	DefaultBranch string
	Topics        []string
}

// RepoLister is the interface needed from the GitHub client.
type RepoLister interface {
	ListByOrg(ctx context.Context, org string) ([]*ResolvedRepo, error)
	ListByTopic(ctx context.Context, topic string) ([]*ResolvedRepo, error)
}

// ResolvedTarget represents a fully resolved target repository.
type ResolvedTarget struct {
	Repo             string
	DefaultBranch    string
	Variables        map[string]string
	ExcludeTemplates []string
	IncludeTemplates []string
	Enabled          bool
}

// Registry resolves target configurations into concrete repository targets.
type Registry struct {
	lister RepoLister
}

// New creates a new Registry.
func New(lister RepoLister) *Registry {
	return &Registry{lister: lister}
}

// Resolve resolves the targets configuration into a list of ResolvedTargets.
func (r *Registry) Resolve(ctx context.Context, targets config.TargetsConfig, globalVars map[string]string) ([]ResolvedTarget, error) {
	repoMap := make(map[string]*ResolvedRepo)

	// Process include patterns
	for _, pattern := range targets.Include {
		org, globPat, err := parsePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern %q: %w", pattern, err)
		}

		repos, err := r.lister.ListByOrg(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("listing repos for org %q: %w", org, err)
		}

		for _, repo := range repos {
			matched, err := path.Match(globPat, repo.Name)
			if err != nil {
				return nil, fmt.Errorf("matching pattern %q against %q: %w", globPat, repo.Name, err)
			}
			if matched {
				repoMap[repo.FullName] = repo
			}
		}
	}

	// Process topic-based includes
	for _, topic := range targets.Topics {
		repos, err := r.lister.ListByTopic(ctx, topic)
		if err != nil {
			return nil, fmt.Errorf("listing repos for topic %q: %w", topic, err)
		}
		for _, repo := range repos {
			repoMap[repo.FullName] = repo
		}
	}

	// Apply exclusions
	for _, pattern := range targets.Exclude {
		org, globPat, err := parsePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
		}

		for fullName, repo := range repoMap {
			if repo.Owner != org {
				continue
			}
			matched, err := path.Match(globPat, repo.Name)
			if err != nil {
				return nil, fmt.Errorf("matching exclude pattern %q against %q: %w", globPat, repo.Name, err)
			}
			if matched {
				delete(repoMap, fullName)
			}
		}
	}

	// Build override index
	overrideIndex := make(map[string]config.TargetOverride, len(targets.Overrides))
	for _, o := range targets.Overrides {
		overrideIndex[o.Repo] = o
	}

	// Build resolved targets
	var results []ResolvedTarget
	for _, repo := range repoMap {
		vars := make(map[string]string, len(globalVars))
		for k, v := range globalVars {
			vars[k] = v
		}

		rt := ResolvedTarget{
			Repo:          repo.FullName,
			DefaultBranch: repo.DefaultBranch,
			Variables:     vars,
			Enabled:       true,
		}

		if override, ok := overrideIndex[repo.FullName]; ok {
			if override.DefaultBranch != "" {
				rt.DefaultBranch = override.DefaultBranch
			}
			for k, v := range override.Variables {
				rt.Variables[k] = v
			}
			rt.ExcludeTemplates = override.ExcludeTemplates
			rt.IncludeTemplates = override.IncludeTemplates
		}

		results = append(results, rt)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Repo < results[j].Repo
	})

	return results, nil
}

// parsePattern splits "owner/glob" into owner and glob parts.
func parsePattern(pattern string) (org, glob string, err error) {
	parts := strings.SplitN(pattern, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("pattern must be in 'owner/glob' format")
	}
	return parts[0], parts[1], nil
}
