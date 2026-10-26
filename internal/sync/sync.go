package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	ID        string    `json:"id"`
	Trigger   string    `json:"trigger"`
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	// ReposTargeted is how many repositories the targets section resolved
	// to after include, exclude, topics and the --target-filter. Zero means
	// the token could not list the organization or nothing matched, which
	// otherwise looks exactly like a run with nothing to do (GH #59).
	ReposTargeted int          `json:"repos_targeted"`
	PRsCreated    int          `json:"prs_created"`
	PRsUpdated    int          `json:"prs_updated"`
	ReposSkipped  int          `json:"repos_skipped"`
	Errors        int          `json:"errors"`
	RepoResults   []RepoResult `json:"repo_results"`
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

func (a *repoListerAdapter) ListByTopic(ctx context.Context, topic, org string) ([]*registry.ResolvedRepo, error) {
	ghRepos, err := a.repos.ListByTopic(ctx, topic, org)
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
	result.ReposTargeted = len(targets)
	if len(targets) == 0 {
		sslog.L().Warn("no target repositories resolved; check that the token can list the organization's repositories (a fine-grained PAT only sees the organization it was issued for) and that targets.include matches",
			"operation", "sync", "include", s.cfg.Targets.Include, "target_filter", opts.TargetFilter)
	} else {
		names := make([]string, 0, len(targets))
		for _, t := range targets {
			names = append(names, t.Repo)
		}
		sslog.L().Info("resolved target repositories",
			"operation", "sync", "count", len(targets), "repos", names)
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

	// Save state back (skip on dry run).
	//
	// A sync that cannot record its results has violated SYNCOP-008 ("MUST
	// record results in the DeploymentState"), and every later run depends on
	// that state: without it the hash check never skips, and monitor and
	// conflict have nothing to compare against. This used to be logged at WARN
	// and dropped, so runs reported errors:0 while writing nothing (GH #43).
	if !dryRun {
		if err := s.saveState(ctx); err != nil {
			sslog.L().Error("could not save deployment state",
				"operation", "sync", "err", err)
			result.Errors++
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

	// A symlink must be detected before the read: the Contents API follows a working link and returns the target
	// as a regular file, so the content alone never shows it (GH #82).
	isLink, err := s.ghClient.Repos.IsSymlink(ctx, owner, repo, tmpl.Destination, target.DefaultBranch)
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("checking destination type: %v", err)
		return rr
	}
	if isLink {
		// Writing through the Contents API would store the rendered template as the
		// symlink's target path, which git cannot check out.
		sslog.L().Warn("destination is a symlink, skipping",
			"operation", "sync", "template_id", tmpl.ID, "repo", target.Repo, "destination", tmpl.Destination)
		rr.Status = "skipped"
		rr.Error = fmt.Sprintf("destination %s is a symlink; point the template at the real file", tmpl.Destination)
		return rr
	}

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

	// What the deployment state last recorded for this repo/template. Read
	// before the force gate: the PR body reports the previous version
	// (SYNCPR-003) whether or not --force skipped the hash comparison.
	s.stateMu.Lock()
	prev := s.state.GetTemplateState(target.Repo, tmpl.ID)
	s.stateMu.Unlock()
	previousVersion := baselineVersion(prev, tmpl.Version)

	// Check state - skip if hash matches and not forced
	if !force && prev != nil && prev.Hash == contentHash {
		rr.Status = "skipped"
		s.refreshPRStatus(ctx, owner, repo, target.Repo, tmpl.ID, prev)
		return rr
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
	commitMsg := updateMessage(s.commitPrefix(), tmpl.ID, tmpl.Version)
	err = s.ghClient.Repos.CreateOrUpdateFile(ctx, owner, repo, tmpl.Destination, branchName, rendered, fileSHA, commitMsg)
	if err != nil {
		rr.Status = "error"
		rr.Error = fmt.Sprintf("creating/updating file: %v", err)
		return rr
	}

	// Find existing PR by its exact head branch. The label is not part of the
	// lookup: a PR whose label request failed must still be found on a retry.
	prLabel := s.cfg.Sync.PRLabel
	existingPRs, _ := s.ghClient.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{
		State: "open",
		Head:  owner + ":" + branchName,
	})

	prTitle := updateMessage(s.prTitlePrefix(), tmpl.ID, tmpl.Version)
	prBody := s.prBody(tmpl, previousVersion, contentHash)

	var pr *gh.PullRequest
	for _, existing := range existingPRs {
		if existing.Head == branchName {
			pr = existing
			break
		}
	}
	if pr != nil {
		// Update existing PR
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
		if err != nil && pr == nil {
			rr.Status = "error"
			rr.Error = fmt.Sprintf("creating PR: %v", err)
			return rr
		}
		if err != nil {
			// The PR exists but could not be labeled. Keep it so it is recorded in
			// the deployment state instead of orphaned and re-created on the next run.
			sslog.L().Warn("PR created but not labeled",
				"operation", "sync", "template_id", tmpl.ID, "repo", target.Repo,
				"pr_number", pr.Number, "error", err.Error())
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
		Version:         tmpl.Version,
		Hash:            contentHash,
		Timestamp:       time.Now(),
		PRNumber:        pr.Number,
		PRStatus:        "open",
		PreviousVersion: previousVersion,
	})
	s.stateMu.Unlock()

	return rr
}

// refreshPRStatus brings a recorded pr_status up to date once the target is
// in sync: the PR has usually been merged by then, but the state only ever
// recorded "open" at creation time (GH #56). Only "merged" is terminal; an
// unmerged "closed" PR can be reopened and merged later, so it is looked up
// again. Best-effort: a lookup failure leaves the state as it was.
func (s *Syncer) refreshPRStatus(ctx context.Context, owner, repo, fullName, templateID string, prev *state.TemplateState) {
	if prev == nil || prev.PRNumber == 0 || prev.PRStatus == "merged" {
		return
	}
	pr, err := s.ghClient.PullRequests.Get(ctx, owner, repo, prev.PRNumber)
	if err != nil || pr == nil {
		return
	}
	status := pr.State // "open" or "closed"
	if pr.State == "closed" && pr.Merged {
		status = "merged"
	}
	if status == prev.PRStatus {
		return
	}
	updated := *prev
	updated.PRStatus = status
	s.stateMu.Lock()
	s.state.SetTemplateState(fullName, templateID, updated)
	s.stateMu.Unlock()
	sslog.L().Info("deployment state: pull request status updated",
		"operation", "sync", "repo", fullName, "template_id", templateID, "pr", prev.PRNumber, "pr_status", status)
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
	msg := s.commitPrefix() + ": update deployment state"
	return s.ghClient.Repos.CreateOrUpdateFile(ctx, s.centralOwner, s.centralRepo, ".steerspec/deployment-state.json", "", data, s.stateSHA, msg)
}

// legacyTitlePrefix is the title prefix earlier releases generated
// ("[SteerSpec] Update <template-id>"). closeStalePRs still recognises it so
// PRs opened by an older release are closed when a newer version PR
// supersedes them (SYNCPR-006).
const legacyTitlePrefix = "[SteerSpec] Update "

func (s *Syncer) commitPrefix() string {
	if p := s.cfg.Sync.CommitPrefix; p != "" {
		return p
	}
	return config.DefaultCommitPrefix
}

func (s *Syncer) prTitlePrefix() string {
	if p := s.cfg.Sync.PRTitlePrefix; p != "" {
		return p
	}
	return config.DefaultPRTitlePrefix
}

// updateMessage builds the conventional-commit subject shared by sync commits
// and PR titles: "<prefix>: update <template-id> to <version>".
func updateMessage(prefix, templateID, version string) string {
	return fmt.Sprintf("%s: update %s to %s", prefix, templateID, version)
}

// isSyncPRForTemplate reports whether a PR title was generated for templateID,
// in either the current format under prefix or the legacy "[SteerSpec] Update"
// format. The current form matches up to and including " to " so that
// "claude-md" does not claim PRs for "claude-md-extra".
func isSyncPRForTemplate(title, prefix, templateID string) bool {
	return strings.HasPrefix(title, prefix+": update "+templateID+" to ") ||
		strings.HasPrefix(title, legacyTitlePrefix+templateID)
}

// serverURL is the web (not API) base of the GitHub instance, used for links
// in PR bodies. Actions sets GITHUB_SERVER_URL; outside Actions github.com is
// assumed.
func serverURL() string {
	if u := os.Getenv("GITHUB_SERVER_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://github.com"
}

// prBody renders the SyncPullRequest body. SYNCPR-003 requires the previous
// version, the new version, a changelog and a diff link. Templates are not
// tagged in the central repo, so the changelog is the template source's commit
// history there and the diff link points at the source as rendered; the diff
// of the rendered result is the PR itself.
func (s *Syncer) prBody(tmpl config.TemplateConfig, previousVersion, contentHash string) string {
	central := fmt.Sprintf("%s/%s/%s", serverURL(), s.centralOwner, s.centralRepo)
	previous := "none (first deployment)"
	if previousVersion != "" {
		previous = fmt.Sprintf("%#q", previousVersion)
	}
	sourcePath := escapePath(tmpl.Source)

	var b strings.Builder
	b.WriteString("## SteerSpec Sync\n\n")
	fmt.Fprintf(&b, "Updates %#q from template %#q in [%s/%s](%s).\n\n",
		tmpl.Destination, tmpl.ID, s.centralOwner, s.centralRepo, central)
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Previous version | %s |\n", previous)
	fmt.Fprintf(&b, "| New version | %#q |\n", tmpl.Version)
	fmt.Fprintf(&b, "| Content hash | %#q |\n\n", contentHash)
	fmt.Fprintf(&b, "- **Changelog:** [commit history of %#q](%s/commits/HEAD/%s)\n", tmpl.Source, central, sourcePath)
	fmt.Fprintf(&b, "- **Diff:** [template source](%s/blob/HEAD/%s); the rendered change is this PR's **Files changed** tab\n\n",
		central, sourcePath)
	b.WriteString("This PR was automatically generated by SteerSpec Sync. Edits to the managed file are overwritten ")
	b.WriteString("by the next sync; drift is reported by `strspc monitor`.\n")
	return b.String()
}

// baselineVersion is the version a sync PR reports as previous (SYNCPR-003).
// State records the proposed version as soon as the PR is opened, so when the
// state already holds tmpl.Version (a forced re-run of the same open PR) the
// baseline is the one recorded with it, not the proposed version itself.
func baselineVersion(prev *state.TemplateState, proposed string) string {
	if prev == nil {
		return ""
	}
	if prev.Version == proposed && prev.PreviousVersion != "" {
		return prev.PreviousVersion
	}
	return prev.Version
}

// escapePath percent-encodes each segment of a repository path for use in a
// URL, keeping the "/" separators: spaces, "#" and "?" in a template source
// would otherwise break the Markdown link or be read as a fragment or query.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

// isSyncBranchForTemplate reports whether head is the branch of a sync PR for
// templateID: "steerspec-sync/<template-id>/<version>", whatever title prefix
// was configured when the PR was opened. Template IDs may contain "/", so the
// remainder after the prefix must be exactly one version segment: "guide"
// must not claim "steerspec-sync/guide/team/1.0.0" (template "guide/team").
func isSyncBranchForTemplate(head, templateID string) bool {
	rest, ok := strings.CutPrefix(head, "steerspec-sync/"+templateID+"/")
	return ok && rest != "" && !strings.Contains(rest, "/")
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
	prefix := s.prTitlePrefix()
	for _, pr := range prs {
		if pr.Number == currentPR {
			continue
		}
		// The head branch is stable across title-prefix changes; the title
		// match keeps closing PRs whose branch was named by an older release.
		if isSyncBranchForTemplate(pr.Head, templateID) || isSyncPRForTemplate(pr.Title, prefix, templateID) {
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
