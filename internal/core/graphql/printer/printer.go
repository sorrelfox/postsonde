// Package printer formats GraphQL documents, executable and type system
// (SDL, sdl.go), from their syntax tree (GQL-18): Prettify lays a document
// out in the usual style and keeps its comments; Minify prints it on one
// line with as little whitespace as the grammar allows and without
// comments.
//
// Both only accept documents that parse without errors, and both preserve
// the tree: parsing the output gives the same tree as parsing the input,
// apart from positions. Prettify is idempotent.
//
// Layout of Prettify:
//   - Definitions are separated by a blank line; selections go one per line,
//     indented by two spaces.
//   - Argument and variable definition lists stay on one line unless the
//     line would be longer than MaxLineLength, or a comment lies inside
//     them; then they get one entry per line. List and object values break
//     only for comments.
//   - A comment that follows a token on its line stays at the end of that
//     line; a comment on its own line stays on its own line, indented like
//     the code after it. Blank lines are not kept, except between
//     definitions.
//   - String literals and numbers are printed as written, so escapes and
//     block string layout do not change.
//
// See docs/graphql-engine.md.
package printer

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

// MaxLineLength is the line length, in code points, beyond which Prettify
// puts the arguments or variable definitions of a list one per line.
const MaxLineLength = 80

// Indent is one level of indentation.
const Indent = "  "

// Prettify parses src and formats it. When src has syntax errors, it
// returns them and "".
func Prettify(src string) (string, []parser.Error) {
	doc, errs := parser.Parse(src)
	if len(errs) > 0 {
		return "", errs
	}
	return Print(doc), nil
}

// PrettifySchema parses src as a type system document (SDL) and formats
// it. When src has syntax errors, it returns them and "".
func PrettifySchema(src string) (string, []parser.Error) {
	doc, errs := parser.ParseSchema(src)
	if len(errs) > 0 {
		return "", errs
	}
	return Print(doc), nil
}

// Minify parses src and prints it on one line without comments. When src
// has syntax errors, it returns them and "".
func Minify(src string) (string, []parser.Error) {
	doc, errs := parser.Parse(src)
	if len(errs) > 0 {
		return "", errs
	}
	return PrintMinified(doc), nil
}

// Print formats doc like Prettify, with doc.Comments. doc must come from a
// parse without errors; placeholders of a partial tree print as nothing.
func Print(doc *ast.Document) string {
	p := &printer{src: doc.Source, comments: doc.Comments}
	p.document(doc)
	return p.b.String()
}

// PrintMinified prints doc like Minify. doc must come from a parse without
// errors.
func PrintMinified(doc *ast.Document) string {
	p := &printer{src: doc.Source, minify: true}
	p.document(doc)
	return p.b.String()
}

// Pending separators, strongest last.
const (
	sepNone = iota
	sepSpace
	sepLine
	sepBlank
)

type printer struct {
	src      string
	comments []*ast.Comment // not yet printed
	minify   bool
	// flat prints every list on one line (to measure it); it implies no
	// comments.
	flat bool

	b         strings.Builder
	lineStart int // offset in b of the current output line
	indent    int
	pending   int // separator to write before the next token
	// midUnit is set after a token and cleared by an explicit line break:
	// a line break forced by a comment in the middle of a field or header
	// indents the rest by one more level.
	midUnit bool
}

// ---- output ----

func (p *printer) write(s string) {
	p.b.WriteString(s)
	if i := strings.LastIndexAny(s, "\r\n"); i >= 0 {
		p.lineStart = p.b.Len() - len(s) + i + 1
	}
}

func (p *printer) column() int { return utf8.RuneCountInString(p.b.String()[p.lineStart:]) }

func (p *printer) lineEmpty() bool { return p.b.Len() == p.lineStart }

// space asks for a space before the next token.
func (p *printer) space() {
	if !p.minify {
		p.pending = max(p.pending, sepSpace)
	}
}

// line asks for a line break (sepLine) or a blank line (sepBlank) before the
// next token.
func (p *printer) line(sep int) {
	if !p.minify && !p.flat {
		p.pending = max(p.pending, sep)
		p.midUnit = false
	}
}

func (p *printer) indentText() string {
	n := p.indent
	if p.midUnit {
		n++
	}
	return strings.Repeat(Indent, n)
}

// token writes the token text found at offset off in the source, after
// the comments before it and the pending separator.
func (p *printer) token(off int, text string) {
	p.commentsBefore(off)
	switch {
	case p.pending >= sepLine:
		p.write(strings.Repeat("\n", p.pending-sepSpace))
		p.write(p.indentText())
	case p.pending == sepSpace:
		p.write(" ")
	case p.minify && p.b.Len() > 0 && text != "" && needSpace(p.b.String()[p.b.Len()-1], text[0]):
		p.write(" ")
	}
	p.pending = sepNone
	p.write(text)
	p.midUnit = true
}

