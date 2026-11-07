package template

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/SteerSpec/strspc-sync/internal/hash"
)

// SectionsHash returns the Blake3 hash of the STEERSPEC:BEGIN/END sections in content: the part of a file the
// marker strategy manages. Everything outside the sections belongs to the target repository, so it does not
// affect the hash. Sections are taken in name order with LF line endings, because the marker strategy replaces
// sections by name and writes them in the file's own line ending. ok is false when content holds no section.
func SectionsHash(content []byte) (sum string, ok bool, err error) {
	sections, err := extractSections(content)
	if err != nil {
		return "", false, fmt.Errorf("parsing managed sections: %w", err)
	}
	if len(sections) == 0 {
		return "", false, nil
	}
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].name < sections[j].name })
	parts := make([][]byte, len(sections))
	for i, sec := range sections {
		parts[i] = normalizeEOL(sec.content, []byte("\n"))
	}
	return hash.HashBytes(bytes.Join(parts, []byte("\n"))), true, nil
}
