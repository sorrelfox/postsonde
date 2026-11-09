package linker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// FuzzLoad links arbitrary input next to a small dependency and checks that
// Load never panics, that diagnostics point into their files, and that the
// model's pointers are consistent.
//
//	go test ./internal/core/grpc/proto/linker -run '^$' -fuzz FuzzLoad -fuzztime 60s
func FuzzLoad(f *testing.F) {
	seeds, _ := filepath.Glob(filepath.Join("..", "parser", "testdata", "files", "*.proto"))
	for _, s := range seeds {
		b, err := os.ReadFile(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(b))
	}
	f.Add("syntax = \"proto2\";\nimport public \"dep.proto\";\nimport \"in.proto\";\nextend dep.D { optional D x = 100 [default = 1]; }\nmessage D { option (dep.o) = { a: 1 }; }\n")
	f.Add("edition = \"2024\";\nimport option \"dep.proto\";\noption features.(dep.o).b = 1;\nmessage M { map<string, .dep.D> m = 1 [(dep.o).c = \"\"]; }\n")
	dep := `syntax = "proto2";
package dep;
import "google/protobuf/descriptor.proto";
message D { extensions 100 to max; optional int32 a = 1; }
message O { optional int32 a = 1; optional int32 b = 2; optional string c = 3; }
extend google.protobuf.MessageOptions { optional O o = 50000; }
extend google.protobuf.FileOptions { optional O fo = 50000; }
extend google.protobuf.FieldOptions { optional O o = 50000; }
`
	f.Fuzz(func(t *testing.T, src string) {
		files := map[string]string{"in.proto": src, "dep.proto": dep}
		res, err := Load(context.Background(), Options{Files: []string{"in.proto"}, ImportPaths: []string{"."}, FS: mapFS(files)})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Diagnostics {
			content, ok := files[d.File]
			if d.File == "google/protobuf/descriptor.proto" {
				continue
			}
			if !ok || d.Start < 0 || d.End < d.Start || d.End > len(content) || (d.Line == 0) != (d.Column == 0) {
				t.Fatalf("diagnostic out of range: %+v", d)
			}
		}
		for _, file := range res.Pool.Files() {
			desc.Walk(file, func(d desc.Descriptor) bool {
				b := d.Desc()
				if b.File != file || b.FullName == "" || b.Name == "" {
					t.Fatalf("bad descriptor %+v", b)
				}
				if x, ok := d.(*desc.Field); ok {
					if x.Kind == desc.KindMessage && x.Message == nil || x.Kind == desc.KindEnum && x.Enum == nil {
						t.Fatalf("unresolved %s has kind %v", x.FullName, x.Kind)
					}
					if x.Oneof != nil && x.Oneof.Parent != x.Parent {
						t.Fatalf("oneof of %s in another message", x.FullName)
					}
				}
				return true
			})
		}
	})
}
