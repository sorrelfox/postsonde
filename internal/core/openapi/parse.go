package openapi

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Codes of the errors Parse returns (errs.Error.Code). YAML syntax errors
// keep the yaml package's codes (yaml.invalid_yaml, yaml.unsupported,
// yaml.too_large).
const (
	// CodeInvalidJSON: the document looks like JSON but is not valid
	// JSON. errs kind Parse.
	CodeInvalidJSON = "openapi.invalid_json"
	// CodeNotOpenAPI: the document parsed but is not an OpenAPI
	// document (not a mapping, or no openapi field). errs kind Parse.
	CodeNotOpenAPI = "openapi.not_openapi"
	// CodeUnsupportedVersion: a Swagger version other than 2.0, or an
	// OpenAPI version other than 3.x. errs kind Unsupported.
	CodeUnsupportedVersion = "openapi.unsupported_version"
	// CodeTooLarge: the input is over Limits. errs kind Unsupported.
	CodeTooLarge = "openapi.too_large"
)

// Limits bound the work Parse does. A zero field means the DefaultLimits
// value.
type Limits struct {
	// MaxBytes is the largest input accepted.
	MaxBytes int
	// MaxDepth is the deepest nesting of the document. References add
	// no nesting: their targets are built from a queue.
	MaxDepth int
	// MaxNodes bounds the values in the document tree.
	MaxNodes int
	// MaxRefs bounds the work of resolving references: each $ref
	// followed and each JSON pointer segment walked counts once.
	MaxRefs int
	// MaxValueBytes bounds the JSON text of all example, default, enum
	// and const values together (YAML aliases can make it much larger
	// than the input).
	MaxValueBytes int
}

// DefaultLimits are used for zero Limits fields: 32 MiB, 1000 levels,
// 2,000,000 nodes, 10,000,000 reference steps and 64 MiB of values.
var DefaultLimits = Limits{
	MaxBytes:      32 << 20,
	MaxDepth:      1000,
	MaxNodes:      2_000_000,
	MaxRefs:       10_000_000,
	MaxValueBytes: 64 << 20,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = d.MaxNodes
	}
	if l.MaxRefs <= 0 {
		l.MaxRefs = d.MaxRefs
	}
	if l.MaxValueBytes <= 0 {
		l.MaxValueBytes = d.MaxValueBytes
	}
	return l
}

// Parse reads an OpenAPI 3.0 or 3.1 document, or a Swagger 2.0 one
// (converted to 3.0), JSON or YAML, with DefaultLimits. See ParseLimits.
func Parse(data []byte) (*Document, []Diagnostic, error) {
	return ParseLimits(data, Limits{})
}

// ParseLimits reads an OpenAPI 3.0.x or 3.1.x document in JSON or YAML
// into a Document with its local references resolved.
//
// A Swagger 2.0 document is converted to OpenAPI 3.0 first (see
// convertSwagger2 and docs/openapi.md#swagger-20): the Document is the
// 3.0 form, with Swagger set to "2.0", and the positions of its objects
// and diagnostics are in the Swagger document.
//
// The error is non-nil only when there is no document to work with: the
// input is not valid JSON or YAML, is not an OpenAPI 3.x document, or is
// over the limits. It is an *errs.Error with a code (Code* constants or
// the yaml package's) and, when known, "line" and "column" details.
//
// Everything else — broken or external references, values of the wrong
// type, missing required fields, unsupported keywords — is reported as
// Diagnostics with source positions, and the rest of the document is
// still read.
func ParseLimits(data []byte, lim Limits) (*Document, []Diagnostic, error) {
	lim = lim.withDefaults()
	if len(data) > lim.MaxBytes {
		return nil, nil, errs.New(errs.Unsupported,
			"The OpenAPI document is too large: "+strconv.Itoa(len(data))+" bytes, the limit is "+strconv.Itoa(lim.MaxBytes)+".").
			WithCode(CodeTooLarge)
	}
	root, err := parseTree(data, lim)
	if err != nil {
		return nil, nil, err
	}
	root = root.Resolve()
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil, notOpenAPI(root, "The document is not an OpenAPI document: its top level is not an object.")
	}
	if sw := root.Get("swagger").Resolve(); sw != nil {
		return parseSwagger2(root, sw, lim)
	}
	minor, err := version(root)
	if err != nil {
		return nil, nil, err
	}
	b := newBuilder(root, minor, lim)
	doc := b.document()
	return doc, b.diags, nil
}

