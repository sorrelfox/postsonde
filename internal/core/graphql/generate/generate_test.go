package generate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/printer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// bigSDL is a schema of several hundred types full of cycles: objects that
// point to the next one, their parent, themselves and lists of others;
// interfaces implemented by some of them, unions whose members share field
// names of other types, required and optional arguments, recursive input
// objects, enums, and all three root types.
func bigSDL(objects int) string {
	var b strings.Builder
	b.WriteString("interface Node { id: ID! }\n")
	b.WriteString("interface Named implements Node { id: ID! name: String }\n")
	for k := range 10 {
		fmt.Fprintf(&b, "enum Kind%d { A B C }\n", k)
		fmt.Fprintf(&b, "input Filter%d { term: String kind: Kind%d nested: Filter%d }\n", k, k, (k+1)%10)
	}
	for i := range objects {
		// Every fifth object is a Node, every fifteenth also Named.
		ifaces := ""
		switch {
		case i%15 == 0:
			ifaces = " implements Node & Named"
		case i%5 == 0:
			ifaces = " implements Node"
		}
		value := "Int"
		if i%2 == 1 {
			value = "String"
		}
		fmt.Fprintf(&b, `"""Type %d."""
type T%d%s {
  id: ID!
  name: String
  count: Int
  value: %s
  kind: Kind%d
  next: T%d
  parent: T%d!
  self: T%d
  friends(first: Int = 10): [T%d!]!
  search(term: String!): [Result%d]
  lookup(filter: Filter%d!): T%d
  node: Node
  old: String @deprecated(reason: "Gone.")
}
`, i, i, ifaces, value, i%10, (i+1)%objects, i/2, i, (i*7)%objects, i%20, i%10, (i+3)%objects)
	}
	for k := range 20 {
		fmt.Fprintf(&b, "union Result%d = T%d | T%d | T%d\n", k, k, k+1, k+2)
	}
	b.WriteString("type Wrapper { a: T0 b: T1 }\n")
	b.WriteString("type Query {\n  node(id: ID!): Node\n  named: [Named!]!\n  viewer: T0!\n  version: String\n  wrapper: Wrapper\n  query: Query!\n  result(k: Int!): Result0\n")
	for i := range objects {
		fmt.Fprintf(&b, "  t%d(id: ID!, filter: Filter%d): T%d\n", i, i%10, i)
	}
	b.WriteString("}\n")
	b.WriteString("type Mutation {\n")
	for i := range 50 {
		fmt.Fprintf(&b, "  update%d(id: ID!, input: Filter%d!, dryRun: Boolean = false): T%d!\n", i, i%10, i)
	}
	b.WriteString("}\n")
	b.WriteString("type Subscription { changed(id: ID!): Node ticks: Int }\n")
	return b.String()
}

func bigSchema(t testing.TB, objects int) *schema.Schema {
	t.Helper()
	s, err := schema.ParseSDL(bigSDL(objects))
	if err != nil {
		t.Fatalf("ParseSDL = %v", err)
	}
	return s
}

// withoutUnusedFragments are the default rules but NoUnusedFragments: a
// fragment is written on its own.
func withoutUnusedFragments() []validate.Rule {
	var out []validate.Rule
	for _, r := range validate.DefaultRules() {
		if r.Name != validate.NoUnusedFragments.Name {
			out = append(out, r)
		}
	}
	return out
}

// checkValid fails when doc does not parse, does not validate against s,
// is not in Prettify's layout, or has fields that could not merge.
func checkValid(t *testing.T, s *schema.Schema, doc string, fragmentsAlone bool) {
	t.Helper()
	d, errs := parser.Parse(doc)
	if len(errs) > 0 {
		t.Fatalf("syntax errors %v in\n%s", errs, doc)
	}
	rules := validate.DefaultRules()
	if fragmentsAlone {
		rules = withoutUnusedFragments()
	}
	if diags := validate.ValidateRules(s, d, rules); len(diags) > 0 {
		t.Fatalf("diagnostics %v in\n%s", diags, doc)
	}
	checkMergeable(t, s, d, doc)
}

