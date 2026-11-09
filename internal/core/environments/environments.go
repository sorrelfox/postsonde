package environments

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Error codes, in addition to the errs kinds and the projectfile codes that
// reading and writing environment files return.
const (
	// CodeNotFound: no environment has the given ID (kind not_found).
	// Details: id.
	CodeNotFound = "environments.not_found"
	// CodeInvalidName: the name is blank or too long (kind
	// invalid_argument). Details: field.
	CodeInvalidName = "environments.invalid_name"
	// CodeInvalidColor: the colour is not "#rrggbb" or "" (kind
	// invalid_argument). Details: field.
	CodeInvalidColor = "environments.invalid_color"
	// CodeNameTaken: another environment has the name, ignoring case (kind
	// already_exists). Details: name.
	CodeNameTaken = "environments.name_taken"
	// CodeReadOnly: the workspace or the environment file is from a newer
	// PostSonde, so it is not written (kind unsupported). Details: path.
	CodeReadOnly = "environments.read_only"
	// CodeDuplicateID: a file has the ID of another environment file; it is
	// skipped (kind parse, as a Problem). Details: id, path.
	CodeDuplicateID = "environments.duplicate_id"
	// CodeMissingID: a file has no id; it is skipped (kind parse, as a
	// Problem). Details: path.
	CodeMissingID = "environments.missing_id"
	// CodeListFailed: the environments directory could not be read. The
	// kind comes from the cause. Details: path.
	CodeListFailed = "environments.list_failed"
	// CodeSaveFailed: an environment file could not be created, renamed or
	// deleted. The kind comes from the cause. Details: path.
	CodeSaveFailed = "environments.save_failed"
	// CodeLocalDataFailed: the active environment or the secret values
	// could not be saved in the local directory. The kind comes from the
	// cause. Details: path.
	CodeLocalDataFailed = "environments.local_data_failed"
)

// MaxNameLength is the longest environment name, in characters.
const MaxNameLength = 100

// Variable is an environment variable as the UI edits it. For a secret
// variable Value is the value from the local secrets file.
type Variable struct {
	Name string
	// Value is the shared value, written to the project file; for a
	// secret variable, the value from the local secrets file.
	Value       string
	Description string
	Secret      bool
	Disabled    bool
	// Local is set when the variable has a local value on this computer
	// (SEC-02): LocalValue, kept in the local directory, replaces Value
	// here while the project file keeps Value for everyone else. A secret
	// variable has none: its value is local already. Never marshalled
	// (json:"-"): a Variable dumped as JSON, by an importer's model for
	// one, carries no local value.
	Local      bool   `json:"-"`
	LocalValue string `json:"-"`
}

// Effective returns the value this computer uses: the local value when
// there is one, else the value.
func (v Variable) Effective() string {
	if v.Local && !v.Secret {
		return v.LocalValue
	}
	return v.Value
}

// Environment is an environment file with its secret values filled in.
type Environment struct {
	ID    string
	Name  string
	Color string
	// FileName is the file's name in environments/, e.g. "staging.json".
	FileName  string
	Variables []Variable
	// ReadOnly is set for a file from a newer PostSonde: it is shown but
	// not written.
	ReadOnly bool
}

// Scope returns the environment's enabled variables as a template scope
// named "Environment: <name>". When a name is defined twice, the later
// definition wins. Secret variables keep their flag; a variable with a
// local value has that value (Effective) and the Local flag. A secret
// variable without a value on this computer (typed on another one) is left
// out, as a global or folder one is, so a reference to it falls through to
// the global variable of that name, the base every environment inherits
// (ENV-05), or is reported undefined instead of sending "".
func (e Environment) Scope() template.Scope {
	vars := make(map[string]template.Variable, len(e.Variables))
	for _, v := range e.Variables {
		if v.Disabled || (v.Secret && v.Value == "") {
			continue
		}
		vars[v.Name] = template.Variable{Value: v.Effective(), Secret: v.Secret, Local: v.Local && !v.Secret}
	}
	return template.MapScope{Label: "Environment: " + e.Name, Variables: vars}
}

// Problem is an environment file that could not be loaded. The file is left
// as it is.
type Problem struct {
	FileName string
	Err      error
}

