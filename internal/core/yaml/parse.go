package yaml

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parse reads a stream holding at most one document. An empty stream is
// a null scalar at line 1, column 1. Errors are *Error.
func Parse(data []byte) (*Node, error) {
	docs, err := ParseAllLimits(data, Limits{})
	if err != nil {
		return nil, err
	}
	switch len(docs) {
	case 0:
		return &Node{Kind: ScalarNode, Tag: TagNull, Line: 1, Column: 1}, nil
	case 1:
		return docs[0], nil
	}
	return nil, &Error{Code: CodeUnsupported, Message: "the file holds " + strconv.Itoa(len(docs)) + " YAML documents; only one is expected", Line: docs[1].Line, Column: docs[1].Column}
}

// ParseAll reads every document of a stream ("---" separated), with the
// DefaultLimits. Errors are *Error.
func ParseAll(data []byte) ([]*Node, error) { return ParseAllLimits(data, Limits{}) }

// ParseAllLimits is ParseAll with explicit limits.
func ParseAllLimits(data []byte, lim Limits) (docs []*Node, err error) {
	lim = lim.withDefaults()
	if len(data) > lim.MaxBytes {
		return nil, &Error{Code: CodeTooLarge, Message: "the input is over " + strconv.Itoa(lim.MaxBytes) + " bytes"}
	}
	src, e := normalize(data)
	if e != nil {
		return nil, e
	}
	p := &parser{src: src, lim: lim}
	p.lineStarts = append(p.lineStarts, 0)
	for i, c := range src {
		if c == '\n' {
			p.lineStarts = append(p.lineStarts, i+1)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			a, ok := r.(abort)
			if !ok {
				panic(r)
			}
			docs, err = nil, a.err
		}
	}()
	return p.parseStream(), nil
}

// abort carries an *Error out of the recursive descent.
type abort struct{ err *Error }

// normalize checks the encoding and turns CRLF and CR line breaks into LF.
func normalize(data []byte) ([]byte, *Error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if bytes.HasPrefix(data, []byte("\xFE\xFF")) || bytes.HasPrefix(data, []byte("\xFF\xFE")) ||
		(len(data) >= 2 && (data[0] == 0 || data[1] == 0)) {
		return nil, &Error{Code: CodeUnsupported, Message: "the file is not UTF-8 (UTF-16 and UTF-32 are not supported)"}
	}
	out := make([]byte, 0, len(data))
	line, lineStart := 1, 0
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '\r':
			out = append(out, '\n')
			i++
			if i < len(data) && data[i] == '\n' {
				i++
			}
			line, lineStart = line+1, len(out)
			continue
		case c == '\n':
			out = append(out, c)
			i++
			line, lineStart = line+1, len(out)
			continue
		case c < 0x20 && c != '\t' || c == 0x7F:
			return nil, &Error{Code: CodeInvalidYAML, Message: fmt.Sprintf("control character U+%04X is not allowed", c), Line: line, Column: utf8.RuneCount(out[lineStart:]) + 1}
		case c < utf8.RuneSelf:
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size <= 1 {
			return nil, &Error{Code: CodeInvalidYAML, Message: "the text is not valid UTF-8", Line: line, Column: utf8.RuneCount(out[lineStart:]) + 1}
		}
		out = append(out, data[i:i+size]...)
		i += size
	}
	return out, nil
}

type mode uint8

const (
	modeDoc   mode = iota // the root node of a document
	modeValue             // after "key:"
	modeEntry             // after "- "
)

type parser struct {
	src        []byte
	pos        int
	lineStarts []int
	lim        Limits
	depth      int
	nodes      int
	anchors    map[string]*Node

	// The last column computed by position.
	cacheLine, cachePos, cacheRunes int
}

// ---- positions and errors ----

func (p *parser) lineIndex(pos int) int {
	return sort.Search(len(p.lineStarts), func(i int) bool { return p.lineStarts[i] > pos }) - 1
}

// col is the 0-based byte column of pos (the indentation of content).
func (p *parser) col(pos int) int { return pos - p.lineStarts[p.lineIndex(pos)] }

func (p *parser) position(pos int) (line, col int) {
	if pos > len(p.src) {
		pos = len(p.src)
	}
	i := p.lineIndex(pos)
	// Columns are counted from the last position on the same line, so a
	// long single-line (JSON-like) document stays linear.
	from, runes := p.lineStarts[i], 0
	if p.cacheLine == i && p.cachePos <= pos {
		from, runes = p.cachePos, p.cacheRunes
	}
	runes += utf8.RuneCount(p.src[from:pos])
	p.cacheLine, p.cachePos, p.cacheRunes = i, pos, runes
	return i + 1, runes + 1
}

func (p *parser) failCode(pos int, code, msg string) {
	line, col := p.position(pos)
	panic(abort{&Error{Code: code, Message: msg, Line: line, Column: col}})
}

