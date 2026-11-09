package environments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Names in the workspace's local directory.
const (
	// ActiveFileName holds the active environment's ID.
	ActiveFileName = "environments.json"
	// SecretsDir holds one file of secret values per environment, read and
	// written through secretstore (encrypted with a master password).
	SecretsDir = "secrets/environments"
)

// Permissions of local files: only the user may read them.
const (
	localDirPerm  fs.FileMode = 0o700
	localFilePerm fs.FileMode = 0o600
)

const (
	activeVersion  = 1
	secretsVersion = 1
)

// activeFile is <local>/environments.json.
type activeFile struct {
	Version  int    `json:"version"`
	ActiveID string `json:"activeId"`
}

// secretsFile is <local>/secrets/environments/<key>.json.
type secretsFile struct {
	Version int `json:"version"`
	// EnvironmentID is the environment the values belong to.
	EnvironmentID string `json:"environmentId"`
	// Values are the secret values by variable name.
	Values map[string]string `json:"values"`
}

// ActiveID returns the saved active environment ID without loading the
// environment (it may no longer exist); "" when there is none. The proxy
// route reads it on every dial (docs/network.md#proxy-scopes).
func (s *Store) ActiveID() string { return s.readActive() }

// readActive returns the saved active environment ID; "" when there is
// none or the file cannot be read (it is only a preference).
func (s *Store) readActive() string {
	data, err := os.ReadFile(filepath.Join(s.local, ActiveFileName))
	if err != nil {
		return ""
	}
	var f activeFile
	if json.Unmarshal(data, &f) != nil {
		return ""
	}
	return f.ActiveID
}

func (s *Store) writeActive(id string) error {
	path := filepath.Join(s.local, ActiveFileName)
	if err := os.MkdirAll(s.local, localDirPerm); err != nil {
		return localDataFailed(err, s.local)
	}
	if err := atomicfile.WriteJSON(path, activeFile{Version: activeVersion, ActiveID: id}, localFilePerm); err != nil {
		return localDataFailed(err, path)
	}
	return nil
}

func (s *Store) secretsPath(id string) string {
	return filepath.Join(s.local, filepath.FromSlash(SecretsDir), SecretsKey(id)+".json")
}

// readSecrets returns the secret values of an environment. A missing file
// is no values; a file that cannot be parsed is an error, so a save never
// overwrites values it could not read.
func (s *Store) readSecrets(id string) (map[string]string, error) {
	path := s.secretsPath(id)
	data, err := secretstore.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if secretstore.IsError(err) {
		return nil, err
	}
	if err != nil {
		return nil, localDataFailed(err, path)
	}
	var f secretsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The saved secret values of an environment could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Fix or delete the file; its secret values then have to be entered again.").
			WithDetail("path", path)
	}
	if f.Values == nil {
		f.Values = map[string]string{}
	}
	return f.Values, nil
}

// writeSecrets replaces the secret values of an environment; no values
// removes the file.
func (s *Store) writeSecrets(id string, values map[string]string) error {
	if len(values) == 0 {
		return s.removeSecrets(id)
	}
	path := s.secretsPath(id)
	if err := os.MkdirAll(filepath.Dir(path), localDirPerm); err != nil {
		return localDataFailed(err, filepath.Dir(path))
	}
	f := secretsFile{Version: secretsVersion, EnvironmentID: id, Values: values}
	if err := secretstore.WriteJSON(path, f, localFilePerm); err != nil {
		if secretstore.IsError(err) {
			return err
		}
		return localDataFailed(err, path)
	}
	return nil
}

// moveOutSecrets moves the values of f's secret variables that were typed
// into the file by hand to the secrets file, and clears them in f, before
// f is written again for another reason (a rename). A value is stored
// only when the secrets file has none for that name: the local value is
// the one shown and sent (toEnvironment). When the secrets file cannot be
// read or written, f is left as it is and the error returned, so the
// caller writes nothing and no value is lost.
func (s *Store) moveOutSecrets(f *projectfile.Environment) error {
	if !slices.ContainsFunc(f.Variables, func(v projectfile.Variable) bool { return v.Secret && v.Value != "" }) {
		return nil
	}
	secrets, err := s.readSecrets(f.ID)
	if err != nil {
		return err
	}
	vars := slices.Clone(f.Variables)
	for i, v := range vars {
		if !v.Secret || v.Value == "" {
			continue
		}
		if _, ok := secrets[v.Name]; !ok {
			secrets[v.Name] = v.Value
		}
		vars[i].Value = ""
	}
	if err := s.writeSecrets(f.ID, secrets); err != nil {
		return err
	}
	f.Variables = vars
	return nil
}

func (s *Store) removeSecrets(id string) error {
	path := s.secretsPath(id)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return localDataFailed(err, path)
	}
	return nil
}

func localDataFailed(err error, path string) error {
	return errs.Wrap(err, errs.KindOf(err), "PostSonde could not save its local environment data for this project.").
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}

// maxPlainKey is the longest environment ID used as a file name as is.
const maxPlainKey = 64

// SecretsKey returns the file name (without extension) of an environment's
// secrets file. A generated ID ("env_" and lower-case letters, digits, "_"
// or "-") is used as it is; any other ID is replaced by "x." and a hash, so
// it cannot escape the directory or clash on a case-insensitive disk.
func SecretsKey(id string) string {
	if rest, ok := strings.CutPrefix(id, projectfile.EnvironmentIDPrefix); ok && rest != "" && len(id) <= maxPlainKey {
		plain := true
		for _, c := range rest {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				plain = false
				break
			}
		}
		if plain {
			return id
		}
	}
	sum := sha256.Sum256([]byte(id))
	return "x." + hex.EncodeToString(sum[:16])
}

// maxSlugLength bounds the file name made from an environment name.
const maxSlugLength = 60

// slug turns an environment name into a file name without extension:
// lower-case ASCII letters and digits, other runs of characters as "-".
// A name with none of them gives "environment"; Windows device names get
// "-env" appended.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			if b.Len() >= maxSlugLength {
				break
			}
			continue
		}
		dash = true
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "environment"
	}
	if isWindowsDeviceName(out) {
		out += "-env"
	}
	return out
}

func isWindowsDeviceName(s string) bool {
	switch s {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(s) == 4 && (strings.HasPrefix(s, "com") || strings.HasPrefix(s, "lpt")) {
		return s[3] >= '1' && s[3] <= '9'
	}
	return false
}

// uniqueFileName returns base.json, or base-2.json, base-3.json, … when an
// entry of that name exists in dir, ignoring case. The entry named current
// (the file being renamed) does not count.
func uniqueFileName(dir, base, current string) (string, error) {
	des, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errs.Wrap(err, errs.KindOf(err), "The environments could not be read.").
			WithCode(CodeListFailed).
			WithDetail("path", dir)
	}
	taken := make(map[string]bool, len(des))
	for _, de := range des {
		if !strings.EqualFold(de.Name(), current) {
			taken[strings.ToLower(de.Name())] = true
		}
	}
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		name += projectfile.Ext
		if !taken[strings.ToLower(name)] {
			return name, nil
		}
	}
}
