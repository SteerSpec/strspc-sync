package conflict

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	sslog "github.com/SteerSpec/strspc-sync/internal/log"
	"github.com/SteerSpec/strspc-sync/internal/registry"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

const (
	fingerprintPrefix = "<!-- steerspec-fingerprint: "
	fingerprintSuffix = " -->"
	// typePrefix marks the conflict type in an issue body so cleanup can
	// recover it after the title was edited.
	typePrefix = "<!-- steerspec-conflict-type: "
	// reposPrefix lists the repositories an aggregate (multi-repo) finding
	// involves, so cleanup only closes it once every one of them was scanned.
	reposPrefix = "<!-- steerspec-conflict-repos: "
)

// Options controls what the scanner checks.
type Options struct {
	ConfigPath string
	Tiers      []int
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
	// missingTracked lists, per repo, the destination paths the deployment
	// state tracks that were absent (404): version drift is not checked for
	// them, so their issues cannot be called resolved. Other files in the
	// same repo are unaffected.
	missingTracked map[string]map[string]bool
	// visibility caches repository visibility ("public", "internal", "private"), from target resolution or looked
	// up; "" when the lookup failed.
	visibility map[string]string
}

// noteMissingTracked records that a tracked destination was absent.
func (s *Scanner) noteMissingTracked(repo, path string) {
	if s.missingTracked[repo] == nil {
		s.missingTracked[repo] = make(map[string]bool)
	}
	s.missingTracked[repo][path] = true
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
	case TypeVersionDrift, TypeManualOverride, TypeCrossReferenceBroken, TypeUnmanagedFile:
		return 1
	case TypeDuplicateSkill, TypeSectionOverlap:
		return 2
	case TypeContradictoryInstruction:
		return 3
	}
	return 0
}

// issueType recovers the conflict type of an issue: from the body marker
// written since cleanup exists, else from the title (issues created before
// the marker, or whose body was edited). A value that is not one of the seven
// types (CNFLENT-003) is not a type: the marker falls back to the title, and
// the result is "" when neither holds one, so cleanup leaves the issue alone.
func issueType(issue *gh.Issue) ConflictType {
	if idx := strings.Index(issue.Body, typePrefix); idx >= 0 {
		rest := issue.Body[idx+len(typePrefix):]
		if end := strings.Index(rest, fingerprintSuffix); end >= 0 {
			if t := ConflictType(strings.TrimSpace(rest[:end])); t.Valid() {
				return t
			}
		}
	}
	return typeFromTitle(issue.Title)
}

// mergeParticipants returns current plus every previously recorded
// participant that was not scanned in this run or whose read failed; a
// participant that was scanned cleanly and no longer conflicts is dropped.
func (s *Scanner) mergeParticipants(current, previous, targets []string) []string {
	scanned := make(map[string]bool, len(targets))
	for _, t := range targets {
		scanned[t] = true
	}
	have := make(map[string]bool, len(current))
	for _, c := range current {
		have[c] = true
	}
	merged := append([]string{}, current...)
	for _, p := range previous {
		if have[p] {
			continue
		}
		if !scanned[p] || s.incomplete[p] {
			merged = append(merged, p)
			have[p] = true
		}
	}
	return merged
}

// issueRepos recovers the participant list of an aggregate issue from its
// body marker; nil when the marker is absent (an issue created before it).
func issueRepos(issue *gh.Issue) []string {
	idx := strings.Index(issue.Body, reposPrefix)
	if idx < 0 {
		return nil
	}
	rest := issue.Body[idx+len(reposPrefix):]
	end := strings.Index(rest, fingerprintSuffix)
	if end < 0 {
		return nil
	}
	var repos []string
	for _, r := range strings.Split(rest[:end], ",") {
		if r = strings.TrimSpace(r); r != "" {
			repos = append(repos, r)
		}
	}
	return repos
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
		if t := ConflictType(rest[:i]); t.Valid() {
			return t
		}
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
		visibility:   make(map[string]string),
	}
}

