package schema

import (
	"strings"
)

// IntrospectionOptions selects the optional parts of an introspection
// query. Each newer field is rejected by servers whose GraphQL library
// predates it, with an error such as `Cannot query field "isRepeatable" on
// type "__Directive"`.
type IntrospectionOptions struct {
	// Descriptions asks for descriptions of types, fields, arguments, enum
	// values and directives.
	Descriptions bool
	// SchemaDescription asks for __Schema.description (spec 2021).
	SchemaDescription bool
	// SpecifiedByURL asks for __Type.specifiedByURL (spec 2021).
	SpecifiedByURL bool
	// DirectiveIsRepeatable asks for __Directive.isRepeatable (spec 2021).
	DirectiveIsRepeatable bool
	// InputValueDeprecation asks for deprecated arguments and input fields
	// (includeDeprecated on args and inputFields, isDeprecated and
	// deprecationReason on __InputValue; spec 2025).
	InputValueDeprecation bool
	// OneOf asks for __Type.isOneOf (spec 2025).
	OneOf bool
	// TypeRefDepth is the number of nested ofType levels asked for each
	// type reference. Zero means DefaultTypeRefDepth. A reference wrapped
	// deeper than this arrives truncated and is reported as a problem.
	TypeRefDepth int
}

// DefaultTypeRefDepth is the ofType depth of the full query: enough for
// references such as [[[String!]!]!]!.
const DefaultTypeRefDepth = 9

// ConservativeTypeRefDepth is the ofType depth of the conservative query,
// the depth most GraphQL clients have used for years.
const ConservativeTypeRefDepth = 7

// FullIntrospectionOptions returns the options of FullIntrospectionQuery.
func FullIntrospectionOptions() IntrospectionOptions {
	return IntrospectionOptions{
		Descriptions:          true,
		SchemaDescription:     true,
		SpecifiedByURL:        true,
		DirectiveIsRepeatable: true,
		InputValueDeprecation: true,
		OneOf:                 true,
		TypeRefDepth:          DefaultTypeRefDepth,
	}
}

// ConservativeIntrospectionOptions returns the options of
// ConservativeIntrospectionQuery.
func ConservativeIntrospectionOptions() IntrospectionOptions {
	return IntrospectionOptions{Descriptions: true, TypeRefDepth: ConservativeTypeRefDepth}
}

// IntrospectionOperationName is the operation name of the generated queries.
const IntrospectionOperationName = "IntrospectionQuery"

// IntrospectionQuery returns an introspection query with the options o.
// The result converts with ParseIntrospection whatever the options.
func IntrospectionQuery(o IntrospectionOptions) string {
	depth := o.TypeRefDepth
	if depth <= 0 {
		depth = DefaultTypeRefDepth
	}
	var b strings.Builder
	w := func(indent int, s string) {
		b.WriteString(strings.Repeat("  ", indent))
		b.WriteString(s)
		b.WriteByte('\n')
	}
	desc := func(indent int, on bool) {
		if on {
			w(indent, "description")
		}
	}
	inputArgs := ""
	if o.InputValueDeprecation {
		inputArgs = "(includeDeprecated: true)"
	}

	w(0, "query "+IntrospectionOperationName+" {")
	w(1, "__schema {")
	desc(2, o.SchemaDescription)
	w(2, "queryType { name kind }")
	w(2, "mutationType { name kind }")
	w(2, "subscriptionType { name kind }")
	w(2, "types {")
	w(3, "...FullType")
	w(2, "}")
	w(2, "directives {")
	w(3, "name")
	desc(3, o.Descriptions)
	if o.DirectiveIsRepeatable {
		w(3, "isRepeatable")
	}
	w(3, "locations")
	w(3, "args"+inputArgs+" {")
	w(4, "...InputValue")
	w(3, "}")
	w(2, "}")
	w(1, "}")
	w(0, "}")
	b.WriteByte('\n')

	w(0, "fragment FullType on __Type {")
	w(1, "kind")
	w(1, "name")
	desc(1, o.Descriptions)
	if o.SpecifiedByURL {
		w(1, "specifiedByURL")
	}
	if o.OneOf {
		w(1, "isOneOf")
	}
	w(1, "fields(includeDeprecated: true) {")
	w(2, "name")
	desc(2, o.Descriptions)
	w(2, "args"+inputArgs+" {")
	w(3, "...InputValue")
	w(2, "}")
	w(2, "type {")
	w(3, "...TypeRef")
	w(2, "}")
	w(2, "isDeprecated")
	w(2, "deprecationReason")
	w(1, "}")
	w(1, "inputFields"+inputArgs+" {")
	w(2, "...InputValue")
	w(1, "}")
	w(1, "interfaces {")
	w(2, "...TypeRef")
	w(1, "}")
	w(1, "enumValues(includeDeprecated: true) {")
	w(2, "name")
	desc(2, o.Descriptions)
	w(2, "isDeprecated")
	w(2, "deprecationReason")
	w(1, "}")
	w(1, "possibleTypes {")
	w(2, "...TypeRef")
	w(1, "}")
	w(0, "}")
	b.WriteByte('\n')

	w(0, "fragment InputValue on __InputValue {")
	w(1, "name")
	desc(1, o.Descriptions)
	w(1, "type {")
	w(2, "...TypeRef")
	w(1, "}")
	w(1, "defaultValue")
	if o.InputValueDeprecation {
		w(1, "isDeprecated")
		w(1, "deprecationReason")
	}
	w(0, "}")
	b.WriteByte('\n')

	w(0, "fragment TypeRef on __Type {")
	w(1, "kind")
	w(1, "name")
	for i := range depth {
		w(1+i, "ofType {")
		w(2+i, "kind")
		w(2+i, "name")
	}
	for i := depth - 1; i >= 0; i-- {
		w(1+i, "}")
	}
	w(0, "}")
	return b.String()
}
