package postman

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/environments"
)

// EnvironmentExtension is the extension Postman gives its environment
// exports, which the Postman importer recognises.
const EnvironmentExtension = ".postman_environment.json"

// pmEnvironment is a Postman environment export.
type pmEnvironment struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Values     []pmEnvVariable `json:"values"`
	Scope      string          `json:"_postman_variable_scope"`
	ExportedAt string          `json:"_postman_exported_at"`
	ExportedBy string          `json:"_postman_exported_using"`
}

type pmEnvVariable struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
}

// ExportEnvironment writes env as a Postman environment export (EXP-05),
// which Postman imports and PostSonde's Postman importer reads back
// (IMP-03). A secret variable is written with "type": "secret" and an
// empty value, whatever env holds; env has no local values (it comes from
// environments.Store.Shared). id is derived from env's ID (a UUID in the
// version 5 layout), so exporting the same environment again gives the
// same ID; version is the app's version, now the export time.
func ExportEnvironment(env environments.Environment, version string, now time.Time) ([]byte, error) {
	if version == "" {
		version = "dev"
	}
	name := strings.TrimSpace(env.Name)
	if name == "" {
		name = "Environment"
	}
	out := pmEnvironment{
		ID:         postmanID(env.ID),
		Name:       name,
		Values:     make([]pmEnvVariable, 0, len(env.Variables)),
		Scope:      "environment",
		ExportedAt: now.UTC().Format("2006-01-02T15:04:05.000Z"),
		ExportedBy: "PostSonde/" + version,
	}
	for _, v := range env.Variables {
		pv := pmEnvVariable{Key: v.Name, Value: v.Value, Type: "default", Enabled: !v.Disabled, Description: v.Description}
		if v.Secret {
			pv.Value, pv.Type = "", "secret"
		}
		out.Values = append(out.Values, pv)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
