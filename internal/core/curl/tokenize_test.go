package curl

import (
	"fmt"
	"testing"
)

func TestTokenize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dialect Dialect
		src     string
		want    []string
		codes   []string // diagnostic codes in order
		end     int      // Tokens.End; 0 for len(src)
	}{
		// POSIX quoting.
		{name: "plain words", src: "curl  a\tb", want: []string{"curl", "a", "b"}},
		{name: "single quotes keep everything", src: `curl 'a "b" \n $x'`, want: []string{"curl", `a "b" \n $x`}},
		{name: "adjacent quoting joins", src: `curl a'b'"c"d`, want: []string{"curl", "abcd"}},
		{name: "empty quotes are a word", src: `curl '' ""`, want: []string{"curl", "", ""}},
		{name: "quote trick", src: `curl 'it'"'"'s'`, want: []string{"curl", "it's"}},
		{name: "double quote escapes", src: `curl "a\"b\\c\$d\` + "`" + `e\x"`, want: []string{"curl", `a"b\c$d` + "`" + `e\x`}},
		{name: "backslash outside quotes", src: `curl a\ b \'c\"`, want: []string{"curl", "a b", `'c"`}},
		{name: "continuation", src: "curl \\\n  -k \\\r\n  url", want: []string{"curl", "-k", "url"}},
		{name: "continuation inside a word", src: "curl ab\\\ncd", want: []string{"curl", "abcd"}},
		{name: "continuation in double quotes", src: "curl \"ab\\\ncd\"", want: []string{"curl", "abcd"}},
		{name: "newline in quotes kept, CRLF normalised", src: "curl 'a\r\nb' \"c\r\nd\"", want: []string{"curl", "a\nb", "c\nd"}},
		{name: "trailing backslash", src: `curl a\`, want: []string{"curl", `a\`}},
		{name: "comment", src: "curl a # comment 'x\n", want: []string{"curl", "a"}},
		{name: "hash inside a word", src: "curl a#b", want: []string{"curl", "a#b"}},
		{name: "locale quotes", src: `curl $"a b"`, want: []string{"curl", "a b"}},

		// ANSI-C quoting.
		{name: "ansi-c simple escapes", src: `curl $'a\nb\tc\\d\'e\"f\?g\a\b\e\E\f\r\v'`, want: []string{"curl", "a\nb\tc\\d'e\"f?g\a\b\x1b\x1b\f\r\v"}},
		{name: "ansi-c hex and octal", src: `curl $'\x41\x4a\x7\101\0\1234\xZ'`, want: []string{"curl", "AJ\aA\x00S4\\xZ"}},
		{name: "ansi-c unicode", src: `curl $'\u00e9\u2615\U0001F600\u{1F600}\u{41}'`, want: []string{"curl", "é☕😀😀A"}},
		{name: "ansi-c surrogate pair", src: `curl $'\uD83D\uDE00|\uD83Dx|\uDE00'`, want: []string{"curl", "😀|\uFFFDx|\uFFFD"}},
		{name: "ansi-c bad unicode kept", src: `curl $'\uZZ\u{}\u{110000}'`, want: []string{"curl", `\uZZ\u{}` + "\uFFFD"}},
		{name: "ansi-c control", src: `curl $'\cA\c['`, want: []string{"curl", "\x01\x1b"}},
		{name: "ansi-c unknown escape kept", src: `curl $'\q\1'`, want: []string{"curl", "\\q\x01"}},
		{name: "ansi-c joined", src: `curl -H$'a\n'b`, want: []string{"curl", "-Ha\nb"}},

		// Shell syntax that is reported.
		{name: "variables", src: `curl $HOME "${X}y" "$(id)" $1 $ "a$"`, want: []string{"curl", "$HOME", "${X}y", "$(id)", "$1", "$", "a$"},
			codes: []string{CodeShellExpansion, CodeShellExpansion, CodeShellExpansion, CodeShellExpansion}},
		{name: "backticks", src: "curl `cat t` \"`x`\"", want: []string{"curl", "`cat", "t`", "`x`"},
			codes: []string{CodeShellExpansion, CodeShellExpansion}},
		{name: "glob", src: `curl http://x/?a=1 '*' b*[c]`, want: []string{"curl", "http://x/?a=1", "*", "b*[c]"},
			codes: []string{CodeShellGlob, CodeShellGlob}},
		{name: "pipe", src: "curl a | jq .", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "semicolon", src: "curl a; echo", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 6},
		{name: "and", src: "curl a && echo", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "background", src: "curl a &", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "ampersand in a word", src: "curl http://x/?a=1&b=2", want: []string{"curl", "http://x/?a=1&b=2"},
			codes: []string{CodeShellGlob, CodeUnquotedAmpersand}},
		{name: "redirections", src: "curl a >out 2>>err 1>&2 &>all <in b", want: []string{"curl", "a", "b"},
			codes: []string{CodeRedirectIgnored, CodeRedirectIgnored, CodeRedirectIgnored, CodeRedirectIgnored, CodeRedirectIgnored}},
		{name: "redirection at end", src: "curl a >", want: []string{"curl", "a"}, codes: []string{CodeRedirectIgnored}},
		{name: "digits word before redirect in quotes", src: `curl '2'>x`, want: []string{"curl", "2"}, codes: []string{CodeRedirectIgnored}},
		{name: "heredoc", src: "curl a <<EOF\nbody\nEOF", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "second command", src: "curl a\ncurl b\n", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "missing continuation", src: "curl a\n  -k\n\n", want: []string{"curl", "a", "-k"}, codes: []string{CodeMissingContinuation}},
		{name: "unterminated single", src: "curl 'abc\r\ndef", want: []string{"curl", "abc\ndef"}, codes: []string{CodeUnterminatedQuote}},
		{name: "unterminated double", src: `curl "abc`, want: []string{"curl", "abc"}, codes: []string{CodeUnterminatedQuote}},
		{name: "unterminated ansi-c", src: `curl $'abc\`, want: []string{"curl", `abc\`}, codes: []string{CodeUnterminatedQuote}},

		// Prompts and leading text.
		{name: "dollar prompt", src: "\n  $ curl a", want: []string{"curl", "a"}},
		{name: "percent prompt", src: "% curl a", want: []string{"curl", "a"}},
		{name: "powershell prompt", src: "PS C:\\> curl.exe a", want: []string{"curl.exe", "a"}},
		{name: "cmd prompt", src: "C:\\Users\\ada>curl a", want: []string{"curl", "a"}},
		{name: "bom", src: "\uFEFFcurl a", want: []string{"curl", "a"}},
		{name: "empty", src: " \n\t", want: nil},

		// cmd.exe.
		{name: "cmd carets", dialect: DialectCmd, src: `curl a^&b ^"c d^" "e ^& f"`, want: []string{"curl", "a&b", "c d", "e ^& f"}},
		{name: "cmd continuation", dialect: DialectCmd, src: "curl a ^\r\n  -k ^\n  b", want: []string{"curl", "a", "-k", "b"}},
		{name: "cmd newline in value", dialect: DialectCmd, src: "curl ^\"a^\n\nb^\"", want: []string{"curl", "a\nb"}},
		{name: "cmd crt backslashes", dialect: DialectCmd, src: `curl "a\\\"b" "c\\" d\e "f""g" \\\\"h"`, want: []string{"curl", `a\"b`, `c\`, `d\e`, `f"g`, `\\h`}},
		{name: "cmd chrome halves backslashes", dialect: DialectCmd, src: `curl ^"C:^\^\tmp^\^\^\^\x^" ^"a\b^"`, want: []string{"curl", `C:\tmp\\x`, `a\b`}},
		{name: "cmd operators", dialect: DialectCmd, src: "curl a | more", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "cmd redirect", dialect: DialectCmd, src: "curl a > x", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "cmd variable", dialect: DialectCmd, src: `curl -u %USER%:%PASS% ^"%^HOME%^"`, want: []string{"curl", "-u", "%USER%:%PASS%", "%HOME%"},
			codes: []string{CodeShellExpansion, CodeShellExpansion}},
		{name: "cmd unterminated", dialect: DialectCmd, src: `curl "abc`, want: []string{"curl", "abc"}, codes: []string{CodeUnterminatedQuote}},
		{name: "cmd trailing caret", dialect: DialectCmd, src: `curl a^`, want: []string{"curl", "a"}},
		{name: "cmd second line", dialect: DialectCmd, src: "curl a\r\ndir", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 8},

		// PowerShell.
		{name: "ps quoting", dialect: DialectPowerShell, src: `curl.exe 'it''s' "say ""hi""" "a` + "`" + `"b` + "`" + `tc" a` + "`" + ` b`, want: []string{"curl.exe", "it's", `say "hi"`, "a\"b\tc", "a b"}},
		{name: "ps continuation", dialect: DialectPowerShell, src: "curl.exe a `\r\n  -k `\n  b", want: []string{"curl.exe", "a", "-k", "b"}},
		{name: "ps unicode escape", dialect: DialectPowerShell, src: "curl \"`u{e9}`u{zz}`0\"", want: []string{"curl", "é" + "u{zz}\x00"}},
		{name: "ps variables", dialect: DialectPowerShell, src: `curl "Bearer $env:TOKEN" '$x'`, want: []string{"curl", "Bearer $env:TOKEN", "$x"}, codes: []string{CodeShellExpansion}},
		{name: "ps pipe", dialect: DialectPowerShell, src: "curl a | ConvertFrom-Json", want: []string{"curl", "a"}, codes: []string{CodeTrailingInput}, end: 7},
		{name: "ps unterminated", dialect: DialectPowerShell, src: `curl 'a`, want: []string{"curl", "a"}, codes: []string{CodeUnterminatedQuote}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := tt.dialect
			if d == DialectAuto {
				d = DialectPOSIX
			}
			got := Tokenize(tt.src, d)
			var words []string
			for _, w := range got.Words {
				words = append(words, w.Value)
				if w.Start < 0 || w.End <= w.Start || w.End > len(tt.src) {
					t.Errorf("word %q has range [%d, %d)", w.Value, w.Start, w.End)
				}
			}
			if fmt.Sprintf("%q", words) != fmt.Sprintf("%q", tt.want) {
				t.Errorf("words:\n got %q\nwant %q", words, tt.want)
			}
			if g, w := fmt.Sprint(allCodes(got.Diagnostics)), fmt.Sprint(orEmpty(tt.codes)); g != w {
				t.Errorf("codes:\n got %s\nwant %s\n%+v", g, w, got.Diagnostics)
			}
			wantEnd := tt.end
			if wantEnd == 0 {
				wantEnd = len(tt.src)
			}
			if got.End != wantEnd {
				t.Errorf("End = %d, want %d", got.End, wantEnd)
			}
		})
	}
}

