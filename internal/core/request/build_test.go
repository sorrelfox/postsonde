package request

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func buildExpand(vars map[string]string) Expand {
	r := template.Resolver{Scopes: template.Chain{template.Vars("test", vars)}}
	return func(text string) (string, error) {
		res := r.Render(text)
		if err := res.Err(); err != nil {
			return "", err
		}
		return res.Text, nil
	}
}

func bodyText(t *testing.T, b httpclient.Body) string {
	t.Helper()
	rc, _, err := b.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func i64(v int64) *int64 { return &v }

func TestBuildHTTP(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{
		Method:     "POST",
		URL:        " {{base}}/users/:id?x=1#frag ",
		PathParams: []projectfile.KeyValue{{Name: "id", Value: "{{user}}"}},
		Headers: []projectfile.KeyValue{
			{Name: "X-Trace", Value: "{{trace}}"},
			{Name: "X-Off", Value: "no", Disabled: true},
		},
		Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"id": "{{user}}"}`},
	}
	auth := projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api key", Value: "{{key}}", In: "query"}
	expand := buildExpand(map[string]string{"base": "https://example.com/v1", "user": "a b", "trace": "t-1", "key": "k&1"})

	b, err := BuildHTTP(f, auth, HTTPOptions{Expand: expand, DefaultTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r := b.Request
	if r.Method != "POST" {
		t.Errorf("method = %q", r.Method)
	}
	if want := "https://example.com/v1/users/a%20b?x=1&api%20key=k%261#frag"; r.URL != want {
		t.Errorf("url = %q, want %q", r.URL, want)
	}
	if want := []httpclient.Header{on("X-Trace", "t-1")}; !reflect.DeepEqual(r.Headers, want) {
		t.Errorf("headers = %+v, want %+v", r.Headers, want)
	}
	if got := bodyText(t, r.Body); got != `{"id": "a b"}` {
		t.Errorf("body = %q", got)
	}
	if r.Body.ContentType() != "application/json" {
		t.Errorf("content type = %q", r.Body.ContentType())
	}
	if r.Settings.Timeout != 30*time.Second || b.TimeoutSource != TimeoutFromSettings {
		t.Errorf("timeout = %v from %q, want the default", r.Settings.Timeout, b.TimeoutSource)
	}
	if !r.Settings.FollowRedirects || r.Settings.MaxRedirects != httpclient.DefaultMaxRedirects || b.InsecureSkipVerify {
		t.Errorf("settings = %+v, insecure %v", r.Settings, b.InsecureSkipVerify)
	}
	if len(b.Warnings) != 0 {
		t.Errorf("warnings = %+v", b.Warnings)
	}
}

func TestBuildHTTPAuthHeaderAndHEAD(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{
		Method: "HEAD",
		URL:    "http://localhost:8080/",
		Body:   projectfile.Body{Mode: projectfile.BodyRaw, Text: "dropped"},
	}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok"}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []httpclient.Header{on("Authorization", "Bearer tok")}; !reflect.DeepEqual(b.Request.Headers, want) {
		t.Errorf("headers = %+v", b.Request.Headers)
	}
	if b.Request.Body != nil || len(b.Warnings) != 1 || b.Warnings[0].Code != WarnBodyOmitted {
		t.Errorf("body = %v, warnings = %+v", b.Request.Body, b.Warnings)
	}
	if b.Request.Settings.Timeout != 0 || b.TimeoutSource != "" {
		t.Errorf("timeout = %v from %q, want none", b.Request.Settings.Timeout, b.TimeoutSource)
	}
}

func TestBuildHTTPSettings(t *testing.T) {
	t.Parallel()
	no := false
	two := 2
	tests := []struct {
		name       string
		settings   projectfile.RequestSettings
		def        time.Duration
		wantTO     time.Duration
		wantSource string
	}{
		{"no default, no own timeout", projectfile.RequestSettings{}, 0, 0, ""},
		{"the default", projectfile.RequestSettings{}, 5 * time.Second, 5 * time.Second, TimeoutFromSettings},
		{"own timeout overrides the default", projectfile.RequestSettings{TimeoutMs: i64(1500)}, 5 * time.Second, 1500 * time.Millisecond, TimeoutFromRequest},
		{"own timeout without a default", projectfile.RequestSettings{TimeoutMs: i64(250)}, 0, 250 * time.Millisecond, TimeoutFromRequest},
		{"own 0 means none, whatever the default", projectfile.RequestSettings{TimeoutMs: i64(0)}, 5 * time.Second, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := projectfile.HTTPRequest{Method: "GET", URL: "http://x/", Settings: tt.settings}
			b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{DefaultTimeout: tt.def})
			if err != nil {
				t.Fatal(err)
			}
			if b.Request.Settings.Timeout != tt.wantTO || b.TimeoutSource != tt.wantSource {
				t.Errorf("timeout = %v from %q, want %v from %q", b.Request.Settings.Timeout, b.TimeoutSource, tt.wantTO, tt.wantSource)
			}
		})
	}

	f := projectfile.HTTPRequest{Method: "GET", URL: "https://x/", Settings: projectfile.RequestSettings{FollowRedirects: &no, MaxRedirects: &two, VerifyTLS: &no}}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.Settings.FollowRedirects || b.Request.Settings.MaxRedirects != 2 || !b.InsecureSkipVerify {
		t.Errorf("settings = %+v, insecure %v", b.Request.Settings, b.InsecureSkipVerify)
	}
}

func TestBuildHTTPErrors(t *testing.T) {
	t.Parallel()
	neg := -1
	none := projectfile.Auth{Type: projectfile.AuthNone}
	expand := buildExpand(map[string]string{"empty": ""})
	tests := []struct {
		name  string
		f     projectfile.HTTPRequest
		auth  projectfile.Auth
		kind  errs.Kind
		code  string
		field string
	}{
		{"invalid method", projectfile.HTTPRequest{Method: "GE T", URL: "http://x/"}, none, errs.InvalidArgument, CodeInvalidMethod, ""},
		{"no URL", projectfile.HTTPRequest{Method: "GET", URL: "  "}, none, errs.InvalidArgument, CodeURLMissing, ""},
		{"URL empty after variables", projectfile.HTTPRequest{Method: "GET", URL: "{{empty}}"}, none, errs.InvalidArgument, CodeURLMissing, ""},
		{"undefined variable in the URL", projectfile.HTTPRequest{Method: "GET", URL: "{{nope}}/x"}, none, errs.InvalidArgument, template.CodeUnresolved, "url"},
		{"missing path param", projectfile.HTTPRequest{Method: "GET", URL: "http://x/:id"}, none, errs.InvalidArgument, CodePathParamMissing, ""},
		{"negative timeout", projectfile.HTTPRequest{Method: "GET", URL: "http://x/", Settings: projectfile.RequestSettings{TimeoutMs: i64(-1)}}, none, errs.InvalidArgument, CodeInvalidSetting, "timeoutMs"},
		{"negative redirects", projectfile.HTTPRequest{Method: "GET", URL: "http://x/", Settings: projectfile.RequestSettings{MaxRedirects: &neg}}, none, errs.InvalidArgument, CodeInvalidSetting, "maxRedirects"},
		{"bad header", projectfile.HTTPRequest{Method: "GET", URL: "http://x/", Headers: []projectfile.KeyValue{{Name: "a b", Value: "1"}}}, none, errs.InvalidArgument, CodeInvalidHeaderName, "name"},
		{"missing body file", projectfile.HTTPRequest{Method: "POST", URL: "http://x/", Body: projectfile.Body{Mode: projectfile.BodyFile, Path: "nope.bin"}}, none, errs.NotFound, CodeBodyFileMissing, ""},
		{"auth secret stored elsewhere", projectfile.HTTPRequest{Method: "GET", URL: "http://x/"}, projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: []string{"token"}}, errs.InvalidArgument, CodeAuthSecretMissing, "token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildHTTP(tt.f, tt.auth, HTTPOptions{Expand: expand, Root: t.TempDir()})
			var e *errs.Error
			if !errors.As(err, &e) || errs.KindOf(err) != tt.kind || e.Code != tt.code {
				t.Fatalf("err = %v (%#v), want %s %s", err, e, tt.kind, tt.code)
			}
			if tt.field != "" && e.Details[DetailField] != tt.field {
				t.Errorf("field = %q, want %q", e.Details[DetailField], tt.field)
			}
		})
	}
}

// TestBuildHTTPSafeDefaults locks in PRV-11 for a request the user has not
// configured: TLS verification on and redirects followed but capped at
// httpclient.DefaultMaxRedirects, Authorization kept to the origin. A
// maxRedirects of 0 follows none (HTTP-22), so no setting is uncapped.
func TestBuildHTTPSafeDefaults(t *testing.T) {
	t.Parallel()
	zero := 0
	for name, s := range map[string]projectfile.RequestSettings{
		"no settings":    {},
		"maxRedirects 0": {MaxRedirects: &zero},
	} {
		f := projectfile.HTTPRequest{Method: "GET", URL: "https://x/", Settings: s}
		b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if b.InsecureSkipVerify {
			t.Errorf("%s: TLS verification is off", name)
		}
		if !b.Request.Settings.FollowRedirects {
			t.Errorf("%s: redirects are not followed", name)
		}
		want := httpclient.DefaultMaxRedirects
		if s.MaxRedirects != nil {
			want = *s.MaxRedirects
		}
		if m := b.Request.Settings.MaxRedirects; m != want {
			t.Errorf("%s: MaxRedirects = %d, want %d", name, m, want)
		}
		if b.Request.Settings.ForwardAuthorization || b.Request.Settings.KeepMethod {
			t.Errorf("%s: settings = %+v, want Authorization stripped and the method switched", name, b.Request.Settings)
		}
	}
}

// TestBuildHTTPRequestOptions maps the URL encoding (HTTP-24), HTTP
// version (HTTP-25) and cookie (HTTP-26) settings, missing members meaning
// the defaults, and refuses unknown values.
func TestBuildHTTPRequestOptions(t *testing.T) {
	t.Parallel()
	no, yes := false, true
	none := projectfile.Auth{Type: projectfile.AuthNone}
	build := func(s projectfile.RequestSettings) (httpclient.Settings, error) {
		b, err := BuildHTTP(projectfile.HTTPRequest{Method: "QUERY", URL: "https://x/", Settings: s}, none, HTTPOptions{})
		return b.Request.Settings, err
	}

	s, err := build(projectfile.RequestSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if s != httpclient.DefaultSettings() {
		t.Errorf("missing members: %+v, want the defaults", s)
	}

	tests := []struct {
		in   projectfile.RequestSettings
		want func(*httpclient.Settings)
	}{
		{projectfile.RequestSettings{URLEncoding: "auto", HTTPVersion: "auto", SendCookies: &yes, StoreCookies: &yes}, func(*httpclient.Settings) {}},
		{projectfile.RequestSettings{URLEncoding: "rfc3986"}, func(s *httpclient.Settings) { s.URLEncoding = httpclient.URLEncodingRFC3986 }},
		{projectfile.RequestSettings{URLEncoding: "none"}, func(s *httpclient.Settings) { s.URLEncoding = httpclient.URLEncodingAsTyped }},
		{projectfile.RequestSettings{HTTPVersion: "http1"}, func(s *httpclient.Settings) { s.HTTPVersion = httpclient.HTTPVersion1 }},
		{projectfile.RequestSettings{HTTPVersion: "http2"}, func(s *httpclient.Settings) { s.HTTPVersion = httpclient.HTTPVersion2 }},
		{projectfile.RequestSettings{SendCookies: &no}, func(s *httpclient.Settings) { s.SkipSendingCookies = true }},
		{projectfile.RequestSettings{StoreCookies: &no}, func(s *httpclient.Settings) { s.SkipStoringCookies = true }},
	}
	for _, tt := range tests {
		got, err := build(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		want := httpclient.DefaultSettings()
		tt.want(&want)
		if got != want {
			t.Errorf("%+v: settings %+v, want %+v", tt.in, got, want)
		}
	}

	for field, in := range map[string]projectfile.RequestSettings{
		"urlEncoding": {URLEncoding: "WHATWG"},
		"httpVersion": {HTTPVersion: "h3"},
	} {
		_, err := build(in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeInvalidSetting || e.Details["field"] != field {
			t.Errorf("%s: err = %v, want %s for the field", field, err, CodeInvalidSetting)
		}
	}
}

func TestFormatTimeout(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		250 * time.Millisecond:                    "250 ms",
		time.Second:                               "1 s",
		1500 * time.Millisecond:                   "1.5 s",
		30 * time.Second:                          "30 s",
		90500 * time.Millisecond:                  "90.5 s",
		2 * time.Minute:                           "2 min",
		90 * time.Second:                          "1 min 30 s",
		time.Hour + 30*time.Minute:                "1 h 30 min",
		24 * time.Hour:                            "24 h",
		time.Hour + 2*time.Minute + 3*time.Second: "1 h 2 min 3 s",
	} {
		if got := FormatTimeout(d); got != want {
			t.Errorf("FormatTimeout(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestBuildHTTPRedirectAndTLSSettings maps every HTTP-22 option and the
// HTTP-23 switch, with the app's TLS default under the request's own.
func TestBuildHTTPRedirectAndTLSSettings(t *testing.T) {
	t.Parallel()
	yes, no, zero := true, false, 0
	none := projectfile.Auth{Type: projectfile.AuthNone}

	f := projectfile.HTTPRequest{Method: "POST", URL: "https://x/", Settings: projectfile.RequestSettings{
		MaxRedirects: &zero, FollowOriginalMethod: &yes, ForwardAuthorization: &yes,
	}}
	b, err := BuildHTTP(f, none, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := b.Request.Settings
	if !s.FollowRedirects || s.MaxRedirects != 0 || !s.KeepMethod || !s.ForwardAuthorization {
		t.Errorf("settings = %+v", s)
	}

	tests := []struct {
		name       string
		verify     *bool
		defInsec   bool
		wantInsec  bool
		wantSource string
	}{
		{"defaults verify", nil, false, false, ""},
		{"request turns it off", &no, false, true, TLSFromRequest},
		{"app setting turns it off", nil, true, true, TLSFromSettings},
		{"request turns it back on over the app setting", &yes, true, false, ""},
		{"request off with the app setting off", &no, true, true, TLSFromRequest},
	}
	for _, tt := range tests {
		f := projectfile.HTTPRequest{Method: "GET", URL: "https://x/", Settings: projectfile.RequestSettings{VerifyTLS: tt.verify}}
		b, err := BuildHTTP(f, none, HTTPOptions{DefaultInsecure: tt.defInsec})
		if err != nil {
			t.Fatal(err)
		}
		if b.InsecureSkipVerify != tt.wantInsec || b.TLSSource != tt.wantSource {
			t.Errorf("%s: insecure %v from %q, want %v from %q", tt.name, b.InsecureSkipVerify, b.TLSSource, tt.wantInsec, tt.wantSource)
		}
	}
}
