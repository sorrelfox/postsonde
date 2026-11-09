package request

import (
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func oauth2Auth() projectfile.Auth {
	return projectfile.Auth{
		Type: projectfile.AuthOAuth2, TokenURL: "{{idp}}/token", ClientID: "app", ClientSecret: "{{cs}}",
		Scope: "read write", Audience: "api", Resource: "urn:r", State: "st",
		TokenParams: []projectfile.KeyValue{{Name: "tenant", Value: "{{tenant}}"}, {Name: "off", Value: "x", Disabled: true}, {Name: "", Value: "blank"}},
	}
}

func oauth2Expand() Expand {
	return expandWith(map[string]string{"idp": "https://idp.test", "cs": "client-s3cret", "tenant": "t1"})
}

// The build renders the oauth2 fields into the token request and leaves
// the token for later; UseOAuth2Token adds it where the auth says.
func TestOAuth2Build(t *testing.T) {
	t.Parallel()
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/items"}, oauth2Auth(), HTTPOptions{Expand: oauth2Expand()})
	if err != nil {
		t.Fatal(err)
	}
	p := b.OAuth2
	if p == nil || p.Applied || p.In != "header" || p.Name != "Authorization" || p.Prefix != "Bearer" {
		t.Fatalf("pending = %+v", p)
	}
	c := p.Token.Config
	if c.TokenURL != "https://idp.test/token" || c.ClientSecret != "client-s3cret" || c.Scope != "read write" ||
		!slices.Equal(c.Params, []oauth2.Param{{Name: "tenant", Value: "t1"}}) || p.Key != p.Token.Key() {
		t.Errorf("config = %+v", c)
	}
	if _, ok := headerOf(b, "Authorization"); ok {
		t.Error("Authorization before the token")
	}
	if !slices.Contains(b.AuthSecrets, "client-s3cret") {
		t.Errorf("secrets = %q", b.AuthSecrets)
	}

	copied := b
	b.UseOAuth2Token("tok-1")
	if got, _ := headerOf(b, "Authorization"); got != "Bearer tok-1" || !slices.Contains(b.AuthSecrets, "tok-1") ||
		b.HeaderSources[len(b.HeaderSources)-1] != HeaderFromAuth || !b.OAuth2.Applied {
		t.Errorf("header = %q, secrets %q, sources %v", got, b.AuthSecrets, b.HeaderSources)
	}
	b.UseOAuth2Token("tok-2")
	if n := len(b.Request.Headers); n != 1 {
		t.Errorf("a second token was added: %+v", b.Request.Headers)
	}
	// A copy made before keeps its own pending auth.
	copied.UseOAuth2Token("tok-3")
	if got, _ := headerOf(copied, "Authorization"); got != "Bearer tok-3" || copied.OAuth2.Applied == false {
		t.Errorf("copy header = %q", got)
	}
	if got, _ := headerOf(b, "Authorization"); got != "Bearer tok-1" {
		t.Errorf("copy changed the original: %q", got)
	}
}

func TestOAuth2TokenPlacement(t *testing.T) {
	t.Parallel()
	empty := ""
	token := "Token"
	tests := []struct {
		name       string
		edit       func(*projectfile.Auth)
		req        projectfile.HTTPRequest
		wantHeader string
		wantValue  string
		wantURL    string
		suppressed string
	}{
		{name: "default", wantHeader: "Authorization", wantValue: "Bearer T"},
		{name: "custom prefix", edit: func(a *projectfile.Auth) { a.Prefix = &token }, wantHeader: "Authorization", wantValue: "Token T"},
		{name: "no prefix", edit: func(a *projectfile.Auth) { a.Prefix = &empty }, wantHeader: "Authorization", wantValue: "T"},
		{name: "custom header", edit: func(a *projectfile.Auth) { a.Key = "X-Token" }, wantHeader: "X-Token", wantValue: "Bearer T"},
		{name: "query", edit: func(a *projectfile.Auth) { a.In = "query" }, wantURL: "https://api.test/x?a=1&access_token=T#f"},
		{name: "query named", edit: func(a *projectfile.Auth) { a.In = "query"; a.Key = "tok en" }, wantURL: "https://api.test/x?a=1&tok%20en=T#f"},
		{name: "query typed wins", edit: func(a *projectfile.Auth) { a.In = "query" }, req: projectfile.HTTPRequest{URL: "https://api.test/x?access_token=mine"}, wantURL: "https://api.test/x?access_token=mine"},
		{
			name: "row wins", req: projectfile.HTTPRequest{Headers: []projectfile.KeyValue{{Name: "authorization", Value: "Basic x"}}},
			wantHeader: "authorization", wantValue: "Basic x", suppressed: SuppressedOverridden,
		},
		{
			name: "omitted", req: projectfile.HTTPRequest{Settings: projectfile.RequestSettings{OmitHeaders: []string{"Authorization"}}},
			suppressed: SuppressedOmitted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: "https://idp.test/token", ClientID: "app"}
			if tt.edit != nil {
				tt.edit(&a)
			}
			req := tt.req
			req.Method = "GET"
			if req.URL == "" {
				req.URL = "https://api.test/x?a=1#f"
			}
			b, err := BuildHTTP(req, a, HTTPOptions{})
			if err != nil {
				t.Fatal(err)
			}
			b.UseOAuth2Token("T")
			if tt.wantURL != "" && b.Request.URL != tt.wantURL {
				t.Errorf("URL = %s", b.Request.URL)
			}
			if tt.wantHeader != "" {
				if got, _ := headerOf(b, tt.wantHeader); got != tt.wantValue {
					t.Errorf("%s = %q", tt.wantHeader, got)
				}
			}
			if tt.suppressed != "" {
				if len(b.Suppressed) != 1 || b.Suppressed[0].Reason != tt.suppressed || b.Suppressed[0].Value != "Bearer T" {
					t.Errorf("suppressed = %+v", b.Suppressed)
				}
				if tt.suppressed == SuppressedOmitted {
					if _, ok := headerOf(b, "Authorization"); ok {
						t.Error("omitted header sent")
					}
				}
			}
		})
	}
}