// checkMergeable is a strict form of the spec's OverlappingFieldsCanBeMerged,
// which the validator does not run: in each selection set, with its
// inline fragments flattened, one response name has one type, and one field
// and arguments where the parent types could be the same object.
func checkMergeable(t *testing.T, s *schema.Schema, d *ast.Document, doc string) {
	t.Helper()
	type seen struct {
		parent, field, args, typ string
	}
	var check func(set *ast.SelectionSet, parent *schema.Type)
	check = func(set *ast.SelectionSet, parent *schema.Type) {
		names := map[string]seen{}
		var visit func(set *ast.SelectionSet, parent *schema.Type)
		visit = func(set *ast.SelectionSet, parent *schema.Type) {
			for _, sel := range set.Selections {
				switch sel := sel.(type) {
				case *ast.Field:
					def := s.Field(parent.Name, sel.Name.Value)
					if def == nil {
						t.Fatalf("unknown field %s.%s in\n%s", parent.Name, sel.Name.Value, doc)
					}
					resp := sel.Name.Value
					if sel.Alias != nil {
						resp = sel.Alias.Value
					}
					args := ""
					if sel.Arguments != nil {
						args = doc[sel.Arguments.Start:sel.Arguments.End]
					}
					cur := seen{parent.Name, sel.Name.Value, args, def.Type.String()}
					if prev, ok := names[resp]; ok {
						sameParent := prev.parent == cur.parent || s.Type(prev.parent).IsAbstract() || parent.IsAbstract()
						if prev.typ != cur.typ || (sameParent && (prev.field != cur.field || prev.args != cur.args)) {
							t.Fatalf("%q cannot merge: %+v and %+v in\n%s", resp, prev, cur, doc)
						}
					}
					names[resp] = cur
					if sel.SelectionSet != nil {
						check(sel.SelectionSet, s.TypeOf(def.Type))
					}
				case *ast.InlineFragment:
					visit(sel.SelectionSet, s.Type(sel.TypeCondition.Type.Name.Value))
				}
			}
		}
		visit(set, parent)
	}
	for _, def := range d.Definitions {
		switch def := def.(type) {
		case *ast.OperationDefinition:
			check(def.SelectionSet, s.RootType(schema.Operation(def.Operation.String())))
		case *ast.FragmentDefinition:
			check(def.SelectionSet, s.Type(def.TypeCondition.Type.Name.Value))
		}
	}
}

func TestOperationExample(t *testing.T) {
	t.Parallel()
	s, err := schema.ParseSDL(`
type Query { user(id: ID!, active: Boolean = true): User, me: User }
type User { id: ID! name: String posts(first: Int!): [Post!]! best: Post friends: [User!]! old: Int @deprecated }
type Post { id: ID! title: String author: User! }
`)
	if err != nil {
		t.Fatal(err)
	}
	got := Operation(s, schema.OpQuery, "user", Options{}, func(n string) bool { return n == "User" })
	want := `query User2($id: ID!, $first: Int!) {
  user(id: $id) {
    id
    name
    posts(first: $first) {
      id
      title
    }
    best {
      id
      title
    }
  }
}`
	if got.Text != want || got.Name != "User2" || got.Reason != "" || got.Truncated {
		t.Errorf("Operation =\n%s\n%+v", got.Text, got)
	}
	// Depth 1: the leaf fields only. Depth 3: posts.author is cut (User is
	// already selected), friends too.
	if got := Operation(s, schema.OpQuery, "me", Options{Depth: 1}, nil); got.Text != "query Me {\n  me {\n    id\n    name\n  }\n}" {
		t.Errorf("depth 1 =\n%s", got.Text)
	}
	got = Fragment(s, "User", "", Options{Depth: 3}, nil)
	if got.Name != "UserFields" || strings.Contains(got.Text, "author") || strings.Contains(got.Text, "friends") || strings.Contains(got.Text, "posts") || strings.Contains(got.Text, "old") {
		t.Errorf("fragment =\n%s", got.Text)
	}
	if got := Fragment(s, "User", "best", Options{}, nil); got.Text != "fragment UserBest on User {\n  best {\n    id\n    title\n  }\n}" {
		t.Errorf("field fragment =\n%s", got.Text)
	}
	if got := Fragment(s, "User", "posts", Options{}, nil); got.Reason != ReasonRequiredArguments {
		t.Errorf("fragment with required arguments = %+v", got)
	}
	if got := Fragment(s, "ID", "", Options{}, nil); got.Reason != ReasonUnknownType {
		t.Errorf("scalar fragment = %+v", got)
	}
	if got := Operation(s, schema.OpQuery, "nope", Options{}, nil); got.Reason != ReasonUnknownField {
		t.Errorf("unknown field = %+v", got)
	}
	if got := Operation(s, schema.OpMutation, "x", Options{}, nil); got.Reason != ReasonUnknownType {
		t.Errorf("no mutation root = %+v", got)
	}
}

