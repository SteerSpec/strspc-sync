package sync

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/hash"
	sslog "github.com/SteerSpec/strspc-sync/internal/log"
	"github.com/SteerSpec/strspc-sync/internal/registry"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

// bundleBranchPrefix starts the branch of a repo-mode SyncPullRequest (SYNCPR-008).
const bundleBranchPrefix = "steerspec-sync/repo/"

// bundleBranch names the branch of a repo-mode SyncPullRequest carrying entries, each "<template-id>@<version>"
// (SYNCPR-008): the first 8 lowercase hex characters of the Blake3 hash of the entries' UTF-8 bytes, sorted bytewise
// and joined by "\n" with no trailing newline.
func bundleBranch(entries []string) string {
	sorted := slices.Clone(entries)
	slices.Sort(sorted) // Go orders strings by their bytes: a bytewise sort of the UTF-8 entries
	sum := strings.TrimPrefix(hash.HashString(strings.Join(sorted, "\n")), "b3_")
	return bundleBranchPrefix + sum[:8]
}

// isBundleBranch reports whether head is the branch of a repo-mode SyncPullRequest: bundleBranchPrefix and exactly
// 8 lowercase hex characters (SYNCPR-008). A per-template branch of a template with id "repo" ends in a version,
// which holds a ".", so it is not one.
func isBundleBranch(head string) bool {
	rest, ok := strings.CutPrefix(head, bundleBranchPrefix)
	if !ok || len(rest) != 8 {
		return false
	}
	for _, c := range rest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// bundleMember is a template a repo-mode SyncPullRequest carries: out of date for its repository and rendered
// without error (SYNCPR-011).
type bundleMember struct {
	tmpl config.TemplateConfig
	r    rendering
}

// syncBundle syncs tmpls to target in one SyncPullRequest (pr-grouping "repo", SYNCCFG-031). Templates are rendered
// in config order, and one whose destination an earlier one already rendered is rendered onto that result, so
// templates sharing a file do not conflict. It returns one result per template.
func (s *Syncer) syncBundle(ctx context.Context, target registry.ResolvedTarget, tmpls []config.TemplateConfig,
	dryRun, force bool,
) []RepoResult {
	var results []RepoResult
	var members []bundleMember
	rendered := make(map[string][]byte) // destination -> content after the members rendered so far
	var paths []string                  // destinations, in the order they were first rendered
	for _, tmpl := range tmpls {
		r, failed := s.render(ctx, target, tmpl, rendered)
		if failed != nil {
			// Ship the rest: a template that cannot be rendered is reported and keeps its state (SYNCPR-011).
			results = append(results, *failed)
			continue
		}
		if r.inSync() {
			rr := RepoResult{Repo: target.Repo, TemplateID: tmpl.ID}
			s.markInSync(ctx, target, tmpl, r, dryRun, &rr)
			results = append(results, rr)
			continue
		}
		if _, seen := rendered[tmpl.Destination]; !seen {
			paths = append(paths, tmpl.Destination)
		}
		rendered[tmpl.Destination] = r.rendered
		members = append(members, bundleMember{tmpl: tmpl, r: r})
	}
	if len(members) == 0 {
		return results
	}
	return append(results, s.deliverBundle(ctx, target, members, rendered, paths, dryRun, force)...)
}

// deliverBundle opens or updates the SyncPullRequest carrying members, and records it for each of them.
func (s *Syncer) deliverBundle(ctx context.Context, target registry.ResolvedTarget, members []bundleMember,
	rendered map[string][]byte, paths []string, dryRun, force bool,
) []RepoResult {
	owner, repo := splitRepo(target.Repo)
	results := make([]RepoResult, len(members))
	ids := make([]string, len(members))
	entries := make([]string, len(members))
	for i, m := range members {
		results[i] = RepoResult{Repo: target.Repo, TemplateID: m.tmpl.ID}
		ids[i] = m.tmpl.ID
		entries[i] = m.tmpl.ID + "@" + m.tmpl.Version
	}
	fail := func(format string, args ...any) []RepoResult {
		for i := range results {
			results[i].Status = "error"
			results[i].Error = fmt.Sprintf(format, args...)
		}
		return results
	}

	// Nothing new: every template renders as the deployment state recorded it, on one PR. The bundle as a whole is
	// skipped, and a PR closed without merging is left to --force, as for a single template (GH #91).
	if number, ok := recordedOnOnePR(members); ok && !force {
		var status string
		var pr *gh.PullRequest
		for i, m := range members {
			var st string
			var p *gh.PullRequest
			if dryRun {
				// A dry run writes nothing (SYNCOP-009): the PR is looked up, the records are left as they are.
				st, p = s.lookupPRStatus(ctx, owner, repo, m.r.prev)
			} else {
				st, p = s.refreshPRStatus(ctx, owner, repo, target.Repo, m.tmpl.ID, m.r.prev)
			}
			if i == 0 {
				status, pr = st, p
			}
			results[i].Status = "skipped"
			results[i].PRNumber = number
		}
		if !dryRun {
			s.repairPRLabel(ctx, owner, repo, status, pr, &results[0])
		}
		if status == "closed" {
			for i := range results {
				results[i].Error = fmt.Sprintf("sync PR #%d was closed without merging; run with force to open it again",
					number)
			}
			sslog.L().Warn("sync PR closed without merging, target not updated",
				"operation", "sync", "template_ids", ids, "repo", target.Repo, "pr_number", number)
		}
		return results
	}

	branch := bundleBranch(entries)
	if dryRun {
		sslog.L().Info("dry-run: would sync templates in one pull request",
			"operation", "sync", "dry_run", true, "template_ids", ids, "repo", target.Repo, "branch", branch)
		for i := range results {
			results[i].Status = "skipped"
		}
		return results
	}

	baseSHA, err := s.getBaseSHA(ctx, owner, repo, target.DefaultBranch)
	if err != nil {
		return fail("getting base SHA: %v", err)
	}
	// A 422 means the branch exists: the same versions were proposed before, and new content adds a commit to it
	// (SYNCPR-009).
	createErr := s.ghClient.PullRequests.CreateBranch(ctx, owner, repo, branch, baseSHA)
	if createErr != nil && !isAlreadyExists(createErr) {
		return fail("creating branch: %v", createErr)
	}
	createdBranch := createErr == nil
	head := baseSHA
	if !createdBranch {
		if head, err = s.ghClient.Repos.GetBranchSHA(ctx, owner, repo, branch); err != nil {
			return fail("reading head of %s: %v", branch, err)
		}
	}

	// One commit per run with every file that changes (SYNCPR-011). On an existing branch, files already holding
	// their render are left out; when none changes, there is no commit.
	var files []gh.FileChange
	for _, p := range paths {
		if !createdBranch {
			current, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, p, branch)
			if err != nil && !isNotFound(err) {
				return fail("reading %s on %s: %v", p, branch, err)
			}
			if err == nil && bytes.Equal(current, rendered[p]) {
				continue
			}
		}
		files = append(files, gh.FileChange{Path: p, Content: rendered[p]})
	}
	if len(files) > 0 {
		err := s.ghClient.Repos.CommitFiles(ctx, owner, repo, branch, head, files, bundleMessage(s.commitPrefix(), members))
		if err != nil {
			if createdBranch {
				s.deleteEmptyBranch(ctx, owner, repo, target.Repo, branch, baseSHA)
			}
			return fail("committing files: %v", err)
		}
	}

	pr, status, errMsg := s.upsertPR(ctx, owner, repo, branch, target.DefaultBranch,
		s.bundleTitle(members), bundleBody(members))
	for i := range results {
		results[i].Status, results[i].Error = status, errMsg
	}
	if pr == nil {
		return results
	}
	for i := range results {
		results[i].PRNumber, results[i].PRURL = pr.Number, pr.URL
	}

	if s.closeStale() {
		s.closeStalePRs(ctx, owner, repo, target.Repo, ids, pr.Number)
	}

	now := time.Now().UTC()
	s.stateMu.Lock()
	for _, m := range members {
		s.state.SetTemplateState(target.Repo, m.tmpl.ID, state.TemplateState{
			Version:         m.tmpl.Version,
			Hash:            m.r.contentHash,
			Timestamp:       now,
			PRNumber:        pr.Number,
			PRStatus:        "open",
			PreviousVersion: m.r.previousVersion,
			Sections:        m.r.managed.names,
			SectionsHash:    m.r.managed.hash,
		})
	}
	s.stateMu.Unlock()
	return results
}

