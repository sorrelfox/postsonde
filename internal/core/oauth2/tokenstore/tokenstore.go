// Package tokenstore keeps the OAuth 2.0 tokens an oauth2 auth obtains
// (AUTH-13) in the open workspace's local side-file directory, so they
// survive a restart without ever going into a project file:
//
//	<local>/secrets/oauth2/tokens.json   {"version": 1, "tokens": {<key>: {"accessToken": …, "expiry": …}}}
//
// A token is stored under the key of the auth configuration that got it
// (oauth2.TokenRequest.Key: a hash of the grant, the endpoints, the client
// and its secret, the scope and the other parameters), so two
// configurations never share a token and a changed field gets a new one.
// <local> is never inside the project (workspace.Manager.LocalDir), and
// every workspace, a copy of a project included, has its own file.
//
// The file is 0600 in a 0700 directory, written atomically. readFile and
// writeFile are the only places that know the format; they go through
// secretstore, which encrypts the file once the user sets a master
// password, so the store's methods do not know about encryption.
//
// A Store implements oauth2.Store, safe for concurrent use. The tokens of
// each local directory are read once, then served from memory; every change
// is written at once. A write that fails is reported to the OnError
// callback and the token is kept in memory for the session, so a send is
// never failed by the token store. Standard library only. See
// docs/auth.md#token-store.
package tokenstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

// FileName is the token file, relative to the workspace's local
// directory, slash-separated.
const FileName = "secrets/oauth2/tokens.json"

// CodeLocalDataFailed: the token file could not be read, parsed or
// written. The kind comes from the cause (parse for a broken file).
// Details: path.
const CodeLocalDataFailed = "tokenstore.local_data_failed"

const (
	fileVersion             = 1
	dirPerm     fs.FileMode = 0o700
	filePerm    fs.FileMode = 0o600
)

// LocalDir returns the open workspace's local side-file directory, or an
// error when no workspace is open (workspace.Manager.LocalDir).
type LocalDir func() (string, error)

// Option configures a Store.
type Option func(*Store)

// WithOnError reports errors reading or writing the token file, which the
// oauth2.Store methods cannot return (the app logs them).
func WithOnError(fn func(error)) Option { return func(s *Store) { s.onError = fn } }

// Store is the persistent token store.
type Store struct {
	dir     LocalDir
	onError func(error)

	mu sync.Mutex
	// tokens are the tokens by local directory ("" without an open
	// workspace, in memory only), then by key; a directory is read on its
	// first use.
	tokens map[string]map[string]oauth2.Token
}

var _ oauth2.Store = (*Store)(nil)

