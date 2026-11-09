// Package postsondeenv imports PostSonde environment files (EXP-05): the
// files of a project's environments/ folder, and what "Export environment"
// writes (docs/import.md#postsonde-environments). An environment becomes a
// new environment of the open project, with a free name and a new ID.
package postsondeenv

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeNewer: the file is from a newer PostSonde; the members this version
// knows were imported (info).
const CodeNewer = "postsondeenv.newer"

// maxDetect is the largest file Detect decodes: environment files are
// small, and a large file of another format is not decoded twice.
const maxDetect = 4 << 20

// Importer reads PostSonde environment files.
type Importer struct{}

var format = importer.Format{ID: "postsonde-environment", Name: "PostSonde environment", Extensions: []string{".json"}}

// Format describes the format.
func (Importer) Format() importer.Format { return format }

// head is what Detect looks at.
type head struct {
	Kind          string          `json:"kind"`
	SchemaVersion json.RawMessage `json:"schemaVersion"`
}

// Detect is Sure for a JSON object with "kind": "environment" and a
// "schemaVersion", as every PostSonde environment file has.
func (Importer) Detect(src importer.Source) int {
	data := bytes.TrimSpace(src.Data)
	if len(data) == 0 || data[0] != '{' || len(data) > maxDetect || !bytes.Contains(data, []byte(`"environment"`)) {
		return importer.No
	}
	var h head
	if json.Unmarshal(data, &h) != nil || h.Kind != string(projectfile.KindEnvironment) || len(h.SchemaVersion) == 0 {
		return importer.No
	}
	return importer.Sure
}

// Parse reads the environment: its name and variables, secret ones with
// the value the file has (none, unless typed by hand; the import keeps it
// on this computer only). Its ID and colour are not imported: the import
// makes a new environment.
func (Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	f, err := projectfile.DecodeEnvironment(src.Data)
	if err != nil {
		return nil, importer.InvalidFile(format, "The file is not a PostSonde environment file.", importer.Location{})
	}
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = strings.TrimSpace(src.BaseName())
	}
	if name == "" {
		name = "Environment"
	}
	env := importer.Environment{Name: name}
	for _, v := range f.Variables {
		if strings.TrimSpace(v.Name) == "" {
			continue
		}
		env.Variables = append(env.Variables, environments.Variable{
			Name: v.Name, Value: v.Value, Description: v.Description, Secret: v.Secret, Disabled: v.Disabled,
		})
	}
	if f.Newer() {
		env.Diagnostics = append(env.Diagnostics, importer.Diagnostic{
			Code: CodeNewer, Severity: importer.SeverityInfo,
			Message: "The file is from a newer PostSonde: what this version knows of it was imported.",
		})
	}
	return &importer.Model{Name: name, Environments: []importer.Environment{env}}, nil
}
