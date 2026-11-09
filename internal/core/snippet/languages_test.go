package snippet

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// trickyText has what each language quotes differently: shell and
// template syntax, every kind of quote, backslash runs, Unicode.
const trickyText = "100% ^caret^ &amp| <tag> (paren) !bang! %PATH% %% $env:PATH ${x} $(id) `tick` {{notAVar}} " +
	`'single' ''two'' "double" ""two"" \ \\ \" end\`

// trickyCases are requests built as Send builds them whose text is hard
// to quote, in every place a snippet writes text.
func trickyCases() []curlCase {
	return []curlCase{
		{name: "quoting_headers", req: with(httpReq("GET", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			r.Headers = []projectfile.KeyValue{
				kv("X-Odd!#$%&'*+.^_`|~", trickyText),
				kv("X-Unicode", "héllo ✓ 日本 😀 ‘curly’ “double” „low“ ‚s‛"),
				kv("X-Long", strings.Repeat("0123456789abcdef", 40)),
				kv("X-Json", `{"a":"b\"c","d":[1,2],"e":"C:\\dir\\"}`),
				kv("X-Tab", "a\tb"),
				kv("X-Empty", ""),
				kv("User-Agent", ""),
			}
		})},
		{name: "quoting_body", req: with(httpReq("POST", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawText, "trailing spaces   \n  indented\n\ttabbed\n"+trickyText+"\n"+
				`"""triple""" \u0041 \n */ // # -- ; C:\dir\`+"\n\n"+"‘curly’ “double” 日本 😀 \u00a0nbsp \u2028ls\n   ")
		})},
		{name: "quoting_lines", req: with(httpReq("PUT", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			// Printable text on several lines: a Java text block, a Go
			// raw string, a JavaScript template literal.
			r.Body = raw(projectfile.RawText, "trailing   \n\ttab first\n   \n\"\"\"q\"\"\"\" \\u0041 C:\\dir\\ ${x} %PATH% 'a'\n\n  last  ")
		})},
		{name: "quoting_json", req: with(httpReq("PATCH", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawJSON, "{\n  \"path\": \"C:\\\\dir\\\\\",\n  \"quote\": \"say \\\"hi\\\"\",\n"+
				"  \"escape\": \"\\u00e9\\n\",\n  \"template\": \"${x} `y` {{z}}\",\n  \"text\": \"héllo ✓\"\n}")
		})},
		{name: "quoting_form", req: with(httpReq("POST", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{
				kv("q", `a&b=c d+e%f "g" 'h' é`), kv("path", `C:\dir\`), kv("sym", "$x `y` ^z %PATH%"),
			}}
		})},
		{name: "quoting_multipart", req: with(httpReq("POST", "https://api.test/q"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyMultipart, Parts: []projectfile.Part{
				{Name: `we"ird name`, Value: "line1\nline2 \"q\" 'q' $x %PATH%"},
				{Name: "unicode ✓", Value: "héllo 😀", ContentType: "text/plain; charset=utf-8"},
				{Name: "a@b:c", Value: "@not-a-file"},
				{Name: "file", File: "odd name;1.txt", ContentType: "text/plain"},
			}}
		}), lossy: []string{"partTypes"}},
	}
}

// languageCases are the requests of the language goldens: the curl
// cases and the tricky ones.
func languageCases() []curlCase { return append(curlCases(), trickyCases()...) }

// modelCase is a request given as the model, for what a build cannot
// make on this machine (Windows paths) or at all (a NUL in a body).
type modelCase struct {
	name string
	r    Request
}

