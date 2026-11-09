package curl

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestImportRealWorld maps every real-world command (realworld_test.go) to
// a request, checks that the ones without errors build as an HTTP request,
// and compares the request and diagnostics with a golden file
// (testdata/import/, -update to rewrite).
func TestImportRealWorld(t *testing.T) {
	for _, tt := range realWorld {
		t.Run(tt.name, func(t *testing.T) {
			im := Import(tt.src)
			for _, d := range im.Diagnostics {
				if d.Start < 0 || d.End < d.Start || d.End > len(tt.src) {
					t.Errorf("diagnostic %s range [%d,%d) outside the text", d.Code, d.Start, d.End)
				}
			}
			if !HasErrors(im.Diagnostics) {
				r := im.Request
				r.ID, r.Name = "req_test", im.Name
				// Files are not there: check the rest of the request.
				if r.Body.Mode == projectfile.BodyFile || slices.ContainsFunc(r.Body.Parts, func(p projectfile.Part) bool { return p.File != "" }) {
					r.Body = projectfile.Body{Mode: projectfile.BodyNone}
				}
				auth := r.Auth
				if auth.Type == projectfile.AuthInherit {
					auth = projectfile.Auth{Type: projectfile.AuthNone}
				}
				if _, err := request.BuildHTTP(r, auth, request.HTTPOptions{}); err != nil {
					t.Errorf("BuildHTTP: %v", err)
				}
				if _, err := projectfile.Marshal(&r); err != nil {
					t.Errorf("Marshal: %v", err)
				}
			}
			out, err := json.MarshalIndent(struct {
				Name        string                  `json:"name"`
				Request     projectfile.HTTPRequest `json:"request"`
				Diagnostics []Diagnostic            `json:"diagnostics"`
			}{im.Name, im.Request, im.Diagnostics}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "import/"+tt.name, append(out, '\n'))
		})
	}
}

func rows(kvs []projectfile.KeyValue) []string {
	out := []string{}
	for _, kv := range kvs {
		out = append(out, kv.Name+"="+kv.Value)
	}
	return out
}

