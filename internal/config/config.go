package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type SyncConfig struct {
	Version     string            `yaml:"version"`
	CentralRepo string            `yaml:"central-repo,omitempty"`
	Auth        AuthConfig        `yaml:"auth"`
	Variables   map[string]string `yaml:"variables,omitempty"`
	Templates   []TemplateConfig  `yaml:"templates"`
	Targets     TargetsConfig     `yaml:"targets"`
	Sync        SyncBehavior      `yaml:"sync,omitempty"`
	Monitor     MonitorBehavior   `yaml:"monitor,omitempty"`
	Conflicts   ConflictBehavior  `yaml:"conflicts,omitempty"`
}

type AuthConfig struct {
	Method         string `yaml:"method"`
	AppID          string `yaml:"app-id,omitempty"`
	PrivateKey     string `yaml:"private-key,omitempty"`
	InstallationID string `yaml:"installation-id,omitempty"`
	Token          string `yaml:"token,omitempty"`
}

type TemplateConfig struct {
	ID      string `yaml:"id"`
	Version string `yaml:"version"`
	Type    string `yaml:"type"`
	Source  string `yaml:"source"`
	// Sources picks a source by target visibility (public, internal, private).
	// A value is a path in the central repo or "owner/repo:path" in another
	// repo; Source is the fallback for visibilities not listed.
	Sources     map[string]string `yaml:"sources,omitempty"`
	Destination string            `yaml:"destination"`
	Strategy    string            `yaml:"strategy"`
	Variables   map[string]string `yaml:"variables,omitempty"`
}

type TargetsConfig struct {
	Include   []string         `yaml:"include"`
	Exclude   []string         `yaml:"exclude,omitempty"`
	Topics    []string         `yaml:"topics,omitempty"`
	Overrides []TargetOverride `yaml:"overrides,omitempty"`
}

type TargetOverride struct {
	Repo             string            `yaml:"repo"`
	DefaultBranch    string            `yaml:"default_branch,omitempty"`
	Variables        map[string]string `yaml:"variables,omitempty"`
	ExcludeTemplates []string          `yaml:"exclude_templates,omitempty"`
	IncludeTemplates []string          `yaml:"include_templates,omitempty"`
}

type SyncBehavior struct {
	Concurrency   int    `yaml:"concurrency,omitempty"`
	PRLabel       string `yaml:"pr-label,omitempty"`
	CloseStalePRs *bool  `yaml:"close-stale-prs,omitempty"`
	DryRun        bool   `yaml:"dry-run,omitempty"`
	// CommitPrefix is what precedes ": update <template-id> to <version>" in
	// the commit messages sync writes to target repos and to the central repo's
	// deployment state. Defaults to DefaultCommitPrefix, a conventional-commit
	// type and scope, so commitlint and semantic-PR checks accept sync commits.
	CommitPrefix string `yaml:"commit-prefix,omitempty"`
	// PRTitlePrefix is the same for SyncPullRequest titles. Defaults to
	// DefaultPRTitlePrefix.
	PRTitlePrefix string `yaml:"pr-title-prefix,omitempty"`
}

// DefaultCommitPrefix and DefaultPRTitlePrefix are the conventional-commit
// type(scope) used for sync commits and PR titles when the config does not set
// sync.commit-prefix or sync.pr-title-prefix.
const (
	DefaultCommitPrefix  = "chore(steerspec-sync)"
	DefaultPRTitlePrefix = "chore(steerspec-sync)"
)

type MonitorBehavior struct {
	Schedule   string `yaml:"schedule,omitempty"`
	IssueLabel string `yaml:"issue-label,omitempty"`
	AutoClose  *bool  `yaml:"auto-close,omitempty"`
}

type ConflictBehavior struct {
	Schedule   string `yaml:"schedule,omitempty"`
	Tiers      []int  `yaml:"tiers,omitempty"`
	IssueLabel string `yaml:"issue-label,omitempty"`
	// AutoClose closes a conflict issue once the conflict it reported is gone
	// (same semantics as monitor.auto-close). Defaults to true.
	AutoClose *bool `yaml:"auto-close,omitempty"`
}

var (
	validAuthMethods   = map[string]bool{"github-app": true, "pat": true, "github-token": true}
	validTemplateTypes = map[string]bool{"claude-md": true, "skill": true, "agent": true, "config": true, "custom": true}
	validStrategies    = map[string]bool{"mustache": true, "marker": true, "full-replace": true}
	validConflictTiers = map[int]bool{1: true, 2: true, 3: true}
	validVisibilities  = map[string]bool{"public": true, "internal": true, "private": true}
	semverRE           = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	sourceRepoRE       = regexp.MustCompile(`^([a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+):(.+)$`)
)

// ParseSource splits a template source into its repository ("owner/repo") and
// path. A bare path has an empty repository and means the central repo.
func ParseSource(src string) (repo, path string, err error) {
	if src == "" {
		return "", "", fmt.Errorf("source must not be empty")
	}
	if m := sourceRepoRE.FindStringSubmatch(src); m != nil {
		return m[1], m[2], nil
	}
	if strings.Contains(src, ":") {
		return "", "", fmt.Errorf("source %q must be a path or owner/repo:path", src)
	}
	return "", src, nil
}