func TestAbstractTypes(t *testing.T) {
	t.Parallel()
	s, err := schema.ParseSDL(`
type Query { search: [Result!]!, node(id: ID!): Node }
interface Node { id: ID! }
type A implements Node { id: ID! value: Int item(id: ID!): A }
type B implements Node { id: ID! value: String label: String }
union Result = A | B
`)
	if err != nil {
		t.Fatal(err)
	}
	got := Operation(s, schema.OpQuery, "search", Options{}, nil)
	want := `query Search {
  search {
    __typename
    ... on A {
      id
      value
    }
    ... on B {
      id
      valueB: value
      label
    }
  }
}`
	if got.Text != want {
		t.Errorf("union =\n%s", got.Text)
	}
	checkValid(t, s, got.Text, false)
	got = Operation(s, schema.OpQuery, "node", Options{}, nil)
	want = `query Node($id: ID!) {
  node(id: $id) {
    __typename
    id
    ... on A {
      value
    }
    ... on B {
      valueB: value
      label
    }
  }
}`
	if got.Text != want {
		t.Errorf("interface =\n%s", got.Text)
	}
	checkValid(t, s, got.Text, false)
}

// TestLargeSchemaDocumentsAreValid writes an operation for every root
// field and a fragment for every composite type of a schema of several
// hundred types with cycles, at several depths, and validates each.
func TestLargeSchemaDocumentsAreValid(t *testing.T) {
	t.Parallel()
	s := bigSchema(t, 300)
	for _, depth := range []int{1, 2, 3} {
		opts := Options{Depth: depth}
		for _, op := range []schema.Operation{schema.OpQuery, schema.OpMutation, schema.OpSubscription} {
			root := s.RootType(op)
			for i, f := range root.Fields {
				if depth != DefaultDepth && i%7 != 0 {
					continue // every field at the default depth, a sample at the others
				}
				got := Operation(s, op, f.Name, opts, nil)
				if got.Reason != "" {
					t.Fatalf("%s %s: %v", op, f.Name, got.Reason)
				}
				checkValid(t, s, got.Text, false)
			}
		}
		for i, typ := range s.Types {
			if !typ.IsComposite() || strings.HasPrefix(typ.Name, "__") || depth != DefaultDepth && i%7 != 0 {
				continue
			}
			got := Fragment(s, typ.Name, "", opts, nil)
			if got.Reason != "" {
				t.Fatalf("fragment on %s: %v", typ.Name, got.Reason)
			}
			checkValid(t, s, got.Text, true)
			if depth != DefaultDepth || i%3 != 0 {
				continue
			}
			for _, f := range typ.Fields {
				got := Fragment(s, typ.Name, f.Name, opts, nil)
				if got.Reason == ReasonRequiredArguments {
					continue
				}
				checkValid(t, s, got.Text, true)
			}
		}
	}
	// The recursive root field Query.query stops at Query's own fields.
	got := Operation(s, schema.OpQuery, "query", Options{Depth: 3}, nil)
	checkValid(t, s, got.Text, false)
	if !strings.Contains(got.Text, "\n  query {\n") || strings.Contains(got.Text, "\n    query {") {
		t.Errorf("recursion not cut:\n%s", got.Text)
	}
	// Wrapper at depth 1 has no leaf field: __typename keeps the set valid.
	if got := Fragment(s, "Wrapper", "", Options{Depth: 1}, nil); got.Text != "fragment WrapperFields on Wrapper {\n  __typename\n}" {
		t.Errorf("wrapper = %s", got.Text)
	}
}

