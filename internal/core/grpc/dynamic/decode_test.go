package dynamic

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
)

// wire builds encodings by hand.
type wire []byte

func (w wire) varint(num protowire.Number, v uint64) wire {
	return protowire.AppendVarint(protowire.AppendTag(w, num, protowire.VarintType), v)
}

func (w wire) bytes(num protowire.Number, v []byte) wire {
	return protowire.AppendBytes(protowire.AppendTag(w, num, protowire.BytesType), v)
}

func (w wire) fixed32(num protowire.Number, v uint32) wire {
	return protowire.AppendFixed32(protowire.AppendTag(w, num, protowire.Fixed32Type), v)
}

func (w wire) tag(num protowire.Number, typ protowire.Type) wire {
	return protowire.AppendTag(w, num, typ)
}

func decode(t *testing.T, typeName string, b []byte) *Message {
	t.Helper()
	m := New(msgType(t, typeName))
	if err := (UnmarshalOptions{Resolver: pool(t)}).Unmarshal(b, m); err != nil {
		t.Fatalf("Unmarshal(%x): %v", b, err)
	}
	return m
}

func encode(t *testing.T, m *Message) []byte {
	t.Helper()
	b, err := MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != Size(m) {
		t.Fatalf("Size = %d, encoded %d bytes", Size(m), len(b))
	}
	return b
}