// Targets returns the repositories to scan, sorted: every repository the config's targets resolve to, so a target
// without a deployment state entry is scanned too (unmanaged files, GH #174), plus every repository in the deployment
// state, which may have left targets but still holds synced files. A non-empty filter keeps the repositories whose
// "owner/repo" it matches, parsed by registry.ParseFilter as sync and monitor parse --target-filter (GH #171).
func (s *Scanner) Targets(ctx context.Context, deployState *state.DeploymentState, filter string) ([]string, error) {
	targetFilter, err := registry.ParseFilter(filter)
	if err != nil {
		return nil, fmt.Errorf("--target-filter: %w", err)
	}
	resolved, err := registry.New(&registry.GitHubLister{Repos: s.ghClient.Repos}).
		Resolve(ctx, s.cfg.Targets, s.cfg.Variables)
	if err != nil {
		return nil, fmt.Errorf("resolving targets: %w", err)
	}
	seen := make(map[string]bool, len(resolved)+len(deployState.Repositories))
	for _, t := range resolved {
		seen[t.Repo] = true
		if t.Visibility != "" {
			s.visibility[t.Repo] = t.Visibility
		}
	}
	for repo := range deployState.Repositories {
		seen[repo] = true
	}
	targets := make([]string, 0, len(seen))
	for repo := range seen {
		if targetFilter.Match(repo) {
			targets = append(targets, repo)
		}
	}
	sort.Strings(targets)
	return targets, nil
}

// Run executes conflict detection for the given targets and options.
func (s *Scanner) Run(ctx context.Context, deployState *state.DeploymentState, targets []string, opts Options) (*ConflictReport, error) {
	report := &ConflictReport{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
	}

	tiers := opts.Tiers
	if len(tiers) == 0 {
		tiers = s.cfg.Conflicts.Tiers
	}
	s.incomplete = make(map[string]bool)
	s.missingTracked = make(map[string]map[string]bool)
	ran := make(map[int]bool, len(tiers))
	var tierErrs []error

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

		// A failing tier does not discard the other tiers' results: the
		// report still combines every tier that ran (CNFLDTS-008), and the
		// failure is returned with it. The failed tier is left out of ran,
		// so its open issues are not closed on missing evidence.
		if err != nil {
			tierErrs = append(tierErrs, fmt.Errorf("tier %d: %w", tier, err))
			continue
		}
		ran[tier] = true
		report.Entries = append(report.Entries, entries...)
	}

	report.ComputeSummary()

	if err := s.createIssuesForConflicts(ctx, report, targets, ran); err != nil {
		tierErrs = append(tierErrs, fmt.Errorf("creating issues: %w", err))
	}

	return report, errors.Join(tierErrs...)
}

// conflictTitle is the title of a conflict's issue. Every issue is filed in the central repository (CNFLACT-006),
// so a conflict about one repository names it; a cross-repository conflict keeps the path alone, as it always has.
func conflictTitle(typ ConflictType, repo, path string, aggregate bool) string {
	if aggregate {
		return targetTitle(typ, path)
	}
	return fmt.Sprintf("[SteerSpec Conflict] %s: %s/%s", typ, repo, path)
}

// targetTitle is the title an issue had when a single-repository conflict was filed in its own repository, before
// CNFLACT-006, and the title of a cross-repository conflict's issue.
func targetTitle(typ ConflictType, path string) string {
	return fmt.Sprintf("[SteerSpec Conflict] %s: %s", typ, path)
}

// visibilityOf returns repo's visibility, looked up once; "" when it cannot be read.
func (s *Scanner) visibilityOf(ctx context.Context, repo string) string {
	if v, ok := s.visibility[repo]; ok {
		return v
	}
	v := ""
	if owner, name, err := splitRepo(repo); err == nil {
		if got, err := s.ghClient.Repos.GetVisibility(ctx, owner, name); err == nil {
			v = got
		}
	}
	s.visibility[repo] = v
	return v
}

