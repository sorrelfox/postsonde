// Package ast defines the syntax tree of GraphQL executable documents and
// of type system documents (SDL, typesystem.go) as produced by package
// parser, and helpers to walk it and locate a source offset in it.
//
// The tree is built for editors: it is produced for incomplete documents too
// (see package parser), so consumers must expect placeholder nodes.
//
// Positions: every node embeds Loc, a half-open byte range [Start, End) into
// Document.Source; Document.Position converts an offset to line and column.
// Delimiter fields (LBrace, RParen, Colon, ...) hold the byte offset of that
// token, or -1 when the token is absent.
//
// Placeholders for missing pieces:
//   - A missing name is a *Name with Value "" whose Loc spans the gap between
//     the previous token and the next one (it may be empty).
//   - A missing value is an *InvalidValue with Missing set, spanning the gap
//     the same way; a malformed value token becomes an *InvalidValue covering it.
//   - A missing type is a *NamedType with a missing Name.
//   - A selection set with no selections, such as "{ }", holds one *Field
//     with a missing Name spanning the inside of the braces. (Only a
//     selection set skipped for exceeding the parser's depth limit is empty.)
//   - A list whose closing delimiter is missing (RBrace, RParen or RBracket is
//     -1) ends where the next token starts, so at end of input it reaches
//     len(Source) and contains a cursor placed there.
//
// A required child that is entirely absent because its opening token never
// came (the selection set of "query Q", the type condition of "fragment F")
// is nil; so are optional children that are absent.
package ast

import (
	"sort"
	"strconv"
	"unicode/utf8"
)

// Loc is a byte range [Start, End) into the document source.
type Loc struct {
	Start, End int
}

// Pos returns the node's byte range.
func (l Loc) Pos() Loc { return l }

// Contains reports whether off lies in the range, counting End itself: a
// cursor right after a node's last character is still in it.
func (l Loc) Contains(off int) bool { return l.Start <= off && off <= l.End }

// Node is any syntax tree node.
type Node interface {
	Pos() Loc
}

// Definition is an *OperationDefinition, *FragmentDefinition or
// *TypeSystemDefinition (from parser.Parse), or a *SchemaDefinition,
// *TypeDefinition or *DirectiveDefinition (from parser.ParseSchema).
type Definition interface {
	Node
	definitionNode()
}

// Selection is a *Field, *FragmentSpread or *InlineFragment.
type Selection interface {
	Node
	selectionNode()
}

// Value is a *Variable, *IntValue, *FloatValue, *StringValue,
// *BooleanValue, *NullValue, *EnumValue, *ListValue, *ObjectValue or
// *InvalidValue.
type Value interface {
	Node
	valueNode()
}

// Type is a *NamedType, *ListType or *NonNullType.
type Type interface {
	Node
	typeNode()
}

// Document is a parsed GraphQL document.
type Document struct {
	Loc         // always [0, len(Source))
	Source      string
	Definitions []Definition
	// Comments are the document's comments in source order. They are not
	// part of the tree: Children and Inspect do not visit them. A printer
	// uses them to keep comments when reformatting.
	Comments []*Comment

	lines []int // byte offsets of line starts
}

// NewDocument returns an empty document over src with its line index built.
func NewDocument(src string) *Document {
	return &Document{Loc: Loc{0, len(src)}, Source: src, lines: lineStarts(src)}
}

func lineStarts(src string) []int {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			lines = append(lines, i+1)
		case '\n':
			lines = append(lines, i+1)
		}
	}
	return lines
}

// Position is a 1-based line and column. Column counts Unicode code points,
// with an invalid UTF-8 byte counting as one, like lexer.Token.
type Position struct {
	Line, Column int
}

func (p Position) String() string { return strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column) }

// Position returns the line and column of byte offset off, clamped to
// [0, len(Source)]. An offset between "\r" and "\n" counts as the end of
// the "\r" line. It is O(log lines) for documents from NewDocument.
func (d *Document) Position(off int) Position {
	off = max(0, min(off, len(d.Source)))
	lines := d.lines
	if lines == nil { // built as a literal; do not mutate a shared document
		lines = lineStarts(d.Source)
	}
	line := sort.Search(len(lines), func(i int) bool { return lines[i] > off }) - 1
	return Position{Line: line + 1, Column: utf8.RuneCountInString(d.Source[lines[line]:off]) + 1}
}

// Comment is a "#" comment. Text is its source text from the "#" to the end
// of the line, without the line terminator.
type Comment struct {
	Loc
	Text string
}

// Name is an identifier. Value is "" for a missing name.
type Name struct {
	Loc
	Value string
}

// Missing reports whether the name is a placeholder for an absent name.
func (n *Name) Missing() bool { return n.Value == "" }

// OperationType is query, mutation or subscription.
type OperationType uint8

// Operation types.
const (
	Query OperationType = iota
	Mutation
	Subscription
)

func (o OperationType) String() string {
	switch o {
	case Query:
		return "query"
	case Mutation:
		return "mutation"
	case Subscription:
		return "subscription"
	}
	return "OperationType(" + strconv.Itoa(int(o)) + ")"
}

// OperationDefinition is a query, mutation or subscription, or the
// query shorthand "{ ... }" (Keyword == -1, Operation == Query).
type OperationDefinition struct {
	Loc
	Operation           OperationType
	Keyword             int   // offset of the operation keyword, -1 for the shorthand
	Name                *Name // nil when anonymous
	VariableDefinitions *VariableDefinitionList
	Directives          []*Directive
	SelectionSet        *SelectionSet // nil only when missing
}

