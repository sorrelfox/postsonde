package schema

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

const minimalSchema = `{
	"queryType": {"name": "Query"},
	"types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "hello", "args": [], "type": {"kind": "SCALAR", "name": "String"}, "isDeprecated": false}
		], "interfaces": []}
	],
	"directives": []
}`

func TestParseIntrospectionEnvelopes(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"graphql response", `{"data": {"__schema": ` + minimalSchema + `}}`},
		{"response with extensions", `{"data": {"__schema": ` + minimalSchema + `}, "extensions": {"x": 1}}`},
		{"response with warnings", `{"data": {"__schema": ` + minimalSchema + `}, "errors": [{"message": "deprecated"}]}`},
		{"bare __schema", `{"__schema": ` + minimalSchema + `}`},
		{"__schema value", minimalSchema},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseIntrospection([]byte(tt.json))
			if err != nil {
				t.Fatalf("ParseIntrospection: %v", err)
			}
			// String is not declared: it falls back to the built-in scalar.
			wantField(t, s, "Query", "hello", "String")
			if s.HasType("String") || s.Type("String") == nil {
				t.Error("String should resolve through the built-ins only")
			}
			if s.Mutation() != nil || s.Subscription() != nil {
				t.Error("unexpected mutation or subscription root")
			}
		})
	}
}

func TestParseIntrospectionErrors(t *testing.T) {
	tests := []struct {
		name string
		json string
		kind errs.Kind
		code string
	}{
		{"not json", `<html>`, errs.Parse, CodeMalformedJSON},
		{"json array", `[1]`, errs.Parse, CodeMalformedJSON},
		{"no schema", `{"hello": 1}`, errs.Parse, CodeNoSchema},
		{"null data", `{"data": null}`, errs.Parse, CodeNoSchema},
		{"data without schema", `{"data": {"other": 1}}`, errs.Parse, CodeNoSchema},
		{"wrong shape", `{"__schema": {"types": 5}}`, errs.Parse, CodeMalformedIntrospection},
		{"wrong errors shape", `{"errors": "boom"}`, errs.Parse, CodeMalformedIntrospection},
		{"server errors", `{"errors": [{"message": "introspection is disabled"}]}`, errs.Protocol, CodeServerErrors},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseIntrospection([]byte(tt.json))
			if s != nil {
				t.Error("schema returned with a fatal error")
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v is not an *errs.Error", err)
			}
			if e.Kind != tt.kind || e.Code != tt.code {
				t.Errorf("kind, code = %s, %s; want %s, %s", e.Kind, e.Code, tt.kind, tt.code)
			}
		})
	}
}

// swapi-full-rejected.json is a real older server's answer to the full
// query: errors only.
func TestParseIntrospectionRejectedFullQuery(t *testing.T) {
	_, err := ParseIntrospection(readFixture(t, "swapi-full-rejected.json"))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeServerErrors {
		t.Fatalf("err = %v, want %s", err, CodeServerErrors)
	}
	if e.Hint != HintServerErrors {
		t.Errorf("hint = %q", e.Hint)
	}
	msgs := strings.Split(e.Details["errors"], "\n")
	if len(msgs) != 9 || !strings.Contains(e.Message, `"description" on type "__Schema"`) {
		t.Errorf("message %q, %d error details", e.Message, len(msgs))
	}
}

