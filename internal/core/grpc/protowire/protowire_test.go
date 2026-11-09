package protowire

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestVarint(t *testing.T) {
	cases := []struct {
		v   uint64
		enc []byte
	}{
		{0, []byte{0}},
		{1, []byte{1}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}},
		{math.MaxUint32, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}},
		{uint64(math.MaxInt64), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		// Also int32(-1), which is sign-extended to 64 bits: ten bytes.
		{math.MaxUint64, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, c := range cases {
		if got := AppendVarint(nil, c.v); !bytes.Equal(got, c.enc) {
			t.Errorf("AppendVarint(%d) = %x, want %x", c.v, got, c.enc)
		}
		if got := SizeVarint(c.v); got != len(c.enc) {
			t.Errorf("SizeVarint(%d) = %d, want %d", c.v, got, len(c.enc))
		}
		v, n, err := ConsumeVarint(append(c.enc, 0xaa))
		if err != nil || v != c.v || n != len(c.enc) {
			t.Errorf("ConsumeVarint(%x) = %d, %d, %v", c.enc, v, n, err)
		}
	}
	for i := range 64 {
		v := uint64(1) << i
		if SizeVarint(v) != len(AppendVarint(nil, v)) || SizeVarint(v-1) != len(AppendVarint(nil, v-1)) {
			t.Errorf("SizeVarint wrong around 1<<%d", i)
		}
	}
}

func TestConsumeVarintErrors(t *testing.T) {
	cases := []struct {
		in  []byte
		err error
	}{
		{nil, ErrTruncated},
		{[]byte{0x80}, ErrTruncated},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, ErrTruncated},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}, ErrOverflow},
		{[]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}, ErrOverflow},
	}
	for _, c := range cases {
		if _, _, err := ConsumeVarint(c.in); err != c.err {
			t.Errorf("ConsumeVarint(%x) error = %v, want %v", c.in, err, c.err)
		}
	}
	// Non-minimal encodings are accepted, as in protobuf-go.
	if v, n, err := ConsumeVarint([]byte{0x81, 0x80, 0x00}); err != nil || v != 1 || n != 3 {
		t.Errorf("non-minimal varint: %d, %d, %v", v, n, err)
	}
}

func TestTag(t *testing.T) {
	b := AppendTag(nil, MaxValidNumber, Fixed32Type)
	if len(b) != SizeTag(MaxValidNumber) {
		t.Errorf("SizeTag = %d, len = %d", SizeTag(MaxValidNumber), len(b))
	}
	num, typ, n, err := ConsumeTag(b)
	if err != nil || num != MaxValidNumber || typ != Fixed32Type || n != len(b) {
		t.Errorf("ConsumeTag = %d, %v, %d, %v", num, typ, n, err)
	}
	bad := []struct {
		in  []byte
		err error
	}{
		{AppendVarint(nil, 0), ErrFieldNumber},                             // number 0
		{AppendVarint(nil, uint64(MaxValidNumber+1)<<3), ErrFieldNumber},   // too large
		{AppendVarint(nil, 1<<3|6), ErrWireType},                           // wire type 6
		{AppendVarint(nil, 1<<3|7), ErrWireType},                           // wire type 7
		{AppendVarint(nil, uint64(math.MaxUint32+1)<<3|0), ErrFieldNumber}, // beyond 32 bits
		{[]byte{0x88}, ErrTruncated},
	}
	for _, c := range bad {
		if _, _, _, err := ConsumeTag(c.in); err != c.err {
			t.Errorf("ConsumeTag(%x) error = %v, want %v", c.in, err, c.err)
		}
	}
}

func TestFixedAndBytes(t *testing.T) {
	b := AppendFixed32(nil, 0x01020304)
	b = AppendFixed64(b, 0x0102030405060708)
	b = AppendString(b, "hé")
	b = AppendBytes(b, []byte{})
	want := []byte{4, 3, 2, 1, 8, 7, 6, 5, 4, 3, 2, 1, 3, 'h', 0xc3, 0xa9, 0}
	if !bytes.Equal(b, want) {
		t.Fatalf("got %x, want %x", b, want)
	}
	v32, n, err := ConsumeFixed32(b)
	if err != nil || v32 != 0x01020304 || n != 4 {
		t.Errorf("ConsumeFixed32 = %x %d %v", v32, n, err)
	}
	v64, n, err := ConsumeFixed64(b[4:])
	if err != nil || v64 != 0x0102030405060708 || n != 8 {
		t.Errorf("ConsumeFixed64 = %x %d %v", v64, n, err)
	}
	s, n, err := ConsumeBytes(b[12:])
	if err != nil || string(s) != "hé" || n != 4 {
		t.Errorf("ConsumeBytes = %q %d %v", s, n, err)
	}
	if SizeBytes(3) != 4 {
		t.Errorf("SizeBytes(3) = %d", SizeBytes(3))
	}
	for _, in := range [][]byte{{1, 2, 3}} {
		if _, _, err := ConsumeFixed32(in); err != ErrTruncated {
			t.Errorf("ConsumeFixed32 short: %v", err)
		}
		if _, _, err := ConsumeFixed64(in); err != ErrTruncated {
			t.Errorf("ConsumeFixed64 short: %v", err)
		}
	}
	if _, _, err := ConsumeBytes([]byte{5, 'a'}); err != ErrTruncated {
		t.Errorf("ConsumeBytes short: %v", err)
	}
	// A length that overflows int on the remaining input.
	if _, _, err := ConsumeBytes(AppendVarint(nil, math.MaxUint64)); err != ErrTruncated {
		t.Errorf("ConsumeBytes huge length: %v", err)
	}
}

