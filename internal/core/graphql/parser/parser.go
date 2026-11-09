// Package parser parses GraphQL executable documents into the syntax tree
// of package ast, recovering from errors so that half-typed documents still
// produce a useful partial tree for validation and autocomplete.
//
// Parse never panics and does work linear in the document size: every
// recovery step consumes at least one token or ends the enclosing construct.
// Nesting is capped (Config.MaxDepth) so deeply nested input cannot exhaust
// the stack, and the error list is capped (Config.MaxErrors).
//
// Recovery rules, in short:
//   - A missing name, value or type becomes a placeholder node (see package
//     ast) and parsing continues with the same token.
//   - An unexpected token inside a list ("{ }", "( )", "[ ]") is reported
//     and skipped.
//   - A closing delimiter that belongs to an enclosing list ends the inner
//     lists, which are reported as unclosed; so do the end of input and an
//     executable definition keyword at column 1 (see atDefinitionStart).
//   - Type system definitions (SDL) are reported as ErrNotExecutable and
//     skipped as a whole.
//
// ParseSchema (sdl.go) parses type system documents (SDL) instead, with
// the same recovery.
//
// See docs/graphql-engine.md.
package parser

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
)

// Default limits used by Parse.
const (
	DefaultMaxDepth  = 256
	DefaultMaxErrors = 100
)

// Config sets parser limits. Zero fields take the defaults.
type Config struct {
	// MaxDepth caps the nesting of selection sets, list and object values
	// and list types.
	MaxDepth int
	// MaxErrors caps the number of errors returned, including the final
	// ErrTooManyErrors.
	MaxErrors int
}

// Parse parses src with the default limits. The document is never nil;
// errors are sorted by position, except that a final ErrTooManyErrors
// stays last.
func Parse(src string) (*ast.Document, []Error) {
	return Config{}.Parse(src)
}

// Parse parses src with the limits in c.
func (c Config) Parse(src string) (*ast.Document, []Error) {
	p, doc := c.newParser(src)
	p.parseDocument(doc)
	return doc, p.finish()
}

func (c Config) newParser(src string) (*parser, *ast.Document) {
	toks, comments := splitComments(src, lexer.Tokenize(src, 0))
	p := &parser{
		src:       src,
		toks:      toks,
		maxDepth:  cmp.Or(c.MaxDepth, DefaultMaxDepth),
		maxErrors: max(cmp.Or(c.MaxErrors, DefaultMaxErrors), 1),
	}
	doc := ast.NewDocument(src)
	doc.Comments = comments
	return p, doc
}

// finish returns the errors sorted by position, a final ErrTooManyErrors
// staying last.
func (p *parser) finish() []Error {
	errs := p.errs
	if n := len(errs); n > 0 && errs[n-1].Code == ErrTooManyErrors {
		errs = errs[:n-1]
	}
	slices.SortStableFunc(errs, func(a, b Error) int { return cmp.Compare(a.Start, b.Start) })
	return p.errs
}

// splitComments removes the comment tokens from toks, in place, and
// returns them as comments.
func splitComments(src string, toks []lexer.Token) ([]lexer.Token, []*ast.Comment) {
	var comments []*ast.Comment
	kept := toks[:0]
	for _, t := range toks {
		if t.Kind == lexer.Comment {
			comments = append(comments, &ast.Comment{Loc: tokLoc(t), Text: t.Text(src)})
			continue
		}
		kept = append(kept, t)
	}
	return kept, comments
}

