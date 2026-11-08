package monitor

import (
	"context"
	"fmt"
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
	ReposInSync   int          `json:"repos_in_sync"`
	ReposDrifted  int          `json:"repos_drifted"`
	IssuesCreated int          `json:"issues_created"`
	IssuesClosed  int          `json:"issues_closed"`
	DriftEntries  []DriftEntry `json:"drift_entries,omitempty"`
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

	// Fetch existing drift issues in the central repo.
	existingIssues, err := m.ghClient.Issues.List(ctx, m.centralOwner, m.centralRepo, &gh.IssueListOptions{
		State:  "open",
		Labels: []string{label},
	})
	if err != nil {
		return nil, fmt.Errorf("listing drift issues: %w", err)
	}

	// Index existing issues by fingerprint (preferred) and title (fallback) for fast lookup.
	issueByFingerprint := make(map[string]*gh.Issue, len(existingIssues))
	issueByTitle := make(map[string]*gh.Issue, len(existingIssues))
	for _, iss := range existingIssues {
		issueByTitle[iss.Title] = iss
		if fp := extractFingerprint(iss.Body); fp != "" {
			issueByFingerprint[fp] = iss
		}
	}

	now := time.Now().UTC()
	result := &Result{}
	driftedRepos := make(map[string]bool)
	inSyncRepos := make(map[string]bool)

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
			existing := findIssueByFingerprintOrTitle(issueByFingerprint, issueByTitle, fp, issTitle)

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
					return nil, err
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
							return nil, fmt.Errorf("closing resolved drift issue #%d: %w", existing.Number, err)
						}
						result.IssuesClosed++
						delete(issueByTitle, existing.Title)
						delete(issueByFingerprint, fp)
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
				return nil, err
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

// findIssueByFingerprintOrTitle looks up an existing issue first by fingerprint
// (stable across title changes), then falls back to title match.
func findIssueByFingerprintOrTitle(
	byFingerprint map[string]*gh.Issue,
	byTitle map[string]*gh.Issue,
	fp, title string,
) *gh.Issue {
	if iss, ok := byFingerprint[fp]; ok {
		return iss
	}
	if iss, ok := byTitle[title]; ok {
		return iss
	}
	return nil
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
