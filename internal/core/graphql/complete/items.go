package complete

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Ranking classes within a context; lower comes first.
const (
	classFirst = iota
	classSecond
	classThird
)

func (c *completer) document() {
	c.res.Context = ContextDocument
	c.add(Item{Label: "query", Kind: KindKeyword, Detail: "operation"}, classFirst)
	if c.anyRoot || c.s.MutationType != "" {
		c.add(Item{Label: "mutation", Kind: KindKeyword, Detail: "operation"}, classFirst)
	}
	if c.anyRoot || c.s.SubscriptionType != "" {
		c.add(Item{Label: "subscription", Kind: KindKeyword, Detail: "operation"}, classFirst)
	}
	c.add(Item{Label: "fragment", Kind: KindKeyword, Detail: "fragment definition"}, classFirst)
	c.add(Item{Label: "{", Kind: KindKeyword, Detail: "query shorthand"}, classSecond)
}

func (c *completer) keywordOn(ctx Context) {
	c.res.Context = ctx
	c.add(Item{Label: "on", Kind: KindKeyword, Detail: "type condition"}, classSecond)
}

// selection suggests the fields of typeName, meta fields included.
func (c *completer) selection(typeName string) {
	c.res.Context = ContextSelection
	c.res.ParentType = typeName
	for _, f := range c.s.FieldsOf(typeName) {
		it := Item{
			Label: f.Name, Kind: KindField, Detail: f.Type.String(), Documentation: f.Description,
			Deprecated: f.IsDeprecated, DeprecationReason: f.DeprecationReason,
		}
		switch {
		case hasRequired(f.Args):
			c.suffix(&it, "()", 1, lexer.ParenL)
		case c.s.TypeOf(f.Type).IsComposite():
			c.suffix(&it, " {  }", 3, lexer.ParenL, lexer.BraceL)
		}
		c.add(it, classFirst)
	}
}

// arguments suggests the arguments of the owner of list that are not
// provided yet; editing is the argument being typed, if any.
func (c *completer) arguments(list *ast.ArgumentList, editing *ast.Argument) {
	c.res.Context = ContextArgument
	c.res.Owner = c.owner
	provided := map[string]bool{}
	for _, a := range list.Arguments {
		if a != editing && a.Name != nil {
			provided[a.Name.Value] = true
		}
	}
	for _, a := range c.args {
		if provided[a.Name] {
			continue
		}
		it := inputItem(a, KindArgument)
		c.suffix(&it, ": ", 2, lexer.Colon)
		class := classSecond
		if isRequired(a) {
			class = classFirst
		}
		c.add(it, class)
	}
}

// objectFields suggests the input fields of the object value's type that
// are not provided yet.
func (c *completer) objectFields(obj *ast.ObjectValue, editing *ast.ObjectField) {
	c.res.Context = ContextObjectField
	if c.inputType == nil {
		return
	}
	c.res.ParentType = c.inputType.Name
	provided := map[string]bool{}
	for _, f := range obj.Fields {
		if f != editing && f.Name != nil {
			provided[f.Name.Value] = true
		}
	}
	for _, f := range c.inputType.InputFields {
		if provided[f.Name] {
			continue
		}
		it := inputItem(f, KindInputField)
		c.suffix(&it, ": ", 2, lexer.Colon)
		class := classSecond
		if isRequired(f) {
			class = classFirst
		}
		c.add(it, class)
	}
}

