package parser

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
)

// ParseSchema parses a type system document (SDL, GQL-11) with the default
// limits: schema, scalar, type, interface, union, enum, input and
// directive definitions with their descriptions, and their extensions
// ("extend type ..."). The definitions are *ast.SchemaDefinition,
// *ast.TypeDefinition and *ast.DirectiveDefinition in source order.
//
// Recovery works as in Parse: missing pieces become placeholders, and a
// type system keyword at column 1 followed by a name ends an unclosed
// block. Operations and fragments are parsed, reported as
// ErrNotTypeSystem and left out of the document.
func ParseSchema(src string) (*ast.Document, []Error) {
	return Config{}.ParseSchema(src)
}

// ParseSchema parses a type system document with the limits in c.
func (c Config) ParseSchema(src string) (*ast.Document, []Error) {
	p, doc := c.newParser(src)
	p.sdl = true
	p.parseSchemaDocument(doc)
	return doc, p.finish()
}

// atTypeSystemStart reports whether the next token starts a new type system
// definition while inside an unclosed list: at column 1, a type keyword or
// "extend" followed by a name, "directive" followed by "@", "schema"
// followed by "{" or "@", or a description followed by one of these
// keywords. Field and argument names are indented in formatted SDL.
func (p *parser) atTypeSystemStart() bool {
	t := p.peek()
	if t.Column != 1 {
		return false
	}
	n := 0
	if t.Kind == lexer.String || t.Kind == lexer.BlockString {
		n = 1
		t = p.lookahead(1)
	}
	if t.Kind != lexer.Name {
		return false
	}
	next := p.lookahead(n + 1).Kind
	switch kw := t.Text(p.src); kw {
	case "schema":
		return next == lexer.BraceL || next == lexer.At
	case "directive":
		return next == lexer.At
	case "extend":
		return n == 0 && next == lexer.Name
	default:
		_, ok := ast.TypeKindOf(kw)
		return ok && next == lexer.Name
	}
}

func (p *parser) parseSchemaDocument(doc *ast.Document) {
	for !p.at(lexer.EOF) {
		t := p.peek()
		switch {
		case t.Kind == lexer.String || t.Kind == lexer.BlockString ||
			t.Kind == lexer.Name && typeSystemKeywords[t.Text(p.src)]:
			if d := p.parseTypeSystemDefinition(); d != nil {
				doc.Definitions = append(doc.Definitions, d)
			}
		case t.Kind == lexer.BraceL || t.Kind == lexer.Name && operationTypes[t.Text(p.src)] != nil:
			op := p.parseOperation()
			p.errorf(ErrNotTypeSystem, op.Start, op.End, "an operation is not allowed in a schema document")
		case t.Kind == lexer.Name && t.Text(p.src) == "fragment":
			f := p.parseFragmentDefinition()
			p.errorf(ErrNotTypeSystem, f.Start, f.End, "a fragment is not allowed in a schema document")
		default:
			p.skipSchemaJunk()
		}
	}
}

// skipSchemaJunk skips top-level tokens that start no definition, as one
// error.
func (p *parser) skipSchemaJunk() {
	first := p.peek()
	p.errorf(ErrUnexpectedToken, first.Start, first.End,
		"expected a type system definition (type, interface, union, enum, input, scalar, directive, schema or extend), found %s",
		p.describe(first))
	errIdx := len(p.errs) - 1
	for p.advance(); !p.at(lexer.EOF) && !p.atSchemaTopLevel(); {
		p.advance()
	}
	if errIdx >= 0 && errIdx < len(p.errs) && p.errs[errIdx].Start == first.Start {
		p.errs[errIdx].End = p.prevEnd
	}
}

// atSchemaTopLevel reports whether the next token can start a definition
// of a schema document.
func (p *parser) atSchemaTopLevel() bool {
	t := p.peek()
	switch t.Kind {
	case lexer.String, lexer.BlockString, lexer.BraceL:
		return true
	case lexer.Name:
		s := t.Text(p.src)
		return typeSystemKeywords[s] || operationTypes[s] != nil || s == "fragment"
	}
	return false
}

