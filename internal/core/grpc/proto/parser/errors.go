package parser

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/lexer"
)

// Code classifies a syntax error.
type Code uint8

// Error codes.
const (
	// ErrLexical is a malformed token; Error.LexCode says which kind.
	ErrLexical Code = iota + 1
	// ErrUnexpectedToken is a token, or the end of input, where something
	// else was expected (`expected "=", found "}"`). A missing ";" at the end
	// of a line is reported as a zero-width range right after the previous
	// token.
	ErrUnexpectedToken
	// ErrUnclosed is a "{", "<" or "[" without its closing delimiter. The
	// range is the opening delimiter.
	ErrUnclosed
	// ErrInvalidLiteral is a literal that lexes but is not allowed where it
	// appears: an integer beyond 64 bits where an integer is required, an
	// "f" float suffix outside a message literal, or a "-" before a string,
	// message or identifier other than inf and nan.
	ErrInvalidLiteral
	// ErrMisplaced is a statement where the grammar does not allow it: a
	// syntax or edition statement that is not the first statement.
	ErrMisplaced
	// ErrTooDeep is nesting beyond Config.MaxDepth; the nested block is
	// skipped up to its closing delimiter. Reported once per file.
	ErrTooDeep
	// ErrTooManyErrors is the last error once Config.MaxErrors is reached;
	// parsing continues but further errors are dropped.
	ErrTooManyErrors
)

var codeNames = [...]string{
	ErrLexical:         "lexical error",
	ErrUnexpectedToken: "unexpected token",
	ErrUnclosed:        "unclosed delimiter",
	ErrInvalidLiteral:  "invalid literal",
	ErrMisplaced:       "misplaced statement",
	ErrTooDeep:         "nesting too deep",
	ErrTooManyErrors:   "too many errors",
}

func (c Code) String() string {
	if int(c) < len(codeNames) && codeNames[c] != "" {
		return codeNames[c]
	}
	return "Code(" + strconv.Itoa(int(c)) + ")"
}

// Error is a syntax error with the byte range [Start, End) it applies to and
// the 1-based line and column of Start. A zero-width range marks a position,
// such as the end of input.
type Error struct {
	Code         Code
	LexCode      lexer.ErrCode // for ErrLexical
	Start, End   int
	Line, Column int
	File         string // file name given to the parser; may be empty
	Message      string
}

// Error returns "file:line:column: message", like protoc.
func (e Error) Error() string {
	return location(e.File, e.Line, e.Column) + ": " + e.Message
}

func location(file string, line, col int) string {
	if file == "" {
		file = "<input>"
	}
	return file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col)
}