// mayPublish reports whether a conflict about repos may be filed in the central repository. A public central
// repository's issues are public, so they must not name a private or internal repository or its files: such a
// conflict is withheld. A visibility that cannot be read counts as public for the central repository and as non-public
// for the others.
func (s *Scanner) mayPublish(ctx context.Context, repos []string) bool {
	if v := s.visibilityOf(ctx, s.centralOwner+"/"+s.centralRepo); v != "public" && v != "" {
		return true
	}
	for _, r := range repos {
		if s.visibilityOf(ctx, r) != "public" {
			return false
		}
	}
	return true
}

// severityRank orders severities for the issue threshold (CNFLACT-006).
var severityRank = map[Severity]int{SeverityInfo: 1, SeverityWarning: 2, SeverityCritical: 3}

// threshold is the lowest severity that gets an issue: conflicts.severity-threshold, warning by default.
func (s *Scanner) threshold() Severity {
	if t := Severity(s.cfg.Conflicts.SeverityThreshold); severityRank[t] > 0 {
		return t
	}
	return SeverityWarning
}

func (s *Scanner) createIssuesForConflicts(ctx context.Context, report *ConflictReport, targets []string, ran map[int]bool) error {
	central := s.centralOwner + "/" + s.centralRepo
	// Keys of every conflict detected this run, per repo the issue may be in, so issues whose conflict is gone can be
	// closed afterwards (SteerSpec/strspc-sync#55). Under central: fingerprints and titles of the central issues.
	// Under a target: the keys its own issues had before CNFLACT-006. A conflict below the threshold is remembered
	// too: it is still there, so its issue is not closed as resolved.
	current := make(map[string]map[string]bool)
	remember := func(repo, key string) {
		if current[repo] == nil {
			current[repo] = make(map[string]bool)
		}
		current[repo][key] = true
	}
	// moved maps the keys of a conflict's issue in its target repository to the central issue that now tracks it,
	// so cleanup can close the target copy (CNFLACT-007).
	moved := make(map[string]map[string]int)
	move := func(repo, key string, number int) {
		if moved[repo] == nil {
			moved[repo] = make(map[string]int)
		}
		moved[repo][key] = number
	}

	// The central repo's open issues, listed once. The label is not required: an issue whose label was removed or
	// renamed is still found by its fingerprint or title, so it is updated rather than duplicated (CNFLACT-007/016).
	// Both keys are specific to one conflict. An unlabeled issue counts only when a trusted author opened it, so a
	// copied title cannot capture the update.
	var openIssues []*gh.Issue
	listed := false
	threshold := s.threshold()

	for _, entry := range report.Entries {
		if _, _, err := splitRepo(entry.Repo); err != nil {
			continue
		}
		// A cross-repository entry (duplicate skills) lists its participants; any other concerns entry.Repo alone.
		aggregate := len(entry.Repos) > 0
		participants := entry.Repos
		if !aggregate {
			participants = []string{entry.Repo}
		}

		title := conflictTitle(entry.Type, entry.Repo, entry.FilePath, aggregate)
		// The fingerprint names the repository the conflict concerns, so the same conflict in two repositories gets
		// two issues. It is the fingerprint the issue had in that repository before CNFLACT-006.
		fp := hash.Fingerprint(string(entry.Type), entry.Repo, entry.FilePath)
		remember(central, fp)
		remember(central, title)
		var oldTitle string // the title of this conflict's issue in its own repository, before CNFLACT-006
		if !aggregate {
			oldTitle = targetTitle(entry.Type, entry.FilePath)
			remember(entry.Repo, fp)
			remember(entry.Repo, oldTitle)
		}
		if severityRank[entry.Severity] < severityRank[threshold] {
			continue
		}
		if !s.mayPublish(ctx, participants) {
			report.IssuesWithheld++ // still remembered above, so its open issues are kept
			continue
		}

		// Before CNFLDTS-009 a duplicate-skill finding was keyed by the skill's bare filename. An open issue still
		// keyed that way describes the same conflict, so it is updated and retitled to the full path rather than
		// closed and replaced (CNFLACT-016). A conflict about the central repo itself was filed there before
		// CNFLACT-006 under its old title, which is retitled the same way.
		var legacyTitle, legacyFP string
		if base := pathpkg.Base(entry.FilePath); entry.Type == TypeDuplicateSkill && base != entry.FilePath {
			legacyTitle = targetTitle(entry.Type, base)
			legacyFP = hash.Fingerprint(string(entry.Type), entry.Repo, base)
		} else if !aggregate && entry.Repo == central {
			legacyTitle = oldTitle
		}

		renderBody := func(participants []string) string {
			body := entry.Description
			if entry.SuggestedResolution != "" {
				body += "\n\n**Suggested resolution:** " + entry.SuggestedResolution
			}
			body += "\n\n" + fingerprintPrefix + fp + fingerprintSuffix
			body += "\n" + typePrefix + string(entry.Type) + fingerprintSuffix
			if len(participants) > 0 {
				body += "\n" + reposPrefix + strings.Join(participants, ", ") + fingerprintSuffix
			}
			return body
		}
		body := renderBody(participants)

		if !listed {
			var err error
			openIssues, err = s.ghClient.Issues.List(ctx, s.centralOwner, s.centralRepo, &gh.IssueListOptions{State: "open"})
			if err != nil {
				return fmt.Errorf("listing issues for %s: %w", central, err)
			}
			listed = true
		}

		number := 0
		for _, issue := range openIssues {
			if !issue.Trusted(s.cfg.Conflicts.IssueLabel) {
				continue
			}
			issueFP := extractFingerprint(issue.Body)
			isCurrent := issueFP == fp || issue.Title == title
			legacyKey := legacyTitle != "" && ((legacyFP != "" && issueFP == legacyFP) || issue.Title == legacyTitle)
			if !isCurrent && !legacyKey {
				continue
			}
			// Retitle an issue still titled with a legacy title, and one found only by a legacy key. An issue
			// found by its current fingerprint keeps any other title someone gave it.
			retitle := issue.Title != title && ((legacyTitle != "" && issue.Title == legacyTitle) || !isCurrent)
			if aggregate {
				// Keep previously recorded participants this run could not vouch for (unscanned or failed read),
				// so a later cleanup still requires their coverage. An issue that predates the marker has an
				// unknown participant list: leave it without one, so cleanup never closes it on partial evidence
				// (it needs a human to close).
				if prev := issueRepos(issue); prev == nil {
					body = renderBody(nil)
				} else {
					body = renderBody(s.mergeParticipants(participants, prev, targets))
				}
			}
			update := &gh.IssueUpdate{Body: &body}
			if retitle {
				update.Title = &title
			}
			if err := s.ghClient.Issues.Update(ctx, s.centralOwner, s.centralRepo, issue.Number, update); err != nil {
				return fmt.Errorf("updating issue for %s: %w", central, err)
			}
			// Keep the listed copy current, so a later entry this run does not claim the same issue again.
			issue.Body = body
			if retitle {
				issue.Title = title
			}
			number = issue.Number
			break
		}

		if number == 0 {
			created, err := s.ghClient.Issues.Create(ctx, s.centralOwner, s.centralRepo, &gh.IssueCreate{
				Title:  title,
				Body:   body,
				Labels: []string{s.cfg.Conflicts.IssueLabel},
			})
			if err != nil {
				return fmt.Errorf("creating issue for %s: %w", central, err)
			}
			if created != nil {
				openIssues = append(openIssues, created)
				number = created.Number
			}
			report.IssuesCreated++
		}
		if !aggregate && entry.Repo != central && number != 0 {
			move(entry.Repo, fp, number)
			move(entry.Repo, oldTitle, number)
		}
	}

	if report.IssuesWithheld > 0 {
		// The central repository's logs are as public as its issues, so this names no repository.
		sslog.L().Warn("conflict: issues withheld: the central repository is public and these conflicts concern "+
			"private or internal repositories; make the central repository private to file them",
			"operation", "conflict", "issues_withheld", report.IssuesWithheld)
	}

	autoClose := s.cfg.Conflicts.AutoClose == nil || *s.cfg.Conflicts.AutoClose
	return s.closeResolvedIssues(ctx, report, targets, current, moved, ran, autoClose)
}

