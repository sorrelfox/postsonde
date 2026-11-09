package protojson

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// kind is a JSON token kind.
type kind uint16

const (
	kindInvalid kind = 0
	kindEOF     kind = 1 << iota
	kindNull
	kindBool
	kindNumber
	kindString
	kindName
	kindObjectOpen
	kindObjectClose
	kindArrayOpen
	kindArrayClose
	kindComma // only between tokens; never returned
)

const kindScalar = kindNull | kindBool | kindNumber | kindString

// token is one JSON token. raw is a subslice of the input.
type token struct {
	kind kind
	pos  int
	raw  []byte
	str  string // parsed string, or object name
	b    bool
}

func (t token) rawString() string { return string(t.raw) }

// scanner is a token-level JSON reader with the same acceptance rules as
// protobuf-go's internal JSON decoder: RFC 8259 syntax, whitespace limited
// to space, tab, CR and LF, literals and numbers that must end at a
// delimiter, strings that must be valid UTF-8 without raw control
// characters, and \u escapes whose surrogates must pair up. Object names
// are returned as kindName tokens; commas are checked and skipped.
type scanner struct {
	orig, in []byte

	peeked  bool
	last    token // last token read, for sequence checks
	lastErr error
	stack   []kind
}

func newScanner(b []byte) *scanner { return &scanner{orig: b, in: b} }

// clone returns an independent copy for reading ahead.
func (s *scanner) clone() *scanner {
	c := *s
	c.stack = append([]kind(nil), s.stack...)
	return &c
}

// peek returns the next token without consuming it.
func (s *scanner) peek() (token, error) {
	if !s.peeked {
		s.last, s.lastErr = s.next()
		s.peeked = true
	}
	return s.last, s.lastErr
}

// read returns the next token.
func (s *scanner) read() (token, error) {
	if s.peeked {
		s.peeked = false
		return s.last, s.lastErr
	}
	return s.next()
}

func (s *scanner) next() (token, error) {
	tok, err := s.parseNext()
	if err != nil {
		return token{}, err
	}
	switch tok.kind {
	case kindEOF:
		if len(s.stack) != 0 {
			return token{}, s.errorf(len(s.orig), "unexpected end of input")
		}
	case kindNull, kindBool, kindNumber:
		if !s.valueNext() {
			return token{}, s.unexpected(tok)
		}
	case kindString:
		if s.valueNext() {
			break
		}
		// A string that is not a value must be an object name.
		if s.last.kind&(kindObjectOpen|kindComma) == 0 {
			return token{}, s.unexpected(tok)
		}
		if len(s.in) == 0 {
			return token{}, s.errorf(len(s.orig), "unexpected end of input")
		}
		if c := s.in[0]; c != ':' {
			return token{}, s.errorf(s.pos(), "unexpected character %q, missing \":\" after object name", c)
		}
		tok.kind = kindName
		s.consume(1)
	case kindObjectOpen, kindArrayOpen:
		if !s.valueNext() {
			return token{}, s.unexpected(tok)
		}
		s.stack = append(s.stack, tok.kind)
	case kindObjectClose:
		if len(s.stack) == 0 || s.last.kind&(kindName|kindComma) != 0 || s.stack[len(s.stack)-1] != kindObjectOpen {
			return token{}, s.unexpected(tok)
		}
		s.stack = s.stack[:len(s.stack)-1]
	case kindArrayClose:
		if len(s.stack) == 0 || s.last.kind == kindComma || s.stack[len(s.stack)-1] != kindArrayOpen {
			return token{}, s.unexpected(tok)
		}
		s.stack = s.stack[:len(s.stack)-1]
	case kindComma:
		if len(s.stack) == 0 || s.last.kind&(kindScalar|kindObjectClose|kindArrayClose) == 0 {
			return token{}, s.unexpected(tok)
		}
	}
	s.last = tok
	if tok.kind == kindComma {
		return s.next()
	}
	return tok, nil
}

// valueNext reports whether a value may come next.
func (s *scanner) valueNext() bool {
	if len(s.stack) == 0 {
		return s.last.kind == kindInvalid
	}
	if s.stack[len(s.stack)-1] == kindObjectOpen {
		return s.last.kind&kindName != 0
	}
	return s.last.kind&(kindArrayOpen|kindComma) != 0
}

func (s *scanner) pos() int { return len(s.orig) - len(s.in) }

