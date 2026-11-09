package openapi

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// jsonError is a JSON document that could not be read, with the 1-based
// line and column (in characters) of the problem.
type jsonError struct {
	msg          string
	line, column int
	tooLarge     bool
}

// jsonParser reads JSON (RFC 8259) into a yaml.Node tree with positions,
// so JSON and YAML documents are walked by the same code. Duplicate keys
// are errors, as they are for YAML.
type jsonParser struct {
	src      []byte
	pos      int
	line     int
	lineAt   int // byte offset where the current line starts
	colPos   int // a byte offset on the current line whose column is colNum
	colNum   int
	depth    int
	nodes    int
	maxDepth int
	maxNodes int
	err      *jsonError
}

type jsonAbort struct{}

// parseJSON parses one JSON value that makes up the whole of src.
func parseJSON(src []byte, maxDepth, maxNodes int) (root *yaml.Node, jerr *jsonError) {
	p := &jsonParser{src: src, line: 1, colNum: 1, maxDepth: maxDepth, maxNodes: maxNodes}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(jsonAbort); !ok {
				panic(r)
			}
			root, jerr = nil, p.err
		}
	}()
	if !utf8.Valid(src) {
		p.fail(p.firstInvalidUTF8(), "the document is not valid UTF-8")
	}
	// A UTF-8 byte order mark is tolerated.
	if strings.HasPrefix(string(src), "\ufeff") {
		p.pos, p.lineAt, p.colPos = 3, 3, 3
	}
	p.space()
	root = p.value()
	p.space()
	if p.pos < len(p.src) {
		p.fail(p.pos, "unexpected text after the JSON value")
	}
	return root, nil
}

func (p *jsonParser) firstInvalidUTF8() int {
	for i := 0; i < len(p.src); {
		r, n := utf8.DecodeRune(p.src[i:])
		if r == utf8.RuneError && n == 1 {
			return i
		}
		i += n
	}
	return 0
}

// position returns the line and column of byte offset pos, which must be
// on the current line or later. Columns on long lines (minified JSON) are
// counted incrementally from the last position asked for.
func (p *jsonParser) position(pos int) (int, int) {
	if pos > len(p.src) {
		pos = len(p.src)
	}
	if p.colPos < p.lineAt || p.colPos > pos {
		p.colPos, p.colNum = p.lineAt, 1
	}
	line, at := p.line, -1
	for i := p.colPos; i < pos; i++ {
		if p.src[i] == '\n' {
			line++
			at = i + 1
		}
	}
	if at >= 0 {
		return line, utf8.RuneCount(p.src[at:pos]) + 1
	}
	p.colNum += utf8.RuneCount(p.src[p.colPos:pos])
	p.colPos = pos
	return line, p.colNum
}

func (p *jsonParser) fail(pos int, msg string) {
	l, c := p.position(pos)
	p.err = &jsonError{msg: msg, line: l, column: c}
	panic(jsonAbort{})
}

func (p *jsonParser) tooLarge(pos int, msg string) {
	l, c := p.position(pos)
	p.err = &jsonError{msg: msg, line: l, column: c, tooLarge: true}
	panic(jsonAbort{})
}

func (p *jsonParser) space() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\n':
			p.pos++
			p.line++
			p.lineAt = p.pos
		case ' ', '\t', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) newNode(kind yaml.Kind, tag string) *yaml.Node {
	p.nodes++
	if p.nodes > p.maxNodes {
		p.tooLarge(p.pos, "the document has more than "+strconv.Itoa(p.maxNodes)+" values")
	}
	l, c := p.position(p.pos)
	return &yaml.Node{Kind: kind, Tag: tag, Style: yaml.Flow, Line: l, Column: c}
}

func (p *jsonParser) value() *yaml.Node {
	if p.pos >= len(p.src) {
		p.fail(p.pos, "unexpected end of the document, expected a value")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		n := p.newNode(yaml.ScalarNode, yaml.TagStr)
		n.Style = yaml.DoubleQuoted
		n.Value = p.str()
		return n
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == 't':
		return p.literal("true", yaml.TagBool)
	case c == 'f':
		return p.literal("false", yaml.TagBool)
	case c == 'n':
		return p.literal("null", yaml.TagNull)
	default:
		p.fail(p.pos, "unexpected character "+strconv.QuoteRune(p.runeAt(p.pos))+", expected a value")
	}
	return nil
}

func (p *jsonParser) runeAt(pos int) rune {
	r, _ := utf8.DecodeRune(p.src[pos:])
	return r
}