// parseSwagger2 converts a Swagger 2.0 document to OpenAPI 3.0 and reads
// that; the diagnostics point into the Swagger document.
func parseSwagger2(root, sw *yaml.Node, lim Limits) (*Document, []Diagnostic, error) {
	if v := scalarText(sw); v != "2.0" || sw.Tag != yaml.TagStr && sw.Tag != yaml.TagFloat {
		e := errs.New(errs.Unsupported, "Swagger version "+strconv.Quote(v)+" is not supported, only 2.0 and OpenAPI 3.0 and 3.1.").
			WithCode(CodeUnsupportedVersion)
		return nil, nil, e.WithDetail("line", strconv.Itoa(sw.Line)).WithDetail("column", strconv.Itoa(sw.Column))
	}
	expandMerges(root)
	conv, c, err := convertSwagger2(root, lim)
	if err != nil {
		return nil, nil, err
	}
	b := newBuilder(conv, 0, lim)
	b.srcPtr = c.sourcePointer
	doc := b.document()
	doc.Swagger = "2.0"
	return doc, append(c.diag, b.diags...), nil
}

// parseTree parses JSON (when the document starts with '{' or '[') or
// YAML into a positioned tree.
func parseTree(data []byte, lim Limits) (*yaml.Node, error) {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\ufeff")), " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		root, jerr := parseJSON(data, lim.MaxDepth, lim.MaxNodes)
		if jerr != nil {
			var e *errs.Error
			if jerr.tooLarge {
				e = errs.New(errs.Unsupported, "The OpenAPI document is too large: "+jerr.msg+
					" (line "+strconv.Itoa(jerr.line)+", column "+strconv.Itoa(jerr.column)+").").WithCode(CodeTooLarge)
			} else {
				e = errs.New(errs.Parse, "The JSON is not valid: "+jerr.msg+
					" (line "+strconv.Itoa(jerr.line)+", column "+strconv.Itoa(jerr.column)+").").WithCode(CodeInvalidJSON)
			}
			return nil, e.WithDetail("line", strconv.Itoa(jerr.line)).WithDetail("column", strconv.Itoa(jerr.column))
		}
		return root, nil
	}
	docs, err := yaml.ParseAllLimits(data, yaml.Limits{MaxBytes: lim.MaxBytes, MaxDepth: lim.MaxDepth, MaxNodes: lim.MaxNodes})
	if err != nil {
		return nil, yaml.AsErr(err)
	}
	if len(docs) == 0 {
		return nil, errs.New(errs.Parse, "The document is empty.").WithCode(CodeNotOpenAPI)
	}
	if len(docs) > 1 {
		return nil, errs.New(errs.Parse, "The file holds "+strconv.Itoa(len(docs))+" YAML documents; an OpenAPI file holds one.").
			WithCode(CodeNotOpenAPI).WithDetail("line", strconv.Itoa(docs[1].Line)).WithDetail("column", strconv.Itoa(docs[1].Column))
	}
	return docs[0], nil
}

func notOpenAPI(n *yaml.Node, msg string) error {
	e := errs.New(errs.Parse, msg).WithCode(CodeNotOpenAPI).
		WithHint("Choose an OpenAPI 3.0 or 3.1 file in JSON or YAML.")
	if n != nil && n.Line > 0 {
		e = e.WithDetail("line", strconv.Itoa(n.Line)).WithDetail("column", strconv.Itoa(n.Column))
	}
	return e
}

// version reads the openapi field and returns the minor version of 3.x.
func version(root *yaml.Node) (int, error) {
	if sw := root.Get("swagger").Resolve(); sw != nil {
		e := errs.New(errs.Unsupported, "Swagger "+scalarText(sw)+" documents are not supported, only OpenAPI 3.0 and 3.1.").
			WithCode(CodeUnsupportedVersion).
			WithHint("Convert the document to OpenAPI 3 (for example with the Swagger Editor) and import it again.")
		return 0, e.WithDetail("line", strconv.Itoa(sw.Line)).WithDetail("column", strconv.Itoa(sw.Column))
	}
	v := root.Get("openapi").Resolve()
	if v == nil {
		return 0, notOpenAPI(root, "The document is not an OpenAPI document: it has no openapi field.")
	}
	text := scalarText(v)
	parts := strings.SplitN(text, ".", 3)
	major, minor := -1, -1
	if len(parts) >= 2 && v.Kind == yaml.ScalarNode {
		major, _ = atoiStrict(parts[0])
		minor, _ = atoiStrict(parts[1])
	}
	if major != 3 || minor < 0 {
		e := errs.New(errs.Unsupported, "OpenAPI version "+strconv.Quote(text)+" is not supported, only 3.0 and 3.1.").
			WithCode(CodeUnsupportedVersion)
		return 0, e.WithDetail("line", strconv.Itoa(v.Line)).WithDetail("column", strconv.Itoa(v.Column))
	}
	return minor, nil
}

func atoiStrict(s string) (int, bool) {
	if s == "" || len(s) > 6 {
		return -1, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func scalarText(n *yaml.Node) string {
	n = n.Resolve()
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}
