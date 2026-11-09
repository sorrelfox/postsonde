package template

import "slices"

// Variable is a value a scope supplies for a name.
type Variable struct {
	Value string
	// Secret marks a value that must be masked in the UI, the timeline and
	// logs. It carries over to every reference whose value includes it.
	Secret bool
	// Local marks this computer's local value of the variable (SEC-02),
	// which replaces the shared value of the project file within the
	// same scope. It changes nothing in rendering; the editor's hover
	// says it (docs/environments.md#local-values).
	Local bool
}

// Scope is a named set of variables: globals, an environment, a collection,
// a folder, runtime values. Response references plug in as scopes too;
// Lookup is called once per reference, so a scope may return a fresh value
// each time. Dynamic values ($uuid, $timestamp) are built into the Resolver
// (see IsDynamic).
type Scope interface {
	// Name identifies the scope in hover and debugging output, for example
	// "Environment: dev".
	Name() string
	// Lookup returns the variable called name, if the scope defines it.
	Lookup(name string) (Variable, bool)
}

// MapScope is a Scope backed by a map.
type MapScope struct {
	Label     string
	Variables map[string]Variable
}

// Name returns the label.
func (s MapScope) Name() string { return s.Label }

// Lookup returns the variable from the map.
func (s MapScope) Lookup(name string) (Variable, bool) {
	v, ok := s.Variables[name]
	return v, ok
}

// Vars returns a MapScope of plain (non-secret) values.
func Vars(label string, values map[string]string) MapScope {
	m := make(map[string]Variable, len(values))
	for k, v := range values {
		m[k] = Variable{Value: v}
	}
	return MapScope{Label: label, Variables: m}
}

// Binding is a variable together with the scope it was found in.
type Binding struct {
	Name  string
	Scope string
	Variable
}

// Chain is an ordered list of scopes from the lowest to the highest
// precedence: a later scope overrides an earlier one. The documented order
// is global < environment < collection < folder < runtime.
type Chain []Scope

// Lookup returns the binding that wins for name: the one from the last
// scope that defines it.
func (c Chain) Lookup(name string) (Binding, bool) {
	for i := len(c) - 1; i >= 0; i-- {
		if v, ok := c[i].Lookup(name); ok {
			return Binding{Name: name, Scope: c[i].Name(), Variable: v}, true
		}
	}
	return Binding{}, false
}

// All returns every binding for name, winning one first, followed by the
// ones it shadows. It is for debugging views ("defined in 3 scopes").
func (c Chain) All(name string) []Binding {
	var out []Binding
	for i := len(c) - 1; i >= 0; i-- {
		if v, ok := c[i].Lookup(name); ok {
			out = append(out, Binding{Name: name, Scope: c[i].Name(), Variable: v})
		}
	}
	return out
}

// Lister is a Scope that can list the names it defines. Scopes whose names
// are not known in advance (dynamic values) do not implement it.
type Lister interface {
	Names() []string
}

// Names returns the names the map defines, sorted.
func (s MapScope) Names() []string {
	out := make([]string, 0, len(s.Variables))
	for k := range s.Variables {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Names returns every name defined by a scope of the chain that is a
// Lister, sorted and without duplicates. It is for the editor, which marks
// the references it can resolve and shows where each one comes from.
func (c Chain) Names() []string {
	var out []string
	for _, s := range c {
		if l, ok := s.(Lister); ok {
			out = append(out, l.Names()...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
