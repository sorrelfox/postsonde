package schema

// Schema is a GraphQL schema. Build it with ParseIntrospection or
// FromIntrospection; a Schema is read-only afterwards.
type Schema struct {
	Description string
	// QueryType, MutationType and SubscriptionType name the root operation
	// types; MutationType and SubscriptionType are empty when the schema has
	// none.
	QueryType        string
	MutationType     string
	SubscriptionType string
	// Types lists the named types in the server's order, including the
	// built-in and introspection types the server reported.
	Types []*Type
	// Directives lists the directives in the server's order.
	Directives []*Directive

	types           map[string]*Type
	directives      map[string]*Directive
	implementations map[string][]*Type
}

// Meta fields available in every selection set (__typename) or on the query
// root only (__schema, __type). They are shared values; do not modify them.
var (
	TypenameField = &Field{
		Name:        "__typename",
		Description: "The name of the current Object type at runtime.",
		Type:        NonNullOf(Named(KindScalar, "String")),
	}
	SchemaField = &Field{
		Name:        "__schema",
		Description: "Access the current type schema of this server.",
		Type:        NonNullOf(Named(KindObject, "__Schema")),
	}
	TypeField = &Field{
		Name:        "__type",
		Description: "Request the type information of a single type.",
		Args: []*InputValue{{
			Name: "name",
			Type: NonNullOf(Named(KindScalar, "String")),
		}},
		Type: Named(KindObject, "__Type"),
	}
)

// index builds the lookup maps. Duplicate names keep the first declaration.
func (s *Schema) index() {
	s.types = make(map[string]*Type, len(s.Types))
	for _, t := range s.Types {
		if _, dup := s.types[t.Name]; !dup {
			s.types[t.Name] = t
		}
	}
	s.directives = make(map[string]*Directive, len(s.Directives))
	for _, d := range s.Directives {
		if _, dup := s.directives[d.Name]; !dup {
			s.directives[d.Name] = d
		}
	}
	s.implementations = make(map[string][]*Type)
	for _, t := range s.Types {
		if t.Kind != KindObject && t.Kind != KindInterface {
			continue
		}
		for _, iface := range t.Interfaces {
			s.implementations[iface] = append(s.implementations[iface], t)
		}
	}
}

// Type returns the named type name: a declared type, else a built-in scalar
// or introspection type, else nil.
func (s *Schema) Type(name string) *Type {
	if t, ok := s.types[name]; ok {
		return t
	}
	if s != builtins {
		return builtins.types[name]
	}
	return nil
}

// HasType reports whether the schema declares name (built-in fallbacks do
// not count).
func (s *Schema) HasType(name string) bool {
	_, ok := s.types[name]
	return ok
}

// TypeOf returns the named type at the core of ref, or nil.
func (s *Schema) TypeOf(ref *TypeRef) *Type {
	n := ref.NamedType()
	if n == nil {
		return nil
	}
	return s.Type(n.Name)
}

// Directive returns the directive name: a declared directive, else a spec
// directive (@skip, @include, @deprecated, @specifiedBy, @oneOf), else nil.
func (s *Schema) Directive(name string) *Directive {
	if d, ok := s.directives[name]; ok {
		return d
	}
	if s != builtins {
		return builtins.directives[name]
	}
	return nil
}

// RootType returns the root type of op, or nil when the schema has none.
func (s *Schema) RootType(op Operation) *Type {
	var name string
	switch op {
	case OpQuery:
		name = s.QueryType
	case OpMutation:
		name = s.MutationType
	case OpSubscription:
		name = s.SubscriptionType
	}
	if name == "" {
		return nil
	}
	return s.types[name]
}

// Query returns the query root type.
func (s *Schema) Query() *Type { return s.RootType(OpQuery) }

// Mutation returns the mutation root type, or nil.
func (s *Schema) Mutation() *Type { return s.RootType(OpMutation) }

// Subscription returns the subscription root type, or nil.
func (s *Schema) Subscription() *Type { return s.RootType(OpSubscription) }

// Field returns field fieldName of the composite type typeName, including
// the meta fields: __typename on objects, interfaces and unions, and
// __schema and __type on the query root. It returns nil when there is no
// such field.
func (s *Schema) Field(typeName, fieldName string) *Field {
	t := s.Type(typeName)
	if !t.IsComposite() {
		return nil
	}
	switch fieldName {
	case TypenameField.Name:
		return TypenameField
	case SchemaField.Name, TypeField.Name:
		if typeName == s.QueryType {
			if fieldName == SchemaField.Name {
				return SchemaField
			}
			return TypeField
		}
		return nil
	}
	return t.Field(fieldName)
}

// FieldsOf returns the fields selectable on typeName, for completion: the
// declared fields in order, then the meta fields that apply. It returns nil
// for a type without a selection set.
func (s *Schema) FieldsOf(typeName string) []*Field {
	t := s.Type(typeName)
	if !t.IsComposite() {
		return nil
	}
	out := make([]*Field, 0, len(t.Fields)+3)
	out = append(out, t.Fields...)
	out = append(out, TypenameField)
	if typeName == s.QueryType {
		out = append(out, SchemaField, TypeField)
	}
	return out
}

// PossibleTypes returns the object types an abstract type may be at run
// time: the members of a union, or the object types of an interface (as
// reported by the server, else derived from the declared implementations).
// It returns nil for other types. Unknown names are skipped.
func (s *Schema) PossibleTypes(name string) []*Type {
	t := s.Type(name)
	if t == nil {
		return nil
	}
	switch t.Kind {
	case KindUnion:
		return s.resolve(t.PossibleTypes)
	case KindInterface:
		if len(t.PossibleTypes) > 0 {
			return s.resolve(t.PossibleTypes)
		}
		var out []*Type
		for _, impl := range s.implementations[name] {
			if impl.Kind == KindObject {
				out = append(out, impl)
			}
		}
		return out
	}
	return nil
}

// Implementations returns the objects and interfaces that declare they
// implement the interface name, in declaration order.
func (s *Schema) Implementations(name string) []*Type {
	return s.implementations[name]
}

// IsPossibleType reports whether the object type object is a possible type
// of the abstract type abstract.
func (s *Schema) IsPossibleType(abstract, object string) bool {
	for _, t := range s.PossibleTypes(abstract) {
		if t.Name == object {
			return true
		}
	}
	return false
}

// IsSubType reports whether a value of type sub is always a value of type
// super: the same type, a possible type of a union or interface, or an
// interface implementing the interface super.
func (s *Schema) IsSubType(super, sub string) bool {
	if super == sub {
		return true
	}
	t := s.Type(super)
	if !t.IsAbstract() {
		return false
	}
	if t.Kind == KindInterface && s.Type(sub).Implements(super) {
		return true
	}
	return s.IsPossibleType(super, sub)
}

func (s *Schema) resolve(names []string) []*Type {
	out := make([]*Type, 0, len(names))
	for _, n := range names {
		if t := s.Type(n); t != nil {
			out = append(out, t)
		}
	}
	return out
}