// allCodes lists every diagnostic code in order.
func allCodes(ds []Diagnostic) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

func TestTokenRanges(t *testing.T) {
	t.Parallel()
	src := `curl  'a b'"c" \` + "\n" + `  $'d' e\ f`
	got := Tokenize(src, DialectPOSIX)
	want := []string{"curl", `'a b'"c"`, `$'d'`, `e\ f`}
	if len(got.Words) != len(want) {
		t.Fatalf("words = %+v", got.Words)
	}
	for i, w := range got.Words {
		if text := src[w.Start:w.End]; text != want[i] {
			t.Errorf("word %d source text = %q, want %q", i, text, want[i])
		}
	}

	cmd := "curl ^\"a b^\" ^\n  \"c\"\"d\""
	got = Tokenize(cmd, DialectCmd)
	want = []string{"curl", `^"a b^"`, `"c""d"`}
	if len(got.Words) != len(want) {
		t.Fatalf("cmd words = %+v", got.Words)
	}
	for i, w := range got.Words {
		if text := cmd[w.Start:w.End]; text != want[i] {
			t.Errorf("cmd word %d source text = %q, want %q", i, text, want[i])
		}
	}
}

func TestDetectDialect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  string
		want Dialect
	}{
		{"curl https://example.com", DialectPOSIX},
		{"curl 'a' \\\n -k", DialectPOSIX},
		{"curl ^\"https://example.com^\"", DialectCmd},
		{"curl https://example.com ^\r\n  -k", DialectCmd},
		{"curl.exe https://example.com `\r\n  -k", DialectPowerShell},
		{"curl.exe https://example.com `  \n  -k", DialectPowerShell},
	}
	for _, tt := range tests {
		if got := DetectDialect(tt.src); got != tt.want {
			t.Errorf("DetectDialect(%q) = %q, want %q", tt.src, got, tt.want)
		}
		if got := Tokenize(tt.src, DialectAuto).Dialect; got != tt.want {
			t.Errorf("Tokenize(%q).Dialect = %q, want %q", tt.src, got, tt.want)
		}
	}
}
