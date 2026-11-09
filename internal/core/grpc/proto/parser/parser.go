// Package parser parses .proto files (proto2, proto3 and editions) into
// the syntax tree of package ast without protoc, and runs the checks that
// need no other file (Check).
//
// Parse never panics and does work linear in the file size: every recovery
// step consumes at least one token or ends the enclosing construct. Nesting
// of bodies and message literals is capped (Config.MaxDepth), and so is the
// error list (Config.MaxErrors).
//
// Recovery rules, in short:
//   - A missing identifier becomes a placeholder *ast.Ident; other missing
//     pieces are nil.
//   - After an unexpected token the statement is abandoned. If the token is
//     on a later line than the last consumed one it starts the next
//     statement; otherwise tokens are skipped through the next ";" or over
//     the next "{ ... }" block, stopping before a "}".
//   - A missing ";" at the end of a line is reported right after the last
//     token, and parsing continues with the next line.
//   - A "}" ends the innermost open body; a stray "}" at the top level is
//     reported and skipped.
//
// See docs/proto.md.
package parser

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/lexer"
)

// Default limits used by Parse.
const (
	DefaultMaxDepth  = 128
	DefaultMaxErrors = 100
)

// Config sets parser limits. Zero fields take the defaults.
type Config struct {
	// MaxDepth caps the nesting of message, enum, service, oneof, extend,
	// group and rpc bodies and of message and list literals.
	MaxDepth int
	// MaxErrors caps the number of errors returned, including the final
	// ErrTooManyErrors.
	MaxErrors int
}

// Parse parses src, the contents of the file name, with the default limits.
// name is only used in ast.File.Name and error messages. The file is never
// nil; errors are sorted by position, except that a final ErrTooManyErrors
// stays last.
func Parse(name, src string) (*ast.File, []Error) {
	return Config{}.Parse(name, src)
}

// Parse parses src with the limits in c.
func (c Config) Parse(name, src string) (*ast.File, []Error) {
	f := ast.NewFile(name, src)
	p := &parser{
		src:        src,
		file:       f,
		prev:       -1,
		pendingIdx: -1,
		maxDepth:   cmp.Or(c.MaxDepth, DefaultMaxDepth),
		maxErrors:  max(cmp.Or(c.MaxErrors, DefaultMaxErrors), 1),
	}
	for _, t := range lexer.Tokenize(src, 0) {
		if t.Kind == lexer.Comment {
			f.Comments = append(f.Comments, ast.Comment{
				Loc: ast.Loc{Start: t.Start, End: t.End}, Block: strings.HasPrefix(t.Text(src), "/*"),
			})
			continue
		}
		p.toks = append(p.toks, t)
	}
	p.parseFile()

	errs := p.errs
	var last []Error
	if n := len(errs); n > 0 && errs[n-1].Code == ErrTooManyErrors {
		errs, last = errs[:n-1], errs[n-1:]
	}
	slices.SortStableFunc(errs, func(a, b Error) int { return cmp.Compare(a.Start, b.Start) })
	errs = append(errs, last...)
	for i := range errs {
		pos := f.Position(errs[i].Start)
		errs[i].Line, errs[i].Column, errs[i].File = pos.Line, pos.Column, name
	}
	return f, errs
}

type parser struct {
	src  string
	file *ast.File
	toks []lexer.Token // without comments; toks[len-1] is EOF
	i    int           // index of the next token
	prev int           // index of the last consumed token, -1 before any

	// Leading and detached comments for the statement starting at token
	// pendingIdx, collected after the last end of a declaration.
	pendingIdx int
	pending    ast.Comments

	depth, maxDepth int
	tooDeep         bool

	errs      []Error
	maxErrors int
}

// ---- tokens ----

// peek returns the next significant token, reporting and skipping lexical
// error tokens in front of it.
func (p *parser) peek() lexer.Token {
	for p.toks[p.i].Kind == lexer.Error {
		t := p.toks[p.i]
		p.report(Error{Code: ErrLexical, LexCode: t.Err, Start: t.Start, End: t.End, Message: t.Message})
		p.i++
	}
	return p.toks[p.i]
}

// lookahead returns the n-th significant token after the next one, without
// reporting or consuming anything.
func (p *parser) lookahead(n int) lexer.Token {
	j := p.i
	for {
		for p.toks[j].Kind == lexer.Error {
			j++
		}
		if n == 0 || p.toks[j].Kind == lexer.EOF {
			return p.toks[j]
		}
		n--
		j++
	}
}

// next consumes and returns the next significant token. At the end of input
// it returns EOF without consuming.
func (p *parser) next() lexer.Token {
	t := p.peek()
	if t.Kind != lexer.EOF {
		p.prev = p.i
		p.i++
	}
	return t
}

func (p *parser) prevEnd() int {
	if p.prev < 0 {
		return 0
	}
	return p.toks[p.prev].End
}

func (p *parser) text(t lexer.Token) string { return t.Text(p.src) }

// isWord reports whether t is the identifier w.
func (p *parser) isWord(t lexer.Token, w string) bool {
	return t.Kind == lexer.Ident && t.End-t.Start == len(w) && p.src[t.Start:t.End] == w
}

// expect consumes a token of kind k or reports want.
func (p *parser) expect(k lexer.Kind, want string) bool {
	if p.peek().Kind == k {
		p.next()
		return true
	}
	p.unexpected(want)
	return false
}

// ---- errors ----

