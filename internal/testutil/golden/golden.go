// Package golden provides golden-file assertions for tests.
//
// A golden file holds the expected output of a test at
// testdata/<name>.golden, relative to the package under test. Compare
// output against it with Assert; rewrite the files from the current
// output by running the tests with -update:
//
//	go test ./internal/core/foo -run TestRender -update
//
// Review the resulting diff before committing it.
package golden

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/ with the current output")

// Path returns the path of the golden file for name: testdata/<name>.golden.
func Path(name string) string {
	return filepath.Join("testdata", filepath.FromSlash(name)+".golden")
}

// Assert compares got with the contents of testdata/<name>.golden and fails
// the test if they differ. With -update it writes got to the file instead.
// name may contain slashes to group files in subdirectories.
func Assert(tb testing.TB, name string, got []byte) {
	tb.Helper()
	assertFile(tb, Path(name), got, *update)
}

func assertFile(tb testing.TB, path string, got []byte, update bool) {
	tb.Helper()
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("golden: create dir for %s: %v", path, err)
			return
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			tb.Fatalf("golden: write %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		tb.Fatalf("golden: %s does not exist; run the test with -update to create it", path)
		return
	}
	if err != nil {
		tb.Fatalf("golden: read %s: %v", path, err)
		return
	}
	if !bytes.Equal(got, want) {
		tb.Errorf("golden: output differs from %s (run with -update to accept it)\n%s", path, diff(want, got))
	}
}

// diff reports the first differing line, which is usually enough to see
// what changed; the full files are one -update and git diff away.
func diff(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	for i := range max(len(wl), len(gl)) {
		w, g := line(wl, i), line(gl, i)
		if w != g {
			return fmt.Sprintf("first difference at line %d:\n  want: %s\n  got:  %s", i+1, w, g)
		}
	}
	return "files differ"
}

// line returns lines[i] quoted, or a marker past the end of the file.
func line(lines []string, i int) string {
	if i >= len(lines) {
		return "<end of file>"
	}
	return strconv.Quote(lines[i])
}
