// Package complete works out what can be typed at a cursor in a GraphQL
// executable document: the Go side of schema-aware autocomplete (GQL-13).
//
// Complete lexes and parses the document (half-typed documents give partial
// trees, see package parser), locates the cursor in the tree, resolves the
// types around it against a schema and returns ranked suggestions with the
// byte range they replace.
//
// Offsets are bytes. Editors that work in UTF-16 (CodeMirror, JavaScript)
// convert the cursor with lexer.UTF16Index.ToByte before calling Complete
// and the returned From and To with ToUTF16. See docs/graphql-engine.md.
package complete

import (
	"sort"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Context is the kind of position the cursor is at.
type Context string

// Cursor contexts.
const (
	// ContextNone: nothing can be suggested (an operation, fragment or
	// variable name being declared, an alias, right after a literal or a
	// closing delimiter, inside skipped SDL, ...).
	ContextNone Context = "none"
	// ContextComment and ContextString: inside a comment or a string; no
	// suggestions.
	ContextComment Context = "comment"
	ContextString  Context = "string"
	// ContextDocument: between definitions; operation keywords, fragment
	// and "{".
	ContextDocument Context = "document"
	// ContextSelection: a field in a selection set.
	ContextSelection Context = "selection"
	// ContextFragmentSpread: after "..."; fragment names and "on".
	ContextFragmentSpread Context = "fragment-spread"
	// ContextTypeCondition: after "... on" or "fragment F on".
	ContextTypeCondition Context = "type-condition"
	// ContextArgument: an argument name of a field or directive.
	ContextArgument Context = "argument"
	// ContextValue: an argument, list item, input field or default value.
	ContextValue Context = "value"
	// ContextVariable: a variable after "$" in a value.
	ContextVariable Context = "variable"
	// ContextObjectField: an input object field name inside "{".
	ContextObjectField Context = "object-field"
	// ContextVariableType: the type of a variable definition.
	ContextVariableType Context = "variable-type"
	// ContextDirective: a directive name after "@".
	ContextDirective Context = "directive"
	// ContextKeyword: a lone keyword ("on" in a fragment definition).
	ContextKeyword Context = "keyword"
)

// ItemKind is the kind of a suggestion.
type ItemKind string

// Suggestion kinds.
const (
	KindField      ItemKind = "field"
	KindArgument   ItemKind = "argument"
	KindType       ItemKind = "type"
	KindEnumValue  ItemKind = "enum-value"
	KindDirective  ItemKind = "directive"
	KindVariable   ItemKind = "variable"
	KindFragment   ItemKind = "fragment"
	KindKeyword    ItemKind = "keyword"
	KindInputField ItemKind = "input-field"
)

// Result is the completion at one cursor position.
type Result struct {
	Context Context
	// From and To are the byte range an accepted item replaces: the whole
	// name under or right before the cursor (with a leading "$" for
	// variables), or the empty range at the cursor. From <= offset <= To.
	From, To int
	// Prefix is the text between From and the cursor that items were
	// filtered by ("$" excluded).
	Prefix string
	// ParentType names the type the context belongs to: the selection set's
	// type for fields, fragment spreads and type conditions, the input
	// object for object fields. Empty when unknown or not applicable.
	ParentType string
	// Owner names the owner of an argument list: "Type.field" or
	// "@directive". Empty otherwise.
	Owner string
	// ExpectedType is the type expected at a value, such as "[ID!]!".
	// Empty when unknown or not a value context.
	ExpectedType string
	// Items are the suggestions, best first.
	Items []Item
}

// Item is one suggestion.
type Item struct {
	Label string
	Kind  ItemKind
	// Detail is a short type string: "[User!]!" for a field, "Int = 10" for
	// an argument with a default, "on User" for a fragment, "(if: Boolean!)"
	// for a directive, "object" for a type.
	Detail string
	// Documentation is the schema description.
	Documentation     string
	Deprecated        bool
	DeprecationReason string
	// InsertText replaces the range [From, To). It is plain text.
	InsertText string
	// Suffix is an optional hint an editor may insert after InsertText,
	// placing the cursor SuffixCursor bytes into it: "()" for a field or
	// directive with required arguments, " {  }" for a field of a composite
	// type, ": " for an argument or input field. It is empty when the
	// delimiter is already there.
	Suffix       string
	SuffixCursor int
	// SortText orders items as ranked ("0000", "0001", ...), for editors
	// that sort by their own key.
	SortText string
}

// Complete returns the suggestions at byte offset offset of src, a GraphQL
// executable document, for schema s. A nil schema completes syntax only
// (keywords, the built-in scalars and directives). The offset is clamped to
// the source.
func Complete(s *schema.Schema, src string, offset int) Result {
	offset = min(max(offset, 0), len(src))
	c := &completer{src: src, off: offset, s: s, anyRoot: s == nil}
	if s == nil {
		c.s = schema.Builtins()
	}
	c.res = Result{Context: ContextNone, From: offset, To: offset}
	if ctx, ok := c.scanTokens(); !ok {
		c.res.Context = ctx
		return c.res
	}
	doc, _ := parser.Parse(src)
	c.doc = doc
	path := ast.NodePathAt(doc, offset)
	if c.hasWord {
		// Placeholders after a keyword ("fragment|", "... on|") start past
		// the word; the word belongs to their parent.
		for len(path) > 1 && path[len(path)-1].Pos().Start > c.word.Start {
			path = path[:len(path)-1]
		}
	}
	c.walk(path)
	c.decide(path)
	c.finish()
	return c.res
}

type completer struct {
	src     string
	off     int
	s       *schema.Schema
	anyRoot bool // no schema: offer every operation type
	doc     *ast.Document
	res     Result

	toks    []lexer.Token
	word    lexer.Token // the name at the cursor, if hasWord
	hasWord bool
	next    lexer.Token // the first non-comment token at or after To

	typeInfo
	cands []candidate
}

// scanTokens finds the name at the cursor and rules out comments, strings
// and positions right after a literal or closing delimiter. It returns
// false with the context to report when nothing can be suggested.
func (c *completer) scanTokens() (Context, bool) {
	c.toks = lexer.Tokenize(c.src, 0)
	toks, off := c.toks, c.off
	i := sort.Search(len(toks), func(i int) bool { return toks[i].End >= off })
	t := toks[i] // exists: the EOF token ends at len(src)
	switch {
	case t.Start >= off:
		if t.Start == off && t.Kind == lexer.Name {
			c.setWord(t)
		}
	case t.Kind == lexer.Name:
		c.setWord(t)
	case t.End == off && i+1 < len(toks) && toks[i+1].Start == off && toks[i+1].Kind == lexer.Name:
		c.setWord(toks[i+1]) // "(|id"
	case t.Kind == lexer.Comment:
		return ContextComment, false
	case t.Kind == lexer.String || t.Kind == lexer.BlockString:
		if off < t.End {
			return ContextString, false
		}
		return ContextNone, false
	case t.Kind == lexer.Error:
		switch {
		case t.Err == lexer.ErrUnterminatedString || t.Err == lexer.ErrUnterminatedBlockString:
			return ContextString, false
		case c.src[t.Start] == '"' && off < t.End: // closed string with a bad escape or byte
			return ContextString, false
		}
		return ContextNone, false
	case t.End > off: // inside "..."
		return ContextNone, false
	default: // a token ends at the cursor
		switch t.Kind {
		case lexer.Int, lexer.Float, lexer.ParenR, lexer.BracketR, lexer.BraceR, lexer.Bang:
			return ContextNone, false
		}
	}
	to := off
	if c.hasWord {
		to = c.word.End
	}
	j := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= to })
	for j < len(toks) && toks[j].Kind == lexer.Comment {
		j++
	}
	c.next = toks[min(j, len(toks)-1)]
	return ContextNone, true
}

