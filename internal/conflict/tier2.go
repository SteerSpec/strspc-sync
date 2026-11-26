package conflict

import (
	"context"
	"fmt"
	"sort"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

func (s *Scanner) runTier2(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	// 1. Duplicate skill detection
	dupEntries, err := s.checkDuplicateSkills(ctx, targets)
	if err != nil {
		return nil, fmt.Errorf("duplicate skill check: %w", err)
	}
	entries = append(entries, dupEntries...)

	// 2. Section overlap detection
	entries = append(entries, s.checkSectionOverlap(ctx, targets)...)

	return entries, nil
}

type skillInfo struct {
	repo string
	hash string
}

func (s *Scanner) checkDuplicateSkills(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	// Collect skill files across all targets, grouped by their path in the repository: a skill is identified by its
	// destination, so .claude/skills/a/SKILL.md and .claude/skills/b/SKILL.md are different skills even though they
	// share a filename (CNFLDTS-009).
	skillsByPath := make(map[string][]skillInfo)

	skillTemplates := s.skillTemplatePaths()
	for _, target := range targets {
		owner, repo, err := splitRepo(target)
		if err != nil {
			return nil, err
		}

		for _, path := range skillTemplates {
			content, found, err := s.readFile(ctx, owner, repo, path)
			if err != nil || !found {
				continue
			}
			skillsByPath[path] = append(skillsByPath[path], skillInfo{
				repo: target,
				hash: hash.HashBytes(content),
			})
		}
	}

	// Flag a skill whose content differs between repositories, in path order so the report and the issue updates
	// are the same on every run.
	paths := make([]string, 0, len(skillsByPath))
	for path := range skillsByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		skills := skillsByPath[path]
		if len(skills) < 2 {
			continue
		}
		hashGroups := make(map[string][]string) // hash -> repos
		for _, si := range skills {
			hashGroups[si.hash] = append(hashGroups[si.hash], si.repo)
		}
		if len(hashGroups) > 1 {
			repos := make([]string, 0, len(skills))
			for _, si := range skills {
				repos = append(repos, si.repo)
			}
			entries = append(entries, ConflictEntry{
				Severity:            SeverityWarning,
				Type:                TypeDuplicateSkill,
				Repo:                s.centralOwner + "/" + s.centralRepo,
				Repos:               repos,
				FilePath:            path,
				Description:         fmt.Sprintf("skill %q exists in %d repos with different content", path, len(skills)),
				SuggestedResolution: "Consolidate the skill into a single template or ensure intentional divergence.",
			})
		}
	}

	return entries, nil
}

// checkSectionOverlap reports a STEERSPEC:BEGIN/END section that appears in more than one marker-managed file of a
// target (CNFLDTS-009). Sync writes each section only to its own template's destination, so a second copy was added by
// hand or by a misconfigured template, and the two may diverge. Several marker templates may share one destination;
// only different files count. A file whose markers cannot be parsed is skipped: tier 1 reports it.
func (s *Scanner) checkSectionOverlap(ctx context.Context, targets []string) []ConflictEntry {
	var entries []ConflictEntry
	for _, target := range targets {
		owner, repo, err := splitRepo(target)
		if err != nil {
			continue
		}
		owners := make(map[string]string) // section name -> first file that holds it
		read := make(map[string]bool)     // destinations already scanned in this target
		for _, tmpl := range s.cfg.Templates {
			if tmpl.Strategy != "marker" || read[tmpl.Destination] {
				continue
			}
			read[tmpl.Destination] = true
			content, found, err := s.readFile(ctx, owner, repo, tmpl.Destination)
			if err != nil || !found {
				continue
			}
			names, err := template.SectionNames(content)
			if err != nil {
				continue
			}
			for _, name := range names {
				first, ok := owners[name]
				if !ok {
					owners[name] = tmpl.Destination
					continue
				}
				entries = append(entries, ConflictEntry{
					Severity:    SeverityWarning,
					Type:        TypeSectionOverlap,
					Repo:        target,
					FilePath:    tmpl.Destination,
					Description: fmt.Sprintf("STEERSPEC section %q appears in both %s and %s", name, first, tmpl.Destination),
					SuggestedResolution: "Keep the section only in the file whose template manages it, and remove it " +
						"from the other file or its template.",
				})
			}
		}
	}
	return entries
}

func (s *Scanner) skillTemplatePaths() []string {
	var paths []string
	for _, tmpl := range s.cfg.Templates {
		if tmpl.Type == "skill" {
			paths = append(paths, tmpl.Destination)
		}
	}
	return paths
}
