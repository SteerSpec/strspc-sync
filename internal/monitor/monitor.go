package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
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

	// Index existing issues by title for fast lookup.
	issueByTitle := make(map[string]*gh.Issue, len(existingIssues))
	for _, iss := range existingIssues {
		issueByTitle[iss.Title] = iss
	}

	result := &Result{}
	driftedRepos := make(map[string]bool)
	inSyncRepos := make(map[string]bool)

	for repoFullName, templates := range deployState.Repositories {
		if opts.TargetFilter != "" && repoFullName != opts.TargetFilter {
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

			content, _, err := m.ghClient.Repos.GetFileContent(ctx, owner, repo, destPath, "")
			if err != nil {
				// Treat fetch error as drift (file may be missing).
				entry := DriftEntry{
					Repo:            repoFullName,
					TemplateID:      templateID,
					ExpectedVersion: ts.Version,
					ExpectedHash:    ts.Hash,
					ActualHash:      "",
					PRNumber:        ts.PRNumber,
					FirstDetected:   time.Now(),
				}
				issTitle := driftIssueTitle(repoFullName, templateID)
				if existing, found := issueByTitle[issTitle]; found {
					body := driftIssueBody(entry)
					if err := m.ghClient.Issues.Update(ctx, m.centralOwner, m.centralRepo, existing.Number, &gh.IssueUpdate{Body: &body}); err != nil {
						return nil, fmt.Errorf("updating drift issue #%d: %w", existing.Number, err)
					}
					entry.IssueNumber = existing.Number
					entry.IssueURL = existing.URL
				} else {
					body := driftIssueBody(entry)
					iss, err := m.ghClient.Issues.Create(ctx, m.centralOwner, m.centralRepo, &gh.IssueCreate{
						Title:  issTitle,
						Body:   body,
						Labels: []string{label},
					})
					if err != nil {
						return nil, fmt.Errorf("creating drift issue: %w", err)
					}
					entry.IssueNumber = iss.Number
					entry.IssueURL = iss.URL
					result.IssuesCreated++
				}
				driftedRepos[repoFullName] = true
				result.DriftEntries = append(result.DriftEntries, entry)
				continue
			}

			actualHash := hash.HashBytes(content)
			issTitle := driftIssueTitle(repoFullName, templateID)

			if actualHash == ts.Hash {
				// In sync. Close existing issue if auto-close is enabled.
				if autoClose {
					if existing, found := issueByTitle[issTitle]; found {
						if err := m.ghClient.Issues.Close(ctx, m.centralOwner, m.centralRepo, existing.Number); err != nil {
							return nil, fmt.Errorf("closing resolved drift issue #%d: %w", existing.Number, err)
						}
						result.IssuesClosed++
						delete(issueByTitle, issTitle)
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
				ExpectedHash:    ts.Hash,
				ActualHash:      actualHash,
				PRNumber:        ts.PRNumber,
				FirstDetected:   time.Now(),
			}

			if existing, found := issueByTitle[issTitle]; found {
				body := driftIssueBody(entry)
				if err := m.ghClient.Issues.Update(ctx, m.centralOwner, m.centralRepo, existing.Number, &gh.IssueUpdate{Body: &body}); err != nil {
					return nil, fmt.Errorf("updating drift issue #%d: %w", existing.Number, err)
				}
				entry.IssueNumber = existing.Number
				entry.IssueURL = existing.URL
			} else {
				body := driftIssueBody(entry)
				iss, err := m.ghClient.Issues.Create(ctx, m.centralOwner, m.centralRepo, &gh.IssueCreate{
					Title:  issTitle,
					Body:   body,
					Labels: []string{label},
				})
				if err != nil {
					return nil, fmt.Errorf("creating drift issue: %w", err)
				}
				entry.IssueNumber = iss.Number
				entry.IssueURL = iss.URL
				result.IssuesCreated++
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
		b.WriteString("**Actual Hash:** _(file not found)_\n")
	}
	if e.PRNumber > 0 {
		fmt.Fprintf(&b, "**PR:** #%d\n", e.PRNumber)
	}
	b.WriteString(fmt.Sprintf("**First Detected:** %s\n", e.FirstDetected.Format(time.RFC3339)))
	return b.String()
}