func TestImport(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		method  string
		url     string
		params  []string
		headers []string
		body    string // mode:type:text, or mode:fields, parts, path
		auth    string
		setting string
		proxy   string
		codes   []string // Import's own codes, in order
	}{
		{
			name:    "get with scheme added",
			src:     `curl example.com/a/b?x=1&y`,
			method:  "GET",
			url:     "http://example.com/a/b?x=1&y",
			params:  []string{"x=1", "y="},
			body:    "none",
			codes:   []string{CodeSchemeAdded},
			headers: []string{},
		},
		{
			name:    "json data with content type",
			src:     `curl -X PUT https://api.test/items/1 -H 'Content-Type: application/json' -d '{"a":1}'`,
			method:  "PUT",
			url:     "https://api.test/items/1",
			headers: []string{"Content-Type=application/json"},
			body:    `raw:json:{"a":1}`,
		},
		{
			name:    "json data without content type",
			src:     `curl https://api.test/items -d '{"a":1}'`,
			method:  "POST",
			url:     "https://api.test/items",
			headers: []string{},
			body:    `raw:json:{"a":1}`,
			codes:   []string{CodeBodyType},
		},
		{
			name:    "--json",
			src:     `curl --json '{"a":1}' https://api.test/items`,
			method:  "POST",
			url:     "https://api.test/items",
			headers: []string{"Accept=application/json"},
			body:    `raw:json:{"a":1}`,
		},
		{
			name:    "form fields decoded",
			src:     `curl https://api.test/login -d 'user=a%40b.c&pass=x+y' --data-urlencode 'note=hi there'`,
			method:  "POST",
			url:     "https://api.test/login",
			headers: []string{},
			body:    "urlencoded:user=a@b.c,pass=x y,note=hi there",
		},
		{
			name:    "text data without content type keeps curl's",
			src:     `curl https://api.test/raw -d 'hello world'`,
			method:  "POST",
			url:     "https://api.test/raw",
			headers: []string{"Content-Type=application/x-www-form-urlencoded"},
			body:    "raw:text:hello world",
		},
		{
			name:    "xml",
			src:     `curl https://api.test/x -H 'content-type: text/xml; charset=utf-8' --data-binary '<a/>'`,
			method:  "POST",
			url:     "https://api.test/x",
			headers: []string{"content-type=text/xml; charset=utf-8"},
			body:    "raw:xml:<a/>",
		},
		{
			name:    "get with data",
			src:     `curl -G https://api.test/search?q=1 -d a=1 --data-urlencode 'b=x y' --url-query 'c=2'`,
			method:  "GET",
			url:     "https://api.test/search?q=1&c=2&a=1&b=x%20y",
			params:  []string{"q=1", "c=2", "a=1", "b=x%20y"},
			headers: []string{},
			body:    "none",
		},
		{
			name:    "multipart",
			src:     `curl https://api.test/up -H 'Content-Type: multipart/form-data' -F 'meta={};type=application/json' -F file=@/tmp/a.png -F rel=@b.txt -F 'c=<c.txt' -F 'd=@/e.bin;filename=x.bin'`,
			method:  "POST",
			url:     "https://api.test/up",
			headers: []string{},
			body:    "multipart:meta={} type=application/json,file=@/tmp/a.png,rel=@b.txt,c=@c.txt,d=@/e.bin",
			codes:   []string{CodeNotImported, CodeFileNotRead, CodeFileNotRead, CodeNotImported},
		},
		{
			name:    "single file body",
			src:     `curl https://api.test/up --data-binary @/tmp/body.json -H 'Content-Type: application/json'`,
			method:  "POST",
			url:     "https://api.test/up",
			headers: []string{"Content-Type=application/json"},
			body:    "file:/tmp/body.json",
		},
		{
			name:    "-d @file",
			src:     `curl https://api.test/up -d @body.txt`,
			method:  "POST",
			url:     "https://api.test/up",
			headers: []string{},
			body:    "file:body.txt ct=application/x-www-form-urlencoded",
			codes:   []string{CodeFileNotRead},
		},
		{
			name:    "file part of data missing",
			src:     `curl https://api.test/up -d a=1 -d @rest.txt`,
			method:  "POST",
			url:     "https://api.test/up",
			headers: []string{},
			body:    "urlencoded:a=1",
			codes:   []string{CodeFileNotRead},
		},
		{
			name:    "upload file",
			src:     `curl -T /tmp/x.bin https://api.test/put`,
			method:  "PUT",
			url:     "https://api.test/put",
			headers: []string{},
			body:    "file:/tmp/x.bin",
		},
		{
			name:    "basic, cookies, agent, referer",
			src:     `curl -u 'key:' -b 'a=1; b=2' -b c=3 -A 'agent/1' -e 'https://ref/;auto' https://api.test/me`,
			method:  "GET",
			url:     "https://api.test/me",
			headers: []string{"User-Agent=agent/1", "Referer=https://ref/", "Cookie=a=1; b=2; c=3"},
			body:    "none",
			auth:    "basic key:",
		},
		{
			name:    "digest",
			src:     `curl --digest -u a:b https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			auth:    "digest a:b",
		},
		{
			name:    "digest wins over bearer",
			src:     `curl --digest -u a:b --oauth2-bearer tok https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			auth:    "digest a:b",
			codes:   []string{CodeNotImported},
		},
		{
			name:    "ntlm not imported",
			src:     `curl --ntlm -u a:b https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			auth:    "inherit",
			codes:   []string{CodeNotImported},
		},
		{
			name:    "bearer",
			src:     `curl --oauth2-bearer tok https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			auth:    "bearer tok",
		},
		{
			name:    "settings",
			src:     `curl -k -L --max-redirs 3 -m 1.5 --connect-timeout 2 --http2 --compressed -x http://proxy:1 https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			setting: "verifyTls=false followRedirects=true maxRedirects=3 timeoutMs=1500",
			codes:   []string{CodeNotImported, CodeNotImported, CodeNotImported},
			proxy:   "custom http://proxy:1",
		},
		{
			name:    "socks5 proxy with credentials",
			src:     `curl -x socks5h://proxy.lan:1080 -U 'bob:p@ss' https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			proxy:   "custom socks5h://bob@proxy.lan:1080 password=p@ss",
		},
		{
			name:    "proxy without a scheme replaces the URL's user",
			src:     `curl --proxy carl@proxy.lan:3128 --proxy-user 'ann:pw' https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			proxy:   "custom http://ann@proxy.lan:3128 password=pw",
		},
		{
			name:    "noproxy star",
			src:     `curl --noproxy '*' https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			proxy:   "none",
		},
		{
			name:    "empty proxy",
			src:     `curl -x '' https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			proxy:   "none",
		},
		{
			name:    "noproxy list and a proxy user alone",
			src:     `curl --noproxy example.com -U bob:pw https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			codes:   []string{CodeNotImported, CodeNotImported},
		},
		{
			name:    "unusable proxy",
			src:     `curl -x ftp://proxy.lan https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			codes:   []string{CodeNotImported},
		},
		{
			name:    "http1.1",
			src:     `curl --http1.1 https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			setting: "httpVersion=http1",
		},
		{
			name:    "http2 prior knowledge",
			src:     `curl --http2-prior-knowledge http://api.test/`,
			method:  "GET",
			url:     "http://api.test/",
			headers: []string{},
			body:    "none",
			setting: "httpVersion=http2",
		},
		{
			name:    "location-trusted",
			src:     `curl --location-trusted -u a:b https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			auth:    "basic a:b",
			setting: "followRedirects=true forwardAuthorization=true",
		},
		{
			name:    "max-redirs without -L",
			src:     `curl --max-redirs 3 https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{},
			body:    "none",
			codes:   []string{CodeNotImported},
		},
		{
			name:    "header removal",
			src:     `curl -H 'Accept:' -H 'X-Empty;' https://api.test/`,
			method:  "GET",
			url:     "https://api.test/",
			headers: []string{"X-Empty="},
			body:    "none",
			codes:   []string{CodeNotImported},
		},
	}
	own := func(im Imported, c *Command) []string {
		out := []string{}
		for _, d := range im.Diagnostics {
			if !slices.Contains(c.Diagnostics, d) {
				out = append(out, d.Code)
			}
		}
		return out
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Parse(tt.src)
			im := c.Import()
			r := im.Request
			check := func(what string, got, want any) {
				t.Helper()
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s:\n got %q\nwant %q", what, got, want)
				}
			}
			check("method", r.Method, tt.method)
			check("url", r.URL, tt.url)
			if tt.params != nil {
				check("params", rows(r.Params), tt.params)
			}
			check("headers", rows(r.Headers), tt.headers)
			check("body", renderImportBody(r.Body), tt.body)
			auth := tt.auth
			if auth == "" {
				auth = "inherit"
			}
			check("auth", renderAuth(r.Auth), auth)
			check("settings", renderSettings(r.Settings), tt.setting)
			check("proxy", renderProxy(r.Settings.Proxy), tt.proxy)
			want := tt.codes
			if want == nil {
				want = []string{}
			}
			check("codes", own(im, c), want)
		})
	}
}

