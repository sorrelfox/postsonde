package xmldom

import (
	"bytes"
	"context"
	"strings"
)

// Indent pretty-prints a well-formed XML document that is UTF-8 text
// already (a response body decoded with its charset; the encoding
// declaration is kept but not used): one element, comment, processing
// instruction or DOCTYPE per line, nested elements indented by indent per
// level. The markup itself is copied from the source as written: tags
// with their attributes, quotes and entity references, comments, CDATA
// sections. Whitespace-only text between elements is replaced by the
// indentation; an element that holds any other text (text only, or mixed
// content) is copied as it is, on one line or several, so no significant
// whitespace is added or removed.
//
// The error is Parse's: a *SyntaxError or *LimitError (convert with
// AsErr), or ctx's error.
func Indent(ctx context.Context, text []byte, indent string) (string, error) {
	doc, err := ParseOptions(ctx, text, Options{Decoded: true})
	if err != nil {
		return "", err
	}
	w := indenter{src: doc.Source, indent: indent}
	w.b.Grow(len(doc.Source) + len(doc.Source)/4)
	if decl := xmlDecl(doc.Source); decl != "" {
		w.line(0, decl)
	}
	for _, n := range doc.Root.Children {
		w.node(n, 0)
	}
	return w.b.String(), nil
}

type indenter struct {
	src    []byte
	indent string
	b      strings.Builder
}

func (w *indenter) line(depth int, s string) {
	if w.b.Len() > 0 {
		w.b.WriteByte('\n')
	}
	for range depth {
		w.b.WriteString(w.indent)
	}
	w.b.WriteString(s)
}

func (w *indenter) node(n *Node, depth int) {
	if n.Type == TextNode && !n.CDATA && isBlank(w.src[n.Offset:n.End]) {
		return
	}
	if n.Type != ElementNode || len(n.Children) == 0 || hasText(n) {
		w.line(depth, strings.TrimSpace(string(w.src[n.Offset:n.End])))
		return
	}
	first, last := n.Children[0], n.Children[len(n.Children)-1]
	w.line(depth, string(w.src[n.Offset:first.Offset]))
	for _, c := range n.Children {
		w.node(c, depth+1)
	}
	w.line(depth, string(w.src[last.End:n.End]))
}

// hasText reports whether an element holds text other than whitespace,
// or a CDATA section.
func hasText(n *Node) bool {
	for _, c := range n.Children {
		if c.Type == TextNode && (c.CDATA || strings.TrimSpace(c.Value) != "") {
			return true
		}
	}
	return false
}

// xmlDecl returns the XML declaration at the start of src, or "".
func xmlDecl(src []byte) string {
	if len(src) < 6 || !bytes.HasPrefix(src, []byte("<?xml")) || !isSpace(src[5]) {
		return ""
	}
	end := bytes.Index(src, []byte("?>"))
	if end < 0 {
		return ""
	}
	return string(src[:end+2])
}

// IndentNode pretty-prints one node of d as Indent does a whole document:
// an element with its content (one element per line, nested elements
// indented, text and mixed content copied as written), a comment or
// processing instruction as written, an attribute as its markup
// (name="value"), a text node as its text with entity references
// replaced, the document as all its children. It is how a filter result
// (a node-set) is shown; an element's namespace declarations on its
// ancestors are not repeated, so the markup may use prefixes it does not
// declare.
func (d *Document) IndentNode(n *Node, indent string) string {
	w := indenter{src: d.Source, indent: indent}
	switch n.Type {
	case DocumentNode:
		for _, c := range n.Children {
			w.node(c, 0)
		}
	case AttributeNode:
		return string(d.Source[n.Offset:n.End])
	case TextNode:
		return n.Value
	default:
		w.node(n, 0)
	}
	return w.b.String()
}
