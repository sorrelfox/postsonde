package validate

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// present reports whether a name was typed (it is not a placeholder).
func present(n *ast.Name) bool { return n != nil && !n.Missing() }

// FieldsOnCorrectType reports fields the parent type does not define
// (spec 5.3.1). For an interface or union it suggests inline fragments on
// the types that have the field; otherwise similar field names.
var FieldsOnCorrectType = Rule{
	Name: "FieldsOnCorrectType",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			f, ok := n.(*ast.Field)
			if !ok || !present(f.Name) {
				return true
			}
			parent := c.TypeInfo.ParentType()
			if parent == nil || c.TypeInfo.FieldDef() != nil {
				return true
			}
			name := f.Name.Value
			var fieldNames []string
			if parent.Kind == schema.KindObject || parent.Kind == schema.KindInterface {
				for _, fd := range parent.Fields {
					fieldNames = append(fieldNames, fd.Name)
				}
			}
			fields := suggest(name, fieldNames)
			hint := didYouMean("to use an inline fragment on", suggestedTypeNames(c.Schema, parent, name))
			if hint == "" {
				hint = didYouMean("", fields)
			}
			c.report(CodeUnknownField, f.Name,
				fmt.Sprintf("Cannot query field %q on type %q.%s", name, parent.Name, hint), fields)
			return true
		}}
	},
}

// suggestedTypeNames returns the possible types of an abstract type, and
// the interfaces they implement, that define field: most used first, then
// interfaces before their implementations, then by name (graphql-js's
// getSuggestedTypeNames).
func suggestedTypeNames(s *schema.Schema, t *schema.Type, field string) []string {
	if !t.IsAbstract() {
		return nil
	}
	var types []*schema.Type
	usage := map[string]int{}
	for _, p := range s.PossibleTypes(t.Name) {
		if p.Field(field) == nil {
			continue
		}
		if _, seen := usage[p.Name]; !seen {
			types = append(types, p)
		}
		usage[p.Name] = 1
		for _, in := range p.Interfaces {
			iface := s.Type(in)
			if iface == nil || iface.Field(field) == nil {
				continue
			}
			if _, seen := usage[in]; !seen {
				types = append(types, iface)
			}
			usage[in]++
		}
	}
	slices.SortStableFunc(types, func(a, b *schema.Type) int {
		if d := cmp.Compare(usage[b.Name], usage[a.Name]); d != 0 {
			return d
		}
		if a.Kind == schema.KindInterface && s.IsSubType(a.Name, b.Name) {
			return -1
		}
		if b.Kind == schema.KindInterface && s.IsSubType(b.Name, a.Name) {
			return 1
		}
		return naturalCompare(a.Name, b.Name)
	})
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.Name
	}
	return names
}

// KnownArgumentNames reports arguments that the field or directive does not
// define (spec 5.4.1). Arguments of unknown fields and directives are not
// checked.
var KnownArgumentNames = Rule{
	Name: "KnownArgumentNames",
	New: func(c *Context) Visitor {
		check := func(args *ast.ArgumentList, defs []*schema.InputValue, on string) {
			if args == nil {
				return
			}
			for _, a := range args.Arguments {
				if !present(a.Name) || findArg(defs, a.Name.Value) != nil {
					continue
				}
				names := make([]string, len(defs))
				for i, d := range defs {
					names[i] = d.Name
				}
				sugg := suggest(a.Name.Value, names)
				c.report(CodeUnknownArgument, a.Name,
					fmt.Sprintf("Unknown argument %q on %s.%s", a.Name.Value, on, didYouMean("", sugg)), sugg)
			}
		}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Field:
				if def, parent := c.TypeInfo.FieldDef(), c.TypeInfo.ParentType(); def != nil && parent != nil {
					check(n.Arguments, def.Args, fmt.Sprintf("field %q", parent.Name+"."+def.Name))
				}
			case *ast.Directive:
				if d := c.TypeInfo.Directive(); d != nil {
					check(n.Arguments, d.Args, fmt.Sprintf("directive %q", "@"+d.Name))
				}
			}
			return true
		}}
	},
}

