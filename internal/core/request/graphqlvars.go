package request

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/jsonfmt"
)

// The GraphQL variables check (GQL-03, GQL-04): the variables editor
// compares the JSON object with the variables the operation that runs
// declares. See docs/graphql.md.

// GraphQLVariable is a variable an operation declares.
type GraphQLVariable struct {
	// Name is without the "$".
	Name string
	// Type is the declared type as written, such as "[ID!]!".
	Type string
	// Required is true for a non-null type without a default value: the
	// server rejects the operation when it is missing.
	Required bool
}

// GraphQLVariablesCheck is the result of CheckGraphQLVariables.
type GraphQLVariablesCheck struct {
	// Operation is the name of the operation the variables are for (empty
	// for an anonymous one). Resolved is false when the document has no
	// operation, or several and none is named operationName: nothing is
	// compared then.
	Operation string
	Resolved  bool
	// Declared are the operation's variables in source order.
	Declared []GraphQLVariable
	// Missing are the declared variables the JSON object does not set.
	Missing []GraphQLVariable
	// Unknown are the JSON object's members the operation does not
	// declare, in source order. Members named by a {{variable}} are left
	// out.
	Unknown []jsonfmt.Key
	// Error is the JSON syntax error of the variables text, nil when it is
	// valid. Nothing is compared then.
	Error *jsonfmt.SyntaxError
	// NotObject is true when the text is valid JSON but neither an object,
	// null nor blank: BuildGraphQL refuses it.
	NotObject bool
}

// CheckGraphQLVariables checks the variables text of a GraphQL request
// against the operation that runs: the only operation of query, or the one
// named operationName when there are several (GraphQLOperationName). Blank
// text and null count as an empty object. {{variables}} are allowed in the
// text as for jsonfmt.
func CheckGraphQLVariables(query, variables, operationName string) GraphQLVariablesCheck {
	var out GraphQLVariablesCheck
	keys, kind, serr := jsonfmt.TopLevelKeys(variables)
	if serr != nil {
		out.Error = serr
	}
	if serr == nil && kind == jsonfmt.TopOther {
		out.NotObject = true
	}

	doc, _ := parser.Parse(query)
	op := runningOperation(doc, operationName)
	if op == nil {
		return out
	}
	out.Resolved = true
	if op.Name != nil {
		out.Operation = op.Name.Value
	}
	if op.VariableDefinitions != nil {
		for _, d := range op.VariableDefinitions.Definitions {
			if d.Variable == nil || d.Variable.Name == nil || d.Variable.Name.Value == "" {
				continue
			}
			v := GraphQLVariable{Name: d.Variable.Name.Value}
			if d.Type != nil {
				v.Type = compactType(query, d.Type)
				_, nonNull := d.Type.(*ast.NonNullType)
				v.Required = nonNull && d.DefaultValue == nil
			}
			out.Declared = append(out.Declared, v)
		}
	}
	if out.Error != nil || out.NotObject {
		return out
	}

	set := make(map[string]bool, len(keys))
	variable := false
	for _, k := range keys {
		set[k.Name] = true
		variable = variable || k.Variable
	}
	declared := make(map[string]bool, len(out.Declared))
	for _, v := range out.Declared {
		declared[v.Name] = true
		// A {{variable}} member name may render to any variable: only
		// required ones are reported then.
		if !set[v.Name] && (!variable || v.Required) {
			out.Missing = append(out.Missing, v)
		}
	}
	for _, k := range keys {
		if !k.Variable && !declared[k.Name] {
			out.Unknown = append(out.Unknown, k)
		}
	}
	return out
}

// runningOperation is the operation a request with operationName runs:
// the only one, or the one of that name.
func runningOperation(doc *ast.Document, operationName string) *ast.OperationDefinition {
	var ops []*ast.OperationDefinition
	for _, d := range doc.Definitions {
		if op, ok := d.(*ast.OperationDefinition); ok {
			ops = append(ops, op)
		}
	}
	if len(ops) == 1 {
		return ops[0]
	}
	if operationName == "" {
		return nil
	}
	for _, op := range ops {
		if op.Name != nil && op.Name.Value == operationName {
			return op
		}
	}
	return nil
}

// compactType is the source text of a type reference without whitespace
// or commas: "[ ID! ]" is "[ID!]".
func compactType(src string, t ast.Type) string {
	loc := t.Pos()
	if loc.Start < 0 || loc.End > len(src) || loc.Start > loc.End {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '\uFEFF':
			return -1
		}
		return r
	}, src[loc.Start:loc.End])
}
