package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/registry"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

const (
	fingerprintPrefix = "<!-- steerspec-fingerprint: "
	fingerprintSuffix = " -->"
)

// Result holds the outcome of a monitor run.
type Result struct {
	ReposInSync   int `json:"repos_in_sync"`
	ReposDrifted  int `json:"repos_drifted"`
	IssuesCreated int `json:"issues_created"`
	IssuesClosed  int `json:"issues_closed"`
	// DuplicatesClosed counts extra open drift issues closed so that each repository and template pair keeps one
	// (MNRACT-006). IssuesClosed counts only issues auto-closed because their drift was resolved.
	DuplicatesClosed int          `json:"duplicates_closed"`
	DriftEntries     []DriftEntry `json:"drift_entries,omitempty"`
	// Errors counts the repo/template checks whose issue could not be
	// created, updated or closed. Each failure is isolated: monitor goes on
	// with the other targets and lists the failures in RepoErrors.
	Errors     int         `json:"errors"`
	RepoErrors []RepoError `json:"repo_errors,omitempty"`
}

// RepoError records a failure while managing the drift issue of one target.
type RepoError struct {
	Repo       string `json:"repo"`
	TemplateID string `json:"template_id"`
	Error      string `json:"error"`
}

// DriftEntry describes a single detected drift between expected and actual file content.
type DriftEntry struct {
	Repo            string    `json:"repo"`
	TemplateID      string    `json:"template_id"`
	ExpectedVersion string    `json:"expected_version"`
	ExpectedHash    string    `json:"expected_hash"`
	ActualHash      string    `json:"actual_hash"`
	PRNumber        int       `json:"pr_number,omitempty"`
	FirstDetected   time.Time `json:"first_detected"`
	IssueNumber     int       `json:"issue_number,omitempty"`
	IssueURL        string    `json:"issue_url,omitempty"`
}

// Options configures a monitor run.
type Options struct {
	ConfigPath   string
	TargetFilter string
}

// Monitor checks target repos for drift from expected state.
type Monitor struct {
	cfg          *config.SyncConfig
	ghClient     *gh.Client
	centralOwner string
	centralRepo  string
}

// New creates a Monitor.
func New(cfg *config.SyncConfig, ghClient *gh.Client, centralOwner, centralRepo string) *Monitor {
	return &Monitor{
		cfg:          cfg,
		ghClient:     ghClient,
		centralOwner: centralOwner,
		centralRepo:  centralRepo,
	}
}

