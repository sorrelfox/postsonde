package schema

import (
	"slices"
	"strings"
	"testing"
)

func TestTypeRefString(t *testing.T) {
	user := Named(KindObject, "User")
	tests := []struct {
		ref  *TypeRef
		want string
	}{
		{user, "User"},
		{NonNullOf(user), "User!"},
		{ListOf(user), "[User]"},
		{NonNullOf(ListOf(NonNullOf(user))), "[User!]!"},
		{ListOf(NonNullOf(ListOf(NonNullOf(Named(KindScalar, "Int"))))), "[[Int!]!]"},
		{ListOf(nil), "[?]"},
		{nil, "?"},
	}
	for _, tt := range tests {
		if got := tt.ref.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestTypeRefHelpers(t *testing.T) {
	user := Named(KindObject, "User")
	ref := NonNullOf(ListOf(NonNullOf(user)))
	if !ref.IsNonNull() || !ref.IsList() || ref.NamedType() != user || ref.NamedTypeName() != "User" {
		t.Error("NonNull/List/NamedType wrong for [User!]!")
	}
	if got := ref.Nullable().String(); got != "[User!]" {
		t.Errorf("Nullable() = %s", got)
	}
	if got := ref.Elem().String(); got != "User!" {
		t.Errorf("Elem() = %s", got)
	}
	if user.Elem() != nil || user.IsList() || user.IsNonNull() {
		t.Error("named reference reported as wrapped")
	}
	if (*TypeRef)(nil).NamedTypeName() != "" || ListOf(nil).NamedType() != nil {
		t.Error("malformed references should have no named type")
	}
	if !ref.Equal(NonNullOf(ListOf(NonNullOf(Named(KindObject, "User"))))) {
		t.Error("Equal false for equal references")
	}
	if ref.Equal(NonNullOf(ListOf(user))) || ref.Equal(nil) || user.Equal(Named(KindInterface, "User")) {
		t.Error("Equal true for different references")
	}
}

func TestKindPredicates(t *testing.T) {
	for _, k := range []Kind{KindScalar, KindEnum, KindInputObject} {
		if !k.IsInput() {
			t.Errorf("%s should be an input kind", k)
		}
	}
	for _, k := range []Kind{KindObject, KindInterface, KindUnion} {
		if k.IsInput() || !k.IsOutput() || !k.IsNamed() {
			t.Errorf("%s predicates wrong", k)
		}
	}
	if KindInputObject.IsOutput() || KindList.IsNamed() || !KindNonNull.IsWrapper() || Kind("X").IsNamed() {
		t.Error("kind predicates wrong")
	}
}

func githubSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseIntrospection(readFixture(t, "github-subset.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMetaFields(t *testing.T) {
	s := githubSchema(t)
	tests := []struct {
		typ, field string
		want       *Field
	}{
		{"Query", "__typename", TypenameField},
		{"Query", "__schema", SchemaField},
		{"Query", "__type", TypeField},
		{"User", "__typename", TypenameField},
		{"Node", "__typename", TypenameField},
		{"SearchResultItem", "__typename", TypenameField},
		{"User", "__schema", nil},
		{"Mutation", "__type", nil},
		{"DateTime", "__typename", nil},
		{"IssueOrder", "__typename", nil},
		{"Nope", "__typename", nil},
		{"SearchResultItem", "id", nil},
	}
	for _, tt := range tests {
		if got := s.Field(tt.typ, tt.field); got != tt.want {
			t.Errorf("Field(%s, %s) = %v, want %v", tt.typ, tt.field, got, tt.want)
		}
	}
	if got := TypeField.Arg("name").Type.String(); got != "String!" {
		t.Errorf("__type(name:) = %s", got)
	}

	// The meta types resolve, so completion can walk into __schema { … }.
	wantField(t, s, "__Schema", "types", "[__Type!]!")
	wantField(t, s, "__Type", "fields", "[__Field!]")
	if s.Field("__Type", "fields").Arg("includeDeprecated") == nil {
		t.Error("__Type.fields(includeDeprecated:) missing")
	}

	names := func(fs []*Field) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	if got := names(s.FieldsOf("Mutation")); !slices.Equal(got, []string{"addStar", "createIssue", "__typename"}) {
		t.Errorf("FieldsOf(Mutation) = %v", got)
	}
	if got := names(s.FieldsOf("Query")); !slices.Equal(got[len(got)-3:], []string{"__typename", "__schema", "__type"}) {
		t.Errorf("FieldsOf(Query) = %v", got)
	}
	if got := names(s.FieldsOf("SearchResultItem")); !slices.Equal(got, []string{"__typename"}) {
		t.Errorf("FieldsOf(SearchResultItem) = %v", got)
	}
	if s.FieldsOf("URI") != nil {
		t.Error("FieldsOf(scalar) should be nil")
	}
}

func TestLookups(t *testing.T) {
	s := githubSchema(t)
	if s.Query().Name != "Query" || s.Mutation().Name != "Mutation" || s.Subscription().Name != "Subscription" {
		t.Error("root types wrong")
	}
	if s.RootType("bogus") != nil {
		t.Error("RootType(bogus) should be nil")
	}
	if s.Type("Nope") != nil || s.Directive("nope") != nil || s.TypeOf(nil) != nil {
		t.Error("unknown names should resolve to nil")
	}
	if d := s.Directive("skip"); d == nil || !d.IsBuiltin() || d.Arg("if").Type.String() != "Boolean!" {
		t.Errorf("@skip = %+v", d)
	}
	if !s.Type("String").IsBuiltin() || s.Type("URI").IsBuiltin() {
		t.Error("IsBuiltin wrong")
	}
	if s.Type("Float") == nil || s.HasType("Float") {
		t.Error("Float should resolve through the built-ins only")
	}
	u := s.Type("User")
	if !u.IsComposite() || u.IsAbstract() || u.IsLeaf() || !s.Type("IssueState").IsLeaf() || !s.Type("Actor").IsAbstract() {
		t.Error("type predicates wrong")
	}
	if !u.Implements("Actor") || u.Implements("Starrable") {
		t.Error("Implements wrong")
	}
	if s.PossibleTypes("User") != nil || s.PossibleTypes("Nope") != nil {
		t.Error("PossibleTypes of a non-abstract type should be nil")
	}
	if s.IsSubType("User", "Organization") || !s.IsSubType("User", "User") {
		t.Error("IsSubType on objects wrong")
	}
	var nilType *Type
	var nilField *Field
	var nilDir *Directive
	if nilType.Field("x") != nil || nilType.InputField("x") != nil || nilType.EnumValue("x") != nil ||
		nilField.Arg("x") != nil || nilDir.Arg("x") != nil || nilDir.HasLocation(LocField) || nilType.IsComposite() {
		t.Error("nil receivers should return zero values")
	}
}

func TestBuiltinsAreConsistent(t *testing.T) {
	out, err := Builtins().MarshalIntrospection()
	if err != nil {
		t.Fatal(err)
	}
	// Give the built-ins a query root so they pass as a schema of their own.
	fixed := strings.Replace(string(out), `"queryType":null`, `"queryType":{"name":"__Schema"}`, 1)
	s, err := ParseIntrospection([]byte(fixed))
	if err != nil {
		t.Fatalf("built-ins do not convert cleanly: %v", err)
	}
	if len(s.Types) != len(builtinTypeNames) || len(s.Directives) != len(builtinDirectiveNames) {
		t.Errorf("%d types, %d directives", len(s.Types), len(s.Directives))
	}
	for _, name := range BuiltinScalarNames {
		if typ := Builtins().Type(name); typ == nil || typ.Kind != KindScalar {
			t.Errorf("built-in scalar %s = %+v", name, typ)
		}
	}
	if d := Builtins().Directive("deprecated"); *d.Arg("reason").DefaultValue != `"`+DefaultDeprecationReason+`"` {
		t.Errorf("@deprecated(reason:) default = %s", *d.Arg("reason").DefaultValue)
	}
}