func (p *parser) report(e Error) {
	switch n := len(p.errs); {
	case n > p.maxErrors-1:
		return
	case e.Code != ErrUnclosed && p.reportedAt(e.Start):
		return // one error per position: the first explains the rest
	case n == p.maxErrors-1:
		e = Error{Code: ErrTooManyErrors, Start: e.Start, End: e.End,
			Message: "too many errors; stopped reporting after " + strconv.Itoa(n)}
	}
	p.errs = append(p.errs, e)
}

func (p *parser) reportedAt(off int) bool {
	for i := len(p.errs) - 1; i >= max(0, len(p.errs)-4); i-- {
		if p.errs[i].Start == off {
			return true
		}
	}
	return false
}

func (p *parser) errorf(code Code, start, end int, format string, args ...any) {
	p.report(Error{Code: code, Start: start, End: end, Message: fmt.Sprintf(format, args...)})
}

// unexpected reports the next token where want was expected, without
// consuming it.
func (p *parser) unexpected(want string) {
	t := p.peek()
	p.errorf(ErrUnexpectedToken, t.Start, t.End, "expected %s, found %s", want, p.describe(t))
}

func (p *parser) describe(t lexer.Token) string {
	switch t.Kind {
	case lexer.EOF:
		return "end of input"
	case lexer.String:
		if r := []rune(p.text(t)); len(r) > 32 {
			return "string " + string(r[:29]) + "..."
		}
		return "string " + p.text(t)
	case lexer.Int, lexer.Float:
		return "number " + p.text(t)
	}
	return strconv.Quote(p.text(t))
}

// resync abandons the current statement after an error: a token on a later
// line than the last consumed one starts the next statement; otherwise the
// rest of the statement is skipped.
func (p *parser) resync() {
	t := p.peek()
	if t.Kind == lexer.EOF || t.Kind == lexer.BraceR || p.prev >= 0 && t.Line > p.toks[p.prev].Line {
		return
	}
	p.skipStatement()
}

// skipStatement skips tokens through the next ";" or over the next
// "{ ... }" block, stopping before a "}" and at the end of input.
func (p *parser) skipStatement() {
	for {
		switch p.peek().Kind {
		case lexer.EOF, lexer.BraceR:
			return
		case lexer.Semicolon:
			p.next()
			return
		case lexer.BraceL:
			open := p.next()
			p.skipRest(open, lexer.BraceL, lexer.BraceR)
			return
		}
		p.next()
	}
}

// skipRest skips to the delimiter closing open, which was consumed, without
// recursion. It returns the closer's offset, or -1 at the end of input.
func (p *parser) skipRest(open lexer.Token, openKind, closeKind lexer.Kind) int {
	depth := 1
	for {
		t := p.peek()
		switch t.Kind {
		case lexer.EOF:
			p.errorf(ErrUnclosed, open.Start, open.End, "%q is not closed", p.text(open))
			return -1
		case openKind:
			depth++
		case closeKind:
			depth--
		}
		p.next()
		if depth == 0 {
			return t.Start
		}
	}
}

// enter counts one nesting level for the block opened by open, or reports
// ErrTooDeep (once) and returns false.
func (p *parser) enter(open lexer.Token) bool {
	if p.depth >= p.maxDepth {
		if !p.tooDeep {
			p.tooDeep = true
			p.errorf(ErrTooDeep, open.Start, open.End, "nesting deeper than %d levels", p.maxDepth)
		}
		return false
	}
	p.depth++
	return true
}

// ---- comments ----

// consumeDeclEnd consumes the ";", "{" or "}" ending or opening a
// declaration, returns the trailing comment after it and keeps the comments
// for the statement that follows.
func (p *parser) consumeDeclEnd() (trailing string) {
	t := p.next()
	nt := p.toks[p.i] // raw: a lexical error token also takes the comments
	closer := nt.Kind == lexer.BraceR || nt.Kind == lexer.BracketR || nt.Kind == lexer.ParenR
	tr, det, lead := gapComments(p.src[t.End:nt.Start], false, closer, nt.Kind == lexer.EOF)
	p.pendingIdx = p.i
	p.pending = ast.Comments{Leading: lead, Detached: det}
	return tr
}

// takeComments gives the pending leading and detached comments to the
// statement starting at token idx, if they were collected for it.
func (p *parser) takeComments(c *ast.Comments, idx int) {
	if idx == p.pendingIdx {
		c.Leading, c.Detached = p.pending.Leading, p.pending.Detached
		p.pendingIdx = -1
	}
}

// semicolon ends a statement with ";", setting its trailing comment.
func (p *parser) semicolon(c *ast.Comments) {
	t := p.peek()
	if t.Kind == lexer.Semicolon {
		c.Trailing = p.consumeDeclEnd()
		return
	}
	if t.Kind == lexer.EOF || t.Kind == lexer.BraceR || p.prev >= 0 && t.Line > p.toks[p.prev].Line {
		end := p.prevEnd()
		p.errorf(ErrUnexpectedToken, end, end, `expected ";", found %s`, p.describe(t))
		return
	}
	p.unexpected(`";"`)
	p.skipStatement()
}

// ---- file ----

func (p *parser) parseFile() {
	f := p.file
	first := p.toks[0]
	gap := strings.TrimPrefix(p.src[:first.Start], "\xef\xbb\xbf")
	closer := first.Kind == lexer.BraceR || first.Kind == lexer.BracketR || first.Kind == lexer.ParenR
	_, det, lead := gapComments(gap, true, closer, first.Kind == lexer.EOF)
	p.pendingIdx, p.pending = 0, ast.Comments{Leading: lead, Detached: det}

	for {
		t := p.peek()
		switch t.Kind {
		case lexer.EOF:
			return
		case lexer.BraceR:
			p.unexpected("top-level declaration")
			p.next()
			continue
		}
		if d := p.parseTopLevel(); d != nil {
			f.Decls = append(f.Decls, d)
		}
	}
}