// lookupPRStatus is the status of prev's PR as refreshPRStatus would record it, without recording it: the recorded
// status when it is final ("merged", "superseded") or the PR cannot be read, otherwise the PR's current one.
func (s *Syncer) lookupPRStatus(ctx context.Context, owner, repo string, prev *state.TemplateState,
) (string, *gh.PullRequest) {
	if prev == nil {
		return "", nil
	}
	if prev.PRNumber == 0 || prev.PRStatus == "merged" || prev.PRStatus == prSuperseded {
		return prev.PRStatus, nil
	}
	pr, err := s.ghClient.PullRequests.Get(ctx, owner, repo, prev.PRNumber)
	if err != nil || pr == nil {
		return prev.PRStatus, nil
	}
	if pr.State == "closed" && pr.Merged {
		return "merged", pr
	}
	return pr.State, pr
}

// recordedOnOnePR reports whether every member renders as its record says, and all records name the same sync PR
// that sync has not superseded. It returns that PR's number.
func recordedOnOnePR(members []bundleMember) (int, bool) {
	number := 0
	for _, m := range members {
		p := m.r.prev
		if p == nil || p.Hash != m.r.contentHash || p.PRNumber == 0 || p.PRStatus == prSuperseded {
			return 0, false
		}
		if number != 0 && p.PRNumber != number {
			return 0, false
		}
		number = p.PRNumber
	}
	return number, true
}

// bundleTitle is a repo-mode PR's title: the per-template title when it carries one template, otherwise
// "<pr-title-prefix>: update <n> templates".
func (s *Syncer) bundleTitle(members []bundleMember) string {
	if len(members) == 1 {
		return updateMessage(s.prTitlePrefix(), members[0].tmpl.ID, members[0].tmpl.Version)
	}
	return fmt.Sprintf("%s: update %d templates", s.prTitlePrefix(), len(members))
}

// bundleMessage is a repo-mode commit's message: the per-template message for one template, otherwise
// "<prefix>: update <n> templates" with one "update <template-id> to <version>" line per template in its body.
func bundleMessage(prefix string, members []bundleMember) string {
	if len(members) == 1 {
		return updateMessage(prefix, members[0].tmpl.ID, members[0].tmpl.Version)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: update %d templates\n", prefix, len(members))
	for _, m := range members {
		fmt.Fprintf(&b, "\n- update %s to %s", m.tmpl.ID, m.tmpl.Version)
	}
	return b.String()
}