func (c *completer) setWord(t lexer.Token) {
	c.word, c.hasWord = t, true
	c.res.From, c.res.To = t.Start, t.End
	c.res.Prefix = c.src[t.Start:c.off]
}

// afterFragmentName reports whether the cursor follows "fragment Name " at
// the top level, where only "on" can come next.
func (c *completer) afterFragmentName() bool {
	if c.hasWord {
		return false
	}
	var prev []lexer.Token // the two tokens before the cursor, nearest first
	for i := sort.Search(len(c.toks), func(i int) bool { return c.toks[i].End > c.off }) - 1; i >= 0 && len(prev) < 2; i-- {
		if k := c.toks[i].Kind; k != lexer.Comment && k != lexer.EOF {
			prev = append(prev, c.toks[i])
		}
	}
	return len(prev) == 2 && prev[0].Kind == lexer.Name && prev[0].End < c.off &&
		prev[1].Kind == lexer.Name && prev[1].Text(c.src) == "fragment"
}

// inside reports whether the cursor is after the opening delimiter at l and
// not after the closing one at r (-1 when missing).
func (c *completer) inside(l, r int) bool {
	return l >= 0 && c.off > l && (r < 0 || c.off <= r)
}

// decide picks the context from the innermost nodes at the cursor and
// collects its candidates.
func (c *completer) decide(path []ast.Node) {
	n := path[len(path)-1] // the document always contains the offset
	parent := func(k int) ast.Node {
		if len(path) > k {
			return path[len(path)-1-k]
		}
		return nil
	}
	switch n := n.(type) {
	case *ast.Document:
		if c.afterFragmentName() {
			c.keywordOn(ContextKeyword)
		} else {
			c.document()
		}
	case *ast.OperationDefinition:
		if c.hasWord && c.word.Start == n.Keyword {
			c.document()
		}
	case *ast.FragmentDefinition:
		if c.hasWord && c.word.Start == n.Keyword {
			c.document()
		}
	case *ast.TypeCondition:
		if c.hasWord && c.word.Start == n.On {
			if _, ok := parent(1).(*ast.InlineFragment); ok {
				c.fragmentSpread()
			} else {
				c.keywordOn(ContextKeyword)
			}
		}
	case *ast.SelectionSet:
		if !c.hasWord && c.inside(n.LBrace, n.RBrace) {
			c.selection(c.outType)
		}
	case *ast.ArgumentList:
		if !c.hasWord && c.inside(n.LParen, n.RParen) {
			c.arguments(n, nil)
		}
	case *ast.Argument:
		if !c.hasWord && n.Colon < 0 {
			if l, ok := parent(1).(*ast.ArgumentList); ok {
				c.arguments(l, nil)
			}
		}
	case *ast.ObjectValue:
		if !c.hasWord && c.inside(n.LBrace, n.RBrace) {
			c.objectFields(n, nil)
		}
	case *ast.ObjectField:
		if !c.hasWord && n.Colon < 0 {
			if o, ok := parent(1).(*ast.ObjectValue); ok {
				c.objectFields(o, nil)
			}
		}
	case *ast.ListValue:
		if !c.hasWord && c.inside(n.LBracket, n.RBracket) {
			c.values(false)
		}
	case *ast.InvalidValue:
		if !n.Missing {
			return
		}
		switch p := parent(1).(type) {
		case *ast.Argument:
			if p.Colon < 0 {
				if l, ok := parent(2).(*ast.ArgumentList); ok {
					c.arguments(l, nil)
				}
				return
			}
		case *ast.ObjectField:
			if p.Colon < 0 {
				if o, ok := parent(2).(*ast.ObjectValue); ok {
					c.objectFields(o, nil)
				}
				return
			}
		}
		c.values(false)
	case *ast.EnumValue, *ast.BooleanValue, *ast.NullValue:
		if c.hasWord {
			c.values(false)
		}
	case *ast.Variable:
		if _, def := parent(1).(*ast.VariableDefinition); !def && !c.hasWord {
			c.variable(n)
		}
	case *ast.ListType:
		if !c.hasWord && c.inside(n.LBracket, n.RBracket) {
			c.variableTypes()
		}
	case *ast.Name:
		if c.hasWord != !n.Missing() || c.hasWord && c.word.Start != n.Start {
			return
		}
		c.name(n, path)
	}
}

