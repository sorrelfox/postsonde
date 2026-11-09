package schema

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// sdlOf prints a schema model as SDL, for the round-trip tests: the
// declared types and directives without the built-ins, descriptions as
// quoted strings (lossless), and a schema definition when the roots are
// not named Query, Mutation and Subscription.
func sdlOf(s *Schema) string {
	var b strings.Builder
	desc := func(indent, d string) {
		if d != "" {
			b.WriteString(indent + quoteString(d) + "\n")
		}
	}
	deprecated := func(d Deprecation) {
		if d.IsDeprecated {
			b.WriteString(" @deprecated(reason: " + quoteString(d.DeprecationReason) + ")")
		}
	}
	inputValue := func(v *InputValue) {
		if v.Description != "" {
			b.WriteString(quoteString(v.Description) + " ")
		}
		b.WriteString(v.Name + ": " + v.Type.String())
		if v.DefaultValue != nil {
			b.WriteString(" = " + *v.DefaultValue)
		}
		deprecated(v.Deprecation)
	}
	args := func(vs []*InputValue) {
		if len(vs) == 0 {
			return
		}
		b.WriteString("(")
		for i, v := range vs {
			if i > 0 {
				b.WriteString(", ")
			}
			inputValue(v)
		}
		b.WriteString(")")
	}

	if s.QueryType != "Query" || s.MutationType != "" && s.MutationType != "Mutation" ||
		s.SubscriptionType != "" && s.SubscriptionType != "Subscription" || s.Description != "" {
		desc("", s.Description)
		b.WriteString("schema {\n  query: " + s.QueryType + "\n")
		if s.MutationType != "" {
			b.WriteString("  mutation: " + s.MutationType + "\n")
		}
		if s.SubscriptionType != "" {
			b.WriteString("  subscription: " + s.SubscriptionType + "\n")
		}
		b.WriteString("}\n\n")
	}
	for _, d := range s.Directives {
		if d.IsBuiltin() {
			continue
		}
		desc("", d.Description)
		b.WriteString("directive @" + d.Name)
		args(d.Args)
		if d.IsRepeatable {
			b.WriteString(" repeatable")
		}
		locs := make([]string, len(d.Locations))
		for i, l := range d.Locations {
			locs[i] = string(l)
		}
		b.WriteString(" on " + strings.Join(locs, " | ") + "\n\n")
	}
	for _, t := range s.Types {
		if t.IsBuiltin() {
			continue
		}
		desc("", t.Description)
		switch t.Kind {
		case KindScalar:
			b.WriteString("scalar " + t.Name)
			if t.SpecifiedByURL != "" {
				b.WriteString(" @specifiedBy(url: " + quoteString(t.SpecifiedByURL) + ")")
			}
		case KindObject, KindInterface:
			kw := "type "
			if t.Kind == KindInterface {
				kw = "interface "
			}
			b.WriteString(kw + t.Name)
			if len(t.Interfaces) > 0 {
				b.WriteString(" implements " + strings.Join(t.Interfaces, " & "))
			}
			b.WriteString(" {\n")
			for _, f := range t.Fields {
				desc("  ", f.Description)
				b.WriteString("  " + f.Name)
				args(f.Args)
				b.WriteString(": " + f.Type.String())
				deprecated(f.Deprecation)
				b.WriteString("\n")
			}
			b.WriteString("}")
		case KindUnion:
			b.WriteString("union " + t.Name + " = " + strings.Join(t.PossibleTypes, " | "))
		case KindEnum:
			b.WriteString("enum " + t.Name + " {\n")
			for _, v := range t.EnumValues {
				desc("  ", v.Description)
				b.WriteString("  " + v.Name)
				deprecated(v.Deprecation)
				b.WriteString("\n")
			}
			b.WriteString("}")
		case KindInputObject:
			b.WriteString("input " + t.Name)
			if t.IsOneOf {
				b.WriteString(" @oneOf")
			}
			b.WriteString(" {\n")
			for _, f := range t.InputFields {
				b.WriteString("  ")
				inputValue(f)
				b.WriteString("\n")
			}
			b.WriteString("}")
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// withoutBuiltins is s without the built-in types and directives, which
// a schema built from SDL does not list, and with the possible types of
// interfaces filled in as servers report them (the hand-authored fixture
// leaves them out).
func withoutBuiltins(s *Schema) *Schema {
	out := *s
	out.Types = slices.DeleteFunc(slices.Clone(s.Types), (*Type).IsBuiltin)
	for i, t := range out.Types {
		if t.Kind == KindInterface && len(t.PossibleTypes) == 0 {
			c := *t
			for _, pt := range s.PossibleTypes(t.Name) {
				c.PossibleTypes = append(c.PossibleTypes, pt.Name)
			}
			out.Types[i] = &c
		}
	}
	out.Directives = slices.DeleteFunc(slices.Clone(s.Directives), (*Directive).IsBuiltin)
	out.index()
	return &out
}

// TestSDLRoundTripsFixtures prints each real introspection fixture as SDL
// and loads it back: the SDL is valid (no problems: the type system checks
// accept real schemas) and gives the same schema, compared as
// introspection JSON.
func TestSDLRoundTripsFixtures(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			orig, err := ParseIntrospection(readFixture(t, fx.file))
			if err != nil {
				t.Fatal(err)
			}
			src := sdlOf(orig)
			s, err := ParseSDL(src)
			if err != nil {
				t.Fatalf("ParseSDL: %v\n%s", err, src)
			}
			want, _ := withoutBuiltins(orig).MarshalIntrospection()
			got, _ := s.MarshalIntrospection()
			assertSameIntrospection(t, schemaValue(t, want), schemaValue(t, got))
			fx.check(t, s)
		})
	}
}

