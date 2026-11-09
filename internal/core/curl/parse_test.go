package curl

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestParseMethod(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src      string
		want     string
		explicit bool
	}{
		{"curl u", "GET", false},
		{"curl -d a u", "POST", false},
		{"curl -F a=b u", "POST", false},
		{"curl --json {} u", "POST", false},
		{"curl -G -d a u", "GET", false},
		{"curl -I u", "HEAD", false},
		{"curl --head --no-head u", "GET", false},
		{"curl -T f u", "PUT", false},
		{"curl -X patch -d a u", "patch", true},
		{"curl -XDELETE -XOPTIONS u", "OPTIONS", true},
		{"curl --request=PURGE u", "PURGE", true},
		{"curl -I -X GET u", "GET", true},
	}
	for _, tt := range tests {
		c := Parse(tt.src)
		if c.Method != tt.want || c.MethodExplicit != tt.explicit {
			t.Errorf("%s: method %q explicit %v, want %q %v", tt.src, c.Method, c.MethodExplicit, tt.want, tt.explicit)
		}
	}
}

func TestParseData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		src      string
		body     string
		complete bool
		parts    []string // kind name value file
		codes    []string
	}{
		{name: "data joined with &", src: `curl u -d a=1 --data b=2 --data-ascii c=3`, body: "a=1&b=2&c=3", complete: true,
			parts: []string{"data||a=1|", "data||b=2|", "data||c=3|"}},
		{name: "data file", src: `curl u -d @body.txt -d x`, body: "&x", complete: false,
			parts: []string{"data|||body.txt", "data||x|"}, codes: []string{CodeFileReference}},
		{name: "data stdin", src: `curl u --data-binary @-`, body: "", complete: false,
			parts: []string{"binary|||-"}, codes: []string{CodeFileReference}},
		{name: "raw keeps @", src: `curl u --data-raw @notafile`, body: "@notafile", complete: true,
			parts: []string{"raw||@notafile|"}},
		{name: "empty data", src: `curl u -d ''`, body: "", complete: true, parts: []string{"data|||"}},
		{name: "urlencode forms", src: `curl u --data-urlencode 'a b&c' --data-urlencode '=x=y' --data-urlencode 'n=v w' --data-urlencode 'n@f.txt' --data-urlencode 'e=m@il' --data-urlencode '@g.txt'`,
			body: "a%20b%26c&x%3Dy&n=v%20w&&e=m%40il&", complete: false,
			parts: []string{"urlencode||a b&c|", "urlencode||x=y|", "urlencode|n|v w|", "urlencode|n||f.txt", "urlencode|e|m@il|", "urlencode|||g.txt"},
			codes: []string{CodeFileReference, CodeFileReference}},
		{name: "urlencode unicode and reserved", src: `curl u --data-urlencode 'q=é/~_.-*'`, body: "q=%C3%A9%2F~_.-%2A", complete: true,
			parts: []string{"urlencode|q|é/~_.-*|"}},
		{name: "json concatenates", src: `curl u --json '{"a":' --json '1}' -d x --json '[]'`, body: `{"a":1}&x[]`, complete: true,
			parts: []string{`json||{"a":|`, "json||1}|", "data||x|", "json||[]|"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Parse(tt.src)
			body, complete := c.Body()
			if body != tt.body || complete != tt.complete {
				t.Errorf("Body() = %q, %v; want %q, %v", body, complete, tt.body, tt.complete)
			}
			parts := []string{}
			for _, d := range c.Data {
				parts = append(parts, fmt.Sprintf("%s|%s|%s|%s", d.Kind, d.Name, d.Value, d.File))
			}
			if fmt.Sprint(parts) != fmt.Sprint(tt.parts) {
				t.Errorf("parts:\n got %q\nwant %q", parts, tt.parts)
			}
			if g, w := fmt.Sprint(allCodes(c.Diagnostics)), fmt.Sprint(orEmpty(tt.codes)); g != w {
				t.Errorf("codes = %s, want %s", g, w)
			}
		})
	}
}

func TestParseURLQuery(t *testing.T) {
	t.Parallel()
	c := Parse(`curl --url-query 'q=a b' --url-query '+raw=a+b' --url-query 'x@f' https://example.com`)
	got := []string{}
	for _, d := range c.URLQuery {
		got = append(got, fmt.Sprintf("%s|%s|%s", d.Kind, d.Encoded(), d.File))
	}
	want := []string{"urlencode|q=a%20b|", "rawquery|raw=a+b|", "urlencode||f"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("URLQuery = %q, want %q", got, want)
	}
}

