// Package lexer tokenizes Protocol Buffers source files (.proto) for the
// parser: proto2, proto3 and editions share one lexical grammar, including
// the protobuf text format used in aggregate option values.
//
// The lexer never aborts: malformed input becomes Error tokens carrying an
// ErrCode and a message, and lexing continues after them. Tokens hold byte
// offsets into the source instead of copies; use Token.Text for the raw text
// and StringValue, IntValue and FloatValue for decoded literals.
//
// Keywords (message, option, max, inf, ...) are plain Ident tokens: protobuf
// keywords are contextual, and the parser decides what they mean.
//
// Positions: Start and End are byte offsets, Line and Column are 1-based and
// Column counts Unicode code points (an invalid UTF-8 byte counts as one; a
// tab counts as one, unlike protoc, which expands tabs to 8 columns). "\r\n",
// "\n" and a lone "\r" end a line. See docs/proto.md.
package lexer

import "strconv"

// Kind is the kind of a token.
type Kind uint8

// Token kinds.
const (
	EOF   Kind = iota // end of input; Start == End == len(src)
	Error             // malformed input; see Token.Err and Token.Message

	Semicolon // ;
	Comma     // ,
	Dot       // .
	Equals    // =
	Colon     // :
	Slash     // / (in type URLs of text-format Any values)
	Minus     // -
	Plus      // +
	ParenL    // (
	ParenR    // )
	BracketL  // [
	BracketR  // ]
	BraceL    // {
	BraceR    // }
	AngleL    // <
	AngleR    // >

	Ident  // [A-Za-z_][A-Za-z0-9_]*
	Int    // decimal, 0x hex or 0 octal integer, without sign
	Float  // 1.5, .5, 1., 1e9, 1.5f; without sign (inf and nan are Ident)
	String // "..." or '...'

	Comment // "// ..." without the line terminator, or "/* ... */"

	numKinds
)

var kindNames = [numKinds]string{
	EOF: "EOF", Error: "Error",
	Semicolon: ";", Comma: ",", Dot: ".", Equals: "=", Colon: ":", Slash: "/",
	Minus: "-", Plus: "+", ParenL: "(", ParenR: ")", BracketL: "[", BracketR: "]",
	BraceL: "{", BraceR: "}", AngleL: "<", AngleR: ">",
	Ident: "Ident", Int: "Int", Float: "Float", String: "String",
	Comment: "Comment",
}

func (k Kind) String() string {
	if k < numKinds {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// IsPunctuator reports whether k is a symbol token.
func (k Kind) IsPunctuator() bool { return k >= Semicolon && k <= AngleR }

// ErrCode classifies an Error token.
type ErrCode uint8

// Error codes.
const (
	ErrNone                ErrCode = iota
	ErrUnexpectedChar              // a character that starts no token
	ErrInvalidUTF8                 // a byte that is not valid UTF-8 outside a string or comment
	ErrInvalidNumber               // malformed number, or a number run into an identifier
	ErrUnterminatedString          // a string without its closing quote on its line
	ErrInvalidEscape               // bad escape sequence in a closed string
	ErrUnterminatedComment         // "/*" without "*/"
)

var errCodeNames = [...]string{
	ErrNone:                "none",
	ErrUnexpectedChar:      "unexpected character",
	ErrInvalidUTF8:         "invalid UTF-8",
	ErrInvalidNumber:       "invalid number",
	ErrUnterminatedString:  "unterminated string",
	ErrInvalidEscape:       "invalid escape sequence",
	ErrUnterminatedComment: "unterminated comment",
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

// SkipComments drops Comment tokens.
const SkipComments Mode = 1
