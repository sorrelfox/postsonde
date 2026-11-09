package parser

import (
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
)

// FuzzParse checks the parser's invariants on arbitrary input: it never
// panics and terminates; every node range lies within the source and within
// its parent; siblings are in source order without overlap; names and
// delimiter offsets match the source; errors are in range, sorted and capped;
// NodePathAt returns a chain of nested nodes; and a document without errors
// has no placeholders.
func FuzzParse(f *testing.F) {
	for _, d := range validDocuments {
		f.Add(d.src)
	}
	for _, d := range halfTyped {
		f.Add(d.src)
	}
	for _, s := range []string{
		"", "{{{{[[[[", "query Q($a: [[[Int = {a: [{b: ", "type T { f(a: Int = {x: 1}): [T!]! }\n{ a }",
		"fragment on on on on { ...on }", "\"\"\"doc\"\"\" schema { query: Q } extend union U = | A",
		"{ a(x: \"\\u{zz}\" y: 01 z: \"\"\"open", "query A {\n  a(\nquery B { b }\nfragment F on T {",
		"{ a: b: c(d: $) @ @e( ... ... on ... { } ] ) }",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		cfg := Config{MaxDepth: 20, MaxErrors: 30}
		doc, errs := cfg.Parse(src)
		if doc == nil || doc.Source != src || doc.Start != 0 || doc.End != len(src) {
			t.Fatalf("bad document %+v", doc)
		}
		checkNode(t, src, doc, ast.Loc{Start: 0, End: len(src)}, 0)

		if len(errs) > cfg.MaxErrors {
			t.Fatalf("%d errors, cap %d", len(errs), cfg.MaxErrors)
		}
		for i, e := range errs {
			if e.Start < 0 || e.Start > e.End || e.End > len(src) || e.Message == "" || e.Code.String() == "" {
				t.Fatalf("bad error %d: %+v", i, e)
			}
			if i > 0 && e.Code != ErrTooManyErrors && errs[i-1].Start > e.Start {
				t.Fatalf("errors not sorted: %+v before %+v", errs[i-1], e)
			}
		}

		if len(errs) == 0 {
			ast.Inspect(doc, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Name:
					if n.Missing() {
						t.Fatalf("missing name at %d without errors", n.Start)
					}
				case *ast.InvalidValue:
					t.Fatalf("invalid value at %d without errors", n.Start)
				case *ast.TypeSystemDefinition:
					t.Fatalf("type system definition without errors")
				}
				return true
			})
		}

		for _, off := range []int{0, len(src) / 2, len(src)} {
			path := ast.NodePathAt(doc, off)
			if len(path) == 0 || path[0] != ast.Node(doc) {
				t.Fatalf("NodePathAt(%d) = %v", off, path)
			}
			for i, n := range path {
				if !n.Pos().Contains(off) {
					t.Fatalf("NodePathAt(%d): %T %v does not contain the offset", off, n, n.Pos())
				}
				if i > 0 && !isChild(path[i-1], n) {
					t.Fatalf("NodePathAt(%d): %T is not a child of %T", off, n, path[i-1])
				}
			}
		}
	})
}

func isChild(parent, child ast.Node) bool {
	for _, c := range ast.Children(parent) {
		if c == child {
			return true
		}
	}
	return false
}

func checkNode(t *testing.T, src string, n ast.Node, parent ast.Loc, depth int) {
	t.Helper()
	l := n.Pos()
	if l.Start < parent.Start || l.End > parent.End || l.Start > l.End {
		t.Fatalf("%T %v not within parent %v", n, l, parent)
	}
	if depth > 4*20+10 {
		t.Fatalf("tree deeper than the nesting limit allows")
	}
	checkFields(t, src, n)
	prev := l.Start
	for _, c := range ast.Children(n) {
		if c == nil || reflect.ValueOf(c).IsNil() {
			t.Fatalf("%T has a nil child", n)
		}
		if c.Pos().Start < prev {
			t.Fatalf("%T child %T %v overlaps its previous sibling (ends %d)", n, c, c.Pos(), prev)
		}
		checkNode(t, src, c, l, depth+1)
		prev = c.Pos().End
	}
}

