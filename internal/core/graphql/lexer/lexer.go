package lexer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Lexer produces tokens from a GraphQL source one at a time.
// The zero value is not usable; call New.
type Lexer struct {
	src  string
	mode Mode
	off  int // next byte to scan

	// Line and column of byte posOff; advanced lazily by position.
	posOff, line, col int
}

// New returns a lexer over src.
func New(src string, mode Mode) *Lexer {
	return &Lexer{src: src, mode: mode, line: 1, col: 1}
}

// Tokenize returns all tokens of src, ending with the EOF token.
func Tokenize(src string, mode Mode) []Token {
	l := New(src, mode)
	var toks []Token
	for {
		t := l.Next()
		toks = append(toks, t)
		if t.Kind == EOF {
			return toks
		}
	}
}

// Next returns the next token allowed by the mode. At the end of input it
// returns an EOF token, and keeps returning it on further calls.
func (l *Lexer) Next() Token {
	for {
		t := l.scan()
		switch {
		case t.Kind.IsIgnored() && l.mode&EmitIgnored == 0:
		case t.Kind == Comment && l.mode&SkipComments != 0:
		default:
			return t
		}
	}
}

func (l *Lexer) token(kind Kind, start, end int) Token {
	line, col := l.position(start)
	l.off = end
	return Token{Kind: kind, Start: start, End: end, Line: line, Column: col}
}

func (l *Lexer) errorToken(code ErrCode, start, end int, msg string) Token {
	t := l.token(Error, start, end)
	t.Err = code
	t.Message = msg
	return t
}

// position returns the line and column of off, which must not be before
// the previous call's offset. Each byte is walked once over the whole run.
func (l *Lexer) position(off int) (line, col int) {
	s := l.src
	i := l.posOff
	for i < off {
		c := s[i]
		switch {
		case c == '\n':
			if i == 0 || s[i-1] != '\r' { // "\r\n" was counted at '\r'
				l.line++
			}
			l.col = 1
			i++
		case c == '\r':
			l.line++
			l.col = 1
			i++
		case c < utf8.RuneSelf:
			l.col++
			i++
		default:
			_, size := utf8.DecodeRuneInString(s[i:])
			l.col++
			i += size
		}
	}
	l.posOff = off
	return l.line, l.col
}

var punctuators = [utf8.RuneSelf]Kind{
	'!': Bang, '$': Dollar, '&': Amp, '(': ParenL, ')': ParenR, ':': Colon,
	'=': Equals, '@': At, '[': BracketL, ']': BracketR, '{': BraceL,
	'|': Pipe, '}': BraceR,
}

func (l *Lexer) scan() Token {
	src, start := l.src, l.off
	if start >= len(src) {
		return l.token(EOF, len(src), len(src))
	}
	c := src[start]
	switch {
	case c == ' ' || c == '\t':
		end := start + 1
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
		return l.token(Whitespace, start, end)
	case c == '\n':
		return l.token(LineTerminator, start, start+1)
	case c == '\r':
		if start+1 < len(src) && src[start+1] == '\n' {
			return l.token(LineTerminator, start, start+2)
		}
		return l.token(LineTerminator, start, start+1)
	case c == ',':
		return l.token(Comma, start, start+1)
	case c == '#':
		end := start + 1
		for end < len(src) && src[end] != '\n' && src[end] != '\r' {
			end++
		}
		return l.token(Comment, start, end)
	case c == '.':
		if strings.HasPrefix(src[start:], "...") {
			return l.token(Spread, start, start+3)
		}
		end := start + 1
		if end < len(src) && src[end] == '.' {
			end++
		}
		return l.errorToken(ErrUnexpectedChar, start, end,
			fmt.Sprintf("unexpected %q, did you mean \"...\"?", src[start:end]))
	case c == '"':
		if strings.HasPrefix(src[start:], `"""`) {
			return l.scanBlockString(start)
		}
		return l.scanString(start)
	case c == '-' || isDigit(c):
		return l.scanNumber(start)
	case isNameStart(c):
		end := start + 1
		for end < len(src) && isNameContinue(src[end]) {
			end++
		}
		return l.token(Name, start, end)
	case c < utf8.RuneSelf:
		if k := punctuators[c]; k != EOF {
			return l.token(k, start, start+1)
		}
		return l.errorToken(ErrUnexpectedChar, start, start+1, unexpectedChar(rune(c)))
	}
	r, size := utf8.DecodeRuneInString(src[start:])
	switch {
	case r == utf8.RuneError && size == 1:
		return l.errorToken(ErrInvalidUTF8, start, start+1,
			fmt.Sprintf("invalid UTF-8 byte 0x%02X", c))
	case r == 0xFEFF:
		return l.token(BOM, start, start+size)
	}
	return l.errorToken(ErrUnexpectedChar, start, start+size, unexpectedChar(r))
}

