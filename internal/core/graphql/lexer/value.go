package lexer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValueError reports why a string literal has no value.
type ValueError struct {
	Code    ErrCode
	Offset  int // byte offset in the literal
	Message string
}

func (e *ValueError) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Offset, e.Message)
}

// StringValue returns the value of a String or BlockString token's text
// (quotes included): escapes decoded for "...", the spec's BlockStringValue
// for """...""". It fails for text the lexer would not return as a String
// or BlockString token.
func StringValue(lit string) (string, error) {
	if strings.HasPrefix(lit, `"""`) {
		return blockLiteralValue(lit)
	}
	if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
		return "", &ValueError{ErrUnterminatedString, 0, "not a quoted string"}
	}
	body := lit[1 : len(lit)-1]
	if simpleString(body) {
		return body, nil
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case c == '\\':
			r, n, msg := readEscape(body, i)
			if msg != "" {
				return "", &ValueError{ErrInvalidEscape, i + 1, msg}
			}
			b.WriteRune(r)
			i += n
		case c == '"' || c == '\n' || c == '\r':
			return "", &ValueError{ErrUnterminatedString, i + 1, "unescaped quote or line terminator in string"}
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(body[i:])
			if r == utf8.RuneError && size == 1 {
				return "", &ValueError{ErrInvalidUTF8, i + 1, "invalid UTF-8 in string"}
			}
			b.WriteString(body[i : i+size])
			i += size
		}
	}
	return b.String(), nil
}

// simpleString reports whether body needs no decoding.
func simpleString(body string) bool {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\', '"', '\n', '\r':
			return false
		}
	}
	return utf8.ValidString(body)
}

func blockLiteralValue(lit string) (string, error) {
	if len(lit) < 6 || !strings.HasSuffix(lit, `"""`) {
		return "", &ValueError{ErrUnterminatedBlockString, 0, "unterminated block string"}
	}
	// The closing delimiter must be the first unescaped """, scanned as the
	// lexer does.
	end := -1
	for i := 3; i+3 <= len(lit); i++ {
		if strings.HasPrefix(lit[i:], `\"""`) {
			i += 3
		} else if strings.HasPrefix(lit[i:], `"""`) {
			end = i
			break
		}
	}
	if end != len(lit)-3 {
		return "", &ValueError{ErrUnterminatedBlockString, max(end, 0), `block string does not end at the first unescaped """`}
	}
	raw := lit[3:end]
	if !utf8.ValidString(raw) {
		return "", &ValueError{ErrInvalidUTF8, 3, "invalid UTF-8 in block string"}
	}
	return BlockStringValue(raw), nil
}

// BlockStringValue implements the spec's BlockStringValue(rawValue): raw is
// the text between the """ delimiters. It unescapes \""", removes the common
// indentation of all lines but the first, drops leading and trailing blank
// lines and joins the lines with "\n".
func BlockStringValue(raw string) string {
	raw = strings.ReplaceAll(raw, `\"""`, `"""`)
	lines := splitLines(raw)

	common := -1
	for _, line := range lines[1:] {
		indent := leadingWhitespace(line)
		if indent < len(line) && (common < 0 || indent < common) {
			common = indent
		}
	}
	if common > 0 {
		for i := 1; i < len(lines); i++ {
			lines[i] = lines[i][min(common, len(lines[i])):]
		}
	}
	first, last := 0, len(lines)
	for first < last && leadingWhitespace(lines[first]) == len(lines[first]) {
		first++
	}
	for last > first && leadingWhitespace(lines[last-1]) == len(lines[last-1]) {
		last--
	}
	return strings.Join(lines[first:last], "\n")
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			lines = append(lines, s[start:i])
			start = i + 1
		case '\r':
			lines = append(lines, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	return append(lines, s[start:])
}

// leadingWhitespace counts leading spaces and tabs; each is one byte.
func leadingWhitespace(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// readEscape reads the escape sequence at s[i] == '\\'. It returns the
// decoded rune and the bytes consumed (at least 1, never past a line
// terminator or a closing quote that is not part of the escape), or a
// non-empty message for an invalid sequence.
func readEscape(s string, i int) (r rune, n int, msg string) {
	if i+1 >= len(s) {
		return 0, 1, `unterminated escape sequence "\"`
	}
	switch c := s[i+1]; c {
	case '"', '\\', '/':
		return rune(c), 2, ""
	case 'b':
		return '\b', 2, ""
	case 'f':
		return '\f', 2, ""
	case 'n':
		return '\n', 2, ""
	case 'r':
		return '\r', 2, ""
	case 't':
		return '\t', 2, ""
	case 'u':
		if i+2 < len(s) && s[i+2] == '{' {
			return readBracedEscape(s, i)
		}
		v, ok := hex4(s, i+2)
		if !ok {
			n := 2
			for n < 6 && i+n < len(s) && isHex(s[i+n]) {
				n++
			}
			return 0, n, fmt.Sprintf("invalid Unicode escape sequence %q", s[i:i+n])
		}
		switch {
		case 0xD800 <= v && v <= 0xDBFF:
			if i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
				if t, ok := hex4(s, i+8); ok && 0xDC00 <= t && t <= 0xDFFF {
					return 0x10000 + (v-0xD800)<<10 + (t - 0xDC00), 12, ""
				}
			}
			return 0, 6, fmt.Sprintf("invalid Unicode escape sequence %q: lone leading surrogate", s[i:i+6])
		case 0xDC00 <= v && v <= 0xDFFF:
			return 0, 6, fmt.Sprintf("invalid Unicode escape sequence %q: lone trailing surrogate", s[i:i+6])
		}
		return v, 6, ""
	case '\n', '\r':
		return 0, 1, "invalid escape sequence: backslash at end of line"
	default:
		_, size := utf8.DecodeRuneInString(s[i+1:])
		return 0, 1 + size, fmt.Sprintf("invalid escape sequence %q", s[i:i+1+size])
	}
}

// readBracedEscape reads the draft's variable-width \u{...} escape.
func readBracedEscape(s string, i int) (rune, int, string) {
	j := i + 3
	var v rune
	for j < len(s) && isHex(s[j]) {
		if v <= utf8.MaxRune { // stop growing once out of range
			v = v<<4 | hexVal(s[j])
		}
		j++
	}
	if j == i+3 || j >= len(s) || s[j] != '}' {
		return 0, j - i, fmt.Sprintf("invalid Unicode escape sequence %q", s[i:j])
	}
	j++
	if v > utf8.MaxRune || 0xD800 <= v && v <= 0xDFFF {
		return 0, j - i, fmt.Sprintf("invalid Unicode escape sequence %q: not a Unicode scalar value", s[i:j])
	}
	return v, j - i, ""
}

func hex4(s string, i int) (rune, bool) {
	if i+4 > len(s) {
		return 0, false
	}
	var v rune
	for k := i; k < i+4; k++ {
		if !isHex(s[k]) {
			return 0, false
		}
		v = v<<4 | hexVal(s[k])
	}
	return v, true
}

func isHex(c byte) bool {
	return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexVal(c byte) rune {
	switch {
	case c <= '9':
		return rune(c - '0')
	case c >= 'a':
		return rune(c-'a') + 10
	default:
		return rune(c-'A') + 10
	}
}