func TestParseForm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arg   string
		want  string // kind|name|value|file|type|filename|headers
		codes []string
	}{
		{`a=b`, "text|a|b||||[]", nil},
		{`a=`, "text|a|||||[]", nil},
		{`a="x;y\"z";type=text/plain`, `text|a|x;y"z||text/plain||[]`, nil},
		{`a=x;y`, "text|a|x||||[]", []string{CodeInvalidValue}},
		{`f=@/tmp/a b.png;type=image/png;filename="c;d.png";headers="X-A: 1"`, "file|f||/tmp/a b.png|image/png|c;d.png|[X-A: 1]", []string{CodeFileReference}},
		{`f=@-`, "file|f||-|||[]", []string{CodeFileReference}},
		{`f=<in.txt;headers=@h.txt`, "file_content|f||in.txt|||[]", []string{CodeFileReference, CodeFileReference}},
		{`f=@x;encoder=base64`, "file|f||x|||[]", []string{CodeFileReference, CodeInvalidValue}},
		{`f=@x; type=a/b ;type`, "file|f||x|a/b ||[]", []string{CodeFileReference, CodeInvalidValue}},
		{`noequals`, "", []string{CodeInvalidValue}},
	}
	for _, tt := range tests {
		c := Parse("curl u -F '" + escapeSingle(tt.arg) + "'")
		got := ""
		if len(c.Form) == 1 {
			f := c.Form[0]
			got = fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", f.Kind, f.Name, f.Value, f.File, f.ContentType, f.Filename, orEmpty(f.Headers))
		}
		if got != tt.want {
			t.Errorf("-F %s:\n got %q\nwant %q", tt.arg, got, tt.want)
		}
		if g, w := fmt.Sprint(allCodes(c.Diagnostics)), fmt.Sprint(orEmpty(tt.codes)); g != w {
			t.Errorf("-F %s: codes = %s, want %s", tt.arg, g, w)
		}
	}

	c := Parse(`curl u --form-string 'a=@b;type=c'`)
	if len(c.Form) != 1 || c.Form[0].Kind != FormText || c.Form[0].Value != "@b;type=c" || len(c.Diagnostics) != 0 {
		t.Errorf("--form-string: %+v %+v", c.Form, c.Diagnostics)
	}
}

func escapeSingle(s string) string {
	out := ""
	for _, r := range s {
		if r == '\'' {
			out += `'"'"'`
		} else {
			out += string(r)
		}
	}
	return out
}

func TestParseOptions(t *testing.T) {
	t.Parallel()
	c := Parse(`curl -A '' -e ';auto' -b a=1 -b 'b=2; c=3' -b jar.txt -u :x --anyauth --oauth2-bearer tok ` +
		`--http1.1 --http3-only --max-redirs -1 -L --no-location -kk --no-insecure --compressed ` +
		`-x socks5h://p:1080 -U onlyuser -- -dash-url`)
	if c.UserAgent == nil || *c.UserAgent != "" {
		t.Errorf("UserAgent = %v, want empty", c.UserAgent)
	}
	if c.Referer == nil || *c.Referer != "" {
		t.Errorf("Referer = %v, want empty", c.Referer)
	}
	if fmt.Sprint(c.Cookies) != "[a=1 b=2; c=3]" {
		t.Errorf("Cookies = %q", c.Cookies)
	}
	if c.User == nil || c.User.User != "" || c.User.Password != "x" || c.AuthScheme != "any" || c.OAuth2Bearer != "tok" {
		t.Errorf("auth: %+v %q %q", c.User, c.AuthScheme, c.OAuth2Bearer)
	}
	if c.HTTPVersion != "3-only" || c.MaxRedirects == nil || *c.MaxRedirects != -1 {
		t.Errorf("HTTPVersion %q MaxRedirects %v", c.HTTPVersion, c.MaxRedirects)
	}
	if c.FollowRedirects || c.Insecure || !c.Compressed {
		t.Errorf("flags: follow %v insecure %v compressed %v", c.FollowRedirects, c.Insecure, c.Compressed)
	}
	if c.Proxy != "socks5h://p:1080" || c.ProxyUser == nil || c.ProxyUser.HasPassword {
		t.Errorf("proxy: %q %+v", c.Proxy, c.ProxyUser)
	}
	if c.URL != "-dash-url" {
		t.Errorf("URL = %q", c.URL)
	}
	if g, w := fmt.Sprint(allCodes(c.Diagnostics)), fmt.Sprint([]string{CodeFileReference, CodePasswordMissing}); g != w {
		t.Errorf("codes = %s, want %s", g, w)
	}
}

