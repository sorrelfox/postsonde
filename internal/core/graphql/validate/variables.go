package validate

import (
	"fmt"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// variableDefinitions returns the variable definitions of op with a name.
func variableDefinitions(op *ast.OperationDefinition) []*ast.VariableDefinition {
	if op.VariableDefinitions == nil {
		return nil
	}
	var defs []*ast.VariableDefinition
	for _, d := range op.VariableDefinitions.Definitions {
		if d != nil && d.Variable != nil && present(d.Variable.Name) {
			defs = append(defs, d)
		}
	}
	return defs
}

// UniqueVariableNames reports variables defined twice by an operation (spec
// 5.8.1), at every later definition's variable.
var UniqueVariableNames = Rule{
	Name: "UniqueVariableNames",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				seen := map[string]bool{}
				for _, d := range variableDefinitions(n) {
					name := d.Variable.Name.Value
					if seen[name] {
						c.report(CodeDuplicateVariable, d.Variable,
							fmt.Sprintf("There can be only one variable named %q.", "$"+name), nil)
					}
					seen[name] = true
				}
			}
			return false
		}}
	},
}

// VariablesAreInputTypes reports variables whose type is an object,
// interface or union (spec 5.8.2), at the type. Unknown types are
// KnownTypeNames'.
var VariablesAreInputTypes = Rule{
	Name: "VariablesAreInputTypes",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document, *ast.OperationDefinition, *ast.VariableDefinitionList:
				return true
			case *ast.VariableDefinition:
				ref := TypeFromAST(c.Schema, n.Type)
				if t := c.Schema.TypeOf(ref); t != nil && !t.Kind.IsInput() {
					subject := "Variable"
					if n.Variable != nil && present(n.Variable.Name) {
						subject = fmt.Sprintf("Variable %q", "$"+n.Variable.Name.Value)
					}
					c.report(CodeNonInputVariableType, n.Type,
						fmt.Sprintf("%s cannot be non-input type %q.", subject, ref.String()), nil)
				}
			}
			return false
		}}
	},
}

// NoUndefinedVariables reports variables used by an operation, directly or
// in the fragments it spreads, that it does not define (spec 5.8.3), at
// every usage. A usage in a fragment shared by several operations is
// reported once for each operation that lacks the variable.
var NoUndefinedVariables = Rule{
	Name: "NoUndefinedVariables",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				defined := map[string]bool{}
				for _, d := range variableDefinitions(n) {
					defined[d.Variable.Name.Value] = true
				}
				for _, u := range c.RecursiveVariableUsages(n) {
					name := u.Variable.Name.Value
					if defined[name] {
						continue
					}
					msg := fmt.Sprintf("Variable %q is not defined.", "$"+name)
					if present(n.Name) {
						msg = fmt.Sprintf("Variable %q is not defined by operation %q.", "$"+name, n.Name.Value)
					}
					c.report(CodeUndefinedVariable, u.Variable, msg, nil)
				}
			}
			return false
		}}
	},
}

// NoUnusedVariables reports variables an operation defines but neither it
// nor the fragments it spreads use (spec 5.8.4), at the variable in its
// definition. An operation whose selection set is missing or unclosed, or
// that reaches a spread without a name or of an unknown fragment, is not
// checked: the part not typed yet may use the variable.
var NoUnusedVariables = Rule{
	Name: "NoUnusedVariables",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				defs := variableDefinitions(n)
				if len(defs) == 0 || n.SelectionSet == nil || n.SelectionSet.RBrace < 0 || !c.completeSpreads(n) {
					return false
				}
				used := map[string]bool{}
				for _, u := range c.RecursiveVariableUsages(n) {
					used[u.Variable.Name.Value] = true
				}
				for _, d := range defs {
					name := d.Variable.Name.Value
					if used[name] {
						continue
					}
					msg := fmt.Sprintf("Variable %q is never used.", "$"+name)
					if present(n.Name) {
						msg = fmt.Sprintf("Variable %q is never used in operation %q.", "$"+name, n.Name.Value)
					}
					c.report(CodeUnusedVariable, d.Variable, msg, nil)
				}
			}
			return false
		}}
	},
}

// VariablesInAllowedPosition reports variables whose type does not fit
// where they are used (spec 5.8.5), at the usage: a nullable variable where
// a non-null value is expected, unless the variable or the argument or input
// field has a non-null default; a single value where a list is expected; a
// different named type. It also reports nullable variables given for a field
// of a @oneOf input object. Variables that are undefined or whose type is
// unknown or not an input type are other rules'.
var VariablesInAllowedPosition = Rule{
	Name: "VariablesInAllowedPosition",
	New: func(c *Context) Visitor {
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Document:
				return true
			case *ast.OperationDefinition:
				defs := map[string]*ast.VariableDefinition{}
				for _, d := range variableDefinitions(n) {
					if _, dup := defs[d.Variable.Name.Value]; !dup {
						defs[d.Variable.Name.Value] = d
					}
				}
				for _, u := range c.RecursiveVariableUsages(n) {
					def := defs[u.Variable.Name.Value]
					if def == nil || u.Type == nil {
						continue
					}
					varType := TypeFromAST(c.Schema, def.Type)
					if t := c.Schema.TypeOf(varType); t == nil || !t.Kind.IsInput() {
						continue
					}
					name := "$" + u.Variable.Name.Value
					if !allowedVariableUsage(c.Schema, varType, def.DefaultValue, u.Type, u.DefaultValue) {
						c.report(CodeVariablePosition, u.Variable, fmt.Sprintf(
							"Variable %q of type %q used in position expecting type %q.", name, varType.String(), u.Type.String()), nil)
						continue
					}
					if parent := c.Schema.TypeOf(u.ParentType); parent != nil && parent.Kind == schema.KindInputObject &&
						parent.IsOneOf && !u.ParentType.IsList() && !varType.IsNonNull() {
						c.report(CodeOneOf, u.Variable, fmt.Sprintf(
							"Variable %q is of type %q but must be non-nullable to be used for OneOf Input Object %q.",
							name, varType.String(), parent.Name), nil)
					}
				}
			}
			return false
		}}
	},
}

// allowedVariableUsage reports whether a variable of type varType with the
// default varDefault may be used where locType, with the default
// locDefault, is expected (graphql-js's allowedVariableUsage).
func allowedVariableUsage(s *schema.Schema, varType *schema.TypeRef, varDefault ast.Value, locType *schema.TypeRef, locDefault *string) bool {
	if locType.IsNonNull() && !varType.IsNonNull() {
		_, isNull := varDefault.(*ast.NullValue)
		if (varDefault == nil || isNull) && locDefault == nil {
			return false
		}
		return isTypeSubTypeOf(s, varType, locType.OfType)
	}
	return isTypeSubTypeOf(s, varType, locType)
}

// isTypeSubTypeOf reports whether a value of type sub is always a valid
// value of type super (graphql-js's isTypeSubTypeOf).
func isTypeSubTypeOf(s *schema.Schema, sub, super *schema.TypeRef) bool {
	switch {
	case sub == nil || super == nil:
		return false
	case super.Kind == schema.KindNonNull:
		return sub.Kind == schema.KindNonNull && isTypeSubTypeOf(s, sub.OfType, super.OfType)
	case sub.Kind == schema.KindNonNull:
		return isTypeSubTypeOf(s, sub.OfType, super)
	case super.Kind == schema.KindList:
		return sub.Kind == schema.KindList && isTypeSubTypeOf(s, sub.OfType, super.OfType)
	case sub.Kind == schema.KindList:
		return false
	}
	return s.IsSubType(super.Name, sub.Name)
}