// closeResolvedIssues closes the open issues carrying the conflict label whose conflict is gone, when autoClose is
// set: in the central repo, those whose fingerprint (or title) this run did not report. Monitor has done the same for
// drift issues since v0.2.0; conflict issues used to stay open forever (SteerSpec/strspc-sync#55).
//
// An issue is only closed when this run can vouch for it: the issue's type belongs to a tier that ran, every
// repository its participants marker lists was scanned with no failed content read, and the tracked file it is
// about was found.
//
// Each target is also cleaned of the issues conflict filed there before CNFLACT-006. One whose conflict now has a
// central issue is a duplicate: it is closed even when autoClose is off, with a line pointing to the central issue
// (CNFLACT-007). One whose conflict is gone is closed as resolved under the rules above. This pass is best-effort:
// the token may not reach the target's issues, and a failure there is logged, not returned.
func (s *Scanner) closeResolvedIssues(ctx context.Context, report *ConflictReport, targets []string,
	current map[string]map[string]bool, moved map[string]map[string]int, ran map[int]bool, autoClose bool,
) error {
	if len(targets) == 0 {
		return nil
	}
	central := s.centralOwner + "/" + s.centralRepo
	scanned := make(map[string]bool, len(targets))
	for _, t := range targets {
		scanned[t] = true
	}
	covered := func(participants []string) bool {
		for _, p := range participants {
			if !scanned[p] || s.incomplete[p] {
				return false
			}
		}
		return len(participants) > 0
	}
	// Version-drift and manual-override issues about a tracked file that was absent this run were not checked;
	// recognise them by fingerprint or title. Central issues use the repo-qualified title, target issues the old one.
	uncheckedCentral := make(map[string]bool)
	uncheckedIn := make(map[string]map[string]bool)
	for repo, paths := range s.missingTracked {
		uncheckedIn[repo] = make(map[string]bool)
		for path := range paths {
			for _, typ := range []ConflictType{TypeVersionDrift, TypeManualOverride} {
				fp := hash.Fingerprint(string(typ), repo, path)
				uncheckedCentral[fp] = true
				uncheckedCentral[conflictTitle(typ, repo, path, false)] = true
				uncheckedIn[repo][fp] = true
				uncheckedIn[repo][targetTitle(typ, path)] = true
				if repo == central {
					uncheckedCentral[targetTitle(typ, path)] = true // a central issue from before CNFLACT-006
				}
			}
		}
	}
	// resolvable reports whether issue may be closed as resolved on this run's evidence, given the keys of the
	// tracked files that were not checked.
	resolvable := func(issue *gh.Issue, unchecked map[string]bool) bool {
		typ := issueType(issue)
		tier := tierOfType(typ)
		if typ == TypeManualOverride && legacyOverlapIssue(issue) {
			tier = 2 // tier 2 filed it, and tier 2 files its section-overlap replacement
		}
		if tier == 0 || !ran[tier] {
			return false // unknown type, or a tier this run did not check
		}
		if (typ == TypeVersionDrift || (typ == TypeManualOverride && !legacyOverlapIssue(issue))) &&
			(unchecked[extractFingerprint(issue.Body)] || unchecked[issue.Title]) {
			return false // that tracked file is gone, so it was not checked
		}
		return true
	}

	if autoClose {
		existing, err := s.ghClient.Issues.List(ctx, s.centralOwner, s.centralRepo, &gh.IssueListOptions{
			State:  "open",
			Labels: []string{s.cfg.Conflicts.IssueLabel},
		})
		if err != nil {
			return fmt.Errorf("listing issues for %s: %w", central, err)
		}
		for _, issue := range existing {
			if current[central][extractFingerprint(issue.Body)] || current[central][issue.Title] {
				continue
			}
			if !resolvable(issue, uncheckedCentral) {
				continue
			}
			if participants := issueRepos(issue); participants != nil {
				if !covered(participants) {
					continue // not every repository it concerns was scanned cleanly this run
				}
			} else if issueType(issue) == TypeDuplicateSkill || !covered([]string{central}) {
				// An aggregate issue from before the participants marker has an unknown coverage, and any other
				// marker-less issue predates CNFLACT-006: it is about the central repo's own files.
				continue
			}
			if err := s.ghClient.Issues.Close(ctx, s.centralOwner, s.centralRepo, issue.Number); err != nil {
				return fmt.Errorf("closing resolved issue #%d in %s: %w", issue.Number, central, err)
			}
			report.IssuesClosed++
		}
	}

	seen := map[string]bool{central: true}
	for _, repo := range targets {
		if seen[repo] {
			continue
		}
		seen[repo] = true
		s.cleanTargetIssues(ctx, report, repo, current[repo], moved[repo], autoClose && !s.incomplete[repo],
			func(issue *gh.Issue) bool { return resolvable(issue, uncheckedIn[repo]) })
	}
	return nil
}

