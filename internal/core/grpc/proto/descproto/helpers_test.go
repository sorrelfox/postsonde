package descproto

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

// schemaFiles are the test schema's files, dependencies first.
var schemaFiles = []string{
	"shop/v1/options.proto",
	"shop/v1/common.proto",
	"shop/v1/orders.proto",
	"legacy/catalog.proto",
	"shop/v1/editions.proto",
}

// linkSchema links testdata/schema from source and requires no
// diagnostics.
func linkSchema(t testing.TB) *linker.Result {
	t.Helper()
	res, err := linker.Load(context.Background(), linker.Options{
		Files:       schemaFiles,
		ImportPaths: []string{filepath.Join("testdata", "schema")},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range res.Diagnostics {
		t.Errorf("source diagnostic: %v", d)
	}
	if t.Failed() {
		t.FailNow()
	}
	return res
}

// goldenDescriptor reads the protocompile encoding of a schema file.
func goldenDescriptor(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", filepath.FromSlash(name)+".pb"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

// linkBytes links descriptors and requires no diagnostics.
func linkBytes(t testing.TB, files [][]byte) *linker.Result {
	t.Helper()
	res, err := Link(context.Background(), files)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if len(res.Diagnostics) > 0 {
		for _, d := range res.Diagnostics {
			t.Errorf("diagnostic: %v", d)
		}
		for _, f := range res.Files {
			t.Logf("rendered %s:\n%s", f.Path, f.AST.Source)
		}
		t.FailNow()
	}
	return res
}
