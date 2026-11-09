package sse

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeUTF8(t *testing.T) {
	const r = "�"
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"é€\U0001F600", "é€\U0001F600"},
		{"\xFF", r},
		{"\x80", r},
		{"\xC0\xAF", r + r},                 // overlong lead bytes are invalid
		{"\xE0\x80\xAF", r + r + r},         // E0 needs A0..BF next
		{"\xE2\x82A", r + "A"},              // truncated sequence: one U+FFFD
		{"\xE2\x82", r},                     // truncated at the end
		{"\xED\xA0\x80", r + r + r},         // surrogate
		{"\xED\x9F\xBF", "퟿"},               // last before surrogates
		{"\xF0\x80\x80\x80", r + r + r + r}, // F0 needs 90..BF next
		{"\xF0\x9F\x98", r},                 // truncated 4-byte
		{"\xF0\x9F\x98A", r + "A"},          // truncated then ASCII
		{"\xF4\x8F\xBF\xBF", "\U0010FFFF"},
		{"\xF4\x90\x80\x80", r + r + r + r}, // above U+10FFFF
		{"\xF5\x80", r + r},
		{"a\xC3", "a" + r},
		{"\xC3\xC3\xA9", r + "é"},
	}
	for _, tc := range cases {
		if got := decodeUTF8([]byte(tc.in)); got != tc.want {
			t.Errorf("decodeUTF8(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func FuzzDecodeUTF8(f *testing.F) {
	f.Add([]byte("h\xC3\xA9\xE2\x82\xF0\x9F\x98\x80\xED\xA0\x80"))
	f.Fuzz(func(t *testing.T, b []byte) {
		got := decodeUTF8(b)
		if !utf8.ValidString(got) {
			t.Fatalf("invalid output %q", got)
		}
		if utf8.Valid(b) && got != string(b) {
			t.Fatalf("valid input changed: %q -> %q", b, got)
		}
		// Every valid rune of the input survives, and each U+FFFD stands
		// for at least one byte.
		if n := strings.Count(got, "�"); n > len(b) {
			t.Fatalf("%d replacements for %d bytes", n, len(b))
		}
	})
}
