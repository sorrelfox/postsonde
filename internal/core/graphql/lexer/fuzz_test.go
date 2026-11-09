package lexer

import (
	"slices"
	"testing"
	"unicode/utf8"
)

// FuzzTokenize checks the lexer's invariants on arbitrary input: it never
// panics, EmitIgnored tokens cover the input contiguously, positions match
// a naive recount, the default mode is the same stream without ignored
// tokens, string tokens have values, and UTF16Index agrees with a reference.
func FuzzTokenize(f *testing.F) {
	for _, seed := range []string{
		"", "query Q($v: Int = 1) { a(b: \"c\") @d { ...F ... on T { e } } }",
		"\U0000FEFF# c\r\n,\t", `"\u{1F600}\uD83D\uDE00\"\\"`, "\"\"\"\n  a \\\"\"\"\n\"\"\"",
		"1. 0x 123abc -01 1e+ .. ....", "\"abc\n\"\"\"x", "\xff\xe2\x82\U0001F600\U000000e9",
		"type A implements B & C { f: [Int!]! } union U = | A | B",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		all := Tokenize(src, EmitIgnored)

		off, line, col := 0, 1, 1
		for i, tk := range all {
			if tk.Start != off {
				t.Fatalf("token %d %+v starts at %d, want %d (not contiguous)", i, tk, tk.Start, off)
			}
			if tk.Line != line || tk.Column != col {
				t.Fatalf("token %d %+v at %d:%d, want %d:%d", i, tk, tk.Line, tk.Column, line, col)
			}
			if tk.Kind == EOF {
				if i != len(all)-1 || tk.End != len(src) || tk.Start != len(src) {
					t.Fatalf("bad EOF token %d %+v", i, tk)
				}
				break
			}
			if tk.End <= tk.Start || tk.End > len(src) {
				t.Fatalf("token %d %+v has bad span", i, tk)
			}
			if (tk.Kind == Error) != (tk.Err != ErrNone) || (tk.Kind == Error) != (tk.Message != "") {
				t.Fatalf("token %d %+v: Err/Message inconsistent with Kind", i, tk)
			}
			text := tk.Text(src)
			switch tk.Kind {
			case String, BlockString:
				if _, err := StringValue(text); err != nil {
					t.Fatalf("token %d %q: StringValue: %v", i, text, err)
				}
			case Error:
			default:
				// A valid token lexes the same on its own.
				if again := Tokenize(text, EmitIgnored); len(again) != 2 || again[0].Kind != tk.Kind {
					t.Fatalf("token %d %v %q relexes as %+v", i, tk.Kind, text, again)
				}
			}
			line, col = advance(src, off, tk.End, line, col)
			off = tk.End
		}

		var want []Token
		for _, tk := range all {
			if !tk.Kind.IsIgnored() {
				want = append(want, tk)
			}
		}
		if got := Tokenize(src, 0); !slices.Equal(got, want) {
			t.Fatalf("default mode differs from EmitIgnored without ignored tokens")
		}

		// StringValue accepts exactly the texts of string tokens.
		if _, err := StringValue(src); err == nil {
			if len(all) != 2 || (all[0].Kind != String && all[0].Kind != BlockString) {
				t.Fatalf("StringValue accepts %q, which lexes as %+v", src, all)
			}
		}

		if len(src) <= 256 {
			x := NewUTF16Index(src)
			for _, tk := range all {
				u := utf16Len(src[:tk.Start])
				if got := x.ToUTF16(tk.Start); got != u {
					t.Fatalf("ToUTF16(%d) = %d, want %d", tk.Start, got, u)
				}
				if got := x.ToByte(u); got != tk.Start {
					t.Fatalf("ToByte(%d) = %d, want %d", u, got, tk.Start)
				}
			}
		}
	})
}

// advance is the naive position reference: it recounts line and column
// from src[from:to].
func advance(src string, from, to, line, col int) (int, int) {
	for i := from; i < to; {
		switch c := src[i]; {
		case c == '\r' && i+1 < len(src) && src[i+1] == '\n':
			line, col = line+1, 1
			i += 2
		case c == '\r' || c == '\n':
			line, col = line+1, 1
			i++
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			col++
			i += size
		}
	}
	return line, col
}