func TestOAuth2Placeholder(t *testing.T) {
	t.Parallel()
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"},
		projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: "https://idp.test/token", ClientSecret: "cs"}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b.UseOAuth2Placeholder()
	if got, _ := headerOf(b, "Authorization"); got != "Bearer "+OAuth2Placeholder || !b.OAuth2.Placeholder || slices.Contains(b.AuthSecrets, OAuth2Placeholder) {
		t.Errorf("header %q, pending %+v, secrets %q", got, b.OAuth2, b.AuthSecrets)
	}
}

func TestOAuth2Errors(t *testing.T) {
	t.Parallel()
	base := projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: "https://idp.test/token", ClientID: "app"}
	tests := []struct {
		name  string
		edit  func(*projectfile.Auth)
		kind  errs.Kind
		code  string
		field string
	}{
		{"authorization code without its URL", func(a *projectfile.Auth) { a.GrantType = "authorization_code" }, errs.InvalidArgument, CodeAuthInvalid, "authUrl"},
		{"authorization code to a web redirect", func(a *projectfile.Auth) {
			a.GrantType, a.AuthURL, a.RedirectURL = "authorization_code", "https://idp.test/authorize", "https://oauth.pstmn.io/v1/callback"
		}, errs.InvalidArgument, CodeAuthInvalid, "redirectUrl"},
		{"password without a username", func(a *projectfile.Auth) { a.GrantType = "password" }, errs.InvalidArgument, CodeAuthInvalid, "username"},
		{"control in password", func(a *projectfile.Auth) { a.GrantType, a.Username, a.Password = "password", "ada", "p\x00" }, errs.InvalidArgument, CodeAuthInvalid, "password"},
		{"implicit to a web redirect", func(a *projectfile.Auth) {
			a.GrantType, a.AuthURL, a.RedirectURL = "implicit", "https://idp.test/authorize", "https://app.test/cb"
		}, errs.InvalidArgument, CodeAuthInvalid, "redirectUrl"},
		{"unknown grant", func(a *projectfile.Auth) { a.GrantType = "urn:x" }, errs.Unsupported, CodeAuthUnsupported, "grantType"},
		{"control in issuer", func(a *projectfile.Auth) { a.Issuer = "a\tb" }, errs.InvalidArgument, CodeAuthInvalid, "issuer"},
		{"unknown client auth", func(a *projectfile.Auth) { a.ClientAuth = "jwt" }, errs.Unsupported, CodeAuthUnsupported, "clientAuth"},
		{"unknown in", func(a *projectfile.Auth) { a.In = "body" }, errs.Unsupported, CodeAuthUnsupported, "in"},
		{"not http", func(a *projectfile.Auth) { a.TokenURL = "ftp://idp.test/token" }, errs.InvalidArgument, CodeAuthInvalid, "tokenUrl"},
		{"relative", func(a *projectfile.Auth) { a.TokenURL = "/token" }, errs.InvalidArgument, CodeAuthInvalid, "tokenUrl"},
		{"control in scope", func(a *projectfile.Auth) { a.Scope = "a\nb" }, errs.InvalidArgument, CodeAuthInvalid, "scope"},
		{"bad header name", func(a *projectfile.Auth) { a.Key = "X Token" }, errs.InvalidArgument, CodeAuthInvalid, "key"},
		{"missing secret", func(a *projectfile.Auth) { a.LocalSecrets = []string{"clientSecret"} }, errs.InvalidArgument, CodeAuthSecretMissing, "clientSecret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := base
			tt.edit(&a)
			_, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{})
			e, ok := err.(*errs.Error)
			if !ok || e.Kind != tt.kind || e.Code != tt.code || e.Details[DetailAuthField] != tt.field || e.Details[DetailAuthType] != "oauth2" {
				t.Fatalf("err = %#v", err)
			}
		})
	}

	// Nothing without a token URL (for the implicit grant, an
	// authorization URL), and nothing when disabled.
	for _, a := range []projectfile.Auth{
		{Type: projectfile.AuthOAuth2, ClientID: "app"}, {Type: projectfile.AuthOAuth2, TokenURL: "https://idp.test/t", Disabled: true},
		{Type: projectfile.AuthOAuth2, GrantType: "implicit", TokenURL: "https://idp.test/t"},
	} {
		b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{})
		if err != nil || b.OAuth2 != nil {
			t.Errorf("%+v: pending %+v, err %v", a, b.OAuth2, err)
		}
	}
}

