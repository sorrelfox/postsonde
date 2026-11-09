package curl

import (
	"testing"
)

// FuzzParse checks the invariants of the tokenizer and parser on arbitrary
// input in every dialect: no panics, words in order without overlap, every
// range inside the source, diagnostics sorted (Import's too), and End
// within the text.
func FuzzParse(f *testing.F) {
	for _, tt := range realWorld {
		f.Add(tt.src)
	}
	for _, seed := range []string{
		"", "curl", `curl $'\u{1F600}\uD83D\x4\101\c'`, "curl ^\"a^\n\nb^\" ^", "curl.exe `\r\n -k `u{41}",
		"curl a 2>&1 | b", `curl "a\\\"b" \\\\"c`, "curl -F 'a=@\"x;y\";type=' -d@- --json", "curl 'x",
		"\xff\xfe curl \x00", "PS C:\\> curl ^", "curl -HX: -H'X;' -- -",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		for _, d := range []Dialect{DialectAuto, DialectPOSIX, DialectCmd, DialectPowerShell} {
			toks := Tokenize(src, d)
			if toks.End < 0 || toks.End > len(src) {
				t.Fatalf("%s: End %d outside [0, %d]", d, toks.End, len(src))
			}
			prev := 0
			for i, w := range toks.Words {
				if w.Start < prev || w.End <= w.Start || w.End > len(src) {
					t.Fatalf("%s: word %d %+v out of order or range (prev end %d, len %d)", d, i, w, prev, len(src))
				}
				prev = w.End
			}
			checkDiagnostics(t, string(d), src, toks.Diagnostics)

			c := ParseDialect(src, d)
			checkDiagnostics(t, string(d), src, c.Diagnostics)
			if (c.Err() != nil) != HasErrors(c.Diagnostics) {
				t.Fatalf("%s: Err and HasErrors disagree", d)
			}
			for _, h := range c.Headers {
				if h.Start < 0 || h.End < h.Start || h.End > len(src) {
					t.Fatalf("%s: header range %+v", d, h)
				}
			}
			checkDiagnostics(t, string(d)+" import", src, c.Import().Diagnostics)
		}
	})
}

func checkDiagnostics(t *testing.T, dialect, src string, ds []Diagnostic) {
	t.Helper()
	for i, d := range ds {
		if d.Start < 0 || d.End < d.Start || d.End > len(src) {
			t.Fatalf("%s: diagnostic %+v outside [0, %d]", dialect, d, len(src))
		}
		if d.Code == "" || d.Message == "" || d.Severity == "" {
			t.Fatalf("%s: incomplete diagnostic %+v", dialect, d)
		}
		if i > 0 && ds[i-1].Start > d.Start {
			t.Fatalf("%s: diagnostics not sorted: %+v", dialect, ds)
		}
	}
}
