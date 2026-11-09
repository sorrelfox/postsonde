package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// fixtures lists the introspection results in testdata (see
// testdata/README.md) with a few facts each must convert to.
var fixtures = []struct {
	file         string
	query        string
	mutation     string
	subscription string
	types        int
	directives   int
	check        func(t *testing.T, s *Schema)
}{
	{
		file: "countries-full.json", query: "Query", types: 23, directives: 5,
		check: func(t *testing.T, s *Schema) {
			wantField(t, s, "Query", "countries", "[Country!]!")
			wantArg(t, s, "Query", "countries", "filter", "CountryFilterInput", `{}`)
			wantArg(t, s, "Query", "country", "code", "ID!", "")
			in := s.Type("StringQueryOperatorInput")
			if in == nil || in.Kind != KindInputObject || in.InputField("in").Type.String() != "[String!]" {
				t.Errorf("StringQueryOperatorInput.in = %+v", in)
			}
			if d := s.Directive("oneOf"); d == nil || !d.HasLocation(LocInputObject) || d.IsBuiltin() != true {
				t.Errorf("@oneOf = %+v", d)
			}
		},
	},
	{
		file: "swapi-conservative.json", query: "Root", types: 66, directives: 3,
		check: func(t *testing.T, s *Schema) {
			wantField(t, s, "Root", "allFilms", "FilmsConnection")
			wantArg(t, s, "Root", "allFilms", "first", "Int", "")
			node := s.Type("Node")
			if node == nil || node.Kind != KindInterface {
				t.Fatalf("Node = %+v", node)
			}
			var names []string
			for _, p := range s.PossibleTypes("Node") {
				names = append(names, p.Name)
			}
			if want := []string{"Film", "Species", "Planet", "Person", "Starship", "Vehicle"}; !slices.Equal(names, want) {
				t.Errorf("PossibleTypes(Node) = %v, want %v", names, want)
			}
			if !s.IsSubType("Node", "Film") || s.IsSubType("Node", "Root") {
				t.Error("IsSubType(Node, …) is wrong")
			}
			if len(s.Implementations("Node")) != 6 {
				t.Errorf("Implementations(Node) = %d types, want 6", len(s.Implementations("Node")))
			}
			// An old server: no @specifiedBy or @oneOf declared, both still resolve.
			if s.Directive("specifiedBy") == nil || s.Directive("oneOf") == nil {
				t.Error("spec directives do not fall back to the built-ins")
			}
		},
	},
	{
		file: "rickandmorty-conservative.json", query: "Query", types: 25, directives: 4,
		check: func(t *testing.T, s *Schema) {
			wantField(t, s, "Query", "charactersByIds", "[Character]")
			wantArg(t, s, "Query", "charactersByIds", "ids", "[ID!]!", "")
			if up := s.Type("Upload"); up == nil || up.Kind != KindScalar || up.IsBuiltin() {
				t.Errorf("Upload = %+v", up)
			}
			cc := s.Directive("cacheControl")
			if cc == nil || cc.IsBuiltin() || !cc.HasLocation(LocObject) || cc.Arg("scope").Type.String() != "CacheControlScope" {
				t.Errorf("@cacheControl = %+v", cc)
			}
		},
	},
	{
		file: "github-subset.json", query: "Query", mutation: "Mutation", subscription: "Subscription",
		types: 37, directives: 9,
		check: func(t *testing.T, s *Schema) {
			if s.Description != "A GitHub-style subset for PostSonde tests." {
				t.Errorf("Description = %q", s.Description)
			}
			wantField(t, s, "Query", "nodes", "[Node]!")
			wantField(t, s, "Repository", "topicMatrix", "[[String!]!]")
			wantArg(t, s, "Repository", "issues", "orderBy", "IssueOrder", "{field: CREATED_AT, direction: DESC}")
			wantArg(t, s, "Query", "repository", "followRenames", "Boolean", "true")

			user := s.Type("User")
			if want := []string{"Node", "Actor", "RepositoryOwner", "UniformResourceLocatable"}; !slices.Equal(user.Interfaces, want) {
				t.Errorf("User.Interfaces = %v, want %v", user.Interfaces, want)
			}
			if db := user.Field("databaseId"); !db.IsDeprecated || db.DeprecationReason == "" {
				t.Errorf("User.databaseId deprecation = %+v", db.Deprecation)
			}
			if m := s.Type("Repository").Field("mirrorUrl"); !m.IsDeprecated || m.DeprecationReason != "" {
				t.Errorf("Repository.mirrorUrl deprecation = %+v", m.Deprecation)
			}
			if a := s.Type("Repository").Field("issues").Arg("labels"); !a.IsDeprecated {
				t.Errorf("deprecated argument lost: %+v", a)
			}
			if f := s.Type("CreateIssueInput").InputField("assigneeId"); !f.IsDeprecated {
				t.Errorf("deprecated input field lost: %+v", f)
			}
			if v := s.Type("SearchType").EnumValue("DISCUSSION"); !v.IsDeprecated {
				t.Errorf("deprecated enum value lost: %+v", v)
			}
			if !s.Type("RepositoryLookup").IsOneOf {
				t.Error("RepositoryLookup lost isOneOf")
			}
			if u := s.Type("DateTime").SpecifiedByURL; u != "https://tools.ietf.org/html/rfc3339" {
				t.Errorf("DateTime.SpecifiedByURL = %q", u)
			}
			if d := s.Directive("requiresScope"); d == nil || !d.IsRepeatable {
				t.Errorf("@requiresScope = %+v", d)
			}
			if d := s.Directive("preview"); d == nil || d.IsRepeatable {
				t.Errorf("@preview = %+v", d)
			}

			// Interfaces implementing interfaces.
			var impl []string
			for _, x := range s.Implementations("Node") {
				impl = append(impl, x.Name)
			}
			if want := []string{"RepositoryOwner", "Starrable", "User", "Organization", "Repository", "Issue", "PullRequest"}; !slices.Equal(impl, want) {
				t.Errorf("Implementations(Node) = %v, want %v", impl, want)
			}
			if !s.IsSubType("Node", "RepositoryOwner") || !s.IsSubType("RepositoryOwner", "User") {
				t.Error("IsSubType misses an interface hierarchy")
			}
			// possibleTypes of interfaces were not reported: derived.
			var owners []string
			for _, x := range s.PossibleTypes("RepositoryOwner") {
				owners = append(owners, x.Name)
			}
			if want := []string{"User", "Organization"}; !slices.Equal(owners, want) {
				t.Errorf("PossibleTypes(RepositoryOwner) = %v, want %v", owners, want)
			}
			if !s.IsPossibleType("SearchResultItem", "PullRequest") || s.IsPossibleType("SearchResultItem", "Label") {
				t.Error("IsPossibleType(SearchResultItem, …) is wrong")
			}
			// No introspection types in this fixture: they fall back.
			if s.HasType("__Schema") || s.Type("__Schema") == nil {
				t.Error("__Schema does not fall back to the built-ins")
			}
			wantField(t, s, "Query", "__schema", "__Schema!")
		},
	},
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wantField(t *testing.T, s *Schema, typeName, field, typ string) {
	t.Helper()
	f := s.Field(typeName, field)
	if f == nil {
		t.Errorf("%s.%s not found", typeName, field)
		return
	}
	if got := f.Type.String(); got != typ {
		t.Errorf("%s.%s type = %s, want %s", typeName, field, got, typ)
	}
}

func wantArg(t *testing.T, s *Schema, typeName, field, arg, typ, def string) {
	t.Helper()
	a := s.Field(typeName, field).Arg(arg)
	if a == nil {
		t.Errorf("%s.%s(%s:) not found", typeName, field, arg)
		return
	}
	if got := a.Type.String(); got != typ {
		t.Errorf("%s.%s(%s:) type = %s, want %s", typeName, field, arg, got, typ)
	}
	got := ""
	if a.DefaultValue != nil {
		got = *a.DefaultValue
	}
	if got != def {
		t.Errorf("%s.%s(%s:) default = %q, want %q", typeName, field, arg, got, def)
	}
}

// TestFixturesConvertWithoutLoss is the "done" check of the converter:
// every fixture converts without problems, and converting the model back
// to introspection JSON gives the fixture again, compared semantically.
func TestFixturesConvertWithoutLoss(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			data := readFixture(t, fx.file)
			s, err := ParseIntrospection(data)
			if err != nil {
				t.Fatalf("ParseIntrospection: %v", err)
			}
			if s.QueryType != fx.query || s.MutationType != fx.mutation || s.SubscriptionType != fx.subscription {
				t.Errorf("roots = %q %q %q, want %q %q %q", s.QueryType, s.MutationType, s.SubscriptionType,
					fx.query, fx.mutation, fx.subscription)
			}
			if len(s.Types) != fx.types || len(s.Directives) != fx.directives {
				t.Errorf("%d types and %d directives, want %d and %d", len(s.Types), len(s.Directives), fx.types, fx.directives)
			}

			out, err := s.MarshalIntrospection()
			if err != nil {
				t.Fatal(err)
			}
			assertSameIntrospection(t, schemaValue(t, data), schemaValue(t, out))

			// The cached form converts to the same model again.
			s2, err := ParseIntrospection(out)
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			out2, _ := s2.MarshalIntrospection()
			if string(out) != string(out2) {
				t.Error("MarshalIntrospection is not stable across a round trip")
			}

			fx.check(t, s)
		})
	}
}