// punct writes a separator that is not a source token (a comma between
// list entries).
func (p *printer) punct(s string) {
	p.write(s)
	p.pending = sepNone
}

// needSpace reports whether two tokens ending and starting with these bytes
// would lex differently without a space between them.
func needSpace(last, next byte) bool {
	switch {
	case isNameByte(last):
		return isNameByte(next) || next == '-' || next == '.'
	case last == '"':
		return next == '"'
	}
	return false
}

func isNameByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// commentsBefore prints the comments that start before off.
func (p *printer) commentsBefore(off int) {
	for len(p.comments) > 0 && p.comments[0].Start < off {
		c := p.comments[0]
		p.comments = p.comments[1:]
		text := strings.TrimRight(c.Text, " \t")
		if trailing(p.src, c.Start) && !p.lineEmpty() {
			p.write(" " + text)
			p.pending = max(p.pending, sepLine)
			continue
		}
		if !p.lineEmpty() {
			p.write(strings.Repeat("\n", max(p.pending, sepLine)-sepSpace))
		}
		p.write(p.indentText() + text)
		p.pending = sepLine
	}
}

// trailing reports whether the comment at off follows a token on its line.
func trailing(src string, off int) bool {
	for i := off; i > 0; {
		switch c := src[i-1]; {
		case c == '\n' || c == '\r':
			return false
		case c == ' ' || c == '\t' || c == ',':
			i--
		case strings.HasSuffix(src[:i], "\uFEFF"):
			i -= len("\uFEFF")
		default:
			return true
		}
	}
	return false
}

// hasComment reports whether a comment starts between the offsets.
func (p *printer) hasComment(from, to int) bool {
	for _, c := range p.comments {
		if c.Start >= to {
			return false
		}
		if c.Start > from {
			return true
		}
	}
	return false
}

// fits reports whether print, run on one line from the current column,
// ends within MaxLineLength.
func (p *printer) fits(print func(q *printer)) bool {
	q := &printer{src: p.src, flat: true}
	print(q)
	col := p.column()
	switch {
	case p.pending >= sepLine:
		col = utf8.RuneCountInString(p.indentText())
	case p.pending == sepSpace:
		col++
	}
	return col+utf8.RuneCountInString(q.b.String()) <= MaxLineLength
}

// ---- document ----

func (p *printer) document(doc *ast.Document) {
	for i, def := range doc.Definitions {
		if i > 0 {
			p.line(sepBlank)
		}
		switch d := def.(type) {
		case *ast.OperationDefinition:
			p.operation(d)
		case *ast.FragmentDefinition:
			p.fragment(d)
		case *ast.SchemaDefinition:
			p.schemaDefinition(d)
		case *ast.TypeDefinition:
			p.typeDefinition(d)
		case *ast.DirectiveDefinition:
			p.directiveDefinition(d)
		}
	}
	// Comments after the last definition; no line break at the end.
	p.line(sepLine)
	p.commentsBefore(len(p.src) + 1)
}

func (p *printer) operation(op *ast.OperationDefinition) {
	if op.Keyword >= 0 {
		p.token(op.Keyword, op.Operation.String())
		if op.Name != nil {
			p.space()
			p.name(op.Name)
		} else if op.VariableDefinitions != nil {
			p.space()
		}
		if op.VariableDefinitions != nil {
			p.variableDefinitions(op.VariableDefinitions)
		}
		p.directives(op.Directives)
		p.space()
	}
	p.selectionSet(op.SelectionSet)
}

func (p *printer) fragment(f *ast.FragmentDefinition) {
	p.token(f.Keyword, "fragment")
	p.space()
	p.name(f.Name)
	if f.TypeCondition != nil {
		p.space()
		p.typeCondition(f.TypeCondition)
	}
	p.directives(f.Directives)
	p.space()
	p.selectionSet(f.SelectionSet)
}

func (p *printer) typeCondition(tc *ast.TypeCondition) {
	p.token(tc.On, "on")
	p.space()
	p.name(tc.Type.Name)
}

func (p *printer) name(n *ast.Name) {
	if n != nil && !n.Missing() {
		p.token(n.Start, n.Value)
	}
}

// ---- selections ----

func (p *printer) selectionSet(set *ast.SelectionSet) {
	if set == nil {
		return
	}
	p.token(set.LBrace, "{")
	p.indent++
	for _, sel := range set.Selections {
		p.line(sepLine)
		switch s := sel.(type) {
		case *ast.Field:
			p.field(s)
		case *ast.FragmentSpread:
			p.token(s.Spread, "...")
			p.name(s.Name)
			p.directives(s.Directives)
		case *ast.InlineFragment:
			p.token(s.Spread, "...")
			if s.TypeCondition != nil {
				p.space()
				p.typeCondition(s.TypeCondition)
			}
			p.directives(s.Directives)
			p.space()
			p.selectionSet(s.SelectionSet)
		}
	}
	p.close(set.RBrace, "}")
}