func (p *parser) fail(pos int, msg string)        { p.failCode(pos, CodeInvalidYAML, msg) }
func (p *parser) unsupported(pos int, msg string) { p.failCode(pos, CodeUnsupported, msg) }

func (p *parser) newNode(kind Kind, pos int) *Node {
	p.nodes++
	if p.nodes > p.lim.MaxNodes {
		p.failCode(pos, CodeTooLarge, "more than "+strconv.Itoa(p.lim.MaxNodes)+" nodes")
	}
	n := &Node{Kind: kind}
	n.Line, n.Column = p.position(pos)
	return n
}

func (p *parser) enter(pos int) {
	p.depth++
	if p.depth > p.lim.MaxDepth {
		p.failCode(pos, CodeTooLarge, "nested more than "+strconv.Itoa(p.lim.MaxDepth)+" levels deep")
	}
}

func (p *parser) leave() { p.depth-- }

// ---- characters ----

func (p *parser) peek(off int) byte {
	if i := p.pos + off; i < len(p.src) {
		return p.src[i]
	}
	return 0
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func isBlank(c byte) bool      { return c == ' ' || c == '\t' }
func isBlankOrEnd(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == 0 }
func isFlowIndicator(c byte) bool {
	return c == ',' || c == '[' || c == ']' || c == '{' || c == '}'
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.src) && isBlank(p.src[p.pos]) {
		p.pos++
	}
}

// commentAt reports whether a '#' at pos starts a comment: it must follow
// a blank or start a line.
func (p *parser) commentAt(pos int) bool {
	return pos < len(p.src) && p.src[pos] == '#' && (pos == 0 || isBlankOrEnd(p.src[pos-1]))
}

// atLineEnd reports whether only a comment or nothing is left on the line.
func (p *parser) atLineEnd() bool {
	c := p.peek(0)
	return c == '\n' || c == 0 || p.commentAt(p.pos)
}

func (p *parser) skipLine() {
	for p.pos < len(p.src) && p.src[p.pos] != '\n' {
		p.pos++
	}
}

// skipToContent skips blanks, comments and line breaks. Content that
// starts a line may not be indented with tabs.
func (p *parser) skipToContent() {
	for {
		p.skipSpaces()
		if p.commentAt(p.pos) {
			p.skipLine()
		}
		if p.peek(0) != '\n' {
			break
		}
		p.pos++
	}
	if p.eof() {
		return
	}
	ls := p.lineStarts[p.lineIndex(p.pos)]
	indent := p.src[ls:p.pos]
	if len(bytes.Trim(indent, " \t")) == 0 && bytes.IndexByte(indent, '\t') >= 0 {
		p.fail(ls+bytes.IndexByte(indent, '\t'), "tab characters must not be used for indentation")
	}
}

func (p *parser) isDocMarker(pos int) bool {
	if pos+3 > len(p.src) || p.col(pos) != 0 {
		return false
	}
	s := string(p.src[pos : pos+3])
	return (s == "---" || s == "...") && (pos+3 == len(p.src) || isBlankOrEnd(p.src[pos+3]))
}

// onlyBlanksBefore reports whether pos is the first non-blank of its line.
func (p *parser) onlyBlanksBefore(pos int) bool {
	ls := p.lineStarts[p.lineIndex(pos)]
	return len(bytes.Trim(p.src[ls:pos], " \t")) == 0
}

// ---- stream and documents ----

func (p *parser) parseStream() []*Node {
	var docs []*Node
	for {
		p.skipToContent()
		if p.eof() {
			return docs
		}
		directives := false
		for p.peek(0) == '%' && p.col(p.pos) == 0 {
			p.parseDirective()
			directives = true
			p.skipToContent()
		}
		if p.isDocMarker(p.pos) && p.peek(0) == '-' {
			p.pos += 3
		} else if directives {
			p.fail(p.pos, "expected '---' after the directives")
		} else if p.isDocMarker(p.pos) { // a stray "..."
			p.pos += 3
			p.expectLineEnd()
			continue
		}
		p.anchors = map[string]*Node{}
		docs = append(docs, p.parseBlockNode(-1, modeDoc))
		p.skipToContent()
		if p.eof() {
			return docs
		}
		if !p.isDocMarker(p.pos) {
			p.fail(p.pos, "unexpected content; check the indentation")
		}
		if p.peek(0) == '.' {
			p.pos += 3
			p.expectLineEnd()
		}
	}
}

func (p *parser) parseDirective() {
	start := p.pos
	p.skipLine()
	line := string(p.src[start:p.pos])
	if i := strings.Index(line, " #"); i >= 0 {
		line = line[:i]
	}
	f := strings.Fields(line)
	switch f[0] {
	case "%YAML":
		if len(f) != 2 || !strings.HasPrefix(f[1], "1.") {
			p.unsupported(start, "only YAML 1.x documents are supported")
		}
	case "%TAG":
		p.unsupported(start, "the %TAG directive is not supported")
	}
	// Other (reserved) directives are ignored, as the spec says.
}

