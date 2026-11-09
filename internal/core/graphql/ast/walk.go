package ast

// Children returns the direct child nodes of n in source order. Names are
// children too. Absent (nil) children are left out.
func Children(n Node) []Node {
	var c children
	switch n := n.(type) {
	case *Document:
		for _, d := range n.Definitions {
			c.add(d)
		}
	case *OperationDefinition:
		c.name(n.Name)
		if n.VariableDefinitions != nil {
			c.add(n.VariableDefinitions)
		}
		c.directives(n.Directives)
		c.selectionSet(n.SelectionSet)
	case *FragmentDefinition:
		c.name(n.Name)
		if n.TypeCondition != nil {
			c.add(n.TypeCondition)
		}
		c.directives(n.Directives)
		c.selectionSet(n.SelectionSet)
	case *VariableDefinitionList:
		for _, d := range n.Definitions {
			c.add(d)
		}
	case *VariableDefinition:
		if n.Variable != nil {
			c.add(n.Variable)
		}
		c.add(n.Type)
		c.add(n.DefaultValue)
		c.directives(n.Directives)
	case *SelectionSet:
		for _, s := range n.Selections {
			c.add(s)
		}
	case *Field:
		c.name(n.Alias)
		c.name(n.Name)
		if n.Arguments != nil {
			c.add(n.Arguments)
		}
		c.directives(n.Directives)
		c.selectionSet(n.SelectionSet)
	case *ArgumentList:
		for _, a := range n.Arguments {
			c.add(a)
		}
	case *Argument:
		c.name(n.Name)
		c.add(n.Value)
	case *FragmentSpread:
		c.name(n.Name)
		c.directives(n.Directives)
	case *InlineFragment:
		if n.TypeCondition != nil {
			c.add(n.TypeCondition)
		}
		c.directives(n.Directives)
		c.selectionSet(n.SelectionSet)
	case *TypeCondition:
		if n.Type != nil {
			c.add(n.Type)
		}
	case *Directive:
		c.name(n.Name)
		if n.Arguments != nil {
			c.add(n.Arguments)
		}
	case *Variable:
		c.name(n.Name)
	case *ListValue:
		for _, v := range n.Values {
			c.add(v)
		}
	case *ObjectValue:
		for _, f := range n.Fields {
			c.add(f)
		}
	case *ObjectField:
		c.name(n.Name)
		c.add(n.Value)
	case *NamedType:
		c.name(n.Name)
	case *ListType:
		c.add(n.Type)
	case *NonNullType:
		c.add(n.Type)
	case *SchemaDefinition:
		c.description(n.Description)
		c.directives(n.Directives)
		for _, o := range n.OperationTypes {
			c.add(o)
		}
	case *RootOperationTypeDefinition:
		if n.Type != nil {
			c.add(n.Type)
		}
	case *TypeDefinition:
		c.description(n.Description)
		c.name(n.Name)
		c.namedTypes(n.Interfaces)
		c.directives(n.Directives)
		c.namedTypes(n.Members)
		for _, f := range n.Fields {
			c.add(f)
		}
		for _, v := range n.EnumValues {
			c.add(v)
		}
		for _, f := range n.InputFields {
			c.add(f)
		}
	case *FieldDefinition:
		c.description(n.Description)
		c.name(n.Name)
		if n.Arguments != nil {
			c.add(n.Arguments)
		}
		c.add(n.Type)
		c.directives(n.Directives)
	case *ArgumentDefinitionList:
		for _, a := range n.Arguments {
			c.add(a)
		}
	case *InputValueDefinition:
		c.description(n.Description)
		c.name(n.Name)
		c.add(n.Type)
		c.add(n.DefaultValue)
		c.directives(n.Directives)
	case *EnumValueDefinition:
		c.description(n.Description)
		c.name(n.Name)
		c.directives(n.Directives)
	case *DirectiveDefinition:
		c.description(n.Description)
		c.name(n.Name)
		if n.Arguments != nil {
			c.add(n.Arguments)
		}
		for _, l := range n.Locations {
			c.name(l)
		}
	}
	return c
}

func (c *children) description(s *StringValue) {
	if s != nil {
		*c = append(*c, s)
	}
}

func (c *children) namedTypes(ts []*NamedType) {
	for _, t := range ts {
		if t != nil {
			*c = append(*c, t)
		}
	}
}

type children []Node

// add appends n unless it is nil, including a typed nil pointer in an
// interface (the parser never stores those, but hand-built trees might).
func (c *children) add(n Node) {
	if n == nil || isNilPointer(n) {
		return
	}
	*c = append(*c, n)
}

func (c *children) name(n *Name) {
	if n != nil {
		*c = append(*c, n)
	}
}

func (c *children) selectionSet(s *SelectionSet) {
	if s != nil {
		*c = append(*c, s)
	}
}

func (c *children) directives(ds []*Directive) {
	for _, d := range ds {
		if d != nil {
			*c = append(*c, d)
		}
	}
}

func isNilPointer(n Node) bool {
	switch n := n.(type) {
	case *Variable:
		return n == nil
	case *IntValue:
		return n == nil
	case *FloatValue:
		return n == nil
	case *StringValue:
		return n == nil
	case *BooleanValue:
		return n == nil
	case *NullValue:
		return n == nil
	case *EnumValue:
		return n == nil
	case *ListValue:
		return n == nil
	case *ObjectValue:
		return n == nil
	case *InvalidValue:
		return n == nil
	case *NamedType:
		return n == nil
	case *ListType:
		return n == nil
	case *NonNullType:
		return n == nil
	case *OperationDefinition:
		return n == nil
	case *FragmentDefinition:
		return n == nil
	case *TypeSystemDefinition:
		return n == nil
	case *SchemaDefinition:
		return n == nil
	case *TypeDefinition:
		return n == nil
	case *DirectiveDefinition:
		return n == nil
	case *Field:
		return n == nil
	case *FragmentSpread:
		return n == nil
	case *InlineFragment:
		return n == nil
	}
	return false
}

// Inspect walks the tree rooted at n depth-first in source order, calling
// f for each node; if f returns false, the node's children are skipped.
func Inspect(n Node, f func(Node) bool) {
	if n == nil || isNilPointer(n) || !f(n) {
		return
	}
	for _, c := range Children(n) {
		Inspect(c, f)
	}
}

// NodePathAt returns the nodes containing byte offset off, from root to the
// innermost node, or nil if root does not contain off.
//
// Containment counts both ends (Loc.Contains), so a cursor right after a
// name is still in the name. When two siblings touch at off, as in "user|("
// (the name ends where the argument list starts), the earlier sibling wins;
// use delimiter offsets such as ArgumentList.LParen to tell "before" from
// "inside". Placeholders (missing names and values) are ordinary nodes here,
// so a cursor in "{ user(id: | }" ends at the *InvalidValue of "id".
func NodePathAt(root Node, off int) []Node {
	if root == nil || isNilPointer(root) || !root.Pos().Contains(off) {
		return nil
	}
	path := []Node{root}
	for n := root; ; {
		var next Node
		for _, c := range Children(n) {
			if c.Pos().Contains(off) {
				next = c
				break
			}
			if c.Pos().Start > off {
				break
			}
		}
		if next == nil {
			return path
		}
		path = append(path, next)
		n = next
	}
}
