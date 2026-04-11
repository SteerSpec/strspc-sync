package template

import (
	"bytes"
	"fmt"
	"regexp"
)

var (
	markerBeginRe = regexp.MustCompile(`<!--\s*STEERSPEC:BEGIN:(\S+)\s*-->`)
	markerEndRe   = regexp.MustCompile(`<!--\s*STEERSPEC:END:(\S+)\s*-->`)
)

type section struct {
	name    string
	content []byte // content between BEGIN and END markers, inclusive of markers + surrounding newline
	inner   []byte // content between BEGIN and END markers, exclusive of markers and their trailing newline
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

// extractSections finds all STEERSPEC:BEGIN/END marker pairs in data by
// byte offsets. Returns an error on nested, unmatched, or mismatched markers.
//
// We match by byte offset rather than line-splitting so mixed line endings
// (e.g. a previously LF-rendered section inside a now-CRLF file during the
// CRLF-preservation rollout) don't break marker detection.
func extractSections(data []byte) ([]section, error) {
	begins := markerBeginRe.FindAllSubmatchIndex(data, -1)
	ends := markerEndRe.FindAllSubmatchIndex(data, -1)

	if len(begins) != len(ends) {
		if len(begins) > len(ends) {
			name := string(data[begins[len(ends)][2]:begins[len(ends)][3]])
			return nil, fmt.Errorf("unclosed STEERSPEC:BEGIN marker %q", name)
		}
		name := string(data[ends[len(begins)][2]:ends[len(begins)][3]])
		return nil, fmt.Errorf("STEERSPEC:END marker %q without matching BEGIN", name)
	}

	sections := make([]section, 0, len(begins))
	for i, b := range begins {
		e := ends[i]
		if e[0] < b[1] {
			// END appears before BEGIN: unmatched END.
			name := string(data[e[2]:e[3]])
			return nil, fmt.Errorf("STEERSPEC:END marker %q without matching BEGIN", name)
		}
		if i+1 < len(begins) && begins[i+1][0] < e[0] {
			// A BEGIN appears before the current END: nested.
			inner := string(data[begins[i+1][2]:begins[i+1][3]])
			outer := string(data[b[2]:b[3]])
			return nil, fmt.Errorf("nested STEERSPEC:BEGIN marker %q inside %q", inner, outer)
		}

		beginName := string(data[b[2]:b[3]])
		endName := string(data[e[2]:e[3]])
		if beginName != endName {
			return nil, fmt.Errorf("STEERSPEC:END marker %q does not match BEGIN %q", endName, beginName)
		}

		innerStart, innerEnd := b[1], e[0]
		inner := trimSurroundingNewline(data[innerStart:innerEnd])
		content := data[b[0]:e[1]]

		sections = append(sections, section{
			name:    beginName,
			inner:   append([]byte(nil), inner...),
			content: append([]byte(nil), content...),
		})
	}
	return sections, nil
}

// trimSurroundingNewline strips one leading and one trailing newline (CRLF or
// LF) from inner marker content so that a round-trip extract/insert doesn't
// accumulate blank lines across renders.
func trimSurroundingNewline(b []byte) []byte {
	if bytes.HasPrefix(b, []byte("\r\n")) {
		b = b[2:]
	} else if bytes.HasPrefix(b, []byte("\n")) {
		b = b[1:]
	}
	if bytes.HasSuffix(b, []byte("\r\n")) {
		b = b[:len(b)-2]
	} else if bytes.HasSuffix(b, []byte("\n")) {
		b = b[:len(b)-1]
	}
	return b
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
