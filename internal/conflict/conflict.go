package conflict

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

const (
	fingerprintPrefix = "<!-- steerspec-fingerprint: "
	fingerprintSuffix = " -->"
)

// Options controls what the scanner checks.
type Options struct {
	ConfigPath   string
	TargetFilter string
	Tiers        []int
}

// Scanner orchestrates conflict detection across target repositories.
type Scanner struct {
	cfg          *config.SyncConfig
	ghClient     *gh.Client
	centralOwner string
	centralRepo  string
}

// New creates a new conflict Scanner.
func New(cfg *config.SyncConfig, ghClient *gh.Client, centralOwner, centralRepo string) *Scanner {
	return &Scanner{
		cfg:          cfg,
		ghClient:     ghClient,
		centralOwner: centralOwner,
		centralRepo:  centralRepo,
	}
}

// Run executes conflict detection for the given targets and options.
func (s *Scanner) Run(ctx context.Context, deployState *state.DeploymentState, targets []string, opts Options) (*ConflictReport, error) {
	report := &ConflictReport{
		ID:        uuid.New().String(),
		Timestamp: time.Now(),
	}

	tiers := opts.Tiers
	if len(tiers) == 0 {
		tiers = s.cfg.Conflicts.Tiers
	}

	for _, tier := range tiers {
		var entries []ConflictEntry
		var err error

		switch tier {
		case 1:
			entries, err = s.runTier1(ctx, deployState, targets)
		case 2:
			entries, err = s.runTier2(ctx, targets)
		case 3:
			entries, err = s.runTier3(ctx, targets)
		default:
			return nil, fmt.Errorf("unknown conflict tier: %d", tier)
		}

		if err != nil {
			return nil, fmt.Errorf("tier %d: %w", tier, err)
		}
		report.Entries = append(report.Entries, entries...)
	}

	report.ComputeSummary()

	if err := s.createIssuesForConflicts(ctx, report, targets); err != nil {
		return report, fmt.Errorf("creating issues: %w", err)
	}

	return report, nil
}

func (s *Scanner) createIssuesForConflicts(ctx context.Context, report *ConflictReport, _ []string) error {
	// Only create issues for critical and warning entries
	for _, entry := range report.Entries {
		if entry.Severity == SeverityInfo {
			continue
		}

		// Determine which repo to file the issue against
		repo := entry.Repo
		if strings.Contains(repo, ", ") {
			// Multi-repo entries (e.g., duplicate skills) go to the central repo
			repo = s.centralOwner + "/" + s.centralRepo
		}

		owner, repoName, err := splitRepo(repo)
		if err != nil {
			continue
		}

		title := fmt.Sprintf("[SteerSpec Conflict] %s: %s", entry.Type, entry.FilePath)
		fp := hash.Fingerprint(string(entry.Type), repo, entry.FilePath)

		body := entry.Description
		if entry.SuggestedResolution != "" {
			body += "\n\n**Suggested resolution:** " + entry.SuggestedResolution
		}
		body += "\n\n" + fingerprintPrefix + fp + fingerprintSuffix

		// Check if an issue already exists
		existing, err := s.ghClient.Issues.List(ctx, owner, repoName, &gh.IssueListOptions{
			State:  "open",
			Labels: []string{s.cfg.Conflicts.IssueLabel},
		})
		if err != nil {
			return fmt.Errorf("listing issues for %s: %w", repo, err)
		}

		found := false
		for _, issue := range existing {
			if extractFingerprint(issue.Body) == fp || issue.Title == title {
				found = true
				if err := s.ghClient.Issues.Update(ctx, owner, repoName, issue.Number, &gh.IssueUpdate{Body: &body}); err != nil {
					return fmt.Errorf("updating issue for %s: %w", repo, err)
				}
				break
			}
		}

		if !found {
			_, err := s.ghClient.Issues.Create(ctx, owner, repoName, &gh.IssueCreate{
				Title:  title,
				Body:   body,
				Labels: []string{s.cfg.Conflicts.IssueLabel},
			})
			if err != nil {
				return fmt.Errorf("creating issue for %s: %w", repo, err)
			}
		}
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

func (s *Scanner) findTemplate(id string) *config.TemplateConfig {
	for i := range s.cfg.Templates {
		if s.cfg.Templates[i].ID == id {
			return &s.cfg.Templates[i]
		}
	}
	return nil
}

func splitRepo(fullName string) (string, string, error) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo name %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}