// consume drops n bytes and the whitespace after them.
func (s *scanner) consume(n int) {
	s.in = s.in[n:]
	for len(s.in) > 0 {
		switch s.in[0] {
		case ' ', '\n', '\r', '\t':
			s.in = s.in[1:]
		default:
			return
		}
	}
}

func (s *scanner) take(k kind, n int) token {
	t := token{kind: k, pos: s.pos(), raw: s.in[:n]}
	s.consume(n)
	return t
}

func (s *scanner) parseNext() (token, error) {
	s.consume(0)
	in := s.in
	if len(in) == 0 {
		return s.take(kindEOF, 0), nil
	}
	switch in[0] {
	case 'n':
		if n := matchLiteral("null", in); n != 0 {
			return s.take(kindNull, n), nil
		}
	case 't':
		if n := matchLiteral("true", in); n != 0 {
			t := s.take(kindBool, n)
			t.b = true
			return t, nil
		}
	case 'f':
		if n := matchLiteral("false", in); n != 0 {
			return s.take(kindBool, n), nil
		}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if n, ok := scanNumber(in); ok {
			return s.take(kindNumber, n), nil
		}
	case '"':
		str, n, err := s.parseString(in)
		if err != nil {
			return token{}, err
		}
		t := s.take(kindString, n)
		t.str = str
		return t, nil
	case '{':
		return s.take(kindObjectOpen, 1), nil
	case '}':
		return s.take(kindObjectClose, 1), nil
	case '[':
		return s.take(kindArrayOpen, 1), nil
	case ']':
		return s.take(kindArrayClose, 1), nil
	case ',':
		return s.take(kindComma, 1), nil
	}
	return token{}, s.errorf(s.pos(), "invalid value %s", invalidPrefix(in))
}

// invalidPrefix returns the start of an invalid value for error messages:
// up to 32 name-like characters, or one character.
func invalidPrefix(in []byte) string {
	n := 0
	for n < len(in) && n < 32 && isNotDelim(in[n]) {
		n++
	}
	if n == 0 {
		_, n = utf8.DecodeRune(in)
	}
	return string(in[:n])
}

func (s *scanner) unexpected(t token) error {
	if t.kind == kindEOF {
		return s.errorf(t.pos, "unexpected end of input")
	}
	return s.errorf(t.pos, "unexpected token %s", t.rawString())
}

func (s *scanner) errorf(pos int, format string, args ...any) error {
	return newError(s.orig, pos, format, args...)
}

// matchLiteral matches lit at the start of b, followed by a delimiter or
// the end of input, and returns its length or 0.
func matchLiteral(lit string, b []byte) int {
	if !bytes.HasPrefix(b, []byte(lit)) {
		return 0
	}
	if len(b) > len(lit) && isNotDelim(b[len(lit)]) {
		return 0
	}
	return len(lit)
}

