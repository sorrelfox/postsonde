// Package openapiimport is the import framework's OpenAPI format (IMP-05,
// IMP-06, docs/import.md#openapi-documents): an OpenAPI 3.0 or 3.1
// document, or a Swagger 2.0 one (converted to 3.0 by the reader), JSON
// or YAML, read by internal/core/openapi, becomes a folder per tag with a
// request per operation, their parameters, example bodies (from the
// document's examples, or generated from the schemas), the servers as a
// baseUrl variable and the security schemes as auth.
//
// FromDocument converts an already read Document (Swagger 2.0 documents
// come through it as the reader's 3.0 form), and SchemaExample and
// MediaTypeExample generate examples for other uses (mock responses).
// Standard library only.
package openapiimport

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
)

// Diagnostic codes of the format (docs/import.md#openapi-documents). The
// reader's own diagnostics (openapi.bad_ref, openapi.external_ref…) keep
// their codes and are attached to the operation they are in, else to the
// file.
const (
	// CodeNoServer: the document names no server; baseUrl is empty
	// (warning, root folder).
	CodeNoServer = "openapi.no_server"
	// CodeRelativeServer: the server URL is relative to where the document
	// is served (warning, root folder).
	CodeRelativeServer = "openapi.relative_server"
	// CodeServers: more servers than the first, or an operation's own
	// server (info).
	CodeServers = "openapi.servers"
	// CodeAuthUnsupported: a security scheme PostSonde has no auth for
	// (mutualTLS, an HTTP scheme other than basic, bearer and digest, an
	// unknown scheme); the request sends no auth (warning).
	CodeAuthUnsupported = "openapi.auth_unsupported"
	// CodeAuthChanged: a scheme imported as a near equivalent: OAuth 2.0
	// and OpenID Connect as Bearer auth, a cookie API key as a Cookie
	// header (warning).
	CodeAuthChanged = "openapi.auth_changed"
	// CodeAuthExtra: a requirement needs several schemes at once; the
	// ones after the first that are not API key headers are not set up
	// (warning).
	CodeAuthExtra = "openapi.auth_extra"
	// CodeParameterIgnored: an Accept, Content-Type or Authorization
	// header parameter (ignored by the specification), or one the auth
	// already sends (info).
	CodeParameterIgnored = "openapi.parameter_ignored"
	// CodeCookieParameter: cookie parameters became a Cookie header (info).
	CodeCookieParameter = "openapi.cookie_parameter"
	// CodePathParameter: a path parameter whose name PostSonde does not
	// see as a placeholder (warning).
	CodePathParameter = "openapi.path_placeholder"
	// CodeMediaType: the body accepts other media types than the one used
	// (info).
	CodeMediaType = "openapi.media_type"
	// CodeExample: which named example the body uses, or that the
	// document's examples are external files (info).
	CodeExample = "openapi.example"
	// CodeExampleTruncated: the generated example stops at a recursive
	// schema, or at the depth or size limit (info).
	CodeExampleTruncated = "openapi.example_truncated"
	// CodeFile: the body or a form part sends a file; choose it (info).
	CodeFile = "openapi.file"
	// CodeCallback: the operation's callbacks are requests the API makes,
	// not imported (info).
	CodeCallback = "openapi.callback"
	// CodeWebhooks: the document's webhooks are requests the API makes,
	// not imported (info, file level).
	CodeWebhooks = "openapi.webhooks"
	// CodeTags: an operation with several tags is in the first tag's
	// folder (info).
	CodeTags = "openapi.tags"
)

// bom is the UTF-8 byte order mark, ignored at the start of a file.
var bom = []byte("\xef\xbb\xbf")

// Importer reads OpenAPI 3.0 and 3.1 documents, and Swagger 2.0 ones.
type Importer struct{}

var _ importer.Importer = Importer{}

var format = importer.Format{
	ID:         "openapi",
	Name:       "OpenAPI 3.0 / 3.1, Swagger 2.0",
	Extensions: []string{".yaml", ".yml", ".json"},
}

// Format describes the OpenAPI format.
func (Importer) Format() importer.Format { return format }

var (
	reJSON3     = regexp.MustCompile(`"openapi"\s*:\s*"3\.|"swagger"\s*:\s*"2\.0"`)
	reJSONAny   = regexp.MustCompile(`"(?:openapi|swagger)"\s*:\s*"`)
	reYAML3     = regexp.MustCompile(`(?m)^(?:["']?openapi["']?[ \t]*:[ \t]*["']?3\.|["']?swagger["']?[ \t]*:[ \t]*["']?2\.0["']?[ \t]*(?:#.*)?$)`)
	reYAMLAny   = regexp.MustCompile(`(?m)^["']?(?:openapi|swagger)["']?[ \t]*:`)
	reYAMLPaths = regexp.MustCompile(`(?m)^["']?paths["']?[ \t]*:`)
)

// Detect is Sure for a document that declares `openapi: 3.x` or
// `swagger: "2.0"` (at the top level of a YAML file, anywhere in the start
// of a JSON one), Maybe for another openapi or swagger version (so the
// reader says why it cannot be imported), No otherwise.
func (Importer) Detect(src importer.Source) int {
	head := bytes.TrimPrefix(src.Data, bom)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 {
		return importer.No
	}
	if head[0] == '{' {
		switch {
		case reJSON3.Match(head):
			return importer.Sure
		case reJSONAny.Match(head):
			return importer.Maybe
		}
		return importer.No
	}
	switch {
	case reYAML3.Match(head):
		return importer.Sure
	case reYAMLAny.Match(head) && reYAMLPaths.Match(head):
		return importer.Maybe
	}
	return importer.No
}

// Parse reads the document and converts it with FromDocument. It fails
// only when the file is not an OpenAPI 3.x or Swagger 2.0 document at all.
func (Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	doc, diags, err := openapi.Parse(src.Data)
	if err != nil {
		var e *errs.Error
		at := importer.Location{}
		msg := "The file is not an OpenAPI document."
		hint := ""
		if errors.As(err, &e) {
			msg = e.Message
			hint = e.Hint
			at.Line, _ = strconv.Atoi(e.Details["line"])
			at.Column, _ = strconv.Atoi(e.Details["column"])
		}
		out := importer.InvalidFile(format, msg, at)
		if hint != "" {
			out = out.WithHint(hint)
		}
		if at.Column > 0 {
			out = out.WithDetail("column", strconv.Itoa(at.Column))
		}
		return nil, out
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	return FromDocument(doc, diags), nil
}
