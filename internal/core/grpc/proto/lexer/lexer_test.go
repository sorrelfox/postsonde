package lexer

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// show renders tokens compactly: kind, text and line:column, with the error
// code for Error tokens.
func show(src string, toks []Token) string {
	var parts []string
	for _, t := range toks {
		s := fmt.Sprintf("%v %q %d:%d", t.Kind, t.Text(src), t.Line, t.Column)
		if t.Kind == Error {
			s += " " + t.Err.String()
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, src string
		want      []string
	}{
		{"declaration", `syntax = "proto3";`, []string{
			`Ident "syntax" 1:1`, `= "=" 1:8`, `String "\"proto3\"" 1:10`, `; ";" 1:18`, `EOF "" 1:19`,
		}},
		{"punctuators", `;,.=:/-+()[]{}<>`, []string{
			`; ";" 1:1`, `, "," 1:2`, `. "." 1:3`, `= "=" 1:4`, `: ":" 1:5`, `/ "/" 1:6`, `- "-" 1:7`,
			`+ "+" 1:8`, `( "(" 1:9`, `) ")" 1:10`, `[ "[" 1:11`, `] "]" 1:12`, `{ "{" 1:13`, `} "}" 1:14`,
			`< "<" 1:15`, `> ">" 1:16`, `EOF "" 1:17`,
		}},
		{"qualified names", `.foo.Bar _x9`, []string{
			`. "." 1:1`, `Ident "foo" 1:2`, `. "." 1:5`, `Ident "Bar" 1:6`, `Ident "_x9" 1:10`, `EOF "" 1:13`,
		}},
		{"numbers", `0 17 0x1F 017 1.5 .5 1. 1e9 2E-3 1.5f inf -nan`, []string{
			`Int "0" 1:1`, `Int "17" 1:3`, `Int "0x1F" 1:6`, `Int "017" 1:11`, `Float "1.5" 1:15`,
			`Float ".5" 1:19`, `Float "1." 1:22`, `Float "1e9" 1:25`, `Float "2E-3" 1:29`, `Float "1.5f" 1:34`,
			`Ident "inf" 1:39`, `- "-" 1:43`, `Ident "nan" 1:44`, `EOF "" 1:47`,
		}},
		{"strings", "\"a\\\"b\" 'c\\'d' \"\\x41\\101\\u00e9\"", []string{
			`String "\"a\\\"b\"" 1:1`, `String "'c\\'d'" 1:8`, `String "\"\\x41\\101\\u00e9\"" 1:15`, `EOF "" 1:31`,
		}},
		{"comments", "// line\n/* block\n * two */ x", []string{
			`Comment "// line" 1:1`, `Comment "/* block\n * two */" 2:1`, `Ident "x" 3:11`, `EOF "" 3:12`,
		}},
		{"line endings and BOM", "\xef\xbb\xbfa\r\nb\rc\nd", []string{
			`Ident "a" 1:2`, `Ident "b" 2:1`, `Ident "c" 3:1`, `Ident "d" 4:1`, `EOF "" 4:2`,
		}},
		{"columns count code points", `"é" x`, []string{
			`String "\"é\"" 1:1`, `Ident "x" 1:5`, `EOF "" 1:6`,
		}},
		{"errors", "@ \xff é 0x 08 1.2.3 12ab 1e+ \"a\\qb\" 'open\nz /* open", []string{
			`Error "@" 1:1 unexpected character`,
			`Error "\xff" 1:3 invalid UTF-8`,
			`Error "é" 1:5 unexpected character`,
			`Error "0x" 1:7 invalid number`,
			`Error "08" 1:10 invalid number`,
			`Error "1.2.3" 1:13 invalid number`,
			`Error "12ab" 1:19 invalid number`,
			`Error "1e+" 1:24 invalid number`,
			`Error "\"a\\qb\"" 1:28 invalid escape sequence`,
			`Error "'open" 1:35 unterminated string`,
			`Ident "z" 2:1`,
			`Error "/* open" 2:3 unterminated comment`,
			`EOF "" 2:10`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := show(tt.src, Tokenize(tt.src, 0))
			if want := strings.Join(tt.want, "\n"); got != want {
				t.Errorf("Tokenize(%q):\n%s\nwant:\n%s", tt.src, got, want)
			}
		})
	}
}

func TestSkipComments(t *testing.T) {
	t.Parallel()
	src := "a // x\n/* y */ b"
	got := show(src, Tokenize(src, SkipComments))
	if want := "Ident \"a\" 1:1\nIdent \"b\" 2:9\nEOF \"\" 2:10"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestErrorMessages(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"0x":           `"0x" must be followed by hex digits`,
		"09":           "numbers starting with a leading zero must be in octal",
		"1.5.":         "already saw decimal point or exponent; can't have another one",
		"0x1.":         "hex and octal numbers must be integers",
		"5x":           "need space between number and identifier",
		"1e":           `"e" must be followed by an exponent`,
		`"\z"`:         `invalid escape sequence "\z"`,
		`"\x"`:         `"\x" must be followed by hex digits`,
		`"\u12"`:       `"\u" must be followed by 4 hex digits`,
		`"\U0011FFFF"`: `escape \U0011FFFF is beyond U+10FFFF`,
		`"\uD800"`:     `unpaired surrogate \uD800 in escape`,
		`"\400"`:       `octal escape \400 is above \377`,
		"\"a\\\n\"":    "unterminated string",
	}
	for src, want := range tests {
		toks := Tokenize(src, 0)
		if toks[0].Kind != Error || toks[0].Message != want {
			t.Errorf("Tokenize(%q)[0] = %v %q, want error %q", src, toks[0].Kind, toks[0].Message, want)
		}
	}
}

func TestStringValue(t *testing.T) {
	t.Parallel()
	tests := []struct{ text, want string }{
		{`""`, ""},
		{`"plain"`, "plain"},
		{`'single "quoted"'`, `single "quoted"`},
		{`"\a\b\f\n\r\t\v\\\'\"\?"`, "\a\b\f\n\r\t\v\\'\"?"},
		{`"\0\7\101\1234"`, "\x00\x07A" + "S4"},
		{`"\x41\X4a\x7"`, "AJ\x07"},
		{"\"\\u00e9\\U0001F600\"", "é😀"},
		{"\"\\uD83D\\uDE00\"", "😀"},
		{`"\xff\xfe"`, "\xff\xfe"},
	}
	for _, tt := range tests {
		got, err := StringValue(tt.text)
		if err != nil || got != tt.want {
			t.Errorf("StringValue(%s) = %q, %v; want %q", tt.text, got, err, tt.want)
		}
	}
	for _, bad := range []string{``, `"`, `"abc'`, `"a"b"`, `"\q"`, "\"a\nb\"", `abc`} {
		if got, err := StringValue(bad); err == nil {
			t.Errorf("StringValue(%q) = %q, want an error", bad, got)
		}
	}
}

func TestIntValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want uint64
	}{
		{"0", 0}, {"42", 42}, {"0x2A", 42}, {"0X2a", 42}, {"052", 42},
		{"18446744073709551615", math.MaxUint64}, {"0xFFFFFFFFFFFFFFFF", math.MaxUint64},
	}
	for _, tt := range tests {
		if got, err := IntValue(tt.text); err != nil || got != tt.want {
			t.Errorf("IntValue(%q) = %d, %v; want %d", tt.text, got, err, tt.want)
		}
	}
	if _, err := IntValue("18446744073709551616"); err != ErrIntRange {
		t.Errorf("IntValue(2^64) error = %v, want ErrIntRange", err)
	}
	for _, bad := range []string{"", "0x", "1.5", "-1", "+1", "1_0", "abc", "08"} {
		if _, err := IntValue(bad); err == nil || err == ErrIntRange {
			t.Errorf("IntValue(%q) error = %v, want invalid", bad, err)
		}
	}
}

func TestFloatValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want float64
	}{
		{"1.5", 1.5}, {".5", 0.5}, {"1.", 1}, {"1e3", 1000}, {"2E-1", 0.2}, {"1.5f", 1.5}, {"3F", 3},
		{"1e999", math.Inf(1)}, {"18446744073709551616", 18446744073709551616},
		{"0x10000000000000000", 18446744073709551616}, {"017", 15},
	}
	for _, tt := range tests {
		if got, err := FloatValue(tt.text); err != nil || got != tt.want {
			t.Errorf("FloatValue(%q) = %v, %v; want %v", tt.text, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "f", "inf", "nan", "0x", "1_0", "-1", "0x1p3"} {
		if got, err := FloatValue(bad); err == nil {
			t.Errorf("FloatValue(%q) = %v, want an error", bad, got)
		}
	}
}

func TestIsIdent(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{"a": true, "_a1": true, "A_b": true, "": false, "1a": false, "a-b": false, "a.b": false, "é": false} {
		if got := IsIdent(s); got != want {
			t.Errorf("IsIdent(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestKindAndCodeStrings(t *testing.T) {
	t.Parallel()
	if got := Semicolon.String(); got != ";" {
		t.Errorf("Semicolon = %q", got)
	}
	if got := Kind(200).String(); got != "Kind(200)" {
		t.Errorf("Kind(200) = %q", got)
	}
	if got := ErrCode(200).String(); got != "ErrCode(200)" {
		t.Errorf("ErrCode(200) = %q", got)
	}
	if !BraceL.IsPunctuator() || Ident.IsPunctuator() {
		t.Error("IsPunctuator")
	}
}