func TestBudget(t *testing.T) {
	t.Parallel()
	s := bigSchema(t, 300)
	got := Operation(s, schema.OpQuery, "query", Options{Depth: MaxDepth}, nil)
	if !got.Truncated {
		t.Errorf("not truncated: %d lines", strings.Count(got.Text, "\n"))
	}
	if n := strings.Count(got.Text, "\n"); n > 2*MaxFields+10 {
		t.Errorf("%d lines past the budget", n)
	}
	checkValid(t, s, got.Text, false)
	// The leaf fields got the budget first.
	if !strings.Contains(got.Text, "\n    version\n") {
		t.Errorf("leaf field version missing")
	}
}

// fill applies Fill at the "|" of text and returns the new text.
func fill(t *testing.T, s *schema.Schema, text string, depth int) (string, FillResult) {
	t.Helper()
	i := strings.Index(text, "|")
	src := text[:i] + text[i+1:]
	r := Fill(s, src, i, Options{Depth: depth})
	if r.Reason != "" {
		return src, r
	}
	return src[:r.From] + r.Text + src[r.To:], r
}

func TestFill(t *testing.T) {
	t.Parallel()
	s, err := schema.ParseSDL(`
type Query { user(id: ID!): User, users: [User!]! }
type User { id: ID! name: String email: String posts(first: Int!): [Post!]! best: Post }
type Post { id: ID! title: String }
`)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, in, want string }{
		{"blank line", "query {\n  user(id: 1) {\n    id\n    |\n  }\n}", "query {\n  user(id: 1) {\n    id\n    name\n    email\n  }\n}"},
		{"after a field", "{ users { id| } }", "{ users { id\n  name\n  email\n} }"},
		{"empty braces", "query Q {\n  users {|}\n}", "query Q {\n  users {\n    id\n    name\n    email\n  }\n}"},
		{"before a field", "{\n  users {\n    |email\n  }\n}", "{\n  users {\n    id\n    name\n    email\n  }\n}"},
		{"aliases block their names", "{ users {\n  name: email\n  | } }", "{ users {\n  name: email\n  id\n} }"},
	}
	for _, c := range cases {
		got, r := fill(t, s, c.in, 1)
		if got != c.want {
			t.Errorf("%s: got\n%s\nwant\n%s\n(%+v)", c.name, got, c.want, r)
		}
	}
	got, r := fill(t, s, "{\n  users {\n    |\n  }\n}", 2)
	if want := "{\n  users {\n    id\n    name\n    email\n    best {\n      id\n      title\n    }\n  }\n}"; got != want || r.Type != "User" || r.Count != 4 {
		t.Errorf("depth 2: got\n%s\n%+v", got, r)
	}
	for _, in := range []string{"{ user(|id: 1) { id } }", "{ us|ers { id } }", "|{ users { id } }", "{ users { id } }|", "# {|\n{ users { id } }", `{ user(id: "|") { id } }`} {
		if _, r := fill(t, s, in, 1); r.Reason != ReasonNotInSelection {
			t.Errorf("%q: %+v", in, r)
		}
	}
	if _, r := fill(t, s, "{ users { id name email | } }", 1); r.Reason != ReasonNothingToAdd {
		t.Errorf("all selected: %+v", r)
	}
	if _, r := fill(t, s, "{ nope { | } }", 1); r.Reason != ReasonUnknownType {
		t.Errorf("unknown type: %+v", r)
	}
	// The root selection set.
	got, _ = fill(t, s, "{\n  |\n}", 2)
	if got != "{\n  users {\n    id\n    name\n    email\n  }\n}" {
		t.Errorf("root: got\n%s", got)
	}
}

