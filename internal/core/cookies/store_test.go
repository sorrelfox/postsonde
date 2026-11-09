package cookies

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func openStore(t *testing.T, dir string, o Options) *Store {
	t.Helper()
	s, err := Open(dir, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if p := s.Problem(); p != nil {
		t.Fatalf("Problem: %v", p)
	}
	return s
}

// TestStoreRestart simulates an app restart: cookies stored in one Store
// are there after a new Open of the same directory.
func TestStoreRestart(t *testing.T) {
	t.Parallel()
	clk := newClock()
	dir := filepath.Join(t.TempDir(), "local")
	o := Options{Now: clk.Now, SaveDelay: time.Hour}

	s := openStore(t, dir, o)
	j := s.Jar()
	set(t, j, "https://api.example.com/v1/login",
		"session=abc; HttpOnly; Secure; SameSite=Lax",
		"pref=dark; Domain=example.com; Path=/; Max-Age=3600",
		"__Host-id=1; Secure; Path=/")
	clk.Advance(time.Second)
	set(t, j, "http://localhost:8080/", "bin=\xff\xfe; Path=/", "noname")
	clk.Advance(time.Second)
	j.Header(mustURL(t, "https://api.example.com/v1/x")) // updates access times
	want := j.All()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("cookie file mode = %v, want 0600", perm)
		}
	}
	if s.Path() != filepath.Join(dir, FileName) {
		t.Errorf("Path = %q", s.Path())
	}

	s2 := openStore(t, dir, o)
	got := s2.Jar().All()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after restart:\n%+v\nwant\n%+v", got, want)
	}
	header(t, s2.Jar(), "https://api.example.com/v1/x", "session=abc; pref=dark; __Host-id=1")
	header(t, s2.Jar(), "http://localhost/", "bin=\xff\xfe; noname")

	// Expired cookies are dropped on load; session cookies stay.
	clk.Advance(2 * time.Hour)
	s3 := openStore(t, dir, o)
	header(t, s3.Jar(), "https://www.example.com/", "")
	header(t, s3.Jar(), "https://api.example.com/v1/x", "session=abc; __Host-id=1")
}

func TestStoreFileFormat(t *testing.T) {
	t.Parallel()
	clk := newClock()
	dir := t.TempDir()
	s := openStore(t, dir, Options{Now: clk.Now, SaveDelay: time.Hour})
	set(t, s.Jar(), "http://example.com/", "a=1; Max-Age=60; SameSite=Strict", "bin=\xff")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int              `json:"version"`
		Cookies []map[string]any `json:"cookies"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || len(f.Cookies) != 2 {
		t.Fatalf("file = %s", data)
	}
	a := f.Cookies[0]
	if a["name"] != "a" || a["value"] != "1" || a["domain"] != "example.com" || a["hostOnly"] != true ||
		a["sameSite"] != "Strict" || a["persistent"] != true || a["expires"] != "2021-01-01T12:01:00Z" {
		t.Errorf("cookie a = %v", a)
	}
	if b := f.Cookies[1]; b["encoding"] != "base64" || b["value"] != "/w==" || b["name"] != "Ymlu" {
		t.Errorf("cookie bin = %v", b)
	}
	if strings.Contains(string(data), "\t") || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("not in the stablejson format:\n%s", data)
	}

	// Flush without changes writes nothing.
	if err := os.Remove(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Flush without changes wrote the file: %v", err)
	}
}

func TestStoreDebouncedSave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var saveErrors atomic.Int32
	s := openStore(t, dir, Options{SaveDelay: 20 * time.Millisecond, OnSaveError: func(error) { saveErrors.Add(1) }})
	defer s.Close()
	path := filepath.Join(dir, FileName)
	for i := range 5 {
		s.Jar().SetCookieLines(mustURL(t, "http://example.com/"), []string{"n=" + strings.Repeat("x", i+1)})
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved before the delay: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), `"xxxxx"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not saved after the delay: %v %s", err, data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := saveErrors.Load(); n != 0 {
		t.Errorf("%d save errors", n)
	}

	// Deleting and clearing are changes too.
	s.Jar().Clear()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openStore(t, dir, Options{})
	if n := s2.Jar().Len(); n != 0 {
		t.Errorf("Len after Clear and restart = %d", n)
	}
	// After Close, changes are kept in memory only.
	set(t, s.Jar(), "http://example.com/", "late=1")
	time.Sleep(50 * time.Millisecond)
	s3 := openStore(t, dir, Options{})
	if n := s3.Jar().Len(); n != 0 {
		t.Errorf("change after Close was saved")
	}
}