// expectLineEnd requires that only blanks and a comment are left on the
// line.
func (p *parser) expectLineEnd() {
	p.skipSpaces()
	if p.atLineEnd() {
		return
	}
	if p.peek(0) == ':' && isBlankOrEnd(p.peek(1)) {
		p.fail(p.pos, "mapping values are not allowed here")
	}
	p.fail(p.pos, "unexpected content after the value")
}

// ---- block nodes ----

type props struct {
	present bool
	pos     int
	anchor  string
	tag     string // "!" or a core schema tag such as "!!str"
	tagPos  int
}

func (p *parser) emptyNode(pos int) *Node {
	n := p.newNode(ScalarNode, pos)
	n.Tag = TagNull
	return n
}

// contentStarts reports whether the content at p.pos (the first of its
// line) belongs to a node whose parent block is indented parent.
func (p *parser) contentStarts(parent int, m mode) bool {
	if p.eof() || p.isDocMarker(p.pos) {
		return false
	}
	c := p.col(p.pos)
	if c > parent {
		return true
	}
	// A block sequence may sit at its key's indentation.
	return m == modeValue && c == parent && p.peek(0) == '-' && isBlankOrEnd(p.peek(1))
}

// parseBlockNode parses the node that follows "key:", "- " or the start of
// a document, whose parent block is indented parent columns.
func (p *parser) parseBlockNode(parent int, m mode) *Node {
	p.enter(p.pos)
	defer p.leave()
	start := p.pos
	p.skipSpaces()
	if p.atLineEnd() {
		p.skipToContent()
		if !p.contentStarts(parent, m) {
			return p.emptyNode(start)
		}
	}
	ownLine := p.onlyBlanksBefore(p.pos)
	col := p.col(p.pos)
	var pr props
	propsSameLine := false
	if c := p.peek(0); c == '&' || c == '!' {
		pr = p.parseProps(false)
		p.skipSpaces()
		if p.atLineEnd() {
			p.skipToContent()
			if !p.contentStarts(parent, m) {
				n := p.emptyNode(pr.pos)
				p.applyProps(n, pr)
				return n
			}
			ownLine, col = true, p.col(p.pos)
		} else {
			propsSameLine = true
		}
	}
	blockOK := ownLine || m == modeEntry
	c := p.peek(0)
	switch {
	case c == '-' && isBlankOrEnd(p.peek(1)):
		if !blockOK {
			p.fail(p.pos, "a block sequence cannot start on the same line as its key")
		}
		n := p.parseBlockSeq(col)
		p.applyProps(n, pr)
		return n
	case c == '?' && isBlankOrEnd(p.peek(1)):
		p.unsupported(p.pos, "explicit mapping keys ('? key') are not supported")
	}
	if p.looksLikeKey() {
		if !blockOK {
			p.fail(p.pos, "mapping values are not allowed here")
		}
		if propsSameLine {
			return p.parseBlockMapping(col, &pr)
		}
		n := p.parseBlockMapping(col, nil)
		p.applyProps(n, pr)
		return n
	}
	var n *Node
	switch c {
	case '|', '>':
		n = p.parseBlockScalar(parent)
		p.applyProps(n, pr)
		return n
	case '[', '{':
		n = p.parseFlowNode()
		p.skipSpaces()
		if p.peek(0) == ':' && isBlankOrEnd(p.peek(1)) {
			p.unsupported(start, "collections as mapping keys are not supported")
		}
	default:
		n = p.parseScalarOrAlias(parent, false, pr)
	}
	p.applyProps(n, pr)
	p.expectLineEnd()
	return n
}

// parseScalarOrAlias parses an alias or a quoted or plain scalar.
func (p *parser) parseScalarOrAlias(parent int, flow bool, pr props) *Node {
	switch c := p.peek(0); c {
	case '*':
		if pr.present {
			p.fail(pr.pos, "an alias cannot have an anchor or a tag")
		}
		return p.parseAlias(flow)
	case '"', '\'':
		n := p.newNode(ScalarNode, p.pos)
		n.Tag = TagStr
		if c == '"' {
			n.Style, n.Value = DoubleQuoted, p.scanDoubleQuoted()
		} else {
			n.Style, n.Value = SingleQuoted, p.scanSingleQuoted()
		}
		return n
	case ',', '[', ']', '{', '}', '@', '`', '|', '>':
		p.fail(p.pos, fmt.Sprintf("a plain value cannot start with %q; quote it", c))
	case '-', '?', ':':
		if n := p.peek(1); isBlankOrEnd(n) || flow && isFlowIndicator(n) {
			p.fail(p.pos, fmt.Sprintf("a plain value cannot start with %q followed by a space; quote it", c))
		}
	}
	n := p.newNode(ScalarNode, p.pos)
	n.Value = p.scanPlain(parent, flow)
	n.Tag = resolvePlain(n.Value)
	return n
}

