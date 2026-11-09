package snippet

import (
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func ptr[T any](v T) *T { return &v }

func kv(name, value string) projectfile.KeyValue {
	return projectfile.KeyValue{Name: name, Value: value}
}

func raw(typ, text string) projectfile.Body {
	return projectfile.Body{Mode: projectfile.BodyRaw, Type: typ, Text: text}
}

// root is the project root of the test requests: testdata/files, which
// holds the body files. Its absolute path is written as <root> in the
// golden files.
func root(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "files"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// curlCase is a request and what its command is made with.
type curlCase struct {
	name    string
	req     projectfile.HTTPRequest
	graphql *projectfile.GraphQLRequest
	auth    projectfile.Auth
	cookies []*http.Cookie
	secrets []string // masked when set
	// lossy lists what the curl importer cannot bring back, which the
	// round trip leaves out.
	lossy []string
}

func httpReq(method, url string) projectfile.HTTPRequest {
	return projectfile.HTTPRequest{Method: method, URL: url, Body: projectfile.Body{Mode: projectfile.BodyNone}}
}

func with(r projectfile.HTTPRequest, f func(*projectfile.HTTPRequest)) projectfile.HTTPRequest {
	f(&r)
	return r
}

func curlCases() []curlCase {
	return []curlCase{
		{name: "get", req: httpReq("GET", "https://api.test/items?page=2&q=a%20b")},
		{name: "get_headers", req: with(httpReq("GET", "https://api.test/items"), func(r *projectfile.HTTPRequest) {
			r.Headers = []projectfile.KeyValue{
				kv("Accept", "application/json"),
				kv("X-Quote", `it's "quoted" $HOME `+"`id`"),
				kv("X-Empty", ""),
				{Name: "X-Disabled", Value: "no", Disabled: true},
				kv("X-Unicode", "héllo ✓"),
				kv("User-Agent", "PostSonde-test"),
			}
		})},
		{name: "post_json", req: with(httpReq("POST", "https://api.test/items"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawJSON, "{\n  \"name\": \"O'Brien\",\n  \"tags\": [\"a\", \"b\"]\n}")
		})},
		{name: "post_at_sign", req: with(httpReq("POST", "https://api.test/items"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawText, "@not-a-file")
		})},
		{name: "put_text", req: with(httpReq("PUT", "https://api.test/items/1"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawText, "plain text")
		})},
		{name: "get_with_body", req: with(httpReq("GET", "https://api.test/search"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawJSON, `{"q":"x"}`)
		})},
		{name: "query_method", req: with(httpReq("QUERY", "https://api.test/search"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawJSON, `{"q":"x"}`)
		})},
		{name: "head", req: httpReq("HEAD", "https://api.test/items")},
		{name: "delete", req: httpReq("DELETE", "https://api.test/items/1")},
		{name: "custom_method", req: httpReq("purge", "https://api.test/cache")},
		{name: "urlencoded", req: with(httpReq("POST", "https://api.test/login"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{kv("user", "a b"), kv("next", "/x?y=1&z=2")}}
		})},
		{name: "multipart", req: with(httpReq("POST", "https://api.test/upload"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyMultipart, Parts: []projectfile.Part{
				{Name: "title", Value: "a;b,c \"q\""},
				{Name: "meta", Value: `{"k":1}`, ContentType: "application/json"},
				{Name: "at", Value: "@literal"},
				{Name: "file", File: "data.json"},
				{Name: "odd", File: "odd name;1.txt", ContentType: "text/x-odd"},
			}}
		})},
		{name: "multipart_type_params", req: with(httpReq("POST", "https://api.test/upload"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyMultipart, Parts: []projectfile.Part{
				{Name: "text", Value: "x", ContentType: "text/plain; charset=utf-8"},
				{Name: "file", File: "data.json", ContentType: `application/json; charset="utf-8"`},
			}}
		}), lossy: []string{"partTypes"}},
		{name: "file_body", req: with(httpReq("POST", "https://api.test/blob"), func(r *projectfile.HTTPRequest) {
			r.Body = projectfile.Body{Mode: projectfile.BodyFile, Path: "data.json"}
		})},
		{name: "basic_auth", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "ann", Password: "p@ss:w'rd"}},
		{name: "digest_auth", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "ann", Password: "secret"}},
		{name: "bearer_auth", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok-123"}},
		{name: "apikey_query", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api_key", Value: "k 1", In: "query"}},
		{name: "userinfo", req: httpReq("GET", "https://bob:pw@api.test/me")},
		{name: "settings", req: with(httpReq("POST", "https://api.test/items"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawJSON, `{}`)
			r.Settings = projectfile.RequestSettings{
				TimeoutMs: ptr[int64](1500), VerifyTLS: ptr(false), FollowRedirects: ptr(true), MaxRedirects: ptr(5),
				ForwardAuthorization: ptr(true), FollowOriginalMethod: ptr(true), HTTPVersion: projectfile.HTTPVersion1,
			}
		}), lossy: []string{"keepMethod"}},
		{name: "no_redirects_http2", req: with(httpReq("GET", "http://api.test/items"), func(r *projectfile.HTTPRequest) {
			r.Settings = projectfile.RequestSettings{FollowRedirects: ptr(false), HTTPVersion: projectfile.HTTPVersion2, TimeoutMs: ptr[int64](30000)}
		}), lossy: []string{"redirects"}},
		{name: "url_as_typed", req: with(httpReq("GET", "https://api.test/a/../b/./c?f={x}&g=[1]|\"q\""), func(r *projectfile.HTTPRequest) {
			r.Settings = projectfile.RequestSettings{URLEncoding: projectfile.URLEncodingNone}
		})},
		{name: "url_rfc3986", req: with(httpReq("GET", "https://api.test/p|q?x=[1]&y=a^b"), func(r *projectfile.HTTPRequest) {
			r.Settings = projectfile.RequestSettings{URLEncoding: projectfile.URLEncodingRFC3986}
		})},
		{name: "url_auto", req: httpReq("GET", "https://api.test/ä ö/a^b?q=\"x\" y#frag")},
		{name: "control_chars", req: with(httpReq("POST", "https://api.test/raw"), func(r *projectfile.HTTPRequest) {
			r.Body = raw(projectfile.RawText, "line1\r\nline2\x01\ttab \\ 'q'")
		})},
		{name: "cookies", req: httpReq("GET", "https://api.test/me"),
			cookies: []*http.Cookie{{Name: "sid", Value: "abc"}, {Name: "theme", Value: "dark"}}},
		{name: "cookies_with_row", req: with(httpReq("GET", "https://api.test/me"), func(r *projectfile.HTTPRequest) {
			r.Headers = []projectfile.KeyValue{kv("Cookie", "theme=light")}
		}), cookies: []*http.Cookie{{Name: "sid", Value: "abc"}, {Name: "theme", Value: "dark"}}},
		{name: "cookies_not_sent", req: with(httpReq("GET", "https://api.test/me"), func(r *projectfile.HTTPRequest) {
			r.Settings = projectfile.RequestSettings{SendCookies: ptr(false)}
		}), cookies: []*http.Cookie{{Name: "sid", Value: "abc"}}},
		{name: "masked", req: with(httpReq("POST", "https://api.test/items?key=s3cr3t"), func(r *projectfile.HTTPRequest) {
			r.Headers = []projectfile.KeyValue{kv("X-Api-Key", "s3cr3t"), kv("Authorization", "Bearer typed-token")}
			r.Body = raw(projectfile.RawJSON, `{"password":"s3cr3t"}`)
		}), secrets: []string{"s3cr3t"}, lossy: []string{"masked"}},
		{name: "masked_basic", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "ann", Password: "pw"}, secrets: []string{"pw"}, lossy: []string{"masked"}},
		{name: "masked_digest", req: httpReq("GET", "https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "ann", Password: "pw"}, secrets: []string{}, lossy: []string{"masked"}},
		{name: "graphql_post", graphql: &projectfile.GraphQLRequest{URL: "https://api.test/graphql",
			Query: "query Q($id: ID!) {\n  user(id: $id) { name }\n}", Variables: `{"id": "1"}`, OperationName: "Q"}},
		{name: "graphql_get", graphql: &projectfile.GraphQLRequest{Method: "GET", URL: "https://api.test/graphql",
			Query: "{ me { name } }"}},
	}
}

