package lexer

import (
	"errors"
	"testing"
)

func TestStringValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, lit, want string
	}{
		{"empty", `""`, ""},
		{"plain", `"hello world"`, "hello world"},
		{"non-ASCII", "\"h\U000000e9 \U0001F600\"", "h\U000000e9 \U0001F600"},
		{"simple escapes", `"\" \\ \/ \b \f \n \r \t"`, "\" \\ / \b \f \n \r \t"},
		{"fixed unicode", `"\u00e9\u0041"`, "\U000000e9A"},
		{"fixed unicode zero", `"\u0000"`, "\x00"},
		{"surrogate pair", `"\uD83D\uDE00"`, "\U0001F600"},
		{"lowercase surrogate pair", `"\ud83d\ude00!"`, "\U0001F600!"},
		{"braced unicode", `"\u{1F600}\u{e9}\u{0000041}"`, "\U0001F600\U000000e9A"},
		{"braced max", `"\u{10FFFF}"`, "\U0010FFFF"},
		{"hash and comma", `"# not, a comment"`, "# not, a comment"},
		{"block empty", `""""""`, ""},
		{"block simple", `"""hello"""`, "hello"},
		{"block no escapes", `"""a\nb \u0041"""`, `a\nb \u0041`},
		{"block escaped quotes", `"""a \""" b"""`, `a """ b`},
		{"block indentation", "\"\"\"\n    Hello,\n      World!\n\n    Yours,\n      GraphQL.\n  \"\"\"",
			"Hello,\n  World!\n\nYours,\n  GraphQL."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := StringValue(tt.lit)
			if err != nil || got != tt.want {
				t.Errorf("StringValue(%q) = %q, %v; want %q", tt.lit, got, err, tt.want)
			}
			if toks := Tokenize(tt.lit, 0); len(toks) != 2 || (toks[0].Kind != String && toks[0].Kind != BlockString) {
				t.Errorf("literal %q does not lex as one string token: %+v", tt.lit, toks)
			}
		})
	}
}

func TestStringValueErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		lit  string
		code ErrCode
	}{
		{"empty input", ``, ErrUnterminatedString},
		{"no quotes", `abc`, ErrUnterminatedString},
		{"unterminated", `"abc`, ErrUnterminatedString},
		{"single quote", `"`, ErrUnterminatedString},
		{"inner quote", `"a"b"`, ErrUnterminatedString},
		{"newline", "\"a\nb\"", ErrUnterminatedString},
		{"escaped closing quote", `"a\"`, ErrInvalidEscape},
		{"bad escape", `"\x"`, ErrInvalidEscape},
		{"short unicode", `"\u41"`, ErrInvalidEscape},
		{"lone surrogate", `"\uDE00"`, ErrInvalidEscape},
		{"braced out of range", `"\u{110000}"`, ErrInvalidEscape},
		{"invalid UTF-8", "\"\xff\"", ErrInvalidUTF8},
		{"block unterminated", `"""abc""`, ErrUnterminatedBlockString},
		{"block too short", `"""""`, ErrUnterminatedBlockString},
		{"block escaped close", `"""a\"""`, ErrUnterminatedBlockString},
		{"block escaped close then quote", `"""\""""`, ErrUnterminatedBlockString},
		{"block with inner close", `"""a"""b"""`, ErrUnterminatedBlockString},
		{"block invalid UTF-8", "\"\"\"\xff\"\"\"", ErrInvalidUTF8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := StringValue(tt.lit)
			var ve *ValueError
			if !errors.As(err, &ve) || ve.Code != tt.code {
				t.Errorf("StringValue(%q) = %q, %v; want error code %v", tt.lit, got, err, tt.code)
			}
			if err != nil && err.Error() == "" {
				t.Error("empty error message")
			}
		})
	}
}

// TestBlockStringValue follows the examples of the spec's BlockStringValue
// and graphql-js's dedentBlockStringLines tests.
func TestBlockStringValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, raw, want string
	}{
		{"empty", "", ""},
		{"only whitespace", "  \n\t\n  ", ""},
		{"single line keeps indentation", "   a  ", "   a  "},
		{"first line not dedented", "  a\n    b\n    c", "  a\nb\nc"},
		{"common indentation removed", "\n    a\n      b\n    c\n", "a\n  b\nc"},
		{"blank lines ignored for indentation", "\n    a\n\n  \n    b", "a\n\n\nb"},
		{"whitespace-only inner lines shortened", "\n    a\n  \n    b", "a\n\nb"},
		{"leading and trailing blank lines", "\n\n  \n  x\n \t\n\n", "x"},
		{"tabs count as whitespace", "\n\t\ta\n\tb", "\ta\nb"},
		{"CRLF and CR", "\r\n  a\r\n  b\r  c\r\n", "a\nb\nc"},
		{"escaped triple quote", `a \""" b`, `a """ b`},
		{"other escapes kept", `\n \" \\`, `\n \" \\`},
		{"no common indent", "a\nb", "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := BlockStringValue(tt.raw); got != tt.want {
				t.Errorf("BlockStringValue(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