type parser struct {
	src  string
	toks []lexer.Token
	i    int // index of the next token; toks[len-1] is EOF

	prevEnd int // end of the last consumed token
	hw      int // high-water mark: max end of consumed tokens and finished nodes

	depth, maxDepth int
	tooDeep         bool

	// Open delimiters on the parse stack, to tell whether a closing token
	// belongs to an enclosing list.
	braces, parens, brackets int

	errs      []Error
	maxErrors int

	// sdl is set by ParseSchema: definitions start with type system
	// keywords (see atDefinitionStart).
	sdl bool
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

// advance consumes and returns the next significant token. At the end of
// input it returns EOF without consuming.
func (p *parser) advance() lexer.Token {
	t := p.peek()
	if t.Kind != lexer.EOF {
		p.i++
		p.prevEnd = t.End
		p.hw = max(p.hw, t.End)
	}
	return t
}

func (p *parser) at(k lexer.Kind) bool { return p.peek().Kind == k }

func (p *parser) atName(name string) bool {
	t := p.peek()
	return t.Kind == lexer.Name && t.Text(p.src) == name
}

// gap is the empty stretch where a missing node belongs: from the end of
// what was parsed so far to the start of the next token.
func (p *parser) gap() ast.Loc {
	l := ast.Loc{Start: p.hw, End: p.peek().Start}
	p.hw = l.End
	return l
}

// loc is the range of a node that started at start and has just finished.
func (p *parser) loc(start int) ast.Loc { return ast.Loc{Start: start, End: p.hw} }

// unclosed reports an opening delimiter at open without its closing one and
// extends the current node up to the next token.
func (p *parser) unclosed(open lexer.Token) {
	p.errorf(ErrUnclosed, open.Start, open.End, "%s has no matching %s", quote(open.Kind.String()), quote(closer(open.Kind)))
	p.hw = max(p.hw, p.peek().Start)
}

func closer(k lexer.Kind) string {
	switch k {
	case lexer.BraceL:
		return "}"
	case lexer.ParenL:
		return ")"
	}
	return "]"
}

// atDefinitionStart reports whether the next token looks like the start of
// a new executable definition while still inside an unclosed list:
// "query", "mutation" or "subscription" followed by a name, "(", "@" or "{",
// or "fragment" followed by a name, all at column 1. Those are valid field
// names too, but formatted documents indent fields, and this keeps a
// half-typed operation from swallowing the definitions after it.
func (p *parser) atDefinitionStart() bool {
	if p.sdl {
		return p.atTypeSystemStart()
	}
	t := p.peek()
	if t.Kind != lexer.Name || t.Column != 1 {
		return false
	}
	next := p.lookahead(1).Kind
	switch t.Text(p.src) {
	case "query", "mutation", "subscription":
		return next == lexer.Name || next == lexer.ParenL || next == lexer.At || next == lexer.BraceL
	case "fragment":
		return next == lexer.Name
	}
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

// reportedAt reports whether one of the last few errors starts at off.
// Errors at one position come in short bursts (an unclosed list reported in
// between), so looking back a little is enough.
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
	case lexer.Name:
		return "name " + quote(t.Text(p.src))
	case lexer.Int, lexer.Float:
		return "number " + t.Text(p.src)
	case lexer.String, lexer.BlockString:
		return "string"
	}
	return quote(t.Kind.String())
}

func quote(s string) string { return "\"" + s + "\"" }

// enter increases the nesting depth; it returns false, reporting ErrTooDeep
// once, if the limit is exceeded. Callers must call leave either way.
func (p *parser) enter() bool {
	p.depth++
	if p.depth <= p.maxDepth {
		return true
	}
	if !p.tooDeep {
		p.tooDeep = true
		t := p.peek()
		p.errorf(ErrTooDeep, t.Start, t.End, "nesting deeper than %d levels", p.maxDepth)
	}
	return false
}

func (p *parser) leave() { p.depth-- }

// skipNested consumes tokens after the opening delimiter open up to and
// including its matching closer, counting only that delimiter pair. It
// returns the closer's offset, or -1 at the end of input.
func (p *parser) skipNested(open lexer.Kind) int {
	cl := lexer.BraceR
	if open == lexer.BracketL {
		cl = lexer.BracketR
	}
	for n := 1; ; {
		t := p.advance()
		switch t.Kind {
		case lexer.EOF:
			p.hw = max(p.hw, t.Start)
			return -1
		case open:
			n++
		case cl:
			if n--; n == 0 {
				return t.Start
			}
		}
	}
}

// ---- document ----

var typeSystemKeywords = map[string]bool{
	"schema": true, "scalar": true, "type": true, "interface": true, "union": true,
	"enum": true, "input": true, "directive": true, "extend": true,
}

func (p *parser) parseDocument(doc *ast.Document) {
	for !p.at(lexer.EOF) {
		t := p.peek()
		switch {
		case t.Kind == lexer.BraceL:
			doc.Definitions = append(doc.Definitions, p.parseOperation())
		case t.Kind == lexer.Name && operationTypes[t.Text(p.src)] != nil:
			doc.Definitions = append(doc.Definitions, p.parseOperation())
		case t.Kind == lexer.Name && t.Text(p.src) == "fragment":
			doc.Definitions = append(doc.Definitions, p.parseFragmentDefinition())
		case t.Kind == lexer.String || t.Kind == lexer.BlockString ||
			t.Kind == lexer.Name && typeSystemKeywords[t.Text(p.src)]:
			doc.Definitions = append(doc.Definitions, p.skipTypeSystem())
		default:
			p.skipJunk()
		}
	}
}

