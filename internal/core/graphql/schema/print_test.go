package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDLExportRoundTripsIntrospection is the GQL-20 round trip:
// introspection JSON → schema → SDL → ParseSDL → the same schema,
// compared as introspection JSON, for every real introspection fixture.
// The exported SDL loads without problems, and printing the loaded
// schema again gives the same text.
func TestSDLExportRoundTripsIntrospection(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			orig, err := ParseIntrospection(readFixture(t, fx.file))
			if err != nil {
				t.Fatal(err)
			}
			src := SDL(orig)
			back, err := ParseSDL(src)
			if err != nil {
				t.Fatalf("ParseSDL: %v\n%s", err, src)
			}
			want, _ := withoutBuiltins(orig).MarshalIntrospection()
			got, _ := back.MarshalIntrospection()
			assertSameIntrospection(t, schemaValue(t, want), schemaValue(t, got))
			fx.check(t, back)
			if again := SDL(back); again != src {
				t.Errorf("SDL of the loaded schema differs:\n%s\n---\n%s", again, src)
			}
		})
	}
}

// TestSDLExportRoundTripsSDLFiles prints the schemas of testdata/sdl and
// loads them back.
func TestSDLExportRoundTripsSDLFiles(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "sdl", "*.graphql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no SDL files: %v", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			orig, err := ParseSDL(string(data))
			if err != nil {
				t.Fatal(err)
			}
			src := SDL(orig)
			back, err := ParseSDL(src)
			if err != nil {
				t.Fatalf("ParseSDL: %v\n%s", err, src)
			}
			want, _ := orig.MarshalIntrospection()
			got, _ := back.MarshalIntrospection()
			assertSameIntrospection(t, schemaValue(t, want), schemaValue(t, got))
		})
	}
}