// looksLikeKey reports whether an implicit key ("key:") starts at p.pos,
// optionally after an anchor and a tag. It does not move p.pos.
func (p *parser) looksLikeKey() bool {
	save := p.pos
	defer func() { p.pos = save }()
	if c := p.peek(0); c == '&' || c == '!' {
		p.parseProps(false)
		p.skipSpaces()
		if p.atLineEnd() {
			return false
		}
	}
	switch c := p.peek(0); c {
	case '"', '\'':
		if !p.skipQuotedOnLine() {
			return false
		}
	case '*':
		p.pos++
		p.scanAnchorName()
	case '[', '{', '|', '>':
		return false
	case '-', '?':
		if isBlankOrEnd(p.peek(1)) {
			return false
		}
		p.scanPlainLine(false)
	default:
		p.scanPlainLine(false)
	}
	p.skipSpaces()
	return p.peek(0) == ':' && isBlankOrEnd(p.peek(1))
}

// skipQuotedOnLine moves past a quoted scalar that ends on its line.
func (p *parser) skipQuotedOnLine() bool {
	q := p.src[p.pos]
	for p.pos++; p.pos < len(p.src); p.pos++ {
		switch c := p.src[p.pos]; {
		case c == '\n':
			return false
		case c == '\\' && q == '"':
			p.pos++
			if p.peek(0) == '\n' {
				return false
			}
		case c == q:
			if q == '\'' && p.peek(1) == '\'' {
				p.pos++
				continue
			}
			p.pos++
			return true
		}
	}
	return false
}

// parseImplicitKey parses a one-line key at p.pos (properties already
// consumed).
func (p *parser) parseImplicitKey() *Node {
	start := p.pos
	switch c := p.peek(0); c {
	case '*':
		return p.parseAlias(false)
	case '"', '\'':
		n := p.parseScalarOrAlias(-1, false, props{})
		if p.lineIndex(start) != p.lineIndex(p.pos) {
			p.fail(start, "a mapping key must be on one line")
		}
		return n
	case ':':
		p.unsupported(start, "empty mapping keys are not supported")
	}
	n := p.newNode(ScalarNode, start)
	s, e := p.scanPlainLine(false)
	n.Value = string(p.src[s:e])
	n.Tag = resolvePlain(n.Value)
	return n
}

func (p *parser) parseBlockMapping(col int, firstKeyProps *props) *Node {
	n := p.newNode(MappingNode, p.pos)
	n.Tag = TagMap
	if firstKeyProps != nil {
		n.Line, n.Column = p.position(firstKeyProps.pos)
	}
	for first := true; ; first = false {
		var kp props
		if first && firstKeyProps != nil {
			kp = *firstKeyProps
		} else if c := p.peek(0); c == '&' || c == '!' {
			kp = p.parseProps(false)
			p.skipSpaces()
		}
		key := p.parseImplicitKey()
		p.applyProps(key, kp)
		p.skipSpaces()
		if p.peek(0) != ':' {
			p.fail(p.pos, "expected ':' after the mapping key")
		}
		p.pos++
		val := p.parseBlockNode(col, modeValue)
		n.Pairs = append(n.Pairs, Pair{Key: key, Value: val})
		p.skipToContent()
		if p.eof() || p.isDocMarker(p.pos) {
			return n
		}
		c := p.col(p.pos)
		if c < col {
			return n
		}
		if c > col {
			p.fail(p.pos, "unexpected indentation")
		}
		if !p.looksLikeKey() {
			if p.peek(0) == '-' && isBlankOrEnd(p.peek(1)) {
				p.fail(p.pos, "a sequence entry is not allowed here; check the indentation")
			}
			p.fail(p.pos, "expected a mapping key (key: value)")
		}
	}
}

func (p *parser) parseBlockSeq(col int) *Node {
	n := p.newNode(SequenceNode, p.pos)
	n.Tag = TagSeq
	for {
		p.pos++ // the '-'
		n.Items = append(n.Items, p.parseBlockNode(col, modeEntry))
		p.skipToContent()
		if p.eof() || p.isDocMarker(p.pos) {
			return n
		}
		c := p.col(p.pos)
		if c < col {
			return n
		}
		if c > col {
			p.fail(p.pos, "unexpected indentation")
		}
		if p.peek(0) != '-' || !isBlankOrEnd(p.peek(1)) {
			return n // a key at the parent mapping's indentation
		}
	}
}

// ---- properties and aliases ----

func (p *parser) scanAnchorName() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if isBlankOrEnd(c) || isFlowIndicator(c) || c == ':' && isBlankOrEnd(p.peek(1)) {
			break
		}
		p.pos++
	}
	return string(p.src[start:p.pos])
}