// checkFields verifies that names hold their source text and delimiter
// offsets point at the delimiter, inside the node.
func checkFields(t *testing.T, src string, n ast.Node) {
	t.Helper()
	at := func(off int, want string) {
		t.Helper()
		if off == -1 {
			return
		}
		l := n.Pos()
		if off < l.Start || off+len(want) > l.End || src[off:off+len(want)] != want {
			t.Fatalf("%T %v: offset %d is not %q", n, l, off, want)
		}
	}
	switch n := n.(type) {
	case *ast.Name:
		if !n.Missing() && src[n.Start:n.End] != n.Value {
			t.Fatalf("name %q at %v, source has %q", n.Value, n.Loc, src[n.Start:n.End])
		}
	case *ast.OperationDefinition:
		if n.Keyword >= 0 {
			at(n.Keyword, n.Operation.String())
		}
	case *ast.FragmentDefinition:
		at(n.Keyword, "fragment")
	case *ast.SelectionSet:
		at(n.LBrace, "{")
		at(n.RBrace, "}")
	case *ast.ArgumentList:
		at(n.LParen, "(")
		at(n.RParen, ")")
	case *ast.VariableDefinitionList:
		at(n.LParen, "(")
		at(n.RParen, ")")
	case *ast.ListValue:
		at(n.LBracket, "[")
		at(n.RBracket, "]")
	case *ast.ListType:
		at(n.LBracket, "[")
		at(n.RBracket, "]")
	case *ast.ObjectValue:
		at(n.LBrace, "{")
		at(n.RBrace, "}")
	case *ast.Field:
		at(n.Colon, ":")
	case *ast.Argument:
		at(n.Colon, ":")
		if n.Value == nil {
			t.Fatalf("argument without value")
		}
	case *ast.ObjectField:
		at(n.Colon, ":")
		if n.Value == nil {
			t.Fatalf("object field without value")
		}
	case *ast.VariableDefinition:
		at(n.Colon, ":")
		at(n.Equals, "=")
		if n.Type == nil || n.Variable == nil {
			t.Fatalf("variable definition without variable or type")
		}
	case *ast.Variable:
		at(n.Dollar, "$")
	case *ast.Directive:
		at(n.At, "@")
	case *ast.FragmentSpread:
		at(n.Spread, "...")
	case *ast.InlineFragment:
		at(n.Spread, "...")
	case *ast.TypeCondition:
		at(n.On, "on")
	case *ast.NonNullType:
		at(n.Bang, "!")
	case *ast.SchemaDefinition:
		at(n.Extend, "extend")
		at(n.Keyword, "schema")
		at(n.LBrace, "{")
		at(n.RBrace, "}")
	case *ast.RootOperationTypeDefinition:
		at(n.Keyword, n.Operation.String())
		at(n.Colon, ":")
	case *ast.TypeDefinition:
		at(n.Extend, "extend")
		at(n.Keyword, n.Kind.String())
		at(n.Implements, "implements")
		at(n.Equals, "=")
		at(n.LBrace, "{")
		at(n.RBrace, "}")
	case *ast.FieldDefinition:
		at(n.Colon, ":")
		if n.Type == nil {
			t.Fatalf("field definition without type")
		}
	case *ast.ArgumentDefinitionList:
		at(n.LParen, "(")
		at(n.RParen, ")")
	case *ast.InputValueDefinition:
		at(n.Colon, ":")
		at(n.Equals, "=")
		if n.Type == nil {
			t.Fatalf("input value definition without type")
		}
	case *ast.DirectiveDefinition:
		at(n.Keyword, "directive")
		at(n.At, "@")
		at(n.Repeatable, "repeatable")
		at(n.On, "on")
	}
}
