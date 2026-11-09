package xmldom

import (
	"sort"
	"strconv"
	"strings"
)

// XMLNamespace is the namespace URI always bound to the prefix "xml".
const XMLNamespace = "http://www.w3.org/XML/1998/namespace"

// NodeType is the kind of a Node.
type NodeType uint8

// Node types.
const (
	DocumentNode NodeType = iota + 1
	ElementNode
	AttributeNode
	TextNode // text and CDATA sections
	CommentNode
	ProcInstNode
	DoctypeNode
)

// String returns "document", "element", "attribute", "text", "comment",
// "processing-instruction" or "doctype".
func (t NodeType) String() string {
	switch t {
	case DocumentNode:
		return "document"
	case ElementNode:
		return "element"
	case AttributeNode:
		return "attribute"
	case TextNode:
		return "text"
	case CommentNode:
		return "comment"
	case ProcInstNode:
		return "processing-instruction"
	case DoctypeNode:
		return "doctype"
	}
	return "unknown"
}

// Name is an element or attribute name.
type Name struct {
	// Space is the namespace URI; "" for no namespace.
	Space string
	// Prefix is the prefix as written in the document; "" for none.
	Prefix string
	// Local is the local part of the name.
	Local string
}

// Qualified returns the name as written: "prefix:local" or "local".
func (n Name) Qualified() string {
	if n.Prefix == "" {
		return n.Local
	}
	return n.Prefix + ":" + n.Local
}

// NSDecl is a namespace declaration on an element: xmlns="uri" (Prefix "")
// or xmlns:p="uri".
type NSDecl struct {
	Prefix string
	URI    string
}

// Node is a node of the tree. Nodes are built by Parse and must be treated
// as read-only; a Document is safe for concurrent reads.
type Node struct {
	Type NodeType
	// Parent is nil for the document; an attribute's parent is its element.
	Parent *Node
	// Children of a document or element, in document order.
	Children []*Node
	// Attrs of an element, in document order, without xmlns declarations.
	Attrs []*Node
	// NSDecls are an element's namespace declarations, in document order.
	NSDecls []NSDecl
	// Name of an element or attribute; for a processing instruction,
	// Name.Local is the target.
	Name Name
	// Value is an attribute's value, the text of a text node, a comment,
	// a processing instruction's data or the raw DOCTYPE declaration
	// (without "<!" and ">"). Entity and character references are
	// replaced.
	Value string
	// CDATA marks a text node that is a CDATA section.
	CDATA bool
	// Offset and End are the byte offsets of the node's markup in
	// Document.Source: from the first byte of a start tag, attribute
	// name, text, comment or instruction to just past its end tag or
	// last byte. An element's End includes its end tag.
	Offset, End int
	// Index is the node's position in document order: the document is 0,
	// an element comes before its attributes, which come before its
	// children.
	Index int

	sibling int // index in Parent.Children or Parent.Attrs
	namePos int // 1-based position among like siblings (see Path)
}

// SiblingIndex returns the node's index in its parent's Children (or
// Attrs, for an attribute).
func (n *Node) SiblingIndex() int { return n.sibling }

// StringValue returns the XPath string-value: the concatenated text of
// all descendant text nodes for a document or element, Value otherwise.
func (n *Node) StringValue() string {
	switch n.Type {
	case DocumentNode, ElementNode:
		// Fast path: one text child.
		if len(n.Children) == 1 && n.Children[0].Type == TextNode {
			return n.Children[0].Value
		}
		var b strings.Builder
		n.appendText(&b)
		return b.String()
	}
	return n.Value
}

func (n *Node) appendText(b *strings.Builder) {
	for _, c := range n.Children {
		switch c.Type {
		case TextNode:
			b.WriteString(c.Value)
		case ElementNode:
			c.appendText(b)
		}
	}
}

// Root returns the document node the node belongs to.
func (n *Node) Root() *Node {
	for n.Parent != nil {
		n = n.Parent
	}
	return n
}

// LookupNamespace returns the namespace URI bound to prefix at the node
// ("" for the default namespace). The prefix "xml" is always bound.
func (n *Node) LookupNamespace(prefix string) (string, bool) {
	if prefix == "xml" {
		return XMLNamespace, true
	}
	for e := n; e != nil; e = e.Parent {
		for i := len(e.NSDecls) - 1; i >= 0; i-- {
			if e.NSDecls[i].Prefix == prefix {
				return e.NSDecls[i].URI, true
			}
		}
	}
	return "", false
}