// values suggests literals of the expected type and the variables that fit.
func (c *completer) values(onlyVariables bool) {
	c.res.Context = ContextValue
	exp := c.expected
	if exp != nil {
		c.res.ExpectedType = exp.String()
	}
	if exp != nil && !onlyVariables {
		t := c.s.TypeOf(exp)
		switch {
		case t == nil:
		case t.Kind == schema.KindEnum:
			for _, v := range t.EnumValues {
				c.add(Item{
					Label: v.Name, Kind: KindEnumValue, Detail: t.Name, Documentation: v.Description,
					Deprecated: v.IsDeprecated, DeprecationReason: v.DeprecationReason,
				}, classFirst)
			}
		case t.Name == "Boolean":
			c.add(Item{Label: "true", Kind: KindKeyword, Detail: "Boolean"}, classFirst)
			c.add(Item{Label: "false", Kind: KindKeyword, Detail: "Boolean"}, classFirst)
		}
		if !exp.IsNonNull() {
			c.add(Item{Label: "null", Kind: KindKeyword, Detail: "null"}, classThird)
		}
	}
	if c.varDef != nil || c.op == nil || c.op.VariableDefinitions == nil {
		return // constant value, or no variables in scope
	}
	seen := map[string]bool{}
	for _, d := range c.op.VariableDefinitions.Definitions {
		if d.Variable == nil || d.Variable.Name == nil || d.Variable.Name.Missing() {
			continue
		}
		name := d.Variable.Name.Value
		if seen[name] {
			continue
		}
		seen[name] = true
		vt := c.typeRef(d.Type)
		if exp != nil && vt != nil && !variableFits(vt, exp) {
			continue
		}
		detail := ""
		if vt != nil {
			detail = vt.String()
		} else if d.Type != nil {
			detail = c.src[d.Type.Pos().Start:d.Type.Pos().End]
		}
		c.addFiltered(Item{Label: "$" + name, Kind: KindVariable, Detail: detail, InsertText: "$" + name}, name, classSecond)
	}
}

// variable handles a cursor after "$" in a value: the range includes "$".
func (c *completer) variable(v *ast.Variable) {
	if v.Dollar >= 0 {
		c.res.From = v.Dollar
	}
	c.values(true)
	c.res.Context = ContextVariable
}

// variableTypes suggests input types.
func (c *completer) variableTypes() {
	c.res.Context = ContextVariableType
	declared := map[string]bool{}
	for _, t := range c.s.Types {
		declared[t.Name] = true
		if t.Kind.IsInput() {
			c.add(typeItem(t), classFirst)
		}
	}
	for _, name := range schema.BuiltinScalarNames {
		if !declared[name] {
			c.add(typeItem(c.s.Type(name)), classFirst)
		}
	}
}

// typeCondition suggests composite types: for an inline fragment those that
// can apply to the parent type (the parent itself, then object types, then
// interfaces and unions), for a fragment definition all of them.
func (c *completer) typeCondition(tc *ast.TypeCondition, inline bool) {
	c.res.Context = ContextTypeCondition
	parent := ""
	if inline {
		parent = c.parentType
		c.res.ParentType = parent
	}
	var parentSet map[string]bool
	if parent != "" {
		parentSet = c.possibleSet(parent)
	}
	for _, t := range c.s.Types {
		if !t.IsComposite() {
			continue
		}
		class := classSecond
		switch {
		case t.Name == parent:
			class = classFirst
		case parentSet != nil && !overlaps(parentSet, c.possibleSet(t.Name)):
			continue
		case t.IsAbstract():
			class = classThird
		}
		c.add(typeItem(t), class)
	}
	if tc.On < 0 {
		c.add(Item{Label: "on", Kind: KindKeyword, Detail: "type condition"}, classFirst)
	}
}

// fragmentSpread suggests the fragments defined in the document whose type
// condition can apply to the parent type, and "on".
func (c *completer) fragmentSpread() {
	c.res.Context = ContextFragmentSpread
	parent := c.parentType
	c.res.ParentType = parent
	var parentSet map[string]bool
	if parent != "" {
		parentSet = c.possibleSet(parent)
	}
	seen := map[string]bool{}
	for _, d := range c.doc.Definitions {
		f, ok := d.(*ast.FragmentDefinition)
		if !ok || f == c.frag || f.Name == nil || f.Name.Missing() || seen[f.Name.Value] {
			continue
		}
		seen[f.Name.Value] = true
		detail := ""
		if tc := f.TypeCondition; tc != nil && tc.Type != nil && tc.Type.Name != nil && !tc.Type.Name.Missing() {
			detail = "on " + tc.Type.Name.Value
			// Unknown types cannot be checked and are offered anyway.
			if cond := c.conditionType(tc); cond != "" && parentSet != nil && !overlaps(parentSet, c.possibleSet(cond)) {
				continue
			}
		}
		c.add(Item{Label: f.Name.Value, Kind: KindFragment, Detail: detail}, classFirst)
	}
	c.add(Item{Label: "on", Kind: KindKeyword, Detail: "inline fragment"}, classSecond)
}

