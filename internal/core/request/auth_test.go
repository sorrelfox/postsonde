package request

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func strPtr(s string) *string { return &s }

func basicValue(userPass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(userPass))
}

func TestAuthSchemes(t *testing.T) {
	tests := []struct {
		name    string
		auth    projectfile.Auth
		secrets map[string]string
		want    AuthResult
	}{
		{"none", projectfile.Auth{Type: projectfile.AuthNone}, nil, AuthResult{}},
		{
			"basic, RFC 7617 example",
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "Aladdin", Password: "open sesame"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==")}},
		},
		{
			"basic, UTF-8 credentials (RFC 7617 section 2.1 example)",
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "test", Password: "123£"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", "Basic dGVzdDoxMjPCow==")}},
		},
		{
			"basic, unicode username and empty password",
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "zoë 日本"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", basicValue("zoë 日本:"))}},
		},
		{
			"basic, password only",
			projectfile.Auth{Type: projectfile.AuthBasic, Password: "p"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", basicValue(":p"))}},
		},
		{"basic, both empty", projectfile.Auth{Type: projectfile.AuthBasic}, nil, AuthResult{}},
		{
			"basic, password from local storage",
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", LocalSecrets: []string{"password"}},
			map[string]string{"auth.password": "sëcret"},
			AuthResult{Headers: []httpclient.Header{on("Authorization", basicValue("u:sëcret"))}},
		},
		{
			"basic, disabled",
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "p", Disabled: true},
			nil,
			AuthResult{},
		},
		{
			"bearer, default prefix",
			projectfile.Auth{Type: projectfile.AuthBearer, Token: "abc.def"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", "Bearer abc.def")}},
		},
		{
			"bearer, custom prefix",
			projectfile.Auth{Type: projectfile.AuthBearer, Prefix: strPtr("Token"), Token: "abc"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", "Token abc")}},
		},
		{
			"bearer, empty prefix sends the token alone",
			projectfile.Auth{Type: projectfile.AuthBearer, Prefix: strPtr(""), Token: "abc"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("Authorization", "abc")}},
		},
		{
			"bearer, token from local storage",
			projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: []string{"token"}},
			map[string]string{"auth.token": "t0k"},
			AuthResult{Headers: []httpclient.Header{on("Authorization", "Bearer t0k")}},
		},
		{
			"bearer, disabled",
			projectfile.Auth{Type: projectfile.AuthBearer, Token: "abc", Disabled: true},
			nil,
			AuthResult{},
		},
		{"bearer, empty token", projectfile.Auth{Type: projectfile.AuthBearer, Prefix: strPtr("Token")}, nil, AuthResult{}},
		{
			"api key in a header, default location",
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-API-Key", Value: "k 1"},
			nil,
			AuthResult{Headers: []httpclient.Header{on("X-API-Key", "k 1")}},
		},
		{
			"api key in a header, empty value",
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-API-Key", In: projectfile.APIKeyInHeader},
			nil,
			AuthResult{Headers: []httpclient.Header{on("X-API-Key", "")}},
		},
		{
			"api key in the query",
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api key", Value: "a&b=ü", In: projectfile.APIKeyInQuery},
			nil,
			AuthResult{Query: []QueryParam{{Name: "api key", Value: "a&b=ü"}}},
		},
		{
			"api key from local storage",
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "key", In: projectfile.APIKeyInQuery, LocalSecrets: []string{"value"}},
			map[string]string{"auth.value": "v"},
			AuthResult{Query: []QueryParam{{Name: "key", Value: "v"}}},
		},
		{"api key without a name", projectfile.Auth{Type: projectfile.AuthAPIKey, Value: "v"}, nil, AuthResult{}},
		{
			"api key, disabled",
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: "v", Disabled: true},
			nil,
			AuthResult{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Auth(tt.auth, tt.secrets, nil)
			if err != nil {
				t.Fatalf("Auth: %v", err)
			}
			got.Secrets, got.Credentials = nil, nil // TestAuthSecrets, TestHandshakeCredentials
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Auth = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func wantAuthErr(t *testing.T, err error, kind errs.Kind, code, typ, field string) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *errs.Error", err)
	}
	if e.Kind != kind || e.Code != code {
		t.Errorf("error = %s/%s, want %s/%s (%v)", e.Kind, e.Code, kind, code, e)
	}
	if e.Details[DetailAuthType] != typ || e.Details[DetailAuthField] != field {
		t.Errorf("details = %v, want type %q field %q", e.Details, typ, field)
	}
	return e
}

