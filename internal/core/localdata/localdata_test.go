package localdata

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureAndRemove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[Category][]string{
		History:          {"history/00000001.jsonl", "history/00000002.jsonl"},
		Runs:             {"runs/run_1.json", "runs/run_1.summary.json"},
		Cookies:          {"cookies.json", "cookies/env_1.json", ".cookies.json.tmp-123"},
		Tokens:           {"secrets/oauth2/tokens.json", "secrets/oauth2/.tokens.json.tmp-9"},
		Schemas:          {"graphql-schemas/abc.json", "grpc/reflection/0123abcd.json"},
		RuntimeVariables: {"runtime-variables.json", "secrets/runtime/variables.json", ".runtime-variables.json.tmp-1"},
	}
	for _, paths := range files {
		for _, p := range paths {
			write(t, filepath.Join(dir, filepath.FromSlash(p)), "12345")
		}
	}
	// Other local data, kept whatever is removed.
	keep := []string{"cookie-settings.json", "secrets/requests/req_1.json", "secrets/environments/env_1.json",
		"local-values/global/ws_1.json", "local.json", "ui-state.json", "environments.json", "scratch/r.json", "trash/x.json"}
	for _, p := range keep {
		write(t, filepath.Join(dir, filepath.FromSlash(p)), "keep")
	}

	for _, c := range All() {
		want := Usage{Files: len(files[c]), Bytes: int64(5 * len(files[c]))}
		if got := Measure(dir, c); got != want {
			t.Errorf("Measure(%s) = %+v, want %+v", c, got, want)
		}
	}
	for i, c := range All() {
		got, err := Remove(dir, c)
		if err != nil {
			t.Fatal(err)
		}
		if got.Files != len(files[c]) {
			t.Errorf("Remove(%s) = %+v", c, got)
		}
		if u := Measure(dir, c); u != (Usage{}) {
			t.Errorf("after Remove(%s): %+v left", c, u)
		}
		// The categories not removed yet are all there.
		for _, other := range All()[i+1:] {
			if u := Measure(dir, other); u.Files != len(files[other]) {
				t.Errorf("Remove(%s) touched %s: %+v", c, other, u)
			}
		}
	}
	for _, p := range keep {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	// Nothing on disk: not an error.
	if got, err := Remove(dir, History); err != nil || got != (Usage{}) {
		t.Errorf("Remove of nothing = %+v, %v", got, err)
	}
	if got, err := Remove(filepath.Join(dir, "missing"), Cookies); err != nil || got != (Usage{}) {
		t.Errorf("Remove in a missing directory = %+v, %v", got, err)
	}
}

func TestRemoveFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a user that respects them")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "history", "00000001.jsonl"), "x")
	write(t, filepath.Join(dir, "runs", "run_1.json"), "y")
	if err := os.Chmod(filepath.Join(dir, "history"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "history"), 0o700) })
	_, err := Remove(dir, History)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeClearFailed || e.Details["category"] != string(History) {
		t.Fatalf("err = %v", err)
	}
	if got, err := Remove(dir, Runs); err != nil || got.Files != 1 {
		t.Errorf("Remove(runs) = %+v, %v", got, err)
	}
}

func TestParseAndLocalDirs(t *testing.T) {
	t.Parallel()
	for _, c := range All() {
		if got, err := Parse(string(c)); err != nil || got != c {
			t.Errorf("Parse(%s) = %q, %v", c, got, err)
		}
	}
	if _, err := Parse("settings"); err == nil {
		t.Error("Parse(settings): no error")
	}

	data := t.TempDir()
	if dirs, err := LocalDirs(data); err != nil || len(dirs) != 0 {
		t.Errorf("LocalDirs without workspaces = %v, %v", dirs, err)
	}
	for _, p := range []string{"workspaces/ws_a/local.json", "workspaces/ws_b@1234/local.json", "workspaces/recent.json", "workspaces/.tmp/x"} {
		write(t, filepath.Join(data, filepath.FromSlash(p)), "{}")
	}
	dirs, err := LocalDirs(data)
	want := []string{filepath.Join(data, "workspaces", "ws_a"), filepath.Join(data, "workspaces", "ws_b@1234")}
	if err != nil || !slices.Equal(dirs, want) {
		t.Errorf("LocalDirs = %v, %v, want %v", dirs, err, want)
	}
	if dirs, err := LocalDirs(""); err != nil || dirs != nil {
		t.Errorf(`LocalDirs("") = %v, %v`, dirs, err)
	}
}
