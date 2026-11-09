package linker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

func TestEditionsFeatures(t *testing.T) {
	res := load(t, map[string]string{
		"shop/features.proto": `edition = "2024";
package shop.features;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions { string label = 50000; }
`,
		"orders.proto": `edition = "2024";
package shop.orders;
import option "shop/features.proto";
option features.field_presence = IMPLICIT;
message Order {
  string id = 1 [features.field_presence = EXPLICIT, (shop.features.label) = "id"];
  repeated LineItem items = 2;
  repeated int32 codes = 3 [features.repeated_field_encoding = EXPANDED];
  repeated int32 bins = 4;
  Status status = 5;
  LineItem gift = 6 [features.message_encoding = DELIMITED];
  int32 count = 7;
  shop.features.Nope nope = 8;
  message LineItem {
    option features.field_presence = LEGACY_REQUIRED;
    string sku = 1;
  }
}
enum Status {
  option features.enum_type = CLOSED;
  STATUS_PLACED = 1;
}
enum Open { OPEN_ZERO = 0; }
`,
	}, "orders.proto")
	// The option import makes the extension usable in options, not its
	// package's types.
	expectDiags(t, res,
		`proto.unknown_type orders.proto:13:3: unknown type "shop.features.Nope"`,
	)
	p := res.Pool
	order := p.FindMessage("shop.orders.Order")
	for _, w := range []struct {
		name     string
		presence bool
		packed   bool
		kind     desc.Kind
		card     desc.Cardinality
	}{
		{"id", true, false, desc.KindString, desc.Optional},
		{"items", false, false, desc.KindMessage, desc.Repeated},
		{"codes", false, false, desc.KindInt32, desc.Repeated},
		{"bins", false, true, desc.KindInt32, desc.Repeated},
		{"status", false, false, desc.KindEnum, desc.Optional},
		{"gift", true, false, desc.KindGroup, desc.Optional},
		{"count", false, false, desc.KindInt32, desc.Optional},
	} {
		x := order.FieldByName(w.name)
		if x.HasPresence != w.presence || x.Packed != w.packed || x.Kind != w.kind || x.Cardinality != w.card {
			t.Errorf("%s: presence %v packed %v kind %v card %v", w.name, x.HasPresence, x.Packed, x.Kind, x.Cardinality)
		}
	}
	if x := p.FindMessage("shop.orders.Order.LineItem").FieldByName("sku"); x.Cardinality != desc.Required || !x.HasPresence {
		t.Errorf("sku: %v presence %v", x.Cardinality, x.HasPresence)
	}
	if !p.FindEnum("shop.orders.Status").Closed || p.FindEnum("shop.orders.Open").Closed {
		t.Errorf("enum_type feature not applied")
	}
	if o := order.FieldByName("id").Options; len(o) != 2 || o[1].Scalar == nil || o[1].Scalar.String != "id" {
		t.Errorf("id options = %+v", o)
	}
}

func TestJSONNamesAndClosedEnums(t *testing.T) {
	res := load(t, map[string]string{
		"p2.proto": `syntax = "proto2";
package p2;
enum Closed { C = 1; }
message Legacy {
  optional int32 foo_bar = 1;
  optional int32 fooBar = 2;           // default names only: allowed in proto2
  optional int32 baz = 3 [json_name = "fooBar"];
}
`,
		"p3.proto": `syntax = "proto3";
package p3;
import "p2.proto";
message M {
  int32 foo_bar = 1;
  int32 fooBar = 2;
  int32 other = 3 [json_name = "x"];
  int32 x = 4;
  p2.Closed closed = 5;
  map<string, p2.Closed> by_name = 6;
}
`,
	}, "p3.proto")
	expectDiags(t, res,
		`proto.json_name_conflict p2.proto:7:18: the custom JSON name of field "baz" ("fooBar") conflicts with the default JSON name of field "foo_bar"`,
		`proto.json_name_conflict p3.proto:6:9: the default JSON name of field "fooBar" ("fooBar") conflicts with the default JSON name of field "foo_bar"`,
		`proto.json_name_conflict p3.proto:8:9: the default JSON name of field "x" ("x") conflicts with the custom JSON name of field "other"`,
		`proto.closed_enum p3.proto:9:3: enum "p2.Closed" is closed (proto2), so it cannot be used in the proto3 message "p3.M"`,
		`proto.closed_enum p3.proto:10:15: enum "p2.Closed" is closed (proto2), so it cannot be used in the proto3 message "p3.M.ByNameEntry"`,
	)
}