func expectDump(t *testing.T, m *Message, want string) {
	t.Helper()
	if got := dump(m); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestPackedAndUnpackedAccepted(t *testing.T) {
	var packedRun []byte
	for _, v := range []uint64{1, 2} {
		packedRun = protowire.AppendVarint(packedRun, v)
	}
	// u_int32 (30) is declared unpacked, r_int32 (3) packed; send the
	// opposite encodings, interleaved.
	b := wire(nil).bytes(30, packedRun).varint(3, 7).varint(30, 3).bytes(3, packedRun)
	m := decode(t, "codec.p3.Repeated", b)
	expectDump(t, m, "r_int32[0]: 7\nr_int32[1]: 1\nr_int32[2]: 2\nu_int32[0]: 1\nu_int32[1]: 2\nu_int32[2]: 3\n")
	// Re-encoded with the declared encodings.
	want := wire(nil).bytes(3, []byte{7, 1, 2}).varint(30, 1).varint(30, 2).varint(30, 3)
	if got := encode(t, m); !bytes.Equal(got, want) {
		t.Errorf("encoded %x, want %x", got, []byte(want))
	}
	// An empty packed run adds nothing.
	m = decode(t, "codec.p3.Repeated", wire(nil).bytes(3, nil))
	if m.Has(field(t, m.md, "r_int32")) || len(encode(t, m)) != 0 {
		t.Error("empty packed run should leave the field unset")
	}
}

func TestSingularMergeAndLastWins(t *testing.T) {
	first := wire(nil).varint(1, 1).bytes(2, []byte("one"))
	second := wire(nil).bytes(2, []byte("two")).bytes(3, wire(nil).varint(1, 9))
	b := wire(nil).varint(3, 5).varint(3, 6).
		bytes(17, first).bytes(17, second).
		fixed32(9, 1).fixed32(9, 2)
	m := decode(t, "codec.p3.Scalars", b)
	expectDump(t, m, "f_int32: 6\nf_fixed32: 2\nf_nested: {}\nf_nested.a: 1\nf_nested.b: \"two\"\nf_nested.child: {}\nf_nested.child.a: 9\n")

	// Groups merge too.
	g1 := wire(nil).tag(1, protowire.StartGroupType).varint(2, 1).tag(1, protowire.EndGroupType)
	g2 := wire(nil).tag(1, protowire.StartGroupType).bytes(3, []byte("b")).tag(1, protowire.EndGroupType)
	m = decode(t, "codec.p2.Groups", append(g1, g2...))
	expectDump(t, m, "g: {}\ng.a: 1\ng.b: \"b\"\n")
}

func TestOneofLastWins(t *testing.T) {
	b := wire(nil).varint(10, 5).bytes(11, []byte("s")).bytes(12, wire(nil).varint(1, 1))
	m := decode(t, "codec.p3.Oneofs", b)
	expectDump(t, m, "o_nested: {}\no_nested.a: 1\n")
	o := m.md.Oneofs[0]
	if got := m.WhichOneof(o); got == nil || got.Name != "o_nested" {
		t.Errorf("WhichOneof = %v", got)
	}
	// A message member merges only with itself.
	b = wire(nil).bytes(12, wire(nil).varint(1, 1)).varint(10, 2).bytes(12, wire(nil).bytes(2, []byte("x")))
	expectDump(t, decode(t, "codec.p3.Oneofs", b), "o_nested: {}\no_nested.b: \"x\"\n")
	// A zero value still selects the member.
	expectDump(t, decode(t, "codec.p3.Oneofs", wire(nil).bytes(11, []byte("s")).varint(10, 0)), "o_int32: 0\n")
}

func TestMapEntries(t *testing.T) {
	b := wire(nil).
		bytes(13, wire(nil).bytes(1, []byte("k")).bytes(2, []byte("v"))).
		bytes(13, wire(nil).bytes(2, []byte("no key"))).                      // key defaults to ""
		bytes(13, wire(nil).bytes(1, []byte("no value"))).                    // value defaults to ""
		bytes(13, wire(nil).bytes(2, []byte("v2")).bytes(1, []byte("k"))).    // value first; replaces "k"
		bytes(13, wire(nil).bytes(1, []byte("x")).varint(7, 1).varint(1, 5)). // unknown field and bad wire type skipped
		bytes(1, wire(nil).varint(1, 4)).                                     // message value missing
		bytes(1, wire(nil).varint(1, 4).bytes(2, wire(nil).varint(1, 1)).bytes(2, wire(nil).bytes(2, []byte("m"))))
	m := decode(t, "codec.p3.Maps", b)
	expectDump(t, m, `m_int32[4]: {}
m_int32[4].a: 1
m_int32[4].b: "m"
m_string_string[""]: "no key"
m_string_string["k"]: "v2"
m_string_string["no value"]: ""
m_string_string["x"]: ""
`)
	mp := m.Get(field(t, m.md, "m_string_string")).(*Map)
	if keys := mp.Keys(false); len(keys) != 4 || keys[0] != "k" || keys[1] != "" || keys[3] != "x" {
		t.Errorf("insertion order = %q", keys)
	}
	// Map entries are not merged; the second one for key 4 replaced the
	// first, and the value messages inside one entry merge.
	if len(m.Unknown()) != 0 {
		t.Errorf("unknown = %x", m.Unknown())
	}
	// Non-deterministic encoding keeps insertion order.
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, wire(nil).bytes(13, wire(nil).bytes(1, []byte("k")).bytes(2, []byte("v2")))) {
		t.Errorf("entry not re-encoded with key first: %x", out)
	}
	det := encode(t, m)
	if bytes.Equal(out, det) || len(out) != len(det) {
		t.Errorf("deterministic and insertion order encodings should differ only in order:\n%x\n%x", out, det)
	}
}

func TestWireTypeMismatchIsUnknown(t *testing.T) {
	// f_int32 (3) as fixed32, f_string (14) as varint, f_nested (17) as a
	// varint, f_double (1) correct.
	b := wire(nil).fixed32(3, 7).varint(14, 1).varint(17, 2)
	b = protowire.AppendFixed64(b.tag(1, protowire.Fixed64Type), math.Float64bits(2))
	m := decode(t, "codec.p3.Scalars", b)
	want := wire(nil).fixed32(3, 7).varint(14, 1).varint(17, 2)
	if got := m.Unknown(); !bytes.Equal(got, want) {
		t.Errorf("unknown = %x, want %x", got, []byte(want))
	}
	if !m.Has(field(t, m.md, "f_double")) || m.Has(field(t, m.md, "f_int32")) {
		t.Error("presence wrong")
	}
	// Unknown fields are written after the known ones.
	wantOut := append(protowire.AppendFixed64(wire(nil).tag(1, protowire.Fixed64Type), math.Float64bits(2)), want...)
	if got := encode(t, m); !bytes.Equal(got, wantOut) {
		t.Errorf("encoded %x, want %x", got, wantOut)
	}
	// A group field sent length-delimited, and a repeated message sent as
	// a group, are unknown too.
	m = decode(t, "codec.p2.Groups", wire(nil).bytes(1, nil))
	if m.Has(field(t, m.md, "g")) || len(m.Unknown()) != 2 {
		t.Errorf("group as bytes: %s", dump(m))
	}
	grp := wire(nil).tag(17, protowire.StartGroupType).tag(17, protowire.EndGroupType)
	m = decode(t, "codec.p3.Repeated", grp)
	if !bytes.Equal(m.Unknown(), grp) {
		t.Errorf("message as group: %s", dump(m))
	}
}

