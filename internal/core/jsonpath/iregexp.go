package jsonpath

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxRegexpLength is the longest pattern match() and search() compile.
// Longer patterns do not match anything.
const MaxRegexpLength = 4096

var errIRegexp = errors.New("not a valid I-Regexp")

// compileIRegexp checks that pat is an I-Regexp (RFC 9485) and compiles
// it as a Go (RE2) regular expression: anchored at both ends for
// match(), unanchored for search().
//
// The translation: '.' becomes [^\n\r]; literal characters are written as
// \x{...}; \p{..} and \P{..} use Go's Unicode tables (the Unicode version
// of the Go release). '^' and '$' outside character classes are anchors (start and
// end of the string), as in the JSONPath Compliance Test Suite and the
// ECMAScript mapping, although RFC 9485's grammar lists them as ordinary
// characters. Anything RE2 cannot compile (for example a repetition count
// over 1000) makes the pattern invalid.
func compileIRegexp(pat string, full bool) (*regexp.Regexp, error) {
	if len(pat) > MaxRegexpLength || !utf8.ValidString(pat) {
		return nil, errIRegexp
	}
	t := &reTranslator{src: pat}
	if !t.regexp() || t.pos != len(pat) {
		return nil, errIRegexp
	}
	out := t.out.String()
	if full {
		out = `^(?:` + out + `)$`
	}
	re, err := regexp.Compile(out)
	if err != nil {
		return nil, errIRegexp
	}
	return re, nil
}

type reTranslator struct {
	src   string
	pos   int
	depth int
	out   strings.Builder
}

func (t *reTranslator) peek() rune {
	if t.pos >= len(t.src) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(t.src[t.pos:])
	return r
}

func (t *reTranslator) next() rune {
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	t.pos += size
	return r
}

// regexp: branch *( "|" branch )
func (t *reTranslator) regexp() bool {
	for {
		if !t.branch() {
			return false
		}
		if t.peek() != '|' {
			return true
		}
		t.next()
		t.out.WriteByte('|')
	}
}

// branch: *piece
func (t *reTranslator) branch() bool {
	for {
		switch t.peek() {
		case -1, '|', ')':
			return true
		}
		if !t.piece() {
			return false
		}
	}
}

// piece: atom [ quantifier ]
func (t *reTranslator) piece() bool {
	switch r := t.peek(); r {
	case '^', '$':
		t.next()
		t.out.WriteRune(r)
		switch t.peek() {
		case '*', '+', '?', '{':
			return false
		}
		return true
	}
	if !t.atom() {
		return false
	}
	switch t.peek() {
	case '*', '+', '?':
		t.out.WriteRune(t.next())
	case '{':
		return t.rangeQuantifier()
	}
	return true
}

// rangeQuantifier: "{" QuantExact [ "," [ QuantExact ] ] "}"
func (t *reTranslator) rangeQuantifier() bool {
	t.next()
	lo, ok := t.quantExact()
	if !ok {
		return false
	}
	t.out.WriteString("{" + strconv.Itoa(lo))
	if t.peek() == ',' {
		t.next()
		t.out.WriteByte(',')
		if t.peek() != '}' {
			hi, ok := t.quantExact()
			if !ok || hi < lo {
				return false
			}
			t.out.WriteString(strconv.Itoa(hi))
		}
	}
	if t.peek() != '}' {
		return false
	}
	t.next()
	t.out.WriteByte('}')
	return true
}

func (t *reTranslator) quantExact() (int, bool) {
	start := t.pos
	for t.pos < len(t.src) && isDigit(t.src[t.pos]) {
		t.pos++
	}
	if t.pos == start || t.pos-start > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(t.src[start:t.pos])
	return n, err == nil
}

// atom: NormalChar / charClass / "(" i-regexp ")"
func (t *reTranslator) atom() bool {
	switch r := t.peek(); r {
	case '(':
		t.depth++
		if t.depth > MaxNesting {
			return false
		}
		t.next()
		t.out.WriteString("(?:")
		if !t.regexp() || t.peek() != ')' {
			return false
		}
		t.next()
		t.out.WriteByte(')')
		t.depth--
		return true
	case '.':
		t.next()
		t.out.WriteString(`[^\n\r]`)
		return true
	case '[':
		return t.classExpr()
	case '\\':
		t.next()
		if r, ok := t.singleCharEsc(); ok {
			writeLiteral(&t.out, r)
			return true
		}
		content, ok := t.classEsc()
		if !ok {
			return false
		}
		t.out.WriteString("[" + content + "]")
		return true
	case ')', '*', '+', '?', '{', '}', ']', '|':
		return false
	default:
		writeLiteral(&t.out, t.next())
		return true
	}
}

