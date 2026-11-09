package schema

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

// CodeSDLSyntax: the SDL document has syntax errors (kind parse). The
// error wraps Problems, one per syntax error with its position, and no
// schema is built.
const CodeSDLSyntax = "graphql.schema.sdl_syntax"

// HintSDLProblems is the hint of a schema built from SDL with problems.
const HintSDLProblems = "The schema file has type system errors. " +
	"The rest of the schema is usable; fix the file and reload it."

// ParseSDL parses a type system document (SDL, GQL-11) and builds a schema
// from it with FromSDL.
//
// A document with syntax errors gives a nil schema and a CodeSDLSyntax
// error wrapping Problems. A document whose type system is invalid gives a
// usable schema and a CodeInvalidSchema error wrapping Problems, like
// ParseIntrospection. Every problem has the position it applies to.
func ParseSDL(src string) (*Schema, error) {
	doc, perrs := parser.ParseSchema(src)
	if len(perrs) > 0 {
		ps := make(Problems, 0, len(perrs))
		for _, e := range perrs {
			pos := doc.Position(e.Start)
			ps = append(ps, Problem{Message: e.Message, Start: e.Start, End: e.End, Line: pos.Line, Column: pos.Column})
		}
		first := ps[0]
		return nil, errs.Wrap(ps, errs.Parse,
			fmt.Sprintf("The schema file has %d syntax error(s); the first, at line %d, column %d: %s.",
				len(ps), first.Line, first.Column, first.Message)).
			WithCode(CodeSDLSyntax).
			WithDetail("problems", strconv.Itoa(len(ps)))
	}
	return FromSDL(doc)
}

// FromSDL builds a schema from a type system document parsed by
// parser.ParseSchema without errors, merging extensions into the types
// they extend, and validates the type system (the spec's type validation
// and the SDL validation rules):
//
//   - names: unique types, directives, fields, arguments, input fields,
//     enum values and root operation types; nothing but introspection
//     starts with "__"; one schema definition;
//   - references: known types; output types for fields, input types for
//     arguments and input fields; extensions of defined types of the same
//     kind;
//   - types: at least one field, member or value; union members that are
//     objects; interfaces that are interfaces, implemented once, with
//     their own interfaces, fields (covariant types) and arguments (equal
//     types, extra ones optional); no circular non-null input fields;
//     @oneOf input fields nullable without defaults; required arguments
//     and input fields not deprecated;
//   - the schema: a query root, roots that are distinct object types;
//   - directives: known, allowed at the location, non-repeatable ones
//     once, known and required arguments.
//
// Without a schema definition the roots are the types named Query,
// Mutation and Subscription. @deprecated, @specifiedBy and @oneOf set
// the model's deprecations, SpecifiedByURL and IsOneOf; default values
// are kept as GraphQL literals. Built-in scalars stay fallbacks: Types
// lists the declared types only.
//
// Problems come as a CodeInvalidSchema error wrapping Problems, with a
// usable schema, as for ParseIntrospection.
func FromSDL(doc *ast.Document) (*Schema, error) {
	b := &sdlBuilder{
		doc:        doc,
		declared:   map[string]*sdlType{},
		directives: map[string]*sdlDirective{},
	}
	s := b.build()
	if len(b.problems) == 0 {
		return s, nil
	}
	slices.SortStableFunc(b.problems, func(x, y Problem) int { return cmp.Compare(x.Start, y.Start) })
	return s, errs.Wrap(b.problems, errs.Parse,
		fmt.Sprintf("The schema has %d problem(s); the first, at line %d, column %d: %s",
			len(b.problems), b.problems[0].Line, b.problems[0].Column, b.problems[0].Message)).
		WithCode(CodeInvalidSchema).
		WithHint(HintSDLProblems).
		WithDetail("problems", strconv.Itoa(len(b.problems)))
}