func TestClosedEnums(t *testing.T) {
	// kind (13) closed, 9 is unknown; packed_kind (10) with 1, 9, 3;
	// expanded_kind (11) with 8; kind_map (14) with an unknown value.
	entry := wire(nil).bytes(1, []byte("a")).varint(2, 9)
	b := wire(nil).varint(13, 9).varint(13, 2).
		bytes(10, []byte{1, 9, 3}).
		varint(11, 8).
		bytes(14, entry).
		bytes(14, wire(nil).bytes(1, []byte("b")).varint(2, 1))
	m := decode(t, "codec.p2.Groups", b)
	expectDump(t, m, "packed_kind[0]: 1\npacked_kind[1]: 3\nkind: 2\nkind_map[\"b\"]: 1\n"+
		"unknown: "+hexOf(wire(nil).varint(13, 9).varint(10, 9).varint(11, 8).bytes(14, entry))+"\n")

	// An unknown closed enum value never replaces a known one.
	m = decode(t, "codec.p2.Groups", wire(nil).varint(13, 2).varint(13, 9))
	if v := m.Get(field(t, m.md, "kind")); v != EnumNumber(2) {
		t.Errorf("kind = %v", v)
	}

	// Editions closed enum.
	m = decode(t, "codec.ed.Features", wire(nil).varint(9, 5))
	if m.Has(field(t, m.md, "closed")) || len(m.Unknown()) != 2 {
		t.Errorf("editions closed enum: %s", dump(m))
	}

	// Open enums keep any number, including negative ones.
	m = decode(t, "codec.p3.Scalars", wire(nil).varint(16, math.MaxUint64))
	if v := m.Get(field(t, m.md, "f_enum")); v != EnumNumber(-1) {
		t.Errorf("open enum = %v", v)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var sb strings.Builder
	for _, c := range b {
		sb.WriteByte(digits[c>>4])
		sb.WriteByte(digits[c&15])
	}
	return sb.String()
}

func TestUnknownFieldsKeptInOrder(t *testing.T) {
	// Unknown fields between known ones keep their relative order and move
	// after the known fields.
	b := wire(nil).varint(50, 1).varint(1, 3).bytes(51, []byte("x")).
		tag(52, protowire.StartGroupType).varint(1, 1).tag(52, protowire.EndGroupType).
		bytes(2, []byte("b")).fixed32(53, 9)
	m := decode(t, "codec.p3.Nested", b)
	wantUnknown := wire(nil).varint(50, 1).bytes(51, []byte("x")).
		tag(52, protowire.StartGroupType).varint(1, 1).tag(52, protowire.EndGroupType).fixed32(53, 9)
	if !bytes.Equal(m.Unknown(), wantUnknown) {
		t.Errorf("unknown = %x\nwant      %x", m.Unknown(), []byte(wantUnknown))
	}
	wantOut := append(wire(nil).varint(1, 3).bytes(2, []byte("b")), wantUnknown...)
	if got := encode(t, m); !bytes.Equal(got, wantOut) {
		t.Errorf("encoded %x, want %x", got, wantOut)
	}

	// DiscardUnknown drops them.
	d := New(m.md)
	if err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, d); err != nil || len(d.Unknown()) != 0 {
		t.Errorf("DiscardUnknown: %v %x", err, d.Unknown())
	}
	// Merge appends.
	if err := (UnmarshalOptions{Merge: true}).Unmarshal(wire(nil).varint(60, 1), m); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(m.Unknown(), wire(nil).varint(60, 1)) || m.Get(field(t, m.md, "a")) != int32(3) {
		t.Errorf("merge: %s", dump(m))
	}
	// SetUnknown replaces.
	m.SetUnknown(nil)
	if len(m.Unknown()) != 0 {
		t.Error("SetUnknown(nil)")
	}
}

