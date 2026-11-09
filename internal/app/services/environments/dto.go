package environments

import (
	"errors"

	core "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DTOs are the only types EnvironmentsService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/environments.md.

// VariableDTO is an environment variable.
type VariableDTO struct {
	Name string `json:"name"`
	// Value is the value; for a secret variable, the value kept in the
	// local directory (the project file has "").
	Value       string `json:"value"`
	Description string `json:"description"`
	// Secret: the value is never written to the project and is masked in
	// the UI.
	Secret bool `json:"secret"`
	// Enabled is false for a variable that is kept but ignored.
	Enabled bool `json:"enabled"`
	// LocalValue is the variable's local value (SEC-02): kept on this
	// computer only, it replaces Value here while the project file keeps
	// Value for everyone else. null when the variable has none; always
	// null for a secret variable, whose value is local already.
	LocalValue *string `json:"localValue"`
}

// localValue is the DTO form of a local value.
func localValue(local bool, value string) *string {
	if !local {
		return nil
	}
	return &value
}

// EnvironmentDTO is an environment.
type EnvironmentDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Color is "#rrggbb" or "".
	Color string `json:"color"`
	// FileName is the file in the project's environments folder.
	FileName  string        `json:"fileName"`
	Variables []VariableDTO `json:"variables"`
	// ReadOnly is true for a file from a newer PostSonde.
	ReadOnly bool `json:"readOnly"`
}

// ProblemDTO is an environment file that could not be loaded.
type ProblemDTO struct {
	FileName string `json:"fileName"`
	Message  string `json:"message"`
	Code     string `json:"code"`
}

// EnvironmentsDTO is every environment of the open workspace.
type EnvironmentsDTO struct {
	// Environments are sorted by name.
	Environments []EnvironmentDTO `json:"environments"`
	// ActiveID is the active environment, "" for none.
	ActiveID string       `json:"activeId"`
	Problems []ProblemDTO `json:"problems"`
	// ReadOnly is true when the workspace is from a newer PostSonde:
	// environments cannot be created, changed or deleted.
	ReadOnly bool `json:"readOnly"`
}

// RenameInputDTO is the input of Rename.
type RenameInputDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SetColorInputDTO is the input of SetColor.
type SetColorInputDTO struct {
	ID string `json:"id"`
	// Color is "#rrggbb", or "" for none.
	Color string `json:"color"`
}

// SaveVariablesInputDTO is the input of SaveVariables.
type SaveVariablesInputDTO struct {
	ID string `json:"id"`
	// Variables replace the environment's variables, in order.
	Variables []VariableDTO `json:"variables"`
}

func toEnvironmentDTO(e core.Environment) EnvironmentDTO {
	vars := make([]VariableDTO, 0, len(e.Variables))
	for _, v := range e.Variables {
		vars = append(vars, VariableDTO{
			Name:        v.Name,
			Value:       v.Value,
			Description: v.Description,
			Secret:      v.Secret,
			Enabled:     !v.Disabled,
			LocalValue:  localValue(v.Local && !v.Secret, v.LocalValue),
		})
	}
	return EnvironmentDTO{
		ID:        e.ID,
		Name:      e.Name,
		Color:     e.Color,
		FileName:  e.FileName,
		Variables: vars,
		ReadOnly:  e.ReadOnly,
	}
}

func toEnvironmentsDTO(s core.Snapshot) EnvironmentsDTO {
	envs := make([]EnvironmentDTO, 0, len(s.Environments))
	for _, e := range s.Environments {
		envs = append(envs, toEnvironmentDTO(e))
	}
	problems := make([]ProblemDTO, 0, len(s.Problems))
	for _, p := range s.Problems {
		dto := ProblemDTO{FileName: p.FileName, Message: p.Err.Error()}
		var e *errs.Error
		if errors.As(p.Err, &e) {
			dto.Message, dto.Code = e.Message, e.Code
		}
		problems = append(problems, dto)
	}
	return EnvironmentsDTO{Environments: envs, ActiveID: s.ActiveID, Problems: problems, ReadOnly: s.ReadOnly}
}

func (in SaveVariablesInputDTO) toCore() []core.Variable {
	vars := make([]core.Variable, 0, len(in.Variables))
	for _, v := range in.Variables {
		cv := core.Variable{
			Name:        v.Name,
			Value:       v.Value,
			Description: v.Description,
			Secret:      v.Secret,
			Disabled:    !v.Enabled,
		}
		if v.LocalValue != nil && !v.Secret {
			cv.Local, cv.LocalValue = true, *v.LocalValue
		}
		vars = append(vars, cv)
	}
	return vars
}