// sdlType is a type being built with the definitions it came from.
type sdlType struct {
	t    *Type
	def  *ast.TypeDefinition   // the definition
	exts []*ast.TypeDefinition // its extensions, in source order

	fields      map[*Field]*ast.FieldDefinition
	args        map[*InputValue]*ast.InputValueDefinition
	inputFields map[*InputValue]*ast.InputValueDefinition
	ifaceNodes  []*ast.NamedType // parallel to t.Interfaces
	memberNodes []*ast.NamedType // parallel to t.PossibleTypes (unions)
}

type sdlDirective struct {
	d    *Directive
	def  *ast.DirectiveDefinition
	args map[*InputValue]*ast.InputValueDefinition
}

type sdlBuilder struct {
	doc        *ast.Document
	s          *Schema
	declared   map[string]*sdlType
	order      []*sdlType
	directives map[string]*sdlDirective
	problems   Problems
}

func (b *sdlBuilder) add(n ast.Node, format string, args ...any) {
	loc := n.Pos()
	pos := b.doc.Position(loc.Start)
	b.problems = append(b.problems, Problem{
		Message: fmt.Sprintf(format, args...),
		Start:   loc.Start, End: loc.End, Line: pos.Line, Column: pos.Column,
	})
}

func (b *sdlBuilder) build() *Schema {
	b.s = &Schema{}
	var schemaDefs []*ast.SchemaDefinition
	var exts []*ast.TypeDefinition
	var dirDefs []*ast.DirectiveDefinition

	// Declarations first, so references can be resolved in any order.
	for _, def := range b.doc.Definitions {
		switch d := def.(type) {
		case *ast.SchemaDefinition:
			schemaDefs = append(schemaDefs, d)
		case *ast.TypeDefinition:
			if d.IsExtension() {
				exts = append(exts, d)
				continue
			}
			b.declare(d)
		case *ast.DirectiveDefinition:
			dirDefs = append(dirDefs, d)
		}
	}
	for _, d := range exts {
		b.extend(d)
	}
	for _, st := range b.order {
		b.members(st)
	}
	for _, d := range dirDefs {
		b.directive(d)
	}
	b.schemaDefinition(schemaDefs)

	for _, st := range b.order {
		b.s.Types = append(b.s.Types, st.t)
	}
	b.s.index()
	for _, st := range b.order {
		if st.t.Kind == KindInterface {
			for _, impl := range b.s.implementations[st.t.Name] {
				if impl.Kind == KindObject {
					st.t.PossibleTypes = append(st.t.PossibleTypes, impl.Name)
				}
			}
		}
	}

	for _, st := range b.order {
		b.validateType(st)
	}
	b.validateInputCycles()
	b.validateDirectiveUses(schemaDefs)
	return b.s
}

func reserved(name string) bool { return strings.HasPrefix(name, "__") }

func (b *sdlBuilder) checkName(n *ast.Name) {
	if reserved(n.Value) {
		b.add(n, "Name %q must not begin with \"__\", which is reserved by GraphQL introspection.", n.Value)
	}
}

var kindOfDefinition = [...]Kind{
	ast.KindScalar:      KindScalar,
	ast.KindObject:      KindObject,
	ast.KindInterface:   KindInterface,
	ast.KindUnion:       KindUnion,
	ast.KindEnum:        KindEnum,
	ast.KindInputObject: KindInputObject,
}

func (b *sdlBuilder) declare(d *ast.TypeDefinition) {
	if d.Name == nil || d.Name.Missing() {
		return
	}
	name := d.Name.Value
	if _, dup := b.declared[name]; dup {
		b.add(d.Name, "There can be only one type named %q.", name)
		return
	}
	b.checkName(d.Name)
	kind := kindOfDefinition[d.Kind]
	if bt := builtins.types[name]; bt != nil && !reserved(name) && bt.Kind != kind {
		b.add(d.Name, "Type %q is a built-in scalar and cannot be redefined as %s.", name, kindArticle(kind))
	}
	st := &sdlType{
		t:           &Type{Kind: kind, Name: name, Description: description(d.Description)},
		def:         d,
		fields:      map[*Field]*ast.FieldDefinition{},
		args:        map[*InputValue]*ast.InputValueDefinition{},
		inputFields: map[*InputValue]*ast.InputValueDefinition{},
	}
	b.declared[name] = st
	b.order = append(b.order, st)
}