func (p *parser) parseAlias(flow bool) *Node {
	start := p.pos
	p.pos++
	name := p.scanAnchorName()
	if name == "" {
		p.fail(start, "an alias needs a name after '*'")
	}
	target := p.anchors[name]
	if target == nil {
		p.fail(start, fmt.Sprintf("unknown anchor %q (an alias must follow its anchor's complete node)", name))
	}
	n := p.newNode(AliasNode, start)
	n.Value, n.Target = name, target
	return n
}

// coreTags are the tags an explicit !!tag may use.
var coreTags = map[string]bool{
	"str": true, "int": true, "float": true, "bool": true, "null": true,
	"map": true, "seq": true, "binary": true, "timestamp": true,
}

func (p *parser) parseProps(flow bool) props {
	pr := props{present: true, pos: p.pos}
	for {
		switch p.peek(0) {
		case '&':
			if pr.anchor != "" {
				p.fail(p.pos, "a node can have only one anchor")
			}
			start := p.pos
			p.pos++
			if pr.anchor = p.scanAnchorName(); pr.anchor == "" {
				p.fail(start, "an anchor needs a name after '&'")
			}
		case '!':
			if pr.tag != "" {
				p.fail(p.pos, "a node can have only one tag")
			}
			pr.tagPos = p.pos
			pr.tag = p.parseTag(flow)
		default:
			return pr
		}
		if c := p.peek(0); !isBlankOrEnd(c) && !(flow && isFlowIndicator(c)) {
			p.fail(p.pos, "expected a space after the anchor or tag")
		}
		save := p.pos
		p.skipSpaces()
		if c := p.peek(0); c != '&' && c != '!' {
			p.pos = save
			return pr
		}
	}
}

func (p *parser) parseTag(flow bool) string {
	start := p.pos
	if p.peek(1) == '<' {
		end := bytes.IndexByte(p.src[p.pos:], '>')
		if end < 0 {
			p.fail(start, "unterminated verbatim tag")
		}
		uri := string(p.src[p.pos+2 : p.pos+end])
		p.pos += end + 1
		if suffix, ok := strings.CutPrefix(uri, "tag:yaml.org,2002:"); ok && coreTags[suffix] {
			return "!!" + suffix
		}
		p.unsupported(start, "the tag !<"+uri+"> is not supported")
	}
	for p.pos < len(p.src) && !isBlankOrEnd(p.src[p.pos]) && !(flow && isFlowIndicator(p.src[p.pos])) {
		p.pos++
	}
	tag := string(p.src[start:p.pos])
	if tag == "!" {
		return tag
	}
	if suffix, ok := strings.CutPrefix(tag, "!!"); ok {
		if coreTags[suffix] {
			return tag
		}
		p.unsupported(start, "the tag "+tag+" is not supported")
	}
	p.unsupported(start, "custom tags such as "+tag+" are not supported")
	return ""
}

func (p *parser) applyProps(n *Node, pr props) {
	if !pr.present {
		return
	}
	if pr.tag != "" {
		p.applyTag(n, pr.tag, pr.tagPos)
	}
	if pr.anchor != "" {
		n.Anchor = pr.anchor
		p.anchors[pr.anchor] = n
	}
}

func (p *parser) applyTag(n *Node, tag string, pos int) {
	switch tag {
	case "!!map", "!!seq":
		want := MappingNode
		if tag == "!!seq" {
			want = SequenceNode
		}
		if n.Kind != want {
			p.fail(pos, "a "+n.Kind.String()+" cannot have the tag "+tag)
		}
		return
	}
	if n.Kind != ScalarNode {
		if tag == "!" {
			return
		}
		p.fail(pos, "a "+n.Kind.String()+" cannot have the tag "+tag)
	}
	switch tag {
	case "!", "!!str", "!!binary", "!!timestamp":
		n.Tag = TagStr
	case "!!float":
		if t := resolvePlain(n.Value); t != TagFloat && t != TagInt {
			p.fail(pos, fmt.Sprintf("%q is not a valid !!float", n.Value))
		}
		n.Tag = TagFloat
	default:
		if resolvePlain(n.Value) != tag {
			p.fail(pos, fmt.Sprintf("%q is not a valid %s", n.Value, tag))
		}
		n.Tag = tag
	}
}

// ---- plain scalars ----

// scanPlainLine scans the part of a plain scalar that is on the current
// line and returns its bounds without trailing blanks.
func (p *parser) scanPlainLine(flow bool) (start, end int) {
	start = p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\n' ||
			c == ':' && (isBlankOrEnd(p.peek(1)) || flow && isFlowIndicator(p.peek(1))) ||
			c == '#' && p.pos > start && isBlank(p.src[p.pos-1]) ||
			flow && isFlowIndicator(c) {
			break
		}
		p.pos++
	}
	end = p.pos
	for end > start && isBlank(p.src[end-1]) {
		end--
	}
	return start, end
}

