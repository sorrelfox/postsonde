package descproto

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

// build returns a FileDescriptorProto made by fn.
func build(t testing.TB, fn func(e *encoder, m *dynamic.Message)) []byte {
	t.Helper()
	e := &encoder{}
	m := dynamic.New(schema().FindMessage(typeFile))
	fn(e, m)
	if e.err != nil {
		t.Fatal(e.err)
	}
	b, err := dynamic.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// simpleFile is a proto3 file with one message, field name fieldName and a
// service.
func simpleFile(name, pkg, msg, fieldName string, deps ...string) func(e *encoder, m *dynamic.Message) {
	return func(e *encoder, m *dynamic.Message) {
		e.set(m, "name", name)
		e.set(m, "package", pkg)
		e.set(m, "syntax", "proto3")
		for _, d := range deps {
			e.add(m, "dependency", d)
		}
		mm := e.addMessage(m, "message_type")
		e.set(mm, "name", msg)
		f := e.addMessage(mm, "field")
		e.set(f, "name", fieldName)
		e.set(f, "number", int32(1))
		e.set(f, "label", dynamic.EnumNumber(labelOptional))
		e.set(f, "type", dynamic.EnumNumber(desc.KindString))
		s := e.addMessage(m, "service")
		e.set(s, "name", msg+"Service")
		x := e.addMessage(s, "method")
		e.set(x, "name", "Call")
		e.set(x, "input_type", "."+pkg+"."+msg)
		e.set(x, "output_type", "."+pkg+"."+msg)
	}
}

func codes(res *linker.Result) []string {
	var out []string
	for _, d := range res.Diagnostics {
		sev := "error"
		if d.Severity == parser.SeverityWarning {
			sev = "warning"
		}
		out = append(out, sev+" "+d.Code+" "+d.File+": "+d.Message)
	}
	return out
}

func wantDiag(t *testing.T, res *linker.Result, want ...string) {
	t.Helper()
	got := codes(res)
	for _, w := range want {
		found := false
		for _, g := range got {
			if strings.Contains(g, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("no diagnostic containing %q in:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

func TestLinkProblems(t *testing.T) {
	ctx := context.Background()
	good := build(t, simpleFile("a/good.proto", "a", "Good", "name"))

	t.Run("not a descriptor", func(t *testing.T) {
		res, err := Link(ctx, [][]byte{{0xff, 0xff}, good})
		if err != nil {
			t.Fatal(err)
		}
		wantDiag(t, res, "error proto.invalid_descriptor descriptor #1: not a valid FileDescriptorProto")
		if res.Pool.FindService("a.GoodService") == nil {
			t.Error("the valid file was not linked")
		}
	})

	t.Run("names that are not identifiers are not rendered", func(t *testing.T) {
		evil := build(t, simpleFile("a/evil.proto", "a", "Evil", "x = 1; } message Injected { string y"))
		res, _ := Link(ctx, [][]byte{evil, good})
		wantDiag(t, res, `error proto.invalid_descriptor a/evil.proto: cannot be read as a .proto file: invalid field name "x = 1; }`)
		if res.Pool.FindMessage("a.Injected") != nil || res.Pool.FindMessage("a.Evil") != nil {
			t.Error("the invalid file was linked")
		}
		badType := build(t, func(e *encoder, m *dynamic.Message) {
			simpleFile("a/t.proto", "a", "T", "f")(e, m)
			svc := getMessages(m, "service")[0]
			e.set(getMessages(svc, "method")[0], "input_type", ".a.T) returns (.a.T); rpc X(.a.T")
		})
		res, _ = Link(ctx, [][]byte{badType})
		wantDiag(t, res, `invalid request type`)
		for _, bad := range []string{"", "/abs.proto", "a/../b.proto", `a\b.proto`, "a//b.proto"} {
			res, _ = Link(ctx, [][]byte{build(t, simpleFile(bad, "a", "B", "f"))})
			wantDiag(t, res, "invalid file name")
		}
	})

	t.Run("unsupported syntax and edition", func(t *testing.T) {
		b1 := build(t, func(e *encoder, m *dynamic.Message) {
			simpleFile("a/s.proto", "a", "S", "f")(e, m)
			e.set(m, "syntax", "proto4")
		})
		b2 := build(t, func(e *encoder, m *dynamic.Message) {
			simpleFile("a/e.proto", "a", "E", "f")(e, m)
			e.set(m, "syntax", "editions")
			e.set(m, "edition", dynamic.EnumNumber(9999))
		})
		res, _ := Link(ctx, [][]byte{b1, b2})
		wantDiag(t, res, `unsupported syntax "proto4"`, "unsupported edition 9999")
	})

	t.Run("duplicates", func(t *testing.T) {
		other := build(t, simpleFile("a/good.proto", "a", "Other", "name"))
		res, _ := Link(ctx, [][]byte{good, good, other})
		wantDiag(t, res, "warning proto.duplicate_descriptor a/good.proto")
		if len(res.Diagnostics) != 1 || res.Pool.FindMessage("a.Other") != nil {
			t.Errorf("diagnostics %q", codes(res))
		}
	})

	t.Run("missing dependency", func(t *testing.T) {
		b := build(t, simpleFile("a/dep.proto", "a", "Dep", "f", "a/missing.proto", "google/protobuf/any.proto"))
		res, _ := Link(ctx, [][]byte{b})
		wantDiag(t, res, `"a/missing.proto" not found`)
		if len(res.Diagnostics) != 1 {
			t.Errorf("diagnostics %q", codes(res))
		}
	})

	t.Run("well-known files use the built-in copies", func(t *testing.T) {
		fake := build(t, simpleFile("google/protobuf/any.proto", "google.protobuf", "Any", "not_type_url"))
		b := build(t, simpleFile("a/dep.proto", "a", "Dep", "f", "google/protobuf/any.proto"))
		res := linkBytes(t, [][]byte{fake, b})
		if res.Pool.FindMessage("google.protobuf.Any").FieldByName("type_url") == nil {
			t.Error("Any is not the built-in one")
		}
	})

	t.Run("unknown custom options", func(t *testing.T) {
		b := build(t, func(e *encoder, m *dynamic.Message) {
			simpleFile("a/opt.proto", "a", "Opt", "f")(e, m)
			f := getMessages(getMessages(m, "message_type")[0], "field")[0]
			opts := dynamic.New(schema().FindMessage(optField))
			e.set(opts, "deprecated", true)
			opts.SetUnknown([]byte{0x8a, 0xb5, 0x18, 0x01, 'x'}) // field 50001, "x"
			e.fail(f.Set(field(f, "options"), opts))
		})
		res, _ := Link(ctx, [][]byte{b})
		wantDiag(t, res, "warning proto.unknown_options : 1 option value(s)")
		if !res.Pool.FindMessage("a.Opt").FieldByName("f").Deprecated {
			t.Error("known options were lost")
		}
	})

	t.Run("nothing linkable", func(t *testing.T) {
		res, err := Link(ctx, [][]byte{{0xff}})
		if err != nil || res.Pool == nil || len(res.Pool.Files()) != 0 || !res.HasErrors() {
			t.Errorf("res %+v, err %v", res, err)
		}
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := Link(cctx, [][]byte{good}); !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled: %v", err)
		}
	})
}

func TestSource(t *testing.T) {
	name, src, err := Source(goldenDescriptor(t, "shop/v1/editions.proto"))
	if err != nil {
		t.Fatal(err)
	}
	want := `edition = "2023";
package shop.v1;
option features.field_presence = IMPLICIT;
option features.utf8_validation = NONE;
message Inventory {
  string warehouse = 1;
  int32 count = 2 [features.field_presence = EXPLICIT];
  string owner = 3 [features.field_presence = LEGACY_REQUIRED];
  .shop.v1.Inventory.Bin bin = 4 [features.message_encoding = DELIMITED];
  repeated int32 slots = 5 [features.repeated_field_encoding = EXPANDED];
  .shop.v1.Mode mode = 6 [features.field_presence = EXPLICIT];
  message Bin {
    string code = 1;
  }
  reserved one, two;
}
enum Mode {
  option features.enum_type = CLOSED;
  MODE_A = 1;
  MODE_B = 2;
}
service InventoryService {
  rpc Get(.shop.v1.Inventory) returns (.shop.v1.Inventory);
}
`
	if name != "shop/v1/editions.proto" || src != want {
		t.Errorf("Source = %q\n%s", name, src)
	}
	if _, _, err := Source([]byte{0x0a}); err == nil {
		t.Error("truncated input: no error")
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":          `"plain"`,
		"q\"b\\":         `"q\"b\\"`,
		"nl\n\ttab":      `"nl\n\ttab"`,
		"\x00\x7f\xff":   `"\000\177\377"`,
		"héllo ✓":        `"héllo ✓"`,
		"​":              `"\342\200\213"`,
		"bad\xe2\x82utf": `"bad\342\202utf"`,
	} {
		if got := quote([]byte(in)); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{`\000\001\377abc\\`: "\x00\x01\xffabc\\", `\x41\n\'`: "A\n'"} {
		got, err := unescapeC(in)
		if err != nil || string(got) != want {
			t.Errorf("unescapeC(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{`\`, `\q`, `\400`, `\x`} {
		if _, err := unescapeC(bad); err == nil {
			t.Errorf("unescapeC(%q): no error", bad)
		}
	}
}

// FuzzLink checks that arbitrary descriptor bytes never make Link panic,
// and that whatever links renders to source that parses again.
func FuzzLink(f *testing.F) {
	for _, name := range schemaFiles {
		f.Add(goldenDescriptor(f, name))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := Link(context.Background(), [][]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range res.Files {
			if _, errs := parser.Parse(file.Path, file.AST.Source); len(errs) > 0 {
				t.Fatalf("rendered source does not parse: %v\n%s", errs[0], file.AST.Source)
			}
		}
	})
}
