package graphql

// The docs explorer's DTOs (GQL-14). See dto.go and docs/bindings.md.

// DocsInputDTO names the cached schema the docs explorer shows.
type DocsInputDTO struct {
	// Key is SchemaInfoDTO.Key.
	Key string `json:"key"`
}

// DocsRootDTO is a root operation type.
type DocsRootDTO struct {
	// Operation is "query", "mutation" or "subscription".
	Operation string `json:"operation"`
	Type      string `json:"type"`
}

// DocsTypeNameDTO is a type in the list of all types.
type DocsTypeNameDTO struct {
	Name string `json:"name"`
	// Kind is OBJECT, INTERFACE, UNION, ENUM, INPUT_OBJECT or SCALAR.
	Kind string `json:"kind"`
}

// DocsInputValueDTO is an argument or an input field.
type DocsInputValueDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Type is the reference as written ("[ID!]!"); TypeName and TypeKind
	// are the named type at its core, the one to open.
	Type     string `json:"type"`
	TypeName string `json:"typeName"`
	TypeKind string `json:"typeKind"`
	// DefaultValue is the default as a GraphQL literal when HasDefault.
	DefaultValue      string `json:"defaultValue"`
	HasDefault        bool   `json:"hasDefault"`
	Deprecated        bool   `json:"deprecated"`
	DeprecationReason string `json:"deprecationReason"`
}

// DocsFieldDTO is a field of an object or interface.
type DocsFieldDTO struct {
	Name              string              `json:"name"`
	Description       string              `json:"description"`
	Args              []DocsInputValueDTO `json:"args"`
	Type              string              `json:"type"`
	TypeName          string              `json:"typeName"`
	TypeKind          string              `json:"typeKind"`
	Deprecated        bool                `json:"deprecated"`
	DeprecationReason string              `json:"deprecationReason"`
}

// DocsEnumValueDTO is a value of an enum.
type DocsEnumValueDTO struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Deprecated        bool   `json:"deprecated"`
	DeprecationReason string `json:"deprecationReason"`
}

// DocsDirectiveDTO is a directive definition.
type DocsDirectiveDTO struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Locations   []string            `json:"locations"`
	Args        []DocsInputValueDTO `json:"args"`
	Repeatable  bool                `json:"repeatable"`
}

// DocsOverviewDTO is the docs explorer's start page.
type DocsOverviewDTO struct {
	// Schema is false when no schema is cached for the key.
	Schema      bool   `json:"schema"`
	Description string `json:"description"`
	// Roots are the root operation types the schema has, query first.
	Roots []DocsRootDTO `json:"roots"`
	// Types are every named type but the introspection types (__Type, …),
	// sorted by name, without their members: DocsType has the details.
	Types      []DocsTypeNameDTO  `json:"types"`
	Directives []DocsDirectiveDTO `json:"directives"`
}

// DocsTypeInputDTO asks for one type.
type DocsTypeInputDTO struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// DocsTypeDTO is one type with its members.
type DocsTypeDTO struct {
	// Found is false when the schema has no such type (or none is cached).
	Found       bool   `json:"found"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// Root is the operation this type is the root of, "" for other types.
	Root           string `json:"root"`
	SpecifiedByURL string `json:"specifiedByUrl"`
	IsOneOf        bool   `json:"isOneOf"`
	// Interfaces are the interfaces an object or interface implements;
	// PossibleTypes the object types of a union or interface;
	// Implementations the objects and interfaces implementing an
	// interface.
	Interfaces      []string `json:"interfaces"`
	PossibleTypes   []string `json:"possibleTypes"`
	Implementations []string `json:"implementations"`
	// Fields, InputFields and EnumValues are in declaration order.
	Fields      []DocsFieldDTO      `json:"fields"`
	InputFields []DocsInputValueDTO `json:"inputFields"`
	EnumValues  []DocsEnumValueDTO  `json:"enumValues"`
}

// DocsSearchInputDTO is a docs search.
type DocsSearchInputDTO struct {
	Key   string `json:"key"`
	Query string `json:"query"`
	// Limit bounds the hits; 0 or more than MaxDocsHits is MaxDocsHits.
	Limit int `json:"limit"`
}

// DocsHitDTO is one search result (docs.Hit).
type DocsHitDTO struct {
	// Kind is type, field, input-field, enum-value or argument.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Type is the type to open; Member the member to show in it ("" for
	// a type; the field of an argument).
	Type   string `json:"type"`
	Member string `json:"member"`
	// Parent is "" for a type, "Type" for a member, "Type.field" for an
	// argument.
	Parent string `json:"parent"`
	// Detail is a type's kind or a member's type ("[User!]!"); "" for an
	// enum value.
	Detail      string `json:"detail"`
	Description string `json:"description"`
	Deprecated  bool   `json:"deprecated"`
}

// DocsSearchDTO is the result of DocsSearch.
type DocsSearchDTO struct {
	// Schema is false when no schema is cached for the key.
	Schema bool `json:"schema"`
	// Items are the best hits, best first; Count is how many matched.
	Items []DocsHitDTO `json:"items"`
	Count int          `json:"count"`
}
