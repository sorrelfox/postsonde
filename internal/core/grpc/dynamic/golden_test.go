package dynamic

import (
	"bytes"
	"embed"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

//go:embed testdata/golden
var goldenFS embed.FS

type golden struct {
	typeName string
	bin      []byte
	text     string // dump without the type line
}

func readGolden(tb testing.TB, name string) golden {
	tb.Helper()
	bin, err := goldenFS.ReadFile("testdata/golden/" + name + ".bin")
	if err != nil {
		tb.Fatal(err)
	}
	txt, err := goldenFS.ReadFile("testdata/golden/" + name + ".txt")
	if err != nil {
		tb.Fatal(err)
	}
	first, rest, _ := strings.Cut(string(txt), "\n")
	typeName, ok := strings.CutPrefix(first, "type: ")
	if !ok {
		tb.Fatalf("%s.txt: no type line", name)
	}
	return golden{typeName, bin, rest}
}

// TestGoldenRoundTrip decodes every reference encoding written by
// protobuf-go, compares the decoded values with protobuf-go's, and checks
// that encoding them again gives the same bytes.
func TestGoldenRoundTrip(t *testing.T) {
	names := goldenNames(t)
	if len(names) < 30 {
		t.Fatalf("only %d golden files", len(names))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			g := readGolden(t, name)
			m := New(msgType(t, g.typeName))
			if err := (UnmarshalOptions{Resolver: pool(t)}).Unmarshal(g.bin, m); err != nil {
				t.Fatal(err)
			}
			if got := dump(m); got != g.text {
				t.Errorf("decoded values differ\n--- got\n%s--- want\n%s", got, g.text)
			}
			for _, det := range []bool{true, false} {
				out, err := MarshalOptions{Deterministic: det}.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				// Decoding keeps map insertion order, which is protobuf-go's
				// sorted order here, so both modes reproduce the reference.
				if !bytes.Equal(out, g.bin) {
					t.Errorf("deterministic=%v: re-encoded bytes differ\ngot  %s\nwant %s", det, hex.EncodeToString(out), hex.EncodeToString(g.bin))
				}
			}
			if Size(m) != len(g.bin) {
				t.Errorf("Size = %d, want %d", Size(m), len(g.bin))
			}
			c := m.Clone()
			if !Equal(m, c) {
				t.Error("clone differs")
			}

			// Without a resolver extensions stay unknown fields, which are
			// written after the known fields; decoding that with the
			// resolver gives the same message.
			plain := New(m.md)
			if err := Unmarshal(g.bin, plain); err != nil {
				t.Fatal(err)
			}
			out, err := Marshal(plain)
			if err != nil {
				t.Fatal(err)
			}
			hasExt := false
			m.Range(func(fd *desc.Field, _ any) bool {
				hasExt = hasExt || fd.IsExtension
				return true
			})
			if !hasExt && !bytes.Equal(out, g.bin) {
				t.Errorf("without resolver: re-encoded bytes differ\ngot  %x\nwant %x", out, g.bin)
			}
			again := New(m.md)
			if err := (UnmarshalOptions{Resolver: pool(t)}).Unmarshal(out, again); err != nil {
				t.Fatal(err)
			}
			if !Equal(again, m) {
				t.Errorf("without resolver: decoding the re-encoded bytes gives\n%s", dump(again))
			}
		})
	}
}

