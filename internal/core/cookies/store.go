package cookies

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// FileName is the cookie file in a workspace's local directory: the
// workspace's shared jar, used by every environment unless the workspace
// keeps a jar per environment (CK-03).
const FileName = "cookies.json"

// JarsDir is the directory, in a workspace's local directory, of the named
// jars: one <name>.json per environment when the workspace keeps separate
// jars per environment (see Settings).
const JarsDir = "cookies"

// DefaultSaveDelay is how long a Store waits after a change before saving.
const DefaultSaveDelay = time.Second

// Error codes.
const (
	// CodeLocalDataFailed: the cookie file could not be read or written.
	CodeLocalDataFailed = "cookies.local_data_failed"
	// CodeFileInvalid: the cookie file is not valid; it is left alone and
	// the jar is not saved.
	CodeFileInvalid = "cookies.file_invalid"
	// CodeFileNewer: the cookie file was written by a newer PostSonde; it
	// is left alone and the jar is not saved.
	CodeFileNewer = "cookies.file_newer"
	// CodeInvalidJar: a jar name is not a plain file name.
	CodeInvalidJar = "cookies.invalid_jar"
)

const (
	fileVersion              = 1
	localDirPerm fs.FileMode = 0o700
	filePerm     fs.FileMode = 0o600
)

// cookieFile is <local>/cookies.json.
type cookieFile struct {
	Version int          `json:"version"`
	Cookies []fileCookie `json:"cookies"`
}

// fileCookie is a Cookie on disk. Header bytes need not be UTF-8, and JSON
// strings must be: a cookie with other bytes in its name, value, domain or
// path has Encoding "base64" and those four fields base64-encoded.
type fileCookie struct {
	Name       string     `json:"name"`
	Value      string     `json:"value"`
	Domain     string     `json:"domain"`
	HostOnly   bool       `json:"hostOnly"`
	Path       string     `json:"path"`
	Encoding   string     `json:"encoding,omitempty"`
	Secure     bool       `json:"secure"`
	HTTPOnly   bool       `json:"httpOnly"`
	SameSite   SameSite   `json:"sameSite,omitempty"`
	Persistent bool       `json:"persistent"`
	Expires    *time.Time `json:"expires,omitempty"`
	Created    time.Time  `json:"created"`
	LastAccess time.Time  `json:"lastAccess"`
}

const encodingBase64 = "base64"

func toFile(c Cookie) fileCookie {
	f := fileCookie{
		Name: c.Name, Value: c.Value, Domain: c.Domain, HostOnly: c.HostOnly, Path: c.Path,
		Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite, Persistent: c.Persistent,
		Created: c.Created.UTC(), LastAccess: c.LastAccess.UTC(),
	}
	if c.Persistent {
		t := c.Expires.UTC()
		f.Expires = &t
	}
	if !utf8.ValidString(c.Name) || !utf8.ValidString(c.Value) || !utf8.ValidString(c.Domain) || !utf8.ValidString(c.Path) {
		enc := base64.StdEncoding.EncodeToString
		f.Encoding = encodingBase64
		f.Name, f.Value, f.Domain, f.Path = enc([]byte(c.Name)), enc([]byte(c.Value)), enc([]byte(c.Domain)), enc([]byte(c.Path))
	}
	return f
}

func fromFile(f fileCookie) (Cookie, bool) {
	c := Cookie{
		Name: f.Name, Value: f.Value, Domain: f.Domain, HostOnly: f.HostOnly, Path: f.Path,
		Secure: f.Secure, HTTPOnly: f.HTTPOnly, SameSite: f.SameSite, Persistent: f.Persistent,
		Created: f.Created, LastAccess: f.LastAccess,
	}
	switch f.Encoding {
	case "":
	case encodingBase64:
		fields := []*string{&c.Name, &c.Value, &c.Domain, &c.Path}
		for _, p := range fields {
			b, err := base64.StdEncoding.DecodeString(*p)
			if err != nil {
				return c, false
			}
			*p = string(b)
		}
	default:
		return c, false
	}
	if c.Persistent {
		if f.Expires == nil {
			return c, false
		}
		c.Expires = *f.Expires
	}
	return c, true
}