func (p *jsonParser) literal(word, tag string) *yaml.Node {
	if !strings.HasPrefix(string(p.src[p.pos:]), word) {
		p.fail(p.pos, "invalid literal, expected "+word)
	}
	n := p.newNode(yaml.ScalarNode, tag)
	n.Style = yaml.Plain
	n.Value = word
	p.pos += len(word)
	return n
}

func (p *jsonParser) number() *yaml.Node {
	start := p.pos
	n := p.newNode(yaml.ScalarNode, yaml.TagInt)
	n.Style = yaml.Plain
	digits := func() int {
		s := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		return p.pos - s
	}
	if p.src[p.pos] == '-' {
		p.pos++
	}
	if p.pos < len(p.src) && p.src[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		p.fail(p.pos, "invalid number")
	}
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		p.pos++
		n.Tag = yaml.TagFloat
		if digits() == 0 {
			p.fail(p.pos, "invalid number: digits expected after the decimal point")
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		p.pos++
		n.Tag = yaml.TagFloat
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			p.fail(p.pos, "invalid number: digits expected in the exponent")
		}
	}
	n.Value = string(p.src[start:p.pos])
	return n
}

func (p *jsonParser) str() string {
	start := p.pos
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.pos >= len(p.src) {
			p.fail(start, "unterminated string")
		}
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String()
		case c == '\\':
			p.escape(&b)
		case c < 0x20:
			p.fail(p.pos, "control character in a string")
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

func (p *jsonParser) escape(b *strings.Builder) {
	at := p.pos
	p.pos++
	if p.pos >= len(p.src) {
		p.fail(at, "unterminated string")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		b.WriteByte(c)
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case 'u':
		r := p.hex4(at)
		if utf16.IsSurrogate(r) {
			if strings.HasPrefix(string(p.src[p.pos:]), `\u`) {
				save := p.pos
				p.pos += 2
				r2 := p.hex4(at)
				if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
					b.WriteRune(d)
					return
				}
				p.pos = save
			}
			r = utf8.RuneError
		}
		b.WriteRune(r)
	default:
		p.fail(at, "invalid escape sequence \\"+string(rune(c)))
	}
}

func (p *jsonParser) hex4(at int) rune {
	if p.pos+4 > len(p.src) {
		p.fail(at, "invalid \\u escape")
	}
	v, err := strconv.ParseUint(string(p.src[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		p.fail(at, "invalid \\u escape")
	}
	p.pos += 4
	return rune(v)
}

func (p *jsonParser) enter() {
	p.depth++
	if p.depth > p.maxDepth {
		p.tooLarge(p.pos, "the document is nested more than "+strconv.Itoa(p.maxDepth)+" levels deep")
	}
}

func (p *jsonParser) object() *yaml.Node {
	p.enter()
	n := p.newNode(yaml.MappingNode, yaml.TagMap)
	p.pos++
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		p.depth--
		return n
	}
	seen := map[string]bool{}
	for {
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			p.fail(p.pos, "expected a string key in the object")
		}
		kpos := p.pos
		k := p.newNode(yaml.ScalarNode, yaml.TagStr)
		k.Style = yaml.DoubleQuoted
		k.Value = p.str()
		if seen[k.Value] {
			p.fail(kpos, "duplicate key "+strconv.Quote(k.Value))
		}
		seen[k.Value] = true
		p.space()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			p.fail(p.pos, "expected ':' after the key")
		}
		p.pos++
		p.space()
		v := p.value()
		n.Pairs = append(n.Pairs, yaml.Pair{Key: k, Value: v})
		p.space()
		if p.pos >= len(p.src) {
			p.fail(p.pos, "unterminated object, expected ',' or '}'")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			p.space()
		case '}':
			p.pos++
			p.depth--
			return n
		default:
			p.fail(p.pos, "expected ',' or '}' in the object")
		}
	}
}

func (p *jsonParser) array() *yaml.Node {
	p.enter()
	n := p.newNode(yaml.SequenceNode, yaml.TagSeq)
	p.pos++
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		p.depth--
		return n
	}
	for {
		n.Items = append(n.Items, p.value())
		p.space()
		if p.pos >= len(p.src) {
			p.fail(p.pos, "unterminated array, expected ',' or ']'")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			p.space()
		case ']':
			p.pos++
			p.depth--
			return n
		default:
			p.fail(p.pos, "expected ',' or ']' in the array")
		}
	}
}