// TestGoldenEncode builds messages through the API, independently of the
// decoder, and compares their encoding with the references.
func TestGoldenEncode(t *testing.T) {
	nan := math.NaN()
	negZero := math.Copysign(0, -1)
	nested := func(a int32, b string) *Message {
		return build(t, "codec.p3.Nested", "a", a, "b", b)
	}
	cases := map[string]*Message{
		"p3_scalars": build(t, "codec.p3.Scalars",
			"f_double", 1.5, "f_float", float32(-2.25), "f_int32", int32(-1), "f_int64", int64(math.MinInt64),
			"f_uint32", uint32(math.MaxUint32), "f_uint64", uint64(math.MaxUint64), "f_sint32", int32(math.MinInt32),
			"f_sint64", int64(math.MinInt64), "f_fixed32", uint32(0xdeadbeef), "f_fixed64", uint64(math.MaxUint64),
			"f_sfixed32", int32(-5), "f_sfixed64", int64(-6), "f_bool", true, "f_string", "héllo, 世界 🌍",
			"f_bytes", []byte{0, 1, 0xff}, "f_enum", EnumNumber(2),
			"f_nested", build(t, "codec.p3.Nested", "a", int32(7), "b", "x", "child", build(t, "codec.p3.Nested", "a", int32(-1))),
		),
		"p3_float_nan_inf":      build(t, "codec.p3.Scalars", "f_double", nan, "f_float", float32(math.Inf(1))),
		"p3_float_negzero":      build(t, "codec.p3.Scalars", "f_double", negZero, "f_float", float32(negZero)),
		"p3_empty":              build(t, "codec.p3.Scalars", "f_int32", int32(0), "f_string", "", "f_enum", EnumNumber(0)),
		"p3_empty_nested":       build(t, "codec.p3.Scalars", "f_nested", build(t, "codec.p3.Nested")),
		"p3_open_enum_unknown":  build(t, "codec.p3.Scalars", "f_enum", EnumNumber(99)),
		"p3_open_enum_negative": build(t, "codec.p3.Scalars", "f_enum", EnumNumber(-3)),
		"p3_repeated": build(t, "codec.p3.Repeated",
			"r_double", []any{1.5, nan, math.Inf(-1), negZero}, "r_float", []any{float32(0.5), float32(-1)},
			"r_int32", []any{int32(0), int32(-1), int32(math.MaxInt32)}, "r_int64", []any{int64(-2), int64(1) << 40},
			"r_uint32", []any{uint32(0), uint32(math.MaxUint32)}, "r_uint64", []any{uint64(1), uint64(math.MaxUint64)},
			"r_sint32", []any{int32(-1), int32(1), int32(math.MinInt32)}, "r_sint64", []any{int64(-1), int64(math.MaxInt64)},
			"r_fixed32", []any{uint32(1), uint32(2)}, "r_fixed64", []any{uint64(3)}, "r_sfixed32", []any{int32(-4)},
			"r_sfixed64", []any{int64(-5), int64(5)}, "r_bool", []any{true, false, true}, "r_string", []any{"", "two", "ß"},
			"r_bytes", []any{[]byte{}, []byte{1}}, "r_enum", []any{EnumNumber(1), EnumNumber(2), EnumNumber(0), EnumNumber(7)},
			"r_nested", []any{nested(1, "a"), build(t, "codec.p3.Nested"), nested(0, "c")},
			"u_int32", []any{int32(-1), int32(2)}, "u_sint64", []any{int64(-3)}, "u_double", []any{2.5, 0.0},
			"u_enum", []any{EnumNumber(2), EnumNumber(5)}, "u_bool", []any{false}, "u_fixed32", []any{uint32(9), uint32(10)},
		),
		"p3_maps": build(t, "codec.p3.Maps",
			"m_int32", map[any]any{int32(-1): nested(1, "neg"), int32(0): build(t, "codec.p3.Nested"), int32(5): nested(5, "")},
			"m_int64", map[any]any{int64(math.MinInt64): nested(2, ""), int64(3): nested(3, "x")},
			"m_uint32", map[any]any{uint32(math.MaxUint32): nested(4, ""), uint32(0): nested(0, "zero")},
			"m_uint64", map[any]any{uint64(math.MaxUint64): nested(6, ""), uint64(10): nested(7, "")},
			"m_sint32", map[any]any{int32(-7): nested(8, ""), int32(7): nested(9, "")},
			"m_sint64", map[any]any{int64(-8): nested(10, ""), int64(0): build(t, "codec.p3.Nested")},
			"m_fixed32", map[any]any{uint32(1): nested(11, "")},
			"m_fixed64", map[any]any{uint64(2): nested(12, ""), uint64(1): nested(13, "")},
			"m_sfixed32", map[any]any{int32(-3): nested(14, "")},
			"m_sfixed64", map[any]any{int64(-4): nested(15, ""), int64(4): nested(16, "")},
			"m_bool", map[any]any{true: nested(17, "t"), false: nested(18, "f")},
			"m_string", map[any]any{"b": nested(19, ""), "a": nested(20, ""), "": nested(21, "empty"), "é": nested(22, "")},
			"m_string_string", map[any]any{"k": "v", "": "", "ключ": "значение"},
			"m_string_bytes", map[any]any{"x": []byte{0xde, 0xad}, "y": []byte{}},
			"m_int32_enum", map[any]any{int32(1): EnumNumber(1), int32(2): EnumNumber(0), int32(3): EnumNumber(42)},
			"m_string_double", map[any]any{"nan": nan, "neg": -1.25},
			"m_uint64_sint64", map[any]any{uint64(0): int64(0), uint64(1): int64(-1)},
		),
		"p3_oneof_enum_zero":   build(t, "codec.p3.Oneofs", "o_int32", int32(5), "o_enum", EnumNumber(0)),
		"p3_oneof_bytes_empty": build(t, "codec.p3.Oneofs", "plain", "q", "o_nested", nested(1, ""), "o_bytes", []byte{}),
		"p3_optional_zero": build(t, "codec.p3.Optionals", "o_int32", int32(0), "o_string", "", "o_bool", false,
			"o_double", 0.0, "o_enum", EnumNumber(0), "o_nested", build(t, "codec.p3.Nested")),
		"p2_defaults_set": build(t, "codec.p2.Defaults", "d_int32", int32(-42), "d_string", "héllo", "d_double", math.Inf(1),
			"d_enum", EnumNumber(2), "d_bytes", []byte{1, 2}, "d_bool", true, "d_float", float32(nan),
			"d_uint64", uint64(math.MaxUint64), "req", int32(0)),
		"p2_extensions": build(t, "codec.p2.Extendable", "plain", int32(1), "[codec.p2.ext_int32]", int32(-7),
			"[codec.p2.ext_strings]", []any{"x", "yz"},
			"[codec.p2.ext_msg]", build(t, "codec.p2.Defaults", "req", int32(5), "d_string", "s"),
			"[codec.p2.ext_packed]", []any{int32(-1), int32(1)}),
		"ed_unverified_file": build(t, "codec.ednone.Text", "s", "\x80", "list", []any{"ok", "\xc0\x80"}),
	}

	groups := build(t, "codec.p2.Groups")
	g := groups.Mutable(field(t, groups.md, "g")).(*Message)
	fill(t, g, "a", int32(1), "b", "group")
	rl := groups.Mutable(field(t, groups.md, "r")).(*List)
	for i := range 2 {
		r := rl.AppendMessage()
		fill(t, r, "c", int32(10+i))
		il := r.Mutable(field(t, r.md, "inner")).(*List)
		for j := range i + 1 {
			fill(t, il.AppendMessage(), "d", int32(-j-1))
		}
	}
	rl.AppendMessage()
	fill(t, groups, "packed_int32", []any{int32(1), int32(-1), int32(300)}, "expanded_int32", []any{int32(2), int32(-2)},
		"packed_kind", []any{EnumNumber(1), EnumNumber(3)}, "expanded_kind", []any{EnumNumber(2)},
		"no_utf8_check", "\xff\xfea", "kind", EnumNumber(3),
		"kind_map", map[any]any{"b": EnumNumber(3), "a": EnumNumber(1)})
	cases["p2_groups"] = groups

	f := build(t, "codec.ed.Features")
	fill(t, f.Mutable(field(t, f.md, "delimited")).(*Message), "x", int32(1))
	dl := f.Mutable(field(t, f.md, "delimited_list")).(*List)
	for i := range 2 {
		fill(t, dl.AppendMessage(), "x", int32(i))
	}
	fill(t, f, "explicit_int32", int32(0), "implicit_int32", int32(3), "packed", []any{int32(1), int32(2)},
		"expanded", []any{int32(3), int32(4)}, "verified", "✓", "unverified", "\xc3", "closed", EnumNumber(1))
	f.Mutable(field(t, f.md, "length_prefixed"))
	cases["ed_features"] = f

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			g := readGolden(t, name)
			if m.md.FullName != g.typeName {
				t.Fatalf("type %s, golden is %s", m.md.FullName, g.typeName)
			}
			out, err := MarshalOptions{Deterministic: true}.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, g.bin) {
				t.Errorf("encoding differs\ngot  %x\nwant %x", out, g.bin)
			}
			if got := dump(m); got != g.text {
				t.Errorf("values differ\n--- got\n%s--- want\n%s", got, g.text)
			}
		})
	}
}