func TestExtensions(t *testing.T) {
	md := msgType(t, "codec.p2.Extendable")
	b := wire(nil).varint(1, 5).bytes(2, []byte("s")).varint(99, 1).varint(100, 2)
	// With a resolver: known extensions; 99 is in the range but not defined.
	m := decode(t, md.FullName, b)
	expectDump(t, m, "plain: 2\n[codec.p2.ext_int32]: 5\n[codec.p2.ext_strings][0]: \"s\"\nunknown: "+hexOf(wire(nil).varint(99, 1))+"\n")
	ext := pool(t).FindExtension("codec.p2.ext_int32")
	if m.FieldByName("codec.p2.ext_int32") != ext || m.FieldByNumber(1) != ext || m.FieldByNumber(99) != nil {
		t.Error("FieldByName/FieldByNumber for extensions")
	}
	// Without: unknown.
	plain := New(md)
	if err := Unmarshal(b, plain); err != nil {
		t.Fatal(err)
	}
	if plain.Has(ext) || !bytes.Equal(plain.Unknown(), wire(nil).varint(1, 5).bytes(2, []byte("s")).varint(99, 1)) {
		t.Errorf("without resolver: %s", dump(plain))
	}
	// Extensions of other messages and non-extensions cannot be set.
	if err := m.Set(field(t, msgType(t, "codec.p3.Nested"), "a"), int32(1)); err == nil {
		t.Error("Set accepted a field of another message")
	}
}

func TestDefaultsAndPresence(t *testing.T) {
	d := New(msgType(t, "codec.p2.Defaults"))
	get := func(name string) any { return d.Get(field(t, d.md, name)) }
	if get("d_int32") != int32(-42) || get("d_string") != "héllo" || get("d_double") != math.Inf(1) ||
		get("d_enum") != EnumNumber(2) || !bytes.Equal(get("d_bytes").([]byte), []byte{1, 2}) || get("d_bool") != true ||
		!math.IsNaN(float64(get("d_float").(float32))) || get("d_uint64") != uint64(math.MaxUint64) ||
		get("no_default_enum") != EnumNumber(1) || get("req") != int32(0) {
		t.Errorf("defaults: %v %v %v %v %v", get("d_int32"), get("d_string"), get("d_double"), get("d_enum"), get("no_default_enum"))
	}
	// Explicit presence: zero is set and encoded.
	fd := field(t, d.md, "d_int32")
	if err := d.Set(fd, int32(0)); err != nil || !d.Has(fd) {
		t.Errorf("explicit presence zero: %v", err)
	}
	if got := encode(t, d); !bytes.Equal(got, wire(nil).varint(1, 0)) {
		t.Errorf("encoded %x", got)
	}

	// Implicit presence: zero clears.
	s := New(msgType(t, "codec.p3.Scalars"))
	fd = field(t, s.md, "f_string")
	_ = s.Set(fd, "x")
	if err := s.Set(fd, ""); err != nil || s.Has(fd) {
		t.Errorf("implicit presence zero: %v", err)
	}
	// ... but -0.0 is not zero.
	fd = field(t, s.md, "f_double")
	_ = s.Set(fd, math.Copysign(0, -1))
	if !s.Has(fd) {
		t.Error("-0.0 should be present")
	}
	// Decoding a zero implicit field leaves it unset.
	s = decode(t, "codec.p3.Scalars", wire(nil).varint(3, 0).bytes(14, nil))
	if len(s.fields) != 0 || len(encode(t, s)) != 0 {
		t.Errorf("zero implicit fields: %s", dump(s))
	}

	// Editions: explicit by default, IMPLICIT by feature.
	f := decode(t, "codec.ed.Features", wire(nil).varint(3, 0).varint(4, 0))
	if !f.Has(field(t, f.md, "explicit_int32")) || f.Has(field(t, f.md, "implicit_int32")) {
		t.Errorf("editions presence: %s", dump(f))
	}
}

