package printer

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
)

// Type system definitions (SDL), in the graphql-js print style:
//
//	"""Description"""
//	type User implements Node & Entity @key(fields: "id") {
//	  "The ID."
//	  id: ID!
//	  friends(first: Int = 10): [User!]!
//	}
//
//	union Result = User | Repo
//
//	directive @auth(role: Role) repeatable on OBJECT | FIELD_DEFINITION
//
// Descriptions are printed as written, on their own line before what they
// describe. Argument definitions stay on one line unless one has a
// description or a comment, or the line would be longer than
// MaxLineLength; then they go one per line.

func (p *printer) description(s *ast.StringValue) {
	if s == nil {
		return
	}
	p.token(s.Start, p.src[s.Start:s.End])
	if p.minify {
		p.space()
	} else {
		p.line(sepLine)
	}
}

func (p *printer) extend(off int) {
	if off >= 0 {
		p.token(off, "extend")
		p.space()
	}
}

func (p *printer) schemaDefinition(d *ast.SchemaDefinition) {
	p.description(d.Description)
	p.extend(d.Extend)
	p.token(d.Keyword, "schema")
	p.directives(d.Directives)
	if d.LBrace < 0 {
		return
	}
	p.space()
	p.token(d.LBrace, "{")
	p.indent++
	for i, o := range d.OperationTypes {
		p.entrySep(i)
		p.token(o.Keyword, o.Operation.String())
		p.token(o.Colon, ":")
		p.space()
		p.name(o.Type.Name)
	}
	p.close(d.RBrace, "}")
}

// entrySep separates the entries of a block: a line break, or a comma when
// minified.
func (p *printer) entrySep(i int) {
	if p.minify && i > 0 {
		p.punct(",")
	}
	p.line(sepLine)
}

func (p *printer) typeDefinition(d *ast.TypeDefinition) {
	p.description(d.Description)
	p.extend(d.Extend)
	p.token(d.Keyword, d.Kind.String())
	p.space()
	p.name(d.Name)
	if d.Implements >= 0 {
		p.space()
		p.token(d.Implements, "implements")
		p.space()
		p.namedTypes(d.Implements, d.Interfaces, "&")
	}
	p.directives(d.Directives)
	if d.Equals >= 0 {
		p.space()
		p.token(d.Equals, "=")
		p.space()
		p.namedTypes(d.Equals, d.Members, "|")
	}
	if d.LBrace < 0 {
		return
	}
	p.space()
	p.token(d.LBrace, "{")
	p.indent++
	i := 0
	for _, f := range d.Fields {
		p.entrySep(i)
		i++
		p.fieldDefinition(f)
	}
	for _, v := range d.EnumValues {
		p.entrySep(i)
		i++
		p.description(v.Description)
		p.name(v.Name)
		p.directives(v.Directives)
	}
	for _, f := range d.InputFields {
		p.entrySep(i)
		i++
		p.inputValueDefinition(f)
	}
	p.close(d.RBrace, "}")
}

// namedTypes prints "A & B" or "A | B" after the token at lead
// ("implements" or "="). A leading separator is dropped, unless a comment
// follows it: then it stays so the comment keeps its place.
func (p *printer) namedTypes(lead int, ts []*ast.NamedType, sep string) {
	if len(ts) > 0 && p.hasComment(lead, ts[0].Start) {
		if off := separatorOffset(p.src, lead+1, ts[0].Start, sep[0]); off >= 0 {
			p.token(off, sep)
			p.space()
		}
	}
	for i, t := range ts {
		if i > 0 {
			p.separator(ts[i-1].End, t.Start, sep)
		}
		p.name(t.Name)
	}
}

// separator writes " & " or " | " between the names that end at from and
// start at to ("&" or "|" minified). The separator's offset is found in
// the source, so the comments around it stay on their side.
func (p *printer) separator(from, to int, sep string) {
	p.space()
	off := separatorOffset(p.src, from, to, sep[0])
	if off < 0 {
		off = from
	}
	p.token(off, sep)
	p.space()
}

// separatorOffset returns the offset of c between from and to outside
// comments, or -1 when there is none.
func separatorOffset(src string, from, to int, c byte) int {
	for i := from; i < to && i < len(src); i++ {
		switch src[i] {
		case c:
			return i
		case '#':
			for i < to && src[i] != '\n' && src[i] != '\r' {
				i++
			}
		}
	}
	return -1
}

func (p *printer) fieldDefinition(f *ast.FieldDefinition) {
	p.description(f.Description)
	p.name(f.Name)
	if f.Arguments != nil {
		p.argumentDefinitions(f.Arguments)
	}
	p.token(f.Colon, ":")
	p.space()
	p.typeRef(f.Type)
	p.directives(f.Directives)
}

func (p *printer) inputValueDefinition(v *ast.InputValueDefinition) {
	p.description(v.Description)
	p.name(v.Name)
	p.token(v.Colon, ":")
	p.space()
	p.typeRef(v.Type)
	if v.DefaultValue != nil {
		p.space()
		p.token(v.Equals, "=")
		p.space()
		p.value(v.DefaultValue)
	}
	p.directives(v.Directives)
}

func (p *printer) argumentDefinitions(l *ast.ArgumentDefinitionList) {
	described := false
	for _, a := range l.Arguments {
		described = described || a.Description != nil
	}
	broken := !p.minify && !p.flat && described ||
		p.broken(l.LParen, l.RParen, true, func(q *printer) { q.argumentDefinitions(l) })
	p.list(l.LParen, l.RParen, "(", ")", len(l.Arguments), broken, func(i int) {
		p.inputValueDefinition(l.Arguments[i])
	})
}

func (p *printer) directiveDefinition(d *ast.DirectiveDefinition) {
	p.description(d.Description)
	p.token(d.Keyword, "directive")
	p.space()
	p.token(d.At, "@")
	p.name(d.Name)
	if d.Arguments != nil {
		p.argumentDefinitions(d.Arguments)
	}
	if d.Repeatable >= 0 {
		p.space()
		p.token(d.Repeatable, "repeatable")
	}
	p.space()
	p.token(d.On, "on")
	p.space()
	for i, l := range d.Locations {
		if i > 0 {
			p.separator(d.Locations[i-1].End, l.Start, "|")
		}
		p.name(l)
	}
}
