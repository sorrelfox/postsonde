package wsframe

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStatusCodeValidOnWire(t *testing.T) {
	t.Parallel()
	valid := map[StatusCode]bool{}
	for _, c := range []StatusCode{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014, 3000, 3999, 4000, 4999} {
		valid[c] = true
	}
	for _, c := range []StatusCode{0, 999, 1004, 1005, 1006, 1015, 1016, 1100, 2000, 2999, 5000, 65535} {
		if valid[c] {
			t.Fatalf("test table lists %d twice", c)
		}
	}
	for c := range StatusCode(6000) {
		want := valid[c] || (c >= 3000 && c <= 4999)
		if c.ValidOnWire() != want {
			t.Errorf("%d: ValidOnWire = %v", c, !want)
		}
	}
	if CloseNormalClosure.String() != "1000 (normal closure)" || StatusCode(4001).String() != "4001" {
		t.Error("String")
	}
}

func TestClosePayload(t *testing.T) {
	t.Parallel()
	p, err := AppendClosePayload(nil, CloseNormalClosure, "bye")
	if err != nil || !bytes.Equal(p, []byte{0x03, 0xe8, 'b', 'y', 'e'}) {
		t.Fatalf("% x %v", p, err)
	}
	if p, err := AppendClosePayload(nil, CloseNoStatus, ""); err != nil || len(p) != 0 {
		t.Fatalf("no status: % x %v", p, err)
	}
	for _, tc := range []struct {
		code   StatusCode
		reason string
	}{
		{CloseNoStatus, "x"},
		{CloseAbnormalClosure, ""},
		{1004, ""},
		{CloseTLSHandshake, ""},
		{999, ""},
		{2000, ""},
		{5000, ""},
		{1000, strings.Repeat("a", 124)},
		{1000, "\xff"},
	} {
		if _, err := AppendClosePayload(nil, tc.code, tc.reason); errCode(err) != CodeInvalidArgument {
			t.Errorf("%d %q: %v", tc.code, tc.reason, err)
		}
	}
	if _, err := AppendClosePayload(nil, 1000, strings.Repeat("a", 123)); err != nil {
		t.Errorf("123-byte reason: %v", err)
	}

	for _, tc := range []struct {
		name   string
		p      []byte
		code   StatusCode
		reason string
		err    string
		status StatusCode
	}{
		{"empty", nil, CloseNoStatus, "", "", 0},
		{"code only", []byte{0x03, 0xe8}, 1000, "", "", 0},
		{"reason", []byte{0x0f, 0xa0, 'o', 'k'}, 4000, "ok", "", 0},
		{"one byte", []byte{0x03}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"1005 on wire", []byte{0x03, 0xed}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"1006 on wire", []byte{0x03, 0xee}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"1004", []byte{0x03, 0xec}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"1015", []byte{0x03, 0xf7}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"999", []byte{0x03, 0xe7}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"2999", []byte{0x0b, 0xb7}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"5000", []byte{0x13, 0x88}, 0, "", CodeInvalidClosePayload, CloseProtocolError},
		{"bad utf-8 reason", []byte{0x03, 0xe8, 0xce, 0xba, 0xe1, 0xbd, 0xb9, 0xcf, 0x83, 0xce, 0xbc, 0xce, 0xb5, 0xed, 0xa0, 0x80, 0x65, 0x64, 0x69, 0x74, 0x65, 0x64}, 0, "", CodeInvalidUTF8, CloseInvalidPayload},
	} {
		code, reason, err := ParseClosePayload(tc.p)
		if tc.err != "" {
			if errCode(err) != tc.err || CloseCodeOf(err) != tc.status {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err != nil || code != tc.code || reason != tc.reason {
			t.Errorf("%s: %d %q %v", tc.name, code, reason, err)
		}
	}
}

func validateChunks(chunks ...[]byte) bool {
	var v utf8Validator
	for _, c := range chunks {
		if !v.write(c) {
			return false
		}
	}
	return v.complete()
}

func TestUTF8ValidatorSplits(t *testing.T) {
	t.Parallel()
	samples := []string{
		"", "Hello", "κόσμε", "é€\U0001F600", "a\U0010FFFFb", "�",
		"\xce\xba\xe1\xbd\xb9\xcf\x83\xce\xbc\xce\xb5\xed\xa0\x80\x65\x64\x69\x74\x65\x64", // Autobahn 6.x surrogate
		"\xf4\x90\x80\x80", "\xc0\xaf", "\xe0\x80\xaf", "\xf8\x88\x80\x80\x80", "\x80", "abc\xff", "\xe2\x82", "\xf0\x9f\x98",
	}
	for _, s := range samples {
		b := []byte(s)
		want := utf8.Valid(b)
		for i := 0; i <= len(b); i++ {
			for j := i; j <= len(b); j++ {
				if got := validateChunks(b[:i], b[i:j], b[j:]); got != want {
					t.Errorf("%q split %d/%d: got %v", s, i, j, got)
				}
			}
		}
	}
	// Byte-at-a-time random strings.
	rng := rand.New(rand.NewPCG(3, 4))
	for range 3000 {
		b := make([]byte, rng.IntN(12))
		for i := range b {
			b[i] = []byte{'a', 0x80, 0xbf, 0xc2, 0xdf, 0xe0, 0xed, 0xef, 0xf0, 0xf4, 0xa0, 0x9f, 0x90, 0x8f, 0xff}[rng.IntN(15)]
		}
		chunks := make([][]byte, len(b))
		for i := range b {
			chunks[i] = b[i : i+1]
		}
		if validateChunks(chunks...) != utf8.Valid(b) {
			t.Fatalf("% x: byte-wise result differs from utf8.Valid", b)
		}
	}
}

func TestUTF8ValidatorFailsFast(t *testing.T) {
	t.Parallel()
	// Each chunk is invalid already, even though more data would follow.
	for _, chunk := range []string{"\xce\xba\xe1\xbd\xb9\xcf\x83\xce\xbc\xce\xb5\xed\xa0", "\xf4\x90", "\xc0", "\xe0\x80", "\xff", "a\x80", "\xf5"} {
		var v utf8Validator
		if v.write([]byte(chunk)) {
			t.Errorf("% x accepted as a prefix", chunk)
		}
	}
	// A valid prefix stays pending.
	var v utf8Validator
	if !v.write([]byte("\xf0\x9f")) || v.complete() || !v.write([]byte("\x98")) || !v.write([]byte("\x80ok")) || !v.complete() {
		t.Error("split emoji")
	}
}
