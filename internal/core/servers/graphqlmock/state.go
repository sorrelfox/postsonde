package graphqlmock

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// state is what a server answers with: its configuration, the schema it
// loaded and the overrides compiled against it. It is replaced whole on
// Reconfigure; requests keep the one they started with.
type state struct {
	cfg    Config
	loaded *Loaded
	// fields are the field overrides by "Type.field", types the type
	// overrides by type name, in configuration order.
	fields map[string][]*compiled
	types  map[string]*compiled
	// problems are the overrides that are skipped, by index.
	problems []Problem
}

// Problem is an override the server cannot use, or a problem of the
// schema it serves anyway.
type Problem struct {
	// OverrideIndex is the override's index, -1 for a schema problem.
	OverrideIndex int
	// Field is the override's member at fault ("target", "args",
	// "value"), "" for a schema problem.
	Field   string
	Message string
}

// compiled is an override checked against the schema.
type compiled struct {
	index   int
	args    map[string]any // nil matches any arguments
	mode    string
	value   any
	message string
	code    string
}

// compile checks the overrides of c against l. Overrides with a problem
// are left out and listed.
func compile(c Config, l *Loaded) *state {
	st := &state{cfg: c, loaded: l, fields: map[string][]*compiled{}, types: map[string]*compiled{}}
	sch := l.Schema
	for i, o := range c.Overrides {
		if !o.Enabled {
			continue
		}
		fail := func(field, format string, args ...any) {
			st.problems = append(st.problems, Problem{OverrideIndex: i, Field: field, Message: fmt.Sprintf("Override %d (%s): ", i+1, o.Target) + fmt.Sprintf(format, args...)})
		}
		if sch == nil {
			continue // every request fails without a schema
		}
		cp := &compiled{index: i, mode: o.Mode, message: o.Error, code: o.Code}
		if cp.message == "" {
			cp.message = "Error from the PostSonde mock for " + o.Target + "."
		}
		typeName, fieldName, isField := strings.Cut(o.Target, ".")
		t := sch.Type(typeName)
		switch {
		case t == nil:
			fail("target", "the schema has no type %q.", typeName)
			continue
		case isField && t.Kind != schema.KindObject && t.Kind != schema.KindInterface:
			fail("target", "%s is %s, which has no fields.", typeName, kindText(t.Kind))
			continue
		case !isField && !t.IsLeaf():
			fail("target", "%s is %s; a type override is for a scalar or enum type, write Type.field for a field.", typeName, kindText(t.Kind))
			continue
		}
		var fd *schema.Field
		if isField {
			if fd = t.Field(fieldName); fd == nil {
				fail("target", "the type %s has no field %q.", typeName, fieldName)
				continue
			}
		}
		if strings.TrimSpace(o.Args) != "" {
			v, err := decodeJSON(o.Args)
			m, isObj := v.(map[string]any)
			if err != nil || !isObj {
				fail("args", "the arguments to match are not a JSON object.")
				continue
			}
			bad := ""
			for k := range m {
				if fd.Arg(k) == nil {
					bad = k
					break
				}
			}
			if bad != "" {
				fail("args", "the field %s has no argument %q.", o.Target, bad)
				continue
			}
			cp.args = m
		}
		if o.Mode == ModeValue {
			text := strings.TrimSpace(o.Value)
			if text == "" {
				text = "null"
			}
			v, err := decodeJSON(text)
			if err != nil {
				fail("value", "the value is not valid JSON: %v.", err)
				continue
			}
			cp.value = v
		}
		if isField {
			st.fields[o.Target] = append(st.fields[o.Target], cp)
		} else if _, dup := st.types[typeName]; !dup {
			st.types[typeName] = cp
		}
	}
	if l.Schema != nil {
		for _, p := range l.Problems {
			st.problems = append(st.problems, Problem{OverrideIndex: -1, Message: "Schema: " + p.String()})
		}
	}
	return st
}