func unexpectedChar(r rune) string {
	return fmt.Sprintf("unexpected character %q (U+%04X)", r, r)
}

// scanNumber scans IntValue or FloatValue. A malformed number becomes one
// Error token that also swallows directly following name characters and
// dots, so "123abc" or "1.2.3" is a single error.
func (l *Lexer) scanNumber(start int) Token {
	src := l.src
	i := start
	digits := func() {
		for i < len(src) && isDigit(src[i]) {
			i++
		}
	}
	fail := func(msg string) Token {
		end := i
		for end < len(src) && (isNameContinue(src[end]) || src[end] == '.') {
			end++
		}
		if end == start {
			end = start + 1
		}
		return l.errorToken(ErrInvalidNumber, start, end, "invalid number: "+msg)
	}
	peek := func() string {
		if i >= len(src) {
			return "end of input"
		}
		r, _ := utf8.DecodeRuneInString(src[i:])
		return fmt.Sprintf("%q", r)
	}

	kind := Int
	if src[i] == '-' {
		i++
	}
	switch {
	case i >= len(src) || !isDigit(src[i]):
		return fail(`expected a digit after "-", got ` + peek())
	case src[i] == '0':
		i++
		if i < len(src) && isDigit(src[i]) {
			return fail("unexpected digit after 0")
		}
	default:
		digits()
	}
	if i < len(src) && src[i] == '.' {
		kind = Float
		i++
		if i >= len(src) || !isDigit(src[i]) {
			return fail(`expected a digit after ".", got ` + peek())
		}
		digits()
	}
	if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
		kind = Float
		i++
		if i < len(src) && (src[i] == '+' || src[i] == '-') {
			i++
		}
		if i >= len(src) || !isDigit(src[i]) {
			return fail("expected a digit in the exponent, got " + peek())
		}
		digits()
	}
	if i < len(src) && (src[i] == '.' || isNameStart(src[i])) {
		return fail("unexpected " + peek() + " after the number")
	}
	return l.token(kind, start, i)
}

// scanString scans a "..." string. A string without a closing quote on its
// line is an ErrUnterminatedString token up to (not including) the line
// terminator. A closed string with a bad escape or invalid UTF-8 is one
// Error token covering the whole literal.
func (l *Lexer) scanString(start int) Token {
	src := l.src
	var code ErrCode
	var msg string
	i := start + 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"':
			if code != ErrNone {
				return l.errorToken(code, start, i+1, msg)
			}
			return l.token(String, start, i+1)
		case c == '\n' || c == '\r':
			return l.errorToken(ErrUnterminatedString, start, i, "unterminated string")
		case c == '\\':
			_, n, emsg := readEscape(src, i)
			if emsg != "" && code == ErrNone {
				code, msg = ErrInvalidEscape, emsg
			}
			i += n
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRuneInString(src[i:])
			if r == utf8.RuneError && size == 1 && code == ErrNone {
				code, msg = ErrInvalidUTF8, fmt.Sprintf("invalid UTF-8 byte 0x%02X in string", c)
			}
			i += size
		}
	}
	return l.errorToken(ErrUnterminatedString, start, len(src), "unterminated string")
}

// scanBlockString scans a """...""" block string. Without a closing """ it
// runs to the end of input as an ErrUnterminatedBlockString token.
func (l *Lexer) scanBlockString(start int) Token {
	src := l.src
	var bad bool
	var msg string
	i := start + 3
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"' && strings.HasPrefix(src[i:], `"""`):
			if bad {
				return l.errorToken(ErrInvalidUTF8, start, i+3, msg)
			}
			return l.token(BlockString, start, i+3)
		case c == '\\' && strings.HasPrefix(src[i:], `\"""`):
			i += 4
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRuneInString(src[i:])
			if r == utf8.RuneError && size == 1 && !bad {
				bad, msg = true, fmt.Sprintf("invalid UTF-8 byte 0x%02X in block string", c)
			}
			i += size
		}
	}
	return l.errorToken(ErrUnterminatedBlockString, start, len(src), "unterminated block string")
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isNameStart(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
}

func isNameContinue(c byte) bool { return isNameStart(c) || isDigit(c) }
