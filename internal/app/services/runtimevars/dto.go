package runtimevars

import core "github.com/sorrelfox/postsonde/internal/core/runtimevars"

// DTOs are the only types RuntimeVarsService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/environments.md.

// RuntimeVariableDTO is a runtime variable.
type RuntimeVariableDTO struct {
	Name string `json:"name"`
	// Value is the value, also of a secret variable (the UI masks it).
	Value string `json:"value"`
	// Secret masks the value in the chips, the timeline, the history, a
	// copied cURL command and a HAR export.
	Secret bool `json:"secret"`
	// Persist keeps the variable in PostSonde's local data, so it is back
	// when the project opens again; false keeps it for the session.
	Persist bool `json:"persist"`
}

// RuntimeVariablesDTO is the list every call returns.
type RuntimeVariablesDTO struct {
	// Variables are sorted by name.
	Variables []RuntimeVariableDTO `json:"variables"`
}

// SetInputDTO is the input of Set.
type SetInputDTO struct {
	// PreviousName is the name the variable had, for a rename; "" or the
	// same name otherwise.
	PreviousName string `json:"previousName"`
	Name         string `json:"name"`
	Value        string `json:"value"`
	Secret       bool   `json:"secret"`
	Persist      bool   `json:"persist"`
}

func (in SetInputDTO) toCore() core.Variable {
	return core.Variable{Name: in.Name, Value: in.Value, Secret: in.Secret, Persist: in.Persist}
}

func toDTO(st *core.Store) RuntimeVariablesDTO {
	list := st.List()
	out := RuntimeVariablesDTO{Variables: make([]RuntimeVariableDTO, 0, len(list))}
	for _, v := range list {
		out.Variables = append(out.Variables, RuntimeVariableDTO{Name: v.Name, Value: v.Value, Secret: v.Secret, Persist: v.Persist})
	}
	return out
}