func TestGroups(t *testing.T) {
	// group 1 { field 2 varint 5; group 3 { field 4 bytes "x" } }
	var body []byte
	body = AppendTag(body, 2, VarintType)
	body = AppendVarint(body, 5)
	body = AppendTag(body, 3, StartGroupType)
	body = AppendTag(body, 4, BytesType)
	body = AppendString(body, "x")
	body = AppendTag(body, 3, EndGroupType)
	in := append(append([]byte{}, body...), AppendTag(nil, 1, EndGroupType)...)
	in = append(in, 0xee) // trailing data after the group

	got, n, err := ConsumeGroup(1, in)
	if err != nil || !bytes.Equal(got, body) || n != len(in)-1 {
		t.Fatalf("ConsumeGroup = %x, %d, %v", got, n, err)
	}
	// A non-minimal end tag is excluded from the body too.
	nonMinimal := append(append([]byte{}, body...), 0x8c, 0x00)
	got, n, err = ConsumeGroup(1, nonMinimal)
	if err != nil || !bytes.Equal(got, body) || n != len(nonMinimal) {
		t.Fatalf("ConsumeGroup non-minimal end = %x, %d, %v", got, n, err)
	}

	full := append(AppendTag(nil, 1, StartGroupType), in...)
	num, typ, n, err := ConsumeField(full)
	if err != nil || num != 1 || typ != StartGroupType || n != len(full)-1 {
		t.Errorf("ConsumeField = %d %v %d %v", num, typ, n, err)
	}

	// Mismatched end group.
	bad := append(append([]byte{}, body...), AppendTag(nil, 9, EndGroupType)...)
	if _, _, err := ConsumeGroup(1, bad); err != ErrEndGroup {
		t.Errorf("mismatched end: %v", err)
	}
	// Missing end group.
	if _, _, err := ConsumeGroup(1, body); err != ErrTruncated {
		t.Errorf("missing end: %v", err)
	}
	// An end group on its own.
	if _, err := ConsumeFieldValue(1, EndGroupType, nil); err != ErrEndGroup {
		t.Errorf("lone end group: %v", err)
	}
	// Deep nesting is limited, without recursion.
	var deep []byte
	for range MaxGroupDepth + 1 {
		deep = AppendTag(deep, 1, StartGroupType)
	}
	if _, err := ConsumeFieldValue(1, StartGroupType, deep); !errors.Is(err, ErrGroupDepth) {
		t.Errorf("deep groups: %v", err)
	}
}

func TestZigZag(t *testing.T) {
	cases := []struct {
		v int64
		z uint64
	}{
		{0, 0}, {-1, 1}, {1, 2}, {-2, 3}, {math.MaxInt32, 0xfffffffe}, {math.MinInt32, 0xffffffff},
		{math.MaxInt64, math.MaxUint64 - 1}, {math.MinInt64, math.MaxUint64},
	}
	for _, c := range cases {
		if got := EncodeZigZag(c.v); got != c.z {
			t.Errorf("EncodeZigZag(%d) = %d, want %d", c.v, got, c.z)
		}
		if got := DecodeZigZag(c.z); got != c.v {
			t.Errorf("DecodeZigZag(%d) = %d, want %d", c.z, got, c.v)
		}
	}
	if EncodeBool(true) != 1 || EncodeBool(false) != 0 || !DecodeBool(2) || DecodeBool(0) {
		t.Error("bool")
	}
}

// FuzzConsumeField checks that ConsumeField never panics, stays inside its
// input, and agrees with ConsumeTag, ConsumeFieldValue and ConsumeGroup.
//
//	go test ./internal/core/grpc/protowire -run '^$' -fuzz FuzzConsumeField -fuzztime 60s
func FuzzConsumeField(f *testing.F) {
	f.Add([]byte{0x08, 0x96, 0x01})
	f.Add([]byte{0x0b, 0x10, 0x01, 0x0c})
	f.Add([]byte{0x12, 0x02, 'h', 'i'})
	f.Add([]byte{0x0d, 1, 2, 3, 4})
	f.Add([]byte{0x09, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Fuzz(func(t *testing.T, b []byte) {
		num, typ, n, err := ConsumeField(b)
		if err != nil {
			if n != 0 {
				t.Fatalf("n = %d with error %v", n, err)
			}
			return
		}
		if n <= 0 || n > len(b) || num < MinValidNumber || num > MaxValidNumber || typ == EndGroupType {
			t.Fatalf("ConsumeField = %d %v %d", num, typ, n)
		}
		_, _, tn, _ := ConsumeTag(b)
		m, err := ConsumeFieldValue(num, typ, b[tn:])
		if err != nil || tn+m != n {
			t.Fatalf("ConsumeFieldValue = %d, %v; want %d", m, err, n-tn)
		}
		if typ == StartGroupType {
			body, gn, err := ConsumeGroup(num, b[tn:])
			if err != nil || gn != m || len(body) > gn {
				t.Fatalf("ConsumeGroup = %d, %d, %v", len(body), gn, err)
			}
		}
	})
}