// atTopLevelStart reports whether the next token starts a definition.
func (p *parser) atTopLevelStart() bool {
	t := p.peek()
	switch t.Kind {
	case lexer.BraceL, lexer.String, lexer.BlockString:
		return true
	case lexer.Name:
		s := t.Text(p.src)
		return operationTypes[s] != nil || s == "fragment" || typeSystemKeywords[s]
	}
	return false
}

// skipJunk skips top-level tokens that start no definition, as one error.
func (p *parser) skipJunk() {
	first := p.peek()
	p.errorf(ErrUnexpectedToken, first.Start, first.End,
		"expected query, mutation, subscription, fragment or \"{\", found %s", p.describe(first))
	errIdx := len(p.errs) - 1
	for p.advance(); !p.at(lexer.EOF) && !p.atTopLevelStart(); {
		p.advance()
	}
	if errIdx >= 0 && errIdx < len(p.errs) && p.errs[errIdx].Start == first.Start {
		p.errs[errIdx].End = p.prevEnd
	}
}

// skipTypeSystem skips a type system definition or extension: an optional
// description, the keyword ("extend" takes the next name too), then tokens
// up to the next definition keyword or description at the top level, or
// through the first balanced "{ ... }" block, whichever comes first. For
// scalar, union and directive definitions, which have no block, a
// top-level "{" starts the next definition (a query shorthand) instead.
func (p *parser) skipTypeSystem() *ast.TypeSystemDefinition {
	start := p.peek().Start
	def := &ast.TypeSystemDefinition{}
	if t := p.peek(); t.Kind == lexer.String || t.Kind == lexer.BlockString {
		p.advance()
	}
	if t := p.peek(); t.Kind == lexer.Name && typeSystemKeywords[t.Text(p.src)] {
		def.Keyword = t.Text(p.src)
		p.advance()
		if def.Keyword == "extend" && p.at(lexer.Name) {
			p.advance()
		}
	}
	hasBlock := def.Keyword != "scalar" && def.Keyword != "union" && def.Keyword != "directive"
	braces, parens, brackets := 0, 0, 0
loop:
	for {
		t := p.peek()
		nested := braces+parens+brackets > 0
		switch {
		case t.Kind == lexer.EOF:
			break loop
		case !nested && p.atTopLevelStart() && (t.Kind != lexer.BraceL || !hasBlock):
			break loop
		}
		p.advance()
		switch t.Kind {
		case lexer.BraceL:
			braces++
		case lexer.BraceR:
			braces = max(braces-1, 0)
			if braces == 0 && parens+brackets == 0 {
				break loop
			}
		case lexer.ParenL:
			parens++
		case lexer.ParenR:
			parens = max(parens-1, 0)
		case lexer.BracketL:
			brackets++
		case lexer.BracketR:
			brackets = max(brackets-1, 0)
		}
	}
	def.Loc = p.loc(start)
	what := "type system definition"
	if def.Keyword != "" {
		what = quote(def.Keyword) + " definition"
	}
	p.errorf(ErrNotExecutable, def.Start, def.End, "%s is not allowed in an executable document", what)
	return def
}

var operationTypes = map[string]*ast.OperationType{
	"query": ptr(ast.Query), "mutation": ptr(ast.Mutation), "subscription": ptr(ast.Subscription),
}

func ptr[T any](v T) *T { return &v }

// parseOperation parses an operation; the next token is "{" or an operation
// type keyword.
func (p *parser) parseOperation() *ast.OperationDefinition {
	t := p.peek()
	op := &ast.OperationDefinition{Keyword: -1}
	if t.Kind == lexer.Name {
		op.Operation = *operationTypes[t.Text(p.src)]
		op.Keyword = t.Start
		p.advance()
		if p.at(lexer.Name) {
			op.Name = p.parseName("")
		}
		if p.at(lexer.ParenL) {
			op.VariableDefinitions = p.parseVariableDefinitions()
		}
		op.Directives = p.parseDirectives(false)
	}
	if p.at(lexer.BraceL) {
		op.SelectionSet = p.parseSelectionSet()
	} else {
		p.unexpected(quote("{"))
	}
	op.Loc = p.loc(t.Start)
	return op
}