func TestAuthErrors(t *testing.T) {
	const secret = "s3cr3t-value"
	tests := []struct {
		name  string
		auth  projectfile.Auth
		kind  errs.Kind
		code  string
		field string
	}{
		{"colon in the username", projectfile.Auth{Type: projectfile.AuthBasic, Username: "a:" + secret, Password: secret}, errs.InvalidArgument, CodeAuthInvalid, "username"},
		{"control character in the username", projectfile.Auth{Type: projectfile.AuthBasic, Username: secret + "\x01"}, errs.InvalidArgument, CodeAuthInvalid, "username"},
		{"line break in the password", projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: secret + "\n"}, errs.InvalidArgument, CodeAuthInvalid, "password"},
		{"missing local password", projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", LocalSecrets: []string{"password"}}, errs.InvalidArgument, CodeAuthSecretMissing, "password"},
		{"line break in the token", projectfile.Auth{Type: projectfile.AuthBearer, Token: secret + "\r\nX: y"}, errs.InvalidArgument, CodeAuthInvalid, "token"},
		{"line break in the prefix", projectfile.Auth{Type: projectfile.AuthBearer, Prefix: strPtr("A\nB"), Token: secret}, errs.InvalidArgument, CodeAuthInvalid, "prefix"},
		{"missing local token", projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: []string{"token"}}, errs.InvalidArgument, CodeAuthSecretMissing, "token"},
		{"api key name is not a token", projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X Key", Value: secret}, errs.InvalidArgument, CodeAuthInvalid, "key"},
		{"line break in the api key", projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Key", Value: secret + "\n"}, errs.InvalidArgument, CodeAuthInvalid, "value"},
		{"unknown api key location", projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: secret, In: "cookie"}, errs.Unsupported, CodeAuthUnsupported, "in"},
		{"missing local api key", projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", LocalSecrets: []string{"value"}}, errs.InvalidArgument, CodeAuthSecretMissing, "value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Auth(tt.auth, nil, nil)
			e := wantAuthErr(t, err, tt.kind, tt.code, tt.auth.Type, tt.field)
			if strings.Contains(e.Error(), secret) || strings.Contains(e.Hint, secret) || strings.Contains(e.Message, secret) {
				t.Errorf("error shows the secret: %v", e)
			}
			for _, v := range e.Details {
				if strings.Contains(v, secret) {
					t.Errorf("details show the secret: %v", e.Details)
				}
			}
		})
	}

	_, err := Auth(projectfile.Auth{Type: "edgegrid", Password: secret}, nil, nil)
	wantAuthErr(t, err, errs.Unsupported, CodeAuthUnsupported, "edgegrid", "")
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthInherit}, nil, nil)
	if errs.KindOf(err) != errs.Internal {
		t.Errorf("inherit: %v, want an internal error", err)
	}
	// A disabled auth with a missing secret sends nothing, without error.
	if _, err := Auth(projectfile.Auth{Type: projectfile.AuthBearer, Disabled: true, LocalSecrets: []string{"token"}}, nil, nil); err != nil {
		t.Errorf("disabled with a missing secret: %v", err)
	}
}

