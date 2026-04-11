package template

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
)

var (
	markerBeginRe = regexp.MustCompile(`<!--\s*STEERSPEC:BEGIN:(\S+)\s*-->`)
	markerEndRe   = regexp.MustCompile(`<!--\s*STEERSPEC:END:(\S+)\s*-->`)
)

type section struct {
	name    string
	content []byte // content from the BEGIN marker through the END marker, inclusive of both markers
}

// detectEOL returns the dominant line ending of data — CRLF if \r\n occurrences
// outnumber bare \n occurrences, LF otherwise. Returns LF for empty input.
func detectEOL(data []byte) []byte {
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
	if crlf > lf {
		return []byte("\r\n")
	}
	return []byte("\n")
}

// normalizeEOL converts every line ending in data to target. It first
// collapses CRLF → LF, then swaps LF → target. This lets renderMarker
// feed a LF-authored template into CRLF-flavored existing content (and
// vice versa) without re-implementing split/join on mixed endings.
func normalizeEOL(data, target []byte) []byte {
	lf := []byte("\n")
	crlf := []byte("\r\n")
	normalized := bytes.ReplaceAll(data, crlf, lf)
	if bytes.Equal(target, lf) {
		return normalized
	}
	return bytes.ReplaceAll(normalized, lf, target)
}

// markerKind tags a BEGIN or END marker match during extractSections'
// ordered scan.
type markerKind int

const (
	markerBegin markerKind = iota
	markerEnd
)

type markerMatch struct {
	kind  markerKind
	name  string
	start int // byte offset of the full marker (e.g. '<' in "<!--")
	end   int // byte offset one past the closing '>'
}

// extractSections finds all STEERSPEC:BEGIN/END marker pairs in data by
// byte offsets. Returns an error on nested, unmatched, or mismatched markers,
// always pointing at the specific offending marker by scanning them in order.
//
// Matching by byte offset rather than line-splitting makes this robust to
// mixed line endings (e.g. a previously LF-rendered section living inside a
// now-CRLF file during the CRLF-preservation rollout).
func extractSections(data []byte) ([]section, error) {
	matches := collectMarkers(data)

	var sections []section
	// Stack depth = 1 because nesting is disallowed; the extra state just
	// lets us report the nesting case with the correct outer/inner names.
	var open *markerMatch
	for i := range matches {
		m := &matches[i]
		switch m.kind {
		case markerBegin:
			if open != nil {
				return nil, fmt.Errorf("nested STEERSPEC:BEGIN marker %q inside %q", m.name, open.name)
			}
			open = m
		case markerEnd:
			if open == nil {
				return nil, fmt.Errorf("STEERSPEC:END marker %q without matching BEGIN", m.name)
			}
			if open.name != m.name {
				return nil, fmt.Errorf("STEERSPEC:END marker %q does not match BEGIN %q", m.name, open.name)
			}
			content := append([]byte(nil), data[open.start:m.end]...)
			sections = append(sections, section{name: open.name, content: content})
			open = nil
		}
	}
	if open != nil {
		return nil, fmt.Errorf("unclosed STEERSPEC:BEGIN marker %q", open.name)
	}
	return sections, nil
}

// collectMarkers returns every BEGIN and END marker occurrence in data,
// sorted by their byte offset so the caller can walk them in source order.
func collectMarkers(data []byte) []markerMatch {
	begins := markerBeginRe.FindAllSubmatchIndex(data, -1)
	ends := markerEndRe.FindAllSubmatchIndex(data, -1)

	matches := make([]markerMatch, 0, len(begins)+len(ends))
	for _, b := range begins {
		matches = append(matches, markerMatch{
			kind:  markerBegin,
			name:  string(data[b[2]:b[3]]),
			start: b[0],
			end:   b[1],
		})
	}
	for _, e := range ends {
		matches = append(matches, markerMatch{
			kind:  markerEnd,
			name:  string(data[e[2]:e[3]]),
			start: e[0],
			end:   e[1],
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].start < matches[j].start
	})
	return matches
}

func renderMarker(templateContent, existingContent []byte) ([]byte, error) {
	if len(existingContent) == 0 {
		return templateContent, nil
	}

	// Preserve the existing file's line ending style so the rendered
	// result doesn't introduce spurious CRLF/LF churn on Windows-centric
	// repos. Template content (usually LF-only, as stored in the central
	// repo) is normalized to the target EOL before section extraction so
	// the replacement we splice into existing content uses the dominant EOL.
	eol := detectEOL(existingContent)
	templateContent = normalizeEOL(templateContent, eol)

	templateSections, err := extractSections(templateContent)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	if len(templateSections) == 0 {
		return templateContent, nil
	}

	result := make([]byte, len(existingContent))
	copy(result, existingContent)

	for _, ts := range templateSections {
		existingSections, err := extractSections(result)
		if err != nil {
			return nil, fmt.Errorf("parsing existing content: %w", err)
		}

		found := false
		for _, es := range existingSections {
			if es.name == ts.name {
				result = bytes.Replace(result, es.content, ts.content, 1)
				found = true
				break
			}
		}
		if !found {
			// Section not in existing content; append it with the detected EOL.
			result = append(result, eol...)
			result = append(result, ts.content...)
		}
	}

	return result, nil
}
