// Package localvalues keeps the local-only values of variables (SEC-02):
// a value that stays on this computer and, here, replaces the
// variable's shared value in the project file. A personal token, a local
// base URL: teammates who open the same project see and send the shared
// value; this computer sends its own.
//
// The values live in the workspace's local side-file directory, next to
// the secret values, one file per owner of variables:
//
//	<local>/local-values/environments/<environment id>.json   an environment's variables
//	<local>/local-values/global/<workspace id>.json           the global variables (postsonde.json)
//
// Each file is {"version": 1, "owner": <id>, "values": {<variable name>:
// <local value>}}, 0600 in 0700 directories, written atomically. <local> is
// never inside the project (workspace.Manager.LocalDir), so a local value
// cannot reach a project file, an export or a commit.
//
// A local value is not a secret: it is shown, sent, logged and recorded
// like any value. A variable whose value must not be shown is marked secret
// instead (its value is always local, see requestsecrets and
// environments). Local values are kept apart from secrets on purpose: a
// store that encrypts or locks secrets leaves them readable, and a
// log masker never treats them as secrets.
//
// Store is the one reader and writer of these files. It is not safe for
// concurrent use; callers serialise access. Standard library only. See
// docs/environments.md#local-values.
package localvalues

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Dir is the directory of the files in the workspace's local directory,
// slash-separated.
const Dir = "local-values"

// CodeLocalDataFailed: a file of local values could not be read, parsed or
// written. The kind comes from the cause (parse for a broken file).
// Details: path.
const CodeLocalDataFailed = "localvalues.local_data_failed"

// Kinds of owner: the subdirectory of an owner's file.
const (
	// KindEnvironment is an environment's variables, by environment ID.
	KindEnvironment = "environments"
	// KindGlobal is the project's global variables, by workspace ID.
	KindGlobal = "global"
)

const (
	fileVersion             = 1
	dirPerm     fs.FileMode = 0o700
	filePerm    fs.FileMode = 0o600
)

// Owner is what a set of variables belongs to.
type Owner struct {
	// Kind is KindEnvironment or KindGlobal.
	Kind string
	// ID is the environment's or the workspace's ID.
	ID string
}

// Environment is the owner of an environment's variables.
func Environment(id string) Owner { return Owner{Kind: KindEnvironment, ID: id} }

// Global is the owner of the global variables of workspace id.
func Global(workspaceID string) Owner { return Owner{Kind: KindGlobal, ID: workspaceID} }

// file is <local>/local-values/<kind>/<key>.json.
type file struct {
	Version int `json:"version"`
	// Owner is the ID the values belong to.
	Owner string `json:"owner"`
	// Values are the local values by variable name.
	Values map[string]string `json:"values"`
}

// Store reads and writes the local values of one workspace.
type Store struct {
	local string
}

// New returns a Store for the workspace local directory localDir.
func New(localDir string) *Store { return &Store{local: localDir} }

// Path returns the file of owner o. An ID that is not a generated one is
// hashed (requestsecrets.Key), so it cannot escape the directory.
func (s *Store) Path(o Owner) string {
	kind := o.Kind
	if kind != KindEnvironment && kind != KindGlobal {
		kind = requestsecrets.Key(kind)
	}
	return filepath.Join(s.local, filepath.FromSlash(Dir), kind, requestsecrets.Key(o.ID)+".json")
}

// Read returns the local values of o by variable name; an empty map when it
// has no file. A file that cannot be parsed is an error, so nothing
// overwrites values that could not be read.
func (s *Store) Read(o Owner) (map[string]string, error) {
	path := s.Path(o)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, failed(err, "PostSonde could not read the local values of variables.", path)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The local values of variables could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Fix or delete the file; the local values then have to be entered again.").
			WithDetail("path", path)
	}
	if f.Values == nil {
		f.Values = map[string]string{}
	}
	return f.Values, nil
}

// Write replaces the local values of o; no values removes its file.
func (s *Store) Write(o Owner, values map[string]string) error {
	if len(values) == 0 {
		return s.Remove(o)
	}
	path := s.Path(o)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return failed(err, "PostSonde could not save the local values of variables.", filepath.Dir(path))
	}
	if err := atomicfile.WriteJSON(path, file{Version: fileVersion, Owner: o.ID, Values: values}, filePerm); err != nil {
		return failed(err, "PostSonde could not save the local values of variables.", path)
	}
	return nil
}

// Remove deletes the file of o, if there is one.
func (s *Store) Remove(o Owner) error {
	path := s.Path(o)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not remove the local values of variables.", path)
	}
	return nil
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}