func TestStoreBadFile(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"invalid": "{not json",
		"version": `{"version": 0, "cookies": []}`,
		"newer":   `{"version": 99, "cookies": [{"name": "future"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir, Options{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			wantCode := CodeFileInvalid
			if name == "newer" {
				wantCode = CodeFileNewer
			}
			var e *errs.Error
			if !errors.As(s.Problem(), &e) || e.Code != wantCode || e.Details["path"] != path {
				t.Fatalf("Problem = %v, want code %s", s.Problem(), wantCode)
			}
			// The jar works, but the file is never overwritten.
			set(t, s.Jar(), "http://example.com/", "a=1")
			header(t, s.Jar(), "http://example.com/", "a=1")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(path); string(data) != content {
				t.Errorf("file overwritten: %s", data)
			}
		})
	}

	t.Run("bad entries skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		content := `{"version": 1, "cookies": [
			{"name": "ok", "value": "1", "domain": "example.com", "hostOnly": true, "path": "/"},
			{"name": "nodomain", "value": "1", "domain": "", "path": "/"},
			{"name": "relpath", "value": "1", "domain": "example.com", "path": "x"},
			{"name": "noexpiry", "value": "1", "domain": "example.com", "path": "/", "persistent": true},
			{"name": "badenc", "value": "!!", "domain": "example.com", "path": "/", "encoding": "base64"},
			{"name": "unknownenc", "value": "1", "domain": "example.com", "path": "/", "encoding": "rot13"},
			{"name": "extra", "value": "1", "domain": "EXAMPLE.com", "hostOnly": true, "path": "/x", "unknownKey": 1}
		]}`
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s := openStore(t, dir, Options{Now: newClock().Now})
		header(t, s.Jar(), "http://example.com/x", "extra=1; ok=1")
	})

	t.Run("unreadable", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// A directory where the file should be cannot be read as a file.
		if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := Open(dir, Options{})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeLocalDataFailed {
			t.Fatalf("Open = %v, want %s", err, CodeLocalDataFailed)
		}
	})

	t.Run("unwritable", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs Unix permissions")
		}
		dir := t.TempDir()
		s := openStore(t, dir, Options{SaveDelay: time.Hour})
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		set(t, s.Jar(), "http://example.com/", "a=1")
		var e *errs.Error
		if err := s.Flush(); !errors.As(err, &e) || e.Code != CodeLocalDataFailed {
			t.Fatalf("Flush = %v, want %s", err, CodeLocalDataFailed)
		}
	})
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	r := NewRegistry(Options{SaveDelay: time.Hour})
	sa, err := r.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.Open(a); again != sa {
		t.Error("Open(a) twice returned different stores")
	}
	sb, err := r.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	set(t, sa.Jar(), "http://example.com/", "in=a")
	set(t, sb.Jar(), "http://example.com/", "in=b")
	if err := r.Close(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(a); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.Open(a); again == sa {
		t.Error("Open after Close returned the closed store")
	}
	if err := r.CloseAll(); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{a: "in=a", b: "in=b"} {
		s := openStore(t, dir, Options{})
		header(t, s.Jar(), "http://example.com/", want)
	}
	if _, err := r.Open(filepath.Join(base, "c")); err != nil {
		t.Fatal(err)
	}
}

// TestNamedJars covers CK-03's storage: named jars live in
// <local>/cookies/<name>.json, apart from the shared cookies.json and from
// each other, and Registry.Close closes every jar of a directory.
func TestNamedJars(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := NewRegistry(Options{SaveDelay: time.Hour})
	shared, err := r.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.OpenJar(dir, ""); again != shared {
		t.Error(`OpenJar(dir, "") is not the shared store`)
	}
	x, err := r.OpenJar(dir, "env_x")
	if err != nil {
		t.Fatal(err)
	}
	y, err := r.OpenJar(dir, "x.0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	if x == shared || y == x {
		t.Fatal("named jars share a store")
	}
	if want := filepath.Join(dir, JarsDir, "env_x.json"); x.Path() != want {
		t.Errorf("Path = %q, want %q", x.Path(), want)
	}
	set(t, shared.Jar(), "http://example.com/", "in=shared")
	set(t, x.Jar(), "http://example.com/", "in=x")
	header(t, y.Jar(), "http://example.com/", "")
	if err := r.Close(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.OpenJar(dir, "env_x"); again == x {
		t.Error("Close(dir) kept a named store open")
	}
	header(t, openStore(t, dir, Options{}).Jar(), "http://example.com/", "in=shared")
	sx, err := OpenJar(dir, "env_x", Options{})
	if err != nil {
		t.Fatal(err)
	}
	header(t, sx.Jar(), "http://example.com/", "in=x")
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, JarsDir)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("jars directory: %v, %v", fi, err)
		}
	}
	for _, bad := range []string{".hidden", "a/b", `a\b`, "..", "a b", strings.Repeat("a", 129)} {
		if _, err := r.OpenJar(dir, bad); !errors.Is(err, &errs.Error{Code: CodeInvalidJar}) {
			t.Errorf("OpenJar(%q) = %v, want %s", bad, err, CodeInvalidJar)
		}
	}
	_ = r.CloseAll()
}

func TestSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := ReadSettings(dir); got.PerEnvironment {
		t.Errorf("default = %+v", got)
	}
	if err := WriteSettings(dir, Settings{PerEnvironment: true}); err != nil {
		t.Fatal(err)
	}
	if got := ReadSettings(dir); !got.PerEnvironment {
		t.Errorf("after write = %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, SettingsFileName))
	if err != nil || !strings.Contains(string(data), `"perEnvironment": true`) || !strings.Contains(string(data), `"version": 1`) {
		t.Errorf("file = %s, %v", data, err)
	}
	for _, content := range []string{"{", `{"version": 2, "perEnvironment": true}`, `{"perEnvironment": true}`} {
		if err := os.WriteFile(filepath.Join(dir, SettingsFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ReadSettings(dir); got.PerEnvironment {
			t.Errorf("ReadSettings(%s) = %+v, want the default", content, got)
		}
	}
}

// Forget empties the jars of a directory in memory and keeps them
// from writing their files again; other directories keep theirs.
func TestRegistryForget(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	r := NewRegistry(Options{SaveDelay: time.Millisecond})
	shared, err := r.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	named, err := r.OpenJar(a, "env_x")
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	set(t, shared.Jar(), "http://example.com/", "in=a")
	set(t, named.Jar(), "http://example.com/", "in=x")
	set(t, other.Jar(), "http://example.com/", "in=b")
	if err := shared.Flush(); err != nil {
		t.Fatal(err)
	}

	r.Forget(a)
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	header(t, shared.Jar(), "http://example.com/", "")
	header(t, named.Jar(), "http://example.com/", "")
	// A send still holding the old jar stores a cookie: it is not written.
	set(t, shared.Jar(), "http://example.com/", "late=1")
	if err := shared.Flush(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the forgotten jars wrote %s again: %v", a, err)
	}
	if again, _ := r.Open(a); again == shared || again.Jar().Len() != 0 {
		t.Error("Open after Forget returned the old store or its cookies")
	}
	header(t, other.Jar(), "http://example.com/", "in=b")
	if err := r.CloseAll(); err != nil {
		t.Fatal(err)
	}
}