func kindArticle(k Kind) string {
	switch k {
	case KindObject:
		return "an object type"
	case KindInterface:
		return "an interface"
	case KindUnion:
		return "a union"
	case KindEnum:
		return "an enum"
	case KindInputObject:
		return "an input object"
	}
	return "a scalar"
}

var extensionKindNames = [...]string{
	ast.KindScalar:      "scalar",
	ast.KindObject:      "object",
	ast.KindInterface:   "interface",
	ast.KindUnion:       "union",
	ast.KindEnum:        "enum",
	ast.KindInputObject: "input object",
}

func (b *sdlBuilder) extend(d *ast.TypeDefinition) {
	if d.Name == nil || d.Name.Missing() {
		return
	}
	st := b.declared[d.Name.Value]
	switch {
	case st == nil:
		b.add(d.Name, "Cannot extend type %q because it is not defined.", d.Name.Value)
	case st.t.Kind != kindOfDefinition[d.Kind]:
		b.add(d.Name, "Cannot extend non-%s type %q.", extensionKindNames[d.Kind], d.Name.Value)
	default:
		st.exts = append(st.exts, d)
	}
}

// members fills a type's fields, interfaces, members, values and input
// fields from its definition and extensions.
func (b *sdlBuilder) members(st *sdlType) {
	t := st.t
	for _, d := range append([]*ast.TypeDefinition{st.def}, st.exts...) {
		for _, dir := range d.Directives {
			switch dir.Name.Value {
			case "specifiedBy":
				if v, ok := argString(dir, "url"); ok {
					t.SpecifiedByURL = v
				}
			case "oneOf":
				t.IsOneOf = t.Kind == KindInputObject
			}
		}
		for _, n := range d.Interfaces {
			if n.Name.Missing() {
				continue
			}
			b.ref(n) // reports an unknown type
			t.Interfaces = append(t.Interfaces, n.Name.Value)
			st.ifaceNodes = append(st.ifaceNodes, n)
		}
		for _, n := range d.Members {
			if n.Name.Missing() {
				continue
			}
			b.ref(n)
			t.PossibleTypes = append(t.PossibleTypes, n.Name.Value)
			st.memberNodes = append(st.memberNodes, n)
		}
		for _, fd := range d.Fields {
			if fd.Name.Missing() {
				continue
			}
			path := t.Name + "." + fd.Name.Value
			if t.Field(fd.Name.Value) != nil {
				b.add(fd.Name, "Field %q can only be defined once.", path)
				continue
			}
			b.checkName(fd.Name)
			f := &Field{
				Name:        fd.Name.Value,
				Description: description(fd.Description),
				Type:        b.ref(fd.Type),
				Deprecation: deprecation(fd.Directives),
			}
			if named := f.Type.NamedType(); named != nil && named.Kind != "" && !named.Kind.IsOutput() {
				b.add(fd.Type, "The type of %s must be Output Type but got: %s.", path, f.Type)
			}
			if fd.Arguments != nil {
				f.Args = b.inputValues(path, fd.Arguments.Arguments, st.args)
			}
			t.Fields = append(t.Fields, f)
			st.fields[f] = fd
		}
		for _, vd := range d.EnumValues {
			if vd.Name.Missing() {
				continue
			}
			if t.EnumValue(vd.Name.Value) != nil {
				b.add(vd.Name, "Enum value %q can only be defined once.", t.Name+"."+vd.Name.Value)
				continue
			}
			b.checkName(vd.Name)
			t.EnumValues = append(t.EnumValues, &EnumValue{
				Name:        vd.Name.Value,
				Description: description(vd.Description),
				Deprecation: deprecation(vd.Directives),
			})
		}
		for _, id := range d.InputFields {
			if id.Name.Missing() {
				continue
			}
			if t.InputField(id.Name.Value) != nil {
				b.add(id.Name, "Field %q can only be defined once.", t.Name+"."+id.Name.Value)
				continue
			}
			v := b.inputValue(t.Name+"."+id.Name.Value, id)
			t.InputFields = append(t.InputFields, v)
			st.inputFields[v] = id
		}
	}
}

