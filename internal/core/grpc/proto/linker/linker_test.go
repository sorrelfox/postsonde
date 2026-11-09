package linker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// mapFS builds a file system from path/content pairs.
func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for p, s := range files {
		m[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return m
}

// load links inputs from files with the file system root as import path.
func load(t *testing.T, files map[string]string, inputs ...string) *Result {
	t.Helper()
	res, err := Load(context.Background(), Options{Files: inputs, ImportPaths: []string{"."}, FS: mapFS(files)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// diags returns "code file:line:col: message" for every diagnostic.
func diags(res *Result) []string {
	out := make([]string, len(res.Diagnostics))
	for i, d := range res.Diagnostics {
		out[i] = d.Code + " " + d.Error()
	}
	return out
}

func expectDiags(t *testing.T, res *Result, want ...string) {
	t.Helper()
	got := diags(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMissingImport(t *testing.T) {
	fsys := mapFS(map[string]string{
		"api/svc.proto": "syntax = \"proto3\";\npackage svc;\nimport \"google/api/annotations.proto\";\nimport weak \"opt/weak.proto\";\nmessage M {}\n",
	})
	res, err := Load(context.Background(), Options{Files: []string{"api/svc.proto"}, ImportPaths: []string{"api", "third_party"}, FS: fsys})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.import_not_found svc.proto:3:8: "google/api/annotations.proto" not found: searched api, third_party, the built-in well-known types`,
		`proto.weak_import_not_found svc.proto:4:13: warning: "opt/weak.proto" not found: searched api, third_party, the built-in well-known types (weak import, ignored)`,
	)
	if len(res.Files) != 1 || res.Files[0].Imports[0].File != nil || res.Files[0].Imports[1].Kind.String() != "weak" {
		t.Errorf("imports = %+v", res.Files[0].Imports)
	}
	err = res.Err()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Parse || e.Details["errors"] != "1" ||
		e.Message != `svc.proto:3:8: "google/api/annotations.proto" not found: searched api, third_party, the built-in well-known types` {
		t.Errorf("Err() = %#v", err)
	}

	// Without well-known types or roots.
	res, err = Load(context.Background(), Options{Files: []string{"api/svc.proto"}, FS: fsys, NoWellKnown: true})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.implicit_import_path svc.proto: warning: "api/svc.proto" is not under any import path; added its directory "api" as an import path`,
		`proto.import_not_found svc.proto:3:8: "google/api/annotations.proto" not found: searched api`,
		`proto.weak_import_not_found svc.proto:4:13: warning: "opt/weak.proto" not found: searched api (weak import, ignored)`,
	)
}

func TestFileNotFound(t *testing.T) {
	res := load(t, map[string]string{"a.proto": "syntax = \"proto3\";\n"}, "nope/b.proto", "a.proto")
	expectDiags(t, res,
		`proto.file_not_found nope/b.proto: file not found; also looked for it as an import path: searched ., the built-in well-known types`,
	)
	if len(res.Files) != 1 {
		t.Errorf("Files = %v", res.Files)
	}
}

func TestImportCycle(t *testing.T) {
	res := load(t, map[string]string{
		"a.proto": "syntax = \"proto3\";\nimport \"b.proto\";\nmessage A { B b = 1; }\n",
		"b.proto": "syntax = \"proto3\";\nimport \"c.proto\";\nmessage B { C c = 1; }\n",
		"c.proto": "syntax = \"proto3\";\nimport \"a.proto\";\nimport \"c.proto\";\nmessage C { A a = 1; }\n",
	}, "a.proto")
	expectDiags(t, res,
		`proto.import_cycle c.proto:2:8: import cycle: "a.proto" -> "b.proto" -> "c.proto" -> "a.proto"`,
		`proto.import_cycle c.proto:3:8: import cycle: "c.proto" -> "c.proto"`,
		`proto.not_imported c.proto:4:13: "A" is defined in "a.proto", which is not imported by "c.proto"; add import "a.proto";`,
	)
}

func TestVisibility(t *testing.T) {
	res := load(t, map[string]string{
		"main.proto": `syntax = "proto3";
package app;
import "b.proto";
message Main {
  lib.c.C c = 1;      // b -> public c
  lib.d.D d = 2;      // b -> public c -> public d
  lib.e.E e = 3;      // c imports e, not publicly
  lib.b.B b = 4;
}
`,
		"b.proto": "syntax = \"proto3\";\npackage lib.b;\nimport public \"c.proto\";\nmessage B {}\n",
		"c.proto": "syntax = \"proto3\";\npackage lib.c;\nimport public \"d.proto\";\nimport \"e.proto\";\nmessage C { lib.e.E e = 1; }\n",
		"d.proto": "syntax = \"proto3\";\npackage lib.d;\nmessage D {}\n",
		"e.proto": "syntax = \"proto3\";\npackage lib.e;\nmessage E {}\n",
	}, "main.proto")
	expectDiags(t, res,
		`proto.not_imported main.proto:7:3: "lib.e.E" is defined in "e.proto", which is not imported by "main.proto"; add import "e.proto";`,
	)
	m := res.Pool.FindMessage("app.Main")
	if m.FieldByName("d").Message != res.Pool.FindMessage("lib.d.D") || m.FieldByName("e").Message != nil || m.FieldByName("e").Kind != desc.KindUnknown {
		t.Errorf("Main fields: %+v", m.Fields)
	}
}

func TestUnknownTypes(t *testing.T) {
	res := load(t, map[string]string{
		"shop.proto": `syntax = "proto3";
package shop.v1;
message Order {
  Ordr parent = 1;
  google.protobuf.Timestamp created = 2;
  shop.Order other = 3;
  Missing missing = 4;
  Order.Line.Nope nope = 5;
  string.x bad = 6;
  Service svc = 7;
  message Line {}
}
service Service {
  rpc Get(Order) returns (Nothing);
  rpc Put(Order.Line) returns (Line);
}
enum E { E_ZERO = 0; }
extend E { string x = 1; }
`,
	}, "shop.proto")
	expectDiags(t, res,
		`proto.unknown_type shop.proto:4:3: unknown type "Ordr"; did you mean "shop.v1.Order"?`,
		`proto.unknown_type shop.proto:5:3: unknown type "google.protobuf.Timestamp"; add import "google/protobuf/timestamp.proto";`,
		`proto.unknown_type shop.proto:6:3: unknown type "shop.Order"; did you mean "shop.v1.Order"?`,
		`proto.unknown_type shop.proto:7:3: unknown type "Missing"`,
		`proto.unknown_type shop.proto:8:3: "Order.Line.Nope" resolved to "shop.v1.Order.Line.Nope", which is not defined; names are searched from the innermost scope outwards, so use ".Order.Line.Nope" to start from the outermost scope`,
		`proto.unknown_type shop.proto:9:3: unknown type "string.x"`,
		`proto.wrong_kind shop.proto:10:3: "Service" is a service, not a message or enum type`,
		`proto.unknown_type shop.proto:14:27: unknown message type "Nothing"`,
		`proto.unknown_type shop.proto:15:32: unknown message type "Line"; did you mean "shop.v1.Order.Line"?`,
		`proto.wrong_kind shop.proto:18:8: "E" is an enum, not a message type`,
	)
}

func TestDuplicateSymbols(t *testing.T) {
	res := load(t, map[string]string{
		"a.proto":     "syntax = \"proto3\";\npackage p;\nmessage M {}\nenum E { UNKNOWN = 0; }\nmessage q {}\n",
		"b.proto":     "syntax = \"proto3\";\npackage p;\nimport \"a.proto\";\nmessage M {}\nenum F { UNKNOWN = 0; }\n",
		"c.proto":     "syntax = \"proto3\";\npackage p.q;\nmessage X {}\n",
		"sub/d.proto": "syntax = \"proto3\";\npackage d;\nmessage D {}\n",
		"e.proto":     "syntax = \"proto3\";\nimport \"d.proto\";\nimport \"sub/d.proto\";\n",
	}, "b.proto", "c.proto")
	expectDiags(t, res,
		`proto.duplicate_symbol b.proto:4:9: "p.M" is already defined in "a.proto"`,
		`proto.duplicate_symbol b.proto:5:10: "p.UNKNOWN" is already defined in "a.proto"; enum values are siblings of their enum type, not children of it (C++ scoping)`,
		`proto.duplicate_symbol c.proto:2:9: package "p.q" conflicts with "p.q", which is a message defined in "a.proto"`,
	)

	// The same file through two import paths.
	res, err := Load(context.Background(), Options{
		Files:       []string{"e.proto"},
		ImportPaths: []string{".", "sub"},
		FS: mapFS(map[string]string{
			"sub/d.proto": "syntax = \"proto3\";\npackage d;\nmessage D {}\n",
			"e.proto":     "syntax = \"proto3\";\nimport \"d.proto\";\nimport \"sub/d.proto\";\n",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res,
		`proto.duplicate_symbol sub/d.proto:3:9: "d.D" is already defined in "d.proto"; the same file is loaded as both "d.proto" and "sub/d.proto" through different import paths`,
	)
}

func TestExtensions(t *testing.T) {
	res := load(t, map[string]string{
		"base.proto": `syntax = "proto2";
package base;
message Target {
  optional int32 id = 1;
  extensions 100 to 199, 1000 to max;
}
message Set {
  option message_set_wire_format = true;
  extensions 4 to max;
}
extend Target { optional string a = 100; }
`,
		"ext.proto": `syntax = "proto2";
package ext;
import "base.proto";
extend base.Target {
  optional string b = 100;   // duplicate of base.a
  optional string c = 5;     // outside the ranges
  optional string d = 150;
}
message Holder {
  extend base.Target { optional Holder e = 150; }  // duplicate of ext.d
}
extend base.Set {
  optional Holder ok = 2000000000;
  optional int32 bad = 5;
}
extend base.Target { optional int32 big = 536870912; }
`,
		"p3.proto": `syntax = "proto3";
package p3;
import "base.proto";
import "google/protobuf/descriptor.proto";
extend base.Target { string x = 101; }
extend google.protobuf.FieldOptions { string y = 50000; }
`,
	}, "ext.proto", "p3.proto")
	expectDiags(t, res,
		`proto.duplicate_extension ext.proto:5:23: extension number 100 of "base.Target" is already used by "base.a" in "base.proto"`,
		`proto.extension_number ext.proto:6:23: "base.Target" does not declare 5 as an extension number`,
		`proto.duplicate_extension ext.proto:10:44: extension number 150 of "base.Target" is already used by "ext.d" in "ext.proto"`,
		`proto.invalid_extension ext.proto:14:18: extensions of message set "base.Set" must be optional messages`,
		`proto.extension_number ext.proto:16:43: "base.Target" does not declare 536870912 as an extension number`,
		`proto.invalid_extension p3.proto:5:8: extensions in proto3 are only allowed for custom options; "base.Target" is not an options message`,
	)
	p := res.Pool
	if x := p.FindExtension("ext.Holder.e"); x == nil || x.Parent != p.FindMessage("ext.Holder") || x.Extendee != p.FindMessage("base.Target") {
		t.Errorf("ext.Holder.e = %+v", x)
	}
}

func TestOverlay(t *testing.T) {
	fsys := mapFS(map[string]string{
		"api/a.proto": "syntax = \"proto3\";\nimport \"b.proto\";\nmessage A { Brok b = 1; }\n",
		"api/b.proto": "syntax = \"proto3\";\nmessage B {}\n",
	})
	res, err := Load(context.Background(), Options{
		Files:       []string{"api/a.proto"},
		ImportPaths: []string{"api"},
		FS:          fsys,
		Overlay: map[string]string{
			"api/a.proto":     "syntax = \"proto3\";\nimport \"b.proto\";\nimport \"new.proto\";\nmessage A { B b = 1; New n = 2; }\n",
			"./api/new.proto": "syntax = \"proto3\";\nmessage New {}\n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectDiags(t, res)
	if a := res.Pool.FindMessage("A"); a.FieldByName("n").Message != res.Pool.FindMessage("New") || res.Files[0].DiskPath != "api/a.proto" {
		t.Errorf("overlay not used")
	}
}

func TestOptions(t *testing.T) {
	res := load(t, map[string]string{
		"opts.proto": `syntax = "proto3";
package opts;
import "google/protobuf/descriptor.proto";
message Rule { string path = 1; repeated Rule more = 2; Mode mode = 3; }
enum Mode { MODE_UNSPECIFIED = 0; FAST = 1; }
extend google.protobuf.FieldOptions {
  Rule rule = 50000;
  uint32 limit = 50001;
  Mode mode = 50002;
  repeated string tags = 50003;
}
extend google.protobuf.MessageOptions { double ratio = 50000; }
`,
		"hidden.proto": `syntax = "proto3";
package hidden;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FileOptions { bool secret = 50100; }
`,
		"use.proto": `syntax = "proto3";
package use;
import "opts.proto";
option java_package = 12;
option optimize_for = FASTEST;
option (hidden.secret) = true;
option (nope) = 1;
option deprecated = true;
option deprecated = false;
message M {
  option (opts.ratio) = -inf;
  option map_entry = true;
  string a = 1 [(opts.limit) = -1, (opts.mode) = SLOW, (opts.tags) = "x", (opts.tags) = "y"];
  string b = 2 [(opts.rule) = { path: "/x" more { path: "/y" } mode: FAST nope: 1 }, (opts.rule).path = "z"];
  string c = 3 [(opts.rule).mode.x = 1, (opts.limit).x = 2, (opts.ratio) = 1, packed = true];
  string d = 4 [(opts.rule) = "text", ctype = CORD, features.field_presence = EXPLICIT];
  int32 e = 5 [default = 3, json_name = 7];
  string f = 6 [(opts.rule) = { more: [{ path: "a" }, { path: "b" }] path: ["x"] }];
}
`,
	}, "use.proto", "hidden.proto")
	expectDiags(t, res,
		`proto.option_value use.proto:4:23: option "java_package" must be a string`,
		`proto.option_value use.proto:5:23: enum "google.protobuf.FileOptions.OptimizeMode" has no value named "FASTEST" (option "optimize_for")`,
		`proto.not_imported use.proto:6:8: "hidden.secret" is defined in "hidden.proto", which is not imported by "use.proto"; add import "hidden.proto";`,
		`proto.unknown_option use.proto:7:8: unknown option "(nope)"; import the file that defines it`,
		`proto.duplicate_option use.proto:9:8: option "deprecated" is already set`,
		`proto.option_value use.proto:12:10: map_entry must not be set explicitly; use map<KeyType, ValueType> instead`,
		`proto.option_value use.proto:13:32: option "(opts.limit)" must be an integer from 0 to 4294967295`,
		`proto.option_value use.proto:13:50: enum "opts.Mode" has no value named "SLOW" (option "(opts.mode)")`,
		`proto.option_value use.proto:14:75: "nope" is not a field of "opts.Rule" (option "(opts.rule)")`,
		`proto.duplicate_option use.proto:14:86: option "(opts.rule).path" is already set`,
		`proto.unknown_option use.proto:15:34: option "(opts.rule).mode.x": "mode" is an enum, not a message`,
		`proto.unknown_option use.proto:15:54: option "(opts.limit).x": "limit" is a uint32, not a message`,
		`proto.unknown_option use.proto:15:61: "opts.ratio" extends "google.protobuf.MessageOptions", not "google.protobuf.FieldOptions"`,
		`proto.option_value use.proto:15:79: packed can only be set on repeated fields of numeric, bool or enum types`,
		`proto.option_value use.proto:16:31: option "(opts.rule)" must be a message value in braces, such as { name: value }`,
		`proto.option_value use.proto:16:53: features are only allowed in editions files, not in proto3`,
		`proto.check.default_value use.proto:17:16: explicit default values are not allowed in proto3`,
		`proto.option_value use.proto:17:41: json_name must be a string`,
		`proto.option_value use.proto:18:76: "path" is not repeated, so its value cannot be a list (option "(opts.rule)")`,
	)
	m := res.Pool.FindMessage("use.M")
	if o := m.Options[0]; o.Scalar == nil || o.Scalar.Float > -1e308 {
		t.Errorf("ratio = %+v", o.Scalar)
	}
	if !res.Files[0].Deprecated {
		t.Errorf("file not deprecated")
	}
}
