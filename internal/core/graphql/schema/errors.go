package schema

import (
	"fmt"
	"strings"
)

// Error codes of package schema. Errors are *errs.Error values.
const (
	// CodeMalformedJSON: the input is not JSON (kind parse).
	CodeMalformedJSON = "graphql.schema.malformed_json"
	// CodeMalformedIntrospection: the JSON does not have the shape of an
	// introspection result, such as a number where a name belongs (kind
	// parse).
	CodeMalformedIntrospection = "graphql.schema.malformed_introspection"
	// CodeNoSchema: the JSON holds no __schema and no errors (kind parse).
	CodeNoSchema = "graphql.schema.no_schema"
	// CodeServerErrors: the response holds errors instead of a __schema,
	// usually because the server rejected a field of the query (kind
	// protocol). Details["errors"] joins the messages with newlines.
	CodeServerErrors = "graphql.schema.server_errors"
	// CodeInvalidSchema: the result converted, but some references are
	// broken (kind parse). The error wraps Problems, and the Schema returned
	// with it is usable.
	CodeInvalidSchema = "graphql.schema.invalid"
)

// Hints of package schema.
const (
	HintServerErrors = "The server may not support every field of the introspection query. " +
		"Retry with the conservative introspection query, or check that introspection is enabled."
	HintInvalidSchema = "The server's introspection result is inconsistent. " +
		"The rest of the schema is usable; broken references are ignored."
)

// Problem is one inconsistency found while converting an introspection
// result, or one error of a schema built from SDL.
type Problem struct {
	// Path locates the element of an introspection result: "User",
	// "User.friends", "Query.user(id:)", "ReviewInput.stars",
	// "Episode.JEDI", "@include", "@include(if:)", "schema.queryType".
	// Empty for SDL problems, which have a position instead.
	Path string
	// Message says what is wrong.
	Message string
	// Start and End are the byte range in the SDL source; Line and Column
	// its 1-based start (Column in code points). All zero for
	// introspection problems.
	Start, End   int
	Line, Column int
}

func (p Problem) String() string {
	switch {
	case p.Line > 0:
		return fmt.Sprintf("%d:%d: %s", p.Line, p.Column, p.Message)
	case p.Path == "":
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Problems is the list of problems of a conversion. It is the cause of a
// CodeInvalidSchema error; get it with errors.As.
type Problems []Problem

// maxProblemsInMessage bounds how many problems Error spells out.
const maxProblemsInMessage = 3

func (ps Problems) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d schema problem", len(ps))
	if len(ps) != 1 {
		b.WriteByte('s')
	}
	for i, p := range ps {
		if i == maxProblemsInMessage {
			fmt.Fprintf(&b, "; and %d more", len(ps)-i)
			break
		}
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(p.String())
	}
	return b.String()
}
