package validate

import (
	"fmt"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// KnownFragmentNames reports spreads of fragments the document does not
// define (spec 5.5.2.1), at the spread's name, suggesting similar fragment
// names.
var KnownFragmentNames = Rule{
	Name: "KnownFragmentNames",
	New: func(c *Context) Visitor {
		var names []string
		return Visitor{Enter: func(n ast.Node) bool {
			spread, ok := n.(*ast.FragmentSpread)
			if !ok || !present(spread.Name) || c.Fragment(spread.Name.Value) != nil {
				return true
			}
			if names == nil {
				names = []string{}
				for _, d := range c.Doc.Definitions {
					if f, ok := d.(*ast.FragmentDefinition); ok && present(f.Name) {
						names = append(names, f.Name.Value)
					}
				}
			}
			sugg := suggest(spread.Name.Value, names)
			c.report(CodeUnknownFragment, spread.Name,
				fmt.Sprintf("Unknown fragment %q.%s", spread.Name.Value, didYouMean("", sugg)), sugg)
			return true
		}}
	},
}

// UniqueFragmentNames reports fragments named like an earlier fragment
// (spec 5.5.1.1), at the name of every later one.
var UniqueFragmentNames = Rule{
	Name: "UniqueFragmentNames",
	New: func(c *Context) Visitor {
		seen := map[string]bool{}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.FragmentDefinition:
				if present(n.Name) {
					if seen[n.Name.Value] {
						c.report(CodeDuplicateFragmentName, n.Name,
							fmt.Sprintf("There can be only one fragment named %q.", n.Name.Value), nil)
					}
					seen[n.Name.Value] = true
				}
			}
			return false
		}}
	},
}

// FragmentsOnCompositeTypes reports fragment definitions and inline
// fragments whose type condition is a scalar, enum or input object (spec
// 5.5.1.3), at the type name. Unknown types are KnownTypeNames'.
var FragmentsOnCompositeTypes = Rule{
	Name: "FragmentsOnCompositeTypes",
	New: func(c *Context) Visitor {
		check := func(tc *ast.TypeCondition, subject string) {
			if tc == nil || tc.Type == nil || !present(tc.Type.Name) {
				return
			}
			if t := c.Schema.Type(tc.Type.Name.Value); t != nil && !t.IsComposite() {
				c.report(CodeFragmentOnNonComposite, tc.Type.Name,
					fmt.Sprintf("%s cannot condition on non composite type %q.", subject, t.Name), nil)
			}
		}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.InlineFragment:
				check(n.TypeCondition, "Fragment")
			case *ast.FragmentDefinition:
				subject := "Fragment"
				if present(n.Name) {
					subject = fmt.Sprintf("Fragment %q", n.Name.Value)
				}
				check(n.TypeCondition, subject)
			}
			return true
		}}
	},
}

// PossibleFragmentSpreads reports inline fragments and fragment spreads
// whose type condition can never apply where they are used, because no
// object type is both the parent type and the condition (spec 5.5.2.3): an
// inline fragment at its type name, a spread at its name.
var PossibleFragmentSpreads = Rule{
	Name: "PossibleFragmentSpreads",
	New: func(c *Context) Visitor {
		check := func(at ast.Node, tc *ast.TypeCondition, subject string) {
			parent := c.TypeInfo.ParentType()
			if parent == nil || tc == nil || tc.Type == nil || !present(tc.Type.Name) {
				return
			}
			frag := c.Schema.Type(tc.Type.Name.Value)
			if !frag.IsComposite() || typesOverlap(c.Schema, parent, frag) {
				return
			}
			c.report(CodeImpossibleFragmentSpread, at, fmt.Sprintf(
				"%s cannot be spread here as objects of type %q can never be of type %q.", subject, parent.Name, frag.Name), nil)
		}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.InlineFragment:
				if n.TypeCondition != nil && n.TypeCondition.Type != nil {
					check(n.TypeCondition.Type.Name, n.TypeCondition, "Fragment")
				}
			case *ast.FragmentSpread:
				if present(n.Name) {
					if f := c.Fragment(n.Name.Value); f != nil {
						check(n.Name, f.TypeCondition, fmt.Sprintf("Fragment %q", n.Name.Value))
					}
				}
			}
			return true
		}}
	},
}

