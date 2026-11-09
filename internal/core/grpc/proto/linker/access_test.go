package linker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
)

// Options.Access (PRV-10): input files it refuses are fileaccess.not_allowed,
// and an import that leads out of the allowed directories through a link
// is not found, not read.
func TestAccess(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	write := func(path, src string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "api.proto"), `syntax = "proto3"; import "dep.proto"; message A { Dep d = 1; }`)
	write(filepath.Join(project, "leaky.proto"), `syntax = "proto3"; import "out/secret.proto"; message L {}`)
	write(filepath.Join(project, "dep.proto"), `syntax = "proto3"; message Dep {}`)
	write(filepath.Join(outside, "secret.proto"), `syntax = "proto3"; message Secret {}`)
	if err := os.Symlink(outside, filepath.Join(project, "out")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need privileges on Windows:", err)
		}
		t.Fatal(err)
	}
	access := fileaccess.Policy{ProjectRoot: project}
	load := func(files ...string) *Result {
		t.Helper()
		res, err := Load(context.Background(), Options{Files: files, ImportPaths: []string{project}, Access: access})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	codes := func(res *Result) []string {
		var out []string
		for _, d := range res.Diagnostics {
			out = append(out, d.Code)
		}
		return out
	}

	if res := load(filepath.Join(project, "api.proto")); res.HasErrors() || len(res.Files) != 1 {
		t.Errorf("inside: %v", res.Diagnostics)
	}
	res := load(filepath.Join(outside, "secret.proto"))
	if len(res.Files) != 0 || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != fileaccess.CodeNotAllowed {
		t.Errorf("outside: %v", codes(res))
	}
	res = load(filepath.Join(project, "out", "secret.proto"))
	if len(res.Files) != 0 || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != fileaccess.CodeNotAllowed {
		t.Errorf("through a link: %v", codes(res))
	}
	res = load(filepath.Join(project, "leaky.proto"))
	found := false
	for _, d := range res.Diagnostics {
		found = found || d.Code == CodeImportNotFound
	}
	if !found || res.Pool.FindFile("out/secret.proto") != nil {
		t.Errorf("import through a link: %v", codes(res))
	}
	// Without Access the same import is read.
	res, err := Load(context.Background(), Options{Files: []string{filepath.Join(project, "leaky.proto")}, ImportPaths: []string{project}})
	if err != nil || res.HasErrors() {
		t.Errorf("without Access: %v, %v", err, res.Diagnostics)
	}
}
