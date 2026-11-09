// Package requestsecrets keeps the secret parts of a request's auth (a
// Basic password, a bearer token, an API key, a JWT key, OAuth 1.0
// consumer and token secrets, AWS and Hawk keys, an OAuth 2.0 client
// secret) out of project files (AUTH-03…05, AUTH-08, AUTH-15…18,
// STO-04).
//
// A secret member of projectfile.Auth is written to the project only when
// it is empty or made of {{variable}} references (IsReference), so a shared
// project names the secret environment variable instead of holding the
// value. A value typed as it is goes to a local side file instead:
//
//	<local>/secrets/requests/<request key>.json   {"version": 1, "itemId": …, "values": {"auth.token": …}}
//
// and the file's auth lists the member in "localSecrets", with the member
// itself "". Split does the separation before a save and Merge puts the
// value back after a load. The values of secret variables (folders and
// global variables) share the side file, keyed "var.<name>" (files.go);
// SplitFile, MergeFile and Store.MoveOut work on whole project files, so
// no writer has to know which members are secret. <local> is the workspace's local side-file
// directory (workspace.Manager.LocalDir), never inside the project. Files
// are 0600 in 0700 directories.
//
// The side file is read and written through secretstore, so it is
// encrypted once the user sets a master password (SEC-03); until
// then it is plain JSON readable by the user's account. While the secrets
// are locked, Read and Write fail with secretstore.locked.
//
// A Store is not safe for concurrent use; callers serialise access.
// Standard library only. See docs/auth.md.
package requestsecrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Dir is the directory of the side files in the workspace's local
// directory, slash-separated.
const Dir = "secrets/requests"

// CodeLocalDataFailed: a side file could not be read, parsed or written.
// The kind comes from the cause (parse for a broken file). Details: path.
const CodeLocalDataFailed = "requestsecrets.local_data_failed"

// AuthKeyPrefix prefixes the auth members in a side file's values:
// "auth.password", "auth.token", "auth.value".
const AuthKeyPrefix = "auth."

const (
	fileVersion              = 1
	dirPerm      fs.FileMode = 0o700
	filePerm     fs.FileMode = 0o600
	maxPlainKey              = 64
	hashedPrefix             = "x."
)

// sideFile is <local>/secrets/requests/<key>.json.
type sideFile struct {
	Version int `json:"version"`
	// ItemID is the request the values belong to.
	ItemID string `json:"itemId"`
	// Values are the secret values by member path ("auth.token").
	Values map[string]string `json:"values"`
}

// IsReference reports whether a secret value may be written to a project
// file: it has at least one {{variable}} reference and nothing else but
// whitespace. "{{token}}" and "{{user}} {{suffix}}" qualify; "abc{{x}}",
// "\{{x}}" (an escape) and an unterminated "{{x" do not, since their text
// may itself be part of a secret.
func IsReference(v string) bool {
	refs := 0
	for _, seg := range template.Parse(v) {
		switch seg.Kind {
		case template.Var:
			refs++
		case template.Text:
			// The source text, so an escape counts as text.
			if strings.TrimSpace(v[seg.Start:seg.End]) != "" {
				return false
			}
		default:
			return false
		}
	}
	return refs > 0
}

// Split separates a's secrets from what may be written to a project
// file. It returns the auth to write and the values for the side file
// (nil when there are none). Each secret member of a's type (one, or two
// for oauth1) is handled on its own:
//
//   - a typed value goes to the values, keyed "auth.<member>"; in the
//     result the member is "" and LocalSecrets names it;
//   - a reference (IsReference) stays in the result, not marked local;
//   - an empty member stays marked local when a marks it: its value was
//     typed on another machine and is not here, and the mark keeps saying
//     so until the member is set or the mark is removed.
//
// Types without a secret member, including types this version does not
// know, are returned unchanged.
func Split(a projectfile.Auth) (projectfile.Auth, map[string]string) {
	members := a.SecretMembers()
	if len(members) == 0 {
		return a, nil
	}
	var local []string
	var values map[string]string
	for _, member := range members {
		v := a.SecretOf(member)
		switch {
		case v == "":
			if a.IsLocal(member) {
				local = append(local, member)
			}
		case IsReference(v):
		default:
			a = a.WithSecretOf(member, "")
			local = append(local, member)
			if values == nil {
				values = map[string]string{}
			}
			values[AuthKeyPrefix+member] = v
		}
	}
	a.LocalSecrets = local
	return a, values
}

