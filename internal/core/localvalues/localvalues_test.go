package localvalues

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestStore(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(local)
	env := Environment("env_dev")
	global := Global("ws_0123456789abcdef")

	// No file: no values.
	got, err := s.Read(env)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("Read without a file = %v, %v", got, err)
	}

	want := map[string]string{"base": "http://localhost:8080", "empty": ""}
	if err := s.Write(env, want); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(global, map[string]string{"base": "http://127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(env); err != nil || !maps.Equal(got, want) {
		t.Errorf("Read = %v, %v, want %v", got, err, want)
	}
	if got, err := s.Read(global); err != nil || got["base"] != "http://127.0.0.1" || len(got) != 1 {
		t.Errorf("Read global = %v, %v", got, err)
	}

	path := s.Path(env)
	if wantPath := filepath.Join(local, "local-values", "environments", "env_dev.json"); path != wantPath {
		t.Errorf("Path = %s, want %s", path, wantPath)
	}
	if wantPath := filepath.Join(local, "local-values", "global", "ws_0123456789abcdef.json"); s.Path(global) != wantPath {
		t.Errorf("Path(global) = %s, want %s", s.Path(global), wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"owner": "env_dev"`) || !strings.Contains(string(data), `"version": 1`) {
		t.Errorf("file =\n%s", data)
	}
	if runtime.GOOS != "windows" {
		for p, perm := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700, filepath.Join(local, "local-values"): 0o700} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != perm {
				t.Errorf("%s: mode %v, %v, want %v", p, fi.Mode().Perm(), err, perm)
			}
		}
	}

	// No values removes the file; removing twice is fine.
	if err := s.Write(env, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file after writing no values: %v", err)
	}
	if err := s.Remove(env); err != nil {
		t.Errorf("Remove without a file = %v", err)
	}
}

// IDs that are not generated ones cannot escape the directory.
func TestPathHashesOddIDs(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(local)
	for _, id := range []string{"../../escape", "ENV_Upper", "", "a/b"} {
		path := s.Path(Environment(id))
		dir := filepath.Join(local, "local-values", "environments")
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "x.") {
			t.Errorf("Path(%q) = %s", id, path)
		}
	}
	if path := s.Path(Owner{Kind: "../x", ID: "env_a"}); !strings.HasPrefix(path, filepath.Join(local, "local-values")+string(filepath.Separator)) ||
		strings.Contains(path, "..") {
		t.Errorf("Path with an odd kind = %s", path)
	}
}

// A broken file is an error, never silently replaced.
func TestBrokenFile(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	o := Environment("env_dev")
	if err := os.MkdirAll(filepath.Dir(s.Path(o)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(o), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Read(o)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeLocalDataFailed || e.Kind != errs.Parse || e.Details["path"] != s.Path(o) {
		t.Fatalf("Read of a broken file = %v", err)
	}
	// The message names the file, never a value.
	if strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %v", err)
	}
}
