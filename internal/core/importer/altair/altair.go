// Package altair is the import framework's Altair GraphQL Client format
// (IMP-08, docs/import.md#altair-exports): window exports (.agq, one
// GraphQL request), collection exports (.agc, a folder of requests with
// nested sub-collections) and environment exports. Every window becomes a
// graphql request: endpoint, query, variables, headers, extensions,
// connection params, auth and file variables (upload rows whose files are
// left to choose). Altair's {{variable}} syntax is PostSonde's,
// so texts are imported as they are. Pre- and post-request scripts are
// kept as notes, not run. Standard library only.
package altair

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

// Diagnostic codes of the format (docs/import.md#altair-exports).
const (
	// CodeScript: a pre-request or post-request script was kept in the
	// description, not run (warning).
	CodeScript = "altair.script"
	// CodeAuthUnsupported: an auth type PostSonde lacks (OAuth 2.0…); the
	// request sends no auth (warning).
	CodeAuthUnsupported = "altair.auth_unsupported"
	// CodeSubscriptionURL: a subscription URL other than the one PostSonde
	// derives from the endpoint was kept in the description (warning).
	CodeSubscriptionURL = "altair.subscription_url"
	// CodeTransport: a request or subscription handler PostSonde does not
	// have (Action Cable, AppSync, Socket.IO, SSE…), or one it replaces
	// with its own (info or warning).
	CodeTransport = "altair.transport"
	// CodeFiles: file variables were imported as upload rows without
	// their files, which the export does not have (info).
	CodeFiles = "altair.files"
	// CodeMethod: an HTTP method other than POST or GET; sent with POST
	// (warning).
	CodeMethod = "altair.method"
	// CodeUnsupported: an entry that is not a window or collection,
	// skipped (warning).
	CodeUnsupported = "altair.unsupported"
	// CodeOrphan: a sub-collection whose parent (parentPath) is not in the
	// file, or in a loop of parents; placed in the collection it is
	// listed in (warning).
	CodeOrphan = "altair.orphan"
	// CodeVersion: an export version this reader was not written for; read
	// as version 1 (info, file level).
	CodeVersion = "altair.version"
)

// bom is the UTF-8 byte order mark, ignored at the start of a file.
var bom = []byte("\xef\xbb\xbf")

// Importer reads Altair window, collection and environment exports.
type Importer struct{}

var _ importer.Importer = Importer{}

var format = importer.Format{
	ID:         "altair",
	Name:       "Altair GraphQL Client window, collection or environment",
	Extensions: []string{".agq", ".agc", ".json"},
}

// Format describes the Altair format.
func (Importer) Format() importer.Format { return format }

var (
	reType = regexp.MustCompile(`"type"\s*:\s*"(window|collection|environment)"`)
	// Members only an Altair window has.
	reWindow = regexp.MustCompile(`"(?:windowName|apiUrl|subscriptionUrl|preRequestScriptEnabled|requestHandlerId)"\s*:`)
	// Members only an Altair collection has.
	reCollection = regexp.MustCompile(`"queries"\s*:\s*\[`)
	// Altair's own environment state.
	reEnvironment = regexp.MustCompile(`"variablesJson"\s*:`)
)

// Detect is Sure for JSON that says it is a window (with a window's
// members), a collection (with queries) or an environment, Likely for a
// window or collection without the type, Maybe for other JSON named
// *.agq or *.agc, No otherwise.
func (Importer) Detect(src importer.Source) int {
	head := bytes.TrimPrefix(src.Data, bom)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 || head[0] != '{' {
		return importer.No
	}
	window, collection := reWindow.Match(head), reCollection.Match(head)
	if m := reType.FindSubmatch(head); m != nil {
		switch string(m[1]) {
		case "window":
			if window {
				return importer.Sure
			}
		case "collection":
			if collection {
				return importer.Sure
			}
		case "environment":
			if bytes.Contains(head, []byte(`"variables"`)) || reEnvironment.Match(head) {
				return importer.Sure
			}
		}
	}
	switch {
	case collection && window, reEnvironment.Match(head) && bytes.Contains(head, []byte(`"title"`)):
		return importer.Likely
	}
	name := strings.ToLower(src.Name)
	if strings.HasSuffix(name, ".agq") || strings.HasSuffix(name, ".agc") {
		return importer.Maybe
	}
	return importer.No
}

// Parse reads a window, collection or environment export into the model.
// It fails only when the file is not JSON, or not an Altair export.
func (Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
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
		return nil, importer.InvalidFile(format, "The file is not an Altair export: it is not a JSON object.", importer.Location{Line: root.Line})
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	var m *importer.Model
	switch typ := sget(root, "type"); {
	case typ == "collection" || typ == "" && get(root, "queries") != nil:
		m, err = parseCollection(ctx, root)
	case typ == "window" || typ == "" && (get(root, "query") != nil || get(root, "apiUrl") != nil):
		m = parseWindowFile(root)
	case typ == "environment" || typ == "" && get(root, "variablesJson") != nil:
		m = parseEnvironment(root)
	default:
		return nil, importer.InvalidFile(format, "The file is not an Altair export: it is not a window, a collection or an environment.", importer.Location{Line: root.Line})
	}
	if err != nil {
		return nil, err
	}
	if d := version(root); d != nil {
		m.Diagnostics = append(m.Diagnostics, *d)
	}
	return m, nil
}

// version returns an info when the export names a version other than 1.
// Altair has written version 1 since its first export format; the members
// changed within it.
func version(root *yaml.Node) *importer.Diagnostic {
	n := get(root, "version")
	if n == nil || str(n) == "1" {
		return nil
	}
	return &importer.Diagnostic{Code: CodeVersion, Severity: importer.SeverityInfo,
		Source:  importer.Location{Line: n.Line, Column: n.Column, Path: "version"},
		Message: "The export's version is not 1; it is read as version 1."}
}
