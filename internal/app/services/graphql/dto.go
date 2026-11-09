package graphql

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
)

// DTOs are the only types GraphQLService methods take or return. See
// docs/bindings.md.

// SchemaInputDTO names the GraphQL request whose schema is meant.
type SchemaInputDTO struct {
	RequestID string `json:"requestId"`
}

// SchemaInfoDTO describes a request's schema: the one cached for its
// endpoint (Source "introspection"), or the one in its schema file
// (Source "file", GQL-11).
type SchemaInfoDTO struct {
	// Source is "introspection" or "file".
	Source string `json:"source"`
	// File is the request's schema file as stored (relative to the project
	// root or absolute); "" for introspection.
	File string `json:"file"`
	// ModifiedAt is the schema file's modification time, RFC 3339 in UTC;
	// "" for introspection or a file that cannot be read.
	ModifiedAt string `json:"modifiedAt"`
	// Error says why the schema file gives no schema (missing, unreadable,
	// syntax errors); nil otherwise.
	Error *SchemaErrorDTO `json:"error"`
	// ProblemList holds the first MaxSchemaProblems problems of a schema
	// file, with positions: syntax errors (Error is set) or type system
	// errors (the schema is usable). Empty for introspection.
	ProblemList []SchemaProblemDTO `json:"problemList"`

	// Endpoint is the request's URL with its {{variables}} rendered.
	Endpoint string `json:"endpoint"`
	// Cached is false when no schema was fetched for the endpoint yet, or
	// the schema file gives none.
	Cached bool `json:"cached"`
	// Key is what Diagnostics and Complete take to use this schema; ""
	// when none is cached.
	Key string `json:"key"`
	// FetchedAt is when the schema was fetched (for a file, its
	// modification time), RFC 3339 in UTC; "" when none is cached.
	FetchedAt string `json:"fetchedAt"`
	// TypeCount is the number of named types, introspection types left
	// out.
	TypeCount int `json:"typeCount"`
	// Query is "full" or "conservative": the introspection query that
	// returned the schema.
	Query string `json:"query"`
	// Problems is the number of broken references in the schema, which is
	// usable anyway; for a file, the number of problems in ProblemList
	// before the cap.
	Problems int `json:"problems"`
	// Change is set on the result of a fetch only (FetchSchema's data
	// item, GQL-19): "new" when nothing was cached for the endpoint
	// before, "changed" or "unchanged" against the schema cached before.
	// "" otherwise.
	Change string `json:"change"`
}

// SaveSchemaInputDTO asks SaveSchemaSDL to save a schema as SDL (GQL-20).
type SaveSchemaInputDTO struct {
	// Key is SchemaInfoDTO.Key: the schema to save.
	Key string `json:"key"`
	// Name is the request's name, for the suggested file name; "" uses
	// the endpoint's host.
	Name string `json:"name"`
}

// SaveSchemaResultDTO is the result of SaveSchemaSDL.
type SaveSchemaResultDTO struct {
	// Path is where the SDL was written; "" when the dialog was
	// cancelled.
	Path string `json:"path"`
	// TypeCount is the number of named types saved: built-in scalars and
	// introspection types are left out of SDL.
	TypeCount int `json:"typeCount"`
}

