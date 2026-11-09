package ast

import "strconv"

// Type system definitions (SDL), as produced by parser.ParseSchema. The
// executable parser (parser.Parse) never produces them: it reports type
// system definitions and keeps only an opaque *TypeSystemDefinition.
//
// Offsets follow the executable nodes: a keyword or delimiter field holds
// the token's byte offset, -1 when it is absent. Extend is the offset of
// "extend" on an extension, -1 on a definition.

// TypeKind is the kind of a type definition, named by its keyword.
type TypeKind uint8

// Type definition kinds.
const (
	KindScalar      TypeKind = iota // scalar
	KindObject                      // type
	KindInterface                   // interface
	KindUnion                       // union
	KindEnum                        // enum
	KindInputObject                 // input
)

var typeKindKeywords = [...]string{
	KindScalar:      "scalar",
	KindObject:      "type",
	KindInterface:   "interface",
	KindUnion:       "union",
	KindEnum:        "enum",
	KindInputObject: "input",
}

// String returns the kind's keyword: "scalar", "type", "interface",
// "union", "enum" or "input".
func (k TypeKind) String() string {
	if int(k) < len(typeKindKeywords) {
		return typeKindKeywords[k]
	}
	return "TypeKind(" + strconv.Itoa(int(k)) + ")"
}

// TypeKindOf returns the kind a type definition keyword introduces.
func TypeKindOf(keyword string) (TypeKind, bool) {
	for k, kw := range typeKindKeywords {
		if kw == keyword {
			return TypeKind(k), true
		}
	}
	return 0, false
}

// SchemaDefinition is "schema @directives { query: Query ... }", or its
// extension "extend schema ...".
type SchemaDefinition struct {
	Loc
	Description    *StringValue // nil without one; never on an extension
	Extend         int
	Keyword        int // offset of "schema"
	Directives     []*Directive
	LBrace, RBrace int // -1 without the block (an extension may omit it)
	OperationTypes []*RootOperationTypeDefinition
}

// RootOperationTypeDefinition is "query: Query" in a schema definition.
type RootOperationTypeDefinition struct {
	Loc
	Operation OperationType
	Keyword   int // offset of the operation keyword
	Colon     int
	Type      *NamedType
}

// TypeDefinition is a scalar, object ("type"), interface, union, enum or
// input object definition, or its extension. Which members apply depends
// on Kind:
//   - object, interface: Implements and Interfaces, Fields;
//   - union: Equals and Members;
//   - enum: EnumValues;
//   - input: InputFields.
//
// LBrace and RBrace enclose the fields, enum values or input fields; they
// are -1 when the block is absent ("type Query" or an extension adding
// only directives).
type TypeDefinition struct {
	Loc
	Description *StringValue // nil without one; never on an extension
	Extend      int
	Kind        TypeKind
	Keyword     int
	Name        *Name
	Implements  int // offset of "implements", -1 without it
	Interfaces  []*NamedType
	Directives  []*Directive
	Equals      int // offset of "=" of a union, -1 without members
	Members     []*NamedType

	LBrace, RBrace int
	Fields         []*FieldDefinition
	EnumValues     []*EnumValueDefinition
	InputFields    []*InputValueDefinition
}

// IsExtension reports whether d is "extend ...".
func (d *TypeDefinition) IsExtension() bool { return d.Extend >= 0 }

// IsExtension reports whether d is "extend schema ...".
func (d *SchemaDefinition) IsExtension() bool { return d.Extend >= 0 }

// FieldDefinition is "name(arguments): Type @directives" in an object or
// interface.
type FieldDefinition struct {
	Loc
	Description *StringValue
	Name        *Name
	Arguments   *ArgumentDefinitionList // nil without "("
	Colon       int
	Type        Type // a missing *NamedType when absent
	Directives  []*Directive
}

// ArgumentDefinitionList is "( name: Type = default ... )" after a field or
// directive name.
type ArgumentDefinitionList struct {
	Loc
	LParen, RParen int
	Arguments      []*InputValueDefinition
}

// InputValueDefinition is an argument or input field definition:
// "name: Type = default @directives".
type InputValueDefinition struct {
	Loc
	Description  *StringValue
	Name         *Name
	Colon        int
	Type         Type
	Equals       int
	DefaultValue Value // nil without "="
	Directives   []*Directive
}

// EnumValueDefinition is "NAME @directives" in an enum.
type EnumValueDefinition struct {
	Loc
	Description *StringValue
	Name        *Name
	Directives  []*Directive
}

// DirectiveDefinition is
// "directive @name(arguments) repeatable on LOCATION | LOCATION".
type DirectiveDefinition struct {
	Loc
	Description *StringValue
	Keyword     int // offset of "directive"
	At          int
	Name        *Name
	Arguments   *ArgumentDefinitionList // nil without "("
	Repeatable  int                     // offset of "repeatable", -1 without it
	On          int                     // offset of "on", -1 when missing
	Locations   []*Name
}

func (*SchemaDefinition) definitionNode()    {}
func (*TypeDefinition) definitionNode()      {}
func (*DirectiveDefinition) definitionNode() {}
