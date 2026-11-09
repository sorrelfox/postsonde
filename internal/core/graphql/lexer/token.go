// Package lexer tokenizes GraphQL documents (October 2021 spec plus the
// current working draft) for the parser, validation and autocomplete.
//
// The lexer never aborts: malformed input becomes Error tokens carrying an
// ErrCode and a message, and lexing continues after them, so half-typed
// queries still tokenize. Tokens hold byte offsets into the source instead
// of copies; use Token.Text for the raw text and StringValue for the decoded
// value of a string or block string.
//
// Positions: Start and End are byte offsets, Line and Column are 1-based and
// Column counts Unicode code points (an invalid UTF-8 byte counts as one).
// Editors that use UTF-16 offsets (CodeMirror, the browser) convert with
// UTF16Index. See docs/graphql-engine.md.
package lexer

import "strconv"

// Kind is the kind of a token.
type Kind uint8

// Token kinds. Punctuators are named after the GraphQL spec's lexical
// grammar; Whitespace, LineTerminator, Comma and BOM are the spec's ignored
// tokens and are only returned in EmitIgnored mode.
const (
	EOF   Kind = iota // end of input; Start == End == len(src)
	Error             // malformed input; see Token.Err and Token.Message

	Bang     // !
	Dollar   // $
	Amp      // &
	ParenL   // (
	ParenR   // )
	Spread   // ...
	Colon    // :
	Equals   // =
	At       // @
	BracketL // [
	BracketR // ]
	BraceL   // {
	Pipe     // |
	BraceR   // }

	Name        // [_A-Za-z][_0-9A-Za-z]*
	Int         // IntValue
	Float       // FloatValue
	String      // "..." StringValue
	BlockString // """...""" StringValue

	Comment // # to the end of the line, without the line terminator

	Whitespace     // run of spaces and horizontal tabs
	LineTerminator // "\n", "\r\n" or "\r"
	Comma          // ,
	BOM            // U+FEFF

	numKinds
)

var kindNames = [numKinds]string{
	EOF: "EOF", Error: "Error",
	Bang: "!", Dollar: "$", Amp: "&", ParenL: "(", ParenR: ")", Spread: "...",
	Colon: ":", Equals: "=", At: "@", BracketL: "[", BracketR: "]",
	BraceL: "{", Pipe: "|", BraceR: "}",
	Name: "Name", Int: "Int", Float: "Float", String: "String", BlockString: "BlockString",
	Comment:    "Comment",
	Whitespace: "Whitespace", LineTerminator: "LineTerminator", Comma: "Comma", BOM: "BOM",
}

func (k Kind) String() string {
	if k < numKinds {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// IsPunctuator reports whether k is one of the spec's punctuators.
func (k Kind) IsPunctuator() bool { return k >= Bang && k <= BraceR }

// IsIgnored reports whether k is an ignored token other than a comment
// (whitespace, line terminator, comma, byte order mark).
func (k Kind) IsIgnored() bool { return k >= Whitespace && k <= BOM }

// ErrCode classifies an Error token.
type ErrCode uint8

// Error codes.
const (
	ErrNone                    ErrCode = iota
	ErrUnexpectedChar                  // a character that starts no token
	ErrInvalidUTF8                     // a byte that is not valid UTF-8
	ErrInvalidNumber                   // malformed IntValue or FloatValue
	ErrUnterminatedString              // "..." without a closing quote on its line
	ErrUnterminatedBlockString         // """ without a closing """
	ErrInvalidEscape                   // bad escape sequence in a closed "..." string
)

var errCodeNames = [...]string{
	ErrNone:                    "none",
	ErrUnexpectedChar:          "unexpected character",
	ErrInvalidUTF8:             "invalid UTF-8",
	ErrInvalidNumber:           "invalid number",
	ErrUnterminatedString:      "unterminated string",
	ErrUnterminatedBlockString: "unterminated block string",
	ErrInvalidEscape:           "invalid escape sequence",
}

func (c ErrCode) String() string {
	if int(c) < len(errCodeNames) {
		return errCodeNames[c]
	}
	return "ErrCode(" + strconv.Itoa(int(c)) + ")"
}

// Token is one lexical token. It refers to the source by offset.
type Token struct {
	Kind Kind
	Err  ErrCode // ErrNone unless Kind == Error

	Start, End   int // byte offsets: the token is src[Start:End]
	Line, Column int // 1-based position of Start; Column counts code points

	Message string // human-readable description for Error tokens
}

// Text returns the token's raw source text.
func (t Token) Text(src string) string { return src[t.Start:t.End] }

// Mode selects which tokens Next returns.
type Mode uint8

const (
	// SkipComments drops Comment tokens.
	SkipComments Mode = 1 << iota
	// EmitIgnored returns Whitespace, LineTerminator, Comma and BOM tokens.
	// With EmitIgnored and without SkipComments the returned tokens cover
	// the source contiguously.
	EmitIgnored
)
