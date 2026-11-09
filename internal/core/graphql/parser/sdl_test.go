package parser

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// renderSchema is the golden form of ParseSchema, like render.
func renderSchema(src string) string {
	doc, errs := ParseSchema(src)
	var b strings.Builder
	fmt.Fprintf(&b, "# source\n%s\n# tree\n%s# errors\n", src, ast.Dump(doc))
	for _, e := range errs {
		fmt.Fprintf(&b, "%v [%d:%d] %s: %s\n", doc.Position(e.Start), e.Start, e.End, e.Code, e.Message)
	}
	return b.String()
}

// validSchemas are type system documents from the spec's type system
// chapter (sections 3.1 to 3.13, with the examples' names) and corner
// cases of the grammar. The printer's SDL corpus reads them from the
// golden files.
var validSchemas = []struct{ name, src string }{
	{"schema definition", `"""
The root of the schema.
"""
schema @link(url: "https://example.com") {
  query: MyQueryRootType
  mutation: MyMutationRootType
  subscription: MySubscriptionRootType
}

type MyQueryRootType { someField: String }`},
	{"descriptions", `"""
A simple GraphQL schema which is well described.
"""
schema {
  query: Query
}

"""
Root type for all your query operations
"""
type Query {
  """
  Translates a string from a given language into a different language.
  """
  translate(
    "The original language that ` + "`text`" + ` is provided in."
    fromLanguage: Language

    "The translated language to be returned."
    toLanguage: Language

    "The text to be translated."
    text: String
  ): String
}

"""
The set of languages supported by ` + "`translate`" + `.
"""
enum Language {
  "English"
  EN

  "French"
  FR

  "Chinese"
  CH
}`},
	{"scalars", `scalar Time
scalar Url @specifiedBy(url: "https://tools.ietf.org/html/rfc3986")
extend scalar Url @example`},
	{"objects and interfaces", `type Person implements NamedEntity & ValuedEntity {
  name: String
  age: Int
  picture(size: Int = 50, "The format" format: Format = PNG @deprecated): Url
  relationship: Person
}

interface NamedEntity { name: String }
interface ValuedEntity { value: Int }

interface Resource implements & Node {
  id: ID!
  url: String
}

type Business implements
  & NamedEntity
  & ValuedEntity {
  name: String
  value: Int
  employeeCount: Int
}

type Empty

extend type Story { isHiddenLocally: Boolean }
extend type User @addedDirective
extend type Person implements Node
extend interface NamedEntity { nickname: String }
extend interface Resource @key`},
	{"unions", `union SearchResult = Photo | Person
union Wrapped =
  | Photo
  | Person
union Pending
extend union SearchResult = Video
extend union SearchResult @deprecated`},
	{"enums", `enum Direction {
  NORTH
  EAST @deprecated(reason: "no")
  SOUTH
  WEST
}
extend enum Direction { UP DOWN }
extend enum Direction @flag`},
	{"input objects", `input Point2D {
  x: Float
  y: Float = 0.5 @deprecated
  tags: [[String!]!] = [["a"], []]
  where: Filter = {field: "name", op: EQ, values: [1, null]}
}
input Filter @oneOf { a: Int b: String }
extend input Point2D { z: Float }
extend input Point2D @x`},
	{"directives", `directive @example on FIELD_DEFINITION | ARGUMENT_DEFINITION
directive @delegateField(name: String!) repeatable on OBJECT | INTERFACE
directive @pipe on
  | FIELD
  | FRAGMENT_SPREAD
  | INLINE_FRAGMENT
"Documented" directive @d(
  "an argument" a: Int = 1 @deprecated
) on QUERY | MUTATION | SUBSCRIPTION | FRAGMENT_DEFINITION | VARIABLE_DEFINITION | SCHEMA | SCALAR | OBJECT | UNION | ENUM | ENUM_VALUE | INPUT_OBJECT | INPUT_FIELD_DEFINITION`},
	{"schema extension", `extend schema @link(url: "x")
extend schema { mutation: M }
extend schema @a { subscription: S }`},
	{"keywords as names", `type type { type: type interface: interface input(input: input): input enum: enum on: on implements: implements repeatable: repeatable extend: extend schema: schema directive: directive }
enum on { type input on extend }
input schema { query: query }`},
	{"comments and commas", "# a schema\ntype Query { a: Int, b: Int, # trailing\n  c(x: Int, y: Int): Int }\n\n# the end"},
}