func TestFillLargeSchemaIsValid(t *testing.T) {
	t.Parallel()
	s := bigSchema(t, 300)
	for _, typ := range s.Types {
		if !typ.IsComposite() || strings.HasPrefix(typ.Name, "__") {
			continue
		}
		for _, depth := range []int{1, 2} {
			in := "fragment F on " + typ.Name + " {\n  id\n  |\n}"
			if typ.Kind == schema.KindUnion || typ.Name == "Query" || typ.Name == "Mutation" || typ.Name == "Subscription" || typ.Name == "Wrapper" {
				in = "fragment F on " + typ.Name + " {\n  __typename\n  |\n}"
			}
			got, r := fill(t, s, in, depth)
			if r.Reason == ReasonNothingToAdd && (typ.Name == "Wrapper" && depth == 1 || typ.Name == "Mutation") {
				continue // only object fields, which need depth 2
			}
			if r.Reason != "" {
				t.Fatalf("%s: %v", typ.Name, r.Reason)
			}
			checkValid(t, s, got, true)
			if pretty, errs := printer.Prettify(got); len(errs) > 0 || strings.TrimRight(pretty, "\n") != got {
				t.Fatalf("%s: not laid out like Prettify:\n%q\n%q", typ.Name, got, pretty)
			}
		}
	}
}

func TestTargetAt(t *testing.T) {
	t.Parallel()
	s, err := schema.ParseSDL(`
type Query { user(id: ID!, filter: Filter): User }
type User { id: ID! kind: Kind }
enum Kind { A B }
input Filter { kind: Kind nested: Filter }
`)
	if err != nil {
		t.Fatal(err)
	}
	doc := `query Q($f: Filter) {
  u: user(id: 1, filter: {nested: {kind: A}}) @include(if: true) {
    id
    __typename
    ...F
  }
}
fragment F on User { kind }`
	at := func(word string, nth int) int {
		i := -1
		for range nth + 1 {
			i += 1 + strings.Index(doc[i+1:], word)
		}
		return i + 1 // inside the word
	}
	cases := []struct {
		word string
		nth  int
		want Target
	}{
		{"user", 0, Target{Kind: TargetField, Type: "Query", Member: "user", Name: "user"}},
		{"u:", 0, Target{Kind: TargetField, Type: "Query", Member: "user", Name: "u"}},
		{"id:", 0, Target{Kind: TargetArgument, Type: "Query", Member: "user", Name: "id"}},
		{"nested", 0, Target{Kind: TargetInputField, Type: "Filter", Member: "nested", Name: "nested"}},
		{"kind:", 0, Target{Kind: TargetInputField, Type: "Filter", Member: "kind", Name: "kind"}},
		{"A}", 0, Target{Kind: TargetEnumValue, Type: "Kind", Member: "A", Name: "A"}},
		{"include", 0, Target{Kind: TargetDirective, Directive: "include", Name: "include"}},
		{"if:", 0, Target{Kind: TargetArgument, Directive: "include", Name: "if"}},
		{"Filter", 0, Target{Kind: TargetType, Type: "Filter", Name: "Filter"}},
		{"id\n", 0, Target{Kind: TargetField, Type: "User", Member: "id", Name: "id"}},
		{"__typename", 0, Target{Kind: TargetType, Type: "String", Name: "__typename"}},
		{"F\n", 0, Target{Kind: TargetType, Type: "User", Name: "F"}},
		{"User", 0, Target{Kind: TargetType, Type: "User", Name: "User"}},
		{"kind }", 0, Target{Kind: TargetField, Type: "User", Member: "kind", Name: "kind"}},
	}
	for _, c := range cases {
		off := at(c.word, c.nth)
		got, ok := TargetAt(s, doc, off)
		got.Start, got.End = 0, 0
		if !ok || got != c.want {
			t.Errorf("%q: got %+v, %v; want %+v", c.word, got, ok, c.want)
		}
	}
	for _, word := range []string{"query", "Q(", "1,", "true"} {
		if got, ok := TargetAt(s, doc, at(word, 0)); ok {
			t.Errorf("%q: got %+v", word, got)
		}
	}
	if _, ok := TargetAt(nil, doc, 5); ok {
		t.Error("no schema: found")
	}
}