func TestAuthExpand(t *testing.T) {
	resolver := template.Resolver{Scopes: template.Chain{
		template.MapScope{Label: "Environment: dev", Variables: map[string]template.Variable{
			"user":   {Value: "zoë"},
			"pass":   {Value: "pä:ss", Secret: true},
			"token":  {Value: "t-{{suffix}}", Secret: true},
			"suffix": {Value: "42"},
			"prefix": {Value: "JWT"},
			"hdr":    {Value: "X-Key"},
			"empty":  {Value: ""},
			"crlf":   {Value: "a\r\nb"},
		}},
	}}
	expand := func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}

	check := func(a projectfile.Auth, secrets map[string]string, want AuthResult) {
		t.Helper()
		got, err := Auth(a, secrets, expand)
		if err != nil {
			t.Fatalf("Auth(%+v): %v", a, err)
		}
		got.Secrets, got.Credentials = nil, nil // TestAuthSecrets, TestHandshakeCredentials
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Auth(%+v) = %#v, want %#v", a, got, want)
		}
	}
	check(projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}", Password: "{{pass}}"}, nil,
		AuthResult{Headers: []httpclient.Header{on("Authorization", basicValue("zoë:pä:ss"))}})
	check(projectfile.Auth{Type: projectfile.AuthBearer, Prefix: strPtr("{{prefix}}"), Token: "{{token}}"}, nil,
		AuthResult{Headers: []httpclient.Header{on("Authorization", "JWT t-42")}})
	// A locally stored value is rendered too.
	check(projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: []string{"token"}}, map[string]string{"auth.token": "x{{suffix}}"},
		AuthResult{Headers: []httpclient.Header{on("Authorization", "Bearer x42")}})
	check(projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "{{hdr}}", Value: "{{token}}"}, nil,
		AuthResult{Headers: []httpclient.Header{on("X-Key", "t-42")}})
	check(projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{empty}}"}, nil, AuthResult{})

	_, err := Auth(projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{missing}}"}, nil, expand)
	e := wantAuthErr(t, err, errs.InvalidArgument, "template.unresolved", "bearer", "token")
	if !strings.Contains(e.Message, "missing") {
		t.Errorf("message %q does not name the variable", e.Message)
	}
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}:x"}, nil, expand)
	wantAuthErr(t, err, errs.InvalidArgument, CodeAuthInvalid, "basic", "username")
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "{{empty}}", Value: "v"}, nil, expand)
	wantAuthErr(t, err, errs.InvalidArgument, CodeAuthInvalid, "apikey", "key")
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "{{empty}}", In: projectfile.APIKeyInQuery}, nil, expand)
	wantAuthErr(t, err, errs.InvalidArgument, CodeAuthInvalid, "apikey", "key")
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{crlf}}"}, nil, expand)
	wantAuthErr(t, err, errs.InvalidArgument, CodeAuthInvalid, "bearer", "token")

	plain := errors.New("boom")
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthBasic, Username: "u"}, nil, func(string) (string, error) { return "", plain })
	wantAuthErr(t, err, errs.InvalidArgument, "", "basic", "username")
	if !errors.Is(err, plain) {
		t.Error("the expand error is not kept as the cause")
	}
}