func TestParseProblems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src   string
		codes []string
		text  []string // source text of each diagnostic
	}{
		{"", []string{CodeEmpty}, []string{""}},
		{"http https://x", []string{CodeNotCurl}, []string{"http"}},
		{"/usr/bin/curl https://x", nil, nil},
		{`'C:\bin\CURL.EXE' https://x`, nil, nil},
		{"curl https://x -m soon --connect-timeout -1 --max-redirs lots", []string{CodeInvalidValue, CodeInvalidValue, CodeInvalidValue},
			[]string{"-m soon", "--connect-timeout -1", "--max-redirs lots"}},
		{"curl https://x --compressed=yes", []string{CodeUnexpectedValue}, []string{"--compressed=yes"}},
		{"curl https://x -d a -F b=c", []string{CodeConflictingBody}, []string{"curl https://x -d a -F b=c"}},
		{"curl https://x -G -d a -I", nil, nil},
		{"curl https://x --next https://y -d z", []string{CodeUnsupportedOption}, []string{"--next https://y -d z"}},
		{"curl https://x -K cfg", []string{CodeUnsupportedOption, CodeFileReference}, []string{"-K cfg", "-K cfg"}},
		{"curl https://x --resolve x:443:127.0.0.1", []string{CodeUnsupportedOption}, []string{"--resolve x:443:127.0.0.1"}},
		{"curl --unknown example.com", []string{CodeUnknownOption}, []string{"--unknown"}},
		{"curl --unknown -k x", []string{CodeUnknownOption}, []string{"--unknown"}},
		{"curl x --unknown", []string{CodeUnknownOption}, []string{"--unknown"}},
		{"curl x -é", []string{CodeUnknownOption}, []string{"-é"}},
		{"curl x -kéL", []string{CodeUnknownOption}, []string{"-kéL"}},
		{"curl x --data", []string{CodeMissingValue}, []string{"--data"}},
		{"curl x -sd", []string{CodeIgnoredOption, CodeMissingValue}, []string{"-sd", "-sd"}},
	}
	for _, tt := range tests {
		c := Parse(tt.src)
		if g, w := fmt.Sprint(allCodes(c.Diagnostics)), fmt.Sprint(orEmpty(tt.codes)); g != w {
			t.Errorf("%q: codes = %s, want %s", tt.src, g, w)
			continue
		}
		if tt.text != nil {
			for i, d := range c.Diagnostics {
				if got := tt.src[d.Start:d.End]; got != tt.text[i] {
					t.Errorf("%q: diagnostic %d covers %q, want %q", tt.src, i, got, tt.text[i])
				}
			}
		}
	}
}

func TestCommandErr(t *testing.T) {
	t.Parallel()
	if err := Parse("curl https://x --bogus").Err(); err != nil {
		t.Errorf("warnings only: Err() = %v", err)
	}
	err := Parse(`curl 'https://x`).Err()
	if err == nil {
		t.Fatal("Err() = nil for an unterminated quote")
	}
	if errs.KindOf(err) != errs.Parse {
		t.Errorf("kind = %s", errs.KindOf(err))
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidCommand || e.Details["diagnostic"] != CodeUnterminatedQuote ||
		e.Details["start"] != "5" || e.Hint == "" {
		t.Errorf("err = %+v", e)
	}
}

func TestEscape(t *testing.T) {
	t.Parallel()
	if got, want := Escape("aZ09-._~ !\"#$%&'()*+,/:;<=>?@[\\]^`{|}\x00\xff"), "aZ09-._~%20%21%22%23%24%25%26%27%28%29%2A%2B%2C%2F%3A%3B%3C%3D%3E%3F%40%5B%5C%5D%5E%60%7B%7C%7D%00%FF"; got != want {
		t.Errorf("Escape = %q, want %q", got, want)
	}
}