// Snapshot is what List returns.
type Snapshot struct {
	// Environments are sorted by name, ignoring case, then by file name.
	Environments []Environment
	// ActiveID is the active environment's ID, or "" when none is active
	// or the saved one no longer exists.
	ActiveID string
	// Problems are the files in environments/ that were skipped.
	Problems []Problem
	// ReadOnly is set when the workspace is from a newer PostSonde: nothing
	// can be created, changed or deleted.
	ReadOnly bool
}

// Store reads and writes the environments of one workspace.
type Store struct {
	root     string
	local    string
	readOnly bool
}

// New returns a Store for the project at root (the folder with
// postsonde.json) with local side files in localDir. readOnly refuses
// every change to project files, for a workspace from a newer PostSonde;
// the active environment can still be chosen.
func New(root, localDir string, readOnly bool) *Store {
	return &Store{root: root, local: localDir, readOnly: readOnly}
}

// Dir returns the environments directory of the project.
func (s *Store) Dir() string { return filepath.Join(s.root, projectfile.EnvironmentsDir) }

// entry is a loaded environment file.
type entry struct {
	fileName string
	file     *projectfile.Environment
	data     []byte
}

func (e entry) path(dir string) string { return filepath.Join(dir, e.fileName) }

// List loads every environment. Files that cannot be loaded are reported
// as Problems, not errors; only an unreadable directory fails.
func (s *Store) List() (Snapshot, error) {
	entries, problems, err := s.load()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Environments: make([]Environment, 0, len(entries)),
		Problems:     problems,
		ReadOnly:     s.readOnly,
	}
	active := s.readActive()
	for _, e := range entries {
		env, err := s.withLocal(e)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Environments = append(snap.Environments, env)
		if e.file.ID == active {
			snap.ActiveID = active
		}
	}
	return snap, nil
}

// Get returns the environment with the given ID.
func (s *Store) Get(id string) (Environment, error) {
	e, _, err := s.find(id)
	if err != nil {
		return Environment{}, err
	}
	return s.withLocal(e)
}

// Active returns the active environment, or false when none is active.
func (s *Store) Active() (Environment, bool, error) {
	id := s.readActive()
	if id == "" {
		return Environment{}, false, nil
	}
	env, err := s.Get(id)
	if errors.Is(err, &errs.Error{Code: CodeNotFound}) {
		return Environment{}, false, nil
	}
	if err != nil {
		return Environment{}, false, err
	}
	return env, true, nil
}

// Create writes a new, empty environment called name to
// environments/<slug of name>.json.
func (s *Store) Create(name string) (Environment, error) {
	if err := s.checkWritable(s.Dir()); err != nil {
		return Environment{}, err
	}
	name, err := checkName(name)
	if err != nil {
		return Environment{}, err
	}
	entries, _, err := s.load()
	if err != nil {
		return Environment{}, err
	}
	if err := checkNameFree(entries, name, ""); err != nil {
		return Environment{}, err
	}
	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Environment{}, saveFailed(err, "The environments folder could not be created.", dir)
	}
	fileName, err := uniqueFileName(dir, slug(name), "")
	if err != nil {
		return Environment{}, err
	}
	f := &projectfile.Environment{ID: projectfile.NewID(projectfile.KindEnvironment), Name: name}
	if err := projectfile.WriteFile(filepath.Join(dir, fileName), f); err != nil {
		return Environment{}, err
	}
	return toEnvironment(entry{fileName: fileName, file: f}, nil, nil), nil
}

// Rename changes the environment's name and renames its file to match.
// If only the file cannot be renamed, the new name is kept in the old file.
func (s *Store) Rename(id, name string) (Environment, error) {
	e, entries, err := s.find(id)
	if err != nil {
		return Environment{}, err
	}
	dir := s.Dir()
	if err := s.checkWritable(e.path(dir)); err != nil {
		return Environment{}, err
	}
	if err := checkEntryWritable(e, dir); err != nil {
		return Environment{}, err
	}
	name, err = checkName(name)
	if err != nil {
		return Environment{}, err
	}
	if err := checkNameFree(entries, name, id); err != nil {
		return Environment{}, err
	}
	if e.file.Name != name {
		e.file.Name = name
		if err := s.moveOutSecrets(e.file); err != nil {
			return Environment{}, err
		}
		if err := projectfile.WriteFile(e.path(dir), e.file); err != nil {
			return Environment{}, err
		}
	}
	base := slug(name)
	if !strings.EqualFold(e.fileName, base+projectfile.Ext) {
		if next, err := uniqueFileName(dir, base, e.fileName); err == nil && next != e.fileName {
			if err := os.Rename(e.path(dir), filepath.Join(dir, next)); err == nil {
				e.fileName = next
			}
		}
	}
	return s.withLocal(e)
}

