package parser

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
)

// parseRealWorld parses a vendored third-party file from testdata/realworld
// (see its README.md) and fails on any syntax error, any Check diagnostic
// (warnings included) or a placeholder identifier.
func parseRealWorld(t *testing.T, rel string) *ast.File {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "realworld", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	f, errs := Parse(rel, string(data))
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
	if f.Syntax() != ast.Proto2 || f.SyntaxDecl() == nil {
		t.Errorf("syntax = %v (declared: %v), want an explicit proto2", f.Syntax(), f.SyntaxDecl() != nil)
	}
	return f
}

// realWorldStats counts the proto2 constructs of a file.
type realWorldStats struct {
	messages, enums, groups, extends, extensions int
	labels                                       map[ast.Label]int // fields and groups
	defaults                                     int
}

func statsOf(f *ast.File) realWorldStats {
	s := realWorldStats{labels: map[ast.Label]int{}}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Message:
			s.messages++
		case *ast.Enum:
			s.enums++
		case *ast.Group:
			s.groups++
			s.labels[n.Label]++
		case *ast.Extend:
			s.extends++
		case *ast.Extensions:
			s.extensions++
		case *ast.Field:
			s.labels[n.Label]++
		case *ast.Option:
			if n.Name.String() == "default" {
				s.defaults++
			}
		}
		return true
	})
	return s
}

// findDecl walks a dotted path of message, enum, group, field and extend
// (by extendee) names from the file's top level.
func findDecl(t *testing.T, f *ast.File, path string) ast.Decl {
	t.Helper()
	body := f.Decls
	var found ast.Decl
	for _, name := range strings.Split(path, ".") {
		found = nil
		for _, d := range body {
			var n string
			var inner []ast.Decl
			switch d := d.(type) {
			case *ast.Message:
				n, inner = d.Name.Value, d.Body
			case *ast.Enum:
				n, inner = d.Name.Value, d.Body
			case *ast.Group:
				n, inner = d.Name.Value, d.Body
			case *ast.Oneof:
				n, inner = d.Name.Value, d.Body
			case *ast.Extend:
				n, inner = "extend "+d.Extendee.String(), d.Body
			case *ast.Field:
				n = d.Name.Value
			}
			if n == name {
				found, body = d, inner
				break
			}
		}
		if found == nil {
			t.Fatalf("%s: no %q", path, name)
		}
	}
	return found
}

func findField(t *testing.T, f *ast.File, path string) *ast.Field {
	t.Helper()
	fd, ok := findDecl(t, f, path).(*ast.Field)
	if !ok {
		t.Fatalf("%s is not a field", path)
	}
	return fd
}

// compactOption returns the value of the named option in a field's brackets.
func compactOption(t *testing.T, fd *ast.Field, name string) ast.Value {
	t.Helper()
	if fd.Options != nil {
		for _, o := range fd.Options.Options {
			if o.Name.String() == name {
				return o.Value
			}
		}
	}
	t.Fatalf("field %s has no option %s", fd.Name.Value, name)
	return nil
}