// Store is a Jar kept in a workspace's local directory. Changes are saved
// DefaultSaveDelay (Options.SaveDelay) after they happen, and on Flush and
// Close. It is safe for concurrent use.
type Store struct {
	jar     *Jar
	dir     string
	path    string
	delay   time.Duration
	onError func(error)
	// problem, when set, is why the file could not be loaded; the jar then
	// works in memory and is never saved, so the file is not overwritten.
	problem error

	mu     sync.Mutex
	timer  *time.Timer
	dirty  bool
	closed bool
	// discarded is set by Registry.Forget: the store never writes again.
	discarded bool
}

// Open loads the cookie jar of the workspace whose local directory is
// localDir from <localDir>/cookies.json; a missing file is an empty jar.
//
// A file that cannot be parsed or is from a newer version is not an error:
// the Store works in memory, Problem reports it, and the file is left as
// it is. Open fails only when the file exists but cannot be read.
//
// Session cookies are saved too: like other API tools, PostSonde keeps
// them until the user clears them (docs/cookies.md).
func Open(localDir string, o Options) (*Store, error) {
	return openPath(filepath.Join(localDir, FileName), o)
}

// OpenJar loads the named jar of the workspace whose local directory is
// localDir: jar "" is the shared jar, <localDir>/cookies.json (as Open);
// any other name is <localDir>/cookies/<jar>.json. A name must be a plain
// file name (letters, digits, '.', '_' and '-', not starting with '.'),
// else OpenJar fails with CodeInvalidJar.
func OpenJar(localDir, jar string, o Options) (*Store, error) {
	path, err := JarPath(localDir, jar)
	if err != nil {
		return nil, err
	}
	return openPath(path, o)
}

// JarPath returns the file of the named jar of localDir (see OpenJar).
func JarPath(localDir, jar string) (string, error) {
	if jar == "" {
		return filepath.Join(localDir, FileName), nil
	}
	if !validJarName(jar) {
		return "", errs.New(errs.InvalidArgument, "The cookie jar name is not valid.").
			WithCode(CodeInvalidJar).
			WithHint("Use letters, digits, '.', '_' and '-' only.").
			WithDetail("jar", jar)
	}
	return filepath.Join(localDir, JarsDir, jar+".json"), nil
}

func validJarName(name string) bool {
	if name == "" || len(name) > 128 || name[0] == '.' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func openPath(path string, o Options) (*Store, error) {
	s := &Store{
		jar:     New(o),
		dir:     filepath.Dir(path),
		path:    path,
		delay:   o.SaveDelay,
		onError: o.OnSaveError,
	}
	if s.delay <= 0 {
		s.delay = DefaultSaveDelay
	}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, localDataFailed(err, s.path, "PostSonde could not read the saved cookies of this project.")
	default:
		s.problem = s.load(data)
	}
	s.jar.mu.Lock()
	s.jar.onChange = s.changed
	s.jar.mu.Unlock()
	return s, nil
}

func (s *Store) load(data []byte) error {
	var f cookieFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 {
		return errs.Wrap(err, errs.Parse, "The saved cookies of this project could not be read, so cookies are not saved.").
			WithCode(CodeFileInvalid).
			WithHint("Fix or delete the file; PostSonde then saves cookies again.").
			WithDetail("path", s.path)
	}
	if f.Version > fileVersion {
		return errs.New(errs.Unsupported, "The saved cookies were written by a newer version of PostSonde, so cookies are not saved.").
			WithCode(CodeFileNewer).
			WithHint("Update PostSonde to use them.").
			WithDetail("path", s.path).
			WithDetail("version", strconv.Itoa(f.Version))
	}
	cookies := make([]Cookie, 0, len(f.Cookies))
	for _, fc := range f.Cookies {
		if c, ok := fromFile(fc); ok {
			cookies = append(cookies, c)
		}
	}
	s.jar.load(cookies)
	return nil
}

// Jar returns the jar.
func (s *Store) Jar() *Jar { return s.jar }

// Path returns the path of the cookie file.
func (s *Store) Path() string { return s.path }

