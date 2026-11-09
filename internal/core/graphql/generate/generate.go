// Package generate writes GraphQL documents from a schema: a whole
// operation or fragment for a field or type of the docs explorer (GQL-16)
// and the fields to fill in at a cursor in a selection set (GQL-17). It
// also finds what the name at a cursor refers to, for the editor's jump to
// the docs (GQL-15, target.go).
//
// Selections go Depth selection sets deep. Leaf fields (scalars and enums)
// come first when the size budget runs short; a composite field is only
// selected when something fits inside it. Recursion is cut: a field whose
// type is already selected on the way down (User.friends inside User) is
// left out, so cycles never repeat. Abstract types select __typename, the
// interface's own fields, and an inline fragment per possible type with the
// fields it adds; fields that would conflict between fragments (same
// response name, other type or arguments) get an alias. Deprecated fields
// are left out.
//
// Arguments: in an operation, each required argument (non-null, no
// default) becomes a variable of the operation, named after the argument
// (and the field when the name is taken). Fragments and filled fields cannot
// declare variables, so they leave out fields with required arguments.
// Optional arguments are never written.
//
// Every document written parses and validates against the schema it was
// written for (see the tests on a generated schema of several hundred types
// with cycles). See docs/graphql-engine.md.
package generate

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/printer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Depth bounds: the default and the most selection sets written below the
// field or type an action starts from.
const (
	DefaultDepth = 2
	MaxDepth     = 5
)

// MaxFields bounds the fields (and inline fragments) one action writes, so
// a wide schema at a large depth stays a usable document. Result.Truncated
// says when it cut the selection.
const MaxFields = 1500

// Options tune the selection.
type Options struct {
	// Depth is how many selection sets deep the selection goes: 1 selects
	// only the leaf fields of the starting type, 2 also the leaf fields of
	// its object fields, and so on. Values below 1 mean DefaultDepth; above
	// MaxDepth, MaxDepth.
	Depth int
}

func (o Options) depth() int {
	switch {
	case o.Depth < 1:
		return DefaultDepth
	case o.Depth > MaxDepth:
		return MaxDepth
	}
	return o.Depth
}

// Reason says why an action wrote nothing.
type Reason string

// Reasons.
const (
	// ReasonUnknownType: the type (or root type) is not in the schema, or
	// has no selection set.
	ReasonUnknownType Reason = "unknown-type"
	// ReasonUnknownField: the field is not a field of the type.
	ReasonUnknownField Reason = "unknown-field"
	// ReasonRequiredArguments: a fragment for a field with required
	// arguments, which a fragment cannot declare as variables.
	ReasonRequiredArguments Reason = "required-arguments"
	// ReasonNotInSelection: the cursor is not in a selection set where a
	// field can go (inside a name, arguments, a string or a comment, or
	// outside any braces).
	ReasonNotInSelection Reason = "not-in-selection"
	// ReasonNothingToAdd: every field that could be filled in is selected.
	ReasonNothingToAdd Reason = "nothing-to-add"
)

// Result is a written operation or fragment.
type Result struct {
	// Text is the definition, formatted like printer.Prettify, without a
	// trailing newline. Empty when Reason is set.
	Text string
	// Name is the operation or fragment name.
	Name string
	// Truncated: MaxFields cut the selection.
	Truncated bool
	Reason    Reason
}

// Taken reports whether an operation or fragment name is already used in
// the document the result goes into; nil means none is.
type Taken func(name string) bool

// Operation writes an operation of type op selecting the root field field,
// with its required arguments as variables:
//
//	query User($id: ID!) {
//	  user(id: $id) {
//	    id
//	    name
//	  }
//	}
//
// The operation is named after the field, with a number added while taken
// says the name is used.
func Operation(s *schema.Schema, op schema.Operation, field string, opts Options, taken Taken) Result {
	root := s.RootType(op)
	if root == nil {
		return Result{Reason: ReasonUnknownType}
	}
	f := root.Field(field)
	if f == nil {
		return Result{Reason: ReasonUnknownField}
	}
	g := newGen(s, opts, true)
	sel, ok := g.field(f, g.opts.depth()+1, []string{root.Name}, nil, true)
	if !ok {
		// Only when the field's type is unknown: a composite type always
		// selects at least __typename here.
		return Result{Reason: ReasonUnknownType}
	}
	name := unique(upperFirst(f.Name), taken)
	var b strings.Builder
	b.WriteString(string(op))
	b.WriteString(" ")
	b.WriteString(name)
	if len(g.vars.defs) > 0 {
		b.WriteString("(")
		b.WriteString(strings.Join(g.vars.defs, ", "))
		b.WriteString(")")
	}
	b.WriteString(" {\n")
	writeNodes(&b, []node{sel}, printer.Indent)
	b.WriteString("}")
	return g.result(b.String(), name)
}