// singleCharEsc parses the character after '\' of a SingleCharEsc.
func (t *reTranslator) singleCharEsc() (rune, bool) {
	switch r := t.peek(); r {
	case '(', ')', '*', '+', '-', '.', '?', '[', '\\', ']', '^', '{', '|', '}':
		t.next()
		return r, true
	case 'n':
		t.next()
		return '\n', true
	case 'r':
		t.next()
		return '\r', true
	case 't':
		t.next()
		return '\t', true
	}
	return 0, false
}

// classEsc parses the rest of \p{..} or \P{..} after '\' and returns the
// content for a Go character class.
func (t *reTranslator) classEsc() (string, bool) {
	neg := false
	switch t.peek() {
	case 'p':
	case 'P':
		neg = true
	default:
		return "", false
	}
	t.next()
	if t.peek() != '{' {
		return "", false
	}
	t.next()
	end := strings.IndexByte(t.src[t.pos:], '}')
	if end < 0 {
		return "", false
	}
	name := t.src[t.pos : t.pos+end]
	t.pos += end + 1
	return categoryClass(name, neg)
}

// iRegexpCategories are the category names RFC 9485 allows.
var iRegexpCategories = map[string]bool{
	"L": true, "Ll": true, "Lm": true, "Lo": true, "Lt": true, "Lu": true,
	"M": true, "Mc": true, "Me": true, "Mn": true,
	"N": true, "Nd": true, "Nl": true, "No": true,
	"P": true, "Pc": true, "Pd": true, "Pe": true, "Pf": true, "Pi": true, "Po": true, "Ps": true,
	"Z": true, "Zl": true, "Zp": true, "Zs": true,
	"S": true, "Sc": true, "Sk": true, "Sm": true, "So": true,
	"C": true, "Cc": true, "Cf": true, "Cn": true, "Co": true,
}

// categoryClass returns Go character class content for \p{name} (or
// \P{name} when neg). Go's Unicode tables have every category I-Regexp
// allows, Cn (unassigned) included, and C includes Cn as in Unicode.
func categoryClass(name string, neg bool) (string, bool) {
	if !iRegexpCategories[name] {
		return "", false
	}
	if neg {
		return `\P{` + name + `}`, true
	}
	return `\p{` + name + `}`, true
}

// classExpr: "[" [ "^" ] ( "-" / CCE1 ) *CCE1 [ "-" ] "]"
func (t *reTranslator) classExpr() bool {
	t.next() // '['
	t.out.WriteByte('[')
	if t.peek() == '^' {
		t.next()
		t.out.WriteByte('^')
	}
	items := 0
	if t.peek() == '-' {
		t.next()
		writeLiteral(&t.out, '-')
		items++
	}
	for {
		switch t.peek() {
		case -1, '[':
			return false
		case ']':
			if items == 0 {
				return false
			}
			t.next()
			t.out.WriteByte(']')
			return true
		case '-':
			t.next()
			if t.peek() != ']' || items == 0 {
				return false
			}
			writeLiteral(&t.out, '-')
			items++
			continue
		}
		items++
		if t.peek() == '\\' && t.pos+1 < len(t.src) && (t.src[t.pos+1] == 'p' || t.src[t.pos+1] == 'P') {
			t.next()
			content, ok := t.classEsc()
			if !ok {
				return false
			}
			t.out.WriteString(content)
			continue
		}
		lo, ok := t.classChar()
		if !ok {
			return false
		}
		// A range, unless the '-' is the last character before ']'.
		if t.peek() == '-' && t.pos+1 < len(t.src) && t.src[t.pos+1] != ']' {
			t.next()
			hi, ok := t.classChar()
			if !ok || hi < lo {
				return false
			}
			writeLiteral(&t.out, lo)
			t.out.WriteByte('-')
			writeLiteral(&t.out, hi)
			continue
		}
		writeLiteral(&t.out, lo)
	}
}

// classChar: CCchar / SingleCharEsc
func (t *reTranslator) classChar() (rune, bool) {
	switch r := t.peek(); r {
	case -1, '-', '[', ']':
		return 0, false
	case '\\':
		t.next()
		return t.singleCharEsc()
	default:
		return t.next(), true
	}
}

// writeLiteral writes r so that RE2 reads it as that character, also
// inside a character class.
func writeLiteral(b *strings.Builder, r rune) {
	if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
		b.WriteRune(r)
		return
	}
	b.WriteString(`\x{`)
	b.WriteString(strconv.FormatInt(int64(r), 16))
	b.WriteByte('}')
}
