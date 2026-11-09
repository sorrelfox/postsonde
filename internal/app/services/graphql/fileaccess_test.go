package graphql

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
)

// PRV-10: a request's schemaFile is read from the project folder or an
// allowed directory only; any other is fileaccess.not_allowed, and its
// schema is not served even for a key made before.
func TestSchemaFileAllowlist(t *testing.T) {
	t.Parallel()
	project, allowed, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{project, allowed, outside} {
		writeFile(t, filepath.Join(dir, "pets.graphql"), petsSDL, -60)
	}
	for target, link := range map[string]string{filepath.Join(outside, "pets.graphql"): "escape.graphql", outside: "outdir"} {
		if err := os.Symlink(target, filepath.Join(project, link)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symbolic links need privileges on Windows:", err)
			}
			t.Fatal(err)
		}
	}
	f := newFileFixture(t, project)
	dirs := []string{allowed}
	WithAllowedDirs(func() []string { return dirs })(f.svc)

	for _, tc := range []struct {
		name, stored string
		allowed      bool
	}{
		{"project-relative", "pets.graphql", true},
		{"allowed directory", filepath.Join(allowed, "pets.graphql"), true},
		{"outside", filepath.Join(outside, "pets.graphql"), false},
		{"link escaping", "escape.graphql", false},
		{"folder link escaping", "outdir/pets.graphql", false},
		{"missing outside", filepath.Join(outside, "nope.graphql"), false},
	} {
		f.stored = tc.stored
		info := f.info(t)
		if tc.allowed {
			if !info.Cached || info.Error != nil || info.TypeCount != 4 {
				t.Errorf("%s: SchemaInfo = %+v", tc.name, info)
			}
			continue
		}
		if info.Cached || info.Key != "" || info.Error == nil || info.Error.Code != fileaccess.CodeNotAllowed ||
			!strings.HasPrefix(info.Error.Message, "The schema file ") || info.Error.Hint != fileaccess.HintAllow {
			t.Errorf("%s: SchemaInfo = %+v (error %+v)", tc.name, info, info.Error)
		}
	}

	// A key of an allowed file stops serving once its folder is removed.
	f.stored = filepath.Join(allowed, "pets.graphql")
	key := f.info(t).Key
	if f.svc.cached(key) == nil {
		t.Fatal("no schema for an allowed file")
	}
	dirs = nil
	if f.svc.cached(key) != nil {
		t.Error("a schema served for a file no longer allowed")
	}
}