// Fragment writes a fragment on the composite type typeName. With field
// "", it selects the type's fields ("fragment UserFields on User"); with a
// field, only that field ("fragment UserPosts on User { posts { … } }").
// A field with required arguments gives ReasonRequiredArguments.
func Fragment(s *schema.Schema, typeName, field string, opts Options, taken Taken) Result {
	t := s.Type(typeName)
	if !t.IsComposite() {
		return Result{Reason: ReasonUnknownType}
	}
	g := newGen(s, opts, false)
	var nodes []node
	var name string
	if field == "" {
		nodes = g.selection(t, g.opts.depth(), []string{t.Name}, nil)
		if len(nodes) == 0 {
			nodes = []node{{text: schema.TypenameField.Name}}
		}
		name = t.Name + "Fields"
	} else {
		f := t.Field(field)
		if f == nil {
			return Result{Reason: ReasonUnknownField}
		}
		if hasRequired(f.Args) {
			return Result{Reason: ReasonRequiredArguments}
		}
		sel, ok := g.field(f, g.opts.depth()+1, []string{t.Name}, nil, true)
		if !ok {
			return Result{Reason: ReasonUnknownType}
		}
		nodes = []node{sel}
		name = t.Name + upperFirst(f.Name)
	}
	name = unique(name, taken)
	var b strings.Builder
	b.WriteString("fragment ")
	b.WriteString(name)
	b.WriteString(" on ")
	b.WriteString(t.Name)
	b.WriteString(" {\n")
	writeNodes(&b, nodes, printer.Indent)
	b.WriteString("}")
	return g.result(b.String(), name)
}

// result formats the written text with the printer, so long variable
// lists break like Prettify's.
func (g *gen) result(text, name string) Result {
	if out, errs := printer.Prettify(text); len(errs) == 0 {
		text = strings.TrimRight(out, "\n")
	}
	return Result{Text: text, Name: name, Truncated: g.truncated}
}

// node is a selection: a field (text with its alias and arguments) or an
// inline fragment ("... on T"), with its selection set.
type node struct {
	text     string
	children []node
}

func writeNodes(b *strings.Builder, nodes []node, indent string) {
	for _, n := range nodes {
		b.WriteString(indent)
		b.WriteString(n.text)
		if len(n.children) > 0 {
			b.WriteString(" {\n")
			writeNodes(b, n.children, indent+printer.Indent)
			b.WriteString(indent)
			b.WriteString("}")
		}
		b.WriteString("\n")
	}
}

type gen struct {
	s         *schema.Schema
	opts      Options
	budget    int
	truncated bool
	// vars collects the operation's variables; nil when none can be
	// declared (fragments, filled fields).
	vars *varSet
}

func newGen(s *schema.Schema, opts Options, variables bool) *gen {
	g := &gen{s: s, opts: opts, budget: MaxFields}
	if variables {
		g.vars = &varSet{names: map[string]bool{}}
	}
	return g
}

// take spends one unit of the budget, or reports that it is spent.
func (g *gen) take() bool {
	if g.budget <= 0 {
		g.truncated = true
		return false
	}
	g.budget--
	return true
}

// responses tracks the response names of one selection set (across its
// inline fragments), so fields that would not merge get aliases.
type responses map[string]string // response name -> field signature

// field selects f with depth selection sets left for its own (depth 1: a
// leaf only). It reports false when f cannot be selected: its type is
// unknown, it is composite and nothing fits inside (depth or budget), or it
// has required arguments that cannot be variables. path lists the types
// selected on the way down; seen the response names of the enclosing set
// (nil: no aliasing needed). The start field (the one an operation or a
// field fragment is for) is never cut for recursion and selects at least
// __typename.
func (g *gen) field(f *schema.Field, depth int, path []string, seen responses, start bool) (node, bool) {
	t := g.s.TypeOf(f.Type)
	if t == nil || (!t.IsLeaf() && !t.IsComposite()) {
		return node{}, false
	}
	if hasRequired(f.Args) && g.vars == nil {
		return node{}, false
	}
	if t.IsComposite() && !start && (depth <= 1 || contains(path, t.Name)) {
		return node{}, false
	}
	if !g.take() {
		return node{}, false
	}
	var args string
	if start {
		args = g.arguments(f) // the operation's first variables
	}
	var children []node
	if t.IsComposite() {
		children = g.selection(t, depth-1, append(path[:len(path):len(path)], t.Name), nil)
		if len(children) == 0 {
			if !start {
				g.budget++ // given back: the field is left out
				return node{}, false
			}
			children = []node{{text: schema.TypenameField.Name}}
		}
	}
	if !start {
		args = g.arguments(f) // only once the field is kept
	}
	text := f.Name + args
	if seen != nil {
		sig := f.Type.String() + args
		resp := f.Name
		if prev, ok := seen[resp]; ok && prev != sig {
			resp = f.Name + upperFirst(path[len(path)-1])
			for i := 2; seen[resp] != "" && seen[resp] != sig; i++ {
				resp = f.Name + upperFirst(path[len(path)-1]) + strconv.Itoa(i)
			}
			text = resp + ": " + text
		}
		seen[resp] = sig
	}
	return node{text: text, children: children}, true
}