// buildCase builds c as Send would.
func buildCase(t *testing.T, c curlCase) request.HTTPBuild {
	t.Helper()
	opts := request.HTTPOptions{Root: root(t)}
	if c.auth.Type == "" {
		c.auth.Type = projectfile.AuthNone
	}
	var b request.HTTPBuild
	var err error
	if c.graphql != nil {
		b, err = request.BuildGraphQL(*c.graphql, c.auth, opts)
	} else {
		b, err = request.BuildHTTP(c.req, c.auth, opts)
	}
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return b
}

func generate(t *testing.T, c curlCase) (string, Request) {
	t.Helper()
	r, err := FromBuild(buildCase(t, c), c.cookies)
	if err != nil {
		t.Fatalf("FromBuild: %v", err)
	}
	if c.secrets != nil {
		var masked bool
		if r, masked = r.Masked(c.secrets); !masked {
			t.Errorf("Masked reports nothing masked")
		}
	}
	cmd, err := Curl(r)
	if err != nil {
		t.Fatalf("Curl: %v", err)
	}
	return cmd, r
}

func TestCurlGolden(t *testing.T) {
	rt := root(t)
	for _, c := range curlCases() {
		t.Run(c.name, func(t *testing.T) {
			_, r := generate(t, c)
			cmd, err := Curl(withGoldenRoot(r, rt))
			if err != nil {
				t.Fatalf("Curl: %v", err)
			}
			got := strings.ReplaceAll(cmd, goldenRoot, "<root>") + "\n"
			path := filepath.Join("testdata", "curl", c.name+".golden")
			if *update {
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
				t.Errorf("command:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestCurlRoundTrip reads every command back with the cURL importer
// (internal/core/curl), builds the imported request and checks it sends
// the same request.
func TestCurlRoundTrip(t *testing.T) {
	for _, c := range curlCases() {
		if slices.Contains(c.lossy, "masked") {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			cmd, want := generate(t, c)
			imp := curl.Import(cmd)
			for _, d := range imp.Diagnostics {
				if d.Severity == curl.SeverityError {
					t.Fatalf("import error %s: %s\n%s", d.Code, d.Message, cmd)
				}
			}
			f := imp.Request
			// The command's URL is already encoded as sent.
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

// canon writes what r sends in a form that does not depend on how it is
// expressed: headers sorted by lower-case name, cookies and credentials as
// the headers they become, the Content-Type in effect.
func canon(r Request, lossy []string) string {
	var hs []string
	hasCT := false
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			hasCT = true
		}
		if h.Value == "" && strings.EqualFold(h.Name, "User-Agent") {
			continue
		}
		hs = append(hs, strings.ToLower(h.Name)+": "+h.Value)
	}
	if r.Cookies != "" {
		hs = append(hs, "cookie: "+r.Cookies)
	}
	if !hasCT && r.Body.ContentType != "" {
		hs = append(hs, "content-type: "+r.Body.ContentType)
	}
	slices.Sort(hs)
	var b strings.Builder
	b.WriteString(r.Method + " " + r.URL + "\n")
	for _, h := range hs {
		b.WriteString(h + "\n")
	}
	if r.Basic != nil {
		b.WriteString("basic " + r.Basic.Username + ":" + r.Basic.Password + "\n")
	}
	if r.Digest != nil {
		b.WriteString("digest " + r.Digest.Username + ":" + r.Digest.Password + "\n")
	}
	b.WriteString("body " + string(r.Body.Kind) + " " + strconv.Quote(string(r.Body.Data)) + " " + r.Body.File + "\n")
	for _, p := range r.Body.Parts {
		if slices.Contains(lossy, "partTypes") {
			p.ContentType = ""
		}
		b.WriteString("part " + strconv.Quote(p.Name) + " " + strconv.Quote(p.Value) + " " + p.File + " " + p.Filename + " " + p.ContentType + "\n")
	}
	b.WriteString("insecure " + strconv.FormatBool(r.Insecure) + "\n")
	if !slices.Contains(lossy, "redirects") {
		b.WriteString("follow " + strconv.FormatBool(r.FollowRedirects) + " " + strconv.Itoa(r.MaxRedirects) + " trusted " + strconv.FormatBool(r.ForwardAuthorization) + "\n")
	}
	if !slices.Contains(lossy, "keepMethod") {
		b.WriteString("keep " + strconv.FormatBool(r.KeepMethod) + "\n")
	}
	b.WriteString("timeout " + r.Timeout.String() + " version " + string(r.HTTPVersion) + "\n")
	return b.String()
}

func TestQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "''"},
		{"abc", "abc"},
		{"https://a.test/x/y%20", "https://a.test/x/y%20"},
		{"https://a.test/x?y=1", "'https://a.test/x?y=1'"},
		{"-rf", "'-rf'"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"$HOME `x` \"q\" \\", "'$HOME `x` \"q\" \\'"},
		{"a\nb\tc", "'a\nb\tc'"},
		{"a\r\nb", `$'a\r\nb'`},
		{"\x1b[0m'\\", `$'\x1b[0m\'\\'`},
		{"é✓", "'é✓'"},
		{"\xffok", `$'\xffok'`},
	} {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
		// The curl tokenizer reads the word back.
		toks := curl.Tokenize("curl "+Quote(tc.in), curl.DialectPOSIX)
		if len(toks.Words) != 2 || toks.Words[1].Value != tc.in {
			t.Errorf("Quote(%q) = %s reads back as %+v", tc.in, Quote(tc.in), toks)
		}
	}
}

func TestCurlNulByte(t *testing.T) {
	r := Request{Method: "POST", URL: "https://a.test/", Body: Body{Kind: BodyData, Data: []byte("a\x00b")}}
	_, err := Curl(r)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeNulByte {
		t.Fatalf("err = %v, want %s", err, CodeNulByte)
	}
}

func TestFromBuildBodyTooLarge(t *testing.T) {
	b, err := request.BuildHTTP(with(httpReq("POST", "https://a.test/"), func(r *projectfile.HTTPRequest) {
		r.Body = raw(projectfile.RawText, strings.Repeat("x", MaxBody+1))
	}), projectfile.Auth{Type: projectfile.AuthNone}, request.HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = FromBuild(b, nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeBodyTooLarge {
		t.Fatalf("err = %v, want %s", err, CodeBodyTooLarge)
	}
}

func TestMaskedLeavesNoSecret(t *testing.T) {
	for _, c := range curlCases() {
		if c.secrets == nil {
			continue
		}
		cmd, _ := generate(t, c)
		for _, s := range append(c.secrets, c.auth.Password, "typed-token") {
			if s != "" && strings.Contains(cmd, s) {
				t.Errorf("%s: masked command holds %q:\n%s", c.name, s, cmd)
			}
		}
	}
}

func TestFromBuildTimeout(t *testing.T) {
	b, err := request.BuildHTTP(httpReq("GET", "https://a.test/"), projectfile.Auth{Type: projectfile.AuthNone}, request.HTTPOptions{DefaultTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r, err := FromBuild(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := Curl(r)
	if !strings.Contains(cmd, "-m 2") {
		t.Errorf("the app's default timeout is not in the command:\n%s", cmd)
	}
}
