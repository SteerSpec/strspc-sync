package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

type SyncConfig struct {
	Version   string            `yaml:"version"`
	Auth      AuthConfig        `yaml:"auth"`
	Variables map[string]string `yaml:"variables,omitempty"`
	Templates []TemplateConfig  `yaml:"templates"`
	Targets   TargetsConfig     `yaml:"targets"`
	Sync      SyncBehavior      `yaml:"sync,omitempty"`
	Monitor   MonitorBehavior   `yaml:"monitor,omitempty"`
	Conflicts ConflictBehavior  `yaml:"conflicts,omitempty"`
}

type AuthConfig struct {
	Method         string `yaml:"method"`
	AppID          string `yaml:"app-id,omitempty"`
	PrivateKey     string `yaml:"private-key,omitempty"`
	InstallationID string `yaml:"installation-id,omitempty"`
	Token          string `yaml:"token,omitempty"`
}

type TemplateConfig struct {
	ID          string            `yaml:"id"`
	Version     string            `yaml:"version"`
	Type        string            `yaml:"type"`
	Source      string            `yaml:"source"`
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
}

type MonitorBehavior struct {
	Schedule   string `yaml:"schedule,omitempty"`
	IssueLabel string `yaml:"issue-label,omitempty"`
	AutoClose  *bool  `yaml:"auto-close,omitempty"`
}

type ConflictBehavior struct {
	Schedule   string `yaml:"schedule,omitempty"`
	Tiers      []int  `yaml:"tiers,omitempty"`
	IssueLabel string `yaml:"issue-label,omitempty"`
}

var (
	validAuthMethods   = map[string]bool{"github-app": true, "pat": true, "github-token": true}
	validTemplateTypes = map[string]bool{"claude-md": true, "skill": true, "agent": true, "config": true, "custom": true}
	validStrategies    = map[string]bool{"mustache": true, "marker": true, "full-replace": true}
	validConflictTiers = map[int]bool{1: true, 2: true, 3: true}
	semverRE           = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

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