// Delete removes the environment's file, its secret values and its local
// values. If it was the active environment, none is active afterwards.
func (s *Store) Delete(id string) error {
	e, _, err := s.find(id)
	if err != nil {
		return err
	}
	dir := s.Dir()
	if err := s.checkWritable(e.path(dir)); err != nil {
		return err
	}
	if err := checkEntryWritable(e, dir); err != nil {
		return err
	}
	if err := os.Remove(e.path(dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return saveFailed(err, "The environment could not be deleted.", e.path(dir))
	}
	if err := s.removeSecrets(id); err != nil {
		return err
	}
	if err := localvalues.New(s.local).Remove(localvalues.Environment(id)); err != nil {
		return err
	}
	if s.readActive() == id {
		return s.SetActive("")
	}
	return nil
}

// SaveVariables replaces the environment's variables. Secret values go to
// the local secrets file; the project file gets "" for them. Local values
// (SEC-02) go to the local values file (localvalues) and replace those it
// had; the project file keeps the shared values, so setting or changing a
// local value never rewrites it. Unknown members of a variable are kept
// when a variable of the same name remains. The project file is not
// rewritten when nothing in it changed.
func (s *Store) SaveVariables(id string, vars []Variable) (Environment, error) {
	e, _, err := s.find(id)
	if err != nil {
		return Environment{}, err
	}
	dir := s.Dir()
	if err := s.checkWritable(e.path(dir)); err != nil {
		return Environment{}, err
	}
	if err := checkEntryWritable(e, dir); err != nil {
		return Environment{}, err
	}

	old := e.file.Variables
	used := make([]bool, len(old))
	secrets := map[string]string{}
	locals := map[string]string{}
	fileVars := make([]projectfile.Variable, 0, len(vars))
	for _, v := range vars {
		fv := projectfile.Variable{Name: v.Name, Value: v.Value, Secret: v.Secret, Description: v.Description, Disabled: v.Disabled}
		if v.Secret {
			secrets[v.Name] = v.Value
			fv.Value = ""
		} else if v.Local {
			locals[v.Name] = v.LocalValue
		}
		for i, o := range old {
			if !used[i] && o.Name == v.Name {
				used[i] = true
				fv.Unknown = o.Unknown
				break
			}
		}
		fileVars = append(fileVars, fv)
	}

	// Secrets and local values first: if the project file then fails, no
	// value is lost.
	if err := s.writeSecrets(id, secrets); err != nil {
		return Environment{}, err
	}
	if err := localvalues.New(s.local).Write(localvalues.Environment(id), locals); err != nil {
		return Environment{}, err
	}
	updated := *e.file
	updated.Variables = fileVars
	data, err := projectfile.Marshal(&updated)
	if err != nil {
		return Environment{}, err
	}
	if !bytes.Equal(data, e.data) && !sameContent(data, e.file) {
		if err := projectfile.WriteFile(e.path(dir), &updated); err != nil {
			return Environment{}, err
		}
	}
	return toEnvironment(entry{fileName: e.fileName, file: &updated}, secrets, locals), nil
}

// sameContent reports whether data, a marshalled environment, says what
// the loaded file f says, whatever the file's layout: a file written by
// hand (other indentation, member order) is then left byte for byte as it
// is, so saving unchanged variables, from the table or the raw JSON editor
// (ENV-07), round-trips every file exactly. A file with a secret value
// typed into it by hand never matches: the save moves the value out.
func sameContent(data []byte, f *projectfile.Environment) bool {
	old, err := projectfile.Marshal(f)
	return err == nil && bytes.Equal(data, old)
}

// SetColor sets the environment's accent colour (ENV-08): "#rrggbb", or ""
// for none. It is written to the environment file, so everyone sharing the
// project sees it ("production is red"). Upper-case digits are stored in
// lower case; anything else is an invalid_argument error with code
// CodeInvalidColor. The file is not rewritten when the colour is the same.
func (s *Store) SetColor(id, color string) (Environment, error) {
	color, err := checkColor(color)
	if err != nil {
		return Environment{}, err
	}
	e, _, err := s.find(id)
	if err != nil {
		return Environment{}, err
	}
	dir := s.Dir()
	if err := s.checkWritable(e.path(dir)); err != nil {
		return Environment{}, err
	}
	if err := checkEntryWritable(e, dir); err != nil {
		return Environment{}, err
	}
	if e.file.Color != color {
		e.file.Color = color
		if err := s.moveOutSecrets(e.file); err != nil {
			return Environment{}, err
		}
		if err := projectfile.WriteFile(e.path(dir), e.file); err != nil {
			return Environment{}, err
		}
	}
	return s.withLocal(e)
}

// IsColor reports whether color is an environment colour: "#rrggbb".
func IsColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for _, c := range color[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func checkColor(color string) (string, error) {
	if color == "" || IsColor(color) {
		return strings.ToLower(color), nil
	}
	return "", errs.New(errs.InvalidArgument, "The environment colour must be a hex colour such as #d0342c.").
		WithCode(CodeInvalidColor).
		WithHint("Pick one of the colours or enter six hex digits.").
		WithDetail("field", "color")
}

// SetActive makes the environment with the given ID the active one; ""
// means none. The choice is saved in the local directory, never in the
// project.
func (s *Store) SetActive(id string) error {
	if id != "" {
		if _, _, err := s.find(id); err != nil {
			return err
		}
	}
	return s.writeActive(id)
}

// load reads every environment file in environments/.
func (s *Store) load() ([]entry, []Problem, error) {
	dir := s.Dir()
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, []Problem{}, nil
	}
	if err != nil {
		return nil, nil, errs.Wrap(err, errs.KindOf(err), "The environments could not be read.").
			WithCode(CodeListFailed).
			WithHint("Check that the environments folder in the project is readable.").
			WithDetail("path", dir)
	}
	entries := make([]entry, 0, len(des))
	problems := []Problem{}
	ids := map[string]bool{}
	for _, de := range des {
		name := de.Name()
		if !isEnvironmentFileName(name) || de.IsDir() {
			continue
		}
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, Problem{FileName: name, Err: errs.Wrap(err, errs.KindOf(err), "An environment file could not be read.").
				WithCode(projectfile.CodeReadFailed).
				WithDetail("path", path)})
			continue
		}
		f, err := projectfile.DecodeEnvironment(data)
		if err != nil {
			var pe *errs.Error
			if errors.As(err, &pe) {
				err = pe.WithDetail("path", path)
			}
			problems = append(problems, Problem{FileName: name, Err: err})
			continue
		}
		switch {
		case strings.TrimSpace(f.ID) == "":
			problems = append(problems, Problem{FileName: name, Err: errs.New(errs.Parse, "The environment file has no id.").
				WithCode(CodeMissingID).
				WithHint(`Add an "id" such as "env_…" to the file.`).
				WithDetail("path", path)})
			continue
		case ids[f.ID]:
			problems = append(problems, Problem{FileName: name, Err: errs.New(errs.Parse, "Two environment files have the same id.").
				WithCode(CodeDuplicateID).
				WithHint(`Give the copy a new "id".`).
				WithDetail("id", f.ID).
				WithDetail("path", path)})
			continue
		}
		ids[f.ID] = true
		entries = append(entries, entry{fileName: name, file: f, data: data})
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		if c := strings.Compare(strings.ToLower(a.file.Name), strings.ToLower(b.file.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.fileName, b.fileName)
	})
	return entries, problems, nil
}