// isNotDelim reports whether c may continue a literal or number.
func isNotDelim(c byte) bool {
	return c == '-' || c == '+' || c == '.' || c == '_' ||
		'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// scanNumber returns the length of the JSON number at the start of in.
func scanNumber(in []byte) (int, bool) {
	_, n, ok := splitNumber(in)
	if !ok {
		return 0, false
	}
	if n < len(in) && isNotDelim(in[n]) {
		return 0, false
	}
	return n, true
}

// numberParts are the pieces of a JSON number, as subslices of the input.
type numberParts struct {
	neg  bool
	intp []byte // without a lone leading 0
	frac []byte // without trailing zeros
	exp  []byte // with its sign
}

// splitNumber parses a JSON number prefix of in.
func splitNumber(in []byte) (numberParts, int, bool) {
	var p numberParts
	s := in
	if len(s) == 0 {
		return p, 0, false
	}
	if s[0] == '-' {
		p.neg = true
		s = s[1:]
		if len(s) == 0 {
			return p, 0, false
		}
	}
	switch {
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		n := 1
		for n < len(s) && isDigit(s[n]) {
			n++
		}
		p.intp = s[:n]
		s = s[n:]
	default:
		return p, 0, false
	}
	if len(s) >= 2 && s[0] == '.' && isDigit(s[1]) {
		n := 2
		for n < len(s) && isDigit(s[n]) {
			n++
		}
		p.frac = bytes.TrimRight(s[1:n], "0")
		s = s[n:]
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		e := s[1:]
		n := 0
		if e[0] == '+' || e[0] == '-' {
			n++
			if len(e) == 1 {
				return p, 0, false
			}
		}
		for n < len(e) && isDigit(e[n]) {
			n++
		}
		p.exp = e[:n]
		s = e[n:]
	}
	return p, len(in) - len(s), true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// intString returns the integer a JSON number denotes, in plain decimal,
// or false if it is not an integer (or its exponent is unreasonably large).
func (p numberParts) intString() (string, bool) {
	if len(p.intp) == 0 && len(p.frac) == 0 {
		return "0", true
	}
	exp := 0
	if len(p.exp) > 0 {
		e, err := strconv.ParseInt(string(p.exp), 10, 32)
		if err != nil {
			return "", false
		}
		exp = int(e)
	}
	var num []byte
	if exp >= 0 {
		if len(p.frac) > exp {
			return "", false
		}
		const maxDigits = 20 // len("18446744073709551615")
		if len(p.intp)+exp > maxDigits {
			return "", false
		}
		num = append(num, p.intp...)
		num = append(num, p.frac...)
		for range exp - len(p.frac) {
			num = append(num, '0')
		}
	} else {
		if len(p.frac) > 0 {
			return "", false
		}
		idx := len(p.intp) + exp
		if idx < 0 {
			return "", false
		}
		for _, c := range p.intp[idx:] {
			if c != '0' {
				return "", false
			}
		}
		num = p.intp[:idx]
	}
	if p.neg {
		return "-" + string(num), true
	}
	return string(num), true
}

// parseString parses the string literal at the start of in and returns its
// value and length.
func (s *scanner) parseString(in []byte) (string, int, error) {
	start := s.pos()
	rest := in[1:]
	var out []byte
	for len(rest) > 0 {
		off := start + len(in) - len(rest)
		r, n := utf8.DecodeRune(rest)
		switch {
		case r == utf8.RuneError && n == 1:
			return "", 0, s.errorf(off, "invalid UTF-8 in string")
		case r < ' ':
			return "", 0, s.errorf(off, "invalid character %q in string", r)
		case r == '"':
			return string(out), len(in) - len(rest) + 1, nil
		case r == '\\':
			if len(rest) < 2 {
				return "", 0, s.errorf(len(s.orig), "unexpected end of input")
			}
			switch c := rest[1]; c {
			case '"', '\\', '/':
				out, rest = append(out, c), rest[2:]
			case 'b':
				out, rest = append(out, '\b'), rest[2:]
			case 'f':
				out, rest = append(out, '\f'), rest[2:]
			case 'n':
				out, rest = append(out, '\n'), rest[2:]
			case 'r':
				out, rest = append(out, '\r'), rest[2:]
			case 't':
				out, rest = append(out, '\t'), rest[2:]
			case 'u':
				if len(rest) < 6 {
					return "", 0, s.errorf(len(s.orig), "unexpected end of input")
				}
				v, err := strconv.ParseUint(string(rest[2:6]), 16, 16)
				if err != nil {
					return "", 0, s.errorf(off, "invalid escape code %q in string", rest[:6])
				}
				rest = rest[6:]
				r := rune(v)
				if utf16.IsSurrogate(r) {
					if len(rest) < 6 {
						return "", 0, s.errorf(len(s.orig), "unexpected end of input")
					}
					v2, err := strconv.ParseUint(string(rest[2:6]), 16, 16)
					r = utf16.DecodeRune(r, rune(v2))
					if rest[0] != '\\' || rest[1] != 'u' || r == utf8.RuneError || err != nil {
						return "", 0, s.errorf(off, "invalid escape code %q in string", rest[:6])
					}
					rest = rest[6:]
				}
				out = utf8.AppendRune(out, r)
			default:
				return "", 0, s.errorf(off, "invalid escape code %q in string", rest[:2])
			}
		default:
			out, rest = append(out, rest[:n]...), rest[n:]
		}
	}
	return "", 0, s.errorf(len(s.orig), "unexpected end of input")
}

// numberInString reads a number written as a JSON string ("123", "1e3"):
// no surrounding whitespace, and it must start with a JSON number that ends
// at a delimiter; anything after that delimiter is ignored, as in
// protobuf-go.
func numberInString(str string) ([]byte, bool) {
	if str == "" || len(strings.TrimSpace(str)) != len(str) {
		return nil, false
	}
	b := []byte(str)
	if b[0] != '-' && !isDigit(b[0]) {
		return nil, false
	}
	n, ok := scanNumber(b)
	if !ok {
		return nil, false
	}
	return b[:n], true
}