func TestUTF8Validation(t *testing.T) {
	bad := []byte{0xff}
	cases := []struct {
		typeName string
		b        []byte
		ok       bool
	}{
		{"codec.p3.Scalars", wire(nil).bytes(14, bad), false},
		{"codec.p3.Repeated", wire(nil).bytes(14, bad), false},
		{"codec.p3.Maps", wire(nil).bytes(13, wire(nil).bytes(1, bad)), false},
		{"codec.p3.Maps", wire(nil).bytes(13, wire(nil).bytes(2, bad)), false},
		{"codec.p3.Scalars", wire(nil).bytes(15, bad), true}, // bytes
		{"codec.p2.Groups", wire(nil).bytes(12, bad), true},  // proto2
		{"codec.ed.Features", wire(nil).bytes(7, bad), false},
		{"codec.ed.Features", wire(nil).bytes(8, bad), true},
		{"codec.ed.Unverified", wire(nil).bytes(1, wire(nil).bytes(1, bad).bytes(2, bad)), true},
		{"codec.ed.Unverified", wire(nil).bytes(2, wire(nil).bytes(1, bad)), false},
		{"codec.ednone.Text", wire(nil).bytes(1, bad).bytes(2, bad), true},
	}
	for _, c := range cases {
		m := New(msgType(t, c.typeName))
		err := Unmarshal(c.b, m)
		if (err == nil) != c.ok || err != nil && !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("%s %x: %v", c.typeName, c.b, err)
		}
	}

	// Encoding checks too.
	s := New(msgType(t, "codec.p3.Scalars"))
	_ = s.Set(field(t, s.md, "f_nested"), build(t, "codec.p3.Nested", "b", "\xff"))
	_, err := Marshal(s)
	var e *Error
	if !errors.As(err, &e) || e.Path != "codec.p3.Scalars.f_nested.b" || e.Offset != -1 || !errors.Is(err, ErrInvalidUTF8) {
		t.Errorf("encode error = %v", err)
	}
	if err.Error() != "protobuf: encoding codec.p3.Scalars.f_nested.b: string field contains invalid UTF-8" {
		t.Errorf("message = %q", err.Error())
	}
}

