package dynamic

import (
	"bytes"
	"math"
	"testing"
	"unicode/utf8"
)

// fuzzTypes are the message types FuzzDecode decodes into, chosen by the
// first input byte.
var fuzzTypes = []string{
	"codec.p3.Scalars", "codec.p3.Repeated", "codec.p3.Maps", "codec.p3.Oneofs", "codec.p3.Optionals",
	"codec.p2.Defaults", "codec.p2.Groups", "codec.p2.Extendable", "codec.p2.Wide", "codec.ed.Features",
	"codec.ed.Unverified",
}

// FuzzDecode decodes arbitrary bytes into rich message types and checks
// that decoding never panics and that decode, encode, decode gives the
// first decoded message again, with a stable encoding.
//
//	go test ./internal/core/grpc/dynamic -run '^$' -fuzz FuzzDecode -fuzztime 60s
func FuzzDecode(f *testing.F) {
	for _, name := range goldenNames(f) {
		g := readGolden(f, name)
		for i, tn := range fuzzTypes {
			if tn == g.typeName {
				f.Add(append([]byte{byte(i)}, g.bin...))
			}
		}
	}
	f.Add([]byte{0, 0x08, 0x80})
	f.Add([]byte{6, 0x0b, 0x10, 0x01})
	p := pool(f)
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) == 0 {
			return
		}
		md := p.FindMessage(fuzzTypes[int(in[0])%len(fuzzTypes)])
		b := in[1:]
		opts := UnmarshalOptions{Resolver: p}
		m1 := New(md)
		if err := opts.Unmarshal(b, m1); err != nil {
			return
		}
		for _, det := range []bool{false, true} {
			out1, err := MarshalOptions{Deterministic: det}.Marshal(m1)
			if err != nil {
				// Only invalid UTF-8 can fail, and decoding rejects it.
				t.Fatalf("encode: %v", err)
			}
			if len(out1) != Size(m1) {
				t.Fatalf("Size = %d, encoded %d", Size(m1), len(out1))
			}
			m2 := New(md)
			if err := opts.Unmarshal(out1, m2); err != nil {
				t.Fatalf("decoding the re-encoded message: %v\n%x", err, out1)
			}
			if !Equal(m1, m2) {
				t.Fatalf("decode(encode(m)) != m\nfirst:\n%s\nsecond:\n%s", dump(m1), dump(m2))
			}
			out2, err := MarshalOptions{Deterministic: det}.Marshal(m2)
			if err != nil || !bytes.Equal(out1, out2) {
				t.Fatalf("encoding is not stable: %v\n%x\n%x", err, out1, out2)
			}
		}
		if !Equal(m1, m1.Clone()) {
			t.Fatal("clone differs")
		}
	})
}

