// Package jsonfmt validates, beautifies and minifies JSON text typed in an
// editor (HTTP-12): request bodies first, and any other JSON editor later.
//
// It works on the bytes with encoding/json's scanner (json.Indent and
// json.Compact), never by decoding and encoding again, so number literals
// (1.50e+3, 12345678901234567890), string escapes and member order are kept
// exactly. Only whitespace outside strings changes.
//
// {{variables}} (package template) are allowed where a JSON value, or part
// of a string or number, can be: {"id": {{userId}}, "name": "{{name}}"}.
// Before scanning, each variable is replaced by a run of '1' digits of the
// same length, which is valid both as a number and inside a string, so byte
// offsets do not move; afterwards the variable's original text is put back,
// untouched. A variable where no value can be ({{key}}: 1) is an error that
// names the variable.
//
// Errors carry the position of the offending character as a byte offset,
// a UTF-16 offset (CodeMirror and JavaScript strings), and a 1-based line
// and column (the column counted in UTF-16 code units). See
// docs/json-tools.md.
package jsonfmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/template"
)

// MaxIndent is the largest indent width Beautify accepts, in spaces.
const MaxIndent = 8

// SyntaxError is invalid JSON at a position of the source.
type SyntaxError struct {
	// Offset is the byte offset of the offending character, or len(src)
	// when the text ends too early.
	Offset int
	// Offset16 is Offset in UTF-16 code units.
	Offset16 int
	// Line and Column are 1-based; Column counts UTF-16 code units. "\n",
	// "\r\n" and a lone "\r" end a line, as in CodeMirror.
	Line, Column int
	// Message says what is wrong, in encoding/json's words with the first
	// letter capitalized ("Invalid character '}' looking for beginning of
	// value"). A variable in the way is named ("Unexpected variable
	// {{id}} after object key").
	Message string
}

func (e *SyntaxError) Error() string { return e.Message }

// Validate reports whether src is one valid JSON value, allowing
// {{variables}}. It returns nil for valid JSON. Blank text is an error
// ("Unexpected end of JSON input" at offset 0 or the end).
func Validate(src string) *SyntaxError {
	masked, vars := mask(src)
	var buf bytes.Buffer
	// Compact scans the whole input like json.Valid but also reports the
	// offset; the output is discarded.
	if err := json.Compact(&buf, masked); err != nil {
		return syntaxError(src, vars, err)
	}
	return nil
}

// Beautify indents src with indent spaces per level (a tab when indent is
// 0), one member or element per line; empty objects and arrays stay "{}"
// and "[]". Leading and trailing whitespace is dropped. Invalid JSON is not
// changed: it returns the error instead. indent is clamped to 0..MaxIndent.
func Beautify(src string, indent int) (string, *SyntaxError) {
	unit := "\t"
	if indent > 0 {
		unit = strings.Repeat(" ", min(indent, MaxIndent))
	}
	masked, vars := mask(src)
	var buf bytes.Buffer
	if err := json.Indent(&buf, masked, "", unit); err != nil {
		return "", syntaxError(src, vars, err)
	}
	// json.Indent keeps trailing whitespace; beautified text ends at the value.
	out := bytes.TrimRight(buf.Bytes(), " \t\r\n")
	return unmask(src, masked, out, vars), nil
}

// Minify removes all whitespace outside strings. Invalid JSON is not
// changed: it returns the error instead.
func Minify(src string) (string, *SyntaxError) {
	masked, vars := mask(src)
	var buf bytes.Buffer
	if err := json.Compact(&buf, masked); err != nil {
		return "", syntaxError(src, vars, err)
	}
	return unmask(src, masked, buf.Bytes(), vars), nil
}

// span is the byte range [start, end) of a variable in the source.
type span struct{ start, end int }

// maskDigit fills variables: a digit run is a valid number and valid string
// content, and cannot contain whitespace the formatters would touch.
const maskDigit = '1'