// FragmentDefinition is "fragment Name on Type @directives { ... }".
type FragmentDefinition struct {
	Loc
	Keyword       int   // offset of "fragment"
	Name          *Name // missing name when absent
	TypeCondition *TypeCondition
	Directives    []*Directive
	SelectionSet  *SelectionSet
}

// TypeSystemDefinition is a skipped schema definition or extension (SDL),
// which is not allowed in an executable document; the parser reports it.
type TypeSystemDefinition struct {
	Loc
	Keyword string // "type", "schema", "extend", ...; "" if the keyword is missing
}

// VariableDefinitionList is "( $a: T ... )".
type VariableDefinitionList struct {
	Loc
	LParen, RParen int
	Definitions    []*VariableDefinition
}

// VariableDefinition is "$name: Type = default @directives".
type VariableDefinition struct {
	Loc
	Variable     *Variable
	Colon        int
	Type         Type // a missing *NamedType when absent
	Equals       int
	DefaultValue Value // nil when there is no "="
	Directives   []*Directive
}

// SelectionSet is "{ selections }".
type SelectionSet struct {
	Loc
	LBrace, RBrace int
	Selections     []Selection
}

// Field is "alias: name(arguments) @directives { selections }".
type Field struct {
	Loc
	Alias        *Name // nil without an alias
	Colon        int   // offset of the alias colon, -1 without an alias
	Name         *Name
	Arguments    *ArgumentList // nil without "("
	Directives   []*Directive
	SelectionSet *SelectionSet // nil without "{"
}

// ArgumentList is "( name: value ... )".
type ArgumentList struct {
	Loc
	LParen, RParen int
	Arguments      []*Argument
}

// Argument is "name: value".
type Argument struct {
	Loc
	Name  *Name
	Colon int
	Value Value // never nil; *InvalidValue when missing or malformed
}

// FragmentSpread is "...Name @directives".
type FragmentSpread struct {
	Loc
	Spread     int   // offset of "..."
	Name       *Name // missing name for a bare "..."
	Directives []*Directive
}

// InlineFragment is "... on Type @directives { selections }"; the type
// condition is optional.
type InlineFragment struct {
	Loc
	Spread        int
	TypeCondition *TypeCondition // nil without "on"
	Directives    []*Directive
	SelectionSet  *SelectionSet // nil only when missing
}

// TypeCondition is "on Type".
type TypeCondition struct {
	Loc
	On   int // offset of "on", -1 when it is missing before a type name
	Type *NamedType
}

// Directive is "@name(arguments)".
type Directive struct {
	Loc
	At        int
	Name      *Name
	Arguments *ArgumentList // nil without "("
}

// Variable is "$name".
type Variable struct {
	Loc
	Dollar int // offset of "$", -1 when it is missing in a variable definition
	Name   *Name
}

// IntValue is an integer literal; Raw is its source text.
type IntValue struct {
	Loc
	Raw string
}

// FloatValue is a float literal; Raw is its source text.
type FloatValue struct {
	Loc
	Raw string
}

// StringValue is a string or block string literal.
type StringValue struct {
	Loc
	Block bool
	Value string // decoded value
}

// BooleanValue is true or false.
type BooleanValue struct {
	Loc
	Value bool
}

// NullValue is null.
type NullValue struct {
	Loc
}

// EnumValue is a name used as a value, other than true, false and null.
type EnumValue struct {
	Loc
	Value string
}

// ListValue is "[ values ]".
type ListValue struct {
	Loc
	LBracket, RBracket int
	Values             []Value
}

// ObjectValue is "{ name: value ... }".
type ObjectValue struct {
	Loc
	LBrace, RBrace int
	Fields         []*ObjectField
}

// ObjectField is "name: value" in an object value.
type ObjectField struct {
	Loc
	Name  *Name
	Colon int
	Value Value // never nil
}

// InvalidValue stands for a value that is missing (Missing is true) or a
// malformed value token such as an unterminated string.
type InvalidValue struct {
	Loc
	Missing bool
}

// NamedType is a type name.
type NamedType struct {
	Loc
	Name *Name
}

// ListType is "[Type]".
type ListType struct {
	Loc
	LBracket, RBracket int
	Type               Type
}

// NonNullType is "Type!".
type NonNullType struct {
	Loc
	Type Type
	Bang int
}

func (*OperationDefinition) definitionNode()  {}
func (*FragmentDefinition) definitionNode()   {}
func (*TypeSystemDefinition) definitionNode() {}

func (*Field) selectionNode()          {}
func (*FragmentSpread) selectionNode() {}
func (*InlineFragment) selectionNode() {}

func (*Variable) valueNode()     {}
func (*IntValue) valueNode()     {}
func (*FloatValue) valueNode()   {}
func (*StringValue) valueNode()  {}
func (*BooleanValue) valueNode() {}
func (*NullValue) valueNode()    {}
func (*EnumValue) valueNode()    {}
func (*ListValue) valueNode()    {}
func (*ObjectValue) valueNode()  {}
func (*InvalidValue) valueNode() {}

func (*NamedType) typeNode()   {}
func (*ListType) typeNode()    {}
func (*NonNullType) typeNode() {}