func (p *parser) parseTopLevel() ast.Decl {
	t := p.peek()
	switch t.Kind {
	case lexer.Semicolon:
		return p.parseEmpty()
	case lexer.Ident:
		switch p.text(t) {
		case "syntax", "edition":
			return p.parseSyntax()
		case "package":
			return p.parsePackage()
		case "import":
			return p.parseImport()
		case "option":
			return p.parseOption()
		case "message":
			return p.parseMessage()
		case "enum":
			return p.parseEnum()
		case "export", "local":
			if p.visibility() {
				return p.parseVisible()
			}
		case "service":
			return p.parseService()
		case "extend":
			return p.parseExtend()
		}
	}
	p.unexpected("top-level declaration (message, enum, service, extend, option, import or package)")
	p.skipStatement()
	return nil
}

func (p *parser) parseEmpty() ast.Decl {
	t := p.peek()
	p.consumeDeclEnd()
	return &ast.Empty{Loc: ast.Loc{Start: t.Start, End: t.End}}
}

func (p *parser) parseSyntax() ast.Decl {
	idx := p.i
	kw := p.next()
	s := &ast.Syntax{Loc: loc(kw), Edition: p.text(kw) == "edition"}
	p.takeComments(&s.Comments, idx)
	defer func() { s.End = p.prevEnd() }()
	if len(p.file.Decls) > 0 {
		p.errorf(ErrMisplaced, kw.Start, kw.End, "the %s statement must be the first statement in the file", p.text(kw))
	}
	if !p.expect(lexer.Equals, `"="`) {
		p.resync()
		return s
	}
	if p.peek().Kind != lexer.String {
		if s.Edition {
			p.unexpected(`a quoted edition such as "2023"`)
		} else {
			p.unexpected(`a quoted syntax such as "proto3"`)
		}
		p.resync()
		return s
	}
	s.Value = p.parseString()
	p.semicolon(&s.Comments)
	return s
}

func (p *parser) parsePackage() ast.Decl {
	idx := p.i
	kw := p.next()
	d := &ast.Package{Loc: loc(kw)}
	p.takeComments(&d.Comments, idx)
	defer func() { d.End = p.prevEnd() }()
	if d.Name = p.parseFullName(); d.Name == nil {
		p.unexpected("package name")
		p.resync()
		return d
	}
	if d.Name.LeadingDot {
		p.errorf(ErrUnexpectedToken, d.Name.Start, d.Name.Start+1, `a package name cannot start with "."`)
	}
	p.semicolon(&d.Comments)
	return d
}

func (p *parser) parseImport() ast.Decl {
	idx := p.i
	kw := p.next()
	d := &ast.Import{Loc: loc(kw)}
	p.takeComments(&d.Comments, idx)
	defer func() { d.End = p.prevEnd() }()
	if t := p.peek(); t.Kind == lexer.Ident && p.lookahead(1).Kind == lexer.String {
		switch p.text(t) {
		case "public":
			d.Kind = ast.ImportPublic
		case "weak":
			d.Kind = ast.ImportWeak
		case "option":
			d.Kind = ast.ImportOption
		}
		if d.Kind != ast.ImportDefault {
			p.next()
		}
	}
	if p.peek().Kind != lexer.String {
		p.unexpected("a quoted import path")
		p.resync()
		return d
	}
	d.Path = p.parseString()
	p.semicolon(&d.Comments)
	return d
}

// ---- names and literals ----

func loc(t lexer.Token) ast.Loc { return ast.Loc{Start: t.Start, End: t.End} }

func (p *parser) missingIdent() *ast.Ident {
	end := p.prevEnd()
	return &ast.Ident{Loc: ast.Loc{Start: end, End: end}}
}

// expectIdent consumes an identifier, or reports want and returns a
// placeholder.
func (p *parser) expectIdent(want string) (*ast.Ident, bool) {
	if t := p.peek(); t.Kind == lexer.Ident {
		p.next()
		return &ast.Ident{Loc: loc(t), Value: p.text(t)}, true
	}
	p.unexpected(want)
	return p.missingIdent(), false
}

// parseFullName parses [.]ident(.ident)*. It returns nil, consuming nothing,
// unless the next token is "." or an identifier.
func (p *parser) parseFullName() *ast.FullName {
	t := p.peek()
	if t.Kind != lexer.Dot && t.Kind != lexer.Ident {
		return nil
	}
	n := &ast.FullName{Loc: loc(t)}
	if t.Kind == lexer.Dot {
		p.next()
		n.LeadingDot = true
	}
	for {
		id, ok := p.expectIdent("identifier")
		n.Parts = append(n.Parts, id)
		if !ok || p.peek().Kind != lexer.Dot {
			break
		}
		p.next()
	}
	n.End = p.prevEnd()
	return n
}

// parseString parses one or more adjacent string literals.
func (p *parser) parseString() *ast.StringLit {
	t := p.next()
	s := &ast.StringLit{Loc: loc(t)}
	var b strings.Builder
	for {
		v, _ := lexer.StringValue(p.text(t)) // the lexer validated it
		b.WriteString(v)
		s.End = t.End
		if p.peek().Kind != lexer.String {
			break
		}
		t = p.next()
	}
	s.Value = b.String()
	return s
}

