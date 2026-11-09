package parser

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	proto2Header = "syntax = \"proto2\";\n"
	proto3Header = "syntax = \"proto3\";\n"
	editionsHdr  = "edition = \"2023\";\n"
)

// checkCases are syntactically valid files with every Check diagnostic as
// "line:column severity code: message".
var checkCases = []struct {
	name, src string
	want      []string
}{
	{"missing syntax", "message M {}", []string{
		`1:1 warning syntax: no syntax specified; the file is treated as proto2 (add syntax = "proto2"; or syntax = "proto3";)`,
	}},
	{"unknown syntax", `syntax = "proto4";`, []string{
		`1:10 error syntax: unrecognized syntax "proto4"; use "proto2" or "proto3"`,
	}},
	{"unsupported edition", `edition = "2022";`, []string{
		`1:11 error syntax: edition "2022" is not supported; supported editions are 2023 and 2024`,
	}},
	{"duplicate package and import", proto3Header + "package a;\npackage b;\nimport \"x.proto\";\nimport public \"x.proto\";", []string{
		`3:1 error duplicate declaration: multiple package declarations`,
		`5:15 error duplicate declaration: "x.proto" is imported more than once`,
	}},
	{"field number ranges", proto3Header + "message M {\n  int32 a = 0;\n  int32 b = -1;\n  int32 c = 536870912;\n  int32 d = 19000;\n  int32 e = 19999;\n  int32 f = 20000;\n  int32 g = 536870911;\n  int32 h = 18999;\n}", []string{
		`3:13 error field number: field numbers must be in the range 1 to 536870911`,
		`4:13 error field number: field numbers must be in the range 1 to 536870911`,
		`5:13 error field number: field numbers must be in the range 1 to 536870911`,
		`6:13 error field number: field numbers 19000 to 19999 are reserved for the protobuf implementation`,
		`7:13 error field number: field numbers 19000 to 19999 are reserved for the protobuf implementation`,
	}},
	{"duplicate names and numbers", proto3Header + "message M {\n  int32 a = 1;\n  string a = 2;\n  oneof o { int32 b = 1; }\n  message a {}\n}", []string{
		`4:10 error duplicate name: "a" is already defined in message "M" (as a field)`,
		`5:23 error duplicate number: field number 1 is already used by "a"`,
		`6:11 error duplicate name: "a" is already defined in message "M" (as a field)`,
	}},
	{"map entry name", proto3Header + "message M {\n  map<string, string> foo_bar = 1;\n  message FooBarEntry {}\n}", []string{
		`4:11 error duplicate name: "FooBarEntry" is already defined in message "M" (as the entry type of map field "foo_bar")`,
	}},
	{"file scope and enum value scoping", proto3Header + "enum A { UNKNOWN = 0; }\nenum B { UNKNOWN = 0; }\nmessage A {}\nservice S { rpc Get(A) returns (A); rpc Get(A) returns (A); }", []string{
		`3:10 error duplicate name: "UNKNOWN" is already defined in the file (as a value of enum "A"; enum values are siblings of their enum, not children of it)`,
		`4:9 error duplicate name: "A" is already defined in the file (as an enum)`,
		`5:41 error duplicate name: "Get" is already defined in service "S" (as an rpc)`,
	}},
	{"package scope name", proto3Header + "package p.q;\nmessage M {}\nmessage M {}", []string{
		`4:9 error duplicate name: "M" is already defined in package "p.q" (as a message)`,
	}},
	{"reserved ranges", proto3Header + "message M {\n  reserved 2, 5 to 10, 8; reserved \"x\";\n  reserved 20 to 10;\n  int32 x = 6;\n  int32 y = 2;\n}", []string{
		`3:24 error range: reserved range 8 overlaps with reserved range 5 to 10`,
		`4:12 error range: range end 10 is before its start 20`,
		`5:13 error reserved: field "x" uses reserved number 6`,
		`6:13 error reserved: field "y" uses reserved number 2`,
		`5:9 error reserved: field name "x" is reserved`,
	}},
	{"extension ranges", proto2Header + "message M {\n  extensions 100 to 200;\n  extensions 150 to max;\n  reserved 190 to 210;\n  optional int32 a = 150;\n}", []string{
		`3:14 error range: extension range 100 to 200 overlaps with reserved range 190 to 210`,
		`4:14 error range: extension range 150 to 536870911 overlaps with extension range 100 to 200`,
		`4:14 error range: extension range 150 to 536870911 overlaps with reserved range 190 to 210`,
		`6:22 error range: field "a" uses number 150, which is in extension range 100 to 200`,
	}},
	{"proto3 labels", proto3Header + "message M {\n  required int32 a = 1;\n  repeated map<string, int32> m = 2;\n  oneof o { optional int32 b = 3; }\n}", []string{
		`3:3 error label: required fields are not allowed in proto3`,
		`4:3 error label: map fields cannot have labels`,
		`5:13 error label: fields in oneofs must not have labels`,
	}},
	{"proto2 labels and groups", proto2Header + "message M {\n  int32 a = 1;\n  optional group lower = 2 {}\n}\nextend M { required int32 x = 100; }", []string{
		`3:3 error label: fields in proto2 files need a label: "required", "optional" or "repeated"`,
		`4:18 error group: group names must start with a capital letter`,
		`6:12 error label: extensions cannot be required`,
	}},
	{"editions labels", editionsHdr + "message M {\n  optional int32 a = 1;\n  required int32 b = 2;\n  repeated int32 c = 3;\n}", []string{
		`3:3 error label: the "optional" label is not allowed in editions; use features.field_presence instead`,
		`4:3 error label: the "required" label is not allowed in editions; use features.field_presence instead`,
	}},
	{"proto3 groups defaults extensions", proto3Header + "message M {\n  group G = 1 {}\n  int32 a = 2 [default = 5];\n  extensions 100 to 200;\n}", []string{
		`3:3 error group: groups are not allowed in proto3; use a nested message instead`,
		`4:16 error default value: explicit default values are not allowed in proto3`,
		`5:3 error extensions: extension ranges are not allowed in proto3`,
	}},
	{"map fields and oneofs", proto3Header + "message M {\n  map<float, int32> a = 1;\n  map<bytes, int32> b = 2;\n  map<M, int32> c = 3;\n  map<.string, int32> d = 4;\n  oneof o { map<string, int32> e = 5; }\n  oneof empty {}\n  map<sint64, M> ok = 7;\n}\nextend M { map<string, string> f = 6; }", []string{
		`3:7 error map field: map key type must be an integral type, bool or string; "float" is not allowed`,
		`4:7 error map field: map key type must be an integral type, bool or string; "bytes" is not allowed`,
		`5:7 error map field: map key type must be an integral type, bool or string; "M" is not allowed`,
		`6:7 error map field: map key type must be an integral type, bool or string; ".string" is not allowed`,
		`7:13 error map field: map fields are not allowed in oneofs`,
		`8:9 error empty oneof: oneof "empty" must have at least one field`,
		`11:12 error map field: map fields are not allowed in extend blocks`,
	}},
	{"enum values", proto3Header + "enum E { A = 1; }\nenum F { option allow_alias = true; B = 0; C = 1; }\nenum G { D = 0; E2 = 0; }\nenum H { I = 0; J = 2147483648; K2 = -2147483648; }\nenum K {}\nenum L { reserved 1 to 3; reserved \"Z\"; M0 = 0; N = 2; Z = 5; }", []string{
		`2:14 error enum value: the first enum value must be zero in proto3`,
		`3:10 error enum value: enum "F" sets allow_alias but no two values share a number`,
		`4:22 error enum value: "E2" uses the same number as "D"; add option allow_alias = true; to the enum to allow aliases`,
		`5:21 error enum value: enum value numbers must be in the int32 range`,
		`6:6 error enum value: enum "K" must have at least one value`,
		`7:53 error reserved: enum value "N" uses reserved number 2`,
		`7:56 error reserved: enum value name "Z" is reserved`,
	}},
	{"proto2 enums may start anywhere", proto2Header + "enum E { A = 1; B = 1 [deprecated = true]; option allow_alias = true; }", nil},
	{"editions enums and reserved names", editionsHdr + "enum Open { A = 1; }\nenum Closed { option features.enum_type = CLOSED; B = 1; }\nmessage M { reserved \"a\", b; }", []string{
		`2:17 error enum value: the first enum value must be zero for open enums`,
		`4:22 error reserved name: reserved names must be identifiers, not strings, in editions`,
	}},
	{"closed enums file-wide", editionsHdr + "option features.enum_type = CLOSED;\nenum E { A = 1; }", nil},
	{"proto3 reserved names", proto3Header + "message M { reserved a, \"b c\"; }", []string{
		`2:22 error reserved name: reserved names must be quoted strings in proto3`,
		`2:25 error reserved name: reserved name "b c" is not a valid identifier`,
	}},
	{"visibility before 2024", proto3Header + "export message M {}\nlocal enum E { A = 0; }", []string{
		`2:1 error visibility: "export" requires edition 2024 or later`,
		`3:1 error visibility: "local" requires edition 2024 or later`,
	}},
	{"visibility in 2024", "edition = \"2024\";\nexport message M { local enum E { A = 0; } }", nil},
	{"group names", proto2Header + "message M {\n  optional group G = 1 { optional int32 g = 1; }\n  optional int32 g = 2;\n  optional int32 x = 1;\n}", []string{
		`4:18 error duplicate name: "g" is already defined in message "M" (as the field of group "G")`,
		`5:22 error duplicate number: field number 1 is already used by "g"`,
	}},
	{"message set extensions", proto2Header + "message MS {\n  option message_set_wire_format = true;\n  extensions 4 to max;\n}\nextend MS { optional int32 big = 2147483647; }\nmessage N { extensions 1 to 2147483647; }", []string{
		`7:29 error range: extension numbers must be in the range 1 to 536870911`,
	}},
	{"nested scopes are separate", proto3Header + "message A { int32 x = 1; message B { int32 x = 1; } }\nmessage C { message B {} }", nil},
	{"extension names share the scope", proto2Header + "message M { extensions 10 to 20; }\nextend M { optional int32 M = 10; }", []string{
		`3:27 error duplicate name: "M" is already defined in the file (as a message)`,
	}},
}

