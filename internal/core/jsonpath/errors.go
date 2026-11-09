package jsonpath

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the errs errors this package returns.
const (
	// CodeInvalidQuery: the JSONPath query does not parse or is not
	// well-typed. Details: offset, offset16.
	CodeInvalidQuery = "jsonpath.invalid_query"
	// CodeInvalidJSON: the document is not valid JSON.
	CodeInvalidJSON = "jsonpath.invalid_json"
	// CodeTooComplex: evaluation went over Limits.MaxSteps.
	CodeTooComplex = "jsonpath.too_complex"
)

// SyntaxError is an invalid JSONPath query: a syntax error or a
// well-typedness error (RFC 9535 §2.4.3), with its position.
type SyntaxError struct {
	// Offset is the byte offset in the query where the problem is; the
	// query's length when it ends too early.
	Offset int
	// Offset16 is Offset in UTF-16 code units (CodeMirror positions).
	Offset16 int
	// Message says what is wrong, e.g. "unexpected ']': expected a
	// selector (name, '*', index, slice or '?' filter)".
	Message string
}

// Error returns "jsonpath: <message> at <offset>".
func (e *SyntaxError) Error() string {
	return "jsonpath: " + e.Message + " at " + strconv.Itoa(e.Offset)
}

// Err returns the error as errs.InvalidArgument with code
// jsonpath.invalid_query and the offsets in the details.
func (e *SyntaxError) Err() error {
	return errs.New(errs.InvalidArgument, "Invalid JSONPath: "+e.Message+" (at position "+strconv.Itoa(e.Offset16)+").").
		WithCode(CodeInvalidQuery).
		WithDetail("offset", strconv.Itoa(e.Offset)).
		WithDetail("offset16", strconv.Itoa(e.Offset16))
}
