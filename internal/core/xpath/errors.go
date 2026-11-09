package xpath

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the errs errors this package returns.
const (
	// CodeInvalidQuery: the XPath expression does not parse. Details:
	// offset, offset16.
	CodeInvalidQuery = "xpath.invalid_query"
	// CodeUnknownPrefix: with Options.StrictNames, a name test uses a
	// prefix Options.Namespaces does not bind. Details: prefix, offset,
	// offset16.
	CodeUnknownPrefix = "xpath.unknown_prefix"
	// CodeTooComplex: evaluation went over Limits.MaxSteps.
	CodeTooComplex = "xpath.too_complex"
)

// SyntaxError is an invalid XPath expression, with its position.
type SyntaxError struct {
	// Offset is the byte offset in the expression where the problem is;
	// the expression's length when it ends too early.
	Offset int
	// Offset16 is Offset in UTF-16 code units (CodeMirror positions).
	Offset16 int
	// Message says what is wrong, e.g. "unexpected ']': expected a node
	// test (...)".
	Message string
}

// Error returns "xpath: <message> at <offset>".
func (e *SyntaxError) Error() string {
	return "xpath: " + e.Message + " at " + strconv.Itoa(e.Offset)
}

// Err returns the error as errs.InvalidArgument with code
// xpath.invalid_query and the offsets in the details.
func (e *SyntaxError) Err() error {
	return errs.New(errs.InvalidArgument, "Invalid XPath: "+e.Message+" (at position "+strconv.Itoa(e.Offset16)+").").
		WithCode(CodeInvalidQuery).
		WithDetail("offset", strconv.Itoa(e.Offset)).
		WithDetail("offset16", strconv.Itoa(e.Offset16))
}

func syntaxErr(src string, offset int, msg string) *SyntaxError {
	offset = max(0, min(offset, len(src)))
	return &SyntaxError{Offset: offset, Offset16: offset16(src, offset), Message: msg}
}

func offset16(s string, offset int) int {
	n := 0
	for _, r := range s[:offset] {
		if r == utf8.RuneError {
			n++
			continue
		}
		n += utf16.RuneLen(r)
	}
	return n
}