// TestAuthSplitThenSend: what the service saves (requestsecrets.Split)
// sends the same as what was typed.
func TestAuthSplitThenSend(t *testing.T) {
	for _, typed := range []projectfile.Auth{
		{Type: projectfile.AuthBasic, Username: "zoë", Password: "pä$$"},
		{Type: projectfile.AuthBearer, Prefix: strPtr("Token"), Token: "abc"},
		{Type: projectfile.AuthAPIKey, Key: "k", Value: "v", In: projectfile.APIKeyInQuery},
		{Type: projectfile.AuthJWT, Algorithm: "HS384", JWTSecret: "s3cr3t", Payload: `{"sub":"u"}`},
	} {
		want, err := Auth(typed, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		file, values := requestsecrets.Split(typed)
		if file.Secret() != "" {
			t.Errorf("%s: the split file keeps the secret", typed.Type)
		}
		got, err := Auth(file, values, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: after Split = %#v, %v; want %#v", typed.Type, got, err, want)
		}
	}
}

func TestWithAuthHeaders(t *testing.T) {
	auth := []httpclient.Header{on("Authorization", "Bearer x")}
	tests := []struct {
		name    string
		headers []httpclient.Header
		want    []httpclient.Header
	}{
		{"added after the rows", []httpclient.Header{on("Accept", "*/*")}, []httpclient.Header{on("Accept", "*/*"), on("Authorization", "Bearer x")}},
		{"an enabled row wins, any case", []httpclient.Header{on("authorization", "Basic y")}, []httpclient.Header{on("authorization", "Basic y")}},
		{"a disabled row does not", []httpclient.Header{{Name: "Authorization", Value: "old"}}, []httpclient.Header{{Name: "Authorization", Value: "old"}, on("Authorization", "Bearer x")}},
		{"no rows", nil, auth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithAuthHeaders(tt.headers, auth); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("WithAuthHeaders = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWithAuthQuery(t *testing.T) {
	p := func(name, value string) []QueryParam { return []QueryParam{{Name: name, Value: value}} }
	tests := []struct {
		url    string
		params []QueryParam
		want   string
	}{
		{"https://a.example/p", p("key", "v"), "https://a.example/p?key=v"},
		{"https://a.example/p?x=1", p("key", "v"), "https://a.example/p?x=1&key=v"},
		{"https://a.example/p?", p("key", "v"), "https://a.example/p?key=v"},
		{"https://a.example/p?x=1&", p("key", "v"), "https://a.example/p?x=1&key=v"},
		{"https://a.example/p#frag", p("key", "v"), "https://a.example/p?key=v#frag"},
		{"https://a.example/p?x=%2F#f?g", p("key", "v"), "https://a.example/p?x=%2F&key=v#f?g"},
		{"https://a.example/p", p("api key", "a&b=c#d+e ü/~"), "https://a.example/p?api%20key=a%26b%3Dc%23d%2Be%20%C3%BC%2F~"},
		{"https://a.example/p?key=typed", p("key", "v"), "https://a.example/p?key=typed"},
		{"https://a.example/p?api%20key", p("api key", "v"), "https://a.example/p?api%20key"},
		{"https://a.example/p", p("key", ""), "https://a.example/p?key="},
		{"https://a.example/p", nil, "https://a.example/p"},
	}
	for _, tt := range tests {
		if got := WithAuthQuery(tt.url, tt.params); got != tt.want {
			t.Errorf("WithAuthQuery(%q, %v) = %q, want %q", tt.url, tt.params, got, tt.want)
		}
	}
}

func TestResolveAuth(t *testing.T) {
	bearer := projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"}
	basic := projectfile.Auth{Type: projectfile.AuthBasic, Username: "u"}
	inherit := projectfile.Auth{Type: projectfile.AuthInherit}
	none := projectfile.Auth{Type: projectfile.AuthNone}
	disabled := projectfile.Auth{Type: projectfile.AuthBearer, Token: "x", Disabled: true}

	tests := []struct {
		name    string
		own     projectfile.Auth
		parents []AuthSource
		want    projectfile.Auth
		from    string
	}{
		{"own auth", basic, []AuthSource{{ID: "f1", Auth: bearer}}, basic, ""},
		{"own none does not inherit", none, []AuthSource{{ID: "f1", Auth: bearer}}, none, ""},
		{"own disabled does not inherit", disabled, []AuthSource{{ID: "f1", Auth: bearer}}, disabled, ""},
		{"closest folder", inherit, []AuthSource{{ID: "f1", Auth: basic}, {ID: "f2", Auth: bearer}}, basic, "f1"},
		{"empty type inherits", projectfile.Auth{}, []AuthSource{{ID: "f1", Auth: bearer}}, bearer, "f1"},
		{"through inheriting folders", inherit, []AuthSource{{ID: "f1", Auth: inherit}, {ID: "f2", Auth: projectfile.Auth{}}, {ID: "f3", Auth: bearer}}, bearer, "f3"},
		{"a folder's none stops the chain", inherit, []AuthSource{{ID: "f1", Auth: none}, {ID: "f2", Auth: bearer}}, none, "f1"},
		{"a folder's disabled auth stops the chain", inherit, []AuthSource{{ID: "f1", Auth: disabled}, {ID: "f2", Auth: bearer}}, disabled, "f1"},
		{"nothing set: none", inherit, []AuthSource{{ID: "f1", Auth: inherit}}, none, ""},
		{"top level request", inherit, nil, none, ""},
		{"an unknown type is inherited as it is", inherit, []AuthSource{{ID: "f1", Auth: projectfile.Auth{Type: "edgegrid"}}}, projectfile.Auth{Type: "edgegrid"}, "f1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, from, err := ResolveAuth(tt.own, tt.parents)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("auth = %+v, want %+v", got, tt.want)
			}
			fromID := ""
			if from != nil {
				fromID = from.ID
			}
			if fromID != tt.from {
				t.Errorf("from = %q, want %q", fromID, tt.from)
			}
		})
	}

	_, _, err := ResolveAuth(inherit, []AuthSource{{ID: "f1", Auth: inherit}, {ID: "f2", Unreadable: true}, {ID: "f3", Auth: bearer}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeAuthInheritFailed || e.Details[DetailAuthFolder] != "f2" {
		t.Errorf("unreadable folder: %v", err)
	}
	// An unreadable folder above the one that sets auth does not matter.
	if _, from, err := ResolveAuth(inherit, []AuthSource{{ID: "f1", Auth: basic}, {ID: "f2", Unreadable: true}}); err != nil || from.ID != "f1" {
		t.Errorf("unreadable folder above: %v, %v", from, err)
	}
}

// TestAuthOnTheWire sends each scheme with httpclient and checks what the
// server receives.
func TestAuthOnTheWire(t *testing.T) {
	type seen struct {
		auth, key, query string
		user, pass       string
		basicOK          bool
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		got <- seen{auth: r.Header.Get("Authorization"), key: r.Header.Get("X-Api-Key"), query: r.URL.Query().Get("api key"), user: u, pass: p, basicOK: ok}
	}))
	defer srv.Close()
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer client.CloseIdleConnections()

	send := func(a projectfile.Auth, rows []projectfile.KeyValue) seen {
		t.Helper()
		out, err := Auth(a, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		headers, err := Headers(rows, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Send(context.Background(), httpclient.Request{
			Method:   "GET",
			URL:      WithAuthQuery(srv.URL+"/p", out.Query),
			Headers:  WithAuthHeaders(headers, out.Headers),
			Settings: httpclient.DefaultSettings(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return <-got
	}

	if s := send(projectfile.Auth{Type: projectfile.AuthBasic, Username: "zoë", Password: "pä:ss"}, nil); !s.basicOK || s.user != "zoë" || s.pass != "pä:ss" {
		t.Errorf("basic: %+v", s)
	}
	if s := send(projectfile.Auth{Type: projectfile.AuthBearer, Token: "abc"}, nil); s.auth != "Bearer abc" {
		t.Errorf("bearer: %+v", s)
	}
	if s := send(projectfile.Auth{Type: projectfile.AuthBearer, Token: "abc"}, []projectfile.KeyValue{kv("Authorization", "Custom 1")}); s.auth != "Custom 1" {
		t.Errorf("bearer with a header row: %+v", s)
	}
	if s := send(projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-API-Key", Value: "k1"}, nil); s.key != "k1" {
		t.Errorf("api key header: %+v", s)
	}
	if s := send(projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api key", Value: "a&b=+ü", In: projectfile.APIKeyInQuery}, nil); s.query != "a&b=+ü" {
		t.Errorf("api key query: %+v", s)
	}
	if s := send(projectfile.Auth{Type: projectfile.AuthBearer, Token: "abc", Disabled: true}, nil); s.auth != "" {
		t.Errorf("disabled: %+v", s)
	}
}
