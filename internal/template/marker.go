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
	content []byte // content between BEGIN and END markers, inclusive of markers
	inner   []byte // content between BEGIN and END markers, exclusive of markers
}

func extractSections(data []byte) ([]section, error) {
	var sections []section
	lines := bytes.Split(data, []byte("\n"))

	var current *section
	var innerLines [][]byte
	var beginLine []byte

	for _, line := range lines {
		if m := markerBeginRe.FindSubmatch(line); m != nil {
			if current != nil {
				return nil, fmt.Errorf("nested STEERSPEC:BEGIN marker %q inside %q", string(m[1]), current.name)
			}
			current = &section{name: string(m[1])}
			beginLine = line
			innerLines = nil
			continue
		}
		if m := markerEndRe.FindSubmatch(line); m != nil {
			name := string(m[1])
			if current == nil {
				return nil, fmt.Errorf("STEERSPEC:END marker %q without matching BEGIN", name)
			}
			if current.name != name {
				return nil, fmt.Errorf("STEERSPEC:END marker %q does not match BEGIN %q", name, current.name)
			}
			current.inner = bytes.Join(innerLines, []byte("\n"))
			// Build full content including markers
			var full bytes.Buffer
			full.Write(beginLine)
			full.WriteByte('\n')
			if len(innerLines) > 0 {
				full.Write(current.inner)
				full.WriteByte('\n')
			}
			full.Write(line)
			current.content = full.Bytes()
			sections = append(sections, *current)
			current = nil
			continue
		}
		if current != nil {
			innerLines = append(innerLines, line)
		}
	}

	if current != nil {
		return nil, fmt.Errorf("unclosed STEERSPEC:BEGIN marker %q", current.name)
	}

	return sections, nil
}

func renderMarker(templateContent []byte, existingContent []byte) ([]byte, error) {
	if len(existingContent) == 0 {
		return templateContent, nil
	}

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
			// Section not in existing content; append it
			result = append(result, '\n')
			result = append(result, ts.content...)
		}
	}

	return result, nil
}