// parseIntLit parses an optionally negative integer. ok is false, with an
// error reported and nothing consumed but the sign, when there is no
// integer; the literal is nil (ok true) for an integer beyond 64 bits.
func (p *parser) parseIntLit(want string) (lit *ast.IntLit, ok bool) {
	start := p.peek()
	neg := start.Kind == lexer.Minus
	if neg {
		p.next()
	}
	t := p.peek()
	if t.Kind != lexer.Int {
		p.unexpected(want)
		return nil, false
	}
	p.next()
	v, err := lexer.IntValue(p.text(t))
	if err != nil {
		p.errorf(ErrInvalidLiteral, start.Start, t.End, "integer %s is out of range", p.src[start.Start:t.End])
		return nil, true
	}
	return &ast.IntLit{Loc: ast.Loc{Start: start.Start, End: t.End}, Negative: neg, Raw: p.text(t), Value: v}, true
}

// parseOptionValue parses a constant: a signed number or identifier, a
// string, or a message literal. In text format (inside a message literal)
// it also accepts list literals, "<...>" messages, "f" float suffixes and
// any negated identifier.
func (p *parser) parseOptionValue(text bool) (ast.Value, bool) {
	start := p.peek()
	neg := start.Kind == lexer.Minus
	if neg {
		p.next()
	}
	t := p.peek()
	span := ast.Loc{Start: start.Start, End: t.End}
	notNegative := func(what string) {
		if neg {
			p.errorf(ErrInvalidLiteral, start.Start, start.End, `"-" is not allowed before %s`, what)
		}
	}
	switch t.Kind {
	case lexer.Int:
		p.next()
		raw := p.text(t)
		v, err := lexer.IntValue(raw)
		if err == nil && (!neg || v <= 1<<63) {
			return &ast.IntLit{Loc: span, Negative: neg, Raw: raw, Value: v}, true
		}
		f, _ := lexer.FloatValue(raw) // too large for an integer: a double, as in protoc
		if neg {
			f = -f
		}
		return &ast.FloatLit{Loc: span, Negative: neg, Raw: raw, Value: f}, true
	case lexer.Float:
		p.next()
		raw := p.text(t)
		if !text && strings.ContainsAny(raw, "fF") {
			p.errorf(ErrInvalidLiteral, t.Start, t.End, `the "f" suffix is only allowed in message literals`)
		}
		f, _ := lexer.FloatValue(raw)
		if neg {
			f = -f
		}
		return &ast.FloatLit{Loc: span, Negative: neg, Raw: raw, Value: f}, true
	case lexer.Ident:
		p.next()
		name := p.text(t)
		if neg && !text && name != "inf" && name != "nan" {
			p.errorf(ErrInvalidLiteral, start.Start, t.End, `"-" is only allowed before a number, inf or nan`)
		}
		return &ast.IdentLit{Loc: span, Negative: neg, Value: name}, true
	case lexer.String:
		notNegative("a string")
		return p.parseString(), true
	case lexer.BraceL:
		notNegative("a message")
		return p.parseMessageLit(), true
	case lexer.AngleL:
		if text {
			notNegative("a message")
			return p.parseMessageLit(), true
		}
	case lexer.BracketL:
		if text {
			notNegative("a list")
			return p.parseListLit(), true
		}
	}
	p.unexpected("a value")
	return nil, false
}

// parseMessageLit parses a text-format message: "{ ... }" or "< ... >".
func (p *parser) parseMessageLit() *ast.MessageLit {
	open := p.next()
	closer := lexer.BraceR
	if open.Kind == lexer.AngleL {
		closer = lexer.AngleR
	}
	m := &ast.MessageLit{Loc: loc(open)}
	defer func() { m.End = p.prevEnd() }()
	if !p.enter(open) {
		p.skipRest(open, open.Kind, closer)
		return m
	}
	defer func() { p.depth-- }()
	for {
		t := p.peek()
		switch t.Kind {
		case closer:
			p.next()
			return m
		case lexer.EOF, lexer.BraceR, lexer.AngleR:
			p.errorf(ErrUnclosed, open.Start, open.End, "%q is not closed", p.text(open))
			return m
		case lexer.Comma, lexer.Semicolon:
			p.next()
		case lexer.Ident, lexer.BracketL:
			if fl := p.parseMessageLitField(); fl != nil {
				m.Fields = append(m.Fields, fl)
			}
		default:
			p.unexpected("a field name")
			p.next()
		}
	}
}

// parseMessageLitField parses "name: value", "name { ... }",
// "[ext.name]: value" or "[type.url/pkg.Type] { ... }".
func (p *parser) parseMessageLitField() *ast.MessageLitField {
	t := p.peek()
	fl := &ast.MessageLitField{Loc: loc(t)}
	defer func() { fl.End = p.prevEnd() }()
	ref := &ast.FieldRef{Loc: loc(t)}
	fl.Name = ref
	if t.Kind == lexer.Ident {
		p.next()
		ref.Name = &ast.FullName{Loc: loc(t), Parts: []*ast.Ident{{Loc: loc(t), Value: p.text(t)}}}
	} else {
		p.next() // "["
		ref.Kind = ast.FieldRefExtension
		if ref.Name = p.parseFullName(); ref.Name == nil {
			p.unexpected("an extension name or type URL")
			ref.End = p.prevEnd()
			return fl
		}
		for p.peek().Kind == lexer.Slash {
			if ref.Kind == ast.FieldRefAny {
				ref.URLPrefix += "/"
			}
			ref.Kind = ast.FieldRefAny
			ref.URLPrefix += ref.Name.String()
			p.next()
			if ref.Name = p.parseFullName(); ref.Name == nil {
				p.unexpected("a type name")
				ref.End = p.prevEnd()
				return fl
			}
		}
		if !p.expect(lexer.BracketR, `"]"`) {
			ref.End = p.prevEnd()
			return fl
		}
	}
	ref.End = p.prevEnd()

	colon := p.peek().Kind == lexer.Colon
	if colon {
		p.next()
	}
	switch k := p.peek().Kind; {
	case k == lexer.BraceL || k == lexer.AngleL:
		fl.Value = p.parseMessageLit()
		return fl
	case k == lexer.BracketL && colon:
		fl.Value = p.parseListLit()
		return fl
	case !colon:
		p.unexpected(`":" or a message`)
		return fl
	}
	fl.Value, _ = p.parseOptionValue(true)
	return fl
}

