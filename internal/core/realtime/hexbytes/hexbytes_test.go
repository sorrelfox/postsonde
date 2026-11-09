package hexbytes

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []byte
	}{
		{"", []byte{}},
		{"  \n\t,;: ", []byte{}},
		{"DE AD be ef", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"deadbeef", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"0xDE,0xAD", []byte{0xde, 0xad}},
		{"0XDE, 0Xad ,0x00", []byte{0xde, 0xad, 0x00}},
		{"0xDEAD", []byte{0xde, 0xad}},
		{`\xde\xad`, []byte{0xde, 0xad}},
		{`\x0a \x0d`, []byte{0x0a, 0x0d}},
		{"de:ad:be:ef", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"00 0a\r\nff", []byte{0x00, 0x0a, 0xff}},
		{"00", []byte{0x00}},
		{"0x00", []byte{0x00}},
		{"de ad\n", []byte{0xde, 0xad}},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if err != nil || !bytes.Equal(got, tt.want) {
			t.Errorf("Parse(%q) = %x, %v; want %x", tt.in, got, err, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in     string
		code   string
		offset string
	}{
		{"DE AG", CodeInvalidChar, "4"},
		{"de-ad", CodeInvalidChar, "2"},
		{"é0", CodeInvalidChar, "0"},
		{"00 é", CodeInvalidChar, "3"},
		{"日本 zz", CodeInvalidChar, "0"},
		{"00日本 zz", CodeInvalidChar, "2"},
		{"😀 zz", CodeInvalidChar, "0"},
		{"00 😀", CodeInvalidChar, "3"},
		{"abc", CodeOddDigits, "2"},
		{"a bc", CodeOddDigits, "0"},
		{"0xabc", CodeOddDigits, "4"},
		{"0x", CodeMissingDigits, "0"},
		{"de 0x ad", CodeMissingDigits, "3"},
		{`\x`, CodeMissingDigits, "0"},
		{`\x\x00`, CodeMissingDigits, "0"},
		{`d\xe`, CodeInvalidChar, "1"},
		{"0x0xab", CodeInvalidChar, "3"},
		{"de\x00", CodeInvalidChar, "2"},
		{"de\xff", CodeInvalidChar, "2"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Errorf("Parse(%q) error = %v, want an *errs.Error", tt.in, err)
			continue
		}
		if e.Kind != errs.InvalidArgument || e.Code != tt.code || e.Details["offset"] != tt.offset || e.Hint == "" {
			t.Errorf("Parse(%q) = %s %s offset %s (%v); want %s offset %s", tt.in, e.Kind, e.Code, e.Details["offset"], e, tt.code, tt.offset)
		}
	}
}

func TestParseMessages(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"DE AG":  "“G” at position 5 is not a hex digit.",
		"de\x00": "U+0000 at position 3 is not a hex digit.",
		"abc":    "The hex digit at position 3 has no partner: bytes are pairs of digits.",
		"0x":     "The prefix at position 1 has no hex digits after it.",
	} {
		_, err := Parse(in)
		if e, ok := errors.AsType[*errs.Error](err); !ok || e.Message != want {
			t.Errorf("Parse(%q) message = %v, want %q", in, err, want)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"DE AD", "0xde,0xad", `\xde`, "abc", "日本"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		b, err := Parse(s)
		if err != nil {
			if _, ok := errors.AsType[*errs.Error](err); !ok {
				t.Fatalf("Parse(%q) error %T", s, err)
			}
			return
		}
		if len(b) > len(s)/2 {
			t.Fatalf("Parse(%q) = %d bytes", s, len(b))
		}
	})
}