// SchemaErrorDTO is why a schema file gives no schema.
type SchemaErrorDTO struct {
	// Code is graphql.schema_file_missing, graphql.schema_file_unreadable,
	// graphql.schema_file_too_large, graphql.schema_file_no_project or
	// graphql.schema.sdl_syntax.
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// SchemaProblemDTO is one problem of a schema file. Line and Column are
// 1-based; Column counts code points.
type SchemaProblemDTO struct {
	Message string `json:"message"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

// DiagnosticsInputDTO is a document to check.
type DiagnosticsInputDTO struct {
	// Key is SchemaInfoDTO.Key: the schema to validate against; "" checks
	// syntax only.
	Key  string `json:"key"`
	Text string `json:"text"`
}

// OperationDTO is an operation of a GraphQL document.
type OperationDTO struct {
	// Name is "" for an anonymous operation.
	Name string `json:"name"`
	// Type is "query", "mutation" or "subscription".
	Type string `json:"type"`
}

// DiagnosticDTO is one problem of a GraphQL document.
type DiagnosticDTO struct {
	// Source is "syntax" (the parser) or "schema" (validation against the
	// schema).
	Source string `json:"source"`
	// Code is stable: graphql.syntax.<kind> or graphql.validate.<rule>.
	Code string `json:"code"`
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Offset and EndOffset are the range to underline in UTF-16 code
	// units, as CodeMirror counts; EndOffset may equal Offset (the end of
	// input).
	Offset    int `json:"offset"`
	EndOffset int `json:"endOffset"`
	// Line and Column are 1-based; Column counts UTF-16 code units.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Message says what is wrong, in graphql-js's words for schema errors.
	Message string `json:"message"`
}

// DiagnosticsDTO is the result of Diagnostics.
type DiagnosticsDTO struct {
	// Schema is true when the document was validated against a schema.
	Schema bool `json:"schema"`
	// Operations are the document's operations in source order, also when
	// it has syntax errors (as far as the parser recovered them).
	Operations []OperationDTO `json:"operations"`
	// Diagnostics are sorted by position, at most MaxDiagnostics.
	Diagnostics []DiagnosticDTO `json:"diagnostics"`
	// Count is the number of diagnostics before the cap; SyntaxCount the
	// number of syntax errors among them.
	Count       int `json:"count"`
	SyntaxCount int `json:"syntaxCount"`
}

// CompleteInputDTO asks for the suggestions at a cursor.
type CompleteInputDTO struct {
	// Key is SchemaInfoDTO.Key; "" completes syntax only.
	Key  string `json:"key"`
	Text string `json:"text"`
	// Offset is the cursor in UTF-16 code units (a CodeMirror position).
	Offset int `json:"offset"`
}

// CompletionItemDTO is one suggestion.
type CompletionItemDTO struct {
	Label string `json:"label"`
	// Kind is field, argument, type, enum-value, directive, variable,
	// fragment, keyword or input-field.
	Kind string `json:"kind"`
	// Detail is a short type string: "[User!]!", "Int = 10", "on User",
	// "(if: Boolean!)".
	Detail string `json:"detail"`
	// Documentation is the schema description.
	Documentation     string `json:"documentation"`
	Deprecated        bool   `json:"deprecated"`
	DeprecationReason string `json:"deprecationReason"`
	// InsertText replaces [From, To).
	InsertText string `json:"insertText"`
	// Suffix is inserted after InsertText with the cursor SuffixCursor
	// code units into it: "()", " {  }", ": ". "" when the delimiter is
	// already there.
	Suffix       string `json:"suffix"`
	SuffixCursor int    `json:"suffixCursor"`
}

// CompletionDTO is the result of Complete.
type CompletionDTO struct {
	// Context is the kind of position: selection, argument, value,
	// directive, … (complete.Context); none, comment and string have no
	// items.
	Context string `json:"context"`
	// From and To are the range an item replaces, in UTF-16 code units.
	From int `json:"from"`
	To   int `json:"to"`
	// Prefix is the typed text the items were filtered by.
	Prefix string `json:"prefix"`
	// ParentType, Owner and ExpectedType describe the position: the
	// selection set's type, "Type.field" or "@directive" for arguments, the
	// value's type such as "[ID!]!". Empty when unknown.
	ParentType   string `json:"parentType"`
	Owner        string `json:"owner"`
	ExpectedType string `json:"expectedType"`
	// Schema is true when a cached schema was used.
	Schema bool `json:"schema"`
	// Items are the suggestions, best first, at most MaxCompletionItems;
	// Count is how many matched.
	Items []CompletionItemDTO `json:"items"`
	Count int                 `json:"count"`
}

func toSchemaInfoDTO(endpoint string, e *schemacache.Entry) SchemaInfoDTO {
	out := SchemaInfoDTO{Endpoint: endpoint, Source: SourceIntrospection, ProblemList: []SchemaProblemDTO{}}
	if e == nil {
		return out
	}
	out.Cached = true
	out.Key = endpoint
	out.FetchedAt = e.FetchedAt.UTC().Format(time.RFC3339)
	out.TypeCount = schemacache.TypeCount(e.Schema)
	out.Query = string(e.Query)
	out.Problems = e.Problems
	return out
}