// parseDescription parses an optional description string.
func (p *parser) parseDescription() *ast.StringValue {
	t := p.peek()
	if t.Kind != lexer.String && t.Kind != lexer.BlockString {
		return nil
	}
	p.advance()
	s, _ := lexer.StringValue(t.Text(p.src)) // valid token: cannot fail
	return &ast.StringValue{Loc: tokLoc(t), Block: t.Kind == lexer.BlockString, Value: s}
}

// parseTypeSystemDefinition parses one definition or extension; the next
// token is a description or a type system keyword. It returns nil when
// nothing but a stray description was there.
func (p *parser) parseTypeSystemDefinition() ast.Definition {
	start := p.peek().Start
	desc := p.parseDescription()
	extend := -1
	if p.atName("extend") {
		extend = p.advance().Start
		if desc != nil {
			p.errorf(ErrUnexpectedToken, desc.Start, desc.End, "an extension cannot have a description")
		}
	}
	t := p.peek()
	kw := ""
	if t.Kind == lexer.Name {
		kw = t.Text(p.src)
	}
	switch {
	case kw == "schema":
		return p.parseSchemaDefinition(start, desc, extend)
	case kw == "directive" && extend < 0:
		return p.parseDirectiveDefinition(start, desc)
	}
	if kind, ok := ast.TypeKindOf(kw); ok {
		return p.parseTypeDefinition(start, desc, extend, kind)
	}
	if extend >= 0 {
		p.unexpected("schema, scalar, type, interface, union, enum or input after \"extend\"")
	} else {
		p.unexpected("a definition after the description")
	}
	// Skip to the next definition so the rest of the document parses.
	for !p.at(lexer.EOF) && !p.atSchemaTopLevel() {
		p.advance()
	}
	return nil
}

// parseSchemaDefinition parses "schema @directives { query: Q ... }"; the
// next token is "schema".
func (p *parser) parseSchemaDefinition(start int, desc *ast.StringValue, extend int) *ast.SchemaDefinition {
	d := &ast.SchemaDefinition{Description: desc, Extend: extend, Keyword: p.advance().Start, LBrace: -1, RBrace: -1}
	d.Directives = p.parseDirectives(true)
	if p.at(lexer.BraceL) {
		lb := p.advance()
		d.LBrace = lb.Start
		p.braces++
	loop:
		for {
			t := p.peek()
			switch {
			case t.Kind == lexer.BraceR:
				d.RBrace = p.advance().Start
				break loop
			case t.Kind == lexer.EOF, p.atDefinitionStart():
				p.unclosed(lb)
				break loop
			case t.Kind == lexer.Name && operationTypes[t.Text(p.src)] != nil:
				d.OperationTypes = append(d.OperationTypes, p.parseRootOperationType())
			default:
				p.unexpected("query, mutation or subscription")
				p.advance()
			}
		}
		p.braces--
		if len(d.OperationTypes) == 0 {
			p.errorAtClose(d.RBrace, "query, mutation or subscription")
		}
	} else if extend < 0 || len(d.Directives) == 0 {
		p.unexpected(quote("{"))
	}
	d.Loc = p.loc(start)
	return d
}

// parseRootOperationType parses "query: Query"; the next token is an
// operation keyword.
func (p *parser) parseRootOperationType() *ast.RootOperationTypeDefinition {
	kw := p.advance()
	r := &ast.RootOperationTypeDefinition{Operation: *operationTypes[kw.Text(p.src)], Keyword: kw.Start, Colon: -1}
	if p.at(lexer.Colon) {
		r.Colon = p.advance().Start
	} else {
		p.unexpected(quote(":"))
	}
	r.Type = p.parseNamedType()
	r.Loc = p.loc(kw.Start)
	return r
}