// FuzzRoundTrip builds messages of every field shape from fuzzed values
// and checks that they encode, decode to an equal message and encode to
// the same bytes again.
//
//	go test ./internal/core/grpc/dynamic -run '^$' -fuzz FuzzRoundTrip -fuzztime 60s
func FuzzRoundTrip(f *testing.F) {
	f.Add(int64(-1), uint64(math.MaxUint64), 1.5, "héllo", []byte{0, 0xff}, true, uint8(0))
	f.Add(int64(0), uint64(0), math.Copysign(0, -1), "", []byte{}, false, uint8(3))
	f.Add(int64(math.MinInt64), uint64(1)<<63, math.NaN(), "\x00", []byte(nil), true, uint8(200))
	p := pool(f)
	f.Fuzz(func(t *testing.T, i int64, u uint64, d float64, s string, bs []byte, bl bool, sel uint8) {
		if !utf8.ValidString(s) {
			s = string([]rune(s))
		}
		i32, u32, f32 := int32(i), uint32(u), float32(d)
		nested := build(t, "codec.p3.Nested", "a", i32, "b", s)
		if sel&1 != 0 {
			fill(t, nested, "child", build(t, "codec.p3.Nested", "a", int32(sel)))
		}
		msgs := []*Message{
			build(t, "codec.p3.Scalars", "f_double", d, "f_float", f32, "f_int32", i32, "f_int64", i,
				"f_uint32", u32, "f_uint64", u, "f_sint32", i32, "f_sint64", i, "f_fixed32", u32, "f_fixed64", u,
				"f_sfixed32", i32, "f_sfixed64", i, "f_bool", bl, "f_string", s, "f_bytes", bs,
				"f_enum", EnumNumber(i32), "f_nested", nested),
			build(t, "codec.p3.Repeated", "r_double", []any{d, -d}, "r_float", []any{f32}, "r_int32", []any{i32, -i32},
				"r_int64", []any{i}, "r_uint32", []any{u32}, "r_uint64", []any{u, u >> 1}, "r_sint32", []any{i32},
				"r_sint64", []any{i, ^i}, "r_fixed32", []any{u32}, "r_fixed64", []any{u}, "r_sfixed32", []any{i32},
				"r_sfixed64", []any{i}, "r_bool", []any{bl, !bl}, "r_string", []any{s, ""}, "r_bytes", []any{bs},
				"r_enum", []any{EnumNumber(i32), EnumNumber(sel)}, "r_nested", []any{nested, build(t, "codec.p3.Nested")},
				"u_int32", []any{i32}, "u_sint64", []any{i}, "u_double", []any{d}, "u_enum", []any{EnumNumber(sel)},
				"u_bool", []any{bl}, "u_fixed32", []any{u32}),
			build(t, "codec.p3.Maps", "m_int32", map[any]any{i32: nested, int32(sel): build(t, "codec.p3.Nested")},
				"m_int64", map[any]any{i: nested}, "m_uint32", map[any]any{u32: nested}, "m_uint64", map[any]any{u: nested},
				"m_sint32", map[any]any{-i32: nested}, "m_sint64", map[any]any{-i: nested}, "m_fixed32", map[any]any{u32: nested},
				"m_fixed64", map[any]any{u: nested}, "m_sfixed32", map[any]any{i32: nested}, "m_sfixed64", map[any]any{i: nested},
				"m_bool", map[any]any{bl: nested}, "m_string", map[any]any{s: nested, "k": nested},
				"m_string_string", map[any]any{s: s + "!"}, "m_string_bytes", map[any]any{s: bs},
				"m_int32_enum", map[any]any{i32: EnumNumber(sel)}, "m_string_double", map[any]any{s: d},
				"m_uint64_sint64", map[any]any{u: i}),
			build(t, "codec.p2.Defaults", "d_int32", i32, "d_string", string(bs), "d_double", d, "d_bytes", bs,
				"d_bool", bl, "d_float", f32, "d_uint64", u, "req", int32(sel)),
			build(t, "codec.p2.Extendable", "plain", i32, "[codec.p2.ext_int32]", i32, "[codec.p2.ext_strings]", []any{s, string(bs)},
				"[codec.p2.ext_packed]", []any{i32, -i32}),
			build(t, "codec.ed.Features", "explicit_int32", i32, "implicit_int32", i32, "packed", []any{i32},
				"expanded", []any{i32, i32}, "verified", s, "unverified", string(bs)),
		}
		oneofs := New(msgType(t, "codec.p3.Oneofs"))
		switch sel % 5 {
		case 0:
			fill(t, oneofs, "o_int32", i32)
		case 1:
			fill(t, oneofs, "o_string", s)
		case 2:
			fill(t, oneofs, "o_nested", nested)
		case 3:
			fill(t, oneofs, "o_enum", EnumNumber(i32))
		case 4:
			fill(t, oneofs, "o_bytes", bs)
		}
		groups := New(msgType(t, "codec.p2.Groups"))
		fill(t, groups.Mutable(field(t, groups.md, "g")).(*Message), "a", i32, "b", string(bs))
		r := groups.Mutable(field(t, groups.md, "r")).(*List).AppendMessage()
		fill(t, r.Mutable(field(t, r.md, "inner")).(*List).AppendMessage(), "d", i32)
		fill(t, groups, "packed_int32", []any{i32}, "expanded_int32", []any{i32}, "no_utf8_check", string(bs),
			"kind", EnumNumber(sel%3+1), "kind_map", map[any]any{string(bs): EnumNumber(sel%3 + 1)})
		msgs = append(msgs, oneofs, groups)

		for _, m := range msgs {
			for _, det := range []bool{true, false} {
				out, err := MarshalOptions{Deterministic: det}.Marshal(m)
				if err != nil {
					t.Fatalf("%s: %v", m.md.FullName, err)
				}
				if len(out) != Size(m) {
					t.Fatalf("%s: Size = %d, encoded %d", m.md.FullName, Size(m), len(out))
				}
				back := New(m.md)
				if err := (UnmarshalOptions{Resolver: p}).Unmarshal(out, back); err != nil {
					t.Fatalf("%s: %v", m.md.FullName, err)
				}
				// Implicit-presence zeros were never stored, so the decoded
				// message equals the built one.
				if !Equal(m, back) {
					t.Fatalf("%s: round trip differs\nbuilt:\n%s\ndecoded:\n%s", m.md.FullName, dump(m), dump(back))
				}
				again, err := MarshalOptions{Deterministic: det}.Marshal(back)
				if err != nil || !bytes.Equal(out, again) {
					t.Fatalf("%s: re-encoding differs: %v", m.md.FullName, err)
				}
			}
		}
	})
}