func TestDecodeErrors(t *testing.T) {
	deep := func(n int) []byte {
		b := wire(nil).varint(1, 1)
		for range n {
			b = wire(nil).bytes(3, b)
		}
		return b
	}
	cases := []struct {
		name     string
		typeName string
		b        []byte
		err      error
		offset   int
		path     string
	}{
		{"truncated varint", "codec.p3.Nested", []byte{0x08, 0x80}, protowire.ErrTruncated, 1, "codec.p3.Nested.a"},
		{"truncated tag", "codec.p3.Nested", []byte{0x08, 0x01, 0x92}, protowire.ErrTruncated, 2, "codec.p3.Nested"},
		{"truncated bytes", "codec.p3.Nested", []byte{0x12, 0x05, 'a'}, protowire.ErrTruncated, 1, "codec.p3.Nested.b"},
		{"field number zero", "codec.p3.Nested", []byte{0x08, 0x01, 0x00}, protowire.ErrFieldNumber, 2, "codec.p3.Nested"},
		{"wire type 7", "codec.p3.Nested", []byte{0x0f}, protowire.ErrWireType, 0, "codec.p3.Nested"},
		{"lone end group", "codec.p3.Nested", []byte{0x0c}, protowire.ErrEndGroup, 0, "codec.p3.Nested"},
		{"nested truncated", "codec.p3.Nested", wire(nil).varint(1, 1).bytes(3, wire(nil).bytes(3, []byte{0x10})), protowire.ErrTruncated, 6, "codec.p3.Nested.child.child"},
		{"unknown truncated", "codec.p3.Nested", []byte{0x08, 0x01, 0xa2, 0x06, 0x09}, protowire.ErrTruncated, 2, "codec.p3.Nested"},
		{"list index", "codec.p3.Repeated", wire(nil).bytes(17, nil).bytes(17, []byte{0x08}), protowire.ErrTruncated, 7, "codec.p3.Repeated.r_nested[1].a"},
		{"packed element", "codec.p3.Repeated", wire(nil).bytes(3, []byte{1, 0x80}), protowire.ErrTruncated, 3, "codec.p3.Repeated.r_int32[1]"},
		{"packed fixed", "codec.p3.Repeated", wire(nil).bytes(9, []byte{1, 2, 3, 4, 5}), protowire.ErrTruncated, 6, "codec.p3.Repeated.r_fixed32[1]"},
		{"map value", "codec.p3.Maps", wire(nil).bytes(13, wire(nil).bytes(1, []byte("k")).bytes(2, []byte{0xc3})), ErrInvalidUTF8, 6, `codec.p3.Maps.m_string_string["k"]`},
		{"group mismatch", "codec.p2.Groups", wire(nil).tag(1, protowire.StartGroupType).tag(2, protowire.EndGroupType), protowire.ErrEndGroup, 1, "codec.p2.Groups.g"},
		{"group unterminated", "codec.p2.Groups", wire(nil).tag(1, protowire.StartGroupType).varint(2, 1), protowire.ErrTruncated, 1, "codec.p2.Groups.g"},
		{"recursion limit", "codec.p3.Nested", deep(100), ErrRecursionLimit, len(deep(100)) - 2, "codec.p3.Nested" + strings.Repeat(".child", 100)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(msgType(t, c.typeName))
			err := Unmarshal(c.b, m)
			var e *Error
			if !errors.As(err, &e) || !errors.Is(err, c.err) || e.Offset != c.offset || e.Path != c.path {
				t.Fatalf("error = %v (%#v), want %v at %d in %s", err, e, c.err, c.offset, c.path)
			}
		})
	}
	// 99 levels below the top-level message are fine.
	if err := Unmarshal(deep(99), New(msgType(t, "codec.p3.Nested"))); err != nil {
		t.Errorf("depth 100: %v", err)
	}
	if err := (UnmarshalOptions{RecursionLimit: 3}).Unmarshal(deep(3), New(msgType(t, "codec.p3.Nested"))); !errors.Is(err, ErrRecursionLimit) {
		t.Errorf("RecursionLimit 3: %v", err)
	}
	// Size limit.
	big := wire(nil).bytes(2, bytes.Repeat([]byte("a"), 100))
	err := (UnmarshalOptions{MaxSize: 50}).Unmarshal(big, New(msgType(t, "codec.p3.Nested")))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("MaxSize: %v", err)
	}
	if err := (UnmarshalOptions{MaxSize: -1}).Unmarshal(big, New(msgType(t, "codec.p3.Nested"))); err != nil {
		t.Errorf("MaxSize -1: %v", err)
	}
	e := &Error{Offset: 3, Path: "a.b", Err: protowire.ErrTruncated}
	if e.Error() != "protobuf: decoding a.b at offset 3: unexpected end of input" {
		t.Errorf("message = %q", e.Error())
	}
}