// parseListLit parses "[v, ...]" in a message literal.
func (p *parser) parseListLit() *ast.ListLit {
	open := p.next()
	l := &ast.ListLit{Loc: loc(open)}
	defer func() { l.End = p.prevEnd() }()
	if !p.enter(open) {
		p.skipRest(open, lexer.BracketL, lexer.BracketR)
		return l
	}
	defer func() { p.depth-- }()
	for {
		t := p.peek()
		switch t.Kind {
		case lexer.BracketR:
			p.next()
			return l
		case lexer.EOF, lexer.BraceR, lexer.AngleR:
			p.errorf(ErrUnclosed, open.Start, open.End, `"[" is not closed`)
			return l
		}
		if len(l.Elements) > 0 && !p.expect(lexer.Comma, `"," or "]"`) {
			// Most likely the "]" is missing: end the list here and let the
			// enclosing message literal go on with its next field.
			p.errorf(ErrUnclosed, open.Start, open.End, `"[" is not closed`)
			return l
		}
		if v, ok := p.parseOptionValue(true); ok {
			l.Elements = append(l.Elements, v)
		} else if k := p.peek().Kind; k != lexer.BracketR && k != lexer.BraceR && k != lexer.AngleR {
			p.next()
		}
	}
}

// ---- options ----

// parseOption parses an option statement.
func (p *parser) parseOption() ast.Decl {
	idx := p.i
	kw := p.next()
	o := &ast.Option{Loc: loc(kw)}
	p.takeComments(&o.Comments, idx)
	defer func() { o.End = p.prevEnd() }()
	if !p.parseOptionBody(o) {
		p.resync()
		return o
	}
	p.semicolon(&o.Comments)
	return o
}

// parseOptionBody parses "name = value" into o.
func (p *parser) parseOptionBody(o *ast.Option) bool {
	var ok bool
	if o.Name, ok = p.parseOptionName(); !ok {
		return false
	}
	if !p.expect(lexer.Equals, `"="`) {
		return false
	}
	o.Value, ok = p.parseOptionValue(false)
	return ok
}

// parseOptionName parses name, (ext.name) and dotted combinations. The name
// is nil if not even its first part is there.
func (p *parser) parseOptionName() (*ast.OptionName, bool) {
	var n *ast.OptionName
	for {
		t := p.peek()
		part := &ast.OptionNamePart{Loc: loc(t)}
		switch t.Kind {
		case lexer.ParenL:
			p.next()
			part.Extension = true
			if part.Name = p.parseFullName(); part.Name == nil {
				p.unexpected("an extension name")
				return n, false
			}
			if !p.expect(lexer.ParenR, `")"`) {
				return n, false
			}
		case lexer.Ident:
			p.next()
			part.Name = &ast.FullName{Loc: loc(t), Parts: []*ast.Ident{{Loc: loc(t), Value: p.text(t)}}}
		default:
			p.unexpected("an option name")
			return n, false
		}
		part.End = p.prevEnd()
		if n == nil {
			n = &ast.OptionName{Loc: part.Loc}
		}
		n.Parts = append(n.Parts, part)
		n.End = part.End
		if p.peek().Kind != lexer.Dot {
			return n, true
		}
		p.next()
	}
}

// parseCompactOptions parses "[name = value, ...]".
func (p *parser) parseCompactOptions() (*ast.CompactOptions, bool) {
	open := p.next()
	co := &ast.CompactOptions{Loc: loc(open)}
	defer func() { co.End = p.prevEnd() }()
	for {
		o := &ast.Option{Loc: loc(p.peek())}
		ok := p.parseOptionBody(o)
		o.End = p.prevEnd()
		if o.Name != nil {
			co.Options = append(co.Options, o)
		}
		if !ok {
			return co, false
		}
		switch p.peek().Kind {
		case lexer.Comma:
			p.next()
		case lexer.BracketR:
			p.next()
			return co, true
		default:
			p.unexpected(`"," or "]"`)
			return co, false
		}
	}
}

// ---- bodies ----

// block parses "{ elements }" after a declaration's head, setting its
// trailing comment from after the "{".
func (p *parser) block(c *ast.Comments, elem func() ast.Decl) (body []ast.Decl, rbrace int) {
	open := p.peek()
	if open.Kind != lexer.BraceL {
		p.unexpected(`"{"`)
		p.resync()
		return nil, -1
	}
	c.Trailing = p.consumeDeclEnd()
	if !p.enter(open) {
		return nil, p.skipRest(open, lexer.BraceL, lexer.BraceR)
	}
	defer func() { p.depth-- }()
	for {
		t := p.peek()
		switch t.Kind {
		case lexer.EOF:
			p.errorf(ErrUnclosed, open.Start, open.End, `"{" is not closed`)
			return body, -1
		case lexer.BraceR:
			p.consumeDeclEnd()
			return body, t.Start
		}
		if d := elem(); d != nil {
			body = append(body, d)
		}
	}
}

