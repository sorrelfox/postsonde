package parser

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExternalCorpus parses every .proto file under the directory named by
// POSTSONDE_PROTO_CORPUS (for example a protobuf or googleapis checkout, or
// the Go module cache) and fails on any syntax error or error-severity Check
// diagnostic. It is skipped when the variable is unset, since the files are
// not part of this repository.
func TestExternalCorpus(t *testing.T) {
	root := os.Getenv("POSTSONDE_PROTO_CORPUS")
	if root == "" {
		t.Skip("POSTSONDE_PROTO_CORPUS not set")
	}
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		f, errs := Parse(path, string(src))
		for _, e := range errs {
			t.Errorf("%v", e)
		}
		for _, d := range Check(f) {
			if d.Severity == SeverityError {
				t.Errorf("%v", d)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d files", n)
}
