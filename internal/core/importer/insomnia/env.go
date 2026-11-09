package insomnia

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// envSource is an Insomnia environment of either version.
type envSource struct {
	n       *yaml.Node
	name    string
	loc     importer.Location
	private bool
}

// environmentsOf maps a base environment and its sub-environments. In
// Insomnia the base environment's variables apply under every
// sub-environment, so each sub-environment becomes an environment with
// the base's variables first and its own over them; a base without
// sub-environments becomes an environment of its own. prefix goes before
// every name.
func environmentsOf(base *envSource, subs []*envSource, prefix string) []importer.Environment {
	baseVars, baseDiags := envVars(base)
	if len(subs) == 0 {
		name := strings.TrimSpace(base.name)
		if name == "" {
			name = "Base Environment"
		}
		return []importer.Environment{{Name: prefix + name, Variables: baseVars, Source: base.loc, Diagnostics: baseDiags}}
	}
	var out []importer.Environment
	for _, s := range subs {
		vars, diags := envVars(s)
		env := importer.Environment{Name: strings.TrimSpace(s.name), Source: s.loc}
		if env.Name == "" {
			env.Name = "Environment"
		}
		env.Name = prefix + env.Name
		if len(baseVars) > 0 {
			env.Variables = merge(baseVars, vars)
			env.Diagnostics = append(env.Diagnostics, importer.Diagnostic{
				Code: CodeBaseEnvironment, Severity: importer.SeverityInfo, Source: s.loc,
				Message: "The base environment's variables were added to this environment; its own values win.",
			})
			env.Diagnostics = append(env.Diagnostics, baseDiags...)
		} else {
			env.Variables = vars
		}
		env.Diagnostics = append(env.Diagnostics, diags...)
		out = append(out, env)
	}
	return out
}

// merge returns base with over's variables replacing those of the same
// name, and over's others after them.
func merge(base, over []environments.Variable) []environments.Variable {
	out := append([]environments.Variable(nil), base...)
	for _, v := range over {
		replaced := false
		for i := range out {
			if out[i].Name == v.Name {
				out[i], replaced = v, true
				break
			}
		}
		if !replaced {
			out = append(out, v)
		}
	}
	return out
}

// envVars reads an environment's variables: its data object, or its
// key-value list (Insomnia's table editor, with secrets from the vault).
func envVars(e *envSource) ([]environments.Variable, []importer.Diagnostic) {
	t := &templates{}
	var vars []environments.Variable
	var diags []importer.Diagnostic
	kv := items(get(e.n, "kvPairData"))
	if sget(e.n, "environmentType") == "kv" || (len(kv) > 0 && len(flatten(get(e.n, "data"))) == 0) {
		secrets := false
		for _, it := range kv {
			name := sget(it, "name")
			if name == "" {
				continue
			}
			v := environments.Variable{Name: name, Value: t.conv(sget(it, "value"), "a variable"), Disabled: isFalse(get(it, "enabled"))}
			if sget(it, "type") == "secret" {
				v.Secret, v.Value, secrets = true, "", true
			}
			vars = append(vars, v)
		}
		if secrets {
			diags = append(diags, importer.Diagnostic{
				Code: CodeSecretValue, Severity: importer.SeverityWarning, Source: e.loc,
				Message: "Vault secrets are encrypted in the export: their variables were imported without values. Type the values in the environment.",
			})
		}
	} else {
		data := get(e.n, "data")
		for _, v := range flatten(data) {
			vars = append(vars, environments.Variable{Name: v.name, Value: t.conv(v.value, "a variable")})
		}
		if nestedIn(data) {
			diags = append(diags, nestedDiag(e.loc))
		}
	}
	if e.private && len(vars) > 0 {
		for i := range vars {
			vars[i].Secret = true
		}
		diags = append(diags, importer.Diagnostic{
			Code: CodePrivateEnvironment, Severity: importer.SeverityInfo, Source: e.loc,
			Message: "The environment was private in Insomnia, so its variables were imported as secrets, kept on this machine only.",
		})
	}
	return vars, append(diags, t.diagnostics(e.loc)...)
}
