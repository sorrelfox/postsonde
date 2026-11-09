package xmldom

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Limits bound what Parse accepts. A zero field means the DefaultLimits
// value; a negative field means no limit.
type Limits struct {
	// MaxBytes bounds the input size in bytes.
	MaxBytes int
	// MaxDepth bounds element nesting.
	MaxDepth int
	// MaxNodes bounds the nodes (elements, attributes, text, comments,
	// processing instructions, DOCTYPE) of the tree.
	MaxNodes int
}

// DefaultLimits accept any ordinary API response while keeping memory use
// bounded (a node takes about 200 bytes).
var DefaultLimits = Limits{MaxBytes: 64 << 20, MaxDepth: 1024, MaxNodes: 4_000_000}

func (l Limits) resolve() Limits {
	pick := func(v, def int) int {
		switch {
		case v == 0:
			return def
		case v < 0:
			return int(^uint(0) >> 1)
		}
		return v
	}
	return Limits{
		MaxBytes: pick(l.MaxBytes, DefaultLimits.MaxBytes),
		MaxDepth: pick(l.MaxDepth, DefaultLimits.MaxDepth),
		MaxNodes: pick(l.MaxNodes, DefaultLimits.MaxNodes),
	}
}

// Options configure Parse.
type Options struct {
	// HTML parses leniently, for HTML-ish markup: encoding/xml's
	// non-strict mode (attributes without values or quotes, unknown
	// entities kept as text), the HTML entities (&nbsp; ...), void
	// elements (<br>, <img> ...) closed automatically, end tags matched
	// case-insensitively, unmatched end tags ignored, open elements closed
	// at the end, unbound prefixes, several top-level elements and text
	// outside them allowed.
	HTML bool
	// Encoding is used when the document has no byte order mark and no
	// encoding declaration, e.g. the charset of a Content-Type header.
	// "" means UTF-8.
	Encoding string
	// Decoded says data is UTF-8 text already, e.g. a response body
	// decoded with its charset: a UTF-8 byte order mark is dropped and the
	// encoding declaration is not used (Encoding is ignored).
	Decoded bool
	// Limits bound the input; zero fields take DefaultLimits.
	Limits Limits
}

// Parse parses a well-formed XML document with DefaultLimits.
func Parse(data []byte) (*Document, error) {
	return ParseOptions(context.Background(), data, Options{})
}

// ParseOptions parses a document with options. The error is a
// *SyntaxError, *LimitError or *EncodingError (convert with AsErr), or
// ctx's error wrapped by errs when ctx is done.
func ParseOptions(ctx context.Context, data []byte, opts Options) (*Document, error) {
	lim := opts.Limits.resolve()
	if len(data) > lim.MaxBytes {
		return nil, &LimitError{Limit: LimitBytes, Max: lim.MaxBytes}
	}
	src, enc := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), EncodingUTF8
	if !opts.Decoded {
		var err error
		if src, enc, err = decodeInput(data, opts.Encoding); err != nil {
			return nil, err
		}
	}
	doc := &Document{Source: src, Encoding: enc}
	doc.index()
	b := &builder{
		ctx: ctx, doc: doc, src: src, html: opts.HTML, lim: lim,
		counts: map[posKey]int{}, ns: map[string][]string{}, open: map[string]int{},
	}
	if err := b.run(); err != nil {
		return nil, err
	}
	return doc, nil
}

type builder struct {
	ctx  context.Context
	doc  *Document
	src  []byte
	html bool
	lim  Limits

	dec      *xml.Decoder
	stack    []*Node             // open nodes; stack[0] is the document
	ns       map[string][]string // prefix -> URIs bound, innermost last
	bindings []string            // prefixes bound, in binding order
	marks    []int               // len(bindings) when each open element started
	open     map[string]int      // HTML mode: open elements by lower-case name
	index    int
	seenRoot bool
	doctype  bool
	counts   map[posKey]int
	pending  *Node // HTML void element waiting for its end tag
	tokens   int
	merge    *Node  // text node whose value is being extended (HTML mode)
	mergeBuf []byte // its value
	attrSeen map[Name]struct{}
}

