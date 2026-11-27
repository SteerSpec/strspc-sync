package conflict

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"slices"
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
	// repoHashesPrefix lists a stub's participants as keyed hashes (stubRepoHash), so cleanup can check its
	// coverage without their names.
	repoHashesPrefix = "<!-- steerspec-conflict-repo-hashes: "
)

// Options controls what the scanner checks.
type Options struct {
	ConfigPath string
	Tiers      []int
	// HashKey keys the fingerprint and participant hashes of a stub, the central issue of a conflict about a private
	// or internal repository when the central repository is public. Without it such conflicts are not filed.
	HashKey string
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
	// targetIssues caches the open issues of a target listed this run to keep a conflict's full issue there.
	targetIssues map[string][]*gh.Issue
	// hashKey keys the values a stub carries (Options.HashKey).
	hashKey []byte
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

// issueRepos recovers the participant list of an issue from its body
// marker; nil when the marker is absent (an issue created before it, or one
// whose names are withheld).
func issueRepos(issue *gh.Issue) []string { return markerList(issue.Body, reposPrefix) }

// markerList returns the comma-separated values of the marker starting with prefix in body, or nil.
func markerList(body, prefix string) []string {
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return nil
	}
	rest := body[idx+len(prefix):]
	end := strings.Index(rest, fingerprintSuffix)
	if end < 0 {
		return nil
	}
	var values []string
	for _, v := range strings.Split(rest[:end], ",") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values
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
	s.targetIssues = make(map[string][]*gh.Issue)
	s.hashKey = []byte(opts.HashKey)
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

// errVisibility reports a repository visibility that could not be read. It names no repository: the central
// repository's job logs may be public.
var errVisibility = errors.New("a repository's visibility could not be read, so its conflicts were not filed")

// centralPublic reports whether the central repository's issues and logs are public: it is public, or its
// visibility cannot be read (filing stops then, see createIssuesForConflicts; logging stays cautious).
func (s *Scanner) centralPublic(ctx context.Context) bool {
	v := s.visibilityOf(ctx, s.centralOwner+"/"+s.centralRepo)
	return v == "public" || v == ""
}

// mayPublish reports whether the details of a conflict about repos may be shown in the central repository. A public
// central repository's issues are public, so they must not name a private or internal repository or its files. A
// target visibility that cannot be read is an error, not a guess: either guess would rewrite issues wrongly.
func (s *Scanner) mayPublish(ctx context.Context, repos []string) (bool, error) {
	if !s.centralPublic(ctx) {
		return true, nil
	}
	for _, r := range repos {
		switch s.visibilityOf(ctx, r) {
		case "":
			return false, errVisibility
		case "public":
		default:
			return false, nil
		}
	}
	return true, nil
}

// errNoHashKey stops a conflict that needs a stub when no key is set.
var errNoHashKey = errors.New("a conflict about a private or internal repository needs a stub in the public central " +
	"repository, and stubs need the conflict hash key (the action's hash-key input, STEERSPEC_CONFLICT_HASH_KEY); " +
	"the stub was not opened, though its issue in the repository concerned is kept")

// keyed returns the HMAC-SHA256 of parts under the conflict hash key, truncated like a fingerprint. A stub in a public
// central repository carries only keyed values: an unkeyed hash of a guessable repository name or path would let anyone
// who tries candidates learn what the stub is about.
func (s *Scanner) keyed(parts ...string) string {
	m := hmac.New(sha256.New, s.hashKey)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// stubFP is the fingerprint a stub carries for the conflict of type typ about path in repo.
func (s *Scanner) stubFP(typ ConflictType, repo, path string) string {
	return s.keyed("fingerprint", string(typ), repo, path)
}

// stubRepoHash stands for a participant in a stub's marker, so cleanup can check its coverage without its name.
func (s *Scanner) stubRepoHash(repo string) string { return s.keyed("repo", repo) }

func (s *Scanner) stubRepoHashes(repos []string) []string {
	hashes := make([]string, len(repos))
	for i, r := range repos {
		hashes[i] = s.stubRepoHash(r)
	}
	return hashes
}

// mergeStubHashes is mergeParticipants on stub hashes: current, plus every previous hash this run cannot vouch for
// because no target scanned cleanly hashes to it.
func (s *Scanner) mergeStubHashes(current, previous, targets []string) []string {
	vouched := make(map[string]bool, len(targets))
	for _, t := range targets {
		if !s.incomplete[t] {
			vouched[s.stubRepoHash(t)] = true
		}
	}
	merged := s.stubRepoHashes(current)
	have := make(map[string]bool, len(merged))
	for _, h := range merged {
		have[h] = true
	}
	for _, h := range previous {
		if !have[h] && !vouched[h] {
			merged = append(merged, h)
			have[h] = true
		}
	}
	return merged
}

// isStub reports whether a central issue is a stub: details withheld from a public central repository.
func isStub(issue *gh.Issue) bool { return strings.HasPrefix(issue.Body, withheldNotice) }

// withheldTitle is the title of a stub.
func withheldTitle(typ ConflictType) string {
	return fmt.Sprintf("[SteerSpec Conflict] %s: non-public repository", typ)
}

// withheldNotice opens the body of a stub.
const withheldNotice = "**Details withheld.** This conflict concerns a private or internal repository, and this " +
	"repository is public, so the repositories, files and description are not shown here. They are in an issue in " +
	"each private or internal repository concerned, and in the conflict report of the run. Make this repository " +
	"private or internal to show them here."

// fileInTarget keeps a conflict's full issue in a private or internal repository while its central issue is a stub:
// the open issue matching fp or title is updated (CNFLACT-016), or one is opened when mayCreate is set. It is
// best-effort, since the token may be scoped to the central repository, and its warnings name nothing: the central
// repository's job logs are public.
func (s *Scanner) fileInTarget(ctx context.Context, report *ConflictReport, repo, fp, title, body string, mayCreate bool) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return
	}
	warn := func(msg string) { sslog.L().Warn("conflict: "+msg, "operation", "conflict") }
	existing, listed := s.targetIssues[repo]
	if !listed {
		existing, err = s.ghClient.Issues.List(ctx, owner, name, &gh.IssueListOptions{State: "open"})
		if err != nil {
			warn("could not list the issues of a non-public repository to describe its conflict there")
			return
		}
		s.targetIssues[repo] = existing
	}
	for _, issue := range existing {
		if !issue.Trusted(s.cfg.Conflicts.IssueLabel) || (extractFingerprint(issue.Body) != fp && issue.Title != title) {
			continue
		}
		if err := s.ghClient.Issues.Update(ctx, owner, name, issue.Number, &gh.IssueUpdate{Body: &body}); err != nil {
			warn("could not update the issue describing a conflict in a non-public repository")
			return
		}
		issue.Body = body
		return
	}
	if !mayCreate {
		return
	}
	created, err := s.ghClient.Issues.Create(ctx, owner, name, &gh.IssueCreate{
		Title: title, Body: body, Labels: []string{s.cfg.Conflicts.IssueLabel},
	})
	if err != nil {
		warn("could not open the issue describing a conflict in a non-public repository")
		return
	}
	if created != nil {
		s.targetIssues[repo] = append(existing, created)
	}
	report.IssuesCreated++
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

// createIssuesForConflicts files every conflict at or above the threshold in the central repository (CNFLACT-006)
// and updates the open issue of every conflict still detected (CNFLACT-016).
//
// A public central repository's issues are public. A conflict about a private or internal repository gets a stub
// there, which names nothing and carries only keyed hashes, and a full issue in each private or internal repository
// it concerns, pointing to the stub. A full central issue whose details must now be hidden is closed and replaced by a
// fresh stub: its title and body history would keep them (an organization owner can delete it).
func (s *Scanner) createIssuesForConflicts(ctx context.Context, report *ConflictReport, targets []string, ran map[int]bool) error {
	central := s.centralOwner + "/" + s.centralRepo
	// Keys of every conflict detected this run, per repo the issue may be in, so issues whose conflict is gone can be
	// closed afterwards (SteerSpec/strspc-sync#55). Under central: fingerprints and titles of the central issues.
	// Under a target: the keys of its own issues. A conflict below the threshold is remembered too: it is still
	// there, so its issue is not closed as resolved.
	current := make(map[string]map[string]bool)
	remember := func(repo, key string) {
		if current[repo] == nil {
			current[repo] = make(map[string]bool)
		}
		current[repo][key] = true
	}
	// moved maps the keys of a conflict's issue in a target repository to the central issue that now tracks it, so
	// cleanup can close the target copy (CNFLACT-007).
	moved := make(map[string]map[string]int)
	move := func(repo, key string, number int) {
		if moved[repo] == nil {
			moved[repo] = make(map[string]int)
		}
		moved[repo][key] = number
	}

	// The central repository's visibility decides how every issue is written. When it cannot be read, nothing is
	// written or closed: treating it as public would redact every issue about a private target and open their
	// copies there, and treating it as private could publish private details.
	if s.visibilityOf(ctx, central) == "" {
		return errVisibility
	}
	var skipped []error

	// The central repo's open issues, listed once. The label is not required: an issue whose label was removed or
	// renamed is still found by its fingerprint or title, so it is updated rather than duplicated (CNFLACT-007/016).
	// Both keys are specific to one conflict. An unlabeled issue counts only when a trusted author opened it, so a
	// copied title cannot capture the update.
	var openIssues []*gh.Issue
	listed := false
	threshold := s.threshold()
	label := s.cfg.Conflicts.IssueLabel

entries:
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
		stubFP := s.stubFP(entry.Type, entry.Repo, entry.FilePath)
		remember(central, fp)
		remember(central, title)
		remember(central, stubFP)
		// The title of the conflict's issue in a target: before CNFLACT-006, and for a full issue in a private or
		// internal repository behind a stub. A duplicate skill's copy in a participant has its own fingerprint.
		ownTitle := targetTitle(entry.Type, entry.FilePath)
		ownFP := func(repo string) string {
			if !aggregate {
				return fp
			}
			return hash.Fingerprint(string(entry.Type), repo, entry.FilePath)
		}
		for _, p := range participants {
			remember(p, ownFP(p))
			remember(p, ownTitle)
		}
		// Below the threshold a conflict gets no new issue, but an open one is still updated (CNFLACT-016).
		mayCreate := severityRank[entry.Severity] >= severityRank[threshold]
		publishable, err := s.mayPublish(ctx, participants)
		if err != nil {
			skipped = append(skipped, err) // remembered above, so its issues are kept; nothing is written for it
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
			legacyTitle = ownTitle
		}
		hidden := withheldTitle(entry.Type)

		// fullBody is the issue with its details: description, resolution, an optional note, then the markers.
		fullBody := func(fingerprint, note string, names []string) string {
			body := entry.Description
			if entry.SuggestedResolution != "" {
				body += "\n\n**Suggested resolution:** " + entry.SuggestedResolution
			}
			if note != "" {
				body += "\n\n" + note
			}
			body += "\n\n" + fingerprintPrefix + fingerprint + fingerprintSuffix
			body += "\n" + typePrefix + string(entry.Type) + fingerprintSuffix
			if len(names) > 0 {
				body += "\n" + reposPrefix + strings.Join(names, ", ") + fingerprintSuffix
			}
			return body
		}
		// stubBody names nothing: its fingerprint and participants are keyed.
		stubBody := func(hashes []string) string {
			body := withheldNotice + "\n\n" + fingerprintPrefix + stubFP + fingerprintSuffix
			body += "\n" + typePrefix + string(entry.Type) + fingerprintSuffix
			if len(hashes) > 0 {
				body += "\n" + repoHashesPrefix + strings.Join(hashes, ", ") + fingerprintSuffix
			}
			return body
		}

		if !listed {
			openIssues, err = s.ghClient.Issues.List(ctx, s.centralOwner, s.centralRepo, &gh.IssueListOptions{State: "open"})
			if err != nil {
				return fmt.Errorf("listing issues for %s: %w", central, err)
			}
			listed = true
		}

		number := 0
		redacted := !publishable
		replace := false // a full issue was closed for a stub, which is opened even below the threshold
		for _, issue := range openIssues {
			if !issue.Trusted(label) {
				continue
			}
			issueFP := extractFingerprint(issue.Body)
			stub := isStub(issue)
			isCurrent := issueFP == fp || (stub && issueFP == stubFP) || (!stub && issue.Title == title)
			legacyKey := !stub && legacyTitle != "" &&
				((legacyFP != "" && issueFP == legacyFP) || issue.Title == legacyTitle)
			if !isCurrent && !legacyKey {
				continue
			}
			// The participants the update writes. An aggregate issue keeps previously recorded participants this run
			// could not vouch for (unscanned or failed read), so a later cleanup still requires their coverage. One
			// that predates the markers has an unknown participant list: it stays without one, so cleanup never
			// closes it on partial evidence (it needs a human to close). A stub holds keyed hashes, not names.
			names := participants
			var hashes []string
			if aggregate {
				switch prev := issueRepos(issue); {
				case stub:
					names = nil
					hashes = s.mergeStubHashes(participants, markerList(issue.Body, repoHashesPrefix), targets)
				case prev == nil:
					names = nil
				default:
					names = s.mergeParticipants(participants, prev, targets)
				}
			}
			// Publication is decided on the participants written, not only the ones found this run. A stub's
			// retained names are unknown, so only a central repository that is not public shows its details again.
			publish := publishable
			switch {
			case names != nil:
				ok, err := s.mayPublish(ctx, names)
				if err != nil {
					skipped = append(skipped, err)
					continue entries
				}
				publish = ok
			case stub:
				publish = publishable && !s.centralPublic(ctx)
			}
			redacted = !publish

			switch {
			case publish:
				// Retitle an issue titled as a stub or with a legacy title, and one found only by a legacy key. An
				// issue found by its current fingerprint otherwise keeps any other title someone gave it.
				body := fullBody(fp, "", names)
				retitle := issue.Title != title &&
					(stub || (legacyTitle != "" && issue.Title == legacyTitle) || !isCurrent)
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
			case stub && len(s.hashKey) == 0:
				// Without the key, a stub cannot be rewritten: it is left as is, and not duplicated.
				number = issue.Number
			case stub:
				if hashes == nil {
					hashes = s.stubRepoHashes(participants)
				}
				body := stubBody(hashes)
				update := &gh.IssueUpdate{Body: &body}
				if issue.Title != hidden {
					update.Title = &hidden
				}
				if err := s.ghClient.Issues.Update(ctx, s.centralOwner, s.centralRepo, issue.Number, update); err != nil {
					return fmt.Errorf("updating issue for %s: %w", central, err)
				}
				issue.Body, issue.Title = body, hidden
				number = issue.Number
			default:
				// A full issue whose details must now be hidden. Editing it would keep them in its title and body
				// history, so it is closed and a fresh stub opened; an organization owner can delete the old one. It is
				// closed even without the hash key, which only the stub needs.
				if err := s.ghClient.Issues.Close(ctx, s.centralOwner, s.centralRepo, issue.Number); err != nil {
					return fmt.Errorf("closing issue #%d in %s: %w", issue.Number, central, err)
				}
				issue.Body, issue.Title = "", "" // no later entry claims it
				report.IssuesReplaced++
				replace = true
			}
			break
		}

		if number == 0 && (mayCreate || replace) && redacted && len(s.hashKey) == 0 {
			// The stub needs the hash key. The conflict's full issue in its private or internal repository needs
			// none, so it is still kept below; the run reports the missing stub.
			skipped = append(skipped, errNoHashKey)
		} else if number == 0 && (mayCreate || replace) {
			create := &gh.IssueCreate{Title: title, Body: fullBody(fp, "", participants), Labels: []string{label}}
			if redacted {
				create.Title, create.Body = hidden, stubBody(s.stubRepoHashes(participants))
			}
			created, err := s.ghClient.Issues.Create(ctx, s.centralOwner, s.centralRepo, create)
			if err != nil {
				return fmt.Errorf("creating issue for %s: %w", central, err)
			}
			if created != nil {
				created.Body = create.Body
				openIssues = append(openIssues, created)
				number = created.Number
			}
			report.IssuesCreated++
		}

		if !redacted {
			// Copies of this conflict in its targets, filed before CNFLACT-006 or behind a former stub, are now
			// duplicates of the central issue.
			if number != 0 {
				for _, p := range participants {
					if p != central {
						move(p, ownFP(p), number)
						move(p, ownTitle, number)
					}
				}
			}
			continue
		}
		if number != 0 {
			report.IssuesRedacted++
		}
		pointer := ""
		if number != 0 {
			pointer = fmt.Sprintf("Tracked in %s/%s#%d without its details, since that repository is public.",
				s.centralOwner, s.centralRepo, number)
		}
		if !aggregate {
			if entry.Repo != central {
				s.fileInTarget(ctx, report, entry.Repo, fp, ownTitle, fullBody(fp, pointer, participants), mayCreate)
			}
			continue
		}
		// A duplicate skill: a full issue in each private or internal participant, naming itself and the public
		// participants only, so one team's repository does not learn another's private one.
		var public []string
		hiddenCount := 0
		for _, p := range participants {
			if s.visibilityOf(ctx, p) == "public" {
				public = append(public, p)
			} else {
				hiddenCount++
			}
		}
		for _, p := range participants {
			if s.visibilityOf(ctx, p) == "public" || p == central {
				continue
			}
			note := fmt.Sprintf("Repositories with this skill: %s (this repository)", p)
			for _, q := range public {
				note += ", " + q
			}
			if others := hiddenCount - 1; others > 0 {
				note += fmt.Sprintf(", and %d other private or internal repositories", others)
			}
			note += "."
			if pointer != "" {
				note += " " + pointer
			}
			s.fileInTarget(ctx, report, p, ownFP(p), ownTitle, fullBody(ownFP(p), note, []string{p}), mayCreate)
		}
	}

	autoClose := s.cfg.Conflicts.AutoClose == nil || *s.cfg.Conflicts.AutoClose
	return errors.Join(append([]error{s.closeResolvedIssues(ctx, report, targets, current, moved, ran, autoClose)},
		dedupeErrors(skipped)...)...)
}