// parseFragmentDefinition parses a fragment definition; the next token is
// "fragment".
func (p *parser) parseFragmentDefinition() *ast.FragmentDefinition {
	kw := p.advance()
	def := &ast.FragmentDefinition{Keyword: kw.Start}
	switch {
	case p.atName("on") && !(p.lookahead(1).Kind == lexer.Name && p.lookahead(1).Text(p.src) == "on"):
		// "fragment on T": the name is missing.
		p.unexpected("fragment name")
		def.Name = &ast.Name{Loc: p.gap()}
	default:
		def.Name = p.parseName("fragment name")
		if def.Name.Value == "on" {
			p.errorf(ErrReservedName, def.Name.Start, def.Name.End, "a fragment cannot be named \"on\"")
		}
	}
	switch {
	case p.atName("on"):
		def.TypeCondition = p.parseTypeCondition()
	case p.at(lexer.Name) && !p.atDefinitionStart():
		// "fragment F User": the "on" is missing.
		p.unexpected(quote("on"))
		tc := &ast.TypeCondition{On: -1, Type: p.parseNamedType()}
		tc.Loc = p.loc(tc.Type.Start)
		def.TypeCondition = tc
	default:
		p.unexpected(quote("on"))
	}
	def.Directives = p.parseDirectives(false)
	if p.at(lexer.BraceL) {
		def.SelectionSet = p.parseSelectionSet()
	} else {
		p.unexpected(quote("{"))
	}
	def.Loc = p.loc(kw.Start)
	return def
}

// parseTypeCondition parses "on Type"; the next token is "on".
func (p *parser) parseTypeCondition() *ast.TypeCondition {
	on := p.advance()
	tc := &ast.TypeCondition{On: on.Start, Type: p.parseNamedType()}
	tc.Loc = p.loc(on.Start)
	return tc
}

// ---- names and types ----

// parseName parses a name, or returns a missing name spanning the gap and
// reports that want was expected. With want == "" the caller guarantees a
// name is next.
func (p *parser) parseName(want string) *ast.Name {
	if t := p.peek(); t.Kind == lexer.Name && (want == "" || !p.atDefinitionStart()) {
		p.advance()
		return &ast.Name{Loc: ast.Loc{Start: t.Start, End: t.End}, Value: t.Text(p.src)}
	}
	p.unexpected(want)
	return &ast.Name{Loc: p.gap()}
}

func (p *parser) parseNamedType() *ast.NamedType {
	n := &ast.NamedType{Name: p.parseName("type name")}
	n.Loc = p.loc(n.Name.Start)
	return n
}

// parseType parses a type reference: Name, [Type] or either followed by "!".
func (p *parser) parseType() ast.Type {
	var typ ast.Type
	if t := p.peek(); t.Kind == lexer.BracketL {
		p.advance()
		lt := &ast.ListType{LBracket: t.Start, RBracket: -1}
		if p.enter() {
			p.brackets++
			lt.Type = p.parseType()
			p.brackets--
			if p.at(lexer.BracketR) {
				lt.RBracket = p.advance().Start
			} else {
				p.unclosed(t)
			}
		} else {
			lt.RBracket = p.skipNested(lexer.BracketL)
		}
		p.leave()
		lt.Loc = p.loc(t.Start)
		typ = lt
	} else {
		typ = p.parseNamedType()
	}
	if t := p.peek(); t.Kind == lexer.Bang {
		p.advance()
		typ = &ast.NonNullType{Loc: p.loc(typ.Pos().Start), Type: typ, Bang: t.Start}
	}
	return typ
}

// ---- variables ----

// parseVariableDefinitions parses "( ... )"; the next token is "(".
func (p *parser) parseVariableDefinitions() *ast.VariableDefinitionList {
	lp := p.advance()
	list := &ast.VariableDefinitionList{LParen: lp.Start, RParen: -1}
	p.parens++
loop:
	for {
		t := p.peek()
		switch {
		case t.Kind == lexer.ParenR:
			list.RParen = p.advance().Start
			break loop
		case t.Kind == lexer.EOF, t.Kind == lexer.BraceL, t.Kind == lexer.At,
			t.Kind == lexer.BraceR && p.braces > 0, p.atDefinitionStart():
			p.unclosed(lp)
			break loop
		case t.Kind == lexer.Dollar || t.Kind == lexer.Name:
			list.Definitions = append(list.Definitions, p.parseVariableDefinition())
		default:
			p.unexpected("variable definition")
			p.advance()
		}
	}
	p.parens--
	if len(list.Definitions) == 0 {
		p.errorAtClose(list.RParen, "variable definition")
	}
	list.Loc = p.loc(lp.Start)
	return list
}

