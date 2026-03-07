package conflict

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/state"
)

var crossRefPattern = regexp.MustCompile(`(?:\.claude/)?((?:agents|skills)/[^\s)]+\.md)`)

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
		unmanagedEntries := s.checkUnmanagedFiles(deployState, target)
		entries = append(entries, unmanagedEntries...)
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

		content, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, tmplCfg.Destination, "")
		if err != nil {
			continue // file may not exist yet
		}

		actualHash := hash.HashBytes(content)
		if actualHash != ts.Hash {
			entries = append(entries, ConflictEntry{
				Severity:            SeverityCritical,
				Type:                TypeVersionDrift,
				Repo:                target,
				FilePath:            tmplCfg.Destination,
				Description:         fmt.Sprintf("template %q has drifted: expected hash %s, got %s", templateID, ts.Hash, actualHash),
				SuggestedResolution: "Re-run sync to update the file, or update the deployment state if the change is intentional.",
			})
		}
	}

	return entries, nil
}

func (s *Scanner) checkCrossReferences(ctx context.Context, owner, repo, target string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	content, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, "CLAUDE.md", "")
	if err != nil {
		return nil, nil // no CLAUDE.md, nothing to check
	}

	refs := crossRefPattern.FindAllStringSubmatch(string(content), -1)
	for _, match := range refs {
		refPath := ".claude/" + match[1]
		if strings.HasPrefix(match[1], ".claude/") {
			refPath = match[1]
		}

		_, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, refPath, "")
		if err != nil {
			entries = append(entries, ConflictEntry{
				Severity:            SeverityCritical,
				Type:                TypeCrossReferenceBroken,
				Repo:                target,
				FilePath:            refPath,
				Description:         fmt.Sprintf("CLAUDE.md references %q but the file does not exist", refPath),
				SuggestedResolution: "Create the missing file or remove the reference from CLAUDE.md.",
			})
		}
	}

	return entries, nil
}

func (s *Scanner) checkUnmanagedFiles(deployState *state.DeploymentState, target string) []ConflictEntry {
	var entries []ConflictEntry

	// Build set of managed destinations for this target
	managed := make(map[string]bool)
	repoTemplates, ok := deployState.Repositories[target]
	if ok {
		for templateID := range repoTemplates {
			tmplCfg := s.findTemplate(templateID)
			if tmplCfg != nil {
				managed[tmplCfg.Destination] = true
			}
		}
	}

	// Check each configured template destination; if the file is not tracked, flag it.
	// Since we can't list directory contents via the GitHub API without search,
	// we check known paths that should be managed but aren't in the deployment state.
	knownPaths := []string{"CLAUDE.md"}
	for _, tmpl := range s.cfg.Templates {
		knownPaths = append(knownPaths, tmpl.Destination)
	}

	for _, path := range knownPaths {
		if managed[path] {
			continue
		}
		// If this target has no entry in deployment state at all, flag any template destination
		if !ok {
			entries = append(entries, ConflictEntry{
				Severity:    SeverityInfo,
				Type:        TypeUnmanagedFile,
				Repo:        target,
				FilePath:    path,
				Description: fmt.Sprintf("file %q exists in template config but is not tracked in deployment state for this repo", path),
			})
		}
	}

	return entries
}