func TestParseIntrospectionAcceptsOlderSpellings(t *testing.T) {
	s, err := ParseIntrospection([]byte(`{"__schema": {
		"queryType": {"name": "Query"},
		"types": [
			{"kind": "OBJECT", "name": "Query", "fields": [
				{"name": "at", "args": [], "type": {"kind": "SCALAR", "name": "Date"}, "isDeprecated": false}
			], "interfaces": null},
			{"kind": "SCALAR", "name": "Date", "specifiedByUrl": "https://example.com/date"}
		],
		"directives": [{"name": "tag", "locations": ["OBJECT", "SOMEWHERE_NEW"], "args": []}]
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Type("Date").SpecifiedByURL; got != "https://example.com/date" {
		t.Errorf("specifiedByUrl not read: %q", got)
	}
	if d := s.Directive("tag"); d.IsRepeatable || !d.HasLocation("SOMEWHERE_NEW") {
		t.Errorf("@tag = %+v", d)
	}
	if s.Type("Query").Interfaces == nil {
		t.Error("interfaces: null should convert to an empty list")
	}
}

func TestParseIntrospectionReportsProblems(t *testing.T) {
	data := `{"data": {"__schema": {
		"queryType": {"name": "Missing"},
		"mutationType": {"name": "Point"},
		"types": [
			{"kind": "OBJECT", "name": "Query", "interfaces": [{"kind": "INTERFACE", "name": "Ghost"}, {"kind": "OBJECT", "name": "User"}], "fields": [
				{"name": "user", "args": [
					{"name": "id", "type": {"kind": "NON_NULL", "name": null, "ofType": {"kind": "SCALAR", "name": "UUID"}}},
					{"name": "filter", "type": {"kind": "OBJECT", "name": "User"}}
				], "type": {"kind": "OBJECT", "name": "User"}, "isDeprecated": false},
				{"name": "user", "args": [], "type": {"kind": "OBJECT", "name": "User"}, "isDeprecated": false},
				{"name": "deep", "args": [], "type": {"kind": "LIST", "name": null, "ofType": {"kind": "NON_NULL", "name": null, "ofType": null}}, "isDeprecated": false},
				{"name": "point", "args": [], "type": {"kind": "INPUT_OBJECT", "name": "Point"}, "isDeprecated": false},
				{"name": "kindless", "args": [], "type": {"kind": "OBJECT", "name": "Color"}, "isDeprecated": false},
				{"name": "notype", "args": [], "type": null, "isDeprecated": false}
			]},
			{"kind": "OBJECT", "name": "User", "fields": [], "interfaces": []},
			{"kind": "INPUT_OBJECT", "name": "Point", "inputFields": [
				{"name": "x", "type": {"kind": "OBJECT", "name": "User"}, "defaultValue": null}
			]},
			{"kind": "ENUM", "name": "Color", "enumValues": [{"name": "RED", "isDeprecated": false}],
			 "fields": [{"name": "oops", "args": [], "type": {"kind": "SCALAR", "name": "String"}, "isDeprecated": false}]},
			{"kind": "UNION", "name": "Result", "possibleTypes": [{"kind": "INPUT_OBJECT", "name": "Point"}, {"kind": "OBJECT", "name": "Nope"}]},
			{"kind": "OBJECT", "name": "User", "fields": [], "interfaces": []},
			{"kind": "WIDGET", "name": "Odd"}
		],
		"directives": [
			{"name": "auth", "locations": ["FIELD_DEFINITION"], "args": [{"name": "role", "type": {"kind": "ENUM", "name": "Role"}, "defaultValue": null}]}
		]
	}}}`
	s, err := ParseIntrospection([]byte(data))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidSchema || e.Kind != errs.Parse {
		t.Fatalf("err = %v, want %s", err, CodeInvalidSchema)
	}
	var problems Problems
	if !errors.As(err, &problems) {
		t.Fatalf("error does not wrap Problems: %v", err)
	}
	got := make([]string, len(problems))
	for i, p := range problems {
		got[i] = p.String()
	}
	want := []string{
		`schema.queryType: references undeclared type "Missing"`,
		`schema.mutationType: root type "Point" is INPUT_OBJECT, not OBJECT`,
		`Query.user(id:): references undeclared type "UUID"`,
		`Query.user(filter:): "User" is OBJECT, which is not an input type`,
		`Query.deep: the type reference is truncated at a NON_NULL wrapper; raise the introspection query's type depth`,
		`Query.point: "Point" is INPUT_OBJECT, which is not an output type`,
		`Query.kindless: reference to "Color" says OBJECT, but the type is ENUM`,
		`Query.notype: the type is missing`,
		`Query: field "user" is declared more than once`,
		`Query implements: references undeclared type "Ghost"`,
		`Query implements: "User" is OBJECT, not INTERFACE`,
		`Point.x: "User" is OBJECT, which is not an input type`,
		`Color: ENUM types cannot have fields`,
		`Result possibleTypes: "Point" is INPUT_OBJECT, not OBJECT`,
		`Result possibleTypes: references undeclared type "Nope"`,
		`User: the type is declared more than once`,
		`Odd: unknown type kind "WIDGET"`,
		`@auth(role:): references undeclared type "Role"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if e.Details["problems"] != "18" || e.Hint != HintInvalidSchema {
		t.Errorf("details %v, hint %q", e.Details, e.Hint)
	}
	if !strings.Contains(problems.Error(), "18 schema problems: ") || !strings.Contains(problems.Error(), "and 15 more") {
		t.Errorf("Problems.Error() = %q", problems.Error())
	}

	// The schema is still usable; broken references resolve to nil.
	if s == nil {
		t.Fatal("no schema returned with CodeInvalidSchema")
	}
	if s.Query() != nil {
		t.Error("Query() should be nil for an undeclared query type")
	}
	if f := s.Type("Query").Field("user"); f == nil || s.TypeOf(f.Arg("id").Type) != nil {
		t.Error("dangling argument type should resolve to nil")
	}
	if got := s.Type("Query").Field("deep").Type.String(); got != "[?!]" {
		t.Errorf("truncated type prints %q, want [?!]", got)
	}
	if n := len(s.PossibleTypes("Result")); n != 1 {
		t.Errorf("PossibleTypes(Result) = %d types, want the 1 that resolves", n)
	}
}

func TestProblemsErrorSingular(t *testing.T) {
	ps := Problems{{Path: "A", Message: "broken"}}
	if got := ps.Error(); got != "1 schema problem: A: broken" {
		t.Errorf("Error() = %q", got)
	}
}

func TestFromIntrospectionNil(t *testing.T) {
	var e *errs.Error
	if _, err := FromIntrospection(nil); !errors.As(err, &e) || e.Code != CodeNoSchema {
		t.Errorf("err = %v", err)
	}
}

func TestIntrospectionShapeFollowsKinds(t *testing.T) {
	s, err := ParseIntrospection(readFixture(t, "github-subset.json"))
	if err != nil {
		t.Fatal(err)
	}
	is := s.Introspection()
	for _, it := range is.Types {
		k := Kind(it.Kind)
		check := func(field string, present, applies bool) {
			if present != applies {
				t.Errorf("%s (%s): %s present=%v, want %v", *it.Name, k, field, present, applies)
			}
		}
		check("fields", it.Fields != nil, k == KindObject || k == KindInterface)
		check("interfaces", it.Interfaces != nil, k == KindObject || k == KindInterface)
		check("possibleTypes", it.PossibleTypes != nil, k == KindUnion || k == KindInterface)
		check("enumValues", it.EnumValues != nil, k == KindEnum)
		check("inputFields", it.InputFields != nil, k == KindInputObject)
		check("isOneOf", it.IsOneOf != nil, k == KindInputObject)
	}
	if is.QueryType.Kind != "OBJECT" || is.Description == nil {
		t.Errorf("schema header = %+v", is)
	}
}