// The authorization code grant (AUTH-07): its URLs are rendered and
// carried in the token request, which gets its code later; the key does
// not change with the state.
func TestOAuth2AuthorizationCodeBuild(t *testing.T) {
	t.Parallel()
	a := projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: "authorization_code", TokenURL: "{{idp}}/token", AuthURL: "{{idp}}/authorize",
		RedirectURL: "http://localhost:{{port}}/cb", ClientID: "app", Scope: "openid", State: "s1"}
	vars := map[string]string{"idp": "https://idp.test", "port": "8765"}
	expand := func(s string) (string, error) {
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		return s, nil
	}
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{Expand: expand})
	if err != nil || b.OAuth2 == nil {
		t.Fatalf("build: %v", err)
	}
	c := b.OAuth2.Token.Config
	if c.AuthURL != "https://idp.test/authorize" || c.RedirectURL != "http://localhost:8765/cb" || c.TokenURL != "https://idp.test/token" || c.State != "s1" {
		t.Errorf("config = %+v", c)
	}
	a.State = "s2"
	b2, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{Expand: expand})
	if err != nil || b2.OAuth2.Key != b.OAuth2.Key {
		t.Errorf("the state changed the key: %v", err)
	}
}

// A GraphQL request carries the pending auth through its own build; a
// subscription over WebSocket refuses it.
func TestOAuth2GraphQL(t *testing.T) {
	t.Parallel()
	a := projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: "https://idp.test/token", ClientID: "app", In: "query"}
	b, err := BuildGraphQL(projectfile.GraphQLRequest{URL: "https://api.test/graphql", Method: "GET", Query: "{ a }"}, a, HTTPOptions{})
	if err != nil || b.OAuth2 == nil {
		t.Fatalf("build: %v", err)
	}
	b.UseOAuth2Token("T")
	if !strings.HasSuffix(b.Request.URL, "&access_token=T") || !strings.Contains(b.Request.URL, "query=") {
		t.Errorf("URL = %s", b.Request.URL)
	}
	_, err = BuildGraphQLWS(projectfile.GraphQLRequest{URL: "https://api.test/graphql", Query: "subscription { a }"}, a, HTTPOptions{})
	if e, ok := err.(*errs.Error); !ok || e.Code != CodeAuthUnsupported {
		t.Errorf("ws: %v", err)
	}
}

// The password grant (AUTH-09) carries the username and password, the
// password a secret; the implicit grant (AUTH-10) needs no token URL and
// sends no client secret; either can send the ID token (AUTH-14).
func TestOAuth2PasswordAndImplicitBuild(t *testing.T) {
	t.Parallel()
	expand := expandWith(map[string]string{"pw": "hunter2!", "user": "ada"})
	a := projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: "password", TokenURL: "https://idp.test/token", ClientID: "app",
		Username: "{{user}}", Password: "{{pw}}", Scope: "openid", Issuer: " https://idp.test ", IDToken: true}
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{Expand: expand})
	if err != nil || b.OAuth2 == nil {
		t.Fatalf("build: %v", err)
	}
	c := b.OAuth2.Token.Config
	if c.Username != "ada" || c.Password != "hunter2!" || c.Issuer != "https://idp.test" || !b.OAuth2.IDToken ||
		!slices.Contains(b.AuthSecrets, "hunter2!") {
		t.Errorf("config = %+v, secrets %q", c, b.AuthSecrets)
	}
	if got := oauth2.Encode(b.OAuth2.Token.Form()); got != "grant_type=password&username=ada&password=hunter2%21&scope=openid&client_id=app" {
		t.Errorf("form = %s", got)
	}

	a = projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: "implicit", AuthURL: "https://idp.test/authorize", ClientID: "spa", ClientSecret: "ignored"}
	b, err = BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.test/"}, a, HTTPOptions{})
	if err != nil || b.OAuth2 == nil {
		t.Fatalf("implicit build: %v", err)
	}
	if c := b.OAuth2.Token.Config; c.TokenURL != "" || c.AuthURL != "https://idp.test/authorize" || c.ClientSecret != "" || b.OAuth2.IDToken {
		t.Errorf("implicit config = %+v", c)
	}
}