// Problem returns why the cookie file could not be loaded, or nil. When it
// is set, the jar is not saved.
func (s *Store) Problem() error { return s.problem }

// changed schedules a save.
func (s *Store) changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	if s.timer != nil || s.closed || s.problem != nil {
		return
	}
	s.timer = time.AfterFunc(s.delay, func() {
		if err := s.Flush(); err != nil && s.onError != nil {
			s.onError(err)
		}
	})
}

// Flush saves the jar now if it changed since the last save, access times
// included.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.problem != nil || s.discarded || !s.dirty && !s.jar.isTouched() {
		return nil
	}
	cookies := s.jar.snapshot()
	f := cookieFile{Version: fileVersion, Cookies: make([]fileCookie, len(cookies))}
	for i, c := range cookies {
		f.Cookies[i] = toFile(c)
	}
	if err := os.MkdirAll(s.dir, localDirPerm); err != nil {
		return localDataFailed(err, s.dir, "PostSonde could not save the cookies of this project.")
	}
	if err := atomicfile.WriteJSON(s.path, f, filePerm); err != nil {
		return localDataFailed(err, s.path, "PostSonde could not save the cookies of this project.")
	}
	s.dirty = false
	return nil
}

// Close saves pending changes and stops saving after later changes. The
// jar stays usable in memory.
func (s *Store) Close() error {
	err := s.Flush()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return err
}

func localDataFailed(err error, path, msg string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}

// Registry keeps one Store per jar of a workspace local directory (the
// shared jar and, with separate jars per environment, one per
// environment), so every send in a workspace shares its jars. It is safe
// for concurrent use.
type Registry struct {
	o Options

	mu     sync.Mutex
	stores map[jarKey]*Store
}

type jarKey struct{ dir, jar string }

// NewRegistry returns a Registry whose stores use o.
func NewRegistry(o Options) *Registry {
	return &Registry{o: o, stores: map[jarKey]*Store{}}
}

// Open returns the shared Store of localDir, opening it on first use.
func (r *Registry) Open(localDir string) (*Store, error) {
	return r.OpenJar(localDir, "")
}

// OpenJar returns the named Store of localDir ("" for the shared one, see
// OpenJar), opening it on first use.
func (r *Registry) OpenJar(localDir, jar string) (*Store, error) {
	k := jarKey{localDir, jar}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stores[k]; ok {
		return s, nil
	}
	s, err := OpenJar(localDir, jar, r.o)
	if err != nil {
		return nil, err
	}
	r.stores[k] = s
	return s, nil
}

// Close saves and forgets every Store of localDir, when its workspace is
// closed. It returns the first error.
func (r *Registry) Close(localDir string) error {
	r.mu.Lock()
	var stores []*Store
	for k, s := range r.stores {
		if k.dir == localDir {
			stores = append(stores, s)
			delete(r.stores, k)
		}
	}
	r.mu.Unlock()
	return closeStores(stores)
}

// CloseAll saves and forgets every Store, on shutdown. It returns the
// first error.
func (r *Registry) CloseAll() error {
	r.mu.Lock()
	stores := make([]*Store, 0, len(r.stores))
	for _, s := range r.stores {
		stores = append(stores, s)
	}
	r.stores = map[jarKey]*Store{}
	r.mu.Unlock()
	return closeStores(stores)
}

// Forget drops every Store of localDir without saving it, when the
// project's cookies are cleared (docs/cookies.md#clearing): each jar
// is emptied in memory, so a send that still holds one sends none of its
// cookies, and the store never writes its file again; the next open reads
// the directory afresh. The caller removes the files.
func (r *Registry) Forget(localDir string) {
	r.mu.Lock()
	var stores []*Store
	for k, s := range r.stores {
		if k.dir == localDir {
			stores = append(stores, s)
			delete(r.stores, k)
		}
	}
	r.mu.Unlock()
	for _, s := range stores {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		s.closed, s.discarded, s.dirty = true, true, false
		s.mu.Unlock()
		s.jar.Clear()
	}
}

func closeStores(stores []*Store) error {
	var first error
	for _, s := range stores {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