// Run checks all repos in the deployment state for drift and manages drift issues.
func (m *Monitor) Run(ctx context.Context, deployState *state.DeploymentState, opts Options) (*Result, error) {
	// The same comma-separated glob list as sync's --target-filter (GH #171).
	targetFilter, err := registry.ParseFilter(opts.TargetFilter)
	if err != nil {
		return nil, fmt.Errorf("--target-filter: %w", err)
	}
	label := m.cfg.Monitor.IssueLabel
	if label == "" {
		label = "steerspec-drift"
	}
	autoClose := m.cfg.Monitor.AutoClose == nil || *m.cfg.Monitor.AutoClose

	// Build template ID -> destination path lookup.
	destByTemplate := make(map[string]string, len(m.cfg.Templates))
	for _, t := range m.cfg.Templates {
		destByTemplate[t.ID] = t.Destination
	}

	// Fetch the open issues in the central repo. The label is not required: a
	// drift issue whose label was removed or renamed is still found by its
	// fingerprint or title, so it is updated or closed rather than duplicated
	// (MNRACT-008/009/018). Both keys are specific to one repo/template drift.
	// An unlabeled issue counts only when a trusted author opened it, so a
	// copied title cannot capture or close the real drift issue.
	existingIssues, err := m.ghClient.Issues.List(ctx, m.centralOwner, m.centralRepo, &gh.IssueListOptions{
		State: "open",
	})
	if err != nil {
		return nil, fmt.Errorf("listing drift issues: %w", err)
	}

	// Keep the trusted issues, oldest first, so the issue kept for a pair is the one that recorded its drift first.
	var trusted []*gh.Issue
	for _, iss := range existingIssues {
		if iss.Trusted(label) {
			trusted = append(trusted, iss)
		}
	}
	slices.SortFunc(trusted, func(a, b *gh.Issue) int { return a.Number - b.Number })
	// closedIssues holds the issues this run closed, so no later pair matches them.
	closedIssues := make(map[int]bool)

	now := time.Now().UTC()
	result := &Result{}
	driftedRepos := make(map[string]bool)
	inSyncRepos := make(map[string]bool)
	fail := func(repo, templateID string, err error) {
		result.Errors++
		result.RepoErrors = append(result.RepoErrors, RepoError{Repo: repo, TemplateID: templateID, Error: err.Error()})
	}

	for repoFullName, templates := range deployState.Repositories {
		if !targetFilter.Match(repoFullName) {
			continue
		}

		parts := strings.SplitN(repoFullName, "/", 2)
		if len(parts) != 2 {
			continue
		}
		owner, repo := parts[0], parts[1]

		for templateID, ts := range templates {
			destPath, ok := destByTemplate[templateID]
			if !ok {
				continue
			}

			fp := driftFingerprint(repoFullName, templateID)

			issTitle := driftIssueTitle(repoFullName, templateID)
			existing, duplicates := pairIssues(trusted, closedIssues, fp, issTitle)
			// Every path keeps at most one open issue for the pair (MNRACT-006), auto-close or not. A failed close
			// is reported for this pair and does not stop the kept issue from being updated.
			for _, dup := range duplicates {
				if err := m.ghClient.Issues.Close(ctx, m.centralOwner, m.centralRepo, dup.Number); err != nil {
					fail(repoFullName, templateID, fmt.Errorf("closing duplicate drift issue #%d (keeping #%d): %w",
						dup.Number, existing.Number, err))
					continue
				}
				closedIssues[dup.Number] = true
				result.DuplicatesClosed++
			}

			content, _, err := m.ghClient.Repos.GetFileContent(ctx, owner, repo, destPath, "")
			if err != nil {
				// Treat fetch error as drift (file may be missing).
				entry := DriftEntry{
					Repo:            repoFullName,
					TemplateID:      templateID,
					ExpectedVersion: ts.Version,
					ExpectedHash:    ts.ExpectedHash(),
					PRNumber:        ts.PRNumber,
					FirstDetected:   firstDetected(existing, now),
				}
				if err := m.reportDrift(ctx, &entry, existing, issTitle, label, result); err != nil {
					fail(repoFullName, templateID, err)
				}
				driftedRepos[repoFullName] = true
				result.DriftEntries = append(result.DriftEntries, entry)
				continue
			}

			drifted, expectedHash, actualHash := ts.Drift(content)

			if !drifted {
				// In sync. Close existing issue if auto-close is enabled.
				if autoClose {
					if existing != nil {
						if err := m.ghClient.Issues.Close(ctx, m.centralOwner, m.centralRepo, existing.Number); err != nil {
							fail(repoFullName, templateID, fmt.Errorf("closing resolved drift issue #%d: %w", existing.Number, err))
						} else {
							result.IssuesClosed++
							closedIssues[existing.Number] = true
						}
					}
				}
				inSyncRepos[repoFullName] = true
				continue
			}

			// Drift detected.
			entry := DriftEntry{
				Repo:            repoFullName,
				TemplateID:      templateID,
				ExpectedVersion: ts.Version,
				ExpectedHash:    expectedHash,
				ActualHash:      actualHash,
				PRNumber:        ts.PRNumber,
				FirstDetected:   firstDetected(existing, now),
			}
			if err := m.reportDrift(ctx, &entry, existing, issTitle, label, result); err != nil {
				fail(repoFullName, templateID, err)
			}
			driftedRepos[repoFullName] = true
			result.DriftEntries = append(result.DriftEntries, entry)
		}
	}

	result.ReposDrifted = len(driftedRepos)
	// A repo is "in sync" only if it had templates checked and none drifted.
	for repo := range inSyncRepos {
		if !driftedRepos[repo] {
			result.ReposInSync++
		}
	}

	return result, nil
}