// scanPlain scans a plain scalar, folding continuation lines indented
// more than parent (any indentation in flow context).
func (p *parser) scanPlain(parent int, flow bool) string {
	s, e := p.scanPlainLine(flow)
	b := append([]byte(nil), p.src[s:e]...)
	for p.peek(0) == '\n' {
		q, breaks := p.pos, 0
		for q < len(p.src) && p.src[q] == '\n' {
			breaks++
			q++
			for q < len(p.src) && isBlank(p.src[q]) {
				q++
			}
		}
		if q >= len(p.src) || p.src[q] == '#' || p.isDocMarker(q) || !flow && p.col(q) <= parent {
			break
		}
		save := p.pos
		p.pos = q
		s, e := p.scanPlainLine(flow)
		if e == s {
			p.pos = save
			break
		}
		b = appendFold(b, breaks)
		b = append(b, p.src[s:e]...)
	}
	return string(b)
}

// appendFold appends what a run of line breaks folds to: a space for one,
// n-1 newlines for n.
func appendFold(b []byte, breaks int) []byte {
	if breaks == 1 {
		return append(b, ' ')
	}
	for range breaks - 1 {
		b = append(b, '\n')
	}
	return b
}

// ---- quoted scalars ----

// foldBreaks consumes line breaks and the blanks after them inside a
// quoted scalar and returns how many breaks there were.
func (p *parser) foldBreaks(start int, what string) int {
	breaks := 0
	for p.peek(0) == '\n' {
		breaks++
		p.pos++
		if p.isDocMarker(p.pos) {
			p.fail(start, "unterminated "+what)
		}
		p.skipSpaces()
	}
	return breaks
}

func (p *parser) scanSingleQuoted() string {
	start := p.pos
	p.pos++
	var b []byte
	for {
		if p.eof() {
			p.fail(start, "unterminated single-quoted string")
		}
		switch c := p.src[p.pos]; c {
		case '\'':
			if p.peek(1) == '\'' {
				b = append(b, '\'')
				p.pos += 2
				continue
			}
			p.pos++
			return string(b)
		case ' ', '\t':
			j := p.pos
			for j < len(p.src) && isBlank(p.src[j]) {
				j++
			}
			if j < len(p.src) && p.src[j] != '\n' {
				b = append(b, p.src[p.pos:j]...)
			}
			p.pos = j
		case '\n':
			b = appendFold(b, p.foldBreaks(start, "single-quoted string"))
		default:
			b = append(b, c)
			p.pos++
		}
	}
}

var simpleEscapes = map[byte]string{
	'0': "\x00", 'a': "\a", 'b': "\b", 't': "\t", '\t': "\t", 'n': "\n", 'v': "\v",
	'f': "\f", 'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"", '/': "/", '\\': "\\",
	'N': "", '_': " ", 'L': " ", 'P': " ",
}

func (p *parser) scanDoubleQuoted() string {
	start := p.pos
	p.pos++
	var b []byte
	for {
		if p.eof() {
			p.fail(start, "unterminated double-quoted string")
		}
		switch c := p.src[p.pos]; c {
		case '"':
			p.pos++
			return string(b)
		case '\\':
			esc := p.pos
			p.pos++
			e := p.peek(0)
			if e == '\n' { // an escaped line break joins the lines
				p.pos++
				p.skipSpaces()
				for p.peek(0) == '\n' {
					b = append(b, '\n')
					p.pos++
					p.skipSpaces()
				}
				if p.isDocMarker(p.pos) {
					p.fail(start, "unterminated double-quoted string")
				}
				continue
			}
			if s, ok := simpleEscapes[e]; ok {
				b = append(b, s...)
				p.pos++
				continue
			}
			size := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			if size == 0 || p.pos+1+size > len(p.src) {
				p.fail(esc, "invalid escape sequence in a double-quoted string")
			}
			v, err := strconv.ParseUint(string(p.src[p.pos+1:p.pos+1+size]), 16, 32)
			if err != nil || !utf8.ValidRune(rune(v)) {
				p.fail(esc, "invalid escape sequence in a double-quoted string")
			}
			b = utf8.AppendRune(b, rune(v))
			p.pos += 1 + size
		case ' ', '\t':
			j := p.pos
			for j < len(p.src) && isBlank(p.src[j]) {
				j++
			}
			if j < len(p.src) && p.src[j] != '\n' {
				b = append(b, p.src[p.pos:j]...)
			}
			p.pos = j
		case '\n':
			b = appendFold(b, p.foldBreaks(start, "double-quoted string"))
		default:
			b = append(b, c)
			p.pos++
		}
	}
}

// ---- block scalars ----

type blockLine struct {
	text     string
	hasBreak bool
}