// inputValues converts argument definitions; owner is "Type.field" or
// "@directive".
func (b *sdlBuilder) inputValues(owner string, defs []*ast.InputValueDefinition, nodes map[*InputValue]*ast.InputValueDefinition) []*InputValue {
	var out []*InputValue
	for _, d := range defs {
		if d.Name.Missing() {
			continue
		}
		path := owner + "(" + d.Name.Value + ":)"
		if findInput(out, d.Name.Value) != nil {
			b.add(d.Name, "Argument %q can only be defined once.", path)
			continue
		}
		v := b.inputValue(path, d)
		out = append(out, v)
		nodes[v] = d
	}
	return out
}

func (b *sdlBuilder) inputValue(path string, d *ast.InputValueDefinition) *InputValue {
	b.checkName(d.Name)
	v := &InputValue{
		Name:        d.Name.Value,
		Description: description(d.Description),
		Type:        b.ref(d.Type),
		Deprecation: deprecation(d.Directives),
	}
	if named := v.Type.NamedType(); named != nil && named.Kind != "" && !named.Kind.IsInput() {
		b.add(d.Type, "The type of %s must be Input Type but got: %s.", path, v.Type)
	}
	if d.DefaultValue != nil {
		lit := literal(d.DefaultValue)
		v.DefaultValue = &lit
	}
	return v
}

// ref converts a type reference, reporting an unknown named type. The
// named type's Kind is "" when it is unknown.
func (b *sdlBuilder) ref(t ast.Type) *TypeRef {
	switch t := t.(type) {
	case *ast.ListType:
		return ListOf(b.ref(t.Type))
	case *ast.NonNullType:
		return NonNullOf(b.ref(t.Type))
	case *ast.NamedType:
		name := t.Name.Value
		if st := b.declared[name]; st != nil {
			return Named(st.t.Kind, name)
		}
		if bt := builtins.types[name]; bt != nil {
			return Named(bt.Kind, name)
		}
		if name != "" {
			b.add(t, "Unknown type %q.", name)
		}
		return &TypeRef{Name: name}
	}
	return &TypeRef{}
}

func (b *sdlBuilder) directive(d *ast.DirectiveDefinition) {
	if d.Name.Missing() {
		return
	}
	name := d.Name.Value
	if _, dup := b.directives[name]; dup {
		b.add(d.Name, "There can be only one directive named %q.", "@"+name)
		return
	}
	b.checkName(d.Name)
	sd := &sdlDirective{
		d: &Directive{
			Name:         name,
			Description:  description(d.Description),
			IsRepeatable: d.Repeatable >= 0,
		},
		def:  d,
		args: map[*InputValue]*ast.InputValueDefinition{},
	}
	if d.Arguments != nil {
		sd.d.Args = b.inputValues("@"+name, d.Arguments.Arguments, sd.args)
	}
	for _, l := range d.Locations {
		if !l.Missing() {
			sd.d.Locations = append(sd.d.Locations, DirectiveLocation(l.Value))
		}
	}
	for v, vd := range sd.args {
		if v.Type.IsNonNull() && v.DefaultValue == nil && v.IsDeprecated {
			b.add(vd.Name, "Required argument %s cannot be deprecated.", "@"+name+"("+v.Name+":)")
		}
	}
	b.directives[name] = sd
	b.s.Directives = append(b.s.Directives, sd.d)
}