// TestSDLFiles loads the SDL files in testdata/sdl: real-world shapes
// (a GitHub-style API, an Apollo Federation subgraph, the Star Wars
// schema of graphql.org, the spec's examples). Each must load without
// problems.
func TestSDLFiles(t *testing.T) {
	files, err := filepath.Glob("testdata/sdl/*.graphql")
	if err != nil || len(files) < 4 {
		t.Fatalf("SDL files: %v %v", files, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			s, err := ParseSDL(string(data))
			if err != nil {
				t.Fatalf("ParseSDL: %v", err)
			}
			if s.Query() == nil || len(s.Types) < 5 {
				t.Fatalf("query root %q, %d types", s.QueryType, len(s.Types))
			}
			// The introspection form converts back without problems: the
			// cache and the docs explorer can use it.
			data2, err := s.MarshalIntrospection()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseIntrospection(data2); err != nil {
				t.Fatalf("ParseIntrospection(introspection of the SDL schema): %v", err)
			}
		})
	}
}

func TestSDLModel(t *testing.T) {
	s, err := ParseSDL(`
"""
The schema.
"""
schema { query: Root mutation: Mut }

"The root."
type Root {
  "A node."
  node(id: ID!): Node
  search(text: String = "a\"b", first: Int = 10, filter: Filter = {tags: ["x"], where: {n: 1.5}}): [Result!]!
  old: String @deprecated
  older(arg: Int @deprecated(reason: "gone")): String @deprecated(reason: "Use node.")
}
type Mut { noop: Boolean }
interface Node { id: ID! }
interface Named implements Node { id: ID! name: String }
type User implements Named & Node @key(fields: "id") { id: ID! name: String }
type Repo implements Node { id: ID! }
union Result = User | Repo
enum Color { RED GREEN @deprecated BLUE }
input Filter { tags: [String!] where: Where }
input Where @oneOf { n: Float s: String }
scalar Date @specifiedBy(url: "https://example.com/date")
directive @key(fields: String!) repeatable on OBJECT | INTERFACE

extend type User { email: String }
extend enum Color { ALPHA }
extend union Result = Mut
extend input Filter { limit: Int = 5 }
directive @tag on SCALAR
extend scalar Date @tag
extend schema { subscription: Sub }
type Sub { tick: Int }
`)
	if err != nil {
		t.Fatalf("ParseSDL: %v", err)
	}
	if s.Description != "The schema." || s.QueryType != "Root" || s.MutationType != "Mut" || s.SubscriptionType != "Sub" {
		t.Errorf("schema = %q %q %q %q", s.Description, s.QueryType, s.MutationType, s.SubscriptionType)
	}
	root := s.Query()
	if root.Description != "The root." || root.Field("node").Description != "A node." {
		t.Errorf("descriptions: %q %q", root.Description, root.Field("node").Description)
	}
	wantArg(t, s, "Root", "search", "text", "String", `"a\"b"`)
	wantArg(t, s, "Root", "search", "first", "Int", "10")
	wantArg(t, s, "Root", "search", "filter", "Filter", `{tags: ["x"], where: {n: 1.5}}`)
	wantField(t, s, "Root", "search", "[Result!]!")
	if f := root.Field("old"); !f.IsDeprecated || f.DeprecationReason != DefaultDeprecationReason {
		t.Errorf("old = %+v", f.Deprecation)
	}
	if f := root.Field("older"); f.DeprecationReason != "Use node." || f.Arg("arg").DeprecationReason != "gone" {
		t.Errorf("older = %+v, arg %+v", f.Deprecation, f.Arg("arg").Deprecation)
	}
	if got := s.Type("User").Interfaces; !slices.Equal(got, []string{"Named", "Node"}) {
		t.Errorf("User implements %v", got)
	}
	if s.Type("User").Field("email") == nil {
		t.Error("the extension's field is missing")
	}
	if got := s.Type("Node").PossibleTypes; !slices.Equal(got, []string{"User", "Repo"}) {
		t.Errorf("Node possible types %v", got)
	}
	if !s.IsSubType("Node", "Named") || !s.IsPossibleType("Result", "Mut") {
		t.Error("subtypes")
	}
	if c := s.Type("Color"); len(c.EnumValues) != 4 || !c.EnumValue("GREEN").IsDeprecated || c.EnumValue("ALPHA") == nil {
		t.Errorf("Color = %+v", c.EnumValues)
	}
	if !s.Type("Where").IsOneOf || s.Type("Filter").IsOneOf || s.Type("Filter").InputField("limit") == nil {
		t.Error("input objects")
	}
	if s.Type("Date").SpecifiedByURL != "https://example.com/date" {
		t.Errorf("specifiedBy = %q", s.Type("Date").SpecifiedByURL)
	}
	if d := s.Directive("key"); d == nil || !d.IsRepeatable || !d.HasLocation(LocInterface) || d.Arg("fields") == nil {
		t.Errorf("@key = %+v", d)
	}
	for _, t2 := range s.Types {
		if t2.IsBuiltin() {
			t.Errorf("built-in %s listed", t2.Name)
		}
	}
	if s.Type("String") == nil || s.Directive("skip") == nil {
		t.Error("built-ins do not resolve")
	}
}