// errorAtClose reports an empty list: want expected at the closing
// delimiter at off, or at the next token when the list is unclosed.
func (p *parser) errorAtClose(off int, want string) {
	if off < 0 {
		p.unexpected(want)
		return
	}
	p.errorf(ErrUnexpectedToken, off, off+1, "expected %s, found %s", want, quote(p.src[off:off+1]))
}

// parseVariableDefinition parses "$name: Type = default @directives"; the
// next token is "$" or a name (a forgotten "$").
func (p *parser) parseVariableDefinition() *ast.VariableDefinition {
	start := p.peek().Start
	def := &ast.VariableDefinition{Colon: -1, Equals: -1}
	if p.at(lexer.Dollar) {
		def.Variable = p.parseVariable()
	} else {
		p.unexpected(quote("$"))
		v := &ast.Variable{Dollar: -1, Name: p.parseName("")}
		v.Loc = v.Name.Loc
		def.Variable = v
	}
	switch t := p.peek(); {
	case t.Kind == lexer.Colon:
		def.Colon = p.advance().Start
		def.Type = p.parseType()
	case t.Kind == lexer.Name || t.Kind == lexer.BracketL:
		p.unexpected(quote(":"))
		def.Type = p.parseType()
	default:
		p.unexpected(quote(":"))
		l := p.gap()
		def.Type = &ast.NamedType{Loc: l, Name: &ast.Name{Loc: l}}
	}
	if t := p.peek(); t.Kind == lexer.Equals {
		def.Equals = p.advance().Start
		def.DefaultValue = p.parseValue(true)
	}
	def.Directives = p.parseDirectives(true)
	def.Loc = p.loc(start)
	return def
}

// parseVariable parses "$name"; the next token is "$".
func (p *parser) parseVariable() *ast.Variable {
	d := p.advance()
	v := &ast.Variable{Dollar: d.Start, Name: p.parseName("variable name")}
	v.Loc = p.loc(d.Start)
	return v
}

// ---- directives and arguments ----

func (p *parser) parseDirectives(isConst bool) []*ast.Directive {
	var ds []*ast.Directive
	for p.at(lexer.At) {
		at := p.advance()
		d := &ast.Directive{At: at.Start, Name: p.parseName("directive name")}
		if p.at(lexer.ParenL) {
			d.Arguments = p.parseArguments(isConst)
		}
		d.Loc = p.loc(at.Start)
		ds = append(ds, d)
	}
	return ds
}

// parseArguments parses "( name: value ... )"; the next token is "(".
func (p *parser) parseArguments(isConst bool) *ast.ArgumentList {
	lp := p.advance()
	list := &ast.ArgumentList{LParen: lp.Start, RParen: -1}
	p.parens++
loop:
	for {
		t := p.peek()
		switch {
		case t.Kind == lexer.ParenR:
			list.RParen = p.advance().Start
			break loop
		case t.Kind == lexer.EOF, t.Kind == lexer.BraceL, t.Kind == lexer.At,
			t.Kind == lexer.BraceR && p.braces > 0, t.Kind == lexer.BracketR && p.brackets > 0,
			p.atDefinitionStart():
			p.unclosed(lp)
			break loop
		case t.Kind == lexer.Name:
			start := t.Start
			a := &ast.Argument{Colon: -1}
			a.Name = p.parseName("")
			a.Colon, a.Value = p.parseColonValue(isConst)
			a.Loc = p.loc(start)
			list.Arguments = append(list.Arguments, a)
		default:
			p.unexpected("argument name")
			p.advance()
		}
	}
	p.parens--
	if len(list.Arguments) == 0 {
		p.errorAtClose(list.RParen, "argument")
	}
	list.Loc = p.loc(lp.Start)
	return list
}

// parseColonValue parses ": value" after an argument or object field name.
// Without the colon, a value is still parsed if one clearly follows (a
// scalar, variable or list); otherwise the value is a missing placeholder.
func (p *parser) parseColonValue(isConst bool) (colon int, v ast.Value) {
	switch t := p.peek(); t.Kind {
	case lexer.Colon:
		return p.advance().Start, p.parseValue(isConst)
	case lexer.Int, lexer.Float, lexer.String, lexer.BlockString, lexer.Dollar, lexer.BracketL:
		p.unexpected(quote(":"))
		return -1, p.parseValue(isConst)
	}
	p.unexpected(quote(":"))
	return -1, &ast.InvalidValue{Loc: p.gap(), Missing: true}
}