// find returns the entry with the given ID and all entries.
func (s *Store) find(id string) (entry, []entry, error) {
	entries, _, err := s.load()
	if err != nil {
		return entry{}, nil, err
	}
	for _, e := range entries {
		if e.file.ID == id {
			return e, entries, nil
		}
	}
	return entry{}, nil, errs.New(errs.NotFound, "The environment no longer exists.").
		WithCode(CodeNotFound).
		WithHint("It may have been deleted or its file renamed outside PostSonde.").
		WithDetail("id", id)
}

// withLocal returns the environment of e with what this computer keeps
// for it: the values of its secret variables and its local values.
func (s *Store) withLocal(e entry) (Environment, error) {
	if len(e.file.Variables) == 0 {
		return toEnvironment(e, nil, nil), nil
	}
	var secrets map[string]string
	if slices.ContainsFunc(e.file.Variables, func(v projectfile.Variable) bool { return v.Secret }) {
		var err error
		if secrets, err = s.readSecrets(e.file.ID); err != nil {
			return Environment{}, err
		}
	}
	locals, err := localvalues.New(s.local).Read(localvalues.Environment(e.file.ID))
	if err != nil {
		return Environment{}, err
	}
	return toEnvironment(e, secrets, locals), nil
}

// toEnvironment converts a loaded file. A secret variable's value comes
// from secrets; when it has none there, a value typed into the file by hand
// is shown, and moves to the secrets file on the next save. Another
// variable gets its local value from locals, if it has one; a local value
// of a secret variable, or of a name no variable has, is ignored.
func toEnvironment(e entry, secrets, locals map[string]string) Environment {
	vars := make([]Variable, 0, len(e.file.Variables))
	for _, v := range e.file.Variables {
		value := v.Value
		if v.Secret {
			if sv, ok := secrets[v.Name]; ok {
				value = sv
			}
		}
		out := Variable{Name: v.Name, Value: value, Description: v.Description, Secret: v.Secret, Disabled: v.Disabled}
		if lv, ok := locals[v.Name]; ok && !v.Secret {
			out.Local, out.LocalValue = true, lv
		}
		vars = append(vars, out)
	}
	return Environment{
		ID:        e.file.ID,
		Name:      e.file.Name,
		Color:     e.file.Color,
		FileName:  e.fileName,
		Variables: vars,
		ReadOnly:  e.file.Newer(),
	}
}

