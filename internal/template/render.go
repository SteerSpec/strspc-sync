package template

import "fmt"

// Strategy defines how a template is rendered.
type Strategy string

const (
	StrategyMustache    Strategy = "mustache"
	StrategyMarker      Strategy = "marker"
	StrategyFullReplace Strategy = "full-replace"
)

// Render dispatches to the appropriate renderer based on strategy.
func Render(strategy Strategy, templateContent, existingContent []byte, variables map[string]string) ([]byte, error) {
	switch strategy {
	case StrategyMustache:
		return renderMustache(templateContent, variables)
	case StrategyMarker:
		return renderMarker(templateContent, existingContent)
	case StrategyFullReplace:
		return renderFullReplace(templateContent, variables)
	default:
		return nil, fmt.Errorf("unknown strategy: %q", strategy)
	}
}