// keywordDecl reports whether the keyword at the next token starts its
// declaration rather than a field whose type has that name
// ("message foo = 1;", "message.Foo foo = 1;").
func (p *parser) keywordDecl() bool {
	la1 := p.lookahead(1)
	return la1.Kind != lexer.Dot && la1.Kind != lexer.Equals &&
		(la1.Kind != lexer.Ident || p.lookahead(2).Kind != lexer.Equals)
}

// visibility reports whether the next tokens are "export" or "local"
// followed by "message" or "enum" and an identifier (edition 2024).
func (p *parser) visibility() bool {
	t := p.peek()
	if !p.isWord(t, "export") && !p.isWord(t, "local") {
		return false
	}
	la1 := p.lookahead(1)
	return (p.isWord(la1, "message") || p.isWord(la1, "enum")) && p.lookahead(2).Kind == lexer.Ident
}

// parseVisibility consumes an "export" or "local" keyword if visibility
// reports one.
func (p *parser) parseVisibility() (ast.Visibility, ast.Loc) {
	if !p.visibility() {
		return ast.VisibilityDefault, ast.Loc{}
	}
	t := p.next()
	if p.text(t) == "export" {
		return ast.VisibilityExport, loc(t)
	}
	return ast.VisibilityLocal, loc(t)
}

// parseVisible parses a message or enum with a visibility keyword.
func (p *parser) parseVisible() ast.Decl {
	if p.isWord(p.lookahead(1), "message") {
		return p.parseMessage()
	}
	return p.parseEnum()
}

func (p *parser) parseMessage() ast.Decl {
	start := p.peek()
	idx := p.i
	vis, visLoc := p.parseVisibility()
	p.next() // message
	m := &ast.Message{Loc: loc(start), Visibility: vis, VisibilityLoc: visLoc, RBrace: -1}
	p.takeComments(&m.Comments, idx)
	m.Name, _ = p.expectIdent("a message name")
	m.Body, m.RBrace = p.block(&m.Comments, p.parseMessageElement)
	m.End = p.prevEnd()
	return m
}

func (p *parser) parseMessageElement() ast.Decl {
	t := p.peek()
	switch t.Kind {
	case lexer.Semicolon:
		return p.parseEmpty()
	case lexer.Dot:
		return p.parseField()
	case lexer.Ident:
		la1 := p.lookahead(1)
		switch p.text(t) {
		case "message":
			if p.keywordDecl() {
				return p.parseMessage()
			}
		case "enum":
			if p.keywordDecl() {
				return p.parseEnum()
			}
		case "export", "local":
			if p.visibility() {
				return p.parseVisible()
			}
		case "oneof":
			if p.keywordDecl() {
				return p.parseOneof()
			}
		case "extend":
			if la1.Kind == lexer.Dot || p.keywordDecl() {
				return p.parseExtend()
			}
		case "option":
			if la1.Kind == lexer.ParenL || la1.Kind == lexer.Ident {
				return p.parseOption()
			}
		case "extensions":
			if la1.Kind == lexer.Int || la1.Kind == lexer.Minus {
				return p.parseExtensions()
			}
		case "reserved":
			if p.reservedStart() {
				return p.parseReserved()
			}
		}
		return p.parseField()
	}
	p.unexpected("a field, message, enum, oneof, extend, extensions, reserved or option")
	p.skipStatement()
	return nil
}

// reservedStart reports whether "reserved" at the next token starts a
// reserved statement rather than a field of type "reserved".
func (p *parser) reservedStart() bool {
	switch la1 := p.lookahead(1); la1.Kind {
	case lexer.Int, lexer.Minus, lexer.String:
		return true
	case lexer.Ident:
		k := p.lookahead(2).Kind
		return k == lexer.Comma || k == lexer.Semicolon
	}
	return false
}

// parseField parses a field, map field or group, with an optional label.
func (p *parser) parseField() ast.Decl {
	start := p.peek()
	idx := p.i
	var label ast.Label
	var labelLoc ast.Loc
	if start.Kind == lexer.Ident {
		if la1 := p.lookahead(1); la1.Kind == lexer.Ident || la1.Kind == lexer.Dot {
			switch p.text(start) {
			case "optional":
				label = ast.LabelOptional
			case "required":
				label = ast.LabelRequired
			case "repeated":
				label = ast.LabelRepeated
			}
		}
		if label != ast.LabelNone {
			p.next()
			labelLoc = loc(start)
		}
	}
	t := p.peek()
	switch {
	case p.isWord(t, "map") && p.lookahead(1).Kind == lexer.AngleL:
		m := &ast.MapField{Loc: loc(start), Label: label, LabelLoc: labelLoc}
		p.takeComments(&m.Comments, idx)
		p.parseMapField(m)
		m.End = p.prevEnd()
		return m
	case p.isWord(t, "group") && p.lookahead(1).Kind == lexer.Ident && p.lookahead(2).Kind == lexer.Equals:
		g := &ast.Group{Loc: loc(start), Label: label, LabelLoc: labelLoc, RBrace: -1}
		p.takeComments(&g.Comments, idx)
		p.parseGroup(g)
		g.End = p.prevEnd()
		return g
	}

	f := &ast.Field{Loc: loc(start), Label: label, LabelLoc: labelLoc}
	p.takeComments(&f.Comments, idx)
	defer func() { f.End = p.prevEnd() }()
	if f.Type = p.parseFullName(); f.Type == nil {
		p.unexpected("a field type")
		p.resync()
		return f
	}
	var ok bool
	if f.Name, ok = p.expectIdent("a field name"); !ok {
		p.resync()
		return f
	}
	if f.Number, f.Options, ok = p.fieldTail(); !ok {
		p.resync()
		return f
	}
	p.semicolon(&f.Comments)
	return f
}