// reportDrift updates the open drift issue for entry, or opens one, and records its number and URL in entry.
func (m *Monitor) reportDrift(ctx context.Context, entry *DriftEntry, existing *gh.Issue, title, label string, result *Result) error {
	body := driftIssueBody(*entry)
	if existing != nil {
		if err := m.ghClient.Issues.Update(ctx, m.centralOwner, m.centralRepo, existing.Number, &gh.IssueUpdate{Body: &body}); err != nil {
			return fmt.Errorf("updating drift issue #%d: %w", existing.Number, err)
		}
		entry.IssueNumber = existing.Number
		entry.IssueURL = existing.URL
		return nil
	}
	iss, err := m.ghClient.Issues.Create(ctx, m.centralOwner, m.centralRepo, &gh.IssueCreate{
		Title:  title,
		Body:   body,
		Labels: []string{label},
	})
	if err != nil {
		return fmt.Errorf("creating drift issue: %w", err)
	}
	entry.IssueNumber = iss.Number
	entry.IssueURL = iss.URL
	result.IssuesCreated++
	return nil
}

const firstDetectedPrefix = "**First Detected:** "

// firstDetected returns the time the open drift issue recorded as first detected, so rewriting the body on a later
// run keeps it (MNRACT: time since drift was first detected); now when there is no issue or no parseable time.
func firstDetected(existing *gh.Issue, now time.Time) time.Time {
	if existing == nil {
		return now
	}
	for _, line := range strings.Split(existing.Body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), firstDetectedPrefix); ok {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
				return t.UTC()
			}
		}
	}
	return now
}

func driftIssueTitle(repo, templateID string) string {
	return fmt.Sprintf("[SteerSpec Drift] %s: %s", repo, templateID)
}

func driftIssueBody(e DriftEntry) string {
	var b strings.Builder
	b.WriteString("## SteerSpec Drift Detected\n\n")
	fmt.Fprintf(&b, "**Repository:** `%s`\n", e.Repo)
	fmt.Fprintf(&b, "**Template:** `%s`\n", e.TemplateID)
	fmt.Fprintf(&b, "**Expected Version:** `%s`\n", e.ExpectedVersion)
	fmt.Fprintf(&b, "**Expected Hash:** `%s`\n", e.ExpectedHash)
	if e.ActualHash != "" {
		fmt.Fprintf(&b, "**Actual Hash:** `%s`\n", e.ActualHash)
	} else {
		b.WriteString("**Actual Hash:** _(file not found, or a managed section is missing or unreadable)_\n")
	}
	if e.PRNumber > 0 {
		// The issue lives in the central repo, so a bare #N would link the central repo's #N, not the sync PR.
		fmt.Fprintf(&b, "**PR:** %s#%d\n", e.Repo, e.PRNumber)
	}
	fmt.Fprintf(&b, "%s%s\n", firstDetectedPrefix, e.FirstDetected.UTC().Format(time.RFC3339))

	fp := driftFingerprint(e.Repo, e.TemplateID)
	fmt.Fprintf(&b, "\n%s%s%s\n", fingerprintPrefix, fp, fingerprintSuffix)

	return b.String()
}

// driftFingerprint produces a stable fingerprint for a drift issue from its
// identifying components.
func driftFingerprint(repo, templateID string) string {
	return hash.Fingerprint("drift", repo, templateID)
}

// pairIssues returns the open issue to keep for one repository and template pair, and the duplicates to close. The
// pair's issues are those carrying its fingerprint, which is stable across title changes, and legacy issues with
// its title and no fingerprint. The oldest issue carrying the fingerprint is kept, failing that the oldest legacy
// issue; the rest are duplicates. An issue carrying another pair's fingerprint is never kept or closed for this
// pair, whatever its title. issues is sorted by number, and issues already closed this run are skipped.
func pairIssues(issues []*gh.Issue, closed map[int]bool, fp, title string) (keep *gh.Issue, duplicates []*gh.Issue) {
	var byFingerprint, byTitle []*gh.Issue
	for _, iss := range issues {
		if closed[iss.Number] {
			continue
		}
		switch issueFP := extractFingerprint(iss.Body); {
		case issueFP == fp:
			byFingerprint = append(byFingerprint, iss)
		case issueFP == "" && iss.Title == title:
			// A legacy issue without a fingerprint. One carrying another pair's fingerprint belongs to that pair.
			byTitle = append(byTitle, iss)
		}
	}
	all := slices.Concat(byFingerprint, byTitle)
	if len(all) == 0 {
		return nil, nil
	}
	return all[0], all[1:]
}

// extractFingerprint returns the fingerprint embedded in an issue body, or ""
// if none is found.
func extractFingerprint(body string) string {
	idx := strings.Index(body, fingerprintPrefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(fingerprintPrefix)
	end := strings.Index(body[start:], fingerprintSuffix)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}