// ---- values ----

// parseValue parses a value. In a constant context (isConst) variables are
// reported but still returned.
func (p *parser) parseValue(isConst bool) ast.Value {
	if t := p.toks[p.i]; t.Kind == lexer.Error {
		// A malformed literal in value position is the value.
		p.report(Error{Code: ErrLexical, LexCode: t.Err, Start: t.Start, End: t.End, Message: t.Message})
		p.i++
		p.prevEnd, p.hw = t.End, max(p.hw, t.End)
		return &ast.InvalidValue{Loc: tokLoc(t)}
	}
	t := p.peek()
	if p.atDefinitionStart() {
		t.Kind = lexer.EOF // treat like the end of input: the value is missing
	}
	switch t.Kind {
	case lexer.Dollar:
		v := p.parseVariable()
		if isConst {
			p.errorf(ErrVariableInConst, v.Start, v.End, "variable not allowed in a constant value")
		}
		return v
	case lexer.Int:
		p.advance()
		return &ast.IntValue{Loc: tokLoc(t), Raw: t.Text(p.src)}
	case lexer.Float:
		p.advance()
		return &ast.FloatValue{Loc: tokLoc(t), Raw: t.Text(p.src)}
	case lexer.String, lexer.BlockString:
		p.advance()
		s, _ := lexer.StringValue(t.Text(p.src)) // valid token: cannot fail
		return &ast.StringValue{Loc: tokLoc(t), Block: t.Kind == lexer.BlockString, Value: s}
	case lexer.Name:
		p.advance()
		switch s := t.Text(p.src); s {
		case "true", "false":
			return &ast.BooleanValue{Loc: tokLoc(t), Value: s == "true"}
		case "null":
			return &ast.NullValue{Loc: tokLoc(t)}
		default:
			return &ast.EnumValue{Loc: tokLoc(t), Value: s}
		}
	case lexer.BracketL:
		return p.parseList(isConst)
	case lexer.BraceL:
		return p.parseObject(isConst)
	}
	p.unexpected("value")
	return &ast.InvalidValue{Loc: p.gap(), Missing: true}
}

func tokLoc(t lexer.Token) ast.Loc { return ast.Loc{Start: t.Start, End: t.End} }

func startsValue(k lexer.Kind) bool {
	switch k {
	case lexer.Dollar, lexer.Int, lexer.Float, lexer.String, lexer.BlockString,
		lexer.Name, lexer.BracketL, lexer.BraceL:
		return true
	}
	return false
}

// parseList parses "[ values ]"; the next token is "[".
func (p *parser) parseList(isConst bool) *ast.ListValue {
	lb := p.advance()
	list := &ast.ListValue{LBracket: lb.Start, RBracket: -1}
	if p.enter() {
		p.brackets++
	loop:
		for {
			if p.toks[p.i].Kind == lexer.Error {
				list.Values = append(list.Values, p.parseValue(isConst))
				continue
			}
			t := p.peek()
			switch {
			case t.Kind == lexer.BracketR:
				list.RBracket = p.advance().Start
				break loop
			case t.Kind == lexer.EOF, t.Kind == lexer.ParenR && p.parens > 0,
				t.Kind == lexer.BraceR && p.braces > 0, p.atDefinitionStart():
				p.unclosed(lb)
				break loop
			case startsValue(t.Kind):
				list.Values = append(list.Values, p.parseValue(isConst))
			default:
				p.unexpected("value")
				p.advance()
			}
		}
		p.brackets--
	} else {
		list.RBracket = p.skipNested(lexer.BracketL)
	}
	p.leave()
	list.Loc = p.loc(lb.Start)
	return list
}