// close ends a list printed one entry per line: the comments before the
// closing delimiter stay inside, indented like the entries.
func (p *printer) close(off int, text string) {
	p.line(sepLine)
	p.commentsBefore(off)
	p.indent--
	p.token(off, text)
}

func (p *printer) field(f *ast.Field) {
	if f.Alias != nil {
		p.name(f.Alias)
		p.token(f.Colon, ":")
		p.space()
	}
	p.name(f.Name)
	if f.Arguments != nil {
		p.arguments(f.Arguments)
	}
	p.directives(f.Directives)
	if f.SelectionSet != nil {
		p.space()
		p.selectionSet(f.SelectionSet)
	}
}

func (p *printer) directives(ds []*ast.Directive) {
	for _, d := range ds {
		p.space()
		p.token(d.At, "@")
		p.name(d.Name)
		if d.Arguments != nil {
			p.arguments(d.Arguments)
		}
	}
}

// ---- lists ----

// broken reports whether the list between the delimiters goes one entry
// per line: it has a comment inside, or (when long may break it) it does
// not fit on the line.
func (p *printer) broken(open, closing int, long bool, print func(q *printer)) bool {
	switch {
	case p.minify || p.flat:
		return false
	case p.hasComment(open, closing):
		return true
	}
	return long && !p.fits(print)
}

// list prints n entries between the delimiters, one per line when broken,
// else separated by ", " (a comma when minified).
func (p *printer) list(open, closing int, openText, closeText string, n int, broken bool, entry func(i int)) {
	p.token(open, openText)
	if broken {
		p.indent++
		for i := range n {
			p.line(sepLine)
			entry(i)
		}
		p.close(closing, closeText)
		return
	}
	for i := range n {
		if i > 0 {
			p.punct(",")
			p.space()
		}
		entry(i)
	}
	p.token(closing, closeText)
}

func (p *printer) arguments(l *ast.ArgumentList) {
	broken := p.broken(l.LParen, l.RParen, true, func(q *printer) { q.arguments(l) })
	p.list(l.LParen, l.RParen, "(", ")", len(l.Arguments), broken, func(i int) {
		a := l.Arguments[i]
		p.name(a.Name)
		p.token(a.Colon, ":")
		p.space()
		p.value(a.Value)
	})
}

func (p *printer) variableDefinitions(l *ast.VariableDefinitionList) {
	broken := p.broken(l.LParen, l.RParen, true, func(q *printer) { q.variableDefinitions(l) })
	p.list(l.LParen, l.RParen, "(", ")", len(l.Definitions), broken, func(i int) {
		d := l.Definitions[i]
		p.variable(d.Variable)
		p.token(d.Colon, ":")
		p.space()
		p.typeRef(d.Type)
		if d.DefaultValue != nil {
			p.space()
			p.token(d.Equals, "=")
			p.space()
			p.value(d.DefaultValue)
		}
		p.directives(d.Directives)
	})
}

func (p *printer) variable(v *ast.Variable) {
	p.token(v.Dollar, "$")
	p.name(v.Name)
}

func (p *printer) typeRef(t ast.Type) {
	switch t := t.(type) {
	case *ast.NamedType:
		p.name(t.Name)
	case *ast.ListType:
		p.token(t.LBracket, "[")
		p.typeRef(t.Type)
		p.token(t.RBracket, "]")
	case *ast.NonNullType:
		p.typeRef(t.Type)
		p.token(t.Bang, "!")
	}
}

// ---- values ----

func (p *printer) value(v ast.Value) {
	switch v := v.(type) {
	case *ast.Variable:
		p.variable(v)
	case *ast.IntValue:
		p.token(v.Start, v.Raw)
	case *ast.FloatValue:
		p.token(v.Start, v.Raw)
	case *ast.StringValue:
		// As written: escapes and block string lines stay.
		p.token(v.Start, p.src[v.Start:v.End])
	case *ast.BooleanValue:
		p.token(v.Start, strconv.FormatBool(v.Value))
	case *ast.NullValue:
		p.token(v.Start, "null")
	case *ast.EnumValue:
		p.token(v.Start, v.Value)
	case *ast.ListValue:
		broken := p.broken(v.LBracket, v.RBracket, false, nil)
		p.list(v.LBracket, v.RBracket, "[", "]", len(v.Values), broken, func(i int) { p.value(v.Values[i]) })
	case *ast.ObjectValue:
		broken := p.broken(v.LBrace, v.RBrace, false, nil)
		p.list(v.LBrace, v.RBrace, "{", "}", len(v.Fields), broken, func(i int) {
			f := v.Fields[i]
			p.name(f.Name)
			p.token(f.Colon, ":")
			p.space()
			p.value(f.Value)
		})
	}
}