// parseTypeDefinition parses a type definition or extension of kind; the
// next token is its keyword.
func (p *parser) parseTypeDefinition(start int, desc *ast.StringValue, extend int, kind ast.TypeKind) *ast.TypeDefinition {
	d := &ast.TypeDefinition{Description: desc, Extend: extend, Kind: kind, Keyword: p.advance().Start,
		Implements: -1, Equals: -1, LBrace: -1, RBrace: -1}
	d.Name = p.parseName("type name")
	if (kind == ast.KindObject || kind == ast.KindInterface) && p.atName("implements") {
		d.Implements = p.advance().Start
		d.Interfaces = p.parseNamedTypes(lexer.Amp, "interface name")
	}
	d.Directives = p.parseDirectives(true)
	switch kind {
	case ast.KindUnion:
		if p.at(lexer.Equals) {
			d.Equals = p.advance().Start
			d.Members = p.parseNamedTypes(lexer.Pipe, "union member")
		}
	case ast.KindObject, ast.KindInterface:
		if p.at(lexer.BraceL) {
			p.parseBlock(d, "field definition", func() { d.Fields = append(d.Fields, p.parseFieldDefinition()) })
		}
	case ast.KindEnum:
		if p.at(lexer.BraceL) {
			p.parseBlock(d, "enum value", func() { d.EnumValues = append(d.EnumValues, p.parseEnumValueDefinition()) })
		}
	case ast.KindInputObject:
		if p.at(lexer.BraceL) {
			p.parseBlock(d, "input field definition", func() { d.InputFields = append(d.InputFields, p.parseInputValueDefinition()) })
		}
	}
	if extend >= 0 && len(d.Interfaces) == 0 && len(d.Directives) == 0 && len(d.Members) == 0 && d.LBrace < 0 {
		// The grammar requires an extension to add something.
		p.unexpected("something to extend " + quote(d.Name.Value) + " with")
	}
	d.Loc = p.loc(start)
	return d
}

// parseNamedTypes parses "sep? A sep B ...": interfaces joined by "&" or
// union members joined by "|", with an optional leading separator.
func (p *parser) parseNamedTypes(sep lexer.Kind, want string) []*ast.NamedType {
	if p.at(sep) {
		p.advance()
	}
	ts := []*ast.NamedType{p.parseNamedTypeWant(want)}
	for p.at(sep) {
		p.advance()
		ts = append(ts, p.parseNamedTypeWant(want))
	}
	return ts
}

func (p *parser) parseNamedTypeWant(want string) *ast.NamedType {
	n := &ast.NamedType{Name: p.parseName(want)}
	n.Loc = p.loc(n.Name.Start)
	return n
}

// parseBlock parses "{ entries }" of a type definition; the next token is
// "{". Each entry starts with a name or a description.
func (p *parser) parseBlock(d *ast.TypeDefinition, want string, entry func()) {
	lb := p.advance()
	d.LBrace = lb.Start
	p.braces++
	n := 0
loop:
	for {
		t := p.peek()
		switch {
		case t.Kind == lexer.BraceR:
			d.RBrace = p.advance().Start
			break loop
		case t.Kind == lexer.EOF, p.atDefinitionStart():
			p.unclosed(lb)
			break loop
		case t.Kind == lexer.Name || t.Kind == lexer.String || t.Kind == lexer.BlockString:
			before := p.i
			entry()
			n++
			if p.i == before { // a lone description: make progress
				p.advance()
			}
		default:
			p.unexpected(want)
			p.advance()
		}
	}
	p.braces--
	if n == 0 {
		p.errorAtClose(d.RBrace, want)
	}
}

// parseFieldDefinition parses "desc? name(args): Type @directives".
func (p *parser) parseFieldDefinition() *ast.FieldDefinition {
	start := p.peek().Start
	f := &ast.FieldDefinition{Colon: -1, Description: p.parseDescription()}
	f.Name = p.parseName("field name")
	if p.at(lexer.ParenL) {
		f.Arguments = p.parseArgumentDefinitions()
	}
	f.Colon, f.Type = p.parseColonType()
	f.Directives = p.parseDirectives(true)
	f.Loc = p.loc(start)
	return f
}

// parseColonType parses ": Type"; without the colon a type is still read
// when a name or "[" follows.
func (p *parser) parseColonType() (int, ast.Type) {
	switch t := p.peek(); {
	case t.Kind == lexer.Colon:
		return p.advance().Start, p.parseType()
	case (t.Kind == lexer.Name || t.Kind == lexer.BracketL) && !p.atDefinitionStart():
		p.unexpected(quote(":"))
		return -1, p.parseType()
	}
	p.unexpected(quote(":"))
	l := p.gap()
	return -1, &ast.NamedType{Loc: l, Name: &ast.Name{Loc: l}}
}

