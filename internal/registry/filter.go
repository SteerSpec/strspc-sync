package registry

import (
	"fmt"
	"path"
	"strings"
)

// Filter is the parsed value of --target-filter or --template-filter: a comma-separated list of path.Match globs.
// A name matches when any glob matches it. The zero Filter, from an empty value, matches every name. All three
// commands parse their filters with ParseFilter, so a value means the same thing in sync, monitor and conflict
// (GH #171).
type Filter struct {
	patterns []string
}

// ParseFilter splits s on commas, trims each glob and drops empty ones. It fails on a malformed glob, which
// path.Match would otherwise report only by matching nothing.
func ParseFilter(s string) (Filter, error) {
	var f Filter
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return Filter{}, fmt.Errorf("invalid filter pattern %q: %w", p, err)
		}
		f.patterns = append(f.patterns, p)
	}
	return f, nil
}

// Match reports whether name matches the filter. An empty filter matches everything.
func (f Filter) Match(name string) bool {
	if len(f.patterns) == 0 {
		return true
	}
	for _, p := range f.patterns {
		// ParseFilter validated every pattern, so Match cannot fail here.
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}
