package conflict

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	// typePrefix marks the conflict type in an issue body so cleanup can
	// recover it after the title was edited.
	typePrefix = "<!-- steerspec-conflict-type: "
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
	// incomplete lists the repos ("owner/name") where a content read failed
	// for a reason other than "not found" during the current run. Their
	// findings may be missing, so cleanup must not treat them as resolved.
	incomplete map[string]bool
	// missingTracked lists the repos where a file the deployment state
	// tracks was absent: version drift is not checked there, so its issues
	// cannot be called resolved.
	missingTracked map[string]bool
}

// readFile fetches a file from a target. A 404 is a normal outcome (the
// file does not exist) and comes back as found=false with no error; any
// other failure marks the repo incomplete for this run and is returned.
func (s *Scanner) readFile(ctx context.Context, owner, repo, path string) (content []byte, found bool, err error) {
	content, _, err = s.ghClient.Repos.GetFileContent(ctx, owner, repo, path, "")
	if err == nil {
		return content, true, nil
	}
	var apiErr *gh.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	s.incomplete[owner+"/"+repo] = true
	return nil, false, err
}

// tierOfType maps a conflict type to the detection tier that produces it, so
// cleanup only touches issues of tiers that actually ran.
func tierOfType(t ConflictType) int {
	switch t {
	case TypeVersionDrift, TypeCrossReferenceBroken, TypeUnmanagedFile:
		return 1
	case TypeDuplicateSkill, TypeManualOverride:
		return 2
	case TypeContradictoryInstruction:
		return 3
	}
	return 0
}

// issueType recovers the conflict type of an issue: from the body marker
// written since cleanup exists, else from the title (issues created before
// the marker, or whose body was edited).
func issueType(issue *gh.Issue) ConflictType {
	if idx := strings.Index(issue.Body, typePrefix); idx >= 0 {
		rest := issue.Body[idx+len(typePrefix):]
		if end := strings.Index(rest, fingerprintSuffix); end >= 0 {
			return ConflictType(strings.TrimSpace(rest[:end]))
		}
	}
	return typeFromTitle(issue.Title)
}

// typeFromTitle recovers the conflict type from an issue title of the form
// "[SteerSpec Conflict] <type>: <path>".
func typeFromTitle(title string) ConflictType {
	const prefix = "[SteerSpec Conflict] "
	if !strings.HasPrefix(title, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(title, prefix)
	if i := strings.Index(rest, ":"); i > 0 {
		return ConflictType(rest[:i])
	}
	return ""
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
	s.incomplete = make(map[string]bool)
	s.missingTracked = make(map[string]bool)
	ran := make(map[int]bool, len(tiers))

	for _, tier := range tiers {
		ran[tier] = true
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

	if err := s.createIssuesForConflicts(ctx, report, targets, ran); err != nil {
		return report, fmt.Errorf("creating issues: %w", err)
	}

	return report, nil
}

func (s *Scanner) createIssuesForConflicts(ctx context.Context, report *ConflictReport, targets []string, ran map[int]bool) error {
	// Fingerprints and titles reported this run, per repo, so issues whose
	// conflict is gone can be closed afterwards (SteerSpec/strspc-sync#55).
	current := make(map[string]map[string]bool)
	remember := func(repo, key string) {
		if current[repo] == nil {
			current[repo] = make(map[string]bool)
		}
		current[repo][key] = true
	}

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
		remember(repo, fp)
		remember(repo, title)

		body := entry.Description
		if entry.SuggestedResolution != "" {
			body += "\n\n**Suggested resolution:** " + entry.SuggestedResolution
		}
		body += "\n\n" + fingerprintPrefix + fp + fingerprintSuffix
		body += "\n" + typePrefix + string(entry.Type) + fingerprintSuffix

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
			report.IssuesCreated++
		}
	}

	if s.cfg.Conflicts.AutoClose != nil && !*s.cfg.Conflicts.AutoClose {
		return nil
	}
	return s.closeResolvedIssues(ctx, report, targets, current, ran)
}

// closeResolvedIssues closes, in every scanned repo and in the central repo,
// the open issues carrying the conflict label whose fingerprint (or title)
// was not reported by this run: the conflict they described is gone.
// Monitor has done the same for drift issues since v0.2.0; conflict issues
// used to stay open forever (SteerSpec/strspc-sync#55).
//
// An issue is only closed when this run can vouch for it: at least one
// target was scanned, the issue's type belongs to a tier that ran, and no
// content read failed in the repo the issue is about (for a central-repo
// issue about several repos, in any repo).
func (s *Scanner) closeResolvedIssues(ctx context.Context, report *ConflictReport, targets []string, current map[string]map[string]bool, ran map[int]bool) error {
	if len(targets) == 0 {
		return nil
	}
	central := s.centralOwner + "/" + s.centralRepo
	anyIncomplete := len(s.incomplete) > 0
	repos := append([]string{}, targets...)
	repos = append(repos, central)
	seen := make(map[string]bool, len(repos))
	for _, repo := range repos {
		if seen[repo] {
			continue
		}
		seen[repo] = true
		if s.incomplete[repo] {
			continue
		}
		owner, repoName, err := splitRepo(repo)
		if err != nil {
			continue
		}
		existing, err := s.ghClient.Issues.List(ctx, owner, repoName, &gh.IssueListOptions{
			State:  "open",
			Labels: []string{s.cfg.Conflicts.IssueLabel},
		})
		if err != nil {
			return fmt.Errorf("listing issues for %s: %w", repo, err)
		}
		for _, issue := range existing {
			if current[repo][extractFingerprint(issue.Body)] || current[repo][issue.Title] {
				continue
			}
			typ := issueType(issue)
			tier := tierOfType(typ)
			if tier == 0 || !ran[tier] {
				continue // unknown type, or a tier this run did not check
			}
			if repo == central && tier == 2 && anyIncomplete {
				continue // multi-repo findings cannot be vouched for
			}
			if typ == TypeVersionDrift && s.missingTracked[repo] {
				continue // the tracked file is gone, so drift was not checked
			}
			if err := s.ghClient.Issues.Close(ctx, owner, repoName, issue.Number); err != nil {
				return fmt.Errorf("closing resolved issue #%d in %s: %w", issue.Number, repo, err)
			}
			report.IssuesClosed++
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