// TestFixturesKeepDeclarationOrder checks the model keeps the server's
// order of types, fields, arguments and enum values.
func TestFixturesKeepDeclarationOrder(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			data := readFixture(t, fx.file)
			var resp struct {
				Data struct {
					Schema IntrospectionSchema `json:"__schema"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatal(err)
			}
			s, err := ParseIntrospection(data)
			if err != nil {
				t.Fatal(err)
			}
			for i, it := range resp.Data.Schema.Types {
				mt := s.Types[i]
				if mt.Name != *it.Name {
					t.Fatalf("types[%d] = %s, want %s", i, mt.Name, *it.Name)
				}
				for j, f := range it.Fields {
					if mt.Fields[j].Name != f.Name {
						t.Errorf("%s.fields[%d] = %s, want %s", mt.Name, j, mt.Fields[j].Name, f.Name)
					}
					for k, a := range f.Args {
						if mt.Fields[j].Args[k].Name != a.Name {
							t.Errorf("%s.%s args[%d] = %s, want %s", mt.Name, f.Name, k, mt.Fields[j].Args[k].Name, a.Name)
						}
					}
				}
				for j, v := range it.EnumValues {
					if mt.EnumValues[j].Name != v.Name {
						t.Errorf("%s.enumValues[%d] = %s, want %s", mt.Name, j, mt.EnumValues[j].Name, v.Name)
					}
				}
			}
			for i, d := range resp.Data.Schema.Directives {
				if s.Directives[i].Name != d.Name {
					t.Errorf("directives[%d] = %s, want %s", i, s.Directives[i].Name, d.Name)
				}
			}
		})
	}
}

// TestBuiltinsMatchGraphQLJS compares the built-in scalars, introspection
// types and spec directives with those of a current graphql-js server
// (countries-full.json), ignoring descriptions, which are our own words.
func TestBuiltinsMatchGraphQLJS(t *testing.T) {
	fixture := schemaValue(t, readFixture(t, "countries-full.json"))
	out, err := Builtins().MarshalIntrospection()
	if err != nil {
		t.Fatal(err)
	}
	ours := schemaValue(t, out)

	byName := func(v any) map[string]any {
		m := map[string]any{}
		for _, e := range v.([]any) {
			m[nameOf(e)] = stripDescriptions(e)
		}
		return m
	}
	fxTypes, ourTypes := byName(fixture["types"]), byName(ours["types"])
	for name := range builtinTypeNames {
		if name == "Float" {
			continue // countries does not use Float, so graphql-js leaves it out
		}
		want, ok := fxTypes[name]
		if !ok {
			t.Errorf("fixture has no %s", name)
			continue
		}
		var diffs []string
		diff(want, ourTypes[name], name, &diffs, 10)
		for _, d := range diffs {
			t.Error(d)
		}
	}
	fxDirs, ourDirs := byName(fixture["directives"]), byName(ours["directives"])
	if len(fxDirs) != len(ourDirs) {
		t.Errorf("fixture has %d directives, built-ins %d", len(fxDirs), len(ourDirs))
	}
	for name, want := range fxDirs {
		var diffs []string
		diff(want, ourDirs[name], "@"+name, &diffs, 10)
		for _, d := range diffs {
			t.Error(d)
		}
	}
}

func stripDescriptions(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k != "description" {
				out[k] = stripDescriptions(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripDescriptions(e)
		}
		return out
	}
	return v
}
