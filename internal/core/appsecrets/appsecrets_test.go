package appsecrets

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	key := ClientCertPassphraseKey("cc_1")

	if v, err := s.Get(key); err != nil || v != "" {
		t.Fatalf("Get before any file = %q, %v", v, err)
	}
	if err := s.Set(key, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if v, err := New(dir).Get(key); err != nil || v != "correct horse" {
		t.Fatalf("Get from a new Store = %q, %v", v, err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
	if !strings.HasPrefix(s.Path(), dir) {
		t.Errorf("Path = %s, not in the data directory", s.Path())
	}

	// A second key keeps the first; deleting the last removes the file.
	if err := s.Set("other", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("other"); v != "x" {
		t.Errorf("other = %q", v)
	}
	if err := s.Delete("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if err := s.Delete("missing"); err != nil {
		t.Errorf("Delete of a missing key = %v", err)
	}
}

func TestStoreBrokenFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get("k")
	if e, ok := err.(*errs.Error); !ok || e.Code != CodeLocalDataFailed || e.Kind != errs.Parse {
		t.Fatalf("Get = %v", err)
	}
	if err := s.Set("k", "v"); err == nil {
		t.Error("Set overwrote a file it could not read")
	}
}

func TestStoreInMemory(t *testing.T) {
	s := New("")
	if s.Path() != "" {
		t.Fatalf("Path = %q", s.Path())
	}
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("k"); err != nil || v != "v" {
		t.Fatalf("Get = %q, %v", v, err)
	}
}

func TestStoreValues(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		s := New(dir)
		if v, err := s.Values(); err != nil || len(v) != 0 {
			t.Fatalf("Values of an empty store = %q, %v", v, err)
		}
		if err := s.Set("a", "one"); err != nil {
			t.Fatal(err)
		}
		if err := s.Set("b", "two"); err != nil {
			t.Fatal(err)
		}
		v, err := s.Values()
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(v)
		if strings.Join(v, ",") != "one,two" {
			t.Errorf("Values (dir %q) = %q", dir, v)
		}
	}
}