func kindText(k schema.Kind) string {
	switch k {
	case schema.KindScalar:
		return "a scalar"
	case schema.KindObject:
		return "an object type"
	case schema.KindInterface:
		return "an interface"
	case schema.KindUnion:
		return "a union"
	case schema.KindEnum:
		return "an enum"
	case schema.KindInputObject:
		return "an input type"
	}
	return string(k)
}

// fieldOverride is the first override of obj's field name (or of the
// field of an interface obj implements) whose arguments match.
func (st *state) fieldOverride(obj *schema.Type, name string, args map[string]any) *compiled {
	targets := append([]string{obj.Name}, obj.Interfaces...)
	for _, tn := range targets {
		for _, cp := range st.fields[tn+"."+name] {
			if cp.matches(args) {
				return cp
			}
		}
	}
	return nil
}

func (cp *compiled) matches(args map[string]any) bool {
	for k, want := range cp.args {
		got, ok := args[k]
		if !ok || !looseEqual(got, want) {
			return false
		}
	}
	return true
}

// typeOverride is the override of every value of a scalar or enum type.
func (st *state) typeOverride(name string) *compiled { return st.types[name] }

// Target is a field or leaf type an override can answer.
type Target struct {
	// Target is "Type.field" or a type name.
	Target string
	// Type is the field's type ("[User!]!") or the type's kind
	// ("SCALAR", "ENUM").
	Type string
	// Args are the field's argument names.
	Args []string
	// Root is set for a field of the query or mutation root.
	Root bool
}

// Description is what the editor of a server shows about its schema.
type Description struct {
	// Source and Format of the schema, "" when none is configured.
	Source string
	Format string
	// Types counts the named types the schema declares; QueryFields,
	// MutationFields and SubscriptionFields its root fields.
	Types              int
	QueryFields        int
	MutationFields     int
	SubscriptionFields int
	// Targets are what overrides can answer: root fields first, then the
	// other fields by type, then the scalar and enum types.
	Targets []Target
	// Problems are the schema's problems and the overrides that do not fit
	// it.
	Problems []Problem
	// SDL is the schema printed as SDL.
	SDL string
}

// Describe loads the schema of c and checks its overrides, as Start does.
// A schema that does not load is the error.
func Describe(c Config, readFile func(stored string) ([]byte, error)) (Description, error) {
	c, err := Normalize(c)
	if err != nil {
		return Description{}, err
	}
	l, err := LoadSchema(c, readFile)
	if err != nil {
		return Description{}, err
	}
	st := compile(c, l)
	d := Description{Source: l.Source, Format: l.Format, Problems: st.problems, Targets: []Target{}}
	if d.Problems == nil {
		d.Problems = []Problem{}
	}
	sch := l.Schema
	if sch == nil {
		return d, nil
	}
	d.Types = len(sch.Types)
	count := func(t *schema.Type) int {
		if t == nil {
			return 0
		}
		return len(t.Fields)
	}
	d.QueryFields, d.MutationFields, d.SubscriptionFields = count(sch.Query()), count(sch.Mutation()), count(sch.Subscription())
	roots := []string{sch.QueryType, sch.MutationType}
	addFields := func(t *schema.Type, root bool) {
		for _, f := range t.Fields {
			args := make([]string, 0, len(f.Args))
			for _, a := range f.Args {
				args = append(args, a.Name)
			}
			d.Targets = append(d.Targets, Target{Target: t.Name + "." + f.Name, Type: f.Type.String(), Args: args, Root: root})
		}
	}
	for _, r := range roots {
		if t := sch.Type(r); r != "" && t != nil {
			addFields(t, true)
		}
	}
	var leaves []Target
	for _, t := range sch.Types {
		switch {
		case slices.Contains(roots, t.Name) || t.Name == sch.SubscriptionType || strings.HasPrefix(t.Name, "__"):
		case t.Kind == schema.KindObject || t.Kind == schema.KindInterface:
			addFields(t, false)
		case t.IsLeaf() && !t.IsBuiltin():
			leaves = append(leaves, Target{Target: t.Name, Type: string(t.Kind)})
		}
	}
	d.Targets = append(d.Targets, leaves...)
	d.SDL = schema.SDL(sch)
	return d, nil
}
