package complete

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// typeInfo is what the walk from the document root to the cursor learns
// about the types around it.
//
// This is a deliberately small, local resolver: it follows only the path to
// the cursor, and unknown names make the affected types unknown ("" or nil)
// instead of reporting errors. The validator has a full type-info
// walker over the whole document; the two can be unified later.
type typeInfo struct {
	op   *ast.OperationDefinition // enclosing operation, or nil
	frag *ast.FragmentDefinition  // enclosing fragment definition, or nil

	// outType is the composite type of the innermost selection scope, ""
	// when unknown.
	outType string
	// parentType is the type outType had when entering the innermost field,
	// fragment spread or inline fragment: the type the field is selected on,
	// or the type a type condition must apply to.
	parentType string

	// args are the argument definitions of the innermost field or directive
	// on the path; owner names it ("Type.field", "@name"), "" when unknown.
	args  []*schema.InputValue
	owner string

	// expected is the type expected at the innermost value position, nil
	// when unknown.
	expected *schema.TypeRef
	// inputType is the input object of the innermost object value, or nil.
	inputType *schema.Type
	// varDef is the enclosing variable definition: its default value and
	// directive arguments are constant (no variables).
	varDef *ast.VariableDefinition
}

func (c *completer) walk(path []ast.Node) {
	s := c.s
	for _, n := range path {
		switch n := n.(type) {
		case *ast.OperationDefinition:
			c.op = n
			c.outType = ""
			if t := s.RootType(schema.Operation(n.Operation.String())); t != nil {
				c.outType = t.Name
			}
		case *ast.FragmentDefinition:
			c.frag = n
			c.outType = c.conditionType(n.TypeCondition)
		case *ast.Field:
			c.parentType = c.outType
			c.outType, c.args, c.owner = "", nil, ""
			if c.parentType != "" && n.Name != nil && !n.Name.Missing() {
				if f := s.Field(c.parentType, n.Name.Value); f != nil {
					c.args, c.owner = f.Args, c.parentType+"."+f.Name
					if t := s.TypeOf(f.Type); t.IsComposite() {
						c.outType = t.Name
					}
				}
			}
		case *ast.FragmentSpread:
			c.parentType = c.outType
		case *ast.InlineFragment:
			c.parentType = c.outType
			if n.TypeCondition != nil {
				c.outType = c.conditionType(n.TypeCondition)
			}
		case *ast.Directive:
			c.args, c.owner = nil, ""
			if n.Name != nil && !n.Name.Missing() {
				if d := s.Directive(n.Name.Value); d != nil {
					c.args, c.owner = d.Args, "@"+d.Name
				}
			}
		case *ast.VariableDefinition:
			c.varDef = n
			c.expected = c.typeRef(n.Type)
		case *ast.Argument:
			c.expected = nil
			if n.Name != nil {
				if a := findInput(c.args, n.Name.Value); a != nil {
					c.expected = a.Type
				}
			}
		case *ast.ObjectValue:
			c.inputType = nil
			if t := s.TypeOf(c.expected); t != nil && t.Kind == schema.KindInputObject {
				c.inputType = t
			}
			c.expected = nil
		case *ast.ObjectField:
			c.expected = nil
			if c.inputType != nil && n.Name != nil {
				if f := c.inputType.InputField(n.Name.Value); f != nil {
					c.expected = f.Type
				}
			}
		case *ast.ListValue:
			// A non-list type keeps its type: input coercion accepts [x] for x.
			if e := c.expected.Elem(); e != nil {
				c.expected = e
			}
		}
	}
}

// conditionType returns the composite type named by a type condition, or "".
func (c *completer) conditionType(tc *ast.TypeCondition) string {
	if tc == nil || tc.Type == nil || tc.Type.Name == nil || tc.Type.Name.Missing() {
		return ""
	}
	if t := c.s.Type(tc.Type.Name.Value); t.IsComposite() {
		return t.Name
	}
	return ""
}

// typeRef converts a syntax tree type to a schema reference, or nil when a
// name is missing or unknown.
func (c *completer) typeRef(t ast.Type) *schema.TypeRef {
	switch t := t.(type) {
	case *ast.NamedType:
		if t.Name == nil || t.Name.Missing() {
			return nil
		}
		if named := c.s.Type(t.Name.Value); named != nil {
			return schema.Named(named.Kind, named.Name)
		}
	case *ast.ListType:
		if inner := c.typeRef(t.Type); inner != nil {
			return schema.ListOf(inner)
		}
	case *ast.NonNullType:
		if inner := c.typeRef(t.Type); inner != nil {
			return schema.NonNullOf(inner)
		}
	}
	return nil
}

// possibleSet returns the object types a composite type may be at run time.
func (c *completer) possibleSet(name string) map[string]bool {
	t := c.s.Type(name)
	set := map[string]bool{}
	switch {
	case t == nil:
	case t.Kind == schema.KindObject:
		set[name] = true
	default:
		for _, p := range c.s.PossibleTypes(name) {
			set[p.Name] = true
		}
	}
	return set
}

func overlaps(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// variableFits reports whether a variable of type v may be used where loc is
// expected. It follows the spec's AreTypesCompatible, but lets a nullable
// variable into a non-null position (allowed when the variable or the
// location has a default, which completion does not check).
func variableFits(v, loc *schema.TypeRef) bool {
	for v != nil && loc != nil {
		if loc.Kind == schema.KindNonNull {
			loc = loc.OfType
			if v.Kind == schema.KindNonNull {
				v = v.OfType
			}
			continue
		}
		if v.Kind == schema.KindNonNull {
			v = v.OfType
			continue
		}
		if loc.Kind == schema.KindList || v.Kind == schema.KindList {
			if loc.Kind != v.Kind {
				return false
			}
			v, loc = v.OfType, loc.OfType
			continue
		}
		return v.Name == loc.Name
	}
	return false
}

func findInput(vs []*schema.InputValue, name string) *schema.InputValue {
	for _, v := range vs {
		if v.Name == name {
			return v
		}
	}
	return nil
}