// legacyOverlapIssue reports whether a manual-override issue is a section-overlap finding from before section-overlap
// had its own type: tier 2 filed overlaps as manual-override, with the same repo and path fingerprint tier 1 now uses.
// Its body starts with the overlap description, which tier 1 never writes. It is not about a managed-section edit, so
// a missing tracked file must not keep it open: no tier reports it under that type again.
func legacyOverlapIssue(issue *gh.Issue) bool {
	return strings.HasPrefix(issue.Body, "STEERSPEC section ") && strings.Contains(issue.Body, " appears in both ")
}

// cleanTargetIssues closes the conflict issues filed in repo before CNFLACT-006: one whose conflict has a central
// issue (moved), and, when closeResolved is set, one whose conflict this run no longer detects and resolvable accepts.
// Failures are logged: the token may be scoped to the central repository.
func (s *Scanner) cleanTargetIssues(ctx context.Context, report *ConflictReport, repo string, current map[string]bool,
	moved map[string]int, closeResolved bool, resolvable func(*gh.Issue) bool,
) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return
	}
	existing, err := s.ghClient.Issues.List(ctx, owner, name, &gh.IssueListOptions{
		State:  "open",
		Labels: []string{s.cfg.Conflicts.IssueLabel},
	})
	if err != nil {
		sslog.L().Warn("conflict: could not list legacy conflict issues", "operation", "conflict", "repo", repo, "err", err)
		return
	}
	for _, issue := range existing {
		key := extractFingerprint(issue.Body)
		number, ok := moved[key]
		if !ok || key == "" {
			number, ok = moved[issue.Title]
		}
		switch {
		case ok:
			body := issue.Body + fmt.Sprintf("\n\n**Moved:** this conflict is now tracked in %s/%s#%d.",
				s.centralOwner, s.centralRepo, number)
			if err := s.ghClient.Issues.Update(ctx, owner, name, issue.Number, &gh.IssueUpdate{Body: &body}); err != nil {
				sslog.L().Warn("conflict: could not mark a legacy conflict issue as moved", "operation", "conflict",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			if err := s.ghClient.Issues.Close(ctx, owner, name, issue.Number); err != nil {
				sslog.L().Warn("conflict: could not close a moved conflict issue", "operation", "conflict",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			report.IssuesMoved++
		case current[key] && key != "", current[issue.Title]:
			// Still detected, but below the severity threshold or not filed this run: left alone.
		case closeResolved && resolvable(issue):
			if err := s.ghClient.Issues.Close(ctx, owner, name, issue.Number); err != nil {
				sslog.L().Warn("conflict: could not close a resolved legacy conflict issue", "operation", "conflict",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			report.IssuesClosed++
		}
	}
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
