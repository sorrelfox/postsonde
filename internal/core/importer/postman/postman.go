// Package postman is the import framework's Postman format (IMP-02,
// IMP-03, docs/import.md#postman-files): Postman Collection v2.0 and v2.1
// JSON exports (folders, requests, auth, variables, settings, saved
// examples and scripts kept as notes), and Postman environment and globals
// exports. Postman's {{variable}} syntax is PostSonde's; its dynamic
// variables ({{$guid}}, {{$randomInt}}…) become PostSonde dynamic values
// where there is one and are reported otherwise. Standard library only.
package postman

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

// Diagnostic codes of the format (docs/import.md#postman-files).
const (
	// CodeScript: a pre-request or test script was kept in the
	// description, not run (warning).
	CodeScript = "postman.script"
	// CodeExamples: the body of a saved example (a response example,
	// HTTP-38) was longer than 64 KiB and was shortened (info).
	CodeExamples = "postman.examples"
	// CodeDynamicConverted: a Postman dynamic variable became a PostSonde
	// dynamic value of another spelling (info).
	CodeDynamicConverted = "postman.dynamic_converted"
	// CodeDynamicUnsupported: a Postman dynamic variable PostSonde has no
	// equivalent of was left as written (warning).
	CodeDynamicUnsupported = "postman.dynamic_unsupported"
	// CodeVault: a {{vault:…}} reference to Postman Vault was left as
	// written (warning).
	CodeVault = "postman.vault"
	// CodeAuthUnsupported: an auth type PostSonde lacks (NTLM, Akamai
	// EdgeGrid…); the request sends no auth (warning).
	CodeAuthUnsupported = "postman.auth_unsupported"
	// CodeAuthChanged: the auth was imported with a change (warning).
	CodeAuthChanged = "postman.auth_changed"
	// CodeSettingSkipped: request settings (protocolProfileBehavior,
	// proxy, certificate) PostSonde does not have (info).
	CodeSettingSkipped = "postman.setting_skipped"
	// CodeFilePath: a body sends a file by its path on the machine the
	// export was made on (info).
	CodeFilePath = "postman.file_path"
	// CodeGraphQLMethod: a GraphQL request with a method PostSonde's
	// GraphQL requests do not send (not GET or POST) is sent with POST
	// (warning).
	CodeGraphQLMethod = "postman.graphql_method"
	// CodeBodySkipped: a body turned off in Postman (info), or of a mode
	// PostSonde does not know (warning), was not imported.
	CodeBodySkipped = "postman.body_skipped"
	// CodeVariablesSkipped: variables on a request, which PostSonde
	// requests do not have, were not imported (warning).
	CodeVariablesSkipped = "postman.variables_skipped"
	// CodeUnsupported: an item that is not a request or folder, skipped
	// (warning).
	CodeUnsupported = "postman.unsupported"
	// CodeGlobals: a globals export was imported as an environment (info).
	CodeGlobals = "postman.globals"
	// CodeVersion: a collection schema version this reader was not written
	// for; read as v2.1 (info, file level).
	CodeVersion = "postman.version"
)

// bom is the UTF-8 byte order mark, ignored at the start of a file.
var bom = []byte("\xef\xbb\xbf")

// Importer reads Postman collections, environments and globals.
type Importer struct{}

var _ importer.Importer = Importer{}

var format = importer.Format{
	ID:         "postman",
	Name:       "Postman collection, environment or globals (v2.0, v2.1)",
	Extensions: []string{".json"},
}

// Format describes the Postman format.
func (Importer) Format() importer.Format { return format }

var (
	reSchema   = regexp.MustCompile(`schema\.(?:get)?postman\.com/json/collection/`)
	reScope    = regexp.MustCompile(`"_postman_variable_scope"\s*:`)
	reExported = regexp.MustCompile(`"_postman_(?:id|exported_using|exported_at)"\s*:`)
	reInfo     = regexp.MustCompile(`"info"\s*:\s*\{`)
	reItem     = regexp.MustCompile(`"item"\s*:\s*\[`)
	reValues   = regexp.MustCompile(`"values"\s*:\s*\[`)
)

// Detect is Sure for a collection that names a Postman schema URL and for
// an environment or globals export (`_postman_variable_scope`), Likely
// for JSON with Postman's export members (`_postman_id`…) and a
// collection's or environment's shape, Maybe for other JSON named
// *.postman_collection.json and the like, No otherwise.
func (Importer) Detect(src importer.Source) int {
	head := bytes.TrimPrefix(src.Data, bom)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 || head[0] != '{' {
		return importer.No
	}
	switch {
	case reSchema.Match(head), reScope.Match(head):
		return importer.Sure
	case reExported.Match(head) && (reInfo.Match(head) || reItem.Match(head) || reValues.Match(head)):
		return importer.Likely
	}
	name := strings.ToLower(src.Name)
	for _, s := range []string{".postman_collection.json", ".postman_environment.json", ".postman_globals.json"} {
		if strings.HasSuffix(name, s) {
			return importer.Maybe
		}
	}
	return importer.No
}

// Parse reads a collection, environment or globals export into the model.
// It fails only when the file is not JSON, or not a Postman export PostSonde
// reads (a v1 collection, a data dump).
func (im Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	if !utf8.Valid(src.Data) {
		return nil, importer.InvalidFile(format, "The file is not UTF-8 text.", importer.Location{})
	}
	root, err := parseJSON(bytes.TrimPrefix(src.Data, bom))
	if err != nil {
		at := importer.Location{}
		if je, ok := err.(*jsonError); ok {
			at.Line = je.line
		}
		return nil, importer.InvalidFile(format, "The file is not valid JSON: "+err.Error()+".", at)
	}
	if !isMap(root) {
		return nil, importer.InvalidFile(format, "The file is not a Postman export: it is not a JSON object.", importer.Location{Line: root.Line})
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	switch {
	case sget(root, "_postman_variable_scope") != "" || (get(root, "info") == nil && get(root, "values") != nil):
		return parseEnvironment(root), nil
	case get(root, "info") != nil || get(root, "item") != nil:
		return parseCollection(ctx, root)
	case get(root, "requests") != nil && get(root, "order") != nil:
		return nil, v1(root)
	case get(root, "collections") != nil:
		return nil, importer.InvalidFile(format, "The file is a Postman data dump, which PostSonde does not read: export each collection and environment on its own.", importer.Location{Line: root.Line})
	}
	return nil, importer.InvalidFile(format, "The file is not a Postman export: it has neither a collection's info and items nor an environment's values.", importer.Location{Line: root.Line})
}

// v1 is the error of a Collection v1 file.
func v1(root *yaml.Node) error {
	return importer.InvalidFile(format, "The file is a Postman Collection v1, which PostSonde does not read: export the collection again as v2.1 in Postman.", importer.Location{Line: root.Line})
}
