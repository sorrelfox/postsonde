package incremental

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// kind is the kind of a JSON value.
type kind uint8

const (
	kindLiteral kind = iota // a string, number, true, false or null, kept as received
	kindObject
	kindArray
)

// value is a JSON value that keeps what the server sent: member order,
// number digits and string escapes. Merging edits it in place.
type value struct {
	kind    kind
	raw     string // the literal's text
	members []member
	items   []*value
}

// member is an object member; name is decoded.
type member struct {
	name string
	// key is the name's text as received, quotes included.
	key string
	val *value
}

var errNotJSON = errors.New("not valid JSON")

// parseJSON reads one JSON value (surrounding whitespace allowed).
func parseJSON(src []byte) (*value, error) {
	if !json.Valid(src) {
		return nil, errNotJSON
	}
	p := jsonParser{src: src}
	v := p.value()
	return v, nil
}

// jsonParser reads text json.Valid accepted, so it checks nothing.
type jsonParser struct {
	src []byte
	pos int
}

func (p *jsonParser) skip() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) value() *value {
	p.skip()
	switch p.src[p.pos] {
	case '{':
		p.pos++
		v := &value{kind: kindObject}
		for {
			p.skip()
			if p.src[p.pos] == '}' {
				p.pos++
				return v
			}
			if p.src[p.pos] == ',' {
				p.pos++
				p.skip()
			}
			key := p.str()
			var name string
			_ = json.Unmarshal([]byte(key), &name)
			p.skip()
			p.pos++ // ':'
			v.members = append(v.members, member{name: name, key: key, val: p.value()})
		}
	case '[':
		p.pos++
		v := &value{kind: kindArray, items: []*value{}}
		for {
			p.skip()
			if p.src[p.pos] == ']' {
				p.pos++
				return v
			}
			if p.src[p.pos] == ',' {
				p.pos++
			}
			v.items = append(v.items, p.value())
		}
	case '"':
		return &value{raw: p.str()}
	default:
		start := p.pos
		for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n,]}", rune(p.src[p.pos])) {
			p.pos++
		}
		return &value{raw: string(p.src[start:p.pos])}
	}
}

// str returns the string at pos with its quotes.
func (p *jsonParser) str() string {
	start := p.pos
	p.pos++
	for p.src[p.pos] != '"' {
		if p.src[p.pos] == '\\' {
			p.pos++
		}
		p.pos++
	}
	p.pos++
	return string(p.src[start:p.pos])
}

// get returns the member name of an object, or nil.
func (v *value) get(name string) *value {
	if v == nil || v.kind != kindObject {
		return nil
	}
	for i := range v.members {
		if v.members[i].name == name {
			return v.members[i].val
		}
	}
	return nil
}

// set replaces the member name of an object, or adds it at the end.
func (v *value) set(name string, val *value) {
	for i := range v.members {
		if v.members[i].name == name {
			v.members[i].val = val
			return
		}
	}
	key, _ := json.Marshal(name)
	v.members = append(v.members, member{name: name, key: string(key), val: val})
}

// isNull reports whether v is JSON null (or missing).
func (v *value) isNull() bool {
	return v == nil || (v.kind == kindLiteral && v.raw == "null")
}

// str returns the decoded string of a string literal, and whether it is one.
func (v *value) string() (string, bool) {
	if v == nil || v.kind != kindLiteral || !strings.HasPrefix(v.raw, `"`) {
		return "", false
	}
	var s string
	if json.Unmarshal([]byte(v.raw), &s) != nil {
		return "", false
	}
	return s, true
}

// int returns a number literal that is a non-negative integer.
func (v *value) int() (int, bool) {
	if v == nil || v.kind != kindLiteral {
		return 0, false
	}
	n, err := strconv.Atoi(v.raw)
	return n, err == nil && n >= 0
}

// bool returns true or false.
func (v *value) bool() (b, ok bool) {
	if v == nil || v.kind != kindLiteral {
		return false, false
	}
	switch v.raw {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// clone copies v deeply, so merging never edits a payload's parts.
func (v *value) clone() *value {
	if v == nil {
		return nil
	}
	c := &value{kind: v.kind, raw: v.raw}
	if v.members != nil {
		c.members = make([]member, len(v.members))
		for i, m := range v.members {
			c.members[i] = member{name: m.name, key: m.key, val: m.val.clone()}
		}
	}
	if v.items != nil {
		c.items = make([]*value, len(v.items))
		for i, it := range v.items {
			c.items[i] = it.clone()
		}
	}
	return c
}

// write appends v's compact text to b.
func (v *value) write(b *bytes.Buffer) {
	switch v.kind {
	case kindObject:
		b.WriteByte('{')
		for i, m := range v.members {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(m.key)
			b.WriteByte(':')
			m.val.write(b)
		}
		b.WriteByte('}')
	case kindArray:
		b.WriteByte('[')
		for i, it := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			it.write(b)
		}
		b.WriteByte(']')
	default:
		b.WriteString(v.raw)
	}
}

// pretty returns v indented with two spaces, as jsonfmt.Beautify would.
func (v *value) pretty() string {
	var compact, out bytes.Buffer
	v.write(&compact)
	if json.Indent(&out, compact.Bytes(), "", "  ") != nil {
		return compact.String()
	}
	return out.String()
}

// prettyText indents JSON text with two spaces; other text is returned
// as it is.
func prettyText(src []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, bytes.TrimSpace(src), "", "  ") != nil {
		return string(src)
	}
	return out.String()
}
