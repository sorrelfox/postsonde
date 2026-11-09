package validate

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// TypeInfo tracks the schema context of a node while walking a document:
// the composite type a selection set selects on, the field, directive and
// argument definitions in effect, and the input type a value is expected to
// have. It is the Go counterpart of graphql-js's TypeInfo.
//
// Call Enter before visiting a node's children and Leave after them, in
// matching pairs; Walk does this. To get the context of a single node, such
// as the one under the cursor, enter the path from ast.NodePathAt with
// EnterPath.
//
// Anything that cannot be resolved (an unknown field or type, a placeholder
// name, a missing root type) is nil, and so is everything below it, so
// consumers can skip those subtrees instead of reporting cascades. Fragment
// spreads are not followed: a fragment definition is typed by its own type
// condition.
type TypeInfo struct {
	schema *schema.Schema

	types       []*schema.TypeRef // output type of the operation, fragment or field
	parentTypes []*schema.Type    // composite type of the enclosing selection set
	fieldDefs   []*schema.Field
	inputTypes  []*schema.TypeRef // expected type of the value being entered
	defaults    []*string         // default of the argument or input field the value is for
	arguments   []*schema.InputValue

	directive   *schema.Directive
	inDirective bool // arguments belong to the directive, even an unknown one
	enumValue   *schema.EnumValue
}

// NewTypeInfo returns a TypeInfo for s with nothing entered. s must not be
// nil.
func NewTypeInfo(s *schema.Schema) *TypeInfo {
	return &TypeInfo{schema: s}
}

// Schema returns the schema.
func (ti *TypeInfo) Schema() *schema.Schema { return ti.schema }

// Type returns the output type of the innermost operation (its root type),
// fragment (its type condition) or field (the field's type), or nil.
func (ti *TypeInfo) Type() *schema.TypeRef { return top(ti.types) }

// ParentType returns the object, interface or union the innermost selection
// set selects on, or nil. Inside a field it is the type the field is
// selected on.
func (ti *TypeInfo) ParentType() *schema.Type { return top(ti.parentTypes) }

// FieldDef returns the definition of the innermost field, or nil.
func (ti *TypeInfo) FieldDef() *schema.Field { return top(ti.fieldDefs) }

// Directive returns the definition of the directive being visited, or nil.
func (ti *TypeInfo) Directive() *schema.Directive { return ti.directive }

// Argument returns the definition of the argument being visited, or nil.
func (ti *TypeInfo) Argument() *schema.InputValue { return top(ti.arguments) }

// InputType returns the type the value being visited must have: the type of
// the argument, variable definition (for its default value), input object
// field or list item, or nil. A single value where a list is expected is
// coerced to a list of one, so the item type of a list type is also the
// input type of a non-list value inside it.
func (ti *TypeInfo) InputType() *schema.TypeRef { return top(ti.inputTypes) }

// ParentInputType returns the input type of the value enclosing the current
// one (the list or input object type), or nil.
func (ti *TypeInfo) ParentInputType() *schema.TypeRef {
	if n := len(ti.inputTypes); n >= 2 {
		return ti.inputTypes[n-2]
	}
	return nil
}

// DefaultValue returns the default value, in GraphQL notation, of the
// argument or input object field whose value is being visited, or nil when
// it has none or the value is a list item or a variable's default value.
// A variable in a position with a default may be nullable
// (VariablesInAllowedPosition).
func (ti *TypeInfo) DefaultValue() *string { return top(ti.defaults) }

// EnumValue returns the definition of the enum value being visited, or nil.
func (ti *TypeInfo) EnumValue() *schema.EnumValue { return ti.enumValue }

// EnterPath enters every node of path, root first, as returned by
// ast.NodePathAt.
func (ti *TypeInfo) EnterPath(path []ast.Node) {
	for _, n := range path {
		ti.Enter(n)
	}
}