func wantIdent(t *testing.T, what string, v ast.Value, want string) {
	t.Helper()
	id, ok := v.(*ast.IdentLit)
	if !ok {
		t.Fatalf("%s = %T, want identifier %s", what, v, want)
	}
	got := id.Value
	if id.Negative {
		got = "-" + got
	}
	if got != want {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func wantString(t *testing.T, what string, v ast.Value, want string) {
	t.Helper()
	s, ok := v.(*ast.StringLit)
	if !ok {
		t.Fatalf("%s = %T, want a string", what, v)
	}
	if s.Value != want {
		t.Errorf("%s = %q, want %q", what, s.Value, want)
	}
}

func wantStats(t *testing.T, got, want realWorldStats) {
	t.Helper()
	if got.messages != want.messages || got.enums != want.enums || got.groups != want.groups ||
		got.extends != want.extends || got.extensions != want.extensions || got.defaults != want.defaults {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
	for _, l := range []ast.Label{ast.LabelNone, ast.LabelOptional, ast.LabelRequired, ast.LabelRepeated} {
		if got.labels[l] != want.labels[l] {
			t.Errorf("%q labels = %d, want %d", l, got.labels[l], want.labels[l])
		}
	}
}

// TestRealWorldDescriptorProto parses google/protobuf/descriptor.proto, the
// proto2 file every options import depends on.
func TestRealWorldDescriptorProto(t *testing.T) {
	t.Parallel()
	f := parseRealWorld(t, "protobuf/descriptor.proto")

	if got := f.Package(); got != "google.protobuf" {
		t.Errorf("package = %q", got)
	}
	s := statsOf(f)
	wantStats(t, s, realWorldStats{
		messages: 34, enums: 20, extensions: 14, defaults: 27,
		labels: map[ast.Label]int{ast.LabelOptional: 133, ast.LabelRequired: 2, ast.LabelRepeated: 41},
	})

	// option optimize_for = SPEED; is stored raw, as an identifier.
	var optimize *ast.Option
	for _, d := range f.Decls {
		if o, ok := d.(*ast.Option); ok && o.Name.String() == "optimize_for" {
			optimize = o
		}
	}
	if optimize == nil {
		t.Fatal("no file option optimize_for")
	}
	wantIdent(t, "optimize_for", optimize.Value, "SPEED")

	// Every *Options message ends with extensions 1000 to max;.
	for _, name := range []string{"ExtensionRangeOptions", "FileOptions", "MessageOptions", "FieldOptions",
		"OneofOptions", "EnumOptions", "EnumValueOptions", "ServiceOptions", "MethodOptions"} {
		msg := findDecl(t, f, name).(*ast.Message)
		var ok bool
		for _, d := range msg.Body {
			if e, isExt := d.(*ast.Extensions); isExt && len(e.Ranges) == 1 &&
				e.Ranges[0].From.Value == 1000 && e.Ranges[0].Max && e.Ranges[0].To == nil {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s: no `extensions 1000 to max;`", name)
		}
	}

	// Extension declarations: a text-format message literal on a range.
	set := findDecl(t, f, "FileDescriptorSet").(*ast.Message)
	var decl *ast.MessageLit
	for _, d := range set.Body {
		if e, ok := d.(*ast.Extensions); ok && e.Options != nil && e.Options.Options[0].Name.String() == "declaration" {
			decl, _ = e.Options.Options[0].Value.(*ast.MessageLit)
		}
	}
	if decl == nil || len(decl.Fields) != 3 || decl.Fields[2].Name.Name.String() != "full_name" {
		t.Fatalf("FileDescriptorSet extension declaration = %#v", decl)
	}
	wantString(t, "declaration.full_name", decl.Fields[2].Value, ".buf.descriptor.v1.buf_file_descriptor_set_extension")

	// FeatureSet: a bounded range with seven repeated declaration options
	// (comma-separated literal fields), then plain ranges.
	var featureRanges []*ast.Extensions
	for _, d := range findDecl(t, f, "FeatureSet").(*ast.Message).Body {
		if e, ok := d.(*ast.Extensions); ok {
			featureRanges = append(featureRanges, e)
		}
	}
	if len(featureRanges) != 3 {
		t.Fatalf("FeatureSet extension ranges = %d, want 3", len(featureRanges))
	}
	if r := featureRanges[0].Ranges[0]; r.From.Value != 1000 || r.To == nil || r.To.Value != 9994 ||
		featureRanges[0].Options == nil || len(featureRanges[0].Options.Options) != 7 {
		t.Errorf("FeatureSet first range = %#v", featureRanges[0])
	} else {
		goDecl := featureRanges[0].Options.Options[2].Value.(*ast.MessageLit)
		wantString(t, "go declaration full_name", goDecl.Fields[1].Value, ".pb.go")
	}
	if r := featureRanges[2].Ranges[0]; r.From.Value != 10000 || r.To != nil || r.Max {
		t.Errorf("FeatureSet single-number range = %#v", r)
	}
	if verification := findField(t, f, "ExtensionRangeOptions.verification"); verification.Options == nil || len(verification.Options.Options) != 2 {
		t.Errorf("verification options = %#v", verification.Options)
	} else {
		wantIdent(t, "verification default", compactOption(t, verification, "default"), "UNVERIFIED")
		wantIdent(t, "verification retention", compactOption(t, verification, "retention"), "RETENTION_SOURCE")
	}

	// Defaults and field options.
	wantIdent(t, "client_streaming default", compactOption(t, findField(t, f, "MethodDescriptorProto.client_streaming"), "default"), "false")
	wantIdent(t, "optimize_for default", compactOption(t, findField(t, f, "FileOptions.optimize_for"), "default"), "SPEED")
	wantIdent(t, "ctype default", compactOption(t, findField(t, f, "FieldOptions.ctype"), "default"), "STRING")
	weak := findField(t, f, "FieldOptions.weak")
	wantIdent(t, "weak default", compactOption(t, weak, "default"), "false")
	wantIdent(t, "weak deprecated", compactOption(t, weak, "deprecated"), "true")
	conflicts := findField(t, f, "MessageOptions.deprecated_legacy_json_field_conflicts")
	if conflicts.Label != ast.LabelOptional || conflicts.Type.String() != "bool" || conflicts.Number.Value != 11 {
		t.Errorf("deprecated_legacy_json_field_conflicts = %v %v = %d", conflicts.Label, conflicts.Type, conflicts.Number.Value)
	}
	wantIdent(t, "deprecated_legacy_json_field_conflicts deprecated", compactOption(t, conflicts, "deprecated"), "true")
	if jn := findField(t, f, "FieldDescriptorProto.json_name"); jn.Type.String() != "string" || jn.Number.Value != 10 {
		t.Errorf("json_name = %v = %d", jn.Type, jn.Number.Value)
	}
	for _, path := range []string{"UninterpretedOption.NamePart.name_part", "UninterpretedOption.NamePart.is_extension"} {
		if fd := findField(t, f, path); fd.Label != ast.LabelRequired {
			t.Errorf("%s label = %v, want required", path, fd.Label)
		}
	}

	// A multi-line message literal with adjacent strings concatenated.
	support, ok := compactOption(t, findField(t, f, "FileOptions.java_multiple_files"), "feature_support").(*ast.MessageLit)
	if !ok {
		t.Fatal("java_multiple_files feature_support is not a message literal")
	}
	var removal string
	for _, fl := range support.Fields {
		if fl.Name.Name.String() == "removal_error" {
			removal = fl.Value.(*ast.StringLit).Value
		}
	}
	if !strings.HasPrefix(removal, "This behavior is enabled by default in editions 2024 and above. To disable it,") ||
		!strings.HasSuffix(removal, "on individual messages, enums, or services.") {
		t.Errorf("removal_error = %q", removal)
	}
}

// TestRealWorldProto2Fields parses protoc-gen-go's proto2 field test file:
// every label, groups (also in a oneof) and default values of every type.
func TestRealWorldProto2Fields(t *testing.T) {
	t.Parallel()
	f := parseRealWorld(t, "protobuf-go/proto2/fields.proto")

	wantStats(t, statsOf(f), realWorldStats{
		messages: 2, enums: 1, groups: 4, defaults: 24,
		// oneof fields and their group have no label.
		labels: map[ast.Label]int{ast.LabelNone: 21, ast.LabelOptional: 44, ast.LabelRequired: 19, ast.LabelRepeated: 19},
	})

	for path, label := range map[string]ast.Label{
		"FieldTestMessage.OptionalGroup":          ast.LabelOptional,
		"FieldTestMessage.RequiredGroup":          ast.LabelRequired,
		"FieldTestMessage.RepeatedGroup":          ast.LabelRepeated,
		"FieldTestMessage.oneof_field.OneofGroup": ast.LabelNone,
	} {
		g, ok := findDecl(t, f, path).(*ast.Group)
		if !ok {
			t.Fatalf("%s is not a group", path)
		}
		if g.Label != label || len(g.Body) != 1 {
			t.Errorf("%s: label %v, %d body decls", path, g.Label, len(g.Body))
		}
	}
	if g := findDecl(t, f, "FieldTestMessage.RequiredGroup").(*ast.Group); g.Number.Value != 118 {
		t.Errorf("RequiredGroup number = %d", g.Number.Value)
	}
	if fd := findField(t, f, "FieldTestMessage.RequiredGroup.required_group"); fd.Label != ast.LabelRequired || fd.Number.Value != 119 {
		t.Errorf("required_group = %v %d", fd.Label, fd.Number.Value)
	}

	def := func(name string) ast.Value {
		return compactOption(t, findField(t, f, "FieldTestMessage."+name), "default")
	}
	wantIdent(t, "default_bool", def("default_bool"), "true")
	wantIdent(t, "default_enum", def("default_enum"), "ONE")
	if v, ok := def("default_int64").(*ast.IntLit); !ok || v.Value != 1 {
		t.Errorf("default_int64 = %#v", def("default_int64"))
	}
	if v, ok := def("default_double").(*ast.FloatLit); !ok || v.Value != 3.1415 || v.Raw != "3.1415" {
		t.Errorf("default_double = %#v", def("default_double"))
	}
	wantString(t, "default_string", def("default_string"), "hello,\"world!\"\n")
	wantString(t, "default_bytes", def("default_bytes"), "hello,\xde\xad\xbe\xef")
	wantString(t, "default_zero_bytes", def("default_zero_bytes"), "")
	wantIdent(t, "default_float_neginf", def("default_float_neginf"), "-inf")
	wantIdent(t, "default_double_posinf", def("default_double_posinf"), "inf")
	wantIdent(t, "default_double_nan", def("default_double_nan"), "nan")

	if fd := findField(t, f, "FieldTestMessage.oneof_field.oneof_largest_tag"); fd.Number.Value != math.MaxInt32>>2 {
		t.Errorf("oneof_largest_tag = %d", fd.Number.Value)
	}
}

// TestRealWorldProto2Extensions parses protobuf-go's proto2 text-format test
// file: nested groups, extension ranges, top-level and nested extend blocks
// and message sets.
func TestRealWorldProto2Extensions(t *testing.T) {
	t.Parallel()
	f := parseRealWorld(t, "protobuf-go/textpb2/test.proto")

	wantStats(t, statsOf(f), realWorldStats{
		messages: 18, enums: 2, groups: 3, extends: 5, extensions: 3,
		labels: map[ast.Label]int{ast.LabelNone: 1, ast.LabelOptional: 66, ast.LabelRequired: 8, ast.LabelRepeated: 21},
	})
	var imports int
	for _, d := range f.Decls {
		if _, ok := d.(*ast.Import); ok {
			imports++
		}
	}
	if imports != 7 {
		t.Errorf("imports = %d, want 7", imports)
	}

	if fd := findField(t, f, "Nests.OptGroup.OptNestedGroup.opt_fixed32"); fd.Type.String() != "fixed32" {
		t.Errorf("nested group field type = %v", fd.Type)
	}
	if g := findDecl(t, f, "Nests.RptGroup").(*ast.Group); g.Label != ast.LabelRepeated || g.Number.Value != 5 {
		t.Errorf("RptGroup = %v %d", g.Label, g.Number.Value)
	}

	// extensions 20 to 100; between ordinary fields.
	var r *ast.Range
	for _, d := range findDecl(t, f, "Extensions").(*ast.Message).Body {
		if e, ok := d.(*ast.Extensions); ok {
			r = e.Ranges[0]
		}
	}
	if r == nil || r.From.Value != 20 || r.To == nil || r.To.Value != 100 || r.Max {
		t.Errorf("Extensions range = %#v", r)
	}

	// Top-level and nested extend blocks of the same extendee.
	if fd := findField(t, f, "extend Extensions.opt_ext_partial"); fd.Type.String() != "PartialRequired" || fd.Number.Value != 25 {
		t.Errorf("top-level opt_ext_partial = %v %d", fd.Type, fd.Number.Value)
	}
	if fd := findField(t, f, "ExtensionsContainer.extend Extensions.rpt_ext_nested"); fd.Label != ast.LabelRepeated || fd.Number.Value != 63 {
		t.Errorf("nested rpt_ext_nested = %v %d", fd.Label, fd.Number.Value)
	}

	// A message set: the option is stored raw and the range runs to max.
	ms := findDecl(t, f, "MessageSet").(*ast.Message)
	opt, ok := ms.Body[0].(*ast.Option)
	if !ok || opt.Name.String() != "message_set_wire_format" {
		t.Fatalf("MessageSet first decl = %#v", ms.Body[0])
	}
	wantIdent(t, "message_set_wire_format", opt.Value, "true")
	if e, ok := ms.Body[1].(*ast.Extensions); !ok || e.Ranges[0].From.Value != 4 || !e.Ranges[0].Max {
		t.Errorf("MessageSet extensions = %#v", ms.Body[1])
	}
	if fd := findField(t, f, "MessageSetExtension.extend MessageSet.message_set_extension"); fd.Type.String() != "MessageSetExtension" {
		t.Errorf("message_set_extension type = %v", fd.Type)
	}
}
