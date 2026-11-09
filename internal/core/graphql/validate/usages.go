package validate

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// VariableUsage is a variable used as a value in an operation or fragment.
type VariableUsage struct {
	Variable *ast.Variable
	// Type is the input type expected where the variable is used, or nil
	// when it is unknown.
	Type *schema.TypeRef
	// DefaultValue is the default of the argument or input field the
	// variable is given for, or nil (TypeInfo.DefaultValue).
	DefaultValue *string
	// ParentType is the input type of the list or input object the variable
	// is in, or nil (TypeInfo.ParentInputType).
	ParentType *schema.TypeRef
}

// documentInfo holds what the rules derive from the whole document,
// computed on first use and shared by every rule of one validation.
type documentInfo struct {
	spreads    map[ast.Node][]*ast.FragmentSpread
	referenced map[*ast.OperationDefinition][]*ast.FragmentDefinition
	usages     map[ast.Node][]VariableUsage
	recursive  map[*ast.OperationDefinition][]VariableUsage
}

// FragmentSpreads returns the fragment spreads in the selections of an
// operation or fragment definition, in source order, including those in
// nested fields and inline fragments but not those of the fragments they
// spread. Spreads with a placeholder name are included. The result is
// cached.
func (c *Context) FragmentSpreads(def ast.Definition) []*ast.FragmentSpread {
	if spreads, ok := c.info.spreads[def]; ok {
		return spreads
	}
	var set *ast.SelectionSet
	switch d := def.(type) {
	case *ast.OperationDefinition:
		set = d.SelectionSet
	case *ast.FragmentDefinition:
		set = d.SelectionSet
	}
	var spreads []*ast.FragmentSpread
	var visit func(set *ast.SelectionSet)
	visit = func(set *ast.SelectionSet) {
		if set == nil {
			return
		}
		for _, sel := range set.Selections {
			switch sel := sel.(type) {
			case *ast.Field:
				visit(sel.SelectionSet)
			case *ast.InlineFragment:
				visit(sel.SelectionSet)
			case *ast.FragmentSpread:
				spreads = append(spreads, sel)
			}
		}
	}
	visit(set)
	if c.info.spreads == nil {
		c.info.spreads = map[ast.Node][]*ast.FragmentSpread{}
	}
	c.info.spreads[def] = spreads
	return spreads
}

// RecursivelyReferencedFragments returns the fragment definitions that op
// spreads, directly or through other fragments, each once, in the order they
// are first reached. Unknown fragments and cycles are skipped. The result is
// cached.
func (c *Context) RecursivelyReferencedFragments(op *ast.OperationDefinition) []*ast.FragmentDefinition {
	if frags, ok := c.info.referenced[op]; ok {
		return frags
	}
	var frags []*ast.FragmentDefinition
	seen := map[string]bool{}
	queue := []ast.Definition{op}
	for len(queue) > 0 {
		def := queue[0]
		queue = queue[1:]
		for _, spread := range c.FragmentSpreads(def) {
			if !present(spread.Name) || seen[spread.Name.Value] {
				continue
			}
			seen[spread.Name.Value] = true
			if f := c.Fragment(spread.Name.Value); f != nil {
				frags = append(frags, f)
				queue = append(queue, f)
			}
		}
	}
	if c.info.referenced == nil {
		c.info.referenced = map[*ast.OperationDefinition][]*ast.FragmentDefinition{}
	}
	c.info.referenced[op] = frags
	return frags
}

// VariableUsages returns the variables used as values in an operation or
// fragment definition, in source order, not following fragment spreads.
// Variables in variable definitions (default values, directives) are not
// usages, and neither are variables with a placeholder name. The expected
// types come from a separate walk of the definition, so a fragment's usages
// are typed by its own type condition. The result is cached.
func (c *Context) VariableUsages(def ast.Definition) []VariableUsage {
	if usages, ok := c.info.usages[def]; ok {
		return usages
	}
	var usages []VariableUsage
	ti := NewTypeInfo(c.Schema)
	Walk(def, ti, Visitor{Enter: func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.VariableDefinition:
			return false
		case *ast.Variable:
			if present(n.Name) {
				usages = append(usages, VariableUsage{
					Variable:     n,
					Type:         ti.InputType(),
					DefaultValue: ti.DefaultValue(),
					ParentType:   ti.ParentInputType(),
				})
			}
		}
		return true
	}})
	if c.info.usages == nil {
		c.info.usages = map[ast.Node][]VariableUsage{}
	}
	c.info.usages[def] = usages
	return usages
}

// RecursiveVariableUsages returns the variable usages of op and of every
// fragment it references (RecursivelyReferencedFragments), the operation's
// first. The result is cached.
func (c *Context) RecursiveVariableUsages(op *ast.OperationDefinition) []VariableUsage {
	if usages, ok := c.info.recursive[op]; ok {
		return usages
	}
	usages := c.VariableUsages(op)
	if frags := c.RecursivelyReferencedFragments(op); len(frags) > 0 {
		usages = append([]VariableUsage(nil), usages...)
		for _, f := range frags {
			usages = append(usages, c.VariableUsages(f)...)
		}
	}
	if c.info.recursive == nil {
		c.info.recursive = map[*ast.OperationDefinition][]VariableUsage{}
	}
	c.info.recursive[op] = usages
	return usages
}

// operations returns the operation definitions of the document.
func (c *Context) operations() []*ast.OperationDefinition {
	var ops []*ast.OperationDefinition
	for _, d := range c.Doc.Definitions {
		if op, ok := d.(*ast.OperationDefinition); ok {
			ops = append(ops, op)
		}
	}
	return ops
}

// completeSpreads reports whether every fragment op reaches is known: no
// spread with a placeholder name or an unknown fragment. Rules that report
// what is missing from an operation (unused variables) stay quiet
// otherwise, since the missing fragment may be what uses it.
func (c *Context) completeSpreads(op *ast.OperationDefinition) bool {
	defs := []ast.Definition{op}
	for _, f := range c.RecursivelyReferencedFragments(op) {
		defs = append(defs, f)
	}
	for _, d := range defs {
		for _, s := range c.FragmentSpreads(d) {
			if !present(s.Name) || c.Fragment(s.Name.Value) == nil {
				return false
			}
		}
	}
	return true
}
