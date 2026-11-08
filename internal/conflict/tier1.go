package conflict

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/SteerSpec/strspc-sync/internal/state"
)

// crossRefPattern finds references to agent and skill files in CLAUDE.md. The path must start a word (beginning of
// line, whitespace, or an opening bracket, quote or backtick), so "docs/skills/x.md" or a URL is not read as
// "skills/x.md". The capture keeps an optional "./" or ".claude/" prefix so the path is resolved as written.
var crossRefPattern = regexp.MustCompile("(?m)(?:^|[\\s(\\[<\"'`])((?:\\./)?(?:\\.claude/)?(?:agents|skills)/[^\\s)\\]>\"'`]+\\.md)")

func (s *Scanner) runTier1(ctx context.Context, deployState *state.DeploymentState, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	for _, target := range targets {
		owner, repo, err := splitRepo(target)
		if err != nil {
			return nil, err
		}

		// 1. Version drift detection
		driftEntries, err := s.checkVersionDrift(ctx, deployState, owner, repo, target)
		if err != nil {
			return nil, fmt.Errorf("version drift check for %s: %w", target, err)
		}
		entries = append(entries, driftEntries...)

		// 2. Cross-reference broken detection
		crossRefEntries, err := s.checkCrossReferences(ctx, owner, repo, target)
		if err != nil {
			return nil, fmt.Errorf("cross-reference check for %s: %w", target, err)
		}
		entries = append(entries, crossRefEntries...)

		// 3. Unmanaged file detection
		entries = append(entries, s.checkUnmanagedFiles(ctx, deployState, owner, repo, target)...)
	}

	return entries, nil
}

func (s *Scanner) checkVersionDrift(ctx context.Context, deployState *state.DeploymentState, owner, repo, target string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	repoTemplates, ok := deployState.Repositories[target]
	if !ok {
		return nil, nil
	}

	for templateID, ts := range repoTemplates {
		tmplCfg := s.findTemplate(templateID)
		if tmplCfg == nil {
			continue
		}

		content, found, err := s.readFile(ctx, owner, repo, tmplCfg.Destination)
		if err != nil {
			continue // a failed read marks the repo incomplete
		}
		if !found {
			s.noteMissingTracked(target, tmplCfg.Destination)
			continue // file may not exist yet (PR not merged) or was deleted
		}

		drifted, expected, actual := ts.Drift(content)
		if !drifted {
			continue
		}
		entry := ConflictEntry{
			Severity:            SeverityCritical,
			Type:                TypeVersionDrift,
			Repo:                target,
			FilePath:            tmplCfg.Destination,
			Description:         fmt.Sprintf("template %q has drifted: expected hash %s, got %s", templateID, expected, actual),
			SuggestedResolution: "Re-run sync to update the file, or update the deployment state if the change is intentional.",
		}
		if ts.SectionsHash != "" && len(ts.Sections) > 0 {
			entry.Description = fmt.Sprintf("the managed STEERSPEC sections of template %q have changed: expected hash %s, got %s",
				templateID, expected, actualOrNone(actual))
			entry.SuggestedResolution = "Re-run sync with force (`--force`, or the action's `force` input) to " +
				"restore the managed sections; content outside them is kept. To change a managed section, change " +
				"the template in the central repository. If a STEERSPEC marker is broken (unclosed, unpaired or " +
				"nested), fix it by hand first: sync cannot render a file it cannot parse."
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// actualOrNone names a missing hash in a drift description.
func actualOrNone(actual string) string {
	if actual == "" {
		return "no readable section"
	}
	return actual
}

func (s *Scanner) checkCrossReferences(ctx context.Context, owner, repo, target string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	content, found, err := s.readFile(ctx, owner, repo, "CLAUDE.md")
	if err != nil || !found {
		return nil, nil // no CLAUDE.md, nothing to check; a failed read marks the repo incomplete
	}

	seen := make(map[string]bool)
	for _, match := range crossRefPattern.FindAllStringSubmatch(string(content), -1) {
		ref := strings.TrimPrefix(match[1], "./")
		if seen[ref] {
			continue
		}
		seen[ref] = true
		// A glob ("skills/*/SKILL.md") names a set of files, not a path. Checking it would need a directory
		// listing, so it is not reported.
		if strings.ContainsAny(ref, "*?[") {
			continue
		}

		exists, ok := s.referenceExists(ctx, owner, repo, ref)
		if !ok {
			continue // transient; the repo is marked incomplete
		}
		if !exists {
			entries = append(entries, ConflictEntry{
				Severity:            SeverityCritical,
				Type:                TypeCrossReferenceBroken,
				Repo:                target,
				FilePath:            ref,
				Description:         fmt.Sprintf("CLAUDE.md references %q but the file does not exist", ref),
				SuggestedResolution: "Create the missing file or remove the reference from CLAUDE.md.",
			})
		}
	}

	return entries, nil
}

// referenceExists resolves a CLAUDE.md reference as written, from the repository root. A reference without the
// ".claude/" prefix may also mean the file under .claude/, the usual home of agents and skills, so it is found
// if either path exists. ok is false when a read failed for a reason other than 404.
func (s *Scanner) referenceExists(ctx context.Context, owner, repo, ref string) (exists, ok bool) {
	candidates := []string{ref}
	if !strings.HasPrefix(ref, ".claude/") {
		candidates = append(candidates, ".claude/"+ref)
	}
	for _, path := range candidates {
		_, found, err := s.readFile(ctx, owner, repo, path)
		if err != nil {
			return false, false
		}
		if found {
			return true, true
		}
	}
	return false, true
}

// checkUnmanagedFiles reports the AI configuration files a target holds that the deployment state does not track for
// it (rule CNFLRPT: CLAUDE.md or .claude/** not tracked in the manifest). With no directory listing it checks the
// known paths, CLAUDE.md and every configured template destination, and reports only those that exist. A failed read
// marks the repo incomplete and reports nothing.
func (s *Scanner) checkUnmanagedFiles(
	ctx context.Context, deployState *state.DeploymentState, owner, repo, target string,
) []ConflictEntry {
	managed := make(map[string]bool)
	for templateID := range deployState.Repositories[target] {
		if tmplCfg := s.findTemplate(templateID); tmplCfg != nil {
			managed[tmplCfg.Destination] = true
		}
	}

	known := []string{"CLAUDE.md"}
	for _, tmpl := range s.cfg.Templates {
		known = append(known, tmpl.Destination)
	}

	var entries []ConflictEntry
	checked := make(map[string]bool, len(known))
	for _, p := range known {
		if managed[p] || checked[p] {
			continue
		}
		checked[p] = true
		if _, found, err := s.readFile(ctx, owner, repo, p); err != nil || !found {
			continue
		}
		entries = append(entries, ConflictEntry{
			Severity:    SeverityInfo,
			Type:        TypeUnmanagedFile,
			Repo:        target,
			FilePath:    p,
			Description: fmt.Sprintf("file %q exists but is not tracked in the deployment state for this repo", p),
		})
	}
	return entries
}