func TestPartialFiles(t *testing.T) {
	res := load(t, map[string]string{
		"broken.proto": `syntax = "proto3";
package broken;
import "dep.proto";
message A {
  dep.D d = 1;
  int32 half =
  B b = 3;
  Nope n = 4;
}
message B { int32 x = 1 }
message {
  int32 y = 1;
}
service S { rpc Call(A) returns (dep.D); rpc (A) returns (B); }
`,
		"dep.proto": "syntax = \"proto3\";\npackage dep;\nmessage D { int32 = 1; }\n",
	}, "broken.proto")
	codes := map[string]int{}
	for _, d := range res.Diagnostics {
		codes[d.Code]++
	}
	if codes[CodeSyntax] == 0 || codes[CodeUnknownType] != 1 {
		t.Errorf("diagnostics: %q", diags(res))
	}
	p := res.Pool
	a := p.FindMessage("broken.A")
	if a == nil || a.FieldByName("d").Message != p.FindMessage("dep.D") || a.FieldByName("b").Message != p.FindMessage("broken.B") {
		t.Fatalf("A did not link: %+v", a)
	}
	if n := a.FieldByName("n"); n.Kind != desc.KindUnknown || n.TypeName != "Nope" {
		t.Errorf("n = %+v", n)
	}
	s := p.FindService("broken.S")
	if len(s.Methods) != 1 || s.Methods[0].Output != p.FindMessage("dep.D") {
		t.Errorf("S = %+v", s.Methods)
	}
	if res.Err() == nil || !res.HasErrors() {
		t.Errorf("Err() = nil")
	}
}

func TestImportPaths(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	loose := write("loose/svc.proto", "syntax = \"proto3\";\nimport \"sibling.proto\";\nimport \"common/types.proto\";\nmessage S { Sib s = 1; T t = 2; }\n")
	write("loose/sibling.proto", "syntax = \"proto3\";\nmessage Sib {}\n")
	first := write("first/common/types.proto", "syntax = \"proto3\";\nmessage T {}\n")
	second := write("second/common/types.proto", "syntax = \"proto3\";\nmessage T { int32 x = 1; }\n")
	roots := []string{filepath.Join(dir, "first"), filepath.Join(dir, "second")}

	// A file outside every import path: its directory becomes one.
	res, err := Load(context.Background(), Options{Files: []string{loose}, ImportPaths: roots})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.implicit_import_path svc.proto: warning: "`+loose+`" is not under any import path; added its directory "`+filepath.Dir(loose)+`" as an import path`,
		`proto.import_shadowed svc.proto:3:8: warning: "common/types.proto" exists under more than one import path; using "`+first+`", not "`+second+`"`,
	)
	if len(res.ImportPaths) != 3 || res.ImportPaths[2] != filepath.Dir(loose) || res.Files[0].Path != "svc.proto" {
		t.Errorf("ImportPaths %q, file %q", res.ImportPaths, res.Files[0].Path)
	}
	if m := res.Pool.FindMessage("T"); m == nil || len(m.Fields) != 0 {
		t.Errorf("T is not from the first root")
	}

	// An input shadowed by an earlier root.
	res, err = Load(context.Background(), Options{Files: []string{second}, ImportPaths: roots})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.input_shadowed common/types.proto: "`+second+`" is shadowed by "`+first+`", which has the same import path "common/types.proto" in an earlier import path; imports of it resolve to that file. Load that file instead, or reorder the import paths`,
	)
	if m := res.Pool.FindMessage("T"); m == nil || len(m.Fields) != 1 {
		t.Errorf("the input file was not loaded")
	}

	// Two inputs with the same import path.
	res, err = Load(context.Background(), Options{Files: []string{"common/types.proto", second}, ImportPaths: roots})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.input_shadowed common/types.proto: "`+second+`" has import path "common/types.proto", which is already loaded from "`+first+`"; remove one of them or reorder the import paths`,
	)

	// Overlays use OS paths, relative ones made absolute.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, first)
	if err != nil {
		t.Skip("temp dir not reachable relative to the working directory")
	}
	res, err = Load(context.Background(), Options{
		Files:       []string{first},
		ImportPaths: roots,
		Overlay:     map[string]string{rel: "syntax = \"proto3\";\nmessage T { string edited = 1; }\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res)
	if m := res.Pool.FindMessage("T"); m == nil || m.FieldByName("edited") == nil {
		t.Errorf("overlay not applied")
	}
}

// TestQuotePath: a disk path in a message keeps its backslashes, so a
// Windows path reads as written; other characters are quoted as by
// strconv.Quote.
func TestQuotePath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\protos\a.proto`: `"C:\protos\a.proto"`,
		"/work/a.proto":     `"/work/a.proto"`,
		`say "hi".proto`:    `"say \"hi\".proto"`,
		"line\nbreak":       `"line\nbreak"`,
	} {
		if got := quotePath(in); got != want {
			t.Errorf("quotePath(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := Load(context.Background(), Options{})
	if errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("no files: %v", err)
	}
	_, err = Load(context.Background(), Options{Files: []string{"a.proto"}, ImportPaths: []string{"/abs"}, FS: mapFS(nil)})
	if errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("absolute root in an fs.FS: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Load(ctx, Options{Files: []string{"a.proto"}, FS: mapFS(map[string]string{"a.proto": "syntax = \"proto3\";"})})
	if errs.KindOf(err) != errs.Cancelled || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	res := load(t, map[string]string{"a.proto": "syntax = \"proto3\";\nimport \"../x.proto\";\nimport \"b\\\\c.proto\";\n"}, "a.proto")
	expectDiags(t, res,
		`proto.invalid_import_path a.proto:2:8: import path "../x.proto" must be relative to an import path, with forward slashes and without "." or ".." elements`,
		`proto.invalid_import_path a.proto:3:8: import path "b\\c.proto" must be relative to an import path, with forward slashes and without "." or ".." elements`,
	)
	if res.Diagnostics[0].Severity != parser.SeverityError {
		t.Errorf("severity")
	}
}