// Enter updates the context for entering n.
func (ti *TypeInfo) Enter(n ast.Node) {
	s := ti.schema
	switch n := n.(type) {
	case *ast.OperationDefinition:
		var ref *schema.TypeRef
		if root := s.RootType(operation(n.Operation)); root != nil {
			ref = schema.Named(root.Kind, root.Name)
		}
		ti.types = append(ti.types, ref)
	case *ast.FragmentDefinition:
		var ref *schema.TypeRef
		if n.TypeCondition != nil {
			ref = ti.outputType(n.TypeCondition.Type)
		}
		ti.types = append(ti.types, ref)
	case *ast.InlineFragment:
		var ref *schema.TypeRef
		if n.TypeCondition != nil {
			ref = ti.outputType(n.TypeCondition.Type)
		} else if t := s.TypeOf(ti.Type()); t != nil {
			ref = schema.Named(t.Kind, t.Name)
		}
		ti.types = append(ti.types, ref)
	case *ast.SelectionSet:
		t := s.TypeOf(ti.Type())
		if !t.IsComposite() {
			t = nil
		}
		ti.parentTypes = append(ti.parentTypes, t)
	case *ast.Field:
		var def *schema.Field
		var ref *schema.TypeRef
		if parent := ti.ParentType(); parent != nil && n.Name != nil && !n.Name.Missing() {
			def = s.Field(parent.Name, n.Name.Value)
		}
		if def != nil {
			ref = def.Type
		}
		ti.fieldDefs = append(ti.fieldDefs, def)
		ti.types = append(ti.types, ref)
	case *ast.Directive:
		ti.directive, ti.inDirective = nil, true
		if n.Name != nil && !n.Name.Missing() {
			ti.directive = s.Directive(n.Name.Value)
		}
	case *ast.VariableDefinition:
		ti.inputTypes = append(ti.inputTypes, ti.inputTypeOf(n.Type))
		ti.defaults = append(ti.defaults, nil)
	case *ast.Argument:
		var def *schema.InputValue
		if n.Name != nil && !n.Name.Missing() {
			if ti.inDirective {
				def = ti.directive.Arg(n.Name.Value)
			} else {
				def = ti.FieldDef().Arg(n.Name.Value)
			}
		}
		ti.arguments = append(ti.arguments, def)
		ti.inputTypes = append(ti.inputTypes, typeOfInput(def))
		ti.defaults = append(ti.defaults, defaultOf(def))
	case *ast.ListValue:
		t := ti.InputType().Nullable()
		if t != nil && t.Kind == schema.KindList {
			t = t.OfType
		}
		ti.inputTypes = append(ti.inputTypes, t)
		ti.defaults = append(ti.defaults, nil)
	case *ast.ObjectField:
		var def *schema.InputValue
		if obj := s.TypeOf(ti.InputType()); obj != nil && obj.Kind == schema.KindInputObject && n.Name != nil {
			def = obj.InputField(n.Name.Value)
		}
		ti.inputTypes = append(ti.inputTypes, typeOfInput(def))
		ti.defaults = append(ti.defaults, defaultOf(def))
	case *ast.EnumValue:
		ti.enumValue = nil
		if t := s.TypeOf(ti.InputType()); t != nil && t.Kind == schema.KindEnum {
			ti.enumValue = t.EnumValue(n.Value)
		}
	}
}

// Leave restores the context from before Enter(n).
func (ti *TypeInfo) Leave(n ast.Node) {
	switch n.(type) {
	case *ast.OperationDefinition, *ast.FragmentDefinition, *ast.InlineFragment:
		ti.types = pop(ti.types)
	case *ast.SelectionSet:
		ti.parentTypes = pop(ti.parentTypes)
	case *ast.Field:
		ti.fieldDefs = pop(ti.fieldDefs)
		ti.types = pop(ti.types)
	case *ast.Directive:
		ti.directive, ti.inDirective = nil, false
	case *ast.VariableDefinition, *ast.ListValue, *ast.ObjectField:
		ti.inputTypes = pop(ti.inputTypes)
		ti.defaults = pop(ti.defaults)
	case *ast.Argument:
		ti.arguments = pop(ti.arguments)
		ti.inputTypes = pop(ti.inputTypes)
		ti.defaults = pop(ti.defaults)
	case *ast.EnumValue:
		ti.enumValue = nil
	}
}

// outputType resolves a type condition's named type to a reference, or nil
// when it is missing, unknown or not an output type.
func (ti *TypeInfo) outputType(nt *ast.NamedType) *schema.TypeRef {
	if nt == nil || nt.Name == nil || nt.Name.Missing() {
		return nil
	}
	t := ti.schema.Type(nt.Name.Value)
	if t == nil || !t.Kind.IsOutput() {
		return nil
	}
	return schema.Named(t.Kind, t.Name)
}

// inputTypeOf converts a variable's type to a reference, or nil when a
// name is missing or unknown or the named type is not an input type.
func (ti *TypeInfo) inputTypeOf(t ast.Type) *schema.TypeRef {
	ref := TypeFromAST(ti.schema, t)
	if n := ti.schema.TypeOf(ref); n == nil || !n.Kind.IsInput() {
		return nil
	}
	return ref
}

// TypeFromAST converts a type reference from a document ("[ID!]!") to a
// schema reference, or returns nil when a name is missing or not a type of
// s.
func TypeFromAST(s *schema.Schema, t ast.Type) *schema.TypeRef {
	switch t := t.(type) {
	case *ast.NamedType:
		if t == nil || t.Name == nil || t.Name.Missing() {
			return nil
		}
		if named := s.Type(t.Name.Value); named != nil {
			return schema.Named(named.Kind, named.Name)
		}
	case *ast.ListType:
		if t != nil {
			if inner := TypeFromAST(s, t.Type); inner != nil {
				return schema.ListOf(inner)
			}
		}
	case *ast.NonNullType:
		if t != nil {
			if inner := TypeFromAST(s, t.Type); inner != nil && !inner.IsNonNull() {
				return schema.NonNullOf(inner)
			}
		}
	}
	return nil
}

func operation(op ast.OperationType) schema.Operation {
	switch op {
	case ast.Mutation:
		return schema.OpMutation
	case ast.Subscription:
		return schema.OpSubscription
	}
	return schema.OpQuery
}

func typeOfInput(v *schema.InputValue) *schema.TypeRef {
	if v == nil {
		return nil
	}
	return v.Type
}

func defaultOf(v *schema.InputValue) *string {
	if v == nil {
		return nil
	}
	return v.DefaultValue
}

func top[T any](s []*T) *T {
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

func pop[T any](s []*T) []*T {
	if len(s) == 0 {
		return s
	}
	return s[:len(s)-1]
}