func Load(path string) (*SyncConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg SyncConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	ApplyDefaults(&cfg)

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func ApplyDefaults(cfg *SyncConfig) {
	if cfg.Sync.Concurrency == 0 {
		cfg.Sync.Concurrency = 5
	}
	if cfg.Sync.PRLabel == "" {
		cfg.Sync.PRLabel = "steerspec-sync"
	}
	if cfg.Sync.CloseStalePRs == nil {
		t := true
		cfg.Sync.CloseStalePRs = &t
	}
	if cfg.Sync.CommitPrefix == "" {
		cfg.Sync.CommitPrefix = DefaultCommitPrefix
	}
	if cfg.Sync.PRTitlePrefix == "" {
		cfg.Sync.PRTitlePrefix = DefaultPRTitlePrefix
	}

	if cfg.Monitor.Schedule == "" {
		cfg.Monitor.Schedule = "0 8 * * 1"
	}
	if cfg.Monitor.IssueLabel == "" {
		cfg.Monitor.IssueLabel = "steerspec-drift"
	}
	if cfg.Monitor.AutoClose == nil {
		t := true
		cfg.Monitor.AutoClose = &t
	}

	if cfg.Conflicts.Schedule == "" {
		cfg.Conflicts.Schedule = "0 9 * * 1"
	}
	if len(cfg.Conflicts.Tiers) == 0 {
		cfg.Conflicts.Tiers = []int{1, 2}
	}
	if cfg.Conflicts.IssueLabel == "" {
		cfg.Conflicts.IssueLabel = "steerspec-conflict"
	}
	if cfg.Conflicts.AutoClose == nil {
		t := true
		cfg.Conflicts.AutoClose = &t
	}
}

func Validate(cfg *SyncConfig) error {
	if cfg.Version == "" {
		return fmt.Errorf("version is required")
	}

	if !validAuthMethods[cfg.Auth.Method] {
		return fmt.Errorf("auth.method must be one of: github-app, pat, github-token")
	}

	switch cfg.Auth.Method {
	case "github-app":
		// app-id and private-key may arrive via env vars; validated post-resolution in newGHClient()
	case "pat":
		if cfg.Auth.Token == "" {
			return fmt.Errorf("auth.token is required for pat method")
		}
	case "github-token":
		// Token can be empty in config; resolved from GITHUB_TOKEN env var at runtime
	}

	for name, v := range map[string]string{"sync.commit-prefix": cfg.Sync.CommitPrefix, "sync.pr-title-prefix": cfg.Sync.PRTitlePrefix} {
		if strings.ContainsAny(v, "\r\n") || strings.TrimSpace(v) != v {
			return fmt.Errorf("%s %q must be a single line without surrounding whitespace", name, v)
		}
	}

	if cfg.CentralRepo != "" {
		if matched, _ := regexp.MatchString(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`, cfg.CentralRepo); !matched {
			return fmt.Errorf("central-repo must be in owner/repo format, got %q", cfg.CentralRepo)
		}
	}

	if len(cfg.Templates) == 0 {
		return fmt.Errorf("at least one template is required")
	}

	seen := make(map[string]bool)
	for i, tmpl := range cfg.Templates {
		if tmpl.ID == "" {
			return fmt.Errorf("templates[%d].id is required", i)
		}
		if seen[tmpl.ID] {
			return fmt.Errorf("duplicate template id: %s", tmpl.ID)
		}
		seen[tmpl.ID] = true

		if tmpl.Version == "" {
			return fmt.Errorf("templates[%d].version is required", i)
		}
		if !semverRE.MatchString(tmpl.Version) {
			return fmt.Errorf("templates[%d].version %q must be a semver (e.g. \"1.0.0\")", i, tmpl.Version)
		}

		if tmpl.Source == "" && len(tmpl.Sources) == 0 {
			return fmt.Errorf("templates[%d] needs source or sources", i)
		}
		for vis, src := range tmpl.Sources {
			if !validVisibilities[vis] {
				return fmt.Errorf("templates[%d].sources key %q must be one of: public, internal, private", i, vis)
			}
			if _, _, err := ParseSource(src); err != nil {
				return fmt.Errorf("templates[%d].sources.%s: %w", i, vis, err)
			}
		}

		if !validTemplateTypes[tmpl.Type] {
			return fmt.Errorf("templates[%d].type %q is not valid", i, tmpl.Type)
		}
		if !validStrategies[tmpl.Strategy] {
			return fmt.Errorf("templates[%d].strategy %q is not valid", i, tmpl.Strategy)
		}
	}

	if len(cfg.Targets.Include) == 0 {
		return fmt.Errorf("targets.include must not be empty")
	}

	for _, tier := range cfg.Conflicts.Tiers {
		if !validConflictTiers[tier] {
			return fmt.Errorf("conflict tier %d is not valid (must be 1, 2, or 3)", tier)
		}
	}

	return nil
}