// directives suggests directives allowed on owner, leaving out
// non-repeatable ones already present; editing is the directive being typed.
func (c *completer) directives(owner ast.Node, editing *ast.Directive) {
	c.res.Context = ContextDirective
	var loc schema.DirectiveLocation
	var present []*ast.Directive
	switch o := owner.(type) {
	case *ast.OperationDefinition:
		switch o.Operation {
		case ast.Mutation:
			loc = schema.LocMutation
		case ast.Subscription:
			loc = schema.LocSubscription
		default:
			loc = schema.LocQuery
		}
		present = o.Directives
	case *ast.FragmentDefinition:
		loc, present = schema.LocFragmentDefinition, o.Directives
	case *ast.Field:
		loc, present = schema.LocField, o.Directives
	case *ast.FragmentSpread:
		loc, present = schema.LocFragmentSpread, o.Directives
	case *ast.InlineFragment:
		loc, present = schema.LocInlineFragment, o.Directives
	case *ast.VariableDefinition:
		loc, present = schema.LocVariableDefinition, o.Directives
	default:
		return
	}
	used := map[string]bool{}
	for _, d := range present {
		if d != editing && d.Name != nil {
			used[d.Name.Value] = true
		}
	}
	declared := map[string]bool{}
	all := make([]*schema.Directive, 0, len(c.s.Directives)+5)
	for _, d := range c.s.Directives {
		declared[d.Name] = true
		all = append(all, d)
	}
	for _, d := range schema.Builtins().Directives {
		if !declared[d.Name] {
			all = append(all, d)
		}
	}
	for _, d := range all {
		if !d.HasLocation(loc) || used[d.Name] && !d.IsRepeatable {
			continue
		}
		it := Item{Label: d.Name, Kind: KindDirective, Detail: signature(d.Args), Documentation: d.Description}
		if hasRequired(d.Args) {
			c.suffix(&it, "()", 1, lexer.ParenL)
		}
		c.add(it, classFirst)
	}
}

// suffix sets a snippet hint unless the next token is one of skip.
func (c *completer) suffix(it *Item, s string, cursor int, skip ...lexer.Kind) {
	for _, k := range skip {
		if c.next.Kind == k {
			return
		}
	}
	it.Suffix, it.SuffixCursor = s, cursor
}

func inputItem(v *schema.InputValue, kind ItemKind) Item {
	detail := v.Type.String()
	if v.DefaultValue != nil {
		detail += " = " + *v.DefaultValue
	}
	return Item{
		Label: v.Name, Kind: kind, Detail: detail, Documentation: v.Description,
		Deprecated: v.IsDeprecated, DeprecationReason: v.DeprecationReason,
	}
}

func typeItem(t *schema.Type) Item {
	detail := strings.ToLower(strings.ReplaceAll(string(t.Kind), "_", " "))
	return Item{Label: t.Name, Kind: KindType, Detail: detail, Documentation: t.Description}
}

func signature(args []*schema.InputValue) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('(')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.Name)
		b.WriteString(": ")
		b.WriteString(a.Type.String())
	}
	b.WriteByte(')')
	return b.String()
}

func isRequired(v *schema.InputValue) bool { return v.Type.IsNonNull() && v.DefaultValue == nil }

func hasRequired(vs []*schema.InputValue) bool {
	for _, v := range vs {
		if isRequired(v) {
			return true
		}
	}
	return false
}
