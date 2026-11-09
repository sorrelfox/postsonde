// Package jsonschema validates JSON values against a JSON Schema (TEST-07):
// a subset of draft 2020-12, with the draft-07 (and 2019-09, draft-06,
// draft-04) spellings of the keywords that changed. It is pure core: the
// standard library plus this module's jsonpath package, whose value model
// (jsonpath.Decode: *jsonpath.Object, []any, json.Number, string, bool,
// nil) it validates. See docs/jsonschema.md.
//
//	s, err := jsonschema.CompileJSON([]byte(`{"type":"object","required":["id"]}`), jsonschema.Options{})
//	doc, _ := jsonpath.Decode(body)
//	res, err := s.Validate(ctx, doc, jsonschema.DefaultLimits)
//	for _, e := range res.Errors {
//		fmt.Println(e) // $: missing required property "id"
//	}
//
// Only references inside the schema document ("#/$defs/x", "#anchor",
// and URIs of the $id resources it declares) and into the documents of
// Options.Resources are resolved; nothing is fetched, and a reference to
// any other document is a compile error.
package jsonschema

import (
	"context"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
)

// Error codes.
const (
	// CodeInvalidSchema: the schema is not valid JSON, or a keyword has a
	// value of the wrong kind (kind invalid_argument). Details: location.
	CodeInvalidSchema = "jsonschema.invalid_schema"
	// CodeUnresolvedRef: a $ref or $dynamicRef points outside the schema
	// document (and Options.Resources), or to a location that does not
	// exist (kind invalid_argument). Details: location, ref.
	CodeUnresolvedRef = "jsonschema.unresolved_ref"
	// CodeInvalidPattern: a pattern or a patternProperties name is not a
	// regular expression Go's RE2 engine accepts (kind unsupported when
	// it uses an ECMA-262 feature RE2 lacks, such as lookaround or
	// backreferences). Details: location, pattern.
	CodeInvalidPattern = "jsonschema.invalid_pattern"
	// CodeTooComplex: validation went over Limits.MaxSteps (kind
	// unsupported).
	CodeTooComplex = "jsonschema.too_complex"
	// CodeTooDeep: validation went over Limits.MaxDepth nested schemas,
	// or a $ref loops back to a schema that is already being applied to
	// the same value (kind unsupported).
	CodeTooDeep = "jsonschema.too_deep"
)

// Draft is a JSON Schema version.
type Draft int

// Drafts. The zero value is 2020-12.
const (
	Draft2020 Draft = iota
	Draft2019
	Draft7
	Draft6
	Draft4
)

func (d Draft) String() string {
	switch d {
	case Draft2019:
		return "2019-09"
	case Draft7:
		return "draft-07"
	case Draft6:
		return "draft-06"
	case Draft4:
		return "draft-04"
	}
	return "2020-12"
}

// Options control compiling.
type Options struct {
	// Draft is used when the schema has no $schema (or one this package
	// does not know). The zero value is 2020-12.
	Draft Draft
	// FormatAssertion makes "format" fail values that are not valid in a
	// format this package knows (date-time, email, uuid…; see
	// docs/jsonschema.md). Without it, as the 2020-12 specification says
	// by default, format is only an annotation and never fails.
	FormatAssertion bool
	// Resources are other schema documents, by absolute URI (a fragment
	// is dropped; other keys are ignored), that references may point
	// into, in the value model of Compile. Nothing is fetched or read: a
	// reference to a document that is neither the schema, one of its $id
	// resources nor here is a compile error. A document is compiled only
	// when referenced; its own $id resources and anchors are then known
	// too.
	Resources map[string]any
}

// Limits bound one validation.
type Limits struct {
	// MaxSteps bounds the work: each schema applied to a value, each
	// array element and object member visited, and each 64 bytes of a
	// value compared or hashed count as one. 0 means no limit.
	MaxSteps int
	// MaxDepth bounds the nesting of schemas applied (through
	// subschemas and references). 0 means DefaultLimits.MaxDepth.
	MaxDepth int
	// MaxErrors bounds the errors reported; Result.Truncated says when
	// more were found. 0 means DefaultLimits.MaxErrors.
	MaxErrors int
}

// DefaultLimits are generous for API responses and schemas as written.
var DefaultLimits = Limits{MaxSteps: 10_000_000, MaxDepth: 512, MaxErrors: 100}