// name handles a cursor on a name or a missing-name placeholder.
func (c *completer) name(n *ast.Name, path []ast.Node) {
	k := len(path) - 1
	at := func(i int) ast.Node {
		if k-i >= 0 {
			return path[k-i]
		}
		return nil
	}
	switch p := at(1).(type) {
	case *ast.Field:
		if p.Name == n {
			c.selection(c.parentType)
		}
	case *ast.Argument:
		if l, ok := at(2).(*ast.ArgumentList); ok {
			c.arguments(l, p)
		}
	case *ast.ObjectField:
		if o, ok := at(2).(*ast.ObjectValue); ok {
			c.objectFields(o, p)
		}
	case *ast.Directive:
		c.directives(at(2), p)
	case *ast.FragmentSpread:
		c.fragmentSpread()
	case *ast.Variable:
		if _, def := at(2).(*ast.VariableDefinition); !def {
			c.variable(p)
		}
	case *ast.NamedType:
		for i := 2; at(i) != nil; i++ {
			switch q := at(i).(type) {
			case *ast.TypeCondition:
				_, inline := at(i + 1).(*ast.InlineFragment)
				c.typeCondition(q, inline)
				return
			case *ast.VariableDefinition:
				c.variableTypes()
				return
			case *ast.ListType, *ast.NonNullType:
				continue
			}
			return
		}
	}
}
