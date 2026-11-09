package linker

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/wellknown"
)

// TestWellKnownTypes links every embedded well-known type file with no
// diagnostics at all.
func TestWellKnownTypes(t *testing.T) {
	res, err := Load(context.Background(), Options{Files: wellknown.Paths(), FS: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		t.Errorf("%v", d)
	}
	if len(res.Files) != len(wellknown.Paths()) {
		t.Fatalf("got %d files", len(res.Files))
	}
	for _, f := range res.Files {
		if !f.Builtin || f.DiskPath != "" || f.Root != "" {
			t.Errorf("%s: Builtin %v DiskPath %q Root %q", f.Path, f.Builtin, f.DiskPath, f.Root)
		}
	}
	p := res.Pool
	api := p.FindMessage("google.protobuf.Api")
	if api == nil || api.FieldByName("source_context").Message != p.FindMessage("google.protobuf.SourceContext") {
		t.Errorf("Api.source_context does not resolve across files")
	}
	if m := p.FindMessage("google.protobuf.Value"); m == nil || m.FieldByName("null_value").Enum != p.FindEnum("google.protobuf.NullValue") {
		t.Errorf("Value.null_value does not resolve")
	}
	if st := p.FindMessage("google.rpc.Status"); st == nil || st.FieldByName("details").Message != p.FindMessage("google.protobuf.Any") {
		t.Errorf("google.rpc.Status.details does not resolve to Any")
	}
	s := p.FindMessage("google.protobuf.Struct")
	fields := s.FieldByName("fields")
	if !fields.IsMap() || fields.MapKey().Kind != desc.KindString || fields.MapValue().Message != p.FindMessage("google.protobuf.Value") {
		t.Errorf("Struct.fields is not map<string, Value>")
	}
	// descriptor.proto is proto2: closed enums, explicit presence, defaults.
	fo := p.FindMessage("google.protobuf.FileOptions")
	opt := fo.FieldByName("optimize_for")
	if opt.Default == nil || opt.Default.Enum == nil || opt.Default.Enum.Name != "SPEED" || !opt.Enum.Closed || !opt.HasPresence {
		t.Errorf("FileOptions.optimize_for: default %+v, closed %v, presence %v", opt.Default, opt.Enum.Closed, opt.HasPresence)
	}
	if d := fo.FieldByName("java_multiple_files").Default; d == nil || d.Bool {
		t.Errorf("java_multiple_files default = %+v", d)
	}
	if !fo.FieldByName("java_generate_equals_and_hash").Deprecated {
		t.Errorf("java_generate_equals_and_hash is not deprecated")
	}
	if len(fo.ExtensionRanges) != 1 || fo.ExtensionRanges[0] != (desc.Range{Start: 1000, End: 536870911, Loc: fo.ExtensionRanges[0].Loc}) {
		t.Errorf("FileOptions extension ranges = %+v", fo.ExtensionRanges)
	}
	if x := p.FindMessage("google.protobuf.SourceCodeInfo.Location").FieldByName("path"); !x.Packed {
		t.Errorf("SourceCodeInfo.Location.path is not packed")
	}
	if x := p.FindMessage("google.protobuf.Duration").FieldByName("seconds"); x.HasPresence || x.Kind != desc.KindInt64 {
		t.Errorf("Duration.seconds: presence %v kind %v", x.HasPresence, x.Kind)
	}
}

// TestRealWorldParserData links the vendored real-world proto2 files of the
// parser tests (see parser/testdata/realworld/README.md).
func TestRealWorldParserData(t *testing.T) {
	root := filepath.Join("..", "parser", "testdata", "realworld")
	files := []string{
		filepath.Join(root, "protobuf-go", "proto2", "fields.proto"),
		filepath.Join(root, "protobuf-go", "textpb2", "test.proto"),
	}
	for _, name := range files {
		t.Run(filepath.Base(filepath.Dir(name)), func(t *testing.T) {
			res, err := Load(context.Background(), Options{Files: []string{name}, ImportPaths: []string{filepath.Dir(name)}})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range res.Diagnostics {
				if d.Severity == parser.SeverityError {
					t.Errorf("%v", d)
				}
			}
		})
	}
}

// TestExternalCorpus links every .proto file under POSTSONDE_PROTO_CORPUS.
// Import roots are guessed: a directory is a root when some file's path
// under it equals an import string used anywhere in the corpus. Files whose
// imports are not in the corpus are skipped; any other error fails.
func TestExternalCorpus(t *testing.T) {
	dir := os.Getenv("POSTSONDE_PROTO_CORPUS")
	if dir == "" {
		t.Skip("POSTSONDE_PROTO_CORPUS not set")
	}
	var paths []string
	imports := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		paths = append(paths, p)
		a, _ := parser.Parse(p, string(src))
		for _, d := range a.Decls {
			if imp, ok := d.(*ast.Import); ok && imp.Path != nil {
				imports[imp.Path.Value] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, p := range paths {
		slash := filepath.ToSlash(p)
		for imp := range imports {
			if strings.HasSuffix(slash, "/"+imp) {
				r := filepath.FromSlash(strings.TrimSuffix(slash, "/"+imp))
				if !slices.Contains(roots, r) {
					roots = append(roots, r)
				}
			}
		}
	}
	slices.Sort(roots)
	linked, skipped, customOptions := 0, 0, 0
	warnings := map[string]int{}
	for _, p := range paths {
		// Each file gets the roots containing it, most specific first, then
		// every other root.
		var mine, rest []string
		for _, r := range roots {
			if strings.HasPrefix(p, r+string(filepath.Separator)) {
				mine = append(mine, r)
			} else {
				rest = append(rest, r)
			}
		}
		slices.SortFunc(mine, func(a, b string) int { return len(b) - len(a) })
		res, err := Load(context.Background(), Options{Files: []string{p}, ImportPaths: append(mine, rest...)})
		if err != nil {
			t.Fatal(err)
		}
		missing := false
		for _, d := range res.Diagnostics {
			if d.Code == CodeImportNotFound {
				missing = true
			}
		}
		if missing {
			skipped++
			continue
		}
		linked++
		for _, d := range res.Diagnostics {
			if d.Severity == parser.SeverityError {
				t.Errorf("%s: %v", p, d)
			} else {
				warnings[d.Code]++
			}
		}
		for _, f := range res.Files {
			desc.Walk(f, func(d desc.Descriptor) bool {
				for _, o := range d.Desc().Options {
					if len(o.Path) > 0 && o.Path[0].IsExtension {
						customOptions++
					}
				}
				return true
			})
		}
	}
	t.Logf("linked %d files, skipped %d with imports outside the corpus, %d roots, %d custom options, warnings %v",
		linked, skipped, len(roots), customOptions, warnings)
}
