// Package runtimevars keeps a project's runtime variables (ENV-15):
// values set while working, by hand in the Environments view or by the
// extraction rules of requests (internal/core/extract). They form the "Runtime"
// scope, above every scope a project file defines and below the variables
// of a folder run:
//
//	global < environment < collection < folder < runtime < run
//
// Each variable is kept for the session or persisted, its own choice. A
// session variable lives in memory only: it is gone when the project is
// closed or opened again (a new workspace.Workspace value) and when
// PostSonde quits. A persisted variable is also written to the
// workspace's local side-file directory, so it is back the next time the
// project opens:
//
//	<local>/runtime-variables.json            names, values, flags: {"version": 1, "variables": [...]}
//	<local>/secrets/runtime/variables.json    values of the secret ones: {"version": 1, "values": {...}}
//
// Both files are 0600 in 0700 directories and written atomically. <local>
// is never inside the project (workspace.Manager.LocalDir), so a runtime
// value cannot reach a project file, an export or a commit.
//
// A runtime variable can be secret (a token extracted from a login): its
// value is masked like every secret variable's, in the chips, the
// timeline, the history, a copied cURL command and a HAR export, and a
// persisted one keeps its value in the secrets directory with the other
// secret values, apart from the list, read and written through
// secretstore, which encrypts it with the others once the user sets a
// master password. While the secrets are locked, a project with
// persisted secret runtime variables cannot open its runtime scope
// (secretstore.locked); session variables live in memory only.
//
// Store holds the variables of one open project and is safe for
// concurrent use. Registry hands out the Store of the open project and
// starts a new session for every newly opened one. Standard library only.
// See docs/environments.md#runtime-variables.
package runtimevars

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// ScopeName names the runtime scope in hovers, completion and the
// timeline.
const ScopeName = "Runtime"

// Names in the workspace's local directory, slash-separated.
const (
	// FileName holds the persisted runtime variables.
	FileName = "runtime-variables.json"
	// SecretsDir holds the values of the persisted secret ones
	// (SecretsFileName), next to the other secret values.
	SecretsDir = "secrets/runtime"
	// SecretsFileName is the file in SecretsDir.
	SecretsFileName = "variables.json"
)

// MaxNameLength is the longest name, in bytes.
const MaxNameLength = 256

// Error codes.
const (
	// CodeLocalDataFailed: a file of persisted runtime variables could not
	// be read, parsed or written. Details: path.
	CodeLocalDataFailed = "runtimevars.local_data_failed"
	// CodeInvalidName: a name that is empty, too long, has whitespace
	// around it, or cannot be written as a {{reference}}.
	CodeInvalidName = "runtimevars.invalid_name"
	// CodeNotFound: no runtime variable has the name. Details: name.
	CodeNotFound = "runtimevars.not_found"
)

const (
	fileVersion             = 1
	dirPerm     fs.FileMode = 0o700
	filePerm    fs.FileMode = 0o600
)

// Variable is a runtime variable.
type Variable struct {
	Name  string
	Value string
	// Secret masks the value wherever it is shown or recorded.
	Secret bool
	// Persist keeps the variable in the workspace's local directory, so it
	// survives closing the project; otherwise it lasts for the session.
	Persist bool
}

// file is <local>/runtime-variables.json. Secret values are "" here.
type file struct {
	Version   int         `json:"version"`
	Variables []fileEntry `json:"variables"`
}

type fileEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// secretsFile is <local>/secrets/runtime/variables.json.
type secretsFile struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// Store holds the runtime variables of one open project. Safe for
// concurrent use.
type Store struct {
	local string
	// onChange is called with the store after every successful change,
	// outside the lock.
	onChange func(*Store)

	mu   sync.RWMutex
	vars map[string]Variable
}

