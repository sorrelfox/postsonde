package postman

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// parseEnvironment reads an environment or globals export: its values
// become one environment. A "secret" value is a secret variable, kept on
// this machine only; a value turned off keeps its variable disabled.
// Globals become an environment too (the import makes environments, not
// global variables), named after the file's name, "Globals" by default.
func parseEnvironment(root *yaml.Node) *importer.Model {
	t := &texts{}
	globals := sget(root, "_postman_variable_scope") == "globals"
	name := strings.TrimSpace(sget(root, "name"))
	if name == "" {
		name = "Environment"
		if globals {
			name = "Globals"
		}
	}
	loc := importer.Location{Line: root.Line, Column: root.Column}
	env := importer.Environment{Name: name, Source: loc}
	for _, v := range items(get(root, "values")) {
		key := sget(v, "key")
		if key == "" {
			continue
		}
		env.Variables = append(env.Variables, environments.Variable{
			Name:        key,
			Value:       t.conv(str(get(v, "value")), "a variable"),
			Description: description(get(v, "description")),
			Secret:      sget(v, "type") == "secret",
			Disabled:    isFalse(get(v, "enabled")),
		})
	}
	if globals {
		env.Diagnostics = append(env.Diagnostics, importer.Diagnostic{
			Code: CodeGlobals, Severity: importer.SeverityInfo, Source: loc,
			Message: "Postman globals are imported as an environment. To make them global variables, copy them into the project's global variables.",
		})
	}
	env.Diagnostics = append(env.Diagnostics, t.diagnostics(loc)...)
	return &importer.Model{Name: name, Environments: []importer.Environment{env}}
}
