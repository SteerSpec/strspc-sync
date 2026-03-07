package conflict

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/SteerSpec/strspc-sync/internal/hash"
)

var markerPattern = regexp.MustCompile(`(?m)^<!--\s*STEERSPEC:(\S+)\s*-->`)

func (s *Scanner) runTier2(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	// 1. Duplicate skill detection
	dupEntries, err := s.checkDuplicateSkills(ctx, targets)
	if err != nil {
		return nil, fmt.Errorf("duplicate skill check: %w", err)
	}
	entries = append(entries, dupEntries...)

	// 2. Section overlap detection
	overlapEntries, err := s.checkSectionOverlap(ctx, targets)
	if err != nil {
		return nil, fmt.Errorf("section overlap check: %w", err)
	}
	entries = append(entries, overlapEntries...)

	return entries, nil
}

type skillInfo struct {
	repo string
	hash string
}

func (s *Scanner) checkDuplicateSkills(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	// Collect skill files across all targets grouped by filename
	skillsByName := make(map[string][]skillInfo)

	skillTemplates := s.skillTemplatePaths()
	for _, target := range targets {
		owner, repo, err := splitRepo(target)
		if err != nil {
			return nil, err
		}

		for _, path := range skillTemplates {
			content, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, path, "")
			if err != nil {
				continue
			}
			// Extract just the filename from the path
			parts := strings.Split(path, "/")
			filename := parts[len(parts)-1]

			skillsByName[filename] = append(skillsByName[filename], skillInfo{
				repo: target,
				hash: hash.HashBytes(content),
			})
		}
	}

	// Flag skills with the same name but different content
	for filename, skills := range skillsByName {
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
				Repo:                strings.Join(repos, ", "),
				FilePath:            filename,
				Description:         fmt.Sprintf("skill %q exists in %d repos with different content", filename, len(skills)),
				SuggestedResolution: "Consolidate the skill into a single template or ensure intentional divergence.",
			})
		}
	}

	return entries, nil
}

func (s *Scanner) checkSectionOverlap(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	var entries []ConflictEntry

	// For each target, find all managed files with STEERSPEC markers and check for overlapping sections
	for _, target := range targets {
		owner, repo, err := splitRepo(target)
		if err != nil {
			return nil, err
		}

		sectionSources := make(map[string]string) // section ID -> first file that owns it

		for _, tmpl := range s.cfg.Templates {
			if tmpl.Strategy != "marker" {
				continue
			}

			content, _, err := s.ghClient.Repos.GetFileContent(ctx, owner, repo, tmpl.Destination, "")
			if err != nil {
				continue
			}

			matches := markerPattern.FindAllStringSubmatch(string(content), -1)
			for _, match := range matches {
				sectionID := match[1]
				if existing, ok := sectionSources[sectionID]; ok && existing != tmpl.Destination {
					entries = append(entries, ConflictEntry{
						Severity:            SeverityWarning,
						Type:                TypeManualOverride,
						Repo:                target,
						FilePath:            tmpl.Destination,
						Description:         fmt.Sprintf("STEERSPEC section %q appears in both %s and %s", sectionID, existing, tmpl.Destination),
						SuggestedResolution: "Remove the duplicate section from one of the files.",
					})
				} else {
					sectionSources[sectionID] = tmpl.Destination
				}
			}
		}
	}

	return entries, nil
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