// Error is one way the value does not match the schema.
type Error struct {
	// InstancePath is the JSON Pointer (RFC 6901) of the value in the
	// document: "" for the whole value, "/items/0/id".
	InstancePath string `json:"instancePath"`
	// Path is the same location as a JSONPath: "$", "$.items[0].id",
	// "$['a b']".
	Path string `json:"path"`
	// KeywordPath is the JSON Pointer of the failing keyword along the
	// evaluation, through references: "/properties/id/$ref/type".
	KeywordPath string `json:"keywordPath"`
	// SchemaPath is the failing keyword's location in the schema
	// document, as a URI fragment: "#/$defs/id/type"; in a document of
	// Options.Resources, its URI with the fragment:
	// "https://example.com/defs.json#/$defs/id/type".
	SchemaPath string `json:"schemaPath"`
	// Keyword is the failing keyword ("type", "required"…), or "false"
	// for a false schema.
	Keyword string `json:"keyword"`
	// Message says what the value should be, without the location:
	// "must be integer, not string".
	Message string `json:"message"`
}

// String is the error with its JSONPath: "$.id: must be integer, not string".
func (e Error) String() string { return e.Path + ": " + e.Message }

// Result is the outcome of a validation.
type Result struct {
	// Valid is true when the value matches the schema.
	Valid bool `json:"valid"`
	// Errors are the ways it does not, in document order of the schema's
	// keywords; empty when Valid. At most Limits.MaxErrors.
	Errors []Error `json:"errors"`
	// Truncated is true when more errors were found than reported.
	Truncated bool `json:"truncated,omitempty"`
}

// Schema is a compiled schema; it is safe for concurrent use.
type Schema struct {
	root   *node
	nodes  map[string]*node
	draft  Draft
	annots bool // unevaluated* keywords need annotations
	format bool
}

// Draft is the version the schema was compiled as.
func (s *Schema) Draft() Draft { return s.draft }

// CompileJSON parses data as JSON and compiles it. See Compile.
func CompileJSON(data []byte, opts Options) (*Schema, error) {
	doc, err := jsonpath.Decode(data)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "jsonpath: ")
		return nil, errs.Wrap(err, errs.InvalidArgument, "The JSON schema is not valid JSON: "+msg+".").
			WithCode(CodeInvalidSchema)
	}
	return Compile(doc, opts)
}

// Compile compiles a schema given in the value model of jsonpath.Decode
// (map[string]any objects and float64 numbers are accepted too). The
// errors are *errs.Error with CodeInvalidSchema, CodeUnresolvedRef or
// CodeInvalidPattern.
func Compile(doc any, opts Options) (*Schema, error) {
	c := newCompiler(doc, opts)
	root, err := c.compileRoot()
	if err != nil {
		return nil, err
	}
	return &Schema{root: root, nodes: c.nodes, draft: c.draft, annots: c.annots, format: opts.FormatAssertion}, nil
}

// Validate applies the schema to v, a value in the model of
// jsonpath.Decode (map[string]any and Go numbers are accepted too). An
// invalid value is not an error: Result says why. The error is ctx's, or
// an *errs.Error with CodeTooComplex or CodeTooDeep.
func (s *Schema) Validate(ctx context.Context, v any, lim Limits) (Result, error) {
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = DefaultLimits.MaxDepth
	}
	if lim.MaxErrors <= 0 {
		lim.MaxErrors = DefaultLimits.MaxErrors
	}
	st := &state{ctx: ctx, lim: lim, annots: s.annots, format: s.format, nodes: s.nodes, active: map[activeKey]bool{}}
	ok, _ := st.validate(s.root, v, 0)
	if st.fatal != nil {
		return Result{}, st.fatal
	}
	res := Result{Valid: ok, Errors: st.errs, Truncated: st.truncated}
	if res.Errors == nil {
		res.Errors = []Error{}
	}
	if !ok && len(res.Errors) == 0 {
		// Every failure records an error; this is a safety net.
		res.Errors = append(res.Errors, Error{Path: "$", KeywordPath: "", SchemaPath: "#", Message: "does not match the schema"})
	}
	return res, nil
}

// ---- JSON Pointer and JSONPath rendering ----

func escapeToken(s string) string {
	if !strings.ContainsAny(s, "~/") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func unescapeToken(s string) string {
	if !strings.Contains(s, "~") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// pointer joins tokens into a JSON Pointer.
func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(t))
	}
	return b.String()
}

// jsonPath renders instance tokens as a JSONPath; isIndex marks array
// indexes.
func jsonPath(tokens []pathToken) string {
	var b strings.Builder
	b.WriteByte('$')
	for _, t := range tokens {
		if t.index {
			b.WriteByte('[')
			b.WriteString(t.name)
			b.WriteByte(']')
			continue
		}
		if isIdentifier(t.name) {
			b.WriteByte('.')
			b.WriteString(t.name)
			continue
		}
		b.WriteString("['")
		for _, r := range t.name {
			switch r {
			case '\'':
				b.WriteString(`\'`)
			case '\\':
				b.WriteString(`\\`)
			default:
				if r < 0x20 {
					q := strconv.QuoteRune(r)
					b.WriteString(q[1 : len(q)-1])
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteString("']")
	}
	return b.String()
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

type pathToken struct {
	name  string
	index bool
}