func (p *parser) parseBlockScalar(parent int) *Node {
	n := p.newNode(ScalarNode, p.pos)
	n.Tag = TagStr
	literal := p.src[p.pos] == '|'
	n.Style = Folded
	if literal {
		n.Style = Literal
	}
	p.pos++
	chomp, increment := byte(0), 0
	for range 2 {
		switch c := p.peek(0); {
		case (c == '+' || c == '-') && chomp == 0:
			chomp = c
			p.pos++
		case c >= '1' && c <= '9' && increment == 0:
			increment = int(c - '0')
			p.pos++
		case c == '0':
			p.fail(p.pos, "a block scalar's indentation indicator must be 1 to 9")
		}
	}
	if c := p.peek(0); !isBlankOrEnd(c) {
		p.fail(p.pos, "invalid block scalar header (expected | or > with optional + - and 1-9)")
	}
	p.skipSpaces()
	if !p.atLineEnd() {
		p.fail(p.pos, "unexpected content after a block scalar header")
	}
	p.skipLine()
	if p.eof() {
		return n
	}
	p.pos++

	indent := 0
	if increment > 0 {
		indent = max(parent, 0) + increment
	} else {
		// The first non-empty line sets the indentation.
		maxEmpty := 0
		for q := p.pos; q < len(p.src); {
			k := 0
			for q+k < len(p.src) && p.src[q+k] == ' ' {
				k++
			}
			if q+k < len(p.src) && p.src[q+k] != '\n' {
				indent = k
				if maxEmpty > k && k > parent {
					p.fail(q, "the leading empty lines of a block scalar are indented more than its first line")
				}
				break
			}
			maxEmpty = max(maxEmpty, k)
			q += k + 1
		}
		indent = max(indent, parent+1, 1)
	}

	var lines []blockLine
	for p.pos < len(p.src) {
		q, k := p.pos, 0
		for k < indent && q+k < len(p.src) && p.src[q+k] == ' ' {
			k++
		}
		if k < indent {
			j := q + k
			for j < len(p.src) && p.src[j] == ' ' {
				j++
			}
			if j < len(p.src) && p.src[j] != '\n' {
				break // less indented content ends the scalar
			}
			lines = append(lines, blockLine{hasBreak: j < len(p.src)})
			p.pos = min(j+1, len(p.src))
			continue
		}
		if p.isDocMarker(q) {
			break
		}
		end := q + k
		for end < len(p.src) && p.src[end] != '\n' {
			end++
		}
		lines = append(lines, blockLine{text: string(p.src[q+k : end]), hasBreak: end < len(p.src)})
		p.pos = min(end+1, len(p.src))
	}
	// p.pos is at the start of the line that ended the scalar.

	last := -1
	for i, l := range lines {
		if l.text != "" {
			last = i
		}
	}
	var b strings.Builder
	if last >= 0 {
		if literal {
			for i := 0; i <= last; i++ {
				if i > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(lines[i].text)
			}
		} else {
			foldLines(&b, lines[:last+1])
		}
	}
	trailing := 0
	for _, l := range lines[last+1:] {
		if l.hasBreak {
			trailing++
		}
	}
	finalBreak := last >= 0 && lines[last].hasBreak
	switch chomp {
	case '-':
	case '+':
		if finalBreak {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("\n", trailing))
	default:
		if finalBreak {
			b.WriteByte('\n')
		}
	}
	n.Value = b.String()
	return n
}

// foldLines writes the lines of a folded block scalar (the last one is not
// empty): a single break between two lines that do not start with a blank
// becomes a space; other breaks are kept.
func foldLines(b *strings.Builder, lines []blockLine) {
	i := 0
	for i < len(lines) && lines[i].text == "" {
		b.WriteByte('\n')
		i++
	}
	moreIndented := func(s string) bool { return s != "" && isBlank(s[0]) }
	prev := lines[i].text
	b.WriteString(prev)
	for i++; i < len(lines); {
		empties := 0
		for lines[i].text == "" {
			empties++
			i++
		}
		next := lines[i].text
		switch {
		case !moreIndented(prev) && !moreIndented(next) && empties == 0:
			b.WriteByte(' ')
		case !moreIndented(prev) && !moreIndented(next):
			b.WriteString(strings.Repeat("\n", empties))
		default:
			b.WriteString(strings.Repeat("\n", empties+1))
		}
		b.WriteString(next)
		prev = next
		i++
	}
}

// ---- flow collections ----

// skipFlowSpace skips blanks, line breaks and comments inside a flow
// collection.
func (p *parser) skipFlowSpace() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ' ' || c == '\t' || c == '\n' {
			p.pos++
			continue
		}
		if p.commentAt(p.pos) {
			p.skipLine()
			continue
		}
		return
	}
}

func (p *parser) checkFlowOpen(start int, what string) {
	if p.eof() || p.isDocMarker(p.pos) {
		p.fail(start, "unterminated flow "+what)
	}
}