func TestCheck(t *testing.T) {
	t.Parallel()

	for _, tt := range checkCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, errs := Parse("d.proto", tt.src)
			if len(errs) != 0 {
				t.Fatalf("syntax errors: %v", errs)
			}
			var got []string
			for _, d := range Check(f) {
				got = append(got, fmt.Sprintf("%d:%d %s %s: %s", d.Line, d.Column, d.Severity, d.Code, d.Message))
			}
			want := append([]string(nil), tt.want...)
			if !reflect.DeepEqual(sortByPos(got), sortByPos(want)) {
				t.Errorf("Check(%q):\n%s\nwant:\n%s", tt.src, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// sortByPos orders "line:col ..." strings as Check does (by position, then
// in report order), so tables may list diagnostics in any order at one
// position.
func sortByPos(lines []string) []string {
	type keyed struct {
		line, col, i int
		s            string
	}
	var ks []keyed
	for i, s := range lines {
		var l, c int
		fmt.Sscanf(s, "%d:%d", &l, &c)
		ks = append(ks, keyed{l, c, i, s})
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && (ks[j].line < ks[j-1].line || ks[j].line == ks[j-1].line && ks[j].col < ks[j-1].col); j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.s)
	}
	return out
}

func TestDiagnosticString(t *testing.T) {
	t.Parallel()
	f, _ := Parse("w.proto", "message M {}")
	ds := Check(f)
	if len(ds) != 1 || !strings.HasPrefix(ds[0].Error(), "w.proto:1:1: warning: no syntax specified") {
		t.Errorf("diagnostics %v", ds)
	}
	f, _ = Parse("", proto3Header+"message M { int32 a = 0; }")
	ds = Check(f)
	if len(ds) != 1 || ds[0].Error() != "<input>:2:23: field numbers must be in the range 1 to 536870911" {
		t.Errorf("diagnostics %v", ds)
	}
}

// TestCheckPartialTrees runs Check on trees with missing pieces.
func TestCheckPartialTrees(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"message { int32 = ; map< > x; oneof { } reserved 1 to ; extensions to max; }",
		"enum { A = ; = 1; reserved ; } service { rpc (", "extend { group = 1 {", "syntax = ; edition",
		proto3Header + "message M { reserved 99999999999999999999; int32 a = 99999999999999999999; }",
		proto3Header + "enum E { A = 99999999999999999999; }",
	} {
		f, _ := Parse("", src)
		Check(f) // must not panic
	}
}
