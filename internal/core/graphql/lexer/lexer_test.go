package lexer

import (
	"slices"
	"strings"
	"testing"
)

// tok is the part of a Token the kind tests compare.
type tok struct {
	kind Kind
	text string
	err  ErrCode
}

func lex(src string, mode Mode) []tok {
	var out []tok
	for _, t := range Tokenize(src, mode) {
		if t.Kind == EOF {
			break
		}
		out = append(out, tok{t.Kind, t.Text(src), t.Err})
	}
	return out
}

func k(kind Kind, text string) tok { return tok{kind: kind, text: text} }

func e(code ErrCode, text string) tok { return tok{kind: Error, text: text, err: code} }

func TestPunctuators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want Kind
	}{
		{"!", Bang}, {"$", Dollar}, {"&", Amp}, {"(", ParenL}, {")", ParenR},
		{"...", Spread}, {":", Colon}, {"=", Equals}, {"@", At}, {"[", BracketL},
		{"]", BracketR}, {"{", BraceL}, {"|", Pipe}, {"}", BraceR},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			got := lex(tt.src, 0)
			if want := []tok{k(tt.want, tt.src)}; !slices.Equal(got, want) {
				t.Errorf("lex(%q) = %v, want %v", tt.src, got, want)
			}
			if !tt.want.IsPunctuator() || tt.want.String() != tt.src {
				t.Errorf("Kind %v: IsPunctuator=%v String=%q", tt.want, tt.want.IsPunctuator(), tt.want.String())
			}
		})
	}
}

func TestTokenKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want []tok
	}{
		{"empty", "", nil},
		{"names", "a _b Foo_9 __typename", []tok{k(Name, "a"), k(Name, "_b"), k(Name, "Foo_9"), k(Name, "__typename")}},
		{"ints", "0 -0 7 123 -45", []tok{k(Int, "0"), k(Int, "-0"), k(Int, "7"), k(Int, "123"), k(Int, "-45")}},
		{"floats", "1.5 -0.25 1e10 1E+3 2.5e-3 0.0 -1.0E0",
			[]tok{k(Float, "1.5"), k(Float, "-0.25"), k(Float, "1e10"), k(Float, "1E+3"), k(Float, "2.5e-3"), k(Float, "0.0"), k(Float, "-1.0E0")}},
		{"number then punctuator", "[1,2.5]", []tok{k(BracketL, "["), k(Int, "1"), k(Float, "2.5"), k(BracketR, "]")}},
		{"strings", `"" "abc" "a b#c,d"`, []tok{k(String, `""`), k(String, `"abc"`), k(String, `"a b#c,d"`)}},
		{"string escapes", `"\" \\ \/ \b \f \n \r \t"`, []tok{k(String, `"\" \\ \/ \b \f \n \r \t"`)}},
		{"unicode escapes", `"\u00e9" "\u{1F600}" "\uD83D\uDE00" "\u{0}"`,
			[]tok{k(String, `"\u00e9"`), k(String, `"\u{1F600}"`), k(String, `"\uD83D\uDE00"`), k(String, `"\u{0}"`)}},
		{"non-ASCII in string", "\"h\U000000e9llo \U0001F600\"", []tok{k(String, "\"h\U000000e9llo \U0001F600\"")}},
		{"block strings", `"""x""" """""" """a\"""b""" """ "quoted" ""x"" """`,
			[]tok{k(BlockString, `"""x"""`), k(BlockString, `""""""`), k(BlockString, `"""a\"""b"""`), k(BlockString, `""" "quoted" ""x"" """`)}},
		{"multi-line block string", "\"\"\"\n  a\r\n  b\r\"\"\" c",
			[]tok{k(BlockString, "\"\"\"\n  a\r\n  b\r\"\"\""), k(Name, "c")}},
		{"block string then quote", `""""""" x`, []tok{k(BlockString, `""""""`), e(ErrUnterminatedString, `" x`)}},
		{"comments", "# hi\nfoo #tail\r#\r\n", []tok{k(Comment, "# hi"), k(Name, "foo"), k(Comment, "#tail"), k(Comment, "#")}},
		{"spread", "...Frag ... on T", []tok{k(Spread, "..."), k(Name, "Frag"), k(Spread, "..."), k(Name, "on"), k(Name, "T")}},
		{"variables and defaults", "($v:Int!=5)", []tok{k(ParenL, "("), k(Dollar, "$"), k(Name, "v"), k(Colon, ":"), k(Name, "Int"), k(Bang, "!"), k(Equals, "="), k(Int, "5"), k(ParenR, ")")}},
		{"directives and unions", "@include(if:$x) A|B&C", []tok{k(At, "@"), k(Name, "include"), k(ParenL, "("), k(Name, "if"), k(Colon, ":"), k(Dollar, "$"), k(Name, "x"), k(ParenR, ")"), k(Name, "A"), k(Pipe, "|"), k(Name, "B"), k(Amp, "&"), k(Name, "C")}},
		{"no ignored in default mode", "\U0000FEFF a ,\t\r\n,b", []tok{k(Name, "a"), k(Name, "b")}},
		{"document", "query Q {\n  user(id: \"1\") { ...F }\n}",
			[]tok{k(Name, "query"), k(Name, "Q"), k(BraceL, "{"), k(Name, "user"), k(ParenL, "("), k(Name, "id"), k(Colon, ":"), k(String, `"1"`), k(ParenR, ")"), k(BraceL, "{"), k(Spread, "..."), k(Name, "F"), k(BraceR, "}"), k(BraceR, "}")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := lex(tt.src, 0); !slices.Equal(got, tt.want) {
				t.Errorf("lex(%q)\n got %v\nwant %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestIgnoredTokens(t *testing.T) {
	t.Parallel()

	src := "\U0000FEFFa ,\t\r\n#c\rb\n,,"
	want := []tok{
		k(BOM, "\U0000FEFF"), k(Name, "a"), k(Whitespace, " "), k(Comma, ","), k(Whitespace, "\t"),
		k(LineTerminator, "\r\n"), k(Comment, "#c"), k(LineTerminator, "\r"), k(Name, "b"),
		k(LineTerminator, "\n"), k(Comma, ","), k(Comma, ","),
	}
	if got := lex(src, EmitIgnored); !slices.Equal(got, want) {
		t.Errorf("EmitIgnored\n got %v\nwant %v", got, want)
	}
	for _, kind := range []Kind{Whitespace, LineTerminator, Comma, BOM} {
		if !kind.IsIgnored() || kind.IsPunctuator() {
			t.Errorf("%v: IsIgnored=%v IsPunctuator=%v", kind, kind.IsIgnored(), kind.IsPunctuator())
		}
	}
	if Comment.IsIgnored() || Name.IsIgnored() {
		t.Error("Comment and Name must not be IsIgnored")
	}
}

func TestSkipComments(t *testing.T) {
	t.Parallel()

	src := "# lead\na # trail\n#"
	if got, want := lex(src, SkipComments), []tok{k(Name, "a")}; !slices.Equal(got, want) {
		t.Errorf("SkipComments: got %v, want %v", got, want)
	}
	want := []tok{k(LineTerminator, "\n"), k(Name, "a"), k(Whitespace, " "), k(LineTerminator, "\n")}
	if got := lex(src, SkipComments|EmitIgnored); !slices.Equal(got, want) {
		t.Errorf("SkipComments|EmitIgnored: got %v, want %v", got, want)
	}
}

func TestMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want []tok
	}{
		// Numbers.
		{"trailing dot", "1. x", []tok{e(ErrInvalidNumber, "1."), k(Name, "x")}},
		{"dot then name", "1.a", []tok{e(ErrInvalidNumber, "1.a")}},
		{"leading dot", ".5", []tok{e(ErrUnexpectedChar, "."), k(Int, "5")}},
		{"hex", "0x1F", []tok{e(ErrInvalidNumber, "0x1F")}},
		{"name after int", "123abc)", []tok{e(ErrInvalidNumber, "123abc"), k(ParenR, ")")}},
		{"underscore after int", "1_000", []tok{e(ErrInvalidNumber, "1_000")}},
		{"name after float", "1.5e3x", []tok{e(ErrInvalidNumber, "1.5e3x")}},
		{"leading zero", "00 01", []tok{e(ErrInvalidNumber, "00"), e(ErrInvalidNumber, "01")}},
		{"negative leading zero", "-01", []tok{e(ErrInvalidNumber, "-01")}},
		{"lone minus", "- 1", []tok{e(ErrInvalidNumber, "-"), k(Int, "1")}},
		{"minus at end", "-", []tok{e(ErrInvalidNumber, "-")}},
		{"minus name", "-a", []tok{e(ErrInvalidNumber, "-a")}},
		{"empty exponent", "1e 1E+ 1.5e-]", []tok{e(ErrInvalidNumber, "1e"), e(ErrInvalidNumber, "1E+"), e(ErrInvalidNumber, "1.5e-"), k(BracketR, "]")}},
		{"two fractions", "1.2.3", []tok{e(ErrInvalidNumber, "1.2.3")}},
		{"dot at end", "7.", []tok{e(ErrInvalidNumber, "7.")}},

		// Dots.
		{"one dot", ". a", []tok{e(ErrUnexpectedChar, "."), k(Name, "a")}},
		{"two dots", "..a", []tok{e(ErrUnexpectedChar, ".."), k(Name, "a")}},
		{"four dots", "....", []tok{k(Spread, "..."), e(ErrUnexpectedChar, ".")}},
		{"spaced dots", ". . .", []tok{e(ErrUnexpectedChar, "."), e(ErrUnexpectedChar, "."), e(ErrUnexpectedChar, ".")}},

		// Strings.
		{"unterminated at EOF", `"abc`, []tok{e(ErrUnterminatedString, `"abc`)}},
		{"lone quote", `"`, []tok{e(ErrUnterminatedString, `"`)}},
		{"unterminated at newline", "a: \"abc\nb", []tok{k(Name, "a"), k(Colon, ":"), e(ErrUnterminatedString, `"abc`), k(Name, "b")}},
		{"unterminated at CR", "\"abc\rb", []tok{e(ErrUnterminatedString, `"abc`), k(Name, "b")}},
		{"backslash at end of line", "\"a\\\nb", []tok{e(ErrUnterminatedString, `"a\`), k(Name, "b")}},
		{"escaped quote at EOF", `"a\"`, []tok{e(ErrUnterminatedString, `"a\"`)}},
		{"bad escape", `"a\qb" c`, []tok{e(ErrInvalidEscape, `"a\qb"`), k(Name, "c")}},
		{"bad non-ASCII escape", "\"\\\U000000e9\"", []tok{e(ErrInvalidEscape, "\"\\\U000000e9\"")}},
		{"short unicode escape", `"\u12" x`, []tok{e(ErrInvalidEscape, `"\u12"`), k(Name, "x")}},
		{"non-hex unicode escape", `"\uZZZZ"`, []tok{e(ErrInvalidEscape, `"\uZZZZ"`)}},
		{"empty braced escape", `"\u{}"`, []tok{e(ErrInvalidEscape, `"\u{}"`)}},
		{"unclosed braced escape", `"\u{1F600"`, []tok{e(ErrInvalidEscape, `"\u{1F600"`)}},
		{"braced escape out of range", `"\u{110000}"`, []tok{e(ErrInvalidEscape, `"\u{110000}"`)}},
		{"huge braced escape", `"\u{FFFFFFFFFFFFFFFFFFFF}"`, []tok{e(ErrInvalidEscape, `"\u{FFFFFFFFFFFFFFFFFFFF}"`)}},
		{"braced surrogate", `"\u{D800}"`, []tok{e(ErrInvalidEscape, `"\u{D800}"`)}},
		{"lone leading surrogate", `"\uD800"`, []tok{e(ErrInvalidEscape, `"\uD800"`)}},
		{"lone trailing surrogate", `"\uDC00"`, []tok{e(ErrInvalidEscape, `"\uDC00"`)}},
		{"leading surrogate then BMP", `"\uD800\u0041"`, []tok{e(ErrInvalidEscape, `"\uD800\u0041"`)}},
		{"braced surrogate pair", `"\u{D83D}\u{DE00}"`, []tok{e(ErrInvalidEscape, `"\u{D83D}\u{DE00}"`)}},
		{"invalid UTF-8 in string", "\"a\xffb\" c", []tok{e(ErrInvalidUTF8, "\"a\xffb\""), k(Name, "c")}},
		{"escape error wins over later UTF-8", "\"\\q\xff\"", []tok{e(ErrInvalidEscape, "\"\\q\xff\"")}},
		{"unterminated wins over escape", "\"\\q", []tok{e(ErrUnterminatedString, "\"\\q")}},

		// Block strings.
		{"unterminated block string", "\"\"\"abc\n{ x }", []tok{e(ErrUnterminatedBlockString, "\"\"\"abc\n{ x }")}},
		{"block string escaped close only", `"""a\"""`, []tok{e(ErrUnterminatedBlockString, `"""a\"""`)}},
		{"invalid UTF-8 in block string", "\"\"\"\xfe\"\"\" a", []tok{e(ErrInvalidUTF8, "\"\"\"\xfe\"\"\""), k(Name, "a")}},

		// Characters.
		{"unexpected ASCII", "a ? b % ~ ; ' < > * + / \\ ^ `", []tok{
			k(Name, "a"), e(ErrUnexpectedChar, "?"), k(Name, "b"), e(ErrUnexpectedChar, "%"), e(ErrUnexpectedChar, "~"),
			e(ErrUnexpectedChar, ";"), e(ErrUnexpectedChar, "'"), e(ErrUnexpectedChar, "<"), e(ErrUnexpectedChar, ">"),
			e(ErrUnexpectedChar, "*"), e(ErrUnexpectedChar, "+"), e(ErrUnexpectedChar, "/"), e(ErrUnexpectedChar, "\\"),
			e(ErrUnexpectedChar, "^"), e(ErrUnexpectedChar, "`"),
		}},
		{"control characters", "\x00\x01\x0b\x0c\x7f", []tok{e(ErrUnexpectedChar, "\x00"), e(ErrUnexpectedChar, "\x01"), e(ErrUnexpectedChar, "\x0b"), e(ErrUnexpectedChar, "\x0c"), e(ErrUnexpectedChar, "\x7f")}},
		{"non-ASCII letter", "caf\U000000e9", []tok{k(Name, "caf"), e(ErrUnexpectedChar, "\U000000e9")}},
		{"no-break space", "a\U000000a0b", []tok{k(Name, "a"), e(ErrUnexpectedChar, "\U000000a0"), k(Name, "b")}},
		{"emoji", "\U0001F600x", []tok{e(ErrUnexpectedChar, "\U0001F600"), k(Name, "x")}},
		{"invalid UTF-8", "a\xffb\xe2\x82", []tok{k(Name, "a"), e(ErrInvalidUTF8, "\xff"), k(Name, "b"), e(ErrInvalidUTF8, "\xe2"), e(ErrInvalidUTF8, "\x82")}},
		{"encoded surrogate", "\xed\xa0\x80", []tok{e(ErrInvalidUTF8, "\xed"), e(ErrInvalidUTF8, "\xa0"), e(ErrInvalidUTF8, "\x80")}},

		// Half-typed queries.
		{"half-typed argument", "query { user(id: \"12", []tok{k(Name, "query"), k(BraceL, "{"), k(Name, "user"), k(ParenL, "("), k(Name, "id"), k(Colon, ":"), e(ErrUnterminatedString, "\"12")}},
		{"half-typed spread", "{ a .. }", []tok{k(BraceL, "{"), k(Name, "a"), e(ErrUnexpectedChar, ".."), k(BraceR, "}")}},
		{"half-typed float", "f(x: 1.)", []tok{k(Name, "f"), k(ParenL, "("), k(Name, "x"), k(Colon, ":"), e(ErrInvalidNumber, "1."), k(ParenR, ")")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := lex(tt.src, 0); !slices.Equal(got, tt.want) {
				t.Errorf("lex(%q)\n got %v\nwant %v", tt.src, got, tt.want)
			}
			for _, tk := range Tokenize(tt.src, 0) {
				if (tk.Kind == Error) != (tk.Err != ErrNone) || (tk.Kind == Error) != (tk.Message != "") {
					t.Errorf("token %+v: Err and Message must be set exactly for Error tokens", tk)
				}
			}
		})
	}
}

func TestErrorMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src, want string
	}{
		{"1.", `invalid number: expected a digit after ".", got end of input`},
		{"123abc", `invalid number: unexpected 'a' after the number`},
		{"01", "invalid number: unexpected digit after 0"},
		{"-x", `invalid number: expected a digit after "-", got 'x'`},
		{"1e+", "invalid number: expected a digit in the exponent, got end of input"},
		{"..", `unexpected "..", did you mean "..."?`},
		{"?", `unexpected character '?' (U+003F)`},
		{"\U000000e9", "unexpected character '\U000000e9' (U+00E9)"},
		{"\xff", "invalid UTF-8 byte 0xFF"},
		{`"abc`, "unterminated string"},
		{`"""abc`, "unterminated block string"},
		{`"\q"`, `invalid escape sequence "\\q"`},
		{`"\u{D800}"`, `invalid Unicode escape sequence "\\u{D800}": not a Unicode scalar value`},
		{`"\uD800"`, `invalid Unicode escape sequence "\\uD800": lone leading surrogate`},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			got := New(tt.src, 0).Next()
			if got.Kind != Error || got.Message != tt.want {
				t.Errorf("Next() = %v %q, want Error %q", got.Kind, got.Message, tt.want)
			}
		})
	}
}

func TestPositions(t *testing.T) {
	t.Parallel()

	type pos struct {
		text             string
		start, line, col int
	}
	tests := []struct {
		name string
		src  string
		want []pos // all tokens including EOF
	}{
		{"line terminators", "a\r\nb\rc\n\nd", []pos{{"a", 0, 1, 1}, {"b", 3, 2, 1}, {"c", 5, 3, 1}, {"d", 8, 5, 1}, {"", 9, 5, 2}}},
		{"columns count code points", "  \U000000e9\U0001F600 d", []pos{{"\U000000e9", 2, 1, 3}, {"\U0001F600", 4, 1, 4}, {"d", 9, 1, 6}, {"", 10, 1, 7}}},
		{"after block string", "\"\"\"x\r\n y\"\"\"z", []pos{{"\"\"\"x\r\n y\"\"\"", 0, 1, 1}, {"z", 11, 2, 6}, {"", 12, 2, 7}}},
		{"after comment and BOM", "\U0000FEFF# \U000000e9\n  x", []pos{{"# \U000000e9", 3, 1, 2}, {"x", 10, 2, 3}, {"", 11, 2, 4}}},
		{"invalid UTF-8 counts one column", "\xff\xfe a", []pos{{"\xff", 0, 1, 1}, {"\xfe", 1, 1, 2}, {"a", 3, 1, 4}, {"", 4, 1, 5}}},
		{"string with non-ASCII", "\"\U000000e9\" a", []pos{{"\"\U000000e9\"", 0, 1, 1}, {"a", 5, 1, 5}, {"", 6, 1, 6}}},
		{"EOF after newline", "a\n", []pos{{"a", 0, 1, 1}, {"", 2, 2, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []pos
			for _, tk := range Tokenize(tt.src, 0) {
				got = append(got, pos{tk.Text(tt.src), tk.Start, tk.Line, tk.Column})
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("positions of %q\n got %v\nwant %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestNextAfterEOF(t *testing.T) {
	t.Parallel()

	l := New("a", 0)
	if tk := l.Next(); tk.Kind != Name {
		t.Fatalf("first token = %v, want Name", tk.Kind)
	}
	for range 3 {
		if tk := l.Next(); tk.Kind != EOF || tk.Start != 1 || tk.End != 1 || tk.Line != 1 || tk.Column != 2 {
			t.Errorf("Next() at end = %+v, want EOF at 1:2", tk)
		}
	}
}

func TestKindAndErrCodeStrings(t *testing.T) {
	t.Parallel()

	for kind := EOF; kind < numKinds; kind++ {
		if s := kind.String(); s == "" || strings.HasPrefix(s, "Kind(") {
			t.Errorf("Kind %d has no name", kind)
		}
	}
	if got := Kind(200).String(); got != "Kind(200)" {
		t.Errorf("unknown Kind String() = %q", got)
	}
	for code := ErrNone; code <= ErrInvalidEscape; code++ {
		if s := code.String(); s == "" || strings.HasPrefix(s, "ErrCode(") {
			t.Errorf("ErrCode %d has no name", code)
		}
	}
	if got := ErrCode(200).String(); got != "ErrCode(200)" {
		t.Errorf("unknown ErrCode String() = %q", got)
	}
}

func TestLongLineIsLinear(t *testing.T) {
	t.Parallel()

	// A minified single-line document of about 1 MB: position tracking must
	// not rescan the line for every token.
	const n = 150_000
	src := strings.Repeat("\U000000e9{a b}", n)
	toks := Tokenize(src, 0)
	last := toks[len(toks)-2]
	if last.Kind != BraceR || last.Line != 1 || last.Column != n*6 {
		t.Errorf("last token = %+v, want } at 1:%d", last, n*6)
	}
}