// Open returns the runtime variables of the workspace whose local
// directory is localDir: the persisted ones, and no session ones yet. A
// file that cannot be parsed is an error, so no change overwrites values
// that could not be read (Remove deletes the files).
func Open(localDir string) (*Store, error) {
	s := &Store{local: localDir, vars: map[string]Variable{}}
	list, err := readJSON[file](s.path())
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if slices.ContainsFunc(list.Variables, func(e fileEntry) bool { return e.Secret }) {
		sf, err := readSecrets(s.secretsPath())
		if err != nil {
			return nil, err
		}
		secrets = sf.Values
	}
	for _, e := range list.Variables {
		if CheckName(e.Name) != nil {
			continue
		}
		v := Variable{Name: e.Name, Value: e.Value, Secret: e.Secret, Persist: true}
		if e.Secret {
			value, ok := secrets[e.Name]
			if !ok {
				continue // its value is gone: undefined rather than ""
			}
			v.Value = value
		}
		s.vars[e.Name] = v
	}
	return s, nil
}

// Remove deletes the persisted runtime variables of the workspace whose
// local directory is localDir, readable or not.
func Remove(localDir string) error {
	s := &Store{local: localDir}
	for _, p := range []string{s.path(), s.secretsPath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return failed(err, "PostSonde could not remove the runtime variables.", p)
		}
	}
	return nil
}

func (s *Store) path() string { return filepath.Join(s.local, FileName) }

func (s *Store) secretsPath() string {
	return filepath.Join(s.local, filepath.FromSlash(SecretsDir), SecretsFileName)
}

// CheckName returns nil when name can be a runtime variable's: not empty,
// at most MaxNameLength bytes, no whitespace around it, and a name a
// {{reference}} can spell.
func CheckName(name string) error {
	invalid := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidName).
			WithHint("Use a name that a {{reference}} can spell, such as token or auth.id.")
	}
	switch {
	case name == "":
		return invalid("A runtime variable needs a name.")
	case len(name) > MaxNameLength:
		return invalid("The name of a runtime variable is too long.")
	case strings.TrimSpace(name) != name:
		return invalid("The name of a runtime variable cannot start or end with a space.")
	}
	segs := template.Parse("{{" + name + "}}")
	if len(segs) != 1 || segs[0].Kind != template.Var || segs[0].Name != name {
		return invalid("The name of a runtime variable cannot contain {{ or }}.")
	}
	return nil
}

// List returns the variables, sorted by name.
func (s *Store) List() []Variable {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Variable, 0, len(s.vars))
	for _, v := range s.vars {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Variable) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get returns the variable called name.
func (s *Store) Get(name string) (Variable, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vars[name]
	return v, ok
}

// Set sets variable v, replacing the one of the same name: its value, its
// secrecy and whether it is persisted. This is what extraction from a
// response calls, after a single send or a request of a run.
func (s *Store) Set(v Variable) error { return s.Replace("", v) }

// Replace sets v in place of the variable called old (a rename when the
// names differ; "" or a name that is not set only adds v). The persisted
// files are rewritten when either one is persisted; if that fails nothing
// changes.
func (s *Store) Replace(old string, v Variable) error {
	if err := CheckName(v.Name); err != nil {
		return err
	}
	return s.change(func(next map[string]Variable) {
		if old != "" {
			delete(next, old)
		}
		next[v.Name] = v
	})
}

// Delete removes the variable called name; CodeNotFound when there is none.
func (s *Store) Delete(name string) error {
	if _, ok := s.Get(name); !ok {
		return errs.New(errs.NotFound, "There is no runtime variable of that name.").
			WithCode(CodeNotFound).
			WithHint("It may have been cleared or the project opened again; reload the list.").
			WithDetail("name", name)
	}
	return s.change(func(next map[string]Variable) { delete(next, name) })
}

// Clear removes every variable, session and persisted.
func (s *Store) Clear() error {
	if err := Remove(s.local); err != nil {
		return err
	}
	s.mu.Lock()
	s.vars = map[string]Variable{}
	s.mu.Unlock()
	s.changed()
	return nil
}

