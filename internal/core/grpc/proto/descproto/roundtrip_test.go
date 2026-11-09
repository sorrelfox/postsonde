package descproto

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

// TestRoundTrip marshals the linked schema, links the descriptors again and
// requires the same descriptors and the same model.
func TestRoundTrip(t *testing.T) {
	roundTrip(t, linkSchema(t))
}

// TestRoundTripRealWorld does the same for the linker's multi-root test
// project (custom options, aggregates, public imports, proto2 extensions)
// and the vendored protobuf-go proto2 files (every default form, groups,
// message sets).
func TestRoundTripRealWorld(t *testing.T) {
	project := filepath.Join("..", "linker", "testdata", "project")
	realworld := filepath.Join("..", "parser", "testdata", "realworld", "protobuf-go")
	cases := []struct {
		name  string
		opts  linker.Options
		files []string
	}{
		{"project", linker.Options{
			Files:       []string{"shop/v1/orders.proto", "shop/v1/legacy_ext.proto"},
			ImportPaths: []string{filepath.Join(project, "api"), filepath.Join(project, "third_party"), filepath.Join(project, "vendor")},
		}, nil},
		{"proto2", linker.Options{Files: []string{"fields.proto"}, ImportPaths: []string{filepath.Join(realworld, "proto2")}}, nil},
		{"textpb2", linker.Options{Files: []string{"test.proto"}, ImportPaths: []string{filepath.Join(realworld, "textpb2")}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := linker.Load(context.Background(), c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := res.Err(); err != nil {
				t.Fatalf("source does not link: %v", err)
			}
			roundTrip(t, res)
		})
	}
}

// roundTrip marshals every non-built-in file of src, links the descriptors
// and compares descriptors and models.
func roundTrip(t *testing.T, src *linker.Result) {
	t.Helper()
	var names []string
	var files [][]byte
	for _, f := range src.Pool.Files() {
		if f.Builtin {
			continue
		}
		b, err := Marshal(f, src.Pool)
		if err != nil {
			t.Fatalf("Marshal %s: %v", f.Path, err)
		}
		names = append(names, f.Path)
		files = append(files, b)
	}
	res, err := Link(context.Background(), files)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	failed := false
	for _, d := range res.Diagnostics {
		if d.Severity == parser.SeverityError {
			t.Errorf("diagnostic: %v", d)
			failed = true
		}
	}
	for i, name := range names {
		f := res.Pool.FindFile(name)
		if f == nil {
			t.Fatalf("%s missing after Link", name)
		}
		if failed {
			t.Logf("rendered %s:\n%s", name, f.AST.Source)
			continue
		}
		b, err := Marshal(f, res.Pool)
		if err != nil {
			t.Fatalf("Marshal again %s: %v", name, err)
		}
		if !bytes.Equal(b, files[i]) {
			t.Errorf("%s: descriptor changed after Link\nrendered:\n%s", name, f.AST.Source)
		}
		compareFiles(t, src.Pool.FindFile(name), f)
	}
}