func TestValidSchemas(t *testing.T) {
	t.Parallel()

	for _, tt := range validSchemas {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc, errs := ParseSchema(tt.src)
			if len(errs) != 0 {
				t.Fatalf("ParseSchema(%q) errors: %v", tt.src, errs)
			}
			checkNode(t, tt.src, doc, ast.Loc{Start: 0, End: len(tt.src)}, 0)
			ast.Inspect(doc, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Name:
					if n.Missing() {
						t.Errorf("missing name at %d in a valid document", n.Start)
					}
				case *ast.InvalidValue:
					t.Errorf("invalid value at %d in a valid document", n.Start)
				}
				return true
			})
			golden.Assert(t, "sdl/"+strings.ReplaceAll(tt.name, " ", "_"), []byte(renderSchema(tt.src)))
		})
	}
}

func TestParseSchemaShapes(t *testing.T) {
	t.Parallel()

	doc, errs := ParseSchema(`"Obj" type A implements B & C @d { "F" f(a: Int = 1): [A!]! @e }
extend union U = | X | Y
directive @r(x: Int) repeatable on OBJECT | FIELD`)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(doc.Definitions) != 3 {
		t.Fatalf("definitions = %d", len(doc.Definitions))
	}
	a := doc.Definitions[0].(*ast.TypeDefinition)
	if a.Kind != ast.KindObject || a.Name.Value != "A" || a.Description.Value != "Obj" || a.IsExtension() ||
		len(a.Interfaces) != 2 || a.Interfaces[1].Name.Value != "C" || len(a.Directives) != 1 || len(a.Fields) != 1 {
		t.Fatalf("type A = %s", ast.Dump(a))
	}
	f := a.Fields[0]
	if f.Description.Value != "F" || f.Name.Value != "f" || len(f.Arguments.Arguments) != 1 ||
		f.Arguments.Arguments[0].DefaultValue.(*ast.IntValue).Raw != "1" || len(f.Directives) != 1 {
		t.Fatalf("field f = %s", ast.Dump(f))
	}
	if _, ok := f.Type.(*ast.NonNullType); !ok {
		t.Fatalf("field type %T", f.Type)
	}
	u := doc.Definitions[1].(*ast.TypeDefinition)
	if !u.IsExtension() || u.Kind != ast.KindUnion || len(u.Members) != 2 || u.Members[0].Name.Value != "X" {
		t.Fatalf("union U = %s", ast.Dump(u))
	}
	d := doc.Definitions[2].(*ast.DirectiveDefinition)
	if d.Name.Value != "r" || d.Repeatable < 0 || len(d.Locations) != 2 || d.Locations[1].Value != "FIELD" {
		t.Fatalf("directive = %s", ast.Dump(d))
	}
}

func TestParseSchemaErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want []string // "line:column code: message"
	}{
		{"type A { }", []string{`1:10 unexpected token: expected field definition, found "}"`}},
		{"type A { a }", []string{`1:12 unexpected token: expected ":", found "}"`}},
		{"type A { a: }", []string{`1:13 unexpected token: expected type name, found "}"`}},
		{"type A {\n  a: Int\n\ntype B { b: Int }", []string{`1:8 unclosed delimiter: "{" has no matching "}"`}},
		{"enum E { A true }", []string{`1:12 reserved name: an enum value cannot be named "true"`}},
		{"union U = A |", []string{`1:14 unexpected token: expected union member, found end of input`}},
		{"directive @d on FIELD | NOWHERE", []string{`1:25 unexpected token: unknown directive location "NOWHERE"`}},
		{"directive @d(a: Int)", []string{`1:21 unexpected token: expected "on", found end of input`}},
		{"directive d on FIELD", []string{`1:11 unexpected token: expected "@", found name "d"`}},
		{"extend type A", []string{`1:14 unexpected token: expected something to extend "A" with, found end of input`}},
		{"extend directive @d on FIELD", []string{`1:8 unexpected token: expected schema, scalar, type, interface, union, enum or input after "extend", found name "directive"`}},
		{`"desc" extend type A @d`, []string{`1:1 unexpected token: an extension cannot have a description`}},
		{"schema { query: Q, fragment: F }", []string{
			`1:20 unexpected token: expected query, mutation or subscription, found name "fragment"`,
			`1:28 unexpected token: expected query, mutation or subscription, found ":"`,
			`1:30 unexpected token: expected query, mutation or subscription, found name "F"`}},
		{"schema @d", []string{`1:10 unexpected token: expected "{", found end of input`}},
		{"type A { a: Int }\nquery Q { a }", []string{`2:1 not a type system definition: an operation is not allowed in a schema document`}},
		{"fragment F on A { a }", []string{`1:1 not a type system definition: a fragment is not allowed in a schema document`}},
		{"type A { a(x: Int = $v): Int }", []string{`1:21 variable in constant value: variable not allowed in a constant value`}},
		{"type A { a(): Int }", []string{`1:12 unexpected token: expected argument definition, found ")"`}},
		{"foo bar\ntype A { a: Int }", []string{
			`1:1 unexpected token: expected a type system definition (type, interface, union, enum, input, scalar, directive, schema or extend), found name "foo"`}},
	}
	for _, tt := range tests {
		doc, errs := ParseSchema(tt.src)
		var got []string
		for _, e := range errs {
			got = append(got, fmt.Sprintf("%v %s: %s", doc.Position(e.Start), e.Code, e.Message))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("ParseSchema(%q) errors:\n%s\nwant\n%s", tt.src, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
		checkNode(t, tt.src, doc, ast.Loc{Start: 0, End: len(tt.src)}, 0)
	}
}

func TestParseSchemaRecoversAtNextDefinition(t *testing.T) {
	t.Parallel()

	src := "type A {\n  a(x: Int\n\n\"B\"\ntype B { b: Int }\nenum E { X }"
	doc, errs := ParseSchema(src)
	if len(errs) == 0 {
		t.Fatal("no errors")
	}
	var names []string
	for _, d := range doc.Definitions {
		names = append(names, d.(*ast.TypeDefinition).Name.Value)
	}
	if !slices.Equal(names, []string{"A", "B", "E"}) {
		t.Fatalf("definitions %v, want A B E", names)
	}
	if b := doc.Definitions[1].(*ast.TypeDefinition); b.Description == nil || b.Description.Value != "B" {
		t.Fatalf("B lost its description: %s", ast.Dump(b))
	}
}

// FuzzParseSchema checks ParseSchema's invariants like FuzzParse. Run it
// with go test ./internal/core/graphql/parser -run '^$' -fuzz FuzzParseSchema -fuzztime 60s
func FuzzParseSchema(f *testing.F) {
	for _, d := range validSchemas {
		f.Add(d.src)
	}
	for _, s := range []string{"", "type", "extend", "\"x\"", "type A {", "directive @", "union U = |",
		"schema { query", "input I { a: [[[", "enum E { A @d(x: {a: [", "interface I implements & & {"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		cfg := Config{MaxDepth: 20, MaxErrors: 30}
		doc, errs := cfg.ParseSchema(src)
		if doc == nil || doc.Source != src {
			t.Fatalf("bad document %+v", doc)
		}
		checkNode(t, src, doc, ast.Loc{Start: 0, End: len(src)}, 0)
		if len(errs) > cfg.MaxErrors {
			t.Fatalf("%d errors, cap %d", len(errs), cfg.MaxErrors)
		}
		for i, e := range errs {
			if e.Start < 0 || e.Start > e.End || e.End > len(src) || e.Message == "" {
				t.Fatalf("bad error %d: %+v", i, e)
			}
		}
		if len(errs) == 0 {
			ast.Inspect(doc, func(n ast.Node) bool {
				if n, ok := n.(*ast.Name); ok && n.Missing() {
					t.Fatalf("missing name at %d without errors", n.Start)
				}
				return true
			})
		}
	})
}