func TestMessageAPI(t *testing.T) {
	md := msgType(t, "codec.p3.Oneofs")
	m := New(md)
	oInt, oStr, plain := field(t, md, "o_int32"), field(t, md, "o_string"), field(t, md, "plain")

	if err := m.Set(oInt, "not an int"); err == nil {
		t.Error("Set accepted a wrong type")
	}
	if err := m.Set(oInt, int64(1)); err == nil {
		t.Error("Set accepted int64 for int32")
	}
	_ = m.Set(plain, "p")
	_ = m.Set(oInt, int32(1))
	_ = m.Set(oStr, "s")
	if m.Has(oInt) || !m.Has(oStr) || m.WhichOneof(md.Oneofs[0]) != oStr {
		t.Error("setting a oneof member should clear the others")
	}
	var names []string
	m.Range(func(fd *desc.Field, v any) bool {
		names = append(names, fd.Name)
		return true
	})
	if strings.Join(names, ",") != "plain,o_string" {
		t.Errorf("Range = %v", names)
	}
	m.Clear(oStr)
	if m.WhichOneof(md.Oneofs[0]) != nil {
		t.Error("Clear")
	}

	// Lists and maps.
	rmd := msgType(t, "codec.p3.Repeated")
	r := New(rmd)
	rfd := field(t, rmd, "r_int32")
	if r.Get(rfd).(*List) != nil || r.Get(rfd).(*List).Len() != 0 {
		t.Error("unset list should be a nil *List")
	}
	l := r.Mutable(rfd).(*List)
	if r.Has(rfd) {
		t.Error("empty list should not be set")
	}
	if err := l.Append(int32(1)); err != nil || !r.Has(rfd) {
		t.Errorf("Append: %v", err)
	}
	if err := l.Append(uint32(1)); err == nil {
		t.Error("Append accepted a wrong type")
	}
	_ = l.Append(int32(2))
	_ = l.Set(0, int32(5))
	l.Truncate(1)
	if l.Len() != 1 || l.Get(0) != int32(5) {
		t.Error("List Set/Truncate")
	}
	if err := r.Set(rfd, &List{fd: field(t, rmd, "r_int64")}); err == nil {
		t.Error("Set accepted a list of another field")
	}
	if err := r.Set(field(t, rmd, "r_nested"), (*List)(nil)); err == nil {
		t.Error("Set accepted a nil list")
	}

	mmd := msgType(t, "codec.p3.Maps")
	mm := New(mmd)
	mp := mm.Mutable(field(t, mmd, "m_string_string")).(*Map)
	if err := mp.Set(int32(1), "v"); err == nil {
		t.Error("Map.Set accepted a wrong key type")
	}
	_ = mp.Set("b", "1")
	_ = mp.Set("a", "2")
	_ = mp.Set("b", "3")
	if k := mp.Keys(false); len(k) != 2 || k[0] != "b" {
		t.Errorf("Keys(false) = %v", k)
	}
	if k := mp.Keys(true); k[0] != "a" {
		t.Errorf("Keys(true) = %v", k)
	}
	mp.Delete("b")
	if v, ok := mp.Get("b"); ok || v != nil || mp.Len() != 1 {
		t.Error("Delete")
	}
	bm := mm.Mutable(field(t, mmd, "m_bool")).(*Map)
	_ = bm.Set(true, New(msgType(t, "codec.p3.Nested")))
	_ = bm.Set(false, New(msgType(t, "codec.p3.Nested")))
	if k := bm.Keys(true); k[0] != false {
		t.Errorf("bool keys = %v", k)
	}
	if err := bm.Set(true, New(msgType(t, "codec.p3.Scalars"))); err == nil {
		t.Error("Map.Set accepted a message of another type")
	}

	// Clone is deep.
	c := mm.Clone()
	_ = mp.Set("z", "new")
	if Equal(c, mm) {
		t.Error("clone shares its map")
	}
	// Equal ignores empty composites.
	a, b := New(rmd), New(rmd)
	a.Mutable(rfd)
	if !Equal(a, b) {
		t.Error("empty list should equal unset")
	}

	// Get and Mutable panic for foreign fields.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Get did not panic")
			}
		}()
		New(md).Get(rfd)
	}()
}

func TestProto3OptionalAndSyntheticOneof(t *testing.T) {
	md := msgType(t, "codec.p3.Optionals")
	m := New(md)
	a, b := field(t, md, "o_int32"), field(t, md, "o_string")
	_ = m.Set(a, int32(0))
	_ = m.Set(b, "")
	// Synthetic oneofs do not clear each other.
	if !m.Has(a) || !m.Has(b) {
		t.Error("proto3 optional presence")
	}
	if got := encode(t, m); !bytes.Equal(got, wire(nil).varint(1, 0).bytes(2, nil)) {
		t.Errorf("encoded %x", got)
	}
}
