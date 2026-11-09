package parser

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// render is the golden form of a parse: the tree, the syntax errors and the
// Check diagnostics, with line:column.
func render(name, src string) string {
	f, errs := Parse(name, src)
	var b strings.Builder
	fmt.Fprintf(&b, "# tree\n%s# errors\n", ast.Dump(f))
	for _, e := range errs {
		fmt.Fprintf(&b, "%d:%d [%d:%d] %s: %s\n", e.Line, e.Column, e.Start, e.End, e.Code, e.Message)
	}
	b.WriteString("# diagnostics\n")
	for _, d := range Check(f) {
		fmt.Fprintf(&b, "%d:%d [%d:%d] %s %s: %s\n", d.Line, d.Column, d.Start, d.End, d.Severity, d.Code, d.Message)
	}
	return b.String()
}

// TestRealWorldFiles parses realistic proto3, proto2 and editions files
// without errors or error diagnostics and compares the trees with golden
// dumps.
func TestRealWorldFiles(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join("testdata", "files", "*.proto"))
	if err != nil || len(paths) < 6 {
		t.Fatalf("test files: %v, %v", paths, err)
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".proto")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			f, errs := Parse(filepath.Base(path), src)
			for _, e := range errs {
				t.Errorf("syntax error: %v", e)
			}
			for _, d := range Check(f) {
				t.Errorf("diagnostic: %v", d)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Missing() {
					t.Errorf("missing identifier at %v", f.Position(id.Start))
				}
				return true
			})
			golden.Assert(t, "files/"+name, []byte(render(filepath.Base(path), src)))
		})
	}
}

func TestFileAccessors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src    string
		syntax ast.SyntaxKind
		pkg    string
	}{
		{``, ast.Proto2, ""},
		{`syntax = "proto2"; package a;`, ast.Proto2, "a"},
		{`syntax = 'proto3'; package a.b.c;`, ast.Proto3, "a.b.c"},
		{`syntax = "pro" "to3";`, ast.Proto3, ""},
		{`edition = "2023"; package x; package y;`, ast.Editions, "x"},
		{`syntax = "proto4";`, ast.UnknownSyntax, ""},
	}
	for _, tt := range tests {
		f, _ := Parse("a.proto", tt.src)
		if got := f.Syntax(); got != tt.syntax {
			t.Errorf("Syntax(%q) = %v, want %v", tt.src, got, tt.syntax)
		}
		if got := f.Package(); got != tt.pkg {
			t.Errorf("Package(%q) = %q, want %q", tt.src, got, tt.pkg)
		}
	}
}

// TestComments checks comment attachment against the example in protoc's
// descriptor.proto (SourceCodeInfo.Location.leading_comments).
func TestComments(t *testing.T) {
	t.Parallel()

	src := `// File header, detached from syntax.

// Leading comment for syntax.
syntax = "proto2"; // Trailing comment for syntax.

message M { // Trailing comment for M.
  optional int32 foo = 1;  // Comment attached to foo.
  // Comment attached to bar.
  optional int32 bar = 2;

  optional string baz = 3;
  // Comment attached to baz.
  // Another line attached to baz.

  // Comment attached to moo.
  //
  // Another line attached to moo.
  optional double moo = 4;

  // Detached comment for corge. This is not leading or trailing comments
  // to moo or corge because there are blank lines separating it from
  // both.

  // Detached comment for corge paragraph 2.

  optional string corge = 5;
  /* Block comment attached
   * to corge.  Leading asterisks
   * will be removed. */
  /* Block comment attached to
   * grault. */
  optional int32 grault = 6;

  optional int32 a = 7; /* same line */ optional int32 b = 8;
  optional int32 c = 9; // trailing for c
  optional int32 d = 10; /* ambiguous */ optional int32 e = 11;

  // ignored detached comments.
}
// Leading for N.
message N {}
`
	f, errs := Parse("c.proto", src)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	got := map[string]ast.Comments{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Syntax:
			got["syntax"] = n.Comments
		case *ast.Message:
			got[n.Name.Value] = n.Comments
		case *ast.Field:
			got[n.Name.Value] = n.Comments
		}
		return true
	})
	want := map[string]ast.Comments{
		"syntax": {Leading: " Leading comment for syntax.\n", Trailing: " Trailing comment for syntax.\n",
			Detached: []string{" File header, detached from syntax.\n"}},
		"M":   {Trailing: " Trailing comment for M.\n"},
		"foo": {Trailing: " Comment attached to foo.\n"},
		"bar": {Leading: " Comment attached to bar.\n"},
		"baz": {Trailing: " Comment attached to baz.\n Another line attached to baz.\n"},
		"moo": {Leading: " Comment attached to moo.\n\n Another line attached to moo.\n"},
		"corge": {
			Detached: []string{
				" Detached comment for corge. This is not leading or trailing comments\n" +
					" to moo or corge because there are blank lines separating it from\n both.\n",
				" Detached comment for corge paragraph 2.\n",
			},
			Trailing: " Block comment attached\n to corge.  Leading asterisks\n will be removed. ",
		},
		"grault": {Leading: " Block comment attached to\n grault. "},
		"a":      {},
		"b":      {},
		"c":      {Trailing: " trailing for c\n"},
		"d":      {},
		"e":      {},
		"N":      {Leading: " Leading for N.\n"},
	}
	for name, w := range want {
		if g := got[name]; !reflect.DeepEqual(g, w) {
			t.Errorf("comments of %s:\n got %#v\nwant %#v", name, g, w)
		}
	}
	if len(f.Comments) != 22 || !f.Comments[len(f.Comments)-3].Block {
		t.Errorf("File.Comments = %+v", f.Comments)
	}
}

