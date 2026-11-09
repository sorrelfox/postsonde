package environments

import "github.com/sorrelfox/postsonde/internal/core/storage/projectfile"

// Shared returns the environment with the given ID as the project shares
// it, for an export (EXP-05): read from its file in environments/ only,
// never from the local side files, so it has no local value (SEC-02) and
// every secret variable's value is "", a value typed into the file by
// hand included. leftOut counts the secret variables whose value that
// left out (a hand-typed one; the others have none in the file).
func (s *Store) Shared(id string) (env Environment, leftOut int, err error) {
	e, _, err := s.find(id)
	if err != nil {
		return Environment{}, 0, err
	}
	env = toEnvironment(e, nil, nil)
	for i := range env.Variables {
		v := &env.Variables[i]
		if v.Secret && v.Value != "" {
			v.Value = ""
			leftOut++
		}
		v.Local, v.LocalValue = false, ""
	}
	return env, leftOut, nil
}

// MarshalExport returns env as a PostSonde environment file (EXP-05): the
// format of the project's environments/ files, which the PostSonde
// environment importer reads back and which can be copied into another
// project's environments/ folder as it is. Only the members PostSonde
// knows are written; secret variables are written without a value (Marshal
// refuses one with a value), and local values are never part of it.
func MarshalExport(env Environment) ([]byte, error) {
	f := &projectfile.Environment{ID: env.ID, Name: env.Name, Color: env.Color}
	for _, v := range env.Variables {
		value := v.Value
		if v.Secret {
			value = ""
		}
		f.Variables = append(f.Variables, projectfile.Variable{
			Name: v.Name, Value: value, Secret: v.Secret, Description: v.Description, Disabled: v.Disabled,
		})
	}
	return projectfile.Marshal(f)
}

// ExportFileName returns the file name an export of an environment named
// name suggests: its file name slug (as a new environment's file would
// be named) with ext, e.g. ".json" or ".postman_environment.json".
func ExportFileName(name, ext string) string {
	return slug(name) + ext
}