// change applies edit to a copy of the variables, writes the persisted
// ones when they changed, and keeps the copy only when that worked.
func (s *Store) change(edit func(map[string]Variable)) error {
	s.mu.Lock()
	next := make(map[string]Variable, len(s.vars)+1)
	for k, v := range s.vars {
		next[k] = v
	}
	edit(next)
	if !samePersisted(s.vars, next) {
		if err := s.write(next); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.vars = next
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) changed() {
	if s.onChange != nil {
		s.onChange(s)
	}
}

// samePersisted reports whether a and b persist the same variables.
func samePersisted(a, b map[string]Variable) bool {
	count := 0
	for k, v := range a {
		if !v.Persist {
			continue
		}
		count++
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	for _, v := range b {
		if v.Persist {
			count--
		}
	}
	return count == 0
}

// write writes the persisted variables of vars: the secret values first,
// then the list, so the list never names a secret whose value was not
// saved. No persisted variables remove the files.
func (s *Store) write(vars map[string]Variable) error {
	var entries []fileEntry
	secrets := map[string]string{}
	for _, v := range vars {
		if !v.Persist {
			continue
		}
		e := fileEntry{Name: v.Name, Value: v.Value, Secret: v.Secret}
		if v.Secret {
			secrets[v.Name] = v.Value
			e.Value = ""
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return Remove(s.local)
	}
	slices.SortFunc(entries, func(a, b fileEntry) int { return strings.Compare(a.Name, b.Name) })
	if len(secrets) > 0 {
		if err := writeSecrets(s.secretsPath(), secretsFile{Version: fileVersion, Values: secrets}); err != nil {
			return err
		}
	} else if err := os.Remove(s.secretsPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not save the runtime variables.", s.secretsPath())
	}
	return writeJSON(s.path(), file{Version: fileVersion, Variables: entries})
}

// Scope returns the variables as they are now, as the template scope named
// ScopeName. A secret variable keeps its flag, so every reference to it is
// masked.
func (s *Store) Scope() template.MapScope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]template.Variable, len(s.vars))
	for k, v := range s.vars {
		m[k] = template.Variable{Value: v.Value, Secret: v.Secret}
	}
	return template.MapScope{Label: ScopeName, Variables: m}
}

// Secrets returns the values of the secret variables that are not empty,
// sorted, for masking copied and saved logs.
func (s *Store) Secrets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, v := range s.vars {
		if v.Secret && v.Value != "" {
			out = append(out, v.Value)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func readJSON[T any](path string) (T, error) {
	var out T
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, failed(err, "PostSonde could not read the runtime variables.", path)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, errs.Wrap(err, errs.Parse, "The saved runtime variables could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Clear the runtime variables in the Environments view, or fix or delete the file.").
			WithDetail("path", path)
	}
	return out, nil
}

// readSecrets reads the secrets file through secretstore (it is encrypted
// once the user sets a master password); secretstore errors, such
// as secretstore.locked, are returned as they are.
func readSecrets(path string) (secretsFile, error) {
	var out secretsFile
	data, err := secretstore.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if secretstore.IsError(err) {
		return out, err
	}
	if err != nil {
		return out, failed(err, "PostSonde could not read the runtime variables.", path)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, errs.Wrap(err, errs.Parse, "The saved runtime variables could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Clear the runtime variables in the Environments view, or fix or delete the file.").
			WithDetail("path", path)
	}
	return out, nil
}

func writeSecrets(path string, f secretsFile) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return failed(err, "PostSonde could not save the runtime variables.", filepath.Dir(path))
	}
	if err := secretstore.WriteJSON(path, f, filePerm); err != nil {
		if secretstore.IsError(err) {
			return err
		}
		return failed(err, "PostSonde could not save the runtime variables.", path)
	}
	return nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return failed(err, "PostSonde could not save the runtime variables.", filepath.Dir(path))
	}
	if err := atomicfile.WriteJSON(path, v, filePerm); err != nil {
		return failed(err, "PostSonde could not save the runtime variables.", path)
	}
	return nil
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}