// New returns a Store over the local directory dir returns. A nil dir, or
// one that fails, keeps the tokens in memory for the session.
func New(dir LocalDir, opts ...Option) *Store {
	s := &Store{dir: dir, tokens: map[string]map[string]oauth2.Token{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Path returns the token file of the open workspace; "" without one.
func (s *Store) Path() string {
	d := s.localDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, filepath.FromSlash(FileName))
}

// Get implements oauth2.Store.
func (s *Store) Get(key string) (oauth2.Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.load(s.localDir())[key]
	return t, ok
}

// Put implements oauth2.Store.
func (s *Store) Put(key string, t oauth2.Token) {
	s.change(func(m map[string]oauth2.Token) bool {
		m[key] = t
		return true
	})
}

// Delete implements oauth2.Store.
func (s *Store) Delete(key string) {
	s.change(func(m map[string]oauth2.Token) bool {
		if _, ok := m[key]; !ok {
			return false
		}
		delete(m, key)
		return true
	})
}

// Clear removes every token of the open workspace, and its file ("Clear
// local data" offers it as one action).
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.localDir()
	s.tokens[dir] = map[string]oauth2.Token{}
	if dir == "" {
		return nil
	}
	return removeFile(filepath.Join(dir, filepath.FromSlash(FileName)))
}

// Entry is a stored token and its key.
type Entry struct {
	Key   string
	Token oauth2.Token
}

// List returns the tokens of the open workspace, by key.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load(s.localDir())
	out := make([]Entry, 0, len(m))
	for k, t := range m {
		out = append(out, Entry{Key: k, Token: t})
	}
	slices.SortFunc(out, func(a, b Entry) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return out
}

// Values returns the access, refresh and ID tokens of the open workspace:
// what a copied or saved log must not show (DBG-07, logexport).
func (s *Store) Values() []string {
	var out []string
	for _, e := range s.List() {
		out = append(out, e.Token.Secrets()...)
	}
	return out
}

// change applies fn to the open workspace's tokens and, when it changed
// them, writes the file. While the secrets are locked the write fails
// (reported) and the file is left as it was.
func (s *Store) change(fn func(map[string]oauth2.Token) bool) {
	s.mu.Lock()
	dir := s.localDir()
	m := s.load(dir)
	if !fn(m) {
		s.mu.Unlock()
		return
	}
	var err error
	if dir != "" {
		err = writeFile(filepath.Join(dir, filepath.FromSlash(FileName)), m)
	}
	s.mu.Unlock()
	if err != nil {
		s.report(err)
	}
}

// load returns the tokens of dir, reading its file on first use. A file
// that cannot be read leaves no tokens (they are got again) and is
// reported; it is replaced with the next change. While the secrets are
// locked (secretstore.locked) nothing is kept, so the file is read again
// once they are unlocked, and a change is not written over it.
func (s *Store) load(dir string) map[string]oauth2.Token {
	if m, ok := s.tokens[dir]; ok {
		return m
	}
	m := map[string]oauth2.Token{}
	if dir != "" {
		read, err := readFile(filepath.Join(dir, filepath.FromSlash(FileName)))
		switch {
		case secretstore.IsLocked(err):
			return m
		case err != nil:
			s.report(err)
		default:
			m = read
		}
	}
	s.tokens[dir] = m
	return m
}

// Forget drops the tokens held in memory; they are read from the file
// again on next use. The app calls it when the secrets are locked,
// so no token stays usable while they are.
func (s *Store) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

// localDir returns the open workspace's local directory, "" for none.
func (s *Store) localDir() string {
	if s.dir == nil {
		return ""
	}
	d, err := s.dir()
	if err != nil {
		return ""
	}
	return d
}

func (s *Store) report(err error) {
	if s.onError != nil {
		s.onError(err)
	}
}

// file is the token file.
type file struct {
	Version int                  `json:"version"`
	Tokens  map[string]tokenJSON `json:"tokens"`
}

// tokenJSON is a token in the file; times are RFC 3339, "" for none.
type tokenJSON struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType,omitempty"`
	ExpiresIn    int64  `json:"expiresIn,omitempty"`
	Expiry       string `json:"expiry,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"idToken,omitempty"`
	Obtained     string `json:"obtained,omitempty"`
}

func toJSON(t oauth2.Token) tokenJSON {
	return tokenJSON{
		AccessToken: t.AccessToken, TokenType: t.TokenType, ExpiresIn: t.ExpiresIn, Expiry: formatTime(t.Expiry),
		RefreshToken: t.RefreshToken, Scope: t.Scope, IDToken: t.IDToken, Obtained: formatTime(t.Obtained),
	}
}

func fromJSON(j tokenJSON) oauth2.Token {
	return oauth2.Token{
		AccessToken: j.AccessToken, TokenType: j.TokenType, ExpiresIn: j.ExpiresIn, Expiry: parseTime(j.Expiry),
		RefreshToken: j.RefreshToken, Scope: j.Scope, IDToken: j.IDToken, Obtained: parseTime(j.Obtained),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime reads a time of the file; one that does not parse is zero (a
// token without a known expiry, which the server's 401 drops).
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readFile reads the tokens of path; a missing file has none.
func readFile(path string) (map[string]oauth2.Token, error) {
	out := map[string]oauth2.Token{}
	data, err := secretstore.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if secretstore.IsError(err) {
		return nil, err
	}
	if err != nil {
		return nil, failed(err, "PostSonde could not read its stored OAuth 2.0 tokens.", path)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The stored OAuth 2.0 tokens could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("The file is replaced when the next token is stored; the tokens are got again.").
			WithDetail("path", path)
	}
	for k, t := range f.Tokens {
		if t.AccessToken != "" {
			out[k] = fromJSON(t)
		}
	}
	return out, nil
}

// writeFile replaces path with tokens; no tokens removes it.
func writeFile(path string, tokens map[string]oauth2.Token) error {
	if len(tokens) == 0 {
		return removeFile(path)
	}
	f := file{Version: fileVersion, Tokens: make(map[string]tokenJSON, len(tokens))}
	for k, t := range tokens {
		f.Tokens[k] = toJSON(t)
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return failed(err, "PostSonde could not store an OAuth 2.0 token.", filepath.Dir(path))
	}
	if err := secretstore.WriteJSON(path, f, filePerm); err != nil {
		if secretstore.IsError(err) {
			return err
		}
		return failed(err, "PostSonde could not store an OAuth 2.0 token.", path)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not remove its stored OAuth 2.0 tokens.", path)
	}
	return nil
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}