// TestSDLExportDescriptions checks that every description reads back
// exactly, block string or not.
func TestSDLExportDescriptions(t *testing.T) {
	descs := []string{
		"Plain.",
		"Two\nlines.",
		"Indented\n  second line\n    third",
		"  leading spaces on one line",
		"  leading spaces\nthen a line",
		"  all\n  lines\n  indented",
		"\nstarts with a blank line",
		"ends with a blank line\n",
		"blank\n\nline inside",
		"whitespace\n   \nline inside",
		`ends with a quote "x"`,
		`ends with a backslash \`,
		`has """ triple quotes`,
		"a\\path and \\\"\"\" an escape",
		"tab\tinside",
		"\ttab first\nsecond",
		"carriage\r\nreturn",
		"control \x01 character",
		"unicode: é ✓ 🚀",
		"trailing spaces   \nkept   ",
		"",
	}
	s := &Schema{QueryType: "Query"}
	q := &Type{Kind: KindObject, Name: "Query"}
	e := &Type{Kind: KindEnum, Name: "E"}
	in := &Type{Kind: KindInputObject, Name: "In"}
	for i, d := range descs {
		name := "f" + string(rune('a'+i))
		q.Fields = append(q.Fields, &Field{
			Name: name, Description: d, Type: Named(KindScalar, "String"),
			Args: []*InputValue{{Name: "a", Description: d, Type: Named(KindScalar, "Int")}},
		})
		e.EnumValues = append(e.EnumValues, &EnumValue{Name: strings.ToUpper(name), Description: d})
		in.InputFields = append(in.InputFields, &InputValue{Name: name, Description: d, Type: Named(KindScalar, "String")})
	}
	q.Description = descs[2]
	s.Types = []*Type{q, e, in}
	s.Directives = []*Directive{{Name: "tag", Description: descs[3], Locations: []DirectiveLocation{LocFieldDefinition},
		Args: []*InputValue{{Name: "name", Description: descs[1], Type: NonNullOf(Named(KindScalar, "String"))}}}}
	s.index()

	src := SDL(s)
	back, err := ParseSDL(src)
	if err != nil {
		t.Fatalf("ParseSDL: %v\n%s", err, src)
	}
	for i, d := range descs {
		name := "f" + string(rune('a'+i))
		f := back.Type("Query").Field(name)
		if f == nil {
			t.Fatalf("field %s missing:\n%s", name, src)
		}
		if f.Description != d {
			t.Errorf("field description %q came back as %q", d, f.Description)
		}
		if a := f.Arg("a"); a.Description != d {
			t.Errorf("argument description %q came back as %q", d, a.Description)
		}
		if v := back.Type("E").EnumValue(strings.ToUpper(name)); v.Description != d {
			t.Errorf("enum value description %q came back as %q", d, v.Description)
		}
		if f := back.Type("In").InputField(name); f.Description != d {
			t.Errorf("input field description %q came back as %q", d, f.Description)
		}
	}
	if got := back.Type("Query").Description; got != descs[2] {
		t.Errorf("type description = %q", got)
	}
	if d := back.Directive("tag"); d.Description != descs[3] || d.Arg("name").Description != descs[1] {
		t.Errorf("directive descriptions = %q, %q", d.Description, d.Arg("name").Description)
	}
	// Block strings where they are exact, quoted strings where not.
	for _, want := range []string{`"""Plain."""`, "\"\"\"\n  Two\n  lines.\n  \"\"\"", `"ends with a quote \"x\""`, `"carriage\r\nreturn"`} {
		if !strings.Contains(src, want) {
			t.Errorf("SDL lacks %s:\n%s", want, src)
		}
	}
}

// TestSDLExportRoots writes a schema definition only when the default
// roots would differ.
func TestSDLExportRoots(t *testing.T) {
	obj := func(name string) *Type {
		return &Type{Kind: KindObject, Name: name, Fields: []*Field{{Name: "x", Type: Named(KindScalar, "Int")}}}
	}
	tests := []struct {
		name       string
		s          *Schema
		definition bool
	}{
		{"default names", &Schema{QueryType: "Query", MutationType: "Mutation", Types: []*Type{obj("Query"), obj("Mutation")}}, false},
		{"other query root", &Schema{QueryType: "Root", Types: []*Type{obj("Root")}}, true},
		{"a Mutation type that is no root", &Schema{QueryType: "Query", Types: []*Type{obj("Query"), obj("Mutation")}}, true},
		{"a description", &Schema{Description: "The API.", QueryType: "Query", Types: []*Type{obj("Query")}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.s.index()
			src := SDL(tt.s)
			if got := strings.Contains(src, "schema {"); got != tt.definition {
				t.Errorf("schema definition = %v, want %v:\n%s", got, tt.definition, src)
			}
			back, err := ParseSDL(src)
			if err != nil {
				t.Fatalf("ParseSDL: %v\n%s", err, src)
			}
			if back.QueryType != tt.s.QueryType || back.MutationType != tt.s.MutationType ||
				back.SubscriptionType != tt.s.SubscriptionType || back.Description != tt.s.Description {
				t.Errorf("roots = %q %q %q %q, want %q %q %q %q", back.QueryType, back.MutationType, back.SubscriptionType, back.Description,
					tt.s.QueryType, tt.s.MutationType, tt.s.SubscriptionType, tt.s.Description)
			}
		})
	}
}

// TestSDLExportLayout pins the printed layout on a small schema.
func TestSDLExportLayout(t *testing.T) {
	src := `"""The API."""
schema { query: Root }

directive @tag(name: String!) repeatable on FIELD_DEFINITION | OBJECT

"""
A user.
"""
type User implements Node @tag(name: "x") {
  id: ID!
  "Old." login: String @deprecated
  name(format: Format = SHORT, "Max length." max: Int = 10): String @deprecated(reason: "Use names.")
}

interface Node { id: ID! }
type Root { user(id: ID!): User nodes: [Node!]! }
enum Format { SHORT LONG @deprecated(reason: "No.") }
input Filter @oneOf { id: ID ids: [ID!] }
union Result = User | Root
scalar Date @specifiedBy(url: "https://example.com/date")
`
	s, err := ParseSDL(src)
	if err != nil {
		t.Fatal(err)
	}
	want := `"""The API."""
schema {
  query: Root
}

directive @tag(name: String!) repeatable on FIELD_DEFINITION | OBJECT

"""A user."""
type User implements Node {
  id: ID!
  """Old."""
  login: String @deprecated
  name(
    format: Format = SHORT
    """Max length."""
    max: Int = 10
  ): String @deprecated(reason: "Use names.")
}

interface Node {
  id: ID!
}

type Root {
  user(id: ID!): User
  nodes: [Node!]!
}

enum Format {
  SHORT
  LONG @deprecated(reason: "No.")
}

input Filter @oneOf {
  id: ID
  ids: [ID!]
}

union Result = User | Root

scalar Date @specifiedBy(url: "https://example.com/date")
`
	if got := SDL(s); got != want {
		t.Errorf("SDL =\n%s\nwant\n%s", got, want)
	}
	if SDL(nil) != "" || SDL(&Schema{}) != "" {
		t.Error("an empty schema does not print as nothing")
	}
}
