package charset

import (
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"utf-8": UTF8, "UTF8": UTF8, ` "utf-8" `: UTF8,
		"utf-16": UTF16LE, "UTF-16LE": UTF16LE, "utf-16be": UTF16BE,
		"ISO-8859-1": ISO88591, "latin1": ISO88591,
		"us-ascii": ASCII, "ASCII": ASCII,
		"windows-1252": Windows1252, "CP1252": Windows1252,
		"shift_jis": "", "koi8-r": "", "": "",
	}
	for label, want := range tests {
		if got := Canonical(label); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestBOM(t *testing.T) {
	t.Parallel()
	tests := []struct {
		data string
		enc  string
		n    int
	}{
		{"\xEF\xBB\xBFa", UTF8, 3},
		{"\xFF\xFEa\x00", UTF16LE, 2},
		{"\xFE\xFF\x00a", UTF16BE, 2},
		{"abc", "", 0},
		{"", "", 0},
	}
	for _, tt := range tests {
		if enc, n := BOM([]byte(tt.data)); enc != tt.enc || n != tt.n {
			t.Errorf("BOM(%q) = %q, %d, want %q, %d", tt.data, enc, n, tt.enc, tt.n)
		}
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		data    string
		enc     string
		want    string
		invalid bool
	}{
		{"utf-8", "héllo", UTF8, "héllo", false},
		{"utf-8 invalid", "a\xffb", UTF8, "a\uFFFDb", true},
		{"unsupported as utf-8", "abc", "", "abc", false},
		{"utf-16le", "h\x00\xe9\x00", UTF16LE, "hé", false},
		{"utf-16be", "\x00h\x00\xe9", UTF16BE, "hé", false},
		{"utf-16 surrogate pair", "\x3d\xd8\x00\xde", UTF16LE, "\U0001F600", false},
		{"utf-16 lone surrogate", "\x3d\xd8a\x00", UTF16LE, "\uFFFDa", true},
		{"utf-16 odd length", "a\x00b", UTF16LE, "a\uFFFD", true},
		{"latin-1", "caf\xe9 \x80", ISO88591, "café \u0080", false},
		{"windows-1252", "\x93quoted\x94 \x80 \x81", Windows1252, "\u201cquoted\u201d € \u0081", false},
		{"ascii", "ok", ASCII, "ok", false},
		{"ascii high byte", "o\xe9", ASCII, "o\uFFFD", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, invalid := Decode([]byte(tt.data), tt.enc)
			if got != tt.want || invalid != tt.invalid {
				t.Errorf("Decode = %q, %v, want %q, %v", got, invalid, tt.want, tt.invalid)
			}
		})
	}
}

func TestXMLDeclared(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		`<?xml version="1.0" encoding="ISO-8859-1"?><a/>`:    "ISO-8859-1",
		"\xEF\xBB\xBF<?xml version='1.0' encoding='utf-8'?>": "utf-8",
		`<?xml version="1.0"?><a encoding="x"/>`:             "",
		`<a/>`:                                               "",
		`<?xml encoding="x"`:                                 "",
		`<?xml-stylesheet href="a"?>`:                        "",
	}
	for in, want := range tests {
		if got := XMLDeclared([]byte(in)); got != want {
			t.Errorf("XMLDeclared(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTMLMeta(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		`<html><head><meta charset="windows-1252"><title>x</title>`: "windows-1252",
		`<META CHARSET=Latin1>`: "latin1",
		`<meta http-equiv="Content-Type" content="text/html; charset=ISO-8859-1">`:  "iso-8859-1",
		`<meta name="viewport" content="width=device-width"><meta charset='utf-8'>`: "utf-8",
		`<meta http-equiv="Content-Type" content="text/html">`:                      "",
		`<html><body>no meta</body></html>`:                                         "",
		strings.Repeat(" ", 1100) + `<meta charset="utf-8">`:                        "",
		`<meta name="description" content="Our charset=big deal">`:                  "big",
		`<meta data-charset="x" charset="utf-8">`:                                   "utf-8",
	}
	for in, want := range tests {
		if got := HTMLMeta([]byte(in)); got != want {
			t.Errorf("HTMLMeta(%.60q) = %q, want %q", in, got, want)
		}
	}
}

func TestHexDump(t *testing.T) {
	t.Parallel()
	got := HexDump([]byte("Hello, world!\n\x00\x01\xffABCDEFGH"))
	want := "00000000  48 65 6c 6c 6f 2c 20 77  6f 72 6c 64 21 0a 00 01  |Hello, world!...|\n" +
		"00000010  ff 41 42 43 44 45 46 47  48                       |.ABCDEFGH|"
	if got != want {
		t.Errorf("HexDump =\n%s\nwant\n%s", got, want)
	}
	if HexDump(nil) != "" {
		t.Error("HexDump(nil) is not empty")
	}
	if got := HexDumpAt([]byte("AB"), 0x1230); got != "00001230"+HexDump([]byte("AB"))[8:] {
		t.Errorf("HexDumpAt = %q", got)
	}
	// Past 4 GiB the offsets get more digits.
	if got := HexDumpAt([]byte("A"), 5<<30); !strings.HasPrefix(got, "140000000  41") {
		t.Errorf("HexDumpAt past 4 GiB = %q", got)
	}
}
