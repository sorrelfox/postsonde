// Package appsecrets keeps secrets that belong to the app rather than to a
// project, such as the passphrases of the per-host client certificates
// (AUTH-30), out of settings.json. They live in one side file in the data
// directory:
//
//	<data>/secrets/app.json   {"version": 1, "values": {"clientCert.<id>.passphrase": …}}
//
// The file is 0600 in a 0700 directory, written atomically through
// secretstore, which encrypts it once the user sets a master password
// (SEC-03); while the secrets are locked, reading or writing it
// fails with secretstore.locked. With no data directory (tests, a data
// directory that cannot be used) the values are kept in memory for the
// session only.
//
// The passphrases of the local servers' certificate files (SRV-05) are
// kept here too, by file (ServerCertPassphraseKey), and the password of
// the manual proxy (ProxyPasswordKey, SOCKS-04).
//
// A Store is safe for concurrent use. Standard library only. See
// docs/privacy.md.
package appsecrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

// FileName is the side file, relative to the data directory.
const FileName = "secrets/app.json"

// CodeLocalDataFailed: the side file could not be read, parsed or
// written. The kind comes from the cause (parse for a broken file).
// Details: path.
const CodeLocalDataFailed = "appsecrets.local_data_failed"

const (
	fileVersion             = 1
	dirPerm     fs.FileMode = 0o700
	filePerm    fs.FileMode = 0o600
)

type sideFile struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// Store reads and writes the side file.
type Store struct {
	path string // "" keeps the values in memory

	mu  sync.Mutex
	mem map[string]string
}

// New returns a Store for the data directory dataDir; "" keeps the values
// in memory only.
func New(dataDir string) *Store {
	s := &Store{mem: map[string]string{}}
	if dataDir != "" {
		s.path = filepath.Join(dataDir, filepath.FromSlash(FileName))
	}
	return s
}

// Path returns the side file, "" in memory.
func (s *Store) Path() string { return s.path }

// Get returns the value of key, "" when there is none.
func (s *Store) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.read()
	if err != nil {
		return "", err
	}
	return values[key], nil
}

// Values returns every stored value, in no particular order: what a copied
// or saved log must not show (DBG-07, logexport).
func (s *Store) Values() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out, nil
}

// Set stores value under key; an empty value removes the key.
func (s *Store) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.read()
	if err != nil {
		return err
	}
	if value == "" {
		if _, ok := values[key]; !ok {
			return nil
		}
		delete(values, key)
	} else {
		values[key] = value
	}
	return s.write(values)
}

// Delete removes key.
func (s *Store) Delete(key string) error { return s.Set(key, "") }

// read returns a copy of the values. A missing file has none; a file that
// cannot be parsed is an error, so nothing overwrites values that could
// not be read.
func (s *Store) read() (map[string]string, error) {
	out := map[string]string{}
	if s.path == "" {
		for k, v := range s.mem {
			out[k] = v
		}
		return out, nil
	}
	data, err := secretstore.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if secretstore.IsError(err) {
		return nil, err
	}
	if err != nil {
		return nil, failed(err, "PostSonde could not read its locally stored secrets.", s.path)
	}
	var f sideFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The locally stored secrets of PostSonde could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Fix or delete the file; certificate passphrases and the proxy password then have to be entered again.").
			WithDetail("path", s.path)
	}
	for k, v := range f.Values {
		out[k] = v
	}
	return out, nil
}

func (s *Store) write(values map[string]string) error {
	if s.path == "" {
		s.mem = values
		return nil
	}
	if len(values) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return failed(err, "PostSonde could not remove its locally stored secrets.", s.path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), dirPerm); err != nil {
		return failed(err, "PostSonde could not save a secret locally.", filepath.Dir(s.path))
	}
	if err := secretstore.WriteJSON(s.path, sideFile{Version: fileVersion, Values: values}, filePerm); err != nil {
		if secretstore.IsError(err) {
			return err
		}
		return failed(err, "PostSonde could not save a secret locally.", s.path)
	}
	return nil
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}

// ClientCertPassphraseKey is the key of the passphrase of the per-host
// client certificate with entry ID id.
func ClientCertPassphraseKey(id string) string { return "clientCert." + id + ".passphrase" }

// ProxyPasswordKey is the key of the password of the manual proxy of the
// settings (network.proxyUrl, SOCKS-04), whose user name is in the URL.
const ProxyPasswordKey = "proxy.password"

// ServerCertPassphraseKey is the key of the passphrase of a local server's
// certificate file (SRV-05): the PKCS#12 file or encrypted PEM key at
// path, named by a hash of the path so the key does not show it.
func ServerCertPassphraseKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "serverCert." + hex.EncodeToString(sum[:8]) + ".passphrase"
}
