package template

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/SteerSpec/strspc-sync/internal/hash"
)

// SectionNames returns the names of the STEERSPEC:BEGIN/END sections a marker template defines, sorted and without
// duplicates: the sections the template manages in a target file.
func SectionNames(templateContent []byte) ([]string, error) {
	sections, err := extractSections(templateContent)
	if err != nil {
		return nil, fmt.Errorf("parsing template sections: %w", err)
	}
	names := make([]string, 0, len(sections))
	for _, sec := range sections {
		names = append(names, sec.name)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// SectionsHash returns the Blake3 hash of the named STEERSPEC:BEGIN/END sections in content: the part of a file one
// marker template manages. Everything else, including sections other templates or the repository own, does not
// affect the hash. Sections are taken in name order with LF line endings, because the marker strategy replaces
// sections by name and writes them in the file's own line ending. ok is false when names is empty or content lacks
// one of the named sections.
func SectionsHash(content []byte, names []string) (sum string, ok bool, err error) {
	sections, err := extractSections(content)
	if err != nil {
		return "", false, fmt.Errorf("parsing managed sections: %w", err)
	}
	if len(names) == 0 {
		return "", false, nil
	}
	byName := make(map[string][]byte, len(sections))
	for _, sec := range sections {
		if _, seen := byName[sec.name]; !seen {
			byName[sec.name] = sec.content
		}
	}
	sorted := slices.Sorted(slices.Values(names))
	parts := make([][]byte, 0, len(sorted))
	for _, name := range slices.Compact(sorted) {
		sec, found := byName[name]
		if !found {
			return "", false, nil
		}
		parts = append(parts, normalizeEOL(sec, []byte("\n")))
	}
	return hash.HashBytes(bytes.Join(parts, []byte("\n"))), true, nil
}