// dedupeErrors keeps one of each error, so a run that skipped many conflicts for one reason says it once.
func dedupeErrors(errs []error) []error {
	var out []error
	for _, e := range errs {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
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
	// An issue can only be closed when every participant was scanned this run and none of them had a failed read:
	// by name, or for a stub by keyed hash.
	covered := func(participants []string) bool {
		for _, p := range participants {
			if !scanned[p] || s.incomplete[p] {
				return false
			}
		}
		return len(participants) > 0
	}
	clean := make(map[string]bool, len(targets))
	for _, t := range targets {
		if !s.incomplete[t] {
			clean[s.stubRepoHash(t)] = true
		}
	}
	coveredHashes := func(hashes []string) bool {
		for _, h := range hashes {
			if !clean[h] {
				return false
			}
		}
		return len(hashes) > 0
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
				uncheckedCentral[s.stubFP(typ, repo, path)] = true
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
			if names := issueRepos(issue); names != nil {
				if !covered(names) {
					continue // not every repository it concerns was scanned cleanly this run
				}
			} else if hashes := markerList(issue.Body, repoHashesPrefix); hashes != nil {
				if !coveredHashes(hashes) {
					continue // a stub: not every repository it concerns was scanned cleanly this run
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
// issue (moved), and, when closeResolved is set, one carrying the conflict label whose conflict this run no longer
// detects and resolvable accepts. Like the central lookup, it lists every open issue, so a legacy issue whose label was
// removed or renamed is still moved when a trusted author opened it (CNFLACT-007). Failures are logged: the token may
// be scoped to the central repository. When the central repository is public, or its visibility cannot be read, its
// job logs are public, so the warnings name neither the repository nor the API error.
func (s *Scanner) cleanTargetIssues(ctx context.Context, report *ConflictReport, repo string, current map[string]bool,
	moved map[string]int, closeResolved bool, resolvable func(*gh.Issue) bool,
) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return
	}
	redact := s.centralPublic(ctx)
	warn := func(msg string, number int, err error) {
		attrs := []any{"operation", "conflict"}
		if !redact {
			attrs = append(attrs, "repo", repo, "err", err)
			if number != 0 {
				attrs = append(attrs, "issue", number)
			}
		}
		sslog.L().Warn("conflict: "+msg, attrs...)
	}
	existing, err := s.ghClient.Issues.List(ctx, owner, name, &gh.IssueListOptions{State: "open"})
	if err != nil {
		warn("could not list legacy conflict issues in a target repository", 0, err)
		return
	}
	label := s.cfg.Conflicts.IssueLabel
	for _, issue := range existing {
		if !issue.Trusted(label) {
			continue
		}
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
				warn("could not mark a legacy conflict issue as moved", issue.Number, err)
				continue
			}
			if err := s.ghClient.Issues.Close(ctx, owner, name, issue.Number); err != nil {
				warn("could not close a moved conflict issue", issue.Number, err)
				continue
			}
			report.IssuesMoved++
		case current[key] && key != "", current[issue.Title]:
			// Still detected, but below the severity threshold or not filed this run: left alone.
		case closeResolved && hasLabel(issue, label) && resolvable(issue):
			if err := s.ghClient.Issues.Close(ctx, owner, name, issue.Number); err != nil {
				warn("could not close a resolved legacy conflict issue", issue.Number, err)
				continue
			}
			report.IssuesClosed++
		}
	}
}

// hasLabel reports whether issue carries label, compared case-insensitively as GitHub does. Cleanup only closes
// resolved issues that carry the conflict label.
func hasLabel(issue *gh.Issue, label string) bool {
	return label != "" && slices.ContainsFunc(issue.Labels, func(l string) bool { return strings.EqualFold(l, label) })
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