func (b *builder) run() error {
	root := &Node{Type: DocumentNode, End: len(b.src)}
	b.doc.Root = root
	b.stack = []*Node{root}
	b.index = 1
	if !b.html {
		// encoding/xml checks text, but not comments, instructions or
		// the DOCTYPE.
		if i := invalidUTF8(b.src); i >= 0 {
			return b.syntaxErr(i, "invalid UTF-8")
		}
	}

	b.dec = xml.NewDecoder(bytes.NewReader(b.src))
	b.dec.Strict = !b.html
	if b.html {
		b.dec.Entity = xml.HTMLEntity
	}
	// decodeInput already converted the text to UTF-8.
	b.dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }

	for {
		b.tokens++
		if b.tokens&1023 == 0 && b.ctx != nil {
			if err := b.ctx.Err(); err != nil {
				return errs.Wrap(err, errs.KindOf(err), "Parsing the XML document was stopped.")
			}
		}
		start := int(b.dec.InputOffset())
		tok, err := b.dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b.syntaxErr(int(b.dec.InputOffset()), decoderMessage(err))
		}
		end := int(b.dec.InputOffset())
		if b.pending != nil {
			p := b.pending
			b.pending = nil
			if e, ok := tok.(xml.EndElement); ok && strings.EqualFold(qualified(e.Name), p.Name.Qualified()) {
				b.pop(end)
				continue
			}
			b.pop(p.End)
		}
		if err := b.token(tok, start, end); err != nil {
			return err
		}
	}

	b.flushText()
	end := len(b.src)
	if len(b.stack) > 1 {
		if !b.html {
			top := b.stack[len(b.stack)-1]
			return b.syntaxErr(end, "unexpected end of document: <"+top.Name.Qualified()+"> is not closed")
		}
		for len(b.stack) > 1 {
			b.pop(end)
		}
	}
	if !b.html && !b.seenRoot {
		return b.syntaxErr(end, "the document has no root element")
	}
	assignPositions(root, b.counts)
	b.doc.NodeCount = b.index
	return nil
}

func (b *builder) token(tok xml.Token, start, end int) error {
	parent := b.stack[len(b.stack)-1]
	atTop := parent.Type == DocumentNode
	switch t := tok.(type) {
	case xml.StartElement:
		return b.startElement(t, start, end)

	case xml.EndElement:
		return b.endElement(t, start, end)

	case xml.CharData:
		cdata := bytes.HasPrefix(b.src[start:], []byte("<![CDATA["))
		if atTop {
			if !cdata && isBlank(t) {
				return nil
			}
			if !b.html {
				return b.syntaxErr(start, "text is not allowed outside the root element")
			}
		}
		if k := len(parent.Children); !cdata && k > 0 {
			// Text split only by an ignored tag (HTML mode) stays one node.
			if last := parent.Children[k-1]; last.Type == TextNode && !last.CDATA {
				if b.merge != last {
					b.merge = last
					b.mergeBuf = append(b.mergeBuf[:0], last.Value...)
				}
				b.mergeBuf = append(b.mergeBuf, t...)
				last.End = end
				return nil
			}
		}
		return b.add(parent, &Node{Type: TextNode, Value: string(t), CDATA: cdata, Offset: start, End: end})

	case xml.Comment:
		return b.add(parent, &Node{Type: CommentNode, Value: string(t), Offset: start, End: end})

	case xml.ProcInst:
		if strings.EqualFold(t.Target, "xml") {
			if start != 0 && !b.html {
				return b.syntaxErr(start, "the XML declaration is allowed only at the start of the document")
			}
			return nil
		}
		return b.add(parent, &Node{Type: ProcInstNode, Name: Name{Local: t.Target}, Value: string(t.Inst), Offset: start, End: end})

	case xml.Directive:
		isDoctype := len(t) > len("DOCTYPE") && strings.EqualFold(string(t[:len("DOCTYPE")]), "DOCTYPE") && isSpace(t[len("DOCTYPE")])
		if !isDoctype || !atTop || b.seenRoot || b.doctype {
			if b.html {
				return nil
			}
			return b.syntaxErr(start, "a <!"+firstWord(t)+"> declaration is not allowed here")
		}
		if !b.html && string(t[:len("DOCTYPE")]) != "DOCTYPE" {
			return b.syntaxErr(start, "a <!"+firstWord(t)+"> declaration is not allowed here")
		}
		b.doctype = true
		return b.add(parent, &Node{Type: DoctypeNode, Value: string(t), Offset: start, End: end})
	}
	return nil
}

