package validate

import (
	"fmt"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// keywordLoc returns the range editors mark for a whole operation: its
// keyword ("query"), or the "{" of a shorthand query.
func keywordLoc(op *ast.OperationDefinition) ast.Loc {
	switch {
	case op.Keyword >= 0:
		return ast.Loc{Start: op.Keyword, End: op.Keyword + len(op.Operation.String())}
	case op.SelectionSet != nil:
		return ast.Loc{Start: op.SelectionSet.LBrace, End: op.SelectionSet.LBrace + 1}
	}
	return op.Loc
}

// UniqueOperationNames reports operations named like an earlier operation
// (spec 5.2.1.1), at the name of every later one.
var UniqueOperationNames = Rule{
	Name: "UniqueOperationNames",
	New: func(c *Context) Visitor {
		seen := map[string]bool{}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				if present(n.Name) {
					if seen[n.Name.Value] {
						c.report(CodeDuplicateOperationName, n.Name,
							fmt.Sprintf("There can be only one operation named %q.", n.Name.Value), nil)
					}
					seen[n.Name.Value] = true
				}
			}
			return false
		}}
	},
}

// LoneAnonymousOperation reports anonymous operations in a document with
// more than one operation (spec 5.2.2.1), at the keyword of each anonymous
// one, or its "{" for the shorthand.
var LoneAnonymousOperation = Rule{
	Name: "LoneAnonymousOperation",
	New: func(c *Context) Visitor {
		many := false
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				many = len(c.operations()) > 1
				return true
			case *ast.OperationDefinition:
				if n.Name == nil && many {
					c.report(CodeAnonymousOperation, keywordLoc(n),
						"This anonymous operation must be the only defined operation.", nil)
				}
			}
			return false
		}}
	},
}

// KnownOperationTypes reports operations whose root type the schema does
// not have, such as a mutation against a read-only API, at the keyword.
// Nothing below such an operation is resolved, so other rules stay quiet
// about its fields.
var KnownOperationTypes = Rule{
	Name: "KnownOperationTypes",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				if c.Schema.RootType(operation(n.Operation)) == nil {
					c.report(CodeUnknownOperationType, keywordLoc(n),
						fmt.Sprintf("The %s operation is not supported by the schema.", n.Operation), nil)
				}
			}
			return false
		}}
	},
}

// SingleFieldSubscriptions reports subscriptions that select more than one
// root field, or an introspection root field (spec 5.2.3.1). Root fields are
// collected through inline fragments and fragment spreads by response key;
// @skip and @include are not evaluated. An extra field is reported at the
// name of the first field of each response key after the first; an
// introspection field at its name.
var SingleFieldSubscriptions = Rule{
	Name: "SingleFieldSubscriptions",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				if n.Operation != ast.Subscription || c.Schema.RootType(schema.OpSubscription) == nil {
					return false
				}
				subject := "Anonymous Subscription"
				if present(n.Name) {
					subject = fmt.Sprintf("Subscription %q", n.Name.Value)
				}
				fields := collectRootFields(c, n.SelectionSet)
				for i, f := range fields {
					if i > 0 {
						c.report(CodeSingleFieldSubscription, f.Name,
							subject+" must select only one top level field.", nil)
					}
					if strings.HasPrefix(f.Name.Value, "__") {
						c.report(CodeSingleFieldSubscription, f.Name,
							subject+" must not select an introspection top level field.", nil)
					}
				}
			}
			return false
		}}
	},
}

// collectRootFields returns the first field of each response key selected
// by set, through inline fragments and known fragment spreads (each
// fragment once), in order.
func collectRootFields(c *Context, set *ast.SelectionSet) []*ast.Field {
	var fields []*ast.Field
	keys := map[string]bool{}
	visited := map[string]bool{}
	var collect func(set *ast.SelectionSet)
	collect = func(set *ast.SelectionSet) {
		if set == nil {
			return
		}
		for _, sel := range set.Selections {
			switch sel := sel.(type) {
			case *ast.Field:
				if !present(sel.Name) {
					continue
				}
				key := sel.Name.Value
				if present(sel.Alias) {
					key = sel.Alias.Value
				}
				if !keys[key] {
					keys[key] = true
					fields = append(fields, sel)
				}
			case *ast.InlineFragment:
				collect(sel.SelectionSet)
			case *ast.FragmentSpread:
				if !present(sel.Name) || visited[sel.Name.Value] {
					continue
				}
				visited[sel.Name.Value] = true
				if f := c.Fragment(sel.Name.Value); f != nil {
					collect(f.SelectionSet)
				}
			}
		}
	}
	collect(set)
	return fields
}