// schemaDefinition sets the description and the root types.
func (b *sdlBuilder) schemaDefinition(defs []*ast.SchemaDefinition) {
	seenDef := false
	roots := map[ast.OperationType]string{}
	var rootNodes []*ast.RootOperationTypeDefinition
	for _, d := range defs {
		if !d.IsExtension() {
			if seenDef {
				b.add(d, "Must provide only one schema definition.")
				continue
			}
			seenDef = true
			b.s.Description = description(d.Description)
		}
		for _, o := range d.OperationTypes {
			if _, dup := roots[o.Operation]; dup {
				b.add(o, "There can be only one %s type in schema.", o.Operation)
				continue
			}
			if o.Type == nil || o.Type.Name.Missing() {
				continue
			}
			roots[o.Operation] = o.Type.Name.Value
			rootNodes = append(rootNodes, o)
		}
	}
	if len(rootNodes) == 0 && !seenDef {
		for op, name := range map[ast.OperationType]string{ast.Query: "Query", ast.Mutation: "Mutation", ast.Subscription: "Subscription"} {
			if b.declared[name] != nil {
				roots[op] = name
			}
		}
	}
	b.s.QueryType, b.s.MutationType, b.s.SubscriptionType = roots[ast.Query], roots[ast.Mutation], roots[ast.Subscription]

	var at ast.Node = &ast.Name{Loc: ast.Loc{Start: 0, End: 0}}
	if len(defs) > 0 {
		at = defs[0]
	}
	if b.s.QueryType == "" {
		b.add(at, "Query root type must be provided.")
	}
	usedBy := map[string][]string{}
	for _, o := range rootNodes {
		name := o.Type.Name.Value
		usedBy[name] = append(usedBy[name], o.Operation.String())
		st := b.declared[name]
		switch {
		case st == nil:
			if builtins.types[name] != nil {
				b.add(o.Type, "%s root type must be Object type, it cannot be %s.", capitalize(o.Operation.String()), name)
			} else {
				b.add(o.Type, "Unknown type %q.", name)
			}
		case st.t.Kind != KindObject:
			b.add(o.Type, "%s root type must be Object type, it cannot be %s.", capitalize(o.Operation.String()), name)
		}
	}
	for _, o := range rootNodes {
		name := o.Type.Name.Value
		if ops := usedBy[name]; len(ops) > 1 && ops[0] == o.Operation.String() {
			b.add(o.Type, "All root types must be different, %q type is used as %s root types.", name, joinAnd(ops))
		}
	}
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func joinAnd(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// ---- type validation ----

func (b *sdlBuilder) validateType(st *sdlType) {
	t := st.t
	name := st.def.Name
	switch t.Kind {
	case KindObject, KindInterface:
		if len(t.Fields) == 0 {
			b.add(name, "Type %s must define one or more fields.", t.Name)
		}
		for _, f := range t.Fields {
			for _, a := range f.Args {
				if a.Type.IsNonNull() && a.DefaultValue == nil && a.IsDeprecated {
					b.add(st.args[a].Name, "Required argument %s.%s(%s:) cannot be deprecated.", t.Name, f.Name, a.Name)
				}
			}
		}
		b.validateInterfaces(st)
	case KindUnion:
		if len(t.PossibleTypes) == 0 {
			b.add(name, "Union type %s must define one or more member types.", t.Name)
		}
		seen := map[string]bool{}
		for i, m := range t.PossibleTypes {
			node := st.memberNodes[i]
			if seen[m] {
				b.add(node, "Union type %s can only include type %s once.", t.Name, m)
				continue
			}
			seen[m] = true
			if mt := b.s.Type(m); mt != nil && mt.Kind != KindObject {
				b.add(node, "Union type %s can only include Object types, it cannot include %s.", t.Name, m)
			}
		}
	case KindEnum:
		if len(t.EnumValues) == 0 {
			b.add(name, "Enum type %s must define one or more values.", t.Name)
		}
	case KindInputObject:
		if len(t.InputFields) == 0 {
			b.add(name, "Input Object type %s must define one or more fields.", t.Name)
		}
		for _, f := range t.InputFields {
			node := st.inputFields[f]
			if f.Type.IsNonNull() && f.DefaultValue == nil && f.IsDeprecated {
				b.add(node.Name, "Required input field %s.%s cannot be deprecated.", t.Name, f.Name)
			}
			if t.IsOneOf {
				if f.Type.IsNonNull() {
					b.add(node.Type, "OneOf input field %s.%s must be nullable.", t.Name, f.Name)
				}
				if f.DefaultValue != nil {
					b.add(node.Name, "OneOf input field %s.%s cannot have a default value.", t.Name, f.Name)
				}
			}
		}
	}
}

func (b *sdlBuilder) validateInterfaces(st *sdlType) {
	t := st.t
	seen := map[string]bool{}
	for i, iname := range t.Interfaces {
		node := st.ifaceNodes[i]
		iface := b.s.Type(iname)
		switch {
		case iface == nil:
			continue // reported as an unknown type
		case iname == t.Name:
			b.add(node, "Type %s cannot implement itself because it would create a circular reference.", t.Name)
			continue
		case iface.Kind != KindInterface:
			b.add(node, "Type %s must only implement Interface types, it cannot implement %s.", t.Name, iname)
			continue
		case seen[iname]:
			b.add(node, "Type %s can only implement %s once.", t.Name, iname)
			continue
		}
		seen[iname] = true
		for _, anc := range iface.Interfaces {
			if !t.Implements(anc) {
				if anc == t.Name {
					b.add(node, "Type %s cannot implement %s because it would create a circular reference.", t.Name, iname)
				} else {
					b.add(node, "Type %s must implement %s because it is implemented by %s.", t.Name, anc, iname)
				}
			}
		}
		b.validateImplementation(st, iface, node)
	}
}

// validateImplementation checks that st provides the fields and arguments
// of iface.
func (b *sdlBuilder) validateImplementation(st *sdlType, iface *Type, node *ast.NamedType) {
	t := st.t
	for _, ifield := range iface.Fields {
		f := t.Field(ifield.Name)
		if f == nil {
			b.add(node, "Interface field %s.%s expected but %s does not provide it.", iface.Name, ifield.Name, t.Name)
			continue
		}
		fd := st.fields[f]
		if !b.isTypeSubTypeOf(f.Type, ifield.Type) {
			b.add(fd.Type, "Interface field %s.%s expects type %s but %s.%s is type %s.",
				iface.Name, ifield.Name, ifield.Type, t.Name, f.Name, f.Type)
		}
		for _, iarg := range ifield.Args {
			a := f.Arg(iarg.Name)
			switch {
			case a == nil:
				b.add(fd.Name, "Interface field argument %s.%s(%s:) expected but %s.%s does not provide it.",
					iface.Name, ifield.Name, iarg.Name, t.Name, f.Name)
			case !a.Type.Equal(iarg.Type):
				b.add(st.args[a].Type, "Interface field argument %s.%s(%s:) expects type %s but %s.%s(%s:) is type %s.",
					iface.Name, ifield.Name, iarg.Name, iarg.Type, t.Name, f.Name, a.Name, a.Type)
			}
		}
		for _, a := range f.Args {
			if ifield.Arg(a.Name) == nil && a.Type.IsNonNull() && a.DefaultValue == nil {
				b.add(st.args[a].Name, "Argument %s.%s(%s:) must not be required type %s if not provided by the Interface field %s.%s.",
					t.Name, f.Name, a.Name, a.Type, iface.Name, ifield.Name)
			}
		}
	}
}

// isTypeSubTypeOf reports whether a field of type sub may implement a
// field of type super (covariance).
func (b *sdlBuilder) isTypeSubTypeOf(sub, super *TypeRef) bool {
	switch {
	case sub == nil || super == nil:
		return true // an unknown type, reported elsewhere
	case super.Kind == KindNonNull:
		return sub.Kind == KindNonNull && b.isTypeSubTypeOf(sub.OfType, super.OfType)
	case sub.Kind == KindNonNull:
		return b.isTypeSubTypeOf(sub.OfType, super)
	case super.Kind == KindList:
		return sub.Kind == KindList && b.isTypeSubTypeOf(sub.OfType, super.OfType)
	case sub.Kind == KindList:
		return false
	case sub.Name == super.Name:
		return true
	case super.Kind == "" || sub.Kind == "":
		return true // unknown
	}
	return (super.Kind == KindInterface || super.Kind == KindUnion) &&
		(sub.Kind == KindObject || sub.Kind == KindInterface) && b.s.IsSubType(super.Name, sub.Name)
}

// validateInputCycles reports input objects that reference themselves
// through non-null, non-list fields: no finite value would satisfy them.
func (b *sdlBuilder) validateInputCycles() {
	visited := map[string]bool{}
	for _, st := range b.order {
		if st.t.Kind != KindInputObject || visited[st.t.Name] {
			continue
		}
		var path []*InputValue
		onPath := map[string]int{}
		var walk func(st *sdlType)
		walk = func(st *sdlType) {
			if visited[st.t.Name] {
				return
			}
			visited[st.t.Name] = true
			onPath[st.t.Name] = len(path)
			for _, f := range st.t.InputFields {
				if !f.Type.IsNonNull() || f.Type.OfType.Kind != KindInputObject {
					continue
				}
				next := b.declared[f.Type.OfType.Name]
				if next == nil {
					continue
				}
				path = append(path, f)
				if i, ok := onPath[next.t.Name]; ok {
					names := make([]string, 0, len(path)-i)
					for _, p := range path[i:] {
						names = append(names, p.Name)
					}
					b.add(next.def.Name, "Cannot reference Input Object %q within itself through a series of non-null fields: %q.",
						next.t.Name, strings.Join(names, "."))
				} else {
					walk(next)
				}
				path = path[:len(path)-1]
			}
			delete(onPath, st.t.Name)
		}
		walk(st)
	}
}

// ---- directive uses ----

func (b *sdlBuilder) validateDirectiveUses(schemaDefs []*ast.SchemaDefinition) {
	var schemaUses []*ast.Directive
	for _, d := range schemaDefs {
		schemaUses = append(schemaUses, d.Directives...)
	}
	b.uses(schemaUses, LocSchema)
	typeLocs := [...]DirectiveLocation{
		ast.KindScalar: LocScalar, ast.KindObject: LocObject, ast.KindInterface: LocInterface,
		ast.KindUnion: LocUnion, ast.KindEnum: LocEnum, ast.KindInputObject: LocInputObject,
	}
	for _, st := range b.order {
		var uses []*ast.Directive
		for _, d := range append([]*ast.TypeDefinition{st.def}, st.exts...) {
			uses = append(uses, d.Directives...)
			for _, f := range d.Fields {
				b.uses(f.Directives, LocFieldDefinition)
				if f.Arguments != nil {
					for _, a := range f.Arguments.Arguments {
						b.uses(a.Directives, LocArgumentDefinition)
					}
				}
			}
			for _, v := range d.EnumValues {
				b.uses(v.Directives, LocEnumValue)
			}
			for _, f := range d.InputFields {
				b.uses(f.Directives, LocInputFieldDefinition)
			}
		}
		b.uses(uses, typeLocs[st.def.Kind])
	}
	for _, def := range b.doc.Definitions {
		if d, ok := def.(*ast.DirectiveDefinition); ok && d.Arguments != nil {
			for _, a := range d.Arguments.Arguments {
				b.uses(a.Directives, LocArgumentDefinition)
			}
		}
	}
}

// uses checks the directives applied at one location.
func (b *sdlBuilder) uses(ds []*ast.Directive, loc DirectiveLocation) {
	seen := map[string]bool{}
	for _, d := range ds {
		if d.Name.Missing() {
			continue
		}
		name := d.Name.Value
		def := b.s.Directive(name)
		if def == nil {
			b.add(d, "Unknown directive %q.", "@"+name)
			continue
		}
		if !def.HasLocation(loc) {
			b.add(d, "Directive %q may not be used on %s.", "@"+name, loc)
		}
		if seen[name] && !def.IsRepeatable {
			b.add(d, "The directive %q can only be used once at this location.", "@"+name)
		}
		seen[name] = true
		given := map[string]bool{}
		if d.Arguments != nil {
			for _, a := range d.Arguments.Arguments {
				switch {
				case given[a.Name.Value]:
					b.add(a.Name, "There can be only one argument named %q.", a.Name.Value)
				case def.Arg(a.Name.Value) == nil:
					b.add(a.Name, "Unknown argument %q on directive %q.", a.Name.Value, "@"+name)
				}
				given[a.Name.Value] = true
			}
		}
		for _, arg := range def.Args {
			if arg.Type.IsNonNull() && arg.DefaultValue == nil && !given[arg.Name] {
				b.add(d, "Argument %q of type %q is required, but it was not provided.",
					"@"+name+"("+arg.Name+":)", arg.Type.String())
			}
		}
	}
}

// ---- values ----

func description(s *ast.StringValue) string {
	if s == nil {
		return ""
	}
	return s.Value
}

// deprecation reads @deprecated(reason:) among ds.
func deprecation(ds []*ast.Directive) Deprecation {
	for _, d := range ds {
		if d.Name.Value != "deprecated" {
			continue
		}
		reason, ok := argString(d, "reason")
		if !ok {
			reason = DefaultDeprecationReason
		}
		return Deprecation{IsDeprecated: true, DeprecationReason: reason}
	}
	return Deprecation{}
}

// argString returns the string argument name of d.
func argString(d *ast.Directive, name string) (string, bool) {
	if d.Arguments == nil {
		return "", false
	}
	for _, a := range d.Arguments.Arguments {
		if a.Name.Value == name {
			if s, ok := a.Value.(*ast.StringValue); ok {
				return s.Value, true
			}
		}
	}
	return "", false
}

// literal prints a constant value as a GraphQL literal in the style of
// graphql-js's print: strings as quoted strings (block strings too),
// "{a: 1, b: [1, 2]}".
func literal(v ast.Value) string {
	var b strings.Builder
	writeLiteral(&b, v)
	return b.String()
}

func writeLiteral(b *strings.Builder, v ast.Value) {
	switch v := v.(type) {
	case *ast.IntValue:
		b.WriteString(v.Raw)
	case *ast.FloatValue:
		b.WriteString(v.Raw)
	case *ast.StringValue:
		b.WriteString(quoteString(v.Value))
	case *ast.BooleanValue:
		b.WriteString(strconv.FormatBool(v.Value))
	case *ast.NullValue:
		b.WriteString("null")
	case *ast.EnumValue:
		b.WriteString(v.Value)
	case *ast.Variable:
		b.WriteString("$" + v.Name.Value)
	case *ast.ListValue:
		b.WriteByte('[')
		for i, e := range v.Values {
			if i > 0 {
				b.WriteString(", ")
			}
			writeLiteral(b, e)
		}
		b.WriteByte(']')
	case *ast.ObjectValue:
		b.WriteByte('{')
		for i, f := range v.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f.Name.Value)
			b.WriteString(": ")
			writeLiteral(b, f.Value)
		}
		b.WriteByte('}')
	}
}

// quoteString quotes s as a GraphQL string: ", \ and control characters
// escaped, everything else as is.
func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
