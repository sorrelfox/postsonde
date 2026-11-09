package schema

// Kind is the __TypeKind of a type or type reference.
type Kind string

// The type kinds. LIST and NON_NULL only appear in type references.
const (
	KindScalar      Kind = "SCALAR"
	KindObject      Kind = "OBJECT"
	KindInterface   Kind = "INTERFACE"
	KindUnion       Kind = "UNION"
	KindEnum        Kind = "ENUM"
	KindInputObject Kind = "INPUT_OBJECT"
	KindList        Kind = "LIST"
	KindNonNull     Kind = "NON_NULL"
)

// IsNamed reports whether k is the kind of a named type (not a wrapper).
func (k Kind) IsNamed() bool {
	switch k {
	case KindScalar, KindObject, KindInterface, KindUnion, KindEnum, KindInputObject:
		return true
	}
	return false
}

// IsWrapper reports whether k is LIST or NON_NULL.
func (k Kind) IsWrapper() bool { return k == KindList || k == KindNonNull }

// IsInput reports whether a named type of kind k may be used as an argument
// or input field type.
func (k Kind) IsInput() bool {
	return k == KindScalar || k == KindEnum || k == KindInputObject
}

// IsOutput reports whether a named type of kind k may be used as a field
// type.
func (k Kind) IsOutput() bool {
	switch k {
	case KindScalar, KindObject, KindInterface, KindUnion, KindEnum:
		return true
	}
	return false
}

// DirectiveLocation is a __DirectiveLocation value. Unknown locations from
// newer servers are kept as they are.
type DirectiveLocation string

// The directive locations of the September 2025 spec.
const (
	LocQuery                DirectiveLocation = "QUERY"
	LocMutation             DirectiveLocation = "MUTATION"
	LocSubscription         DirectiveLocation = "SUBSCRIPTION"
	LocField                DirectiveLocation = "FIELD"
	LocFragmentDefinition   DirectiveLocation = "FRAGMENT_DEFINITION"
	LocFragmentSpread       DirectiveLocation = "FRAGMENT_SPREAD"
	LocInlineFragment       DirectiveLocation = "INLINE_FRAGMENT"
	LocVariableDefinition   DirectiveLocation = "VARIABLE_DEFINITION"
	LocSchema               DirectiveLocation = "SCHEMA"
	LocScalar               DirectiveLocation = "SCALAR"
	LocObject               DirectiveLocation = "OBJECT"
	LocFieldDefinition      DirectiveLocation = "FIELD_DEFINITION"
	LocArgumentDefinition   DirectiveLocation = "ARGUMENT_DEFINITION"
	LocInterface            DirectiveLocation = "INTERFACE"
	LocUnion                DirectiveLocation = "UNION"
	LocEnum                 DirectiveLocation = "ENUM"
	LocEnumValue            DirectiveLocation = "ENUM_VALUE"
	LocInputObject          DirectiveLocation = "INPUT_OBJECT"
	LocInputFieldDefinition DirectiveLocation = "INPUT_FIELD_DEFINITION"
)

// Operation is an operation type.
type Operation string

// The operation types.
const (
	OpQuery        Operation = "query"
	OpMutation     Operation = "mutation"
	OpSubscription Operation = "subscription"
)

// Deprecation is the deprecation state of a field, argument, input field or
// enum value.
type Deprecation struct {
	IsDeprecated bool
	// DeprecationReason is the reason given by the server, if any. It is
	// kept even when IsDeprecated is false.
	DeprecationReason string
}

// Type is a named type. Which of the lists apply depends on Kind: Fields
// and Interfaces for objects and interfaces, PossibleTypes for unions and
// interfaces, EnumValues for enums, InputFields for input objects. Other
// types are referenced by name; resolve them with Schema.Type.
type Type struct {
	Kind        Kind
	Name        string
	Description string
	// SpecifiedByURL is the @specifiedBy URL of a custom scalar.
	SpecifiedByURL string
	// IsOneOf marks a @oneOf input object.
	IsOneOf bool

	Fields []*Field
	// Interfaces lists the interfaces this object or interface declares it
	// implements, in declaration order.
	Interfaces []string
	// PossibleTypes lists the object types of a union, or the object types
	// the server reported for an interface.
	PossibleTypes []string
	EnumValues    []*EnumValue
	InputFields   []*InputValue
}

// Field is a field of an object or interface.
type Field struct {
	Name        string
	Description string
	Args        []*InputValue
	Type        *TypeRef
	Deprecation
}

// InputValue is an argument or an input object field.
type InputValue struct {
	Name        string
	Description string
	Type        *TypeRef
	// DefaultValue is the default as a GraphQL literal (`10`, `"x"`,
	// `{a: 1}`), or nil when there is none. A default of `null` is the
	// literal "null", not nil.
	DefaultValue *string
	Deprecation
}

// EnumValue is a value of an enum.
type EnumValue struct {
	Name        string
	Description string
	Deprecation
}

// Directive is a directive definition.
type Directive struct {
	Name         string
	Description  string
	Locations    []DirectiveLocation
	Args         []*InputValue
	IsRepeatable bool
}

// Field returns the declared field name of t, or nil. Meta fields such as
// __typename are resolved by Schema.Field.
func (t *Type) Field(name string) *Field {
	if t == nil {
		return nil
	}
	for _, f := range t.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// InputField returns the input field name of t, or nil.
func (t *Type) InputField(name string) *InputValue {
	if t == nil {
		return nil
	}
	return findInput(t.InputFields, name)
}

// EnumValue returns the enum value name of t, or nil.
func (t *Type) EnumValue(name string) *EnumValue {
	if t == nil {
		return nil
	}
	for _, v := range t.EnumValues {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// Implements reports whether t declares that it implements iface.
func (t *Type) Implements(iface string) bool {
	if t == nil {
		return false
	}
	for _, n := range t.Interfaces {
		if n == iface {
			return true
		}
	}
	return false
}

// IsComposite reports whether t is an object, interface or union: a type
// with a selection set.
func (t *Type) IsComposite() bool {
	return t != nil && (t.Kind == KindObject || t.Kind == KindInterface || t.Kind == KindUnion)
}

// IsAbstract reports whether t is an interface or union.
func (t *Type) IsAbstract() bool {
	return t != nil && (t.Kind == KindInterface || t.Kind == KindUnion)
}

// IsLeaf reports whether t is a scalar or enum.
func (t *Type) IsLeaf() bool {
	return t != nil && (t.Kind == KindScalar || t.Kind == KindEnum)
}

// IsBuiltin reports whether t is a built-in scalar or introspection type.
func (t *Type) IsBuiltin() bool {
	return t != nil && builtinTypeNames[t.Name]
}

// Arg returns the argument name of f, or nil.
func (f *Field) Arg(name string) *InputValue {
	if f == nil {
		return nil
	}
	return findInput(f.Args, name)
}

// Arg returns the argument name of d, or nil.
func (d *Directive) Arg(name string) *InputValue {
	if d == nil {
		return nil
	}
	return findInput(d.Args, name)
}

// HasLocation reports whether d may be used at loc.
func (d *Directive) HasLocation(loc DirectiveLocation) bool {
	if d == nil {
		return false
	}
	for _, l := range d.Locations {
		if l == loc {
			return true
		}
	}
	return false
}

// IsBuiltin reports whether d is one of the spec directives.
func (d *Directive) IsBuiltin() bool {
	return d != nil && builtinDirectiveNames[d.Name]
}

func findInput(vs []*InputValue, name string) *InputValue {
	for _, v := range vs {
		if v.Name == name {
			return v
		}
	}
	return nil
}
