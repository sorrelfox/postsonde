package validate

import (
	"fmt"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// directivesAt returns the directives of n and the location they are at,
// or false when n cannot have directives.
func directivesAt(n ast.Node) ([]*ast.Directive, schema.DirectiveLocation, bool) {
	switch n := n.(type) {
	case *ast.OperationDefinition:
		loc := schema.LocQuery
		switch n.Operation {
		case ast.Mutation:
			loc = schema.LocMutation
		case ast.Subscription:
			loc = schema.LocSubscription
		}
		return n.Directives, loc, true
	case *ast.FragmentDefinition:
		return n.Directives, schema.LocFragmentDefinition, true
	case *ast.VariableDefinition:
		return n.Directives, schema.LocVariableDefinition, true
	case *ast.Field:
		return n.Directives, schema.LocField, true
	case *ast.FragmentSpread:
		return n.Directives, schema.LocFragmentSpread, true
	case *ast.InlineFragment:
		return n.Directives, schema.LocInlineFragment, true
	}
	return nil, "", false
}

// KnownDirectives reports directives the schema does not define, and
// directives used where their definition does not allow them (spec 5.7.1,
// 5.7.2), at the directive name. Unknown directives get similar directive
// names as suggestions. The spec's directives count as defined even when
// the server did not list them.
var KnownDirectives = Rule{
	Name: "KnownDirectives",
	New: func(c *Context) Visitor {
		var names []string
		return Visitor{Enter: func(n ast.Node) bool {
			dirs, loc, ok := directivesAt(n)
			if !ok {
				return true
			}
			for _, d := range dirs {
				if d == nil || !present(d.Name) {
					continue
				}
				name := d.Name.Value
				def := c.Schema.Directive(name)
				switch {
				case def == nil:
					if names == nil {
						names = allDirectiveNames(c.Schema)
					}
					sugg := suggest(name, names)
					shown := make([]string, len(sugg))
					for i, s := range sugg {
						shown[i] = "@" + s
					}
					c.report(CodeUnknownDirective, d.Name,
						fmt.Sprintf("Unknown directive %q.%s", "@"+name, didYouMean("", shown)), sugg)
				case !def.HasLocation(loc):
					c.report(CodeMisplacedDirective, d.Name,
						fmt.Sprintf("Directive %q may not be used on %s.", "@"+name, loc), nil)
				}
			}
			return true
		}}
	},
}

// allDirectiveNames lists the schema's directives and the built-in
// directives it does not declare.
func allDirectiveNames(s *schema.Schema) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, ds := range [][]*schema.Directive{s.Directives, schema.Builtins().Directives} {
		for _, d := range ds {
			if !seen[d.Name] {
				seen[d.Name] = true
				names = append(names, d.Name)
			}
		}
	}
	return names
}

// UniqueDirectivesPerLocation reports non-repeatable directives used more
// than once on the same node (spec 5.7.3), at the name of every later one.
// Unknown directives are not checked.
var UniqueDirectivesPerLocation = Rule{
	Name: "UniqueDirectivesPerLocation",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			dirs, _, ok := directivesAt(n)
			if !ok || len(dirs) < 2 {
				return true
			}
			seen := map[string]bool{}
			for _, d := range dirs {
				if d == nil || !present(d.Name) {
					continue
				}
				def := c.Schema.Directive(d.Name.Value)
				if def == nil || def.IsRepeatable {
					continue
				}
				if seen[def.Name] {
					c.report(CodeDuplicateDirective, d.Name, fmt.Sprintf(
						"The directive %q can only be used once at this location.", "@"+def.Name), nil)
				}
				seen[def.Name] = true
			}
			return true
		}}
	},
}

// UniqueArgumentNames reports arguments given twice to a field or directive
// (spec 5.4.2), at the name of every later one.
var UniqueArgumentNames = Rule{
	Name: "UniqueArgumentNames",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			args, ok := n.(*ast.ArgumentList)
			if !ok || len(args.Arguments) < 2 {
				return true
			}
			seen := map[string]bool{}
			for _, a := range args.Arguments {
				if a == nil || !present(a.Name) {
					continue
				}
				if seen[a.Name.Value] {
					c.report(CodeDuplicateArgument, a.Name,
						fmt.Sprintf("There can be only one argument named %q.", a.Name.Value), nil)
				}
				seen[a.Name.Value] = true
			}
			return true
		}}
	},
}

// UniqueInputFieldNames reports input object fields given twice in the same
// object value (spec 5.6.3), at the name of every later one.
var UniqueInputFieldNames = Rule{
	Name: "UniqueInputFieldNames",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			obj, ok := n.(*ast.ObjectValue)
			if !ok || len(obj.Fields) < 2 {
				return true
			}
			seen := map[string]bool{}
			for _, f := range obj.Fields {
				if f == nil || !present(f.Name) {
					continue
				}
				if seen[f.Name.Value] {
					c.report(CodeDuplicateInputField, f.Name,
						fmt.Sprintf("There can be only one input field named %q.", f.Name.Value), nil)
				}
				seen[f.Name.Value] = true
			}
			return true
		}}
	},
}
