package jsonpath

import (
	"context"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Path is a parsed JSONPath query. It is immutable and safe for
// concurrent use.
type Path struct {
	src string
	q   *query
}

// String returns the query text as given to Parse.
func (p *Path) String() string { return p.src }

// Singular reports whether the query is a singular query (only single
// name and index selectors), which selects at most one node.
func (p *Path) Singular() bool { return p.q.singular() }

// Limits bound the work of one evaluation.
type Limits struct {
	// MaxSteps bounds the work units of an evaluation: each node a
	// selector produces or visits, each filter test, each value compared
	// for equality and each 64 bytes (16 for regular expressions) of a
	// string examined count as one; compiling a pattern taken from the
	// document counts 128 per byte. 0 means no limit.
	MaxSteps int
}

// DefaultLimits is what QueryJSON uses: enough for any reasonable query
// on a multi-megabyte response, while stopping pathological ones (nested
// descendant segments and filters on large documents) within about a
// second.
var DefaultLimits = Limits{MaxSteps: 20_000_000}

// Node is one selected value and its location.
type Node struct {
	// Path is the normalized path of the value (RFC 9535 §2.7).
	Path NormalizedPath
	// Value is the value, in the document's own representation.
	Value any
}

// NodeList is the result of a query, in the order RFC 9535 defines
// (document order for arrays, and member order as decoded for objects).
type NodeList []Node

// Values returns the values of the nodes.
func (l NodeList) Values() []any {
	vs := make([]any, len(l))
	for i, n := range l {
		vs[i] = n.Value
	}
	return vs
}

// Paths returns the normalized paths of the nodes as strings.
func (l NodeList) Paths() []string {
	ps := make([]string, len(l))
	for i, n := range l {
		ps[i] = n.Path.String()
	}
	return ps
}

// PathElement is one step of a normalized path: a member name or an array
// index.
type PathElement struct {
	Name    string
	Index   int
	IsIndex bool
}

// NormalizedPath is the location of a node from the root.
type NormalizedPath []PathElement

// String formats the path as an RFC 9535 normalized path, e.g.
// $['store']['book'][0]: bracket notation only, names in single quotes
// with the minimal escapes (§2.7).
func (p NormalizedPath) String() string {
	var b strings.Builder
	b.WriteByte('$')
	for _, e := range p {
		b.WriteByte('[')
		if e.IsIndex {
			b.WriteString(strconv.Itoa(e.Index))
		} else {
			writeNormalName(&b, e.Name)
		}
		b.WriteByte(']')
	}
	return b.String()
}

func writeNormalName(b *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('\'')
}

// Query evaluates the query against doc with no work limit and returns
// the selected nodes. doc is a value from Decode, or plain Go JSON values
// ([]any, map[string]any with members in name order, string, bool, nil,
// json.Number, float64 and the other integer and float types). Use Eval
// for documents or queries that are not trusted to be small.
func (p *Path) Query(doc any) NodeList {
	l, _ := p.Eval(context.Background(), doc, Limits{})
	return l
}

// Eval evaluates the query against doc within lim. It stops when ctx is
// done (returning ctx's error, wrapped by errs) or when lim.MaxSteps is
// exceeded (errs.Unsupported with code jsonpath.too_complex); the node
// list is nil then.
func (p *Path) Eval(ctx context.Context, doc any, lim Limits) (NodeList, error) {
	e := &evaluator{root: doc, ctx: ctx, maxSteps: lim.MaxSteps}
	nodes, err := e.run(p.q)
	if err != nil {
		return nil, err
	}
	out := make(NodeList, len(nodes))
	for i, n := range nodes {
		out[i] = Node{Path: n.loc.path(), Value: n.v}
	}
	return out, nil
}

// QueryJSON parses expr, decodes data and evaluates the query within
// DefaultLimits. Errors are errs errors: InvalidArgument with code
// jsonpath.invalid_query, Parse with jsonpath.invalid_json, or Unsupported
// with jsonpath.too_complex.
func QueryJSON(expr string, data []byte) (NodeList, error) {
	p, err := Parse(expr)
	if err != nil {
		return nil, err.(*SyntaxError).Err()
	}
	doc, err := Decode(data)
	if err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The response is not valid JSON.").WithCode(CodeInvalidJSON)
	}
	return p.Eval(context.Background(), doc, DefaultLimits)
}