// parseObject parses "{ name: value ... }"; the next token is "{".
func (p *parser) parseObject(isConst bool) *ast.ObjectValue {
	lb := p.advance()
	obj := &ast.ObjectValue{LBrace: lb.Start, RBrace: -1}
	if p.enter() {
		p.braces++
	loop:
		for {
			t := p.peek()
			switch {
			case t.Kind == lexer.BraceR:
				obj.RBrace = p.advance().Start
				break loop
			case t.Kind == lexer.EOF, t.Kind == lexer.ParenR && p.parens > 0,
				t.Kind == lexer.BracketR && p.brackets > 0, p.atDefinitionStart():
				p.unclosed(lb)
				break loop
			case t.Kind == lexer.Name:
				f := &ast.ObjectField{Colon: -1, Name: p.parseName("")}
				f.Colon, f.Value = p.parseColonValue(isConst)
				f.Loc = p.loc(t.Start)
				obj.Fields = append(obj.Fields, f)
			default:
				p.unexpected("object field name")
				p.advance()
			}
		}
		p.braces--
	} else {
		obj.RBrace = p.skipNested(lexer.BraceL)
	}
	p.leave()
	obj.Loc = p.loc(lb.Start)
	return obj
}

// ---- selections ----

// parseSelectionSet parses "{ selections }"; the next token is "{".
func (p *parser) parseSelectionSet() *ast.SelectionSet {
	lb := p.advance()
	set := &ast.SelectionSet{LBrace: lb.Start, RBrace: -1}
	if !p.enter() {
		set.RBrace = p.skipNested(lexer.BraceL)
		p.leave()
		set.Loc = p.loc(lb.Start)
		return set
	}
	p.braces++
loop:
	for {
		t := p.peek()
		switch {
		case t.Kind == lexer.BraceR:
			if len(set.Selections) == 0 {
				p.unexpected("field, fragment spread or inline fragment")
			}
			set.RBrace = p.advance().Start
			break loop
		case t.Kind == lexer.EOF, p.atDefinitionStart():
			p.unclosed(lb)
			if len(set.Selections) == 0 {
				p.unexpected("field, fragment spread or inline fragment")
			}
			break loop
		case t.Kind == lexer.Name:
			set.Selections = append(set.Selections, p.parseField())
		case t.Kind == lexer.Spread:
			set.Selections = append(set.Selections, p.parseFragment())
		default:
			p.unexpected("field, fragment spread or inline fragment")
			p.advance()
		}
	}
	p.braces--
	p.leave()
	if len(set.Selections) == 0 {
		end := set.RBrace
		if end < 0 {
			end = p.peek().Start
		}
		l := ast.Loc{Start: lb.End, End: end}
		set.Selections = []ast.Selection{&ast.Field{Loc: l, Colon: -1, Name: &ast.Name{Loc: l}}}
	}
	set.Loc = p.loc(lb.Start)
	return set
}

// parseField parses a field; the next token is a name.
func (p *parser) parseField() *ast.Field {
	start := p.peek().Start
	f := &ast.Field{Colon: -1, Name: p.parseName("")}
	if p.at(lexer.Colon) {
		f.Alias = f.Name
		f.Colon = p.advance().Start
		f.Name = p.parseName("field name")
	}
	if p.at(lexer.ParenL) {
		f.Arguments = p.parseArguments(false)
	}
	f.Directives = p.parseDirectives(false)
	if p.at(lexer.BraceL) {
		f.SelectionSet = p.parseSelectionSet()
	}
	f.Loc = p.loc(start)
	return f
}

// parseFragment parses a fragment spread or inline fragment; the next token
// is "...".
func (p *parser) parseFragment() ast.Selection {
	spread := p.advance()
	t := p.peek()
	switch {
	case t.Kind == lexer.Name && t.Text(p.src) != "on":
		fs := &ast.FragmentSpread{Spread: spread.Start, Name: p.parseName("")}
		fs.Directives = p.parseDirectives(false)
		fs.Loc = p.loc(spread.Start)
		return fs
	case t.Kind == lexer.Name, t.Kind == lexer.At, t.Kind == lexer.BraceL:
		inl := &ast.InlineFragment{Spread: spread.Start}
		if t.Kind == lexer.Name {
			inl.TypeCondition = p.parseTypeCondition()
		}
		inl.Directives = p.parseDirectives(false)
		if p.at(lexer.BraceL) {
			inl.SelectionSet = p.parseSelectionSet()
		} else {
			p.unexpected(quote("{"))
		}
		inl.Loc = p.loc(spread.Start)
		return inl
	}
	p.unexpected("fragment name, \"on\" or \"{\"")
	fs := &ast.FragmentSpread{Spread: spread.Start, Name: &ast.Name{Loc: p.gap()}}
	fs.Loc = p.loc(spread.Start)
	return fs
}