// parseArgumentDefinitions parses "( input values )"; the next token is
// "(".
func (p *parser) parseArgumentDefinitions() *ast.ArgumentDefinitionList {
	lp := p.advance()
	list := &ast.ArgumentDefinitionList{LParen: lp.Start, RParen: -1}
	p.parens++
loop:
	for {
		t := p.peek()
		switch {
		case t.Kind == lexer.ParenR:
			list.RParen = p.advance().Start
			break loop
		case t.Kind == lexer.EOF, t.Kind == lexer.BraceL,
			t.Kind == lexer.BraceR && p.braces > 0, p.atDefinitionStart():
			p.unclosed(lp)
			break loop
		case t.Kind == lexer.Name || t.Kind == lexer.String || t.Kind == lexer.BlockString:
			before := p.i
			list.Arguments = append(list.Arguments, p.parseInputValueDefinition())
			if p.i == before {
				p.advance()
			}
		default:
			p.unexpected("argument definition")
			p.advance()
		}
	}
	p.parens--
	if len(list.Arguments) == 0 {
		p.errorAtClose(list.RParen, "argument definition")
	}
	list.Loc = p.loc(lp.Start)
	return list
}

// parseInputValueDefinition parses "desc? name: Type = default
// @directives".
func (p *parser) parseInputValueDefinition() *ast.InputValueDefinition {
	start := p.peek().Start
	v := &ast.InputValueDefinition{Colon: -1, Equals: -1, Description: p.parseDescription()}
	v.Name = p.parseName("name")
	v.Colon, v.Type = p.parseColonType()
	if p.at(lexer.Equals) {
		v.Equals = p.advance().Start
		v.DefaultValue = p.parseValue(true)
	}
	v.Directives = p.parseDirectives(true)
	v.Loc = p.loc(start)
	return v
}

// parseEnumValueDefinition parses "desc? NAME @directives".
func (p *parser) parseEnumValueDefinition() *ast.EnumValueDefinition {
	start := p.peek().Start
	v := &ast.EnumValueDefinition{Description: p.parseDescription()}
	v.Name = p.parseName("enum value")
	switch v.Name.Value {
	case "true", "false", "null":
		p.errorf(ErrReservedName, v.Name.Start, v.Name.End, "an enum value cannot be named %q", v.Name.Value)
	}
	v.Directives = p.parseDirectives(true)
	v.Loc = p.loc(start)
	return v
}

// directiveLocations are the valid DirectiveLocation names.
var directiveLocations = map[string]bool{
	"QUERY": true, "MUTATION": true, "SUBSCRIPTION": true, "FIELD": true,
	"FRAGMENT_DEFINITION": true, "FRAGMENT_SPREAD": true, "INLINE_FRAGMENT": true,
	"VARIABLE_DEFINITION": true, "SCHEMA": true, "SCALAR": true, "OBJECT": true,
	"FIELD_DEFINITION": true, "ARGUMENT_DEFINITION": true, "INTERFACE": true,
	"UNION": true, "ENUM": true, "ENUM_VALUE": true, "INPUT_OBJECT": true,
	"INPUT_FIELD_DEFINITION": true,
}

// parseDirectiveDefinition parses "directive @name(args) repeatable on
// LOCATIONS"; the next token is "directive".
func (p *parser) parseDirectiveDefinition(start int, desc *ast.StringValue) *ast.DirectiveDefinition {
	d := &ast.DirectiveDefinition{Description: desc, Keyword: p.advance().Start, At: -1, Repeatable: -1, On: -1}
	if p.at(lexer.At) {
		d.At = p.advance().Start
	} else {
		p.unexpected(quote("@"))
	}
	d.Name = p.parseName("directive name")
	if p.at(lexer.ParenL) {
		d.Arguments = p.parseArgumentDefinitions()
	}
	if p.atName("repeatable") {
		d.Repeatable = p.advance().Start
	}
	if p.atName("on") {
		d.On = p.advance().Start
		if p.at(lexer.Pipe) {
			p.advance()
		}
		for {
			n := p.parseName("directive location")
			if !n.Missing() && !directiveLocations[n.Value] {
				p.errorf(ErrUnexpectedToken, n.Start, n.End, "unknown directive location %q", n.Value)
			}
			d.Locations = append(d.Locations, n)
			if !p.at(lexer.Pipe) {
				break
			}
			p.advance()
		}
	} else {
		p.unexpected(quote("on"))
	}
	d.Loc = p.loc(start)
	return d
}