// parseFlowNode parses a node inside (or starting) a flow collection.
func (p *parser) parseFlowNode() *Node {
	p.enter(p.pos)
	defer p.leave()
	var pr props
	if c := p.peek(0); c == '&' || c == '!' {
		pr = p.parseProps(true)
		p.skipFlowSpace()
		if c := p.peek(0); c == ',' || c == ']' || c == '}' || c == 0 || c == ':' && (isBlankOrEnd(p.peek(1)) || isFlowIndicator(p.peek(1))) {
			n := p.emptyNode(pr.pos)
			p.applyProps(n, pr)
			return n
		}
	}
	var n *Node
	switch p.peek(0) {
	case '[':
		n = p.parseFlowSeq()
	case '{':
		n = p.parseFlowMap()
	case '#':
		p.fail(p.pos, "a comment must be separated from other tokens by a space")
	default:
		n = p.parseScalarOrAlias(-1, true, pr)
	}
	p.applyProps(n, pr)
	return n
}

// flowValueFollows reports whether a ':' at p.pos separates key from
// value after key (adjacent ':' is allowed after JSON-like keys).
func (p *parser) flowValueFollows(key *Node) bool {
	if p.peek(0) != ':' {
		return false
	}
	n := p.peek(1)
	return isBlankOrEnd(n) || isFlowIndicator(n) || key.Style == DoubleQuoted || key.Style == SingleQuoted || key.Style == Flow
}

func (p *parser) flowKey(key *Node, start int) {
	if key.Kind == MappingNode || key.Kind == SequenceNode {
		p.unsupported(start, "collections as mapping keys are not supported")
	}
}

func (p *parser) parseFlowSeq() *Node {
	start := p.pos
	n := p.newNode(SequenceNode, start)
	n.Tag, n.Style = TagSeq, Flow
	p.pos++
	for {
		p.skipFlowSpace()
		p.checkFlowOpen(start, "sequence (missing ']')")
		switch c := p.peek(0); {
		case c == ']':
			p.pos++
			return n
		case c == ',':
			p.fail(p.pos, "unexpected ',' in a flow sequence")
		case c == '?' && isBlankOrEnd(p.peek(1)):
			p.unsupported(p.pos, "explicit mapping keys ('? key') are not supported")
		case c == ':' && (isBlankOrEnd(p.peek(1)) || isFlowIndicator(p.peek(1))):
			p.unsupported(p.pos, "empty mapping keys are not supported")
		}
		itemPos := p.pos
		item := p.parseFlowNode()
		p.skipFlowSpace()
		if p.flowValueFollows(item) {
			p.flowKey(item, itemPos)
			p.pos++
			p.skipFlowSpace()
			var v *Node
			if c := p.peek(0); c == ',' || c == ']' {
				v = p.emptyNode(p.pos)
			} else {
				p.checkFlowOpen(start, "sequence (missing ']')")
				v = p.parseFlowNode()
			}
			m := p.newNode(MappingNode, itemPos)
			m.Tag, m.Style = TagMap, Flow
			m.Pairs = []Pair{{Key: item, Value: v}}
			item = m
			p.skipFlowSpace()
		}
		n.Items = append(n.Items, item)
		p.checkFlowOpen(start, "sequence (missing ']')")
		switch p.peek(0) {
		case ',':
			p.pos++
		case ']':
		default:
			p.fail(p.pos, "expected ',' or ']' in a flow sequence")
		}
	}
}

func (p *parser) parseFlowMap() *Node {
	start := p.pos
	n := p.newNode(MappingNode, start)
	n.Tag, n.Style = TagMap, Flow
	p.pos++
	for {
		p.skipFlowSpace()
		p.checkFlowOpen(start, "mapping (missing '}')")
		switch c := p.peek(0); {
		case c == '}':
			p.pos++
			return n
		case c == ',':
			p.fail(p.pos, "unexpected ',' in a flow mapping")
		case c == '?' && isBlankOrEnd(p.peek(1)):
			p.unsupported(p.pos, "explicit mapping keys ('? key') are not supported")
		case c == ':' && (isBlankOrEnd(p.peek(1)) || isFlowIndicator(p.peek(1))):
			p.unsupported(p.pos, "empty mapping keys are not supported")
		}
		keyPos := p.pos
		key := p.parseFlowNode()
		p.flowKey(key, keyPos)
		p.skipFlowSpace()
		var v *Node
		if p.flowValueFollows(key) {
			p.pos++
			p.skipFlowSpace()
			if c := p.peek(0); c == ',' || c == '}' {
				v = p.emptyNode(p.pos)
			} else {
				p.checkFlowOpen(start, "mapping (missing '}')")
				v = p.parseFlowNode()
			}
			p.skipFlowSpace()
		} else {
			v = p.emptyNode(p.pos)
		}
		n.Pairs = append(n.Pairs, Pair{Key: key, Value: v})
		p.checkFlowOpen(start, "mapping (missing '}')")
		switch p.peek(0) {
		case ',':
			p.pos++
		case '}':
		default:
			p.fail(p.pos, "expected ',' or '}' in a flow mapping")
		}
	}
}