// ProvidedRequiredArguments reports required arguments (non-null without a
// default) that a field or directive leaves out (spec 5.4.2.1).
var ProvidedRequiredArguments = Rule{
	Name: "ProvidedRequiredArguments",
	New: func(c *Context) Visitor {
		check := func(name *ast.Name, args *ast.ArgumentList, defs []*schema.InputValue, what string) {
			for _, d := range defs {
				if !d.Type.IsNonNull() || d.DefaultValue != nil || hasArg(args, d.Name) {
					continue
				}
				c.report(CodeMissingArgument, name, fmt.Sprintf(
					"%s argument %q of type %q is required, but it was not provided.", what, d.Name, d.Type.String()), nil)
			}
		}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Field:
				if def := c.TypeInfo.FieldDef(); def != nil && present(n.Name) {
					check(n.Name, n.Arguments, def.Args, fmt.Sprintf("Field %q", def.Name))
				}
			case *ast.Directive:
				if d := c.TypeInfo.Directive(); d != nil && present(n.Name) {
					check(n.Name, n.Arguments, d.Args, fmt.Sprintf("Directive %q", "@"+d.Name))
				}
			}
			return true
		}}
	},
}

func findArg(defs []*schema.InputValue, name string) *schema.InputValue {
	for _, d := range defs {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func hasArg(args *ast.ArgumentList, name string) bool {
	if args == nil {
		return false
	}
	for _, a := range args.Arguments {
		if a.Name != nil && a.Name.Value == name {
			return true
		}
	}
	return false
}

// KnownTypeNames reports type names that the schema does not have, in
// variable definitions and type conditions (spec 5.5.1.2, 5.8.3).
var KnownTypeNames = Rule{
	Name: "KnownTypeNames",
	New: func(c *Context) Visitor {
		var typeNames []string
		return Visitor{Enter: func(n ast.Node) bool {
			nt, ok := n.(*ast.NamedType)
			if !ok || !present(nt.Name) || c.Schema.Type(nt.Name.Value) != nil {
				return true
			}
			if typeNames == nil {
				typeNames = allTypeNames(c.Schema)
			}
			sugg := suggest(nt.Name.Value, typeNames)
			c.report(CodeUnknownType, nt.Name,
				fmt.Sprintf("Unknown type %q.%s", nt.Name.Value, didYouMean("", sugg)), sugg)
			return true
		}}
	},
}

// allTypeNames lists the schema's types and the built-in types it does not
// declare.
func allTypeNames(s *schema.Schema) []string {
	var names []string
	seen := map[string]bool{}
	add := func(ts []*schema.Type) {
		for _, t := range ts {
			if !seen[t.Name] {
				seen[t.Name] = true
				names = append(names, t.Name)
			}
		}
	}
	add(s.Types)
	add(schema.Builtins().Types)
	return names
}

// ScalarLeafs reports selection sets on scalar and enum fields, and object,
// interface and union fields without one (spec 5.3.3).
var ScalarLeafs = Rule{
	Name: "ScalarLeafs",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			f, ok := n.(*ast.Field)
			if !ok || !present(f.Name) {
				return true
			}
			ref := c.TypeInfo.Type()
			t := c.Schema.TypeOf(ref)
			switch {
			case t == nil:
			case t.IsLeaf() && f.SelectionSet != nil:
				c.report(CodeSelectionOnLeaf, f.SelectionSet, fmt.Sprintf(
					"Field %q must not have a selection since type %q has no subfields.", f.Name.Value, ref.String()), nil)
			case t.IsComposite() && f.SelectionSet == nil && (f.Arguments == nil || f.Arguments.RParen >= 0):
				// With an unclosed argument list the selection set cannot have
				// been parsed yet; the parser reported the list.
				c.report(CodeMissingSelection, f.Name, fmt.Sprintf(
					"Field %q of type %q must have a selection of subfields. Did you mean \"%s { ... }\"?",
					f.Name.Value, ref.String(), f.Name.Value), nil)
			}
			return true
		}}
	},
}