func renderImportBody(b projectfile.Body) string {
	switch b.Mode {
	case projectfile.BodyRaw:
		return "raw:" + b.Type + ":" + b.Text
	case projectfile.BodyURLEncoded:
		var s []string
		for _, f := range b.Fields {
			s = append(s, f.Name+"="+f.Value)
		}
		return "urlencoded:" + strings.Join(s, ",")
	case projectfile.BodyMultipart:
		var s []string
		for _, p := range b.Parts {
			v := p.Name + "=" + p.Value
			if p.File != "" {
				v += "@" + p.File
			}
			if p.ContentType != "" {
				v += " type=" + p.ContentType
			}
			s = append(s, v)
		}
		return "multipart:" + strings.Join(s, ",")
	case projectfile.BodyFile:
		s := "file:" + b.Path
		if b.ContentType != "" {
			s += " ct=" + b.ContentType
		}
		return s
	}
	return b.Mode
}

func renderAuth(a projectfile.Auth) string {
	switch a.Type {
	case projectfile.AuthBasic:
		return "basic " + a.Username + ":" + a.Password
	case projectfile.AuthDigest:
		return "digest " + a.Username + ":" + a.Password
	case projectfile.AuthBearer:
		return "bearer " + a.Token
	}
	return a.Type
}

func renderSettings(s projectfile.RequestSettings) string {
	var out []string
	if s.VerifyTLS != nil {
		out = append(out, fmt.Sprintf("verifyTls=%v", *s.VerifyTLS))
	}
	if s.FollowRedirects != nil {
		out = append(out, fmt.Sprintf("followRedirects=%v", *s.FollowRedirects))
	}
	if s.MaxRedirects != nil {
		out = append(out, fmt.Sprintf("maxRedirects=%d", *s.MaxRedirects))
	}
	if s.ForwardAuthorization != nil {
		out = append(out, fmt.Sprintf("forwardAuthorization=%v", *s.ForwardAuthorization))
	}
	if s.TimeoutMs != nil {
		out = append(out, fmt.Sprintf("timeoutMs=%d", *s.TimeoutMs))
	}
	if s.HTTPVersion != "" {
		out = append(out, "httpVersion="+s.HTTPVersion)
	}
	return strings.Join(out, " ")
}

func renderProxy(p projectfile.RequestProxy) string {
	if p.IsZero() {
		return ""
	}
	out := p.EffectiveMode()
	if p.URL != "" {
		out += " " + p.URL
	}
	if p.Password != "" {
		out += " password=" + p.Password
	}
	return out
}

func TestRequestName(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.test/v1/users/":  "users",
		"https://api.test/a%20b?x=1":  "a b",
		"https://user:pw@api.test":    "api.test",
		"https://api.test:8443/#frag": "api.test:8443",
		"":                            "Imported request",
		"http://h/{{id}}/details?x#y": "details",
	} {
		if got := requestName(in); got != want {
			t.Errorf("requestName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddQuery(t *testing.T) {
	for _, tt := range []struct{ u, q, want string }{
		{"https://h/p", "a=1", "https://h/p?a=1"},
		{"https://h/p?", "a=1", "https://h/p?a=1"},
		{"https://h/p?x=1", "a=1", "https://h/p?x=1&a=1"},
		{"https://h/p?x=1#f", "a=1", "https://h/p?x=1&a=1#f"},
		{"https://h/p", "", "https://h/p"},
	} {
		if got := addQuery(tt.u, tt.q); got != tt.want {
			t.Errorf("addQuery(%q, %q) = %q, want %q", tt.u, tt.q, got, tt.want)
		}
	}
}