// fieldTail parses "= number [options]".
func (p *parser) fieldTail() (*ast.IntLit, *ast.CompactOptions, bool) {
	if !p.expect(lexer.Equals, `"="`) {
		return nil, nil, false
	}
	num, ok := p.parseIntLit("a field number")
	if !ok {
		return nil, nil, false
	}
	if p.peek().Kind != lexer.BracketL {
		return num, nil, true
	}
	opts, ok := p.parseCompactOptions()
	return num, opts, ok
}

func (p *parser) parseMapField(m *ast.MapField) {
	p.next() // map
	p.next() // <
	if m.KeyType = p.parseFullName(); m.KeyType == nil {
		p.unexpected("a map key type")
		p.resync()
		return
	}
	if !p.expect(lexer.Comma, `","`) {
		p.resync()
		return
	}
	if m.ValType = p.parseFullName(); m.ValType == nil {
		p.unexpected("a map value type")
		p.resync()
		return
	}
	if !p.expect(lexer.AngleR, `">"`) {
		p.resync()
		return
	}
	var ok bool
	if m.Name, ok = p.expectIdent("a field name"); !ok {
		p.resync()
		return
	}
	if m.Number, m.Options, ok = p.fieldTail(); !ok {
		p.resync()
		return
	}
	p.semicolon(&m.Comments)
}

func (p *parser) parseGroup(g *ast.Group) {
	p.next() // group
	g.Name, _ = p.expectIdent("a group name")
	var ok bool
	if g.Number, g.Options, ok = p.fieldTail(); !ok {
		p.resync()
		return
	}
	g.Body, g.RBrace = p.block(&g.Comments, p.parseMessageElement)
}

func (p *parser) parseOneof() ast.Decl {
	idx := p.i
	kw := p.next()
	o := &ast.Oneof{Loc: loc(kw), RBrace: -1}
	p.takeComments(&o.Comments, idx)
	o.Name, _ = p.expectIdent("a oneof name")
	o.Body, o.RBrace = p.block(&o.Comments, p.parseOneofElement)
	o.End = p.prevEnd()
	return o
}

func (p *parser) parseOneofElement() ast.Decl {
	t := p.peek()
	switch t.Kind {
	case lexer.Semicolon:
		return p.parseEmpty()
	case lexer.Dot:
		return p.parseField()
	case lexer.Ident:
		if k := p.lookahead(1).Kind; p.isWord(t, "option") && (k == lexer.ParenL || k == lexer.Ident) {
			return p.parseOption()
		}
		return p.parseField()
	}
	p.unexpected("a oneof field or option")
	p.skipStatement()
	return nil
}

func (p *parser) parseEnum() ast.Decl {
	start := p.peek()
	idx := p.i
	vis, visLoc := p.parseVisibility()
	p.next() // enum
	e := &ast.Enum{Loc: loc(start), Visibility: vis, VisibilityLoc: visLoc, RBrace: -1}
	p.takeComments(&e.Comments, idx)
	e.Name, _ = p.expectIdent("an enum name")
	e.Body, e.RBrace = p.block(&e.Comments, p.parseEnumElement)
	e.End = p.prevEnd()
	return e
}

func (p *parser) parseEnumElement() ast.Decl {
	t := p.peek()
	switch t.Kind {
	case lexer.Semicolon:
		return p.parseEmpty()
	case lexer.Ident:
		if k := p.lookahead(1).Kind; p.isWord(t, "option") && (k == lexer.ParenL || k == lexer.Ident) {
			return p.parseOption()
		}
		if p.isWord(t, "reserved") && p.reservedStart() {
			return p.parseReserved()
		}
		return p.parseEnumValue()
	}
	p.unexpected("an enum value, reserved or option")
	p.skipStatement()
	return nil
}

func (p *parser) parseEnumValue() ast.Decl {
	idx := p.i
	name := p.next()
	v := &ast.EnumValue{Loc: loc(name), Name: &ast.Ident{Loc: loc(name), Value: p.text(name)}}
	p.takeComments(&v.Comments, idx)
	defer func() { v.End = p.prevEnd() }()
	if !p.expect(lexer.Equals, `"="`) {
		p.resync()
		return v
	}
	var ok bool
	if v.Number, ok = p.parseIntLit("an enum value number"); !ok {
		p.resync()
		return v
	}
	if p.peek().Kind == lexer.BracketL {
		if v.Options, ok = p.parseCompactOptions(); !ok {
			p.resync()
			return v
		}
	}
	p.semicolon(&v.Comments)
	return v
}

func (p *parser) parseService() ast.Decl {
	idx := p.i
	kw := p.next()
	s := &ast.Service{Loc: loc(kw), RBrace: -1}
	p.takeComments(&s.Comments, idx)
	s.Name, _ = p.expectIdent("a service name")
	s.Body, s.RBrace = p.block(&s.Comments, p.parseServiceElement)
	s.End = p.prevEnd()
	return s
}

func (p *parser) parseServiceElement() ast.Decl {
	t := p.peek()
	switch {
	case t.Kind == lexer.Semicolon:
		return p.parseEmpty()
	case p.isWord(t, "option"):
		return p.parseOption()
	case p.isWord(t, "rpc"):
		return p.parseRPC()
	}
	p.unexpected("an rpc or option")
	p.skipStatement()
	return nil
}