func (b *builder) startElement(t xml.StartElement, start, end int) error {
	parent := b.stack[len(b.stack)-1]
	if parent.Type == DocumentNode {
		if b.seenRoot && !b.html {
			return b.syntaxErr(start, "only one root element is allowed")
		}
		b.seenRoot = true
	}
	if len(b.stack) > b.lim.MaxDepth {
		return &LimitError{Limit: LimitDepth, Max: b.lim.MaxDepth, Offset: start}
	}

	el := &Node{Type: ElementNode, Offset: start, End: end}
	b.marks = append(b.marks, len(b.bindings))
	attrOffs := attrOffsets(b.src, start, end, len(t.Attr))
	var plain []int // indexes in t.Attr of attributes that are not declarations
	for i, a := range t.Attr {
		switch {
		case a.Name.Space == "" && a.Name.Local == "xmlns":
			el.NSDecls = append(el.NSDecls, NSDecl{URI: a.Value})
			b.bind("", a.Value)
		case a.Name.Space == "xmlns":
			if !b.html {
				if a.Value == "" {
					return b.syntaxErr(attrOffs[i][0], "the namespace prefix "+a.Name.Local+" cannot be bound to an empty URI")
				}
				if a.Name.Local == "xmlns" || (a.Name.Local == "xml") != (a.Value == XMLNamespace) {
					return b.syntaxErr(attrOffs[i][0], "the namespace prefix "+a.Name.Local+" cannot be declared this way")
				}
			}
			el.NSDecls = append(el.NSDecls, NSDecl{Prefix: a.Name.Local, URI: a.Value})
			b.bind(a.Name.Local, a.Value)
		default:
			plain = append(plain, i)
		}
	}

	space, err := b.resolve(t.Name.Space, true, start)
	if err != nil {
		return err
	}
	el.Name = Name{Space: space, Prefix: t.Name.Space, Local: t.Name.Local}
	if err := b.add(parent, el); err != nil {
		return err
	}

	if len(plain) > 0 {
		el.Attrs = make([]*Node, 0, len(plain))
		for _, i := range plain {
			a := t.Attr[i]
			space, err := b.resolve(a.Name.Space, false, attrOffs[i][0])
			if err != nil {
				return err
			}
			attr := &Node{
				Type: AttributeNode, Parent: el,
				Name:   Name{Space: space, Prefix: a.Name.Space, Local: a.Name.Local},
				Value:  a.Value,
				Offset: attrOffs[i][0], End: attrOffs[i][1],
				Index: b.index, sibling: len(el.Attrs),
			}
			if !b.html && b.repeated(el.Attrs, attr) {
				return b.syntaxErr(attr.Offset, "attribute "+attr.Name.Qualified()+" is repeated")
			}
			if err := b.count(attr.Offset); err != nil {
				return err
			}
			el.Attrs = append(el.Attrs, attr)
		}
	}

	b.stack = append(b.stack, el)
	if b.html {
		b.open[strings.ToLower(el.Name.Qualified())]++
		if _, ok := voidElements[strings.ToLower(el.Name.Local)]; ok && el.Name.Prefix == "" {
			b.pending = el
		}
	}
	return nil
}

func (b *builder) endElement(t xml.EndElement, start, end int) error {
	name := qualified(t.Name)
	if len(b.stack) == 1 {
		if b.html {
			return nil
		}
		return b.syntaxErr(start, "unexpected end tag </"+name+">")
	}
	top := b.stack[len(b.stack)-1]
	if top.Name.Qualified() == name || (b.html && strings.EqualFold(top.Name.Qualified(), name)) {
		b.pop(end)
		return nil
	}
	if !b.html {
		return b.syntaxErr(start, "element <"+top.Name.Qualified()+"> closed by </"+name+">")
	}
	if b.open[strings.ToLower(name)] == 0 {
		return nil // an end tag with no open element: ignored
	}
	for i := len(b.stack) - 1; i > 0; i-- {
		if strings.EqualFold(b.stack[i].Name.Qualified(), name) {
			for len(b.stack) > i+1 {
				b.pop(start)
			}
			b.pop(end)
			return nil
		}
	}
	return nil
}

// pop closes the innermost open element, ending at end.
func (b *builder) pop(end int) {
	b.flushText()
	el := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	el.End = end
	mark := b.marks[len(b.marks)-1]
	for _, prefix := range b.bindings[mark:] {
		b.ns[prefix] = b.ns[prefix][:len(b.ns[prefix])-1]
	}
	b.bindings = b.bindings[:mark]
	b.marks = b.marks[:len(b.marks)-1]
	if b.html {
		b.open[strings.ToLower(el.Name.Qualified())]--
	}
	if len(el.Children) > 0 {
		assignPositions(el, b.counts)
	}
}

// add appends n to parent's children and gives it its document index.
func (b *builder) add(parent, n *Node) error {
	b.flushText()
	if err := b.count(n.Offset); err != nil {
		return err
	}
	n.Parent = parent
	n.Index = b.index - 1
	n.sibling = len(parent.Children)
	parent.Children = append(parent.Children, n)
	return nil
}

