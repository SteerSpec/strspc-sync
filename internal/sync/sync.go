package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	gosync "sync"
	"time"

	"github.com/google/uuid"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	sslog "github.com/SteerSpec/strspc-sync/internal/log"
	"github.com/SteerSpec/strspc-sync/internal/registry"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

// Result represents the outcome of a sync operation.
type Result struct {
	ID           string       `json:"id"`
	Trigger      string       `json:"trigger"`
	Timestamp    time.Time    `json:"timestamp"`
	Status       string       `json:"status"`
	PRsCreated   int          `json:"prs_created"`
	PRsUpdated   int          `json:"prs_updated"`
	ReposSkipped int          `json:"repos_skipped"`
	Errors       int          `json:"errors"`
	RepoResults  []RepoResult `json:"repo_results"`
}

// RepoResult represents the outcome of syncing a single template to a single repo.
type RepoResult struct {
	Repo       string `json:"repo"`
	Status     string `json:"status"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	Error      string `json:"error,omitempty"`
	TemplateID string `json:"template_id"`
}

// Options configures a sync run.
type Options struct {
	ConfigPath     string
	DryRun         bool
	TargetFilter   string
	TemplateFilter string
	Force          bool
	Trigger        string
}

// Syncer orchestrates template synchronization across repositories.
type Syncer struct {
	cfg          *config.SyncConfig
	ghClient     *gh.Client
	reg          *registry.Registry
	centralOwner string
	centralRepo  string
	stateMu      gosync.Mutex
	state        *state.DeploymentState
	stateSHA     string // SHA of the state file for updates
}

// repoListerAdapter adapts gh.RepoService to registry.RepoLister.
type repoListerAdapter struct {
	repos gh.RepoService
}

func (a *repoListerAdapter) ListByOrg(ctx context.Context, org string) ([]*registry.ResolvedRepo, error) {
	ghRepos, err := a.repos.ListByOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	var result []*registry.ResolvedRepo
	for _, r := range ghRepos {
		result = append(result, &registry.ResolvedRepo{
			Owner:         r.Owner,
			Name:          r.Name,
			FullName:      r.FullName,
			DefaultBranch: r.DefaultBranch,
			Topics:        r.Topics,
		})
	}
	return result, nil
}

func (a *repoListerAdapter) ListByTopic(ctx context.Context, topic string) ([]*registry.ResolvedRepo, error) {
	ghRepos, err := a.repos.ListByTopic(ctx, topic)
	if err != nil {
		return nil, err
	}
	var result []*registry.ResolvedRepo
	for _, r := range ghRepos {
		result = append(result, &registry.ResolvedRepo{
			Owner:         r.Owner,
			Name:          r.Name,
			FullName:      r.FullName,
			DefaultBranch: r.DefaultBranch,
			Topics:        r.Topics,
		})
	}
	return result, nil
}

// New creates a new Syncer. The centralOwner and centralRepo identify the
// repository that holds templates and deployment state.
func New(cfg *config.SyncConfig, ghClient *gh.Client, centralOwner, centralRepo string) *Syncer {
	adapter := &repoListerAdapter{repos: ghClient.Repos}
	return &Syncer{
		cfg:          cfg,
		ghClient:     ghClient,
		reg:          registry.New(adapter),
		centralOwner: centralOwner,
		centralRepo:  centralRepo,
	}
}

// Run executes the sync operation.
func (s *Syncer) Run(ctx context.Context, opts Options) (*Result, error) {
	result := &Result{
		ID:        uuid.New().String(),
		Trigger:   opts.Trigger,
		Timestamp: time.Now(),
	}

	// Resolve targets
	targets, err := s.reg.Resolve(ctx, s.cfg.Targets, s.cfg.Variables)
	if err != nil {
		result.Status = "failed"
		return result, fmt.Errorf("resolving targets: %w", err)
	}

	// Filter targets
	if opts.TargetFilter != "" {
		targets = filterTargets(targets, opts.TargetFilter)
	}

	// Load deployment state from central repo
	s.state, s.stateSHA, err = s.loadState(ctx)
	if err != nil {
		sslog.L().Warn("could not load deployment state, starting fresh",
			"operation", "sync", "err", err)
		s.state = state.NewDeploymentState()
	}

	// Filter templates
	templates := s.cfg.Templates
	if opts.TemplateFilter != "" {
		templates = filterTemplates(templates, opts.TemplateFilter)
	}

	dryRun := opts.DryRun || s.cfg.Sync.DryRun

	// Build work items
	type workItem struct {
		target   registry.ResolvedTarget
		template config.TemplateConfig
	}
	var items []workItem
	for _, target := range targets {
		for _, tmpl := range templates {
			if !shouldApplyTemplate(target, tmpl.ID) {
				continue
			}
			items = append(items, workItem{target: target, template: tmpl})
		}
	}

	// Process with concurrency limit
	concurrency := s.cfg.Sync.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	sem := make(chan struct{}, concurrency)

	var mu gosync.Mutex
	var results []RepoResult

	var wg gosync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(it workItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rr := s.syncOne(ctx, it.target, it.template, dryRun, opts.Force)

			mu.Lock()
			results = append(results, rr)
			mu.Unlock()
		}(item)
	}
	wg.Wait()

	// Tally results
	for _, rr := range results {
		switch rr.Status {
		case "created":
			result.PRsCreated++
		case "updated":
			result.PRsUpdated++
		case "skipped":
			result.ReposSkipped++
		case "error":
			result.Errors++
		}
	}
	result.RepoResults = results
	result.Status = "completed"
	if result.Errors > 0 && result.PRsCreated == 0 && result.PRsUpdated == 0 {
		result.Status = "failed"
	}

	// Save state back (skip on dry run)
	if !dryRun {
		if err := s.saveState(ctx); err != nil {
			sslog.L().Warn("could not save deployment state",
				"operation", "sync", "err", err)
		}
	}

	return result, nil
}

func (s *Syncer) syncOne(ctx context.Context, target registry.ResolvedTarget, tmpl config.TemplateConfig, dryRun, force bool) RepoResult {
	rr := RepoResult{
		Repo:       target.Repo,
		TemplateID: tmpl.ID,
	}

	owner, repo := splitRepo(target.Repo)

	// Read template source from central repo
	// The template source is relative to the config repo
	templateContent, _, err := s.ghClient.Repos.GetFileContent(ctx, s.centralOwner, s.centralRepo, tmpl.Source, "")
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("reading template source: %v", err)
		return rr
	}

	// Merge variables: global < target < template
	vars := make(map[string]string)
	for k, v := range s.cfg.Variables {
		vars[k] = v
	}
	for k, v := range target.Variables {
		vars[k] = v
	}
	for k, v := range tmpl.Variables {
		vars[k] = v
	}
	// Add built-in variables
	vars["repo_name"] = repo
	vars["repo_owner"] = owner
	vars["repo_full_name"] = target.Repo

	// Read existing file from target repo (may not exist)
	existingContent, _, _ := s.ghClient.Repos.GetFileContent(ctx, owner, repo, tmpl.Destination, target.DefaultBranch)

	// Render template
	rendered, err := template.Render(template.Strategy(tmpl.Strategy), templateContent, existingContent, vars)
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("rendering template: %v", err)
		return rr
	}

	// Compute hash
	contentHash := hash.HashBytes(rendered)

	// Check state - skip if hash matches and not forced
	if !force {
		s.stateMu.Lock()
		ts := s.state.GetTemplateState(target.Repo, tmpl.ID)
		s.stateMu.Unlock()
		if ts != nil && ts.Hash == contentHash {
			rr.Status = "skipped"
			return rr
		}
	}

	if dryRun {
		sslog.L().Info("dry-run: would sync template",
			"operation", "sync", "dry_run", true,
			"template_id", tmpl.ID, "repo", target.Repo, "destination", tmpl.Destination)
		rr.Status = "skipped"
		return rr
	}

	// Create or update branch
	branchName := fmt.Sprintf("steerspec-sync/%s/%s", tmpl.ID, tmpl.Version)

	// Get base branch SHA
	baseSHA, err := s.getBaseSHA(ctx, owner, repo, target.DefaultBranch)
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("getting base SHA: %v", err)
		return rr
	}

	// Create the branch. A 422 means it already exists, which is the normal case
	// on re-runs; anything else is a real failure and must not be swallowed —
	// every step below would otherwise operate on a branch that does not exist.
	if err := s.ghClient.PullRequests.CreateBranch(ctx, owner, repo, branchName, baseSHA); err != nil && !isAlreadyExists(err) {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("creating branch: %v", err)
		return rr
	}

	// Get the file SHA on the branch (for updates)
	_, fileSHA, _ := s.ghClient.Repos.GetFileContent(ctx, owner, repo, tmpl.Destination, branchName)

	// Create or update file on branch
	commitMsg := fmt.Sprintf("[SteerSpec] Update %s", tmpl.ID)
	err = s.ghClient.Repos.CreateOrUpdateFile(ctx, owner, repo, tmpl.Destination, branchName, rendered, fileSHA, commitMsg)
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("creating/updating file: %v", err)
		return rr
	}

	// Find existing PR
	prLabel := s.cfg.Sync.PRLabel
	existingPRs, _ := s.ghClient.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{
		State:  "open",
		Head:   branchName,
		Labels: []string{prLabel},
	})

	prTitle := fmt.Sprintf("[SteerSpec] Update %s", tmpl.ID)
	prBody := fmt.Sprintf("## SteerSpec Sync\n\n- **Template:** `%s`\n- **Version:** `%s`\n- **Hash:** `%s`\n\nThis PR was automatically generated by SteerSpec Sync.", tmpl.ID, tmpl.Version, contentHash)

	var pr *gh.PullRequest
	if len(existingPRs) > 0 {
		// Update existing PR
		pr = existingPRs[0]
		err = s.ghClient.PullRequests.Update(ctx, owner, repo, pr.Number, &gh.PullRequestUpdate{
			Title: &prTitle,
			Body:  &prBody,
		})
		if err != nil {
			rr.Status = "error"
			rr.Error = fmt.Sprintf("updating PR: %v", err)
			return rr
		}
		rr.Status = "updated"
		rr.PRNumber = pr.Number
		rr.PRURL = pr.URL
	} else {
		// Create new PR
		pr, err = s.ghClient.PullRequests.Create(ctx, owner, repo, &gh.PullRequestCreate{
			Title:  prTitle,
			Body:   prBody,
			Head:   branchName,
			Base:   target.DefaultBranch,
			Labels: []string{prLabel},
		})
		if err != nil {
			rr.Status = "error"
			rr.Error = fmt.Sprintf("creating PR: %v", err)
			return rr
		}
		rr.Status = "created"
		rr.PRNumber = pr.Number
		rr.PRURL = pr.URL
	}

	// Close stale PRs if configured
	if s.cfg.Sync.CloseStalePRs != nil && *s.cfg.Sync.CloseStalePRs {
		s.closeStalePRs(ctx, owner, repo, tmpl.ID, pr.Number, prLabel)
	}

	// Update deployment state
	s.stateMu.Lock()
	s.state.SetTemplateState(target.Repo, tmpl.ID, state.TemplateState{
		Version:   tmpl.Version,
		Hash:      contentHash,
		Timestamp: time.Now(),
		PRNumber:  pr.Number,
		PRStatus:  "open",
	})
	s.stateMu.Unlock()

	return rr
}

func (s *Syncer) loadState(ctx context.Context) (*state.DeploymentState, string, error) {
	data, sha, err := s.ghClient.Repos.GetFileContent(ctx, s.centralOwner, s.centralRepo, ".steerspec/deployment-state.json", "")
	if err != nil {
		return nil, "", err
	}
	ds, err := state.Load(data)
	return ds, sha, err
}

func (s *Syncer) saveState(ctx context.Context) error {
	data, err := s.state.Save()
	if err != nil {
		return fmt.Errorf("serializing state: %w", err)
	}
	return s.ghClient.Repos.CreateOrUpdateFile(ctx, s.centralOwner, s.centralRepo, ".steerspec/deployment-state.json", "", data, s.stateSHA, "[SteerSpec] Update deployment state")
}

// getBaseSHA resolves the branch a sync branch should be cut from to a commit
// SHA. When no branch is known for the target, it falls back to whatever the
// repository reports as its default.
func (s *Syncer) getBaseSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	if branch == "" {
		var err error
		branch, err = s.ghClient.Repos.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			return "", fmt.Errorf("resolving default branch: %w", err)
		}
	}
	return s.ghClient.Repos.GetBranchSHA(ctx, owner, repo, branch)
}

func (s *Syncer) closeStalePRs(ctx context.Context, owner, repo, templateID string, currentPR int, label string) {
	prs, err := s.ghClient.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{
		State:  "open",
		Labels: []string{label},
	})
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("[SteerSpec] Update %s", templateID)
	for _, pr := range prs {
		if pr.Number != currentPR && strings.HasPrefix(pr.Title, prefix) {
			_ = s.ghClient.PullRequests.Close(ctx, owner, repo, pr.Number)
		}
	}
}

func filterTargets(targets []registry.ResolvedTarget, filter string) []registry.ResolvedTarget {
	var filtered []registry.ResolvedTarget
	for _, t := range targets {
		matched, err := path.Match(filter, t.Repo)
		if err == nil && matched {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func filterTemplates(templates []config.TemplateConfig, filter string) []config.TemplateConfig {
	var filtered []config.TemplateConfig
	for _, t := range templates {
		matched, err := path.Match(filter, t.ID)
		if err == nil && matched {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func shouldApplyTemplate(target registry.ResolvedTarget, templateID string) bool {
	if len(target.IncludeTemplates) > 0 {
		for _, id := range target.IncludeTemplates {
			if id == templateID {
				return true
			}
		}
		return false
	}
	for _, id := range target.ExcludeTemplates {
		if id == templateID {
			return false
		}
	}
	return true
}

// isAlreadyExists reports whether err is the GitHub API's "Reference already
// exists" response. Branch creation is idempotent from the caller's point of
// view: on every run after the first, the sync branch is already there.
//
// The status alone is not enough to decide that. git/refs answers 422 for any
// validation failure — an invalid ref name, or a base SHA that does not resolve
// — and tolerating those would put us back where GH #31 started, with later
// steps running against a branch that was never created. So the message has to
// match as well.
func isAlreadyExists(err error) bool {
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Message), "already exists")
}

func splitRepo(fullName string) (owner, repo string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return fullName, ""
}
