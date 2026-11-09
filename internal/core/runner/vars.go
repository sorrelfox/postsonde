package runner

import (
	"slices"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/template"
)

// VarsScopeName names a run's variables in hovers and the timeline.
const VarsScopeName = "Run"

// Vars are the variables of one run: a template.Scope that every request
// of the run is rendered with, above all the others (the runtime place of
// ENV-06). A run starts with none. They are how values travel from one
// request to the next: extraction into variables (TEST-05) sets them after
// a response, and a data-driven run (RUN-05) sets the row of each
// iteration (SetRow). They live as long as the run and are never written
// to a file. Safe for concurrent use.
type Vars struct {
	mu     sync.RWMutex
	values map[string]template.Variable
	// row is the data row of the current iteration (RUN-05), below the
	// values set.
	row map[string]string
}

// NewVars returns an empty set of run variables.
func NewVars() *Vars {
	return &Vars{values: map[string]template.Variable{}}
}

// Name names the scope.
func (v *Vars) Name() string { return VarsScopeName }

// Lookup returns the variable called name.
func (v *Vars) Lookup(name string) (template.Variable, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if val, ok := v.values[name]; ok {
		return val, true
	}
	if val, ok := v.row[name]; ok {
		return template.Variable{Value: val}, true
	}
	return template.Variable{}, false
}

// Set sets variable name for the rest of the run.
func (v *Vars) Set(name string, value template.Variable) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[name] = value
}

// SetRow makes row the data row of the iteration that starts (RUN-05):
// its values replace those of the row before, and those Set before under
// the same names, so each iteration starts with its own row. A name the
// row does not have is no longer the previous row's. Values Set after it
// (extracted from a response) win over the row until the next one. The
// row is not copied: the caller does not change it afterwards.
func (v *Vars) SetRow(row map[string]string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k := range row {
		delete(v.values, k)
	}
	v.row = row
}

// Names returns the names set, the row's included, sorted
// (template.Lister).
func (v *Vars) Names() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.values)+len(v.row))
	for k := range v.values {
		out = append(out, k)
	}
	for k := range v.row {
		if _, ok := v.values[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