func (s *Store) checkWritable(path string) error {
	if !s.readOnly {
		return nil
	}
	return errs.New(errs.Unsupported, "This project was saved by a newer version of PostSonde, so its environments cannot be changed.").
		WithCode(CodeReadOnly).
		WithHint("Update PostSonde to change them.").
		WithDetail("path", path)
}

func checkEntryWritable(e entry, dir string) error {
	if !e.file.Newer() {
		return nil
	}
	return errs.New(errs.Unsupported, "This environment was saved by a newer version of PostSonde, so it cannot be changed.").
		WithCode(CodeReadOnly).
		WithHint("Update PostSonde to change it.").
		WithDetail("path", e.path(dir))
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errs.New(errs.InvalidArgument, "Enter a name for the environment.").
			WithCode(CodeInvalidName).
			WithHint("For example Local, Staging or Production.").
			WithDetail("field", "name")
	case utf8.RuneCountInString(name) > MaxNameLength:
		return "", errs.New(errs.InvalidArgument, "The environment name is too long.").
			WithCode(CodeInvalidName).
			WithHint("Use at most 100 characters.").
			WithDetail("field", "name")
	}
	return name, nil
}

func checkNameFree(entries []entry, name, exceptID string) error {
	for _, e := range entries {
		if e.file.ID != exceptID && strings.EqualFold(strings.TrimSpace(e.file.Name), name) {
			return errs.New(errs.AlreadyExists, "An environment with this name already exists.").
				WithCode(CodeNameTaken).
				WithHint("Choose another name.").
				WithDetail("name", name)
		}
	}
	return nil
}

func saveFailed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeSaveFailed).
		WithHint("Check that the project folder is writable and the disk is not full.").
		WithDetail("path", path)
}

// isEnvironmentFileName reports whether a directory entry name can be an
// environment file: a .json file that is not reserved (dot files, which
// include interrupted saves, and names starting with "_").
func isEnvironmentFileName(name string) bool {
	return strings.HasSuffix(name, projectfile.Ext) &&
		!strings.HasPrefix(name, ".") &&
		!strings.HasPrefix(name, "_") &&
		len(name) > len(projectfile.Ext)
}