// arguments writes f's required arguments as variables: "(id: $id)".
func (g *gen) arguments(f *schema.Field) string {
	if g.vars == nil {
		return ""
	}
	var parts []string
	for _, a := range f.Args {
		if !isRequired(a) {
			continue
		}
		parts = append(parts, a.Name+": $"+g.vars.add(f.Name, a))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// selection selects the fields of the composite type t with depth
// selection sets (t's own included), leaving out skip's field names and
// inline fragment types. It may be empty.
func (g *gen) selection(t *schema.Type, depth int, path []string, skip map[string]bool) []node {
	if depth < 1 {
		return nil
	}
	seen := responses{}
	var out []node
	if t.IsAbstract() && !skip[schema.TypenameField.Name] && g.take() {
		out = append(out, node{text: schema.TypenameField.Name})
		seen[schema.TypenameField.Name] = schema.TypenameField.Type.String()
	}
	own := map[string]bool{}
	if t.Kind != schema.KindUnion {
		out = append(out, g.fields(t.Fields, depth, path, skip, seen)...)
		for _, f := range t.Fields {
			own[f.Name] = true
		}
	}
	if !t.IsAbstract() {
		return out
	}
	// The possible types' own fields, in an inline fragment each.
	for _, p := range g.s.PossibleTypes(t.Name) {
		if skip["... on "+p.Name] || contains(path, p.Name) {
			continue
		}
		var extra []*schema.Field
		for _, f := range p.Fields {
			if !own[f.Name] {
				extra = append(extra, f)
			}
		}
		if len(extra) == 0 || !g.take() {
			continue
		}
		inner := g.fields(extra, depth, append(path[:len(path):len(path)], p.Name), skip, seen)
		if len(inner) == 0 {
			g.budget++
			continue
		}
		out = append(out, node{text: "... on " + p.Name, children: inner})
	}
	return out
}

// fields selects the fields fs of one type: the leaf fields first (they
// get the budget before any composite field), then the composite ones,
// written in the schema's order.
func (g *gen) fields(fs []*schema.Field, depth int, path []string, skip map[string]bool, seen responses) []node {
	picked := make([]*node, len(fs))
	for pass := range 2 {
		for i, f := range fs {
			if skip[f.Name] || f.IsDeprecated || strings.HasPrefix(f.Name, "__") {
				continue
			}
			t := g.s.TypeOf(f.Type)
			if (pass == 0) != t.IsLeaf() {
				continue
			}
			if n, ok := g.field(f, depth, path, seen, false); ok {
				picked[i] = &n
			}
		}
	}
	var out []node
	for _, n := range picked {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

// varSet is an operation's variable definitions.
type varSet struct {
	names map[string]bool
	defs  []string
}

// add declares a variable for argument a of field and returns its name:
// the argument's name, else the field's and the argument's, else numbered.
func (v *varSet) add(field string, a *schema.InputValue) string {
	name := a.Name
	if v.names[name] {
		name = field + upperFirst(a.Name)
		for i := 2; v.names[name]; i++ {
			name = field + upperFirst(a.Name) + strconv.Itoa(i)
		}
	}
	v.names[name] = true
	v.defs = append(v.defs, "$"+name+": "+a.Type.String())
	return name
}

func isRequired(a *schema.InputValue) bool { return a.Type.IsNonNull() && a.DefaultValue == nil }

func hasRequired(args []*schema.InputValue) bool {
	for _, a := range args {
		if isRequired(a) {
			return true
		}
	}
	return false
}

func contains(path []string, name string) bool {
	for _, p := range path {
		if p == name {
			return true
		}
	}
	return false
}

func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// unique returns name, or name with the smallest number from 2 that taken
// does not report.
func unique(name string, taken Taken) string {
	if taken == nil || !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := name + strconv.Itoa(i); !taken(n) {
			return n
		}
	}
}