// Merge puts the local values of a's secret members back: a member that
// is "" and marked local gets values["auth.<member>"] if there is one.
// The marks stay, so Split stores the values locally again.
func Merge(a projectfile.Auth, values map[string]string) projectfile.Auth {
	for _, member := range a.SecretMembers() {
		if a.SecretOf(member) != "" || !a.IsLocal(member) {
			continue
		}
		if v, ok := values[AuthKeyPrefix+member]; ok {
			a = a.WithSecretOf(member, v)
		}
	}
	return a
}

// Missing reports whether a (after Merge) marks a secret as stored
// locally but has no value for it: it was typed on another machine.
func Missing(a projectfile.Auth) bool {
	return MissingMember(a) != ""
}

// MissingMember returns the first secret member Missing reports; "" when
// none is missing.
func MissingMember(a projectfile.Auth) string {
	for _, member := range a.SecretMembers() {
		if a.IsLocal(member) && a.SecretOf(member) == "" {
			return member
		}
	}
	return ""
}

// Store reads and writes the side files of one workspace.
type Store struct {
	local string
}

// New returns a Store for the workspace local directory localDir.
func New(localDir string) *Store { return &Store{local: localDir} }

// Path returns the side file of item id.
func (s *Store) Path(id string) string {
	return filepath.Join(s.local, filepath.FromSlash(Dir), Key(id)+".json")
}

// Read returns the values of item id; an empty map when it has no file. A
// file that cannot be parsed is an error, so nothing overwrites values
// that could not be read.
func (s *Store) Read(id string) (map[string]string, error) {
	path := s.Path(id)
	data, err := secretstore.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if secretstore.IsError(err) {
		return nil, err
	}
	if err != nil {
		return nil, failed(err, "PostSonde could not read the locally stored secrets of a request.", path)
	}
	var f sideFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The locally stored secrets of a request could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Fix or delete the file; the request's password, token or API key then has to be entered again.").
			WithDetail("path", path)
	}
	if f.Values == nil {
		f.Values = map[string]string{}
	}
	return f.Values, nil
}

// Write replaces the values of item id; no values removes its file.
func (s *Store) Write(id string, values map[string]string) error {
	if len(values) == 0 {
		return s.Remove(id)
	}
	path := s.Path(id)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return failed(err, "PostSonde could not save the secrets of a request locally.", filepath.Dir(path))
	}
	f := sideFile{Version: fileVersion, ItemID: id, Values: values}
	if err := secretstore.WriteJSON(path, f, filePerm); err != nil {
		if secretstore.IsError(err) {
			return err
		}
		return failed(err, "PostSonde could not save the secrets of a request locally.", path)
	}
	return nil
}

// Remove deletes the side file of item id, if there is one.
func (s *Store) Remove(id string) error {
	path := s.Path(id)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not remove the locally stored secrets of a request.", path)
	}
	return nil
}

// Copy gives item to the values of item from, for a duplicated request or
// folder: they are added to to's values (Update), from's winning. Nothing
// happens when from has no values.
func (s *Store) Copy(from, to string) error {
	values, err := s.Read(from)
	if err != nil {
		return err
	}
	return s.Update(to, values)
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}

// Key returns the file name (without extension) of item id's side file. A
// generated ID (a lower-case prefix and "_", then lower-case letters,
// digits, "_" or "-") is used as it is; any other ID is replaced by "x."
// and a hash, so it cannot escape the directory or clash on a
// case-insensitive disk.
func Key(id string) string {
	if isPlain(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hashedPrefix + hex.EncodeToString(sum[:16])
}

func isPlain(id string) bool {
	prefix, rest, ok := strings.Cut(id, "_")
	if !ok || prefix == "" || rest == "" || len(id) > maxPlainKey {
		return false
	}
	for _, c := range prefix {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	for _, c := range rest {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
