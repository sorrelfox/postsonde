package schema

// builtins holds the built-in scalars, the introspection types and the spec
// directives (September 2025 spec). Schema lookups fall back to it.
var builtins *Schema

// Names of the built-in types and directives.
var (
	builtinTypeNames = map[string]bool{
		"Int": true, "Float": true, "String": true, "Boolean": true, "ID": true,
		"__Schema": true, "__Type": true, "__TypeKind": true, "__Field": true,
		"__InputValue": true, "__EnumValue": true, "__Directive": true,
		"__DirectiveLocation": true,
	}
	builtinDirectiveNames = map[string]bool{
		"include": true, "skip": true, "deprecated": true, "specifiedBy": true, "oneOf": true,
	}
)

// BuiltinScalarNames lists the spec scalars in spec order.
var BuiltinScalarNames = []string{"Int", "Float", "String", "Boolean", "ID"}

// DefaultDeprecationReason is the default of @deprecated(reason:).
const DefaultDeprecationReason = "No longer supported"

func init() { builtins = newBuiltins() }

// Builtins returns a schema holding only the built-in scalars, the
// introspection types and the spec directives. It has no root types. The
// result is shared; do not modify it.
func Builtins() *Schema { return builtins }

func newBuiltins() *Schema {
	str := func() *TypeRef { return Named(KindScalar, "String") }
	boolean := func() *TypeRef { return Named(KindScalar, "Boolean") }
	nn := NonNullOf
	obj := func(name string) *TypeRef { return Named(KindObject, name) }
	enum := func(name string) *TypeRef { return Named(KindEnum, name) }
	f := func(name string, t *TypeRef, args ...*InputValue) *Field {
		return &Field{Name: name, Type: t, Args: args}
	}
	includeDeprecated := func() *InputValue {
		v := "false"
		return &InputValue{Name: "includeDeprecated", Type: boolean(), DefaultValue: &v}
	}
	enumValues := func(names ...string) []*EnumValue {
		out := make([]*EnumValue, len(names))
		for i, n := range names {
			out[i] = &EnumValue{Name: n}
		}
		return out
	}
	scalar := func(name, desc string) *Type {
		return &Type{Kind: KindScalar, Name: name, Description: desc}
	}
	object := func(name, desc string, fields ...*Field) *Type {
		return &Type{Kind: KindObject, Name: name, Description: desc, Fields: fields}
	}
	reason := `"` + DefaultDeprecationReason + `"`

	s := &Schema{
		Types: []*Type{
			scalar("Int", "The `Int` scalar type represents non-fractional signed whole numeric values between -(2^31) and 2^31 - 1."),
			scalar("Float", "The `Float` scalar type represents signed double-precision finite values as specified by IEEE 754."),
			scalar("String", "The `String` scalar type represents textual data as UTF-8 character sequences."),
			scalar("Boolean", "The `Boolean` scalar type represents `true` or `false`."),
			scalar("ID", "The `ID` scalar type represents a unique identifier, serialized as a String."),
			object("__Schema", "A GraphQL schema: all its types and directives and the root operation types.",
				f("description", str()),
				f("types", nn(ListOf(nn(obj("__Type"))))),
				f("queryType", nn(obj("__Type"))),
				f("mutationType", obj("__Type")),
				f("subscriptionType", obj("__Type")),
				f("directives", nn(ListOf(nn(obj("__Directive"))))),
			),
			object("__Type", "A type of the schema, or a LIST or NON_NULL wrapper around one.",
				f("kind", nn(enum("__TypeKind"))),
				f("name", str()),
				f("description", str()),
				f("specifiedByURL", str()),
				f("fields", ListOf(nn(obj("__Field"))), includeDeprecated()),
				f("interfaces", ListOf(nn(obj("__Type")))),
				f("possibleTypes", ListOf(nn(obj("__Type")))),
				f("enumValues", ListOf(nn(obj("__EnumValue"))), includeDeprecated()),
				f("inputFields", ListOf(nn(obj("__InputValue"))), includeDeprecated()),
				f("ofType", obj("__Type")),
				f("isOneOf", boolean()),
			),
			&Type{Kind: KindEnum, Name: "__TypeKind", Description: "The kinds of types.",
				EnumValues: enumValues("SCALAR", "OBJECT", "INTERFACE", "UNION", "ENUM", "INPUT_OBJECT", "LIST", "NON_NULL")},
			object("__Field", "A field of an object or interface type.",
				f("name", nn(str())),
				f("description", str()),
				f("args", nn(ListOf(nn(obj("__InputValue")))), includeDeprecated()),
				f("type", nn(obj("__Type"))),
				f("isDeprecated", nn(boolean())),
				f("deprecationReason", str()),
			),
			object("__InputValue", "An argument or an input object field.",
				f("name", nn(str())),
				f("description", str()),
				f("type", nn(obj("__Type"))),
				f("defaultValue", str()),
				f("isDeprecated", nn(boolean())),
				f("deprecationReason", str()),
			),
			object("__EnumValue", "A value of an enum type.",
				f("name", nn(str())),
				f("description", str()),
				f("isDeprecated", nn(boolean())),
				f("deprecationReason", str()),
			),
			object("__Directive", "A directive definition.",
				f("name", nn(str())),
				f("description", str()),
				f("isRepeatable", nn(boolean())),
				f("locations", nn(ListOf(nn(enum("__DirectiveLocation"))))),
				f("args", nn(ListOf(nn(obj("__InputValue")))), includeDeprecated()),
			),
			&Type{Kind: KindEnum, Name: "__DirectiveLocation", Description: "Where a directive may be used.",
				EnumValues: enumValues("QUERY", "MUTATION", "SUBSCRIPTION", "FIELD", "FRAGMENT_DEFINITION",
					"FRAGMENT_SPREAD", "INLINE_FRAGMENT", "VARIABLE_DEFINITION", "SCHEMA", "SCALAR", "OBJECT",
					"FIELD_DEFINITION", "ARGUMENT_DEFINITION", "INTERFACE", "UNION", "ENUM", "ENUM_VALUE",
					"INPUT_OBJECT", "INPUT_FIELD_DEFINITION")},
		},
		Directives: []*Directive{
			{
				Name:        "include",
				Description: "Include this field or fragment only when the `if` argument is true.",
				Locations:   []DirectiveLocation{LocField, LocFragmentSpread, LocInlineFragment},
				Args:        []*InputValue{{Name: "if", Description: "Included when true.", Type: nn(boolean())}},
			},
			{
				Name:        "skip",
				Description: "Skip this field or fragment when the `if` argument is true.",
				Locations:   []DirectiveLocation{LocField, LocFragmentSpread, LocInlineFragment},
				Args:        []*InputValue{{Name: "if", Description: "Skipped when true.", Type: nn(boolean())}},
			},
			{
				Name:        "deprecated",
				Description: "Marks an element of the schema as no longer supported.",
				Locations:   []DirectiveLocation{LocFieldDefinition, LocArgumentDefinition, LocInputFieldDefinition, LocEnumValue},
				Args:        []*InputValue{{Name: "reason", Description: "Why the element was deprecated.", Type: str(), DefaultValue: &reason}},
			},
			{
				Name:        "specifiedBy",
				Description: "Exposes a URL that specifies the behavior of a custom scalar.",
				Locations:   []DirectiveLocation{LocScalar},
				Args:        []*InputValue{{Name: "url", Description: "The URL of the specification.", Type: nn(str())}},
			},
			{
				Name:        "oneOf",
				Description: "Exactly one field of the input object must be given, and it must not be null.",
				Locations:   []DirectiveLocation{LocInputObject},
			},
		},
	}
	s.index()
	return s
}
