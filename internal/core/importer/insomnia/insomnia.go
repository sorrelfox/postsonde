// Package insomnia is the import framework's Insomnia format (IMP-04,
// docs/import.md#insomnia-exports): Insomnia v4 exports (JSON, or the
// same in YAML: `_type: export` with a flat list of resources linked by
// parentId) and v5 exports (YAML files of type
// collection.insomnia.rest/5.0, spec.… or environment.…, with the folder
// tree nested). Folders, requests (HTTP, GraphQL, gRPC, WebSocket), their
// auth and settings, folder variables and environments are mapped onto
// the import model; what PostSonde lacks is reported per item. Standard
// library only; YAML is read by internal/core/yaml.
package insomnia

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Diagnostic codes of the format (docs/import.md#insomnia-exports).
const (
	// CodeTemplateTag: a {% tag %} PostSonde has no equivalent of was left
	// as written (warning).
	CodeTemplateTag = "insomnia.template_tag"
	// CodeTemplateExpression: a {{ … }} that is more than a variable (a
	// filter, an operator) was left as written (warning).
	CodeTemplateExpression = "insomnia.template_expression"
	// CodeTemplateConverted: a uuid or now tag became a dynamic value
	// (info).
	CodeTemplateConverted = "insomnia.template_converted"
	// CodeAuthUnsupported: an auth type PostSonde lacks (AWS IAM, Hawk,
	// NTLM…); the request sends no auth (warning).
	CodeAuthUnsupported = "insomnia.auth_unsupported"
	// CodeAuthChanged: the auth was imported with a change (warning).
	CodeAuthChanged = "insomnia.auth_changed"
	// CodeScript: a pre-request or after-response script was kept in the
	// description, not run (warning).
	CodeScript = "insomnia.script"
	// CodeSettingSkipped: request settings PostSonde lacks (info).
	CodeSettingSkipped = "insomnia.setting_skipped"
	// CodeProtoSkipped: a gRPC request's .proto file, embedded in the
	// export, was not imported (warning).
	CodeProtoSkipped = "insomnia.proto_skipped"
	// CodeFilePath: a body refers to a file on the machine the export was
	// made on (info).
	CodeFilePath = "insomnia.file_path"
	// CodeGraphQLBody: a GraphQL body that is not the JSON Insomnia writes
	// was imported as the query text (warning).
	CodeGraphQLBody = "insomnia.graphql_body"
	// CodeUnsupported: an item of a kind PostSonde does not import (API
	// spec, test suite, mock server, socket.io request…), skipped.
	CodeUnsupported = "insomnia.unsupported"
	// CodeCookies: the cookie jar was not imported (info, file level).
	CodeCookies = "insomnia.cookies_skipped"
	// CodeBaseEnvironment: the base environment's variables were merged
	// into each sub-environment (info).
	CodeBaseEnvironment = "insomnia.base_environment"
	// CodeNestedValue: values nested in objects or lists became variables
	// with dotted names (info).
	CodeNestedValue = "insomnia.nested_value"
	// CodeSecretValue: a vault secret's value is encrypted in the export
	// and was not imported (warning).
	CodeSecretValue = "insomnia.secret_value"
	// CodePrivateEnvironment: a private environment's variables were
	// imported as secrets (info).
	CodePrivateEnvironment = "insomnia.private_environment"
	// CodeOrphan: items whose folder is not in the file were put at the
	// top level (warning, file level).
	CodeOrphan = "insomnia.orphan"
	// CodeVersion: an export format version this reader was not written
	// for; read as the nearest (info, file level).
	CodeVersion = "insomnia.version"
)

// bom is the UTF-8 byte order mark, ignored at the start of a file.
var bom = []byte("\xef\xbb\xbf")

// Importer reads Insomnia v4 and v5 exports.
type Importer struct{}

var _ importer.Importer = Importer{}

var format = importer.Format{
	ID:         "insomnia",
	Name:       "Insomnia export (v4, v5)",
	Extensions: []string{".json", ".yaml", ".yml"},
}

// Format describes the Insomnia format.
func (Importer) Format() importer.Format { return format }

var (
	reV4JSON    = regexp.MustCompile(`"_type"\s*:\s*"export"`)
	reV4Format  = regexp.MustCompile(`"__export_format"\s*:\s*\d`)
	reV4YAML    = regexp.MustCompile(`(?m)^_type:\s*["']?export["']?\s*$`)
	reV5Type    = regexp.MustCompile(`(?m)^type:\s*["']?(?:collection|spec|environment|mock)\.insomnia\.rest/5\.`)
	reOtherType = regexp.MustCompile(`(?m)^type:\s*["']?[a-z]+\.insomnia\.rest/`)
)

// Detect is Sure for a v4 export (`"_type": "export"` or
// `__export_format`) and a v5 file (`type: collection.insomnia.rest/5.0`,
// spec, environment or mock), Likely for another insomnia.rest type, No
// otherwise.
func (Importer) Detect(src importer.Source) int {
	head := bytes.TrimPrefix(src.Data, bom)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) > 0 && head[0] == '{' {
		if reV4JSON.Match(head) || reV4Format.Match(head) {
			return importer.Sure
		}
		return importer.No
	}
	switch {
	case reV5Type.Match(head), reV4YAML.Match(head):
		return importer.Sure
	case reOtherType.Match(head):
		return importer.Likely
	}
	return importer.No
}

// Parse reads a v4 or v5 export into the model. It fails only when the
// file is not JSON or YAML, or not an Insomnia export at all.
func (im Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	if !utf8.Valid(src.Data) {
		return nil, importer.InvalidFile(format, "The file is not UTF-8 text.", importer.Location{})
	}
	data := bytes.TrimPrefix(src.Data, bom)
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	var root *yaml.Node
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		n, err := parseJSON(data)
		if err != nil {
			je, _ := err.(*jsonError)
			at := importer.Location{}
			if je != nil {
				at.Line = je.line
			}
			return nil, importer.InvalidFile(format, "The file is not valid JSON: "+err.Error()+".", at)
		}
		root = n
	} else {
		n, err := yaml.Parse(data)
		if err != nil {
			at := importer.Location{}
			msg := err.Error()
			if ye, ok := err.(*yaml.Error); ok {
				at.Line, msg = ye.Line, ye.Message
			}
			return nil, importer.InvalidFile(format, "The file is not valid YAML: "+msg+".", at)
		}
		root = n.Resolve()
	}
	if !isMap(root) {
		return nil, importer.InvalidFile(format, "The file is not an Insomnia export.", importer.Location{Line: 1})
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	typ := sget(root, "type")
	switch {
	case sget(root, "_type") == "export" || (typ == "" && get(root, "resources") != nil):
		return parseV4(ctx, root)
	case strings.Contains(typ, ".insomnia.rest/"):
		return parseV5(ctx, root)
	}
	return nil, importer.InvalidFile(format, "The file is not an Insomnia export: it has neither `_type: export` nor an insomnia.rest type.", importer.Location{Line: root.Line})
}
