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
// affect the hash, and neither does a malformed marker of another name. Sections are taken in name order with LF
// line endings, because the marker strategy replaces sections by name and writes them in the file's own line
// ending. ok is false when names is empty or one of the named sections is missing or broken: not exactly one BEGIN
// followed by one END, or another marker between them.
func SectionsHash(content []byte, names []string) (sum string, ok bool) {
	if len(names) == 0 {
		return "", false
	}
	markers := collectMarkers(content)
	sorted := slices.Compact(slices.Sorted(slices.Values(names)))
	parts := make([][]byte, 0, len(sorted))
	for _, name := range sorted {
		sec, found := namedSection(content, markers, name)
		if !found {
			return "", false
		}
		parts = append(parts, normalizeEOL(sec, []byte("\n")))
	}
	return hash.HashBytes(bytes.Join(parts, []byte("\n"))), true
}

// namedSection returns the section called name, BEGIN through END marker, from markers (collectMarkers of content,
// in source order). found is false unless name has exactly one BEGIN followed by one END with no marker between.
func namedSection(content []byte, markers []markerMatch, name string) (section []byte, found bool) {
	var idx []int
	for i, m := range markers {
		if m.name == name {
			idx = append(idx, i)
		}
	}
	if len(idx) != 2 || idx[1] != idx[0]+1 {
		return nil, false
	}
	begin, end := markers[idx[0]], markers[idx[1]]
	if begin.kind != markerBegin || end.kind != markerEnd {
		return nil, false
	}
	return content[begin.start:end.end], true
}
