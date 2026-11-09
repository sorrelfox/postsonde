package lexer

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// StringValue decodes the text of one String token (quotes included) into
// its byte value. It fails on anything that does not lex as a single String
// token.
//
// Escapes: \a \b \f \n \r \t \v \\ \' \" \?; octal \o, \oo, \ooo (at most
// \377); hex \xh, \xhh (also \X); \uhhhh and \Uhhhhhhhh as UTF-8, where a
// \u high surrogate followed by a \u low surrogate is one code point and a
// lone surrogate is an error.
func StringValue(text string) (string, error) {
	if len(text) < 2 || (text[0] != '"' && text[0] != '\'') || text[len(text)-1] != text[0] {
		return "", errors.New("not a quoted string")
	}
	q := text[0]
	body := text[1 : len(text)-1]
	if !strings.ContainsAny(body, "\\\n\r"+string(q)) {
		return body, nil
	}
	buf := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		switch c := body[i]; c {
		case '\\':
			var n int
			var msg string
			buf, n, msg = unescape(body, i, buf)
			if msg != "" {
				return "", errors.New(msg)
			}
			i += n
		case q, '\n', '\r':
			return "", fmt.Errorf("unescaped %q in string", rune(c))
		default:
			buf = append(buf, c)
			i++
		}
	}
	return string(buf), nil
}

// unescape decodes the escape sequence at s[i] (a backslash), appending its
// value to buf. It returns the bytes consumed, at least 1, and a message for
// a malformed escape. It never consumes a line terminator.
func unescape(s string, i int, buf []byte) ([]byte, int, string) {
	if i+1 >= len(s) {
		return buf, 1, `"\" at end of string`
	}
	c := s[i+1]
	switch c {
	case 'a':
		return append(buf, '\a'), 2, ""
	case 'b':
		return append(buf, '\b'), 2, ""
	case 'f':
		return append(buf, '\f'), 2, ""
	case 'n':
		return append(buf, '\n'), 2, ""
	case 'r':
		return append(buf, '\r'), 2, ""
	case 't':
		return append(buf, '\t'), 2, ""
	case 'v':
		return append(buf, '\v'), 2, ""
	case '\\', '\'', '"', '?':
		return append(buf, c), 2, ""
	case 'x', 'X':
		n := hexRun(s, i+2, 2)
		if n == 0 {
			return buf, 2, `"\x" must be followed by hex digits`
		}
		v, _ := strconv.ParseUint(s[i+2:i+2+n], 16, 8)
		return append(buf, byte(v)), 2 + n, ""
	case 'u', 'U':
		want := 4
		if c == 'U' {
			want = 8
		}
		if hexRun(s, i+2, want) != want {
			return buf, 2, fmt.Sprintf(`"\%c" must be followed by %d hex digits`, c, want)
		}
		v, _ := strconv.ParseUint(s[i+2:i+2+want], 16, 32)
		r, n := rune(v), 2+want
		if c == 'u' && 0xD800 <= r && r < 0xDC00 && strings.HasPrefix(s[i+n:], `\u`) && hexRun(s, i+n+2, 4) == 4 {
			lo, _ := strconv.ParseUint(s[i+n+2:i+n+6], 16, 32)
			if 0xDC00 <= lo && lo < 0xE000 {
				r = 0x10000 + (r-0xD800)<<10 + rune(lo-0xDC00)
				n += 6
			}
		}
		switch {
		case 0xD800 <= r && r < 0xE000:
			return buf, n, fmt.Sprintf("unpaired surrogate %s in escape", s[i:i+n])
		case r > utf8.MaxRune:
			return buf, n, fmt.Sprintf("escape %s is beyond U+10FFFF", s[i:i+n])
		}
		return utf8.AppendRune(buf, r), n, ""
	}
	if isOctalDigit(c) {
		j := i + 1
		for j < len(s) && j < i+4 && isOctalDigit(s[j]) {
			j++
		}
		v, _ := strconv.ParseUint(s[i+1:j], 8, 16)
		if v > math.MaxUint8 {
			return buf, j - i, fmt.Sprintf("octal escape %s is above \\377", s[i:j])
		}
		return append(buf, byte(v)), j - i, ""
	}
	if c == '\n' || c == '\r' {
		return buf, 1, `"\" at end of line`
	}
	r, size := utf8.DecodeRuneInString(s[i+1:])
	return buf, 1 + size, fmt.Sprintf(`invalid escape sequence "\%c"`, r)
}

func hexRun(s string, i, limit int) int {
	n := 0
	for n < limit && i+n < len(s) && isHexDigit(s[i+n]) {
		n++
	}
	return n
}

// ErrIntRange is returned by IntValue for integers that do not fit in 64 bits.
var ErrIntRange = errors.New("integer out of range")

// IntValue decodes the text of an Int token: decimal, "0x" hex or leading-0
// octal. It returns ErrIntRange for values above math.MaxUint64.
func IntValue(text string) (uint64, error) {
	s, base := text, 10
	switch {
	case len(s) > 1 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X'):
		s, base = s[2:], 16
	case len(s) > 1 && s[0] == '0':
		s, base = s[1:], 8
	}
	if s == "" || strings.ContainsAny(s, "+-_") {
		return 0, fmt.Errorf("invalid integer %q", text)
	}
	v, err := strconv.ParseUint(s, base, 64)
	if errors.Is(err, strconv.ErrRange) {
		return 0, ErrIntRange
	}
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", text)
	}
	return v, nil
}

// FloatValue decodes the text of a Float token, or of an Int token used as a
// float (such as an integer too large for 64 bits). An "f" suffix is
// ignored; values beyond the float64 range become ±Inf, as in protoc.
func FloatValue(text string) (float64, error) {
	s := text
	isHex := len(s) > 1 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
	if n := len(s); !isHex && n > 0 && (s[n-1] == 'f' || s[n-1] == 'F') {
		s = s[:n-1]
	}
	switch {
	case s == "":
	case isHex || len(s) > 1 && s[0] == '0' && isOctalDigit(s[1]) && !strings.ContainsAny(s, ".eE"):
		base, digits := 8, s[1:]
		if isHex {
			base, digits = 16, s[2:]
		}
		if v, ok := new(big.Int).SetString(digits, base); ok && v.Sign() >= 0 {
			f, _ := new(big.Float).SetInt(v).Float64()
			return f, nil
		}
	case (isDigit(s[0]) || s[0] == '.') && !strings.ContainsAny(s, "_xXpPiInN"):
		v, err := strconv.ParseFloat(s, 64)
		if err == nil || errors.Is(err, strconv.ErrRange) {
			return v, nil
		}
	}
	return 0, fmt.Errorf("invalid number %q", text)
}