func TestSDLDefaultRoots(t *testing.T) {
	s, err := ParseSDL("type Query { a: Int } type Mutation { b: Int } type Subscription { c: Int }")
	if err != nil || s.QueryType != "Query" || s.MutationType != "Mutation" || s.SubscriptionType != "Subscription" {
		t.Fatalf("roots %q %q %q, %v", s.QueryType, s.MutationType, s.SubscriptionType, err)
	}
}

// TestSDLValidation checks the type system rules, with the positions of
// their problems.
func TestSDLValidation(t *testing.T) {
	const q = "type Query { a: Int }\n"
	tests := []struct {
		name, src string
		want      []string
	}{
		{"unknown type", q + "type A { b: Missing }", []string{`2:13: Unknown type "Missing".`}},
		{"unknown type in list and argument", q + "type A { b(x: [Nope!]): [Gone] }", []string{
			`2:16: Unknown type "Nope".`, `2:26: Unknown type "Gone".`}},
		{"duplicate type", q + "type A { b: Int }\nenum A { X }", []string{`3:6: There can be only one type named "A".`}},
		{"duplicate field", q + "type A { b: Int b: String }", []string{`2:17: Field "A.b" can only be defined once.`}},
		{"duplicate field from an extension", q + "type A { b: Int }\nextend type A { b: Int }", []string{
			`3:17: Field "A.b" can only be defined once.`}},
		{"duplicate argument", q + "type A { b(x: Int, x: Int): Int }", []string{`2:20: Argument "A.b(x:)" can only be defined once.`}},
		{"duplicate enum value", q + "enum E { X Y X }", []string{`2:14: Enum value "E.X" can only be defined once.`}},
		{"duplicate directive", q + "directive @d on FIELD\ndirective @d on FIELD", []string{`3:12: There can be only one directive named "@d".`}},
		{"reserved names", q + "type __A { __b: Int }", []string{
			`2:6: Name "__A" must not begin with "__", which is reserved by GraphQL introspection.`,
			`2:12: Name "__b" must not begin with "__", which is reserved by GraphQL introspection.`}},
		{"built-in scalar redefined", q + "type String { a: Int }", []string{
			`2:6: Type "String" is a built-in scalar and cannot be redefined as an object type.`}},
		{"output type expected", q + "input I { a: Int }\ntype A { b: I }", []string{`3:13: The type of A.b must be Output Type but got: I.`}},
		{"input type expected", q + "type A { b(x: Query): Int }\ninput I { a: Query }", []string{
			`2:15: The type of A.b(x:) must be Input Type but got: Query.`,
			`3:14: The type of I.a must be Input Type but got: Query.`}},
		{"no fields", q + "type A\ninterface B\ninput C\nenum D\nunion E", []string{
			`2:6: Type A must define one or more fields.`,
			`3:11: Type B must define one or more fields.`,
			`4:7: Input Object type C must define one or more fields.`,
			`5:6: Enum type D must define one or more values.`,
			`6:7: Union type E must define one or more member types.`}},
		{"union members", q + "scalar S\nunion U = Query | S | Query", []string{
			`3:19: Union type U can only include Object types, it cannot include S.`,
			`3:23: Union type U can only include type Query once.`}},
		{"implements a non-interface", q + "type A implements Query { a: Int }", []string{
			`2:19: Type A must only implement Interface types, it cannot implement Query.`}},
		{"implements itself", q + "interface I implements I { a: Int }", []string{
			`2:24: Type I cannot implement itself because it would create a circular reference.`}},
		{"implements twice", q + "interface I { a: Int }\ntype A implements I & I { a: Int }", []string{
			`3:23: Type A can only implement I once.`}},
		{"missing transitive interface", q + "interface N { id: ID }\ninterface R implements N { id: ID }\ntype A implements R { id: ID }", []string{
			`4:19: Type A must implement N because it is implemented by R.`}},
		{"missing interface field", q + "interface I { a: Int b: Int }\ntype A implements I { a: Int }", []string{
			`3:19: Interface field I.b expected but A does not provide it.`}},
		{"field type not covariant", q + "interface I { a: Int! u: I }\ntype A implements I { a: Int u: A }\ntype B implements I { a: Int! u: Query }", []string{
			`3:26: Interface field I.a expects type Int! but A.a is type Int.`,
			`4:34: Interface field I.u expects type I but B.u is type Query.`}},
		{"interface arguments", q + "interface I { a(x: Int, y: Int): Int }\ntype A implements I { a(x: String, z: Int!): Int }", []string{
			`3:23: Interface field argument I.a(y:) expected but A.a does not provide it.`,
			`3:28: Interface field argument I.a(x:) expects type Int but A.a(x:) is type String.`,
			`3:36: Argument A.a(z:) must not be required type Int! if not provided by the Interface field I.a.`}},
		{"extension of an unknown type", q + "extend type Nope { a: Int }", []string{`2:13: Cannot extend type "Nope" because it is not defined.`}},
		{"extension of another kind", q + "enum E { X }\nextend input E { a: Int }", []string{`3:14: Cannot extend non-input object type "E".`}},
		{"no query root", "type A { a: Int }", []string{`1:1: Query root type must be provided.`}},
		{"root types", "schema { query: Q mutation: S subscription: Q }\ntype Q { a: Int }\nscalar S", []string{
			`1:17: All root types must be different, "Q" type is used as query and subscription root types.`,
			`1:29: Mutation root type must be Object type, it cannot be S.`}},
		{"two schema definitions", "schema { query: Query }\nschema { query: Query }\n" + q, []string{
			`2:1: Must provide only one schema definition.`}},
		{"duplicate root operation", "schema { query: Query }\nextend schema { query: Query }\n" + q, []string{
			`2:17: There can be only one query type in schema.`}},
		{"input cycle", q + "input A { b: B! }\ninput B { a: A! list: [A!]! }", []string{
			`2:7: Cannot reference Input Object "A" within itself through a series of non-null fields: "b.a".`}},
		{"oneOf", q + "input I @oneOf { a: Int! b: String = \"x\" }", []string{
			`2:21: OneOf input field I.a must be nullable.`,
			`2:26: OneOf input field I.b cannot have a default value.`}},
		{"required deprecated", q + "type A { b(x: Int! @deprecated): Int }\ninput I { a: Int! @deprecated }", []string{
			`2:12: Required argument A.b(x:) cannot be deprecated.`,
			`3:11: Required input field I.a cannot be deprecated.`}},
		{"directive uses", q + "directive @d(x: Int!) on FIELD_DEFINITION\ntype A @d(x: 1) { b: Int @d @d(x: 1, y: 2) @d(x: 1) @nope }", []string{
			`3:8: Directive "@d" may not be used on OBJECT.`,
			`3:26: Argument "@d(x:)" of type "Int!" is required, but it was not provided.`,
			`3:29: The directive "@d" can only be used once at this location.`,
			`3:38: Unknown argument "y" on directive "@d".`,
			`3:44: The directive "@d" can only be used once at this location.`,
			`3:53: Unknown directive "@nope".`}},
		{"directive uses across extensions", q + "scalar S @specifiedBy(url: \"a\")\nextend scalar S @specifiedBy(url: \"b\")", []string{
			`3:17: The directive "@specifiedBy" can only be used once at this location.`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSDL(tt.src)
			if s == nil {
				t.Fatalf("no schema: %v", err)
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != CodeInvalidSchema {
				t.Fatalf("err = %v, want %s", err, CodeInvalidSchema)
			}
			var ps Problems
			errors.As(err, &ps)
			var got []string
			for _, p := range ps {
				got = append(got, p.String())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("problems:\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

func TestParseSDLSyntaxErrors(t *testing.T) {
	s, err := ParseSDL("type Query {\n  a: Int\n  b(: Int\n}")
	if s != nil {
		t.Fatal("a schema from a document with syntax errors")
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeSDLSyntax || e.Kind != errs.Parse {
		t.Fatalf("err = %v", err)
	}
	var ps Problems
	if !errors.As(err, &ps) || len(ps) == 0 || ps[0].Line != 3 || ps[0].Column != 4 {
		t.Fatalf("problems = %v", ps)
	}
	if !strings.Contains(e.Message, "line 3, column 4") {
		t.Errorf("message %q", e.Message)
	}
}
