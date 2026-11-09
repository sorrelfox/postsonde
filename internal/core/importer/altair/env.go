package altair

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// parseEnvironment reads an environment export: its title and variables,
// an object (variables) or the JSON text of one (variablesJson, Altair's
// own state). Each member becomes a variable: text as it is, numbers and
// booleans as their text, null as an empty value, objects and lists as
// their JSON, so {{name.field}} still reaches into them (dot notation,
// docs/templates.md).
func parseEnvironment(root *yaml.Node) *importer.Model {
	name := strings.TrimSpace(sget(root, "title"))
	if name == "" {
		name = strings.TrimSpace(sget(root, "name"))
	}
	if name == "" {
		name = "Environment"
	}
	loc := importer.Location{Line: root.Line, Column: root.Column}
	env := importer.Environment{Name: name, Source: loc}
	vars := get(root, "variables")
	if vars == nil {
		if text := strings.TrimSpace(sget(root, "variablesJson")); text != "" {
			var err error
			vars, err = parseJSON([]byte(text))
			if err != nil || !isMap(vars) {
				vars = nil
				n := get(root, "variablesJson")
				env.Diagnostics = append(env.Diagnostics, importer.Diagnostic{
					Code: CodeUnsupported, Severity: importer.SeverityWarning,
					Source:  importer.Location{Line: n.Line, Column: n.Column, Path: "variablesJson"},
					Message: "The environment's variables are not a JSON object, so none were imported.",
				})
			}
		}
	}
	if isMap(vars) {
		for _, p := range vars.Resolve().Pairs {
			key := strings.TrimSpace(p.Key.Value)
			if key == "" {
				continue
			}
			env.Variables = append(env.Variables, environments.Variable{Name: key, Value: valueText(p.Value)})
		}
	}
	return &importer.Model{Name: name, Environments: []importer.Environment{env}}
}

// valueText is a variable's value as text.
func valueText(n *yaml.Node) string {
	n = n.Resolve()
	if n == nil {
		return ""
	}
	if n.Kind == yaml.ScalarNode {
		return str(n)
	}
	data, err := n.JSON()
	if err != nil {
		return ""
	}
	return string(data)
}