// typesOverlap reports whether some object type is a possible type of both
// composite types a and b (graphql-js's doTypesOverlap).
func typesOverlap(s *schema.Schema, a, b *schema.Type) bool {
	switch {
	case a.Name == b.Name:
		return true
	case a.IsAbstract() && b.IsAbstract():
		for _, t := range s.PossibleTypes(a.Name) {
			if s.IsPossibleType(b.Name, t.Name) {
				return true
			}
		}
		return false
	case a.IsAbstract():
		return s.IsPossibleType(a.Name, b.Name)
	case b.IsAbstract():
		return s.IsPossibleType(b.Name, a.Name)
	}
	return false
}

// NoUnusedFragments reports fragment definitions that no operation uses,
// directly or through other fragments (spec 5.5.1.4), at the fragment name.
// While a spread without a name is being typed, nothing is reported.
var NoUnusedFragments = Rule{
	Name: "NoUnusedFragments",
	New: func(c *Context) Visitor {
		return Visitor{
			Enter: func(n ast.Node) bool {
				_, ok := n.(*ast.Document)
				return ok
			},
			Leave: func(n ast.Node) {
				if _, ok := n.(*ast.Document); !ok {
					return
				}
				used := map[string]bool{}
				for _, d := range c.Doc.Definitions {
					for _, s := range c.FragmentSpreads(d) {
						if !present(s.Name) {
							return
						}
					}
					if op, ok := d.(*ast.OperationDefinition); ok {
						for _, f := range c.RecursivelyReferencedFragments(op) {
							used[f.Name.Value] = true
						}
					}
				}
				for _, d := range c.Doc.Definitions {
					if f, ok := d.(*ast.FragmentDefinition); ok && present(f.Name) && !used[f.Name.Value] {
						c.report(CodeUnusedFragment, f.Name, fmt.Sprintf("Fragment %q is never used.", f.Name.Value), nil)
					}
				}
			},
		}
	},
}

// maxCyclePath is how many fragments a cycle message lists.
const maxCyclePath = 10

// NoFragmentCycles reports fragments that spread themselves, directly or
// through other fragments (spec 5.5.2.2). Each cycle is reported once, at
// the spread that closes it, with the fragments in between in the message:
// Cannot spread fragment "A" within itself via "B", "C". Past maxCyclePath
// fragments the message ends with "and N more". The search visits every
// fragment once (graphql-js's algorithm).
var NoFragmentCycles = Rule{
	Name: "NoFragmentCycles",
	New: func(c *Context) Visitor {
		visited := map[string]bool{}
		var path []*ast.FragmentSpread
		index := map[string]int{} // fragment name -> its position in path
		var detect func(f *ast.FragmentDefinition)
		detect = func(f *ast.FragmentDefinition) {
			name := f.Name.Value
			if visited[name] {
				return
			}
			visited[name] = true
			spreads := c.FragmentSpreads(f)
			if len(spreads) == 0 {
				return
			}
			index[name] = len(path)
			for _, s := range spreads {
				if !present(s.Name) {
					continue
				}
				path = append(path, s)
				if i, ok := index[s.Name.Value]; ok {
					via := path[i : len(path)-1]
					msg := fmt.Sprintf("Cannot spread fragment %q within itself", s.Name.Value)
					for j, v := range via[:min(len(via), maxCyclePath)] {
						if j == 0 {
							msg += " via "
						} else {
							msg += ", "
						}
						msg += fmt.Sprintf("%q", v.Name.Value)
					}
					if len(via) > maxCyclePath {
						msg += fmt.Sprintf(" and %d more", len(via)-maxCyclePath)
					}
					c.report(CodeFragmentCycle, s.Name, msg+".", nil)
				} else if next := c.Fragment(s.Name.Value); next != nil {
					detect(next)
				}
				path = path[:len(path)-1]
			}
			delete(index, name)
		}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.FragmentDefinition:
				if present(n.Name) {
					detect(n)
				}
			}
			return false
		}}
	},
}