// mask returns src with every {{variable}} replaced by maskDigit characters
// of the same length, and the variables' spans in order.
func mask(src string) ([]byte, []span) {
	b := []byte(src)
	if !strings.Contains(src, "{{") {
		return b, nil
	}
	var vars []span
	for _, seg := range template.Parse(src) {
		if seg.Kind != template.Var {
			continue
		}
		for i := seg.Start; i < seg.End; i++ {
			b[i] = maskDigit
		}
		vars = append(vars, span{seg.Start, seg.End})
	}
	return b, vars
}

// unmask puts the variables of src back into out, the formatted form of
// masked. The formatters copy every byte except whitespace outside strings
// in order, so the n-th significant byte of masked is the n-th significant
// byte of out; each variable is found by walking both texts together.
func unmask(src string, masked, out []byte, vars []span) string {
	if len(vars) == 0 {
		return string(out)
	}
	in, res := significant{text: masked}, significant{text: out}
	starts := make([]int, 0, len(vars)) // where each variable starts in out
	for len(starts) < len(vars) {
		i, o := in.next(), res.next()
		if i >= len(masked) || o >= len(out) {
			break // cannot happen for formatter output; keep what was found
		}
		if i == vars[len(starts)].start {
			starts = append(starts, o)
		}
	}
	// Copy only after the walk: the variables' own spaces must not be
	// mistaken for formatting whitespace.
	for n, at := range starts {
		v := vars[n]
		copy(out[at:at+v.end-v.start], src[v.start:v.end])
	}
	return string(out)
}

// significant walks the bytes of JSON text that the formatters copy: all
// bytes except whitespace outside strings.
type significant struct {
	text []byte
	pos  int // next byte to look at
	// inString and escaped track string literals, where whitespace counts.
	inString bool
	escaped  bool
}

// next returns the offset of the next significant byte and moves past it.
// It returns len(text) at the end.
func (s *significant) next() int {
	for s.pos < len(s.text) {
		c := s.text[s.pos]
		at := s.pos
		s.pos++
		switch {
		case s.inString:
			switch {
			case s.escaped:
				s.escaped = false
			case c == '\\':
				s.escaped = true
			case c == '"':
				s.inString = false
			}
			return at
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c == '"':
			s.inString = true
		}
		return at
	}
	return len(s.text)
}

// syntaxError turns a json.SyntaxError on the masked text into a
// SyntaxError on src.
func syntaxError(src string, vars []span, err error) *SyntaxError {
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		return position(src, len(src), capitalize(err.Error()))
	}
	msg := se.Error()
	// The scanner reports the offset just after the character it rejected
	// (a whole UTF-8 sequence); at the end of the input, the input's length.
	off := min(max(int(se.Offset), 0), len(src))
	if !strings.HasPrefix(msg, "unexpected end") && off > 0 {
		_, width := utf8.DecodeLastRuneInString(src[:off])
		off -= width
	}

	for _, v := range vars {
		if off >= v.start && off < v.end {
			msg = strings.Replace(msg, "invalid character '1'", "unexpected variable "+src[v.start:v.end], 1)
			off = v.start
			break
		}
	}
	return position(src, off, capitalize(msg))
}

// position builds a SyntaxError at byte offset off of src.
func position(src string, off int, msg string) *SyntaxError {
	e := &SyntaxError{Offset: off, Line: 1, Column: 1, Message: msg}
	lineStart16 := 0
	for i := 0; i < off; {
		c := src[i]
		switch {
		case c == '\n' || (c == '\r' && (i+1 >= len(src) || src[i+1] != '\n')):
			e.Offset16++
			i++
			e.Line++
			lineStart16 = e.Offset16
		case c < utf8.RuneSelf:
			e.Offset16++
			i++
		default:
			r, w := utf8.DecodeRuneInString(src[i:])
			e.Offset16 += utf16Len(r)
			i += w
		}
	}
	e.Column = e.Offset16 - lineStart16 + 1
	return e
}

// utf16Len is the number of UTF-16 code units of r; an invalid byte counts
// as one (it decodes to U+FFFD in JavaScript).
func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
