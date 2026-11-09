package schema

import (
	"strings"
	"testing"
)

func TestIntrospectionQueryConstants(t *testing.T) {
	if got := IntrospectionQuery(FullIntrospectionOptions()); got != FullIntrospectionQuery {
		t.Errorf("FullIntrospectionQuery is out of date with IntrospectionQuery:\n%s", got)
	}
	if got := IntrospectionQuery(ConservativeIntrospectionOptions()); got != ConservativeIntrospectionQuery {
		t.Errorf("ConservativeIntrospectionQuery is out of date with IntrospectionQuery:\n%s", got)
	}
}

// Newer fields that older servers reject must stay out of the
// conservative query.
var newerFields = []string{
	"    description\n    queryType", // __Schema.description
	"specifiedByURL",
	"isRepeatable",
	"isOneOf",
	"args(includeDeprecated: true)",
	"inputFields(includeDeprecated: true)",
	"defaultValue\n  isDeprecated",
}

func TestIntrospectionQueryVariants(t *testing.T) {
	for _, f := range newerFields {
		if !strings.Contains(FullIntrospectionQuery, f) {
			t.Errorf("full query lacks %q", f)
		}
		if strings.Contains(ConservativeIntrospectionQuery, f) {
			t.Errorf("conservative query asks for %q", f)
		}
	}
	for _, q := range []string{FullIntrospectionQuery, ConservativeIntrospectionQuery} {
		if !strings.HasPrefix(q, "query IntrospectionQuery {") {
			t.Error("query does not start with the operation")
		}
		if strings.Count(q, "{") != strings.Count(q, "}") || strings.Count(q, "(") != strings.Count(q, ")") {
			t.Error("unbalanced braces or parentheses")
		}
		for _, frag := range []string{"FullType", "InputValue", "TypeRef"} {
			if !strings.Contains(q, "fragment "+frag+" on") || !strings.Contains(q, "..."+frag) {
				t.Errorf("fragment %s not defined or not used", frag)
			}
		}
	}
	if n := strings.Count(FullIntrospectionQuery, "ofType {"); n != DefaultTypeRefDepth {
		t.Errorf("full query has %d ofType levels, want %d", n, DefaultTypeRefDepth)
	}
	if n := strings.Count(ConservativeIntrospectionQuery, "ofType {"); n != ConservativeTypeRefDepth {
		t.Errorf("conservative query has %d ofType levels, want %d", n, ConservativeTypeRefDepth)
	}
}

func TestIntrospectionQueryOptions(t *testing.T) {
	q := IntrospectionQuery(IntrospectionOptions{})
	if strings.Contains(q, "description") {
		t.Error("descriptions asked for with Descriptions false")
	}
	if n := strings.Count(q, "ofType {"); n != DefaultTypeRefDepth {
		t.Errorf("zero TypeRefDepth gave %d levels, want the default %d", n, DefaultTypeRefDepth)
	}
	q = IntrospectionQuery(IntrospectionOptions{DirectiveIsRepeatable: true, TypeRefDepth: 3})
	if !strings.Contains(q, "isRepeatable") || strings.Contains(q, "isOneOf") || strings.Count(q, "ofType {") != 3 {
		t.Errorf("options not applied:\n%s", q)
	}
}