func (b *builder) count(offset int) error {
	if b.index > b.lim.MaxNodes {
		return &LimitError{Limit: LimitNodes, Max: b.lim.MaxNodes, Offset: offset}
	}
	b.index++
	return nil
}

// resolve returns the namespace URI of a prefix in scope; an unprefixed
// element takes the default namespace, an unprefixed attribute none.
func (b *builder) resolve(prefix string, element bool, offset int) (string, error) {
	if prefix == "" && !element {
		return "", nil
	}
	if prefix == "xml" {
		return XMLNamespace, nil
	}
	if uris := b.ns[prefix]; len(uris) > 0 {
		return uris[len(uris)-1], nil
	}
	if prefix == "" || b.html {
		return "", nil
	}
	return "", b.syntaxErr(offset, "the namespace prefix "+prefix+" is not declared")
}

func (b *builder) syntaxErr(offset int, msg string) error {
	p := b.doc.Position(offset)
	return &SyntaxError{Offset: p.Offset, Line: p.Line, Column: p.Column, Message: msg}
}

func decoderMessage(err error) string {
	var se *xml.SyntaxError
	if errors.As(err, &se) {
		return se.Msg
	}
	return strings.TrimPrefix(err.Error(), "xml: ")
}

func qualified(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// invalidUTF8 returns the offset of the first invalid UTF-8 sequence, or
// -1.
func invalidUTF8(b []byte) int {
	if utf8.Valid(b) {
		return -1
	}
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

func isBlank(b []byte) bool {
	for _, c := range b {
		if !isSpace(c) {
			return false
		}
	}
	return true
}

func (b *builder) bind(prefix, uri string) {
	b.ns[prefix] = append(b.ns[prefix], uri)
	b.bindings = append(b.bindings, prefix)
}

// flushText stores the value of a text node extended by merged text.
func (b *builder) flushText() {
	if b.merge != nil {
		b.merge.Value = string(b.mergeBuf)
		b.merge = nil
	}
}

// repeated reports whether attrs has an attribute with a's expanded name.
func (b *builder) repeated(attrs []*Node, a *Node) bool {
	key := Name{Space: a.Name.Space, Local: a.Name.Local}
	if len(attrs) < 8 {
		for _, prev := range attrs {
			if prev.Name.Space == key.Space && prev.Name.Local == key.Local {
				return true
			}
		}
		return false
	}
	if len(attrs) == 8 {
		b.attrSeen = map[Name]struct{}{}
		for _, prev := range attrs {
			b.attrSeen[Name{Space: prev.Name.Space, Local: prev.Name.Local}] = struct{}{}
		}
	}
	if _, ok := b.attrSeen[key]; ok {
		return true
	}
	b.attrSeen[key] = struct{}{}
	return false
}

func firstWord(d []byte) string {
	i := bytes.IndexFunc(d, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '[' })
	if i < 0 {
		i = len(d)
	}
	return string(d[:min(i, 32)])
}

// attrOffsets finds the byte range of each attribute (name to end of
// value) in the start tag src[start:end]. When the tag cannot be scanned
// as expected, every range is the tag's.
func attrOffsets(src []byte, start, end, n int) [][2]int {
	out := make([][2]int, 0, n)
	fallback := func() [][2]int {
		out = out[:0]
		for range n {
			out = append(out, [2]int{start, end})
		}
		return out
	}
	if n == 0 {
		return out
	}
	i := start + 1
	for i < end && !isSpace(src[i]) && src[i] != '>' && src[i] != '/' {
		i++
	}
	for len(out) < n {
		for i < end && isSpace(src[i]) {
			i++
		}
		if i >= end || src[i] == '>' || src[i] == '/' {
			return fallback()
		}
		a := i
		for i < end && !isSpace(src[i]) && src[i] != '=' && src[i] != '>' && src[i] != '/' {
			i++
		}
		if i == a {
			return fallback()
		}
		j := i
		for j < end && isSpace(src[j]) {
			j++
		}
		if j < end && src[j] == '=' {
			j++
			for j < end && isSpace(src[j]) {
				j++
			}
			if j < end && (src[j] == '"' || src[j] == '\'') {
				q := bytes.IndexByte(src[j+1:end], src[j])
				if q < 0 {
					return fallback()
				}
				i = j + 1 + q + 1
			} else {
				for j < end && !isSpace(src[j]) && src[j] != '>' && src[j] != '/' {
					j++
				}
				i = j
			}
		}
		out = append(out, [2]int{a, i})
	}
	return out
}

// voidElements are the HTML elements that never have content; HTML mode
// closes them right after their start tag.
var voidElements = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, s := range xml.HTMLAutoClose {
		m[s] = struct{}{}
	}
	return m
}()
