package template

import "strings"

func renderFullReplace(templateContent []byte, variables map[string]string) ([]byte, error) {
	if len(variables) == 0 {
		return templateContent, nil
	}

	result := string(templateContent)
	for k, v := range variables {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return []byte(result), nil
}