// Path returns the node's canonical XPath location path, e.g.
// /soap:Envelope[1]/soap:Body[1]/item[2]/@id, /root[1]/text()[1],
// /comment()[1] or /processing-instruction('xml-stylesheet')[1]; "/" for
// the document and "" for a DOCTYPE (which XPath cannot select).
//
// Names are written as in the document. The position of an element
// without a prefix counts preceding sibling elements with the same local
// name (in any namespace); with a prefix, those with the same prefix and
// local name. Evaluated by internal/core/xpath with its default lenient
// name matching and no namespace bindings, the path selects exactly this
// node (for an attribute: unless the element repeats the attribute name,
// which only HTML mode allows).
func (n *Node) Path() string {
	switch n.Type {
	case DocumentNode:
		return "/"
	case DoctypeNode:
		return ""
	}
	var steps []*Node
	for c := n; c != nil && c.Type != DocumentNode; c = c.Parent {
		steps = append(steps, c)
	}
	var b strings.Builder
	for i := len(steps) - 1; i >= 0; i-- {
		c := steps[i]
		b.WriteByte('/')
		switch c.Type {
		case ElementNode:
			b.WriteString(c.Name.Qualified())
		case AttributeNode:
			b.WriteByte('@')
			b.WriteString(c.Name.Qualified())
			continue
		case TextNode:
			b.WriteString("text()")
		case CommentNode:
			b.WriteString("comment()")
		case ProcInstNode:
			b.WriteString("processing-instruction('")
			b.WriteString(c.Name.Local)
			b.WriteString("')")
		}
		b.WriteByte('[')
		b.WriteString(strconv.Itoa(c.namePos))
		b.WriteByte(']')
	}
	return b.String()
}

// posKey is the class of like siblings a node's Path position counts in.
type posKey struct {
	t      NodeType
	prefix string
	local  string
}

func keyOf(c *Node) posKey {
	switch c.Type {
	case ElementNode:
		return posKey{ElementNode, c.Name.Prefix, c.Name.Local}
	case ProcInstNode:
		return posKey{t: ProcInstNode, local: c.Name.Local}
	}
	return posKey{t: c.Type}
}

// assignPositions sets namePos on the children of n; counts is scratch.
func assignPositions(n *Node, counts map[posKey]int) {
	clear(counts)
	for _, c := range n.Children {
		k := keyOf(c)
		counts[k]++
		if c.Type == ElementNode && c.Name.Prefix != "" {
			// An unprefixed name test matches the element too.
			counts[posKey{ElementNode, "", c.Name.Local}]++
			c.namePos = counts[k]
			continue
		}
		c.namePos = counts[k]
	}
}

// Document is a parsed XML document.
type Document struct {
	// Root is the document node. Its children are the root element and
	// the comments, processing instructions and DOCTYPE around it.
	Root *Node
	// Source is the text that was parsed and that node offsets refer to:
	// the input converted to UTF-8, without a byte order mark. It shares
	// the input's memory when the input was UTF-8.
	Source []byte
	// Encoding is the input's encoding as detected or declared: "UTF-8",
	// "UTF-16LE", "UTF-16BE", "ISO-8859-1", "Windows-1252" or "US-ASCII".
	Encoding string
	// NodeCount is the number of nodes, the document included.
	NodeCount int

	lineStarts []int // byte offset of each line's first byte
	units16    []int // UTF-16 code units before each 4 KiB block
}

// Element returns the root element, or nil if there is none (HTML mode).
func (d *Document) Element() *Node {
	for _, c := range d.Root.Children {
		if c.Type == ElementNode {
			return c
		}
	}
	return nil
}

// Namespaces returns every prefix declared in the document with the URI
// of its first declaration in document order; the default namespace, if
// declared, is under "". Useful as a namespace map for strict XPath name
// matching.
func (d *Document) Namespaces() map[string]string {
	m := map[string]string{}
	var walk func(n *Node)
	walk = func(n *Node) {
		for _, ns := range n.NSDecls {
			if _, ok := m[ns.Prefix]; !ok {
				m[ns.Prefix] = ns.URI
			}
		}
		for _, c := range n.Children {
			if c.Type == ElementNode {
				walk(c)
			}
		}
	}
	walk(d.Root)
	return m
}

// Position is a location in Document.Source.
type Position struct {
	// Offset is the byte offset.
	Offset int
	// Offset16 is the offset in UTF-16 code units (CodeMirror positions).
	Offset16 int
	// Line and Column are 1-based; Column counts bytes.
	Line, Column int
}

const blockSize = 4096

// Position converts a byte offset in Source (such as Node.Offset) to a
// line, column and UTF-16 offset. Offsets outside Source are clamped.
func (d *Document) Position(offset int) Position {
	offset = max(0, min(offset, len(d.Source)))
	line := sort.Search(len(d.lineStarts), func(i int) bool { return d.lineStarts[i] > offset }) // >= 1
	p := Position{Offset: offset, Line: line, Column: offset - d.lineStarts[line-1] + 1}
	blk := offset / blockSize
	p.Offset16 = d.units16[blk] + utf16Units(d.Source[blk*blockSize:offset])
	return p
}

func (d *Document) index() {
	d.lineStarts = append(d.lineStarts[:0], 0)
	d.units16 = make([]int, len(d.Source)/blockSize+1)
	units := 0
	for i, c := range d.Source {
		if i%blockSize == 0 {
			d.units16[i/blockSize] = units
		}
		if c == '\n' {
			d.lineStarts = append(d.lineStarts, i+1)
		}
		if c&0xC0 != 0x80 {
			units++
			if c >= 0xF0 {
				units++
			}
		}
	}
}

// utf16Units counts the UTF-16 code units of UTF-8 text: one per leading
// byte, two for a four-byte sequence.
func utf16Units(b []byte) int {
	n := 0
	for _, c := range b {
		if c&0xC0 != 0x80 {
			n++
			if c >= 0xF0 {
				n++
			}
		}
	}
	return n
}
