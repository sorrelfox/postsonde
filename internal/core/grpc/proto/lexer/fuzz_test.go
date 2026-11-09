package lexer

import (
	"testing"
	"unicode/utf8"
)

// FuzzTokenize checks the lexer's invariants on arbitrary input: tokens are
// in order without overlap and only whitespace (or a leading byte order
// mark) lies between them; Line and Column match a naive recount; the last
// token is EOF; error tokens carry a code and message; and every String,
// Int and Float token decodes.
func FuzzTokenize(f *testing.F) {
	for _, s := range []string{
		"", `syntax = "proto3"; package a.b;`, "message M { map<string, int32> m = 1 [(x).y = -inf]; }",
		"\xef\xbb\xbf// c\r\n/* b */ 0x1F 017 1.5e3f .5 'a\\x41\\101'", "0x 09 1.2.3 12ab \"\\q\" 'open\n/* open",
		"option (a) = { b: [1, 2] c < d: \"e\" \"f\" > };", "\"\\uD83D\\uDE00\\U0010FFFF\\400\"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		toks := Tokenize(src, 0)
		if last := toks[len(toks)-1]; last.Kind != EOF || last.Start != len(src) || last.End != len(src) {
			t.Fatalf("last token %+v", last)
		}
		prev, line, col := 0, 1, 1
		for i, tk := range toks {
			if tk.Start < prev || tk.End < tk.Start || tk.End > len(src) {
				t.Fatalf("token %d %+v out of order (prev end %d)", i, tk, prev)
			}
			if tk.Kind != EOF && tk.End == tk.Start {
				t.Fatalf("empty token %d %+v", i, tk)
			}
			gap := src[prev:tk.Start]
			if prev == 0 && len(gap) >= 3 && gap[:3] == "\xef\xbb\xbf" {
				gap = gap[3:]
			}
			for j := 0; j < len(gap); j++ {
				if !IsSpace(gap[j]) {
					t.Fatalf("non-space %q before token %d %+v", gap[j], i, tk)
				}
			}
			line, col = advance(src, prev, tk.Start, line, col)
			if tk.Line != line || tk.Column != col {
				t.Fatalf("token %d %+v at %d:%d, want %d:%d", i, tk, tk.Line, tk.Column, line, col)
			}
			line, col = advance(src, tk.Start, tk.End, line, col)
			prev = tk.End

			text := tk.Text(src)
			switch tk.Kind {
			case Error:
				if tk.Err == ErrNone || tk.Message == "" {
					t.Fatalf("error token without code or message: %+v", tk)
				}
			case String:
				if _, err := StringValue(text); err != nil {
					t.Fatalf("String token %q does not decode: %v", text, err)
				}
			case Int:
				if _, err := IntValue(text); err != nil && err != ErrIntRange {
					t.Fatalf("Int token %q does not decode: %v", text, err)
				}
				if _, err := FloatValue(text); err != nil {
					t.Fatalf("Int token %q does not decode as a float: %v", text, err)
				}
			case Float:
				if _, err := FloatValue(text); err != nil {
					t.Fatalf("Float token %q does not decode: %v", text, err)
				}
			case Ident:
				if !IsIdent(text) {
					t.Fatalf("Ident token %q is not an identifier", text)
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