func modelCases() []modelCase {
	return []modelCase{
		{name: "windows_file", r: Request{Method: "PUT", URL: "https://api.test/blob",
			Body: Body{Kind: BodyFile, File: `C:\Users\Me\My Files\report 100%.json`, ContentType: "application/json"}}},
		{name: "windows_multipart", r: Request{Method: "POST", URL: "https://api.test/upload",
			Body: Body{Kind: BodyMultipart, Parts: []Part{
				{Name: "doc", File: `C:\tmp\a&b\up;load.txt`, Filename: "upload.txt", ContentType: "text/plain"},
				{Name: "dir", Value: `C:\tmp\`},
			}}}},
		{name: "nul_body", r: Request{Method: "POST", URL: "https://api.test/raw",
			Body: Body{Kind: BodyData, Data: []byte("a\x00b\x1bc"), ContentType: "application/octet-stream"}}},
		{name: "not_utf8", r: Request{Method: "POST", URL: "https://api.test/raw",
			Body: Body{Kind: BodyData, Data: []byte("a\xffb"), ContentType: "application/octet-stream"}}},
		{name: "duplicate_headers", r: Request{Method: "GET", URL: "https://api.test/me",
			Headers: []Header{{"Accept", "text/html"}, {"X-A", "1"}, {"accept", "application/json"}, {"Host", "example.test"},
				{"Cookie", "a=1"}, {"cookie", "b=2"}}}},
		{name: "templated_url", r: Request{Method: "DELETE", URL: "{{baseUrl}}/items/{{id}}",
			Headers: []Header{{"Authorization", "Bearer {{token}}"}}, Basic: nil,
			FollowRedirects: true, MaxRedirects: 3, Timeout: 250_000_000}},
		{name: "basic_unicode", r: Request{Method: "GET", URL: "http://api.test/me",
			Basic: &Credentials{Username: "jürgen", Password: "pässwörd ✓"}, HTTPVersion: "http2", Insecure: true}},
		{name: "digest_masked", r: Request{Method: "GET", URL: "https://api.test/me",
			Digest: &Credentials{Username: "ann\n// x", Password: Mask}}},
		{name: "empty_body", r: Request{Method: "POST", URL: "https://api.test/items",
			Body: Body{Kind: BodyData, Data: []byte{}, ContentType: "text/plain"}}},
	}
}

// requestOf is the model of c, masked when c has secrets.
func requestOf(t *testing.T, c curlCase) Request {
	t.Helper()
	r, err := FromBuild(buildCase(t, c), c.cookies)
	if err != nil {
		t.Fatalf("FromBuild: %v", err)
	}
	if c.secrets != nil {
		r, _ = r.Masked(c.secrets)
	}
	return r
}

// namedRequest is a request of the goldens and its secrets.
type namedRequest struct {
	name    string
	r       Request
	secrets []string
}

func goldenRequests(t *testing.T) []namedRequest {
	var out []namedRequest
	for _, c := range languageCases() {
		var secrets []string
		if c.secrets != nil {
			secrets = append(append(secrets, c.secrets...), c.auth.Password, "typed-token")
		}
		out = append(out, namedRequest{c.name, requestOf(t, c), secrets})
	}
	for _, c := range modelCases() {
		out = append(out, namedRequest{name: c.name, r: c.r})
	}
	return out
}

// snippetText is lang's code for r, or its error as "error <code>:
// <message>".
func snippetText(lang Language, r Request) string {
	code, err := lang.Generate(r)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return "error " + e.Code + ": " + e.Message
		}
		return "error: " + err.Error()
	}
	return code
}

// TestLanguagesGolden: every language's code for every request, in
// testdata/<language>/<name>.golden (an error as "error <code>: …");
// regenerate with go test ./internal/core/snippet -run TestLanguagesGolden
// -update. A masked request's code holds none of its secrets.
func TestLanguagesGolden(t *testing.T) {
	rt := root(t)
	reqs := goldenRequests(t)
	for _, lang := range Languages() {
		for _, c := range reqs {
			t.Run(lang.ID+"/"+c.name, func(t *testing.T) {
				got := strings.ReplaceAll(snippetText(lang, c.r), rt, "<root>") + "\n"
				// A carriage return and a NUL (in a raw HTTP body) as text:
				// git may turn CRLF into LF, and takes a file with a NUL
				// for binary.
				got = strings.NewReplacer("\r", "<CR>", "\x00", "<NUL>").Replace(got)
				for _, s := range c.secrets {
					if s != "" && strings.Contains(got, s) {
						t.Errorf("masked code holds %q:\n%s", s, got)
					}
				}
				path := filepath.Join("testdata", lang.ID, c.name+".golden")
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if got != string(want) {
					t.Errorf("code:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

// Every language writes every request Send can build, but for what it
// cannot carry by design: cmd.exe a carriage return, wget a multipart
// form.
func TestLanguagesWriteEveryCase(t *testing.T) {
	allowed := map[string][]string{
		"curlCmd": {"control_chars"},
		"wget":    {"multipart", "multipart_type_params", "quoting_multipart"},
	}
	for _, lang := range Languages() {
		for _, c := range languageCases() {
			if _, err := lang.Generate(requestOf(t, c)); err != nil && !slices.Contains(allowed[lang.ID], c.name) {
				t.Errorf("%s %s: %v", lang.ID, c.name, err)
			}
		}
	}
}

// TestCSharpURLKept: the C# code keeps a URL whose path or query .NET's
// Uri would change (as .NET 10 does) out of its canonicalization.
func TestCSharpURLKept(t *testing.T) {
	kept := []string{
		"https://api.test", "https://api.test/", "https://api.test/items?page=2&q=a%20b", "http://h/[x]?[y]",
		"http://h/a'!$&()*+,;=:@~-._", "http://h/A%3a%2F%2f%25", "http://h/%E2%9C%93", "http://h/a?x=../y",
		"http://h/.a/..b/...", "http://user:pw@h:8080/a;b/c;d", "{{base}}/users/{{id}}?q={{q}}", "https://api.test/me?key=" + Mask,
	}
	changed := []string{
		"https://api.test/a/../b/./c?f={x}&g=[1]|\"q\"", "http://h/a/.", "http://h/..", "http://h/%2e%2e/a", "http://h/a b",
		`http://h/a"b`, "http://h/a<b>", `http://h/a\b`, "http://h/a^b", "http://h/a`b", "http://h/a{b}", "http://h/a|b",
		"http://h/é", "http://h/a?b c", "http://h/%", "http://h/%zz", "http://h/%41", "http://h/%7e", "http://h/{{id}",
	}
	for _, u := range kept {
		if !csURLKept(u) {
			t.Errorf("csURLKept(%q) = false, want true", u)
		}
	}
	for _, u := range changed {
		if csURLKept(u) {
			t.Errorf("csURLKept(%q) = true, want false", u)
		}
	}
}

// The quoting of the Windows shells reads back as the text, with the curl
// importer's cmd and PowerShell tokenizers.
func TestQuoteWindows(t *testing.T) {
	for _, s := range []string{
		"", "abc", "https://a.test/x?y=1&z=2", "a b", `a"b`, `a\"b`, `a\\"b`, `C:\dir\`, `C:\dir\\`, `\\server\share`,
		"100%", "%PATH%", "a%b%c", "%%", "&|<>^()!", "line1\nline2", "a\n", "\n", "\n\n", "tab\there", "‘q’ “d” „l‚ ‛",
		"$env:X ${y} `t` $(id)", "'", "''", `"`, `""`, "é✓😀", "-rf", "@file", "a,b", "\u2028", "\u00a0", trickyText,
	} {
		q, err := quoteCmd(s, "value")
		if err != nil {
			t.Errorf("quoteCmd(%q): %v", s, err)
		} else if toks := curl.Tokenize("curl "+q, curl.DialectCmd); len(toks.Words) != 2 || toks.Words[1].Value != s {
			t.Errorf("quoteCmd(%q) = %s reads back as %+v", s, q, toks.Words)
		}
		q, err = quotePowerShell(s, "value")
		if err != nil {
			t.Errorf("quotePowerShell(%q): %v", s, err)
		} else if toks := curl.Tokenize("curl.exe "+q, curl.DialectPowerShell); len(toks.Words) != 2 || toks.Words[1].Value != s {
			t.Errorf("quotePowerShell(%q) = %s reads back as %+v", s, q, toks.Words)
		}
	}
	for _, tc := range []struct{ in, cmd, ps string }{
		{"abc", "abc", "abc"},
		{"a b", `"a b"`, "'a b'"},
		{`say "hi"`, `"say ""hi"""`, `'say "hi"'`},
		{`C:\dir\`, `C:\dir\`, `'C:\dir\'`},
		{`C:\my dir\`, `"C:\my dir\\"`, `'C:\my dir\'`},
		{`a\"b`, `"a\\""b"`, `'a\"b'`},
		{"50%", `"50"%""`, "'50%'"},
		{"a\nb", "\"a\"^\n\n\"b\"", "'a\nb'"},
		{"it's", `"it's"`, "'it''s'"},
		{"it’s $x", `"it’s $x"`, "\"it’s `$x\""},
		{"a\rb", "", "\"a`rb\""},
	} {
		if got, _ := quoteCmd(tc.in, "value"); tc.cmd != "" && got != tc.cmd {
			t.Errorf("quoteCmd(%q) = %s, want %s", tc.in, got, tc.cmd)
		}
		if got, _ := quotePowerShell(tc.in, "value"); got != tc.ps {
			t.Errorf("quotePowerShell(%q) = %s, want %s", tc.in, got, tc.ps)
		}
	}
	for _, s := range []string{"a\rb", "\x01", "\x7f", "\xff"} {
		if _, err := quoteCmd(s, "value"); codeOf(err) != CodeUnsupportedChar {
			t.Errorf("quoteCmd(%q): %v, want %s", s, err, CodeUnsupportedChar)
		}
	}
	for _, f := range []func(s, field string) (string, error){quoteCmd, quotePowerShell} {
		if _, err := f("a\x00b", "value"); codeOf(err) != CodeNulByte {
			t.Errorf("NUL: %v, want %s", err, CodeNulByte)
		}
	}
}

// TestWindowsCurlRoundTrip reads the cmd and PowerShell commands back with
// the cURL importer in that dialect and checks they send the same request,
// as TestCurlRoundTrip does for the POSIX command.
func TestWindowsCurlRoundTrip(t *testing.T) {
	for _, d := range []struct {
		id      string
		gen     func(Request) (string, error)
		dialect curl.Dialect
	}{{"curlCmd", CurlCmd, curl.DialectCmd}, {"curlPowerShell", CurlPowerShell, curl.DialectPowerShell}} {
		for _, c := range languageCases() {
			if slices.Contains(c.lossy, "masked") {
				continue
			}
			t.Run(d.id+"/"+c.name, func(t *testing.T) {
				want := requestOf(t, c)
				cmd, err := d.gen(want)
				if codeOf(err) == CodeUnsupportedChar {
					t.Skip(err)
				}
				if err != nil {
					t.Fatal(err)
				}
				imp := curl.ParseDialect(cmd, d.dialect).Import()
				for _, dg := range imp.Diagnostics {
					if dg.Severity == curl.SeverityError {
						t.Fatalf("import error %s: %s\n%s", dg.Code, dg.Message, cmd)
					}
				}
				f := imp.Request
				f.Settings.URLEncoding = projectfile.URLEncodingNone
				if f.Auth.Type == projectfile.AuthInherit {
					f.Auth.Type = projectfile.AuthNone
				}
				b, err := request.BuildHTTP(f, f.Auth, request.HTTPOptions{})
				if err != nil {
					t.Fatalf("build imported: %v\n%s", err, cmd)
				}
				got, err := FromBuild(b, nil)
				if err != nil {
					t.Fatalf("FromBuild imported: %v", err)
				}
				if g, w := canon(got, c.lossy), canon(want, c.lossy); g != w {
					t.Errorf("round trip differs\ncommand:\n%s\ngot:\n%s\nwant:\n%s", cmd, g, w)
				}
			})
		}
	}
}

// codeOf is the code of err, an *errs.Error; "" for another error or nil.
func codeOf(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
