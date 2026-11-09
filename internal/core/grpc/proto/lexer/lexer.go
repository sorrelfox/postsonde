package lexer

import (
	"fmt"
	"unicode/utf8"
)

// Lexer produces tokens from a .proto source one at a time.
// The zero value is not usable; call New.
type Lexer struct {
	src  string
	mode Mode
	off  int // next byte to scan

	// Line and column of byte posOff; advanced lazily by position.
	posOff, line, col int
}

// New returns a lexer over src. A UTF-8 byte order mark at the start of src
// is skipped like whitespace.
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
		if t.Kind != Comment || l.mode&SkipComments == 0 {
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
	';': Semicolon, ',': Comma, '.': Dot, '=': Equals, ':': Colon, '/': Slash,
	'-': Minus, '+': Plus, '(': ParenL, ')': ParenR, '[': BracketL, ']': BracketR,
	'{': BraceL, '}': BraceR, '<': AngleL, '>': AngleR,
}

// IsSpace reports whether c is whitespace in .proto source.
func IsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

const bom = "\xef\xbb\xbf"

func (l *Lexer) scan() Token {
	src := l.src
	start := l.off
	if start == 0 && len(src) >= len(bom) && src[:len(bom)] == bom {
		start = len(bom)
	}
	for start < len(src) && IsSpace(src[start]) {
		start++
	}
	if start >= len(src) {
		return l.token(EOF, len(src), len(src))
	}
	c := src[start]
	switch {
	case isLetter(c):
		end := start + 1
		for end < len(src) && isAlnum(src[end]) {
			end++
		}
		return l.token(Ident, start, end)
	case isDigit(c):
		return l.scanNumber(start)
	case c == '.' && start+1 < len(src) && isDigit(src[start+1]):
		return l.scanNumber(start)
	case c == '"' || c == '\'':
		return l.scanString(start)
	case c == '/' && start+1 < len(src) && src[start+1] == '/':
		end := start + 2
		for end < len(src) && src[end] != '\n' && src[end] != '\r' {
			end++
		}
		return l.token(Comment, start, end)
	case c == '/' && start+1 < len(src) && src[start+1] == '*':
		for i := start + 2; i+1 < len(src); i++ {
			if src[i] == '*' && src[i+1] == '/' {
				return l.token(Comment, start, i+2)
			}
		}
		return l.errorToken(ErrUnterminatedComment, start, len(src), "unterminated block comment")
	case c < utf8.RuneSelf && punctuators[c] != EOF:
		return l.token(punctuators[c], start, start+1)
	case c < utf8.RuneSelf:
		return l.errorToken(ErrUnexpectedChar, start, start+1, fmt.Sprintf("unexpected character %q", rune(c)))
	}
	r, size := utf8.DecodeRuneInString(src[start:])
	if r == utf8.RuneError && size == 1 {
		return l.errorToken(ErrInvalidUTF8, start, start+1, fmt.Sprintf("invalid UTF-8 byte 0x%02X", c))
	}
	return l.errorToken(ErrUnexpectedChar, start, start+size, fmt.Sprintf("unexpected character %q", r))
}

// scanNumber scans an integer or floating-point literal with protoc's rules:
// "0x" starts a hex integer, a leading "0" followed by digits an octal one,
// anything else is decimal and becomes a float with a ".", an exponent or an
// "f" suffix (text format). A number running into a letter, digit or "."
// is one ErrInvalidNumber token up to the end of that run.
func (l *Lexer) scanNumber(start int) Token {
	src := l.src
	i := start
	digits := func(ok func(byte) bool) int {
		from := i
		for i < len(src) && ok(src[i]) {
			i++
		}
		return i - from
	}
	var msg string
	isFloat := false
	switch {
	case src[i] == '0' && i+1 < len(src) && (src[i+1] == 'x' || src[i+1] == 'X'):
		i += 2
		if digits(isHexDigit) == 0 {
			msg = `"0x" must be followed by hex digits`
		}
	case src[i] == '0' && i+1 < len(src) && isDigit(src[i+1]):
		digits(isOctalDigit)
		if i < len(src) && isDigit(src[i]) {
			msg = "numbers starting with a leading zero must be in octal"
		}
	default:
		if src[i] == '.' {
			i++
			isFloat = true
			digits(isDigit)
		} else {
			digits(isDigit)
			if i < len(src) && src[i] == '.' {
				i++
				isFloat = true
				digits(isDigit)
			}
		}
		if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
			i++
			isFloat = true
			if i < len(src) && (src[i] == '+' || src[i] == '-') {
				i++
			}
			if digits(isDigit) == 0 {
				msg = `"e" must be followed by an exponent`
			}
		}
		if i < len(src) && (src[i] == 'f' || src[i] == 'F') {
			i++
			isFloat = true
		}
	}
	if msg == "" && i < len(src) {
		switch {
		case isAlnum(src[i]):
			msg = "need space between number and identifier"
		case src[i] == '.' && isFloat:
			msg = "already saw decimal point or exponent; can't have another one"
		case src[i] == '.':
			msg = "hex and octal numbers must be integers"
		}
	}
	if msg != "" {
		for i < len(src) && (isAlnum(src[i]) || src[i] == '.') {
			i++
		}
		return l.errorToken(ErrInvalidNumber, start, i, msg)
	}
	if isFloat {
		return l.token(Float, start, i)
	}
	return l.token(Int, start, i)
}

// scanString scans a single- or double-quoted string. A string ends at its
// line: without the closing quote it is an ErrUnterminatedString token up to
// the line terminator. A closed string with a bad escape is an
// ErrInvalidEscape token over the whole string. Strings are byte strings, so
// invalid UTF-8 inside them is allowed.
func (l *Lexer) scanString(start int) Token {
	src := l.src
	q := src[start]
	var msg string
	i := start + 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == q:
			if msg != "" {
				return l.errorToken(ErrInvalidEscape, start, i+1, msg)
			}
			return l.token(String, start, i+1)
		case c == '\n' || c == '\r':
			return l.errorToken(ErrUnterminatedString, start, i, "unterminated string")
		case c == '\\':
			_, n, emsg := unescape(src, i, nil)
			if emsg != "" && msg == "" {
				msg = emsg
			}
			i += n
		default:
			i++
		}
	}
	return l.errorToken(ErrUnterminatedString, start, len(src), "unterminated string")
}

func isDigit(c byte) bool      { return '0' <= c && c <= '9' }
func isOctalDigit(c byte) bool { return '0' <= c && c <= '7' }
func isHexDigit(c byte) bool {
	return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}
func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' }
func isAlnum(c byte) bool  { return isLetter(c) || isDigit(c) }

// IsIdent reports whether s is a single identifier.
func IsIdent(s string) bool {
	if s == "" || !isLetter(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isAlnum(s[i]) {
			return false
		}
	}
	return true
}
