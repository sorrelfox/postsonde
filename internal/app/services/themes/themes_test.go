package themes

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestList(t *testing.T) {
	t.Parallel()

	data := t.TempDir()
	s := New(data, nil)
	got, err := s.List(context.Background())
	if err != nil || len(got.Themes) != 0 || got.Dir != filepath.Join(data, "themes") {
		t.Fatalf("List without a folder = %+v, %v", got, err)
	}

	dir := filepath.Join(data, "themes")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ocean.json": `{"name": "Ocean", "base": "dark", "colors": {"bg0": "#012"}, "extra": 1}`,
		"evil.json":  `{"name": "Evil", "base": "dark", "colors": {"bg0": "red;} *{background:url(https://evil.example)}"}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.List(context.Background())
	if err != nil || len(got.Themes) != 2 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	evil, ocean := got.Themes[0], got.Themes[1]
	if evil.Usable || len(evil.Vars) != 0 || len(evil.Problems) != 1 || evil.Problems[0].Code != "themes.invalid_color" {
		t.Errorf("evil = %+v", evil)
	}
	if !ocean.Usable || ocean.Base != "dark" || len(ocean.Vars) != 1 || ocean.Vars[0] != (ThemeVarDTO{Name: "--bg0", Value: "#001122"}) {
		t.Errorf("ocean = %+v", ocean)
	}
	if len(ocean.Problems) != 1 || ocean.Problems[0].Severity != "warning" {
		t.Errorf("ocean problems = %+v", ocean.Problems)
	}
}

func TestOpenFolder(t *testing.T) {
	t.Parallel()

	if err := New("", nil).OpenFolder(context.Background()); !errors.Is(err, &errs.Error{Code: CodeUnavailable}) {
		t.Errorf("OpenFolder without a data directory = %v", err)
	}

	data := t.TempDir()
	var opened string
	s := New(data, nil, WithOpen(func(path string) error { opened = path; return nil }))
	if err := s.OpenFolder(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "themes")
	if opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("themes folder not created: %v", err)
	}

	failing := New(data, nil, WithOpen(func(string) error { return fs.ErrPermission }))
	if err := failing.OpenFolder(context.Background()); !errors.Is(err, &errs.Error{Code: CodeOpenFailed}) {
		t.Errorf("OpenFolder with a failing opener = %v", err)
	}
}
