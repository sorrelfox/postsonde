package request

import (
	"reflect"
	"testing"
)

func TestCheckGraphQLVariables(t *testing.T) {
	t.Parallel()

	const two = "query A($id: ID!, $first: Int = 10) { a }\nquery B($q: [ String! ]!, $n: Int) { b }"
	names := func(vs []GraphQLVariable) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Name)
		}
		return out
	}
	unknown := func(c GraphQLVariablesCheck) []string {
		var out []string
		for _, k := range c.Unknown {
			out = append(out, k.Name)
		}
		return out
	}

	tests := []struct {
		name      string
		query     string
		variables string
		opName    string
		resolved  bool
		operation string
		missing   []string
		unknown   []string
		err       bool
		notObject bool
	}{
		{name: "single operation, all set", query: "query Q($id: ID!) { a }", variables: `{"id": 1}`, resolved: true, operation: "Q"},
		{name: "single operation, blank variables", query: "query Q($id: ID!, $n: Int) { a }", variables: "", resolved: true, operation: "Q", missing: []string{"id", "n"}},
		{name: "null counts as empty", query: "query Q($id: ID!) { a }", variables: "null", resolved: true, operation: "Q", missing: []string{"id"}},
		{name: "unknown member", query: "{ a }", variables: `{"x": 1, "y": {"id": 2}}`, resolved: true, unknown: []string{"x", "y"}},
		{name: "anonymous with variables", query: "query ($id: ID) { a }", variables: `{"id": null}`, resolved: true},
		{name: "saved name picks the operation", query: two, variables: `{"id": 1, "q": []}`, opName: "B", resolved: true, operation: "B", missing: []string{"n"}, unknown: []string{"id"}},
		{name: "other operation", query: two, variables: `{"id": 1}`, opName: "A", resolved: true, operation: "A", missing: []string{"first"}},
		{name: "several operations, no pick", query: two, variables: `{"zzz": 1}`, resolved: false},
		{name: "stale pick", query: two, variables: `{}`, opName: "C", resolved: false},
		{name: "invalid JSON", query: "query Q($id: ID!) { a }", variables: `{"id": }`, resolved: true, operation: "Q", err: true},
		{name: "not an object", query: "query Q($id: ID!) { a }", variables: `[1]`, resolved: true, operation: "Q", notObject: true},
		{name: "variable member name reports only required", query: "query Q($id: ID!, $n: Int) { a }", variables: `{"{{name}}": 1}`, resolved: true, operation: "Q", missing: []string{"id"}},
		{name: "half-typed definition", query: "query Q($id: ID!, $", variables: `{"id": 1}`, resolved: true, operation: "Q"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := CheckGraphQLVariables(tt.query, tt.variables, tt.opName)
			if c.Resolved != tt.resolved || c.Operation != tt.operation {
				t.Errorf("operation = %q resolved %v, want %q %v", c.Operation, c.Resolved, tt.operation, tt.resolved)
			}
			if !reflect.DeepEqual(names(c.Missing), tt.missing) {
				t.Errorf("missing = %v, want %v", names(c.Missing), tt.missing)
			}
			if !reflect.DeepEqual(unknown(c), tt.unknown) {
				t.Errorf("unknown = %v, want %v", unknown(c), tt.unknown)
			}
			if (c.Error != nil) != tt.err || c.NotObject != tt.notObject {
				t.Errorf("error = %v, notObject = %v", c.Error, c.NotObject)
			}
		})
	}
}

func TestCheckGraphQLVariablesDeclared(t *testing.T) {
	t.Parallel()
	c := CheckGraphQLVariables("query Q($ids: [ ID! ]!, $n: Int! = 3, $s: String) { a }", "{}", "")
	want := []GraphQLVariable{
		{Name: "ids", Type: "[ID!]!", Required: true},
		{Name: "n", Type: "Int!"},
		{Name: "s", Type: "String"},
	}
	if !reflect.DeepEqual(c.Declared, want) {
		t.Errorf("declared = %+v\nwant %+v", c.Declared, want)
	}
}