func (p *parser) parseRPC() ast.Decl {
	idx := p.i
	kw := p.next()
	r := &ast.RPC{Loc: loc(kw), LBrace: -1, RBrace: -1}
	p.takeComments(&r.Comments, idx)
	defer func() { r.End = p.prevEnd() }()
	var ok bool
	if r.Name, ok = p.expectIdent("an rpc name"); !ok {
		p.resync()
		return r
	}
	if r.Input, ok = p.parseRPCType("request"); !ok {
		p.resync()
		return r
	}
	if !p.isWord(p.peek(), "returns") {
		p.unexpected(`"returns"`)
		p.resync()
		return r
	}
	p.next()
	if r.Output, ok = p.parseRPCType("response"); !ok {
		p.resync()
		return r
	}
	if t := p.peek(); t.Kind == lexer.BraceL {
		r.LBrace = t.Start
		r.Body, r.RBrace = p.block(&r.Comments, p.parseRPCElement)
		return r
	}
	p.semicolon(&r.Comments)
	return r
}

// parseRPCType parses "([stream] Type)".
func (p *parser) parseRPCType(what string) (*ast.RPCType, bool) {
	open := p.peek()
	if !p.expect(lexer.ParenL, `"("`) {
		return nil, false
	}
	rt := &ast.RPCType{Loc: loc(open)}
	defer func() { rt.End = p.prevEnd() }()
	if t := p.peek(); p.isWord(t, "stream") {
		if k := p.lookahead(1).Kind; k == lexer.Ident || k == lexer.Dot {
			p.next()
			rt.Stream, rt.StreamLoc = true, loc(t)
		}
	}
	if rt.Type = p.parseFullName(); rt.Type == nil {
		p.unexpected("a " + what + " type")
		return rt, false
	}
	return rt, p.expect(lexer.ParenR, `")"`)
}

func (p *parser) parseRPCElement() ast.Decl {
	t := p.peek()
	switch {
	case t.Kind == lexer.Semicolon:
		return p.parseEmpty()
	case p.isWord(t, "option"):
		return p.parseOption()
	}
	p.unexpected("an option")
	p.skipStatement()
	return nil
}

func (p *parser) parseExtend() ast.Decl {
	idx := p.i
	kw := p.next()
	e := &ast.Extend{Loc: loc(kw), RBrace: -1}
	p.takeComments(&e.Comments, idx)
	defer func() { e.End = p.prevEnd() }()
	if e.Extendee = p.parseFullName(); e.Extendee == nil {
		p.unexpected("the extended message type")
		p.resync()
		return e
	}
	e.Body, e.RBrace = p.block(&e.Comments, p.parseExtendElement)
	return e
}

func (p *parser) parseExtendElement() ast.Decl {
	switch t := p.peek(); t.Kind {
	case lexer.Semicolon:
		return p.parseEmpty()
	case lexer.Ident, lexer.Dot:
		return p.parseField()
	}
	p.unexpected("an extension field")
	p.skipStatement()
	return nil
}

func (p *parser) parseExtensions() ast.Decl {
	idx := p.i
	kw := p.next()
	e := &ast.Extensions{Loc: loc(kw)}
	p.takeComments(&e.Comments, idx)
	defer func() { e.End = p.prevEnd() }()
	var ok bool
	if e.Ranges, ok = p.parseRanges(); !ok {
		p.resync()
		return e
	}
	if p.peek().Kind == lexer.BracketL {
		if e.Options, ok = p.parseCompactOptions(); !ok {
			p.resync()
			return e
		}
	}
	p.semicolon(&e.Comments)
	return e
}

func (p *parser) parseReserved() ast.Decl {
	idx := p.i
	kw := p.next()
	r := &ast.Reserved{Loc: loc(kw)}
	p.takeComments(&r.Comments, idx)
	defer func() { r.End = p.prevEnd() }()
	switch p.peek().Kind {
	case lexer.String, lexer.Ident:
		for {
			t := p.peek()
			switch t.Kind {
			case lexer.String:
				s := p.parseString()
				r.Names = append(r.Names, &ast.ReservedName{Loc: s.Loc, Quoted: true, Value: s.Value})
			case lexer.Ident:
				p.next()
				r.Names = append(r.Names, &ast.ReservedName{Loc: loc(t), Value: p.text(t)})
			default:
				p.unexpected("a reserved name")
				p.resync()
				return r
			}
			if p.peek().Kind != lexer.Comma {
				break
			}
			p.next()
		}
	default:
		var ok bool
		if r.Ranges, ok = p.parseRanges(); !ok {
			p.resync()
			return r
		}
	}
	p.semicolon(&r.Comments)
	return r
}

// parseRanges parses "n", "n to m" and "n to max", separated by commas.
func (p *parser) parseRanges() ([]*ast.Range, bool) {
	var ranges []*ast.Range
	for {
		r := &ast.Range{Loc: loc(p.peek())}
		var ok bool
		r.From, ok = p.parseIntLit("a number")
		if !ok {
			return ranges, false
		}
		if p.isWord(p.peek(), "to") {
			p.next()
			if t := p.peek(); p.isWord(t, "max") {
				p.next()
				r.Max = true
			} else if r.To, ok = p.parseIntLit(`a number or "max"`); !ok {
				r.Loc.End = p.prevEnd()
				ranges = append(ranges, r)
				return ranges, false
			}
		}
		r.Loc.End = p.prevEnd()
		ranges = append(ranges, r)
		if p.peek().Kind != lexer.Comma {
			return ranges, true
		}
		p.next()
	}
}
