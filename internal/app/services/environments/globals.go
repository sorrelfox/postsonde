package environments

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Global variables (ENV-02): the variables of postsonde.json, edited in
// the Environments view next to the environments. The collection service
// owns the file (its tree holds it), so the app hands this service the
// functions that read and write them (WithGlobals).

// CodeGlobalsUnavailable: the service was built without WithGlobals (kind
// unsupported).
const CodeGlobalsUnavailable = "environments.globals_unavailable"

// Globals reads and writes the global variables of the open project
// (collection.LoadGlobalVariables and collection.SaveGlobalVariables).
// Secret variables carry the value stored on this computer.
type Globals struct {
	Load func(ctx context.Context) (vars []projectfile.Variable, readOnly bool, err error)
	Save func(ctx context.Context, vars []projectfile.Variable) ([]projectfile.Variable, error)
}

// WithGlobals makes GlobalVariables and SaveGlobalVariables work.
func WithGlobals(g Globals) Option {
	return func(s *EnvironmentsService) { s.globals = g }
}

// GlobalVariablesDTO is the project's global variables.
type GlobalVariablesDTO struct {
	// Variables are in file order; a secret one has the value stored on
	// this computer ("" when there is none).
	Variables []VariableDTO `json:"variables"`
	// ReadOnly is true when postsonde.json cannot be written.
	ReadOnly bool `json:"readOnly"`
}

// SaveGlobalVariablesInputDTO is the input of SaveGlobalVariables.
type SaveGlobalVariablesInputDTO struct {
	// Variables replace the global variables, in order.
	Variables []VariableDTO `json:"variables"`
}

// GlobalVariables returns the global variables of the open project.
func (s *EnvironmentsService) GlobalVariables(ctx context.Context) (GlobalVariablesDTO, error) {
	if err := interrupted(ctx, "Loading the global variables"); err != nil {
		return GlobalVariablesDTO{}, err
	}
	if s.globals.Load == nil {
		return GlobalVariablesDTO{}, globalsUnavailable()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vars, readOnly, err := s.globals.Load(ctx)
	if err != nil {
		return GlobalVariablesDTO{}, err
	}
	locals, err := s.globalLocals()
	if err != nil {
		return GlobalVariablesDTO{}, err
	}
	return GlobalVariablesDTO{Variables: toVariableDTOs(vars, locals), ReadOnly: readOnly}, nil
}

// globalLocals returns the local values of the global variables of the
// open workspace (SEC-02), by name; none without an open workspace.
func (s *EnvironmentsService) globalLocals() (map[string]string, error) {
	w := s.workspaces.Current()
	if w == nil {
		return map[string]string{}, nil
	}
	return localvalues.New(w.LocalDir).Read(localvalues.Global(w.ID()))
}

// SaveGlobalVariables replaces the global variables of the open project
// and writes postsonde.json. Secret values are kept in the local
// directory, never in the project file. Local values (SEC-02) replace
// those kept in <local>/local-values/global/<workspace id>.json, written
// before the project file; the project file keeps the shared values. Like
// SaveVariables it sends environments:changed, so the editors resolve
// {{variables}} again.
func (s *EnvironmentsService) SaveGlobalVariables(ctx context.Context, in SaveGlobalVariablesInputDTO) (GlobalVariablesDTO, error) {
	if err := interrupted(ctx, "Saving the global variables"); err != nil {
		return GlobalVariablesDTO{}, err
	}
	if s.globals.Save == nil {
		return GlobalVariablesDTO{}, globalsUnavailable()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vars := make([]projectfile.Variable, 0, len(in.Variables))
	locals := map[string]string{}
	for _, v := range in.Variables {
		vars = append(vars, projectfile.Variable{
			Name:        v.Name,
			Value:       v.Value,
			Description: v.Description,
			Secret:      v.Secret,
			Disabled:    !v.Enabled,
		})
		if v.LocalValue != nil && !v.Secret {
			locals[v.Name] = *v.LocalValue
		}
	}
	// The local values first: if the project file then fails, none is
	// lost. They never go to s.globals.Save, which writes the project.
	if w := s.workspaces.Current(); w != nil {
		if err := localvalues.New(w.LocalDir).Write(localvalues.Global(w.ID()), locals); err != nil {
			return GlobalVariablesDTO{}, err
		}
	}
	saved, err := s.globals.Save(ctx, vars)
	if err != nil {
		return GlobalVariablesDTO{}, err
	}
	if st, err := s.store(); err == nil {
		s.changed(st)
	}
	return GlobalVariablesDTO{Variables: toVariableDTOs(saved, locals)}, nil
}

// toVariableDTOs converts global variables, with the local values of those
// that are not secret.
func toVariableDTOs(vars []projectfile.Variable, locals map[string]string) []VariableDTO {
	out := make([]VariableDTO, 0, len(vars))
	for _, v := range vars {
		lv, ok := locals[v.Name]
		out = append(out, VariableDTO{
			Name:        v.Name,
			Value:       v.Value,
			Description: v.Description,
			Secret:      v.Secret,
			Enabled:     !v.Disabled,
			LocalValue:  localValue(ok && !v.Secret, lv),
		})
	}
	return out
}

func globalsUnavailable() error {
	return errs.New(errs.Unsupported, "Global variables are not available.").WithCode(CodeGlobalsUnavailable)
}
