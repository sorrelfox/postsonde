package parser

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
)

// Code classifies a parse error.
type Code uint8

// Error codes.
const (
	// ErrLexical is a malformed token; Error.LexCode says which kind.
	ErrLexical Code = iota + 1
	// ErrUnexpectedToken is a token, or the end of input, where something
	// else was expected ("expected name, found }").
	ErrUnexpectedToken
	// ErrUnclosed is a "{", "(" or "[" without its closing delimiter. The
	// range is the opening delimiter.
	ErrUnclosed
	// ErrVariableInConst is a variable inside a constant value (a variable
	// default value or a directive argument on a variable definition).
	ErrVariableInConst
	// ErrReservedName is a name that is not allowed where it appears, such as
	// a fragment named "on".
	ErrReservedName
	// ErrNotExecutable is a type system definition or extension (SDL). The
	// parser skips it; the range covers the whole definition.
	ErrNotExecutable
	// ErrTooDeep is nesting beyond Config.MaxDepth; the nested part is
	// skipped up to its closing delimiter. Reported once per document.
	ErrTooDeep
	// ErrTooManyErrors is the last error once Config.MaxErrors is reached;
	// parsing continues but further errors are dropped.
	ErrTooManyErrors
	// ErrNotTypeSystem is an operation or fragment in a schema document
	// (ParseSchema). It is parsed and dropped; the range covers it.
	ErrNotTypeSystem
)

var codeNames = [...]string{
	ErrLexical:         "lexical error",
	ErrUnexpectedToken: "unexpected token",
	ErrUnclosed:        "unclosed delimiter",
	ErrVariableInConst: "variable in constant value",
	ErrReservedName:    "reserved name",
	ErrNotExecutable:   "not executable",
	ErrTooDeep:         "nesting too deep",
	ErrTooManyErrors:   "too many errors",
	ErrNotTypeSystem:   "not a type system definition",
}

func (c Code) String() string {
	if int(c) < len(codeNames) && codeNames[c] != "" {
		return codeNames[c]
	}
	return "Code(" + strconv.Itoa(int(c)) + ")"
}

// Error is a syntax error with the byte range [Start, End) it applies to.
// A zero-width range marks a position, such as the end of input.
type Error struct {
	Code       Code
	LexCode    lexer.ErrCode // for ErrLexical
	Start, End int
	Message    string
}

func (e Error) Error() string {
	return "graphql: " + e.Message + " at byte " + strconv.Itoa(e.Start)
}
