package template

import (
	"github.com/cbroglie/mustache"
)

func renderMustache(templateContent []byte, variables map[string]string) ([]byte, error) {
	ctx := make(map[string]interface{}, len(variables))
	for k, v := range variables {
		ctx[k] = v
	}

	tmpl, err := mustache.ParseString(string(templateContent))
	if err != nil {
		return nil, err
	}

	out, err := tmpl.Render(ctx)
	if err != nil {
		return nil, err
	}

	return []byte(out), nil
}