// ---- values ----

func TestOptionValues(t *testing.T) {
	t.Parallel()

	loc := func(s, e int) ast.Loc { return ast.Loc{Start: s, End: e} }
	// "option o = " is 11 bytes.
	tests := []struct {
		src  string
		want ast.Value
	}{
		{"42", &ast.IntLit{Loc: loc(11, 13), Raw: "42", Value: 42}},
		{"-0x10", &ast.IntLit{Loc: loc(11, 16), Negative: true, Raw: "0x10", Value: 16}},
		{"-9223372036854775808", &ast.IntLit{Loc: loc(11, 31), Negative: true, Raw: "9223372036854775808", Value: 1 << 63}},
		{"-9223372036854775809", &ast.FloatLit{Loc: loc(11, 31), Negative: true, Raw: "9223372036854775809", Value: -9223372036854775809}},
		{"18446744073709551616", &ast.FloatLit{Loc: loc(11, 31), Raw: "18446744073709551616", Value: 18446744073709551616}},
		{"-2.5e3", &ast.FloatLit{Loc: loc(11, 17), Negative: true, Raw: "2.5e3", Value: -2500}},
		{"-inf", &ast.IdentLit{Loc: loc(11, 15), Negative: true, Value: "inf"}},
		{"FOO", &ast.IdentLit{Loc: loc(11, 14), Value: "FOO"}},
		{`"a" 'b'`, &ast.StringLit{Loc: loc(11, 18), Value: "ab"}},
		{"{}", &ast.MessageLit{Loc: loc(11, 13)}},
	}
	for _, tt := range tests {
		src := "option o = " + tt.src + ";"
		f, errs := Parse("", src)
		if len(errs) != 0 {
			t.Errorf("Parse(%q) errors: %v", src, errs)
			continue
		}
		if got := f.Decls[0].(*ast.Option).Value; !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Parse(%q) value = %#v, want %#v", src, got, tt.want)
		}
	}

	if v, _ := Parse("", "option o = nan;"); v.Decls[0].(*ast.Option).Value.(*ast.IdentLit).Value != "nan" {
		t.Error("nan")
	}
	f, _ := Parse("", "option o = -1e999;")
	if v := f.Decls[0].(*ast.Option).Value.(*ast.FloatLit).Value; !math.IsInf(v, -1) {
		t.Errorf("-1e999 = %v", v)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	f, errs := Parse("", `option (.a.b).c.(d) = 1; message M { .x . y z = 1; }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	name := f.Decls[0].(*ast.Option).Name
	if got := name.String(); got != "(.a.b).c.(d)" || len(name.Parts) != 3 || !name.Parts[0].Extension || name.Parts[1].Extension {
		t.Errorf("option name %q %+v", got, name.Parts)
	}
	fld := f.Decls[1].(*ast.Message).Body[0].(*ast.Field)
	if got := fld.Type.String(); got != ".x.y" || !fld.Type.LeadingDot || len(fld.Type.Parts) != 2 {
		t.Errorf("type %q", got)
	}
	if got := MapEntryName("foo_bar_1x"); got != "FooBar1xEntry" {
		t.Errorf("MapEntryName = %q", got)
	}
}

// ---- errors ----

// errorCases are invalid inputs with every syntax error as
// "line:column code: message".
var errorCases = []struct {
	name, src string
	want      []string
}{
	{"unquoted syntax", "syntax = proto3;", []string{
		`1:10 unexpected token: expected a quoted syntax such as "proto3", found "proto3"`,
	}},
	{"unquoted edition", "edition = 2023;", []string{
		`1:11 unexpected token: expected a quoted edition such as "2023", found number 2023`,
	}},
	{"missing semicolon at end of line", "package foo.bar\nmessage M {}", []string{
		`1:16 unexpected token: expected ";", found "message"`,
	}},
	{"missing semicolon after field", "syntax = \"proto3\";\nmessage M {\n  int32 a = 1\n  string b = 2;\n}", []string{
		`3:14 unexpected token: expected ";", found "string"`,
	}},
	{"missing semicolon before brace", "message M { int32 a = 1 }", []string{
		`1:24 unexpected token: expected ";", found "}"`,
	}},
	{"missing field number", "message M {\n  int32 a = ;\n  string b = 2;\n}", []string{
		`2:13 unexpected token: expected a field number, found ";"`,
	}},
	{"half-typed field before next line", "message M {\n  int32 a =\n  string b = 2;\n}", []string{
		`3:3 unexpected token: expected a field number, found "string"`,
	}},
	{"missing field name", "message M {\n  int32 = 1;\n}", []string{
		`2:9 unexpected token: expected a field name, found "="`,
	}},
	{"map without comma", "message M {\n  map<string int32> m = 1;\n}", []string{
		`2:14 unexpected token: expected ",", found "int32"`,
	}},
	{"option without equals", "message M {\n  int32 a = 1 [deprecated true];\n}", []string{
		`2:27 unexpected token: expected "=", found "true"`,
	}},
	{"empty compact options", "message M {\n  int32 a = 1 [];\n}", []string{
		`2:16 unexpected token: expected an option name, found "]"`,
	}},
	{"field number beyond 64 bits", "message M {\n  int32 a = 99999999999999999999;\n}", []string{
		`2:13 invalid literal: integer 99999999999999999999 is out of range`,
	}},
	{"float field number", "message M {\n  int32 a = 1.5;\n}", []string{
		`2:13 unexpected token: expected a field number, found number 1.5`,
	}},
	{"rpc returns without parens", "service S {\n  rpc Get(Req) returns Resp;\n}", []string{
		`2:24 unexpected token: expected "(", found "Resp"`,
	}},
	{"field in service", "service S {\n  rpc Get(stream) returns (Resp);\n  int32 x = 1;\n}", []string{
		`3:3 unexpected token: expected an rpc or option, found "int32"`,
	}},
	{"unclosed bodies", "message M {\n  message N {\n    int32 a = 1;\n", []string{
		`1:11 unclosed delimiter: "{" is not closed`,
		`2:13 unclosed delimiter: "{" is not closed`,
	}},
	{"stray closing brace", "}\nmessage M {}", []string{
		`1:1 unexpected token: expected top-level declaration, found "}"`,
	}},
	{"extra closing brace", "message M {} }", []string{
		`1:14 unexpected token: expected top-level declaration, found "}"`,
	}},
	{"enum value errors", "enum E {\n  A = 0\n  B;\n}", []string{
		`2:8 unexpected token: expected ";", found "B"`,
		`3:4 unexpected token: expected "=", found ";"`,
	}},
	{"unclosed extension name", "option (foo = 1;", []string{
		`1:13 unexpected token: expected ")", found "="`,
	}},
	{"negative string", `option foo = -"x";`, []string{
		`1:14 invalid literal: "-" is not allowed before a string`,
	}},
	{"negative identifier", "option foo = -bar;", []string{
		`1:14 invalid literal: "-" is only allowed before a number, inf or nan`,
	}},
	{"float suffix outside literal", "option foo = 1.5f;", []string{
		`1:14 invalid literal: the "f" suffix is only allowed in message literals`,
	}},
	{"literal field without colon", "option foo = { a: 1 b 2 };", []string{
		`1:23 unexpected token: expected ":" or a message, found number 2`,
	}},
	{"unclosed list literal", "option foo = { a: [1, 2 };", []string{
		`1:19 unclosed delimiter: "[" is not closed`,
	}},
	{"misplaced syntax", "message M {}\nsyntax = \"proto3\";", []string{
		`2:1 misplaced statement: the syntax statement must be the first statement in the file`,
	}},
	{"unquoted import", "import foo;", []string{
		`1:8 unexpected token: expected a quoted import path, found "foo"`,
	}},
	{"qualified package", "package .a.b;", []string{
		`1:9 unexpected token: a package name cannot start with "."`,
	}},
	{"reserved numbers then names", `message M { reserved 1, "a"; }`, []string{
		`1:25 unexpected token: expected a number, found string "a"`,
	}},
	{"reserved names then numbers", `message M { reserved "a", 1; }`, []string{
		`1:27 unexpected token: expected a reserved name, found number 1`,
	}},
	{"extension range without end", "message M { extensions 1 to ; }", []string{
		`1:29 unexpected token: expected a number or "max", found ";"`,
	}},
	{"numeric message name", "message 1 {}\nmessage N {}", []string{
		`1:9 unexpected token: expected a message name, found number 1`,
	}},
	{"lexical errors", "message M {\n  int32 a = 1; @\n  $ int32 b = 2;\n}", []string{
		`2:16 lexical error: unexpected character '@'`,
		`3:3 lexical error: unexpected character '$'`,
	}},
	{"oneof without name", "message M { oneof { int32 a = 1; } }", []string{
		`1:19 unexpected token: expected a oneof name, found "{"`,
	}},
	{"unterminated comment", `syntax = "proto3"; /* unterminated`, []string{
		`1:20 lexical error: unterminated block comment`,
	}},
	{"extend without type", "extend { }", []string{
		`1:8 unexpected token: expected the extended message type, found "{"`,
	}},
	{"columns count code points", "// Größe\noption o = \"日本語\" @;\nmessage Maß {}", []string{
		`2:18 lexical error: unexpected character '@'`,
		`3:11 lexical error: unexpected character 'ß'`,
	}},
}

func TestSyntaxErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range errorCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, errs := Parse("e.proto", tt.src)
			var got []string
			for _, e := range errs {
				got = append(got, fmt.Sprintf("%d:%d %s: %s", e.Line, e.Column, e.Code, e.Message))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q) errors:\n%s\nwant:\n%s", tt.src, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// partialCases are invalid inputs whose partial trees are golden files.
var partialCases = []struct{ name, src string }{
	{"half-typed field", "syntax = \"proto3\";\nmessage M {\n  int32 a =\n  string b = 2;\n  repeated\n}"},
	{"unclosed bodies", "syntax = \"proto3\";\n// Outer.\nmessage M {\n  message N {\n    int32 a = 1;\n"},
	{"missing names", "message {\n  int32 = 1;\n  oneof {}\n}\nenum { = 1; }\nservice { rpc (A) returns (B); }"},
	{"missing semicolons", "enum E {\n  A = 0\n  B = 1\n}\nservice S { rpc Get(Req) returns (Resp) }"},
	{"garbage between declarations", "message A {}\n@#$ junk ;\n} message B { int32 x = 1 [a = ]; }"},
	{"unclosed literals", "option (a) = { b: [1, 2 c: <d: 1 };\nmessage M {}"},
}

func TestPartialTrees(t *testing.T) {
	t.Parallel()

	for _, tt := range partialCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			golden.Assert(t, "partial/"+strings.ReplaceAll(tt.name, " ", "_"),
				[]byte("# source\n"+tt.src+"\n"+render("p.proto", tt.src)))
		})
	}
}

func TestErrorString(t *testing.T) {
	t.Parallel()
	_, errs := Parse("dir/a.proto", "message {")
	if len(errs) == 0 || errs[0].Error() != `dir/a.proto:1:9: expected a message name, found "{"` {
		t.Errorf("errors %v", errs)
	}
	_, errs = Parse("", "}")
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "<input>:1:1: ") {
		t.Errorf("errors %v", errs)
	}
	if Code(99).String() != "Code(99)" || DiagCode(99).String() != "DiagCode(99)" {
		t.Error("code strings")
	}
}

func TestDeepNesting(t *testing.T) {
	t.Parallel()

	const n = 100_000
	inputs := map[string]string{
		"messages":      strings.Repeat("message M {", n) + strings.Repeat("}", n),
		"unclosed":      strings.Repeat("message M {", n),
		"literals":      "option o = " + strings.Repeat("{a ", n) + strings.Repeat("}", n) + ";",
		"lists":         "option o = {a: " + strings.Repeat("[", n) + strings.Repeat("]", n) + "};",
		"angle":         "option o = {" + strings.Repeat("a <", n) + strings.Repeat(">", n) + "};",
		"groups":        `syntax = "proto2"; message M {` + strings.Repeat("optional group G = 1 {", n) + strings.Repeat("}", n) + "}",
		"garbage":       strings.Repeat("{[(<", n),
		"closing":       strings.Repeat("}])>", n),
		"many messages": strings.Repeat("message M { int32 a = 1; }\n", n),
	}
	for name, src := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			f, errs := Config{MaxErrors: 10}.Parse("deep.proto", src)
			if f == nil || len(errs) > 10 {
				t.Fatalf("file %v, %d errors", f, len(errs))
			}
			Check(f)
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

func TestTooManyErrors(t *testing.T) {
	t.Parallel()
	_, errs := Config{MaxErrors: 5}.Parse("", strings.Repeat("@ ", 50))
	if len(errs) != 5 || errs[4].Code != ErrTooManyErrors {
		t.Errorf("errors %v", errs)
	}
}
