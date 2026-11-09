package harimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// maxDepth is the deepest nesting of a JSON export this reader follows.
const maxDepth = 512

// errDepth is returned for a JSON document nested deeper than maxDepth.
var errDepth = errors.New("the JSON is nested too deeply")

// The JSON reader is the Insomnia and Postman importers': it reads a
// document into the node tree internal/core/yaml makes, with the line and
// column of every value, so every item's Source has its place in the
// file.

// parseJSON reads a JSON document into the node tree the YAML reader
// makes, with the line and column (1-based, in characters) where each
// value starts. Keys keep their order.
func parseJSON(data []byte) (*yaml.Node, error) {
	p := &jsonParser{data: data, dec: json.NewDecoder(bytes.NewReader(data)), lines: lineStarts(data)}
	p.dec.UseNumber()
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if _, err := p.dec.Token(); err != io.EOF {
		return nil, &jsonError{msg: "unexpected data after the document", line: p.lineAt(p.next())}
	}
	return n, nil
}

type jsonError struct {
	msg  string
	line int
}

func (e *jsonError) Error() string { return e.msg }

type jsonParser struct {
	data  []byte
	dec   *json.Decoder
	lines []int
}

// next returns the offset of the next token: the decoder's offset with
// whitespace, commas and colons skipped.
func (p *jsonParser) next() int {
	off := int(p.dec.InputOffset())
	for off < len(p.data) {
		switch p.data[off] {
		case ' ', '\t', '\r', '\n', ',', ':':
			off++
			continue
		}
		break
	}
	return off
}

func (p *jsonParser) fail(err error, off int) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		off = int(se.Offset)
	}
	msg := err.Error()
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		msg = "the JSON ends too early"
	}
	return &jsonError{msg: strings.TrimPrefix(msg, "json: "), line: p.lineAt(off)}
}

func (p *jsonParser) value(depth int) (*yaml.Node, error) {
	if depth > maxDepth {
		return nil, &jsonError{msg: errDepth.Error(), line: p.lineAt(p.next())}
	}
	off := p.next()
	tok, err := p.dec.Token()
	if err != nil {
		return nil, p.fail(err, off)
	}
	line, col := p.pos(off)
	n := &yaml.Node{Line: line, Column: col}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n.Kind, n.Tag = yaml.MappingNode, yaml.TagMap
			for p.dec.More() {
				koff := p.next()
				ktok, err := p.dec.Token()
				if err != nil {
					return nil, p.fail(err, koff)
				}
				key, _ := ktok.(string)
				kl, kc := p.pos(koff)
				v, err := p.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.Pairs = append(n.Pairs, yaml.Pair{
					Key:   &yaml.Node{Kind: yaml.ScalarNode, Tag: yaml.TagStr, Value: key, Line: kl, Column: kc},
					Value: v,
				})
			}
		case '[':
			n.Kind, n.Tag = yaml.SequenceNode, yaml.TagSeq
			for p.dec.More() {
				v, err := p.value(depth + 1)
				if err != nil {
					return nil, err
				}
				n.Items = append(n.Items, v)
			}
		default:
			return nil, &jsonError{msg: "unexpected " + t.String(), line: line}
		}
		end := p.next()
		if _, err := p.dec.Token(); err != nil {
			return nil, p.fail(err, end)
		}
	case string:
		n.Kind, n.Tag, n.Value = yaml.ScalarNode, yaml.TagStr, t
	case json.Number:
		n.Kind, n.Value = yaml.ScalarNode, t.String()
		n.Tag = yaml.TagInt
		if strings.ContainsAny(n.Value, ".eE") {
			n.Tag = yaml.TagFloat
		}
	case bool:
		n.Kind, n.Tag, n.Value = yaml.ScalarNode, yaml.TagBool, strconv.FormatBool(t)
	case nil:
		n.Kind, n.Tag, n.Value = yaml.ScalarNode, yaml.TagNull, "null"
	}
	return n, nil
}

func lineStarts(data []byte) []int {
	starts := []int{0}
	for i, c := range data {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func (p *jsonParser) lineAt(off int) int {
	l, _ := p.pos(off)
	return l
}

// pos returns the 1-based line and column (in characters) of a byte offset.
func (p *jsonParser) pos(off int) (int, int) {
	off = max(0, min(off, len(p.data)))
	lo, hi := 0, len(p.lines)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if p.lines[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1, utf8.RuneCount(p.data[p.lines[lo]:off]) + 1
}

// Node helpers. They resolve aliases and treat missing members as empty.

// get returns member key of a mapping, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	return n.Get(key).Resolve()
}

// path returns the member at a path of keys, or nil.
func path(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		n = get(n, k)
	}
	return n
}

// str returns a scalar's text; "" for null, missing and collections.
func str(n *yaml.Node) string {
	n = n.Resolve()
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == yaml.TagNull {
		return ""
	}
	return n.Value
}

// sget returns member key of a mapping as text.
func sget(n *yaml.Node, key string) string { return str(get(n, key)) }

// items returns a sequence's items; nil for anything else.
func items(n *yaml.Node) []*yaml.Node {
	n = n.Resolve()
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Items
}

// isMap reports whether n is a mapping.
func isMap(n *yaml.Node) bool {
	n = n.Resolve()
	return n != nil && n.Kind == yaml.MappingNode
}
