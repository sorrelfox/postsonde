package template

import "strings"

// SegmentKind is the kind of a Segment.
type SegmentKind uint8

const (
	// Text is literal text, with escapes already applied.
	Text SegmentKind = iota + 1
	// Var is a {{name}} reference.
	Var
	// Unterminated is a "{{" that has no matching "}}". It renders as a
	// literal "{{" and is reported as a warning.
	Unterminated
)

func (k SegmentKind) String() string {
	switch k {
	case Text:
		return "text"
	case Var:
		return "var"
	case Unterminated:
		return "unterminated"
	}
	return "invalid"
}

// Segment is one piece of a parsed text. Offsets are byte offsets into the
// source passed to Parse; src[Start:End] is the raw text of the segment.
// Segments cover the source without gaps or overlaps, in order.
type Segment struct {
	Kind       SegmentKind
	Start, End int
	// Text is the literal text for Text (escapes applied) and "{{" for
	// Unterminated. It is empty for Var.
	Text string
	// Name is the variable name for Var, without the surrounding
	// whitespace. It may be empty ({{ }}).
	Name string
	// NameStart and NameEnd locate Name in the source (Var only).
	NameStart, NameEnd int
}

// Parse splits src into segments. It never fails: anything that is not a
// well-formed variable is text, and unterminated braces get their own
// segment so the editor can mark them.
func Parse(src string) []Segment {
	p := parser{src: src}
	p.parse()
	return p.segs
}

type parser struct {
	src      string
	segs     []Segment
	lit      strings.Builder
	litStart int
}

func (p *parser) parse() {
	src := p.src
	i := 0
	for i < len(src) {
		j := strings.IndexAny(src[i:], `\{`)
		if j < 0 {
			p.lit.WriteString(src[i:])
			break
		}
		p.lit.WriteString(src[i : i+j])
		i += j

		if src[i] == '\\' {
			n := backslashRun(src[i:])
			if !strings.HasPrefix(src[i+n:], "{{") {
				p.lit.WriteString(src[i : i+n])
				i += n
				continue
			}
			p.lit.WriteString(strings.Repeat(`\`, n/2))
			if n%2 == 1 {
				p.lit.WriteString("{{")
				i += n + 2
			} else {
				i += n // the "{{" opens a variable
			}
			continue
		}

		// src[i] == '{'
		if !strings.HasPrefix(src[i:], "{{") {
			p.lit.WriteByte('{')
			i++
			continue
		}
		if i+2 < len(src) && src[i+2] == '{' {
			// The last two braces of a run open the variable.
			p.lit.WriteByte('{')
			i++
			continue
		}
		rest := src[i+2:]
		end := strings.Index(rest, "}}")
		if end < 0 || strings.Contains(rest[:end], "{{") {
			p.flush(i)
			p.segs = append(p.segs, Segment{Kind: Unterminated, Start: i, End: i + 2, Text: "{{"})
			i += 2
			p.litStart = i
			continue
		}
		inner := rest[:end]
		lead := len(inner) - len(strings.TrimLeft(inner, spaces))
		name := strings.TrimRight(inner[lead:], spaces)
		p.flush(i)
		p.segs = append(p.segs, Segment{
			Kind:      Var,
			Start:     i,
			End:       i + 2 + end + 2,
			Name:      name,
			NameStart: i + 2 + lead,
			NameEnd:   i + 2 + lead + len(name),
		})
		i += 2 + end + 2
		p.litStart = i
	}
	p.flush(len(src))
}

const spaces = " \t\r\n"

// flush ends the current text segment at end.
func (p *parser) flush(end int) {
	if end > p.litStart {
		p.segs = append(p.segs, Segment{Kind: Text, Start: p.litStart, End: end, Text: p.lit.String()})
	}
	p.lit.Reset()
	p.litStart = end
}

func backslashRun(s string) int {
	n := 0
	for n < len(s) && s[n] == '\\' {
		n++
	}
	return n
}

// Escape returns a text that Parse reads as a single literal equal to s:
// every "{{" gets a backslash, and backslashes right before it are doubled.
// Use it to insert a value into a template without it being interpreted.
func Escape(s string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	i := 0
	for i < len(s) {
		switch {
		case s[i] == '\\':
			n := backslashRun(s[i:])
			if strings.HasPrefix(s[i+n:], "{{") {
				b.WriteString(strings.Repeat(`\`, 2*n+1))
				b.WriteString("{{")
				i += n + 2
			} else {
				b.WriteString(s[i : i+n])
				i += n
			}
		case strings.HasPrefix(s[i:], "{{"):
			b.WriteString(`\{{`)
			i += 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
