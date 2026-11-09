package request

// authoauth2.go maps an oauth2 auth (AUTH-07…10, AUTH-12, AUTH-14): its fields are
// rendered and checked with the rest of the auth into a token request
// (package oauth2), which the build carries as HTTPBuild.OAuth2. Getting
// the token is network traffic, so it is not part of the build: the app
// gets it (from its token store, or from the token endpoint through its
// HTTP client, as a user operation) and hands it to UseOAuth2Token, which
// adds it to the request. See docs/auth.md#oauth-20-auth-08-auth-12-auth-33.

import (
	"net/url"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// OAuth2Placeholder is the token shown where an oauth2 auth's token is
// not at hand: a copy or preview of a request whose token has not been
// obtained in this session (UseOAuth2Placeholder).
const OAuth2Placeholder = "…"

// OAuth2Pending is an oauth2 auth rendered and checked: the token request
// that gets its token, and where the token goes.
type OAuth2Pending struct {
	// Token is the request to the token endpoint.
	Token oauth2.TokenRequest
	// Key is the token's key in a token store (oauth2.TokenRequest.Key).
	Key string
	// In is "header" or "query"; Name the header ("" is Authorization)
	// or the query parameter ("" is access_token); Prefix goes before the
	// token in a header.
	In, Name, Prefix string
	// IDToken sends the OpenID Connect ID token of the token response
	// instead of its access token (AUTH-14).
	IDToken bool
	// Applied is set once UseOAuth2Token (or UseOAuth2Placeholder) has
	// added the token; Placeholder when that was the placeholder.
	Applied, Placeholder bool

	// omit are the request's settings.omitHeaders.
	omit []string
}

// OAuth2 returns the oauth2 auth AuthWith rendered and checked, nil for
// another type or an auth that sends nothing: its token request and key,
// for what needs the token without building the whole request (the Auth
// tab's token status).
func (r AuthResult) OAuth2() *OAuth2Pending { return r.oauth2 }

func (m authMapper) oauth2() (AuthResult, error) {
	a := m.auth
	in := a.In
	if in == "" {
		in = projectfile.APIKeyInHeader
	}
	if in != projectfile.APIKeyInHeader && in != projectfile.APIKeyInQuery {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send an OAuth 2.0 token there.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send the token in a header or the query string."), "in")
	}
	switch a.ClientAuth {
	case "", projectfile.OAuth2ClientAuthHeader, projectfile.OAuth2ClientAuthBody:
	default:
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send the client credentials that way.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send the client credentials in the Authorization header or in the body."), "clientAuth")
	}
	v, err := m.fields(
		"grantType", a.GrantType, "tokenUrl", a.TokenURL, "clientId", a.ClientID, "clientSecret", a.ClientSecret,
		"scope", a.Scope, "audience", a.Audience, "resource", a.Resource, "state", a.State,
		"prefix", a.BearerPrefix(), "key", a.Key, "authUrl", a.AuthURL, "redirectUrl", a.RedirectURL,
		"username", a.Username, "password", a.Password, "issuer", a.Issuer,
	)
	if err != nil {
		return AuthResult{}, err
	}
	grant, tokenURL, clientID, clientSecret := v[0], strings.TrimSpace(v[1]), v[2], v[3]
	scope, audience, resource, state, prefix, name := v[4], v[5], v[6], v[7], v[8], v[9]
	authURL, redirectURL := strings.TrimSpace(v[10]), strings.TrimSpace(v[11])
	username, password, issuer := v[12], v[13], strings.TrimSpace(v[14])
	// The implicit grant has no token request: its authorization URL is
	// what it needs.
	implicit := grant == oauth2.GrantImplicit
	if (!implicit && tokenURL == "") || (implicit && authURL == "") {
		return AuthResult{}, nil
	}
	if !oauth2.Supported(grant) {
		_, gerr := oauth2.GrantParams(oauth2.Config{Grant: grant})
		e, _ := gerr.(*errs.Error)
		return AuthResult{}, m.detail(e.WithCode(CodeAuthUnsupported), "grantType")
	}
	if implicit {
		tokenURL = ""
	} else if u, perr := url.Parse(tokenURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return AuthResult{}, m.invalid("tokenUrl", "The OAuth 2.0 token URL is not an http or https URL.",
			"Enter the authorization server's token endpoint, e.g. https://auth.example.com/oauth/token.")
	}
	for _, f := range []struct{ name, label, value string }{
		{"clientId", "client ID", clientID}, {"clientSecret", "client secret", clientSecret},
		{"scope", "scope", scope}, {"audience", "audience", audience}, {"resource", "resource", resource}, {"state", "state", state},
		{"username", "username", username}, {"password", "password", password}, {"issuer", "issuer", issuer},
	} {
		if hasControl(f.value) {
			return AuthResult{}, m.invalid(f.name, "The OAuth 2.0 "+f.label+" contains a line break or another control character.",
				"Remove control characters from the "+f.label+", or from the variables it uses.")
		}
	}
	cfg := oauth2.Config{
		Grant: grant, TokenURL: tokenURL, ClientID: clientID, ClientSecret: clientSecret,
		ClientAuth: a.ClientAuth, Scope: scope, Audience: audience, Resource: resource, State: state, Issuer: issuer,
	}
	switch cfg.GrantType() {
	case oauth2.GrantPassword:
		// AUTH-09: the resource owner's credentials go in the token
		// request.
		if strings.TrimSpace(username) == "" {
			return AuthResult{}, m.invalid("username", "The OAuth 2.0 password grant needs a username.",
				"Enter the username of the account to get the token for.")
		}
		cfg.Username, cfg.Password = username, password
	case oauth2.GrantImplicit:
		// AUTH-10: a public client; the client secret is not sent.
		cfg.ClientSecret = ""
	}
	if g := cfg.GrantType(); g == oauth2.GrantAuthorizationCode || g == oauth2.GrantImplicit {
		// The browser leg (AUTH-07, AUTH-10): the authorization endpoint,
		// and the loopback redirect the code or token comes back to.
		if u, perr := url.Parse(authURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return AuthResult{}, m.invalid("authUrl", "The OAuth 2.0 authorization URL is not an http or https URL.",
				"Enter the authorization server's authorization endpoint, e.g. https://auth.example.com/authorize.")
		}
		if _, rerr := oauth2.ParseRedirectURL(redirectURL); rerr != nil {
			e, _ := rerr.(*errs.Error)
			return AuthResult{}, m.detail(errs.New(errs.InvalidArgument, e.Message).WithCode(CodeAuthInvalid).WithHint(e.Hint), "redirectUrl")
		}
		cfg.AuthURL, cfg.RedirectURL = authURL, redirectURL
	}
	for _, p := range a.TokenParams {
		if p.Disabled {
			continue
		}
		pv, err := m.fields("tokenParams", p.Name, "tokenParams", p.Value)
		if err != nil {
			return AuthResult{}, err
		}
		if strings.TrimSpace(pv[0]) == "" {
			continue
		}
		cfg.Params = append(cfg.Params, oauth2.Param{Name: pv[0], Value: pv[1]})
	}
	if in == projectfile.APIKeyInHeader {
		if name == "" {
			name = AuthorizationHeader
		}
		if !isToken(name) {
			return AuthResult{}, m.invalid("key", "The OAuth 2.0 token's header name is not a valid header name.",
				"Header names may contain only letters, digits and !#$%&'*+-.^_`|~, with no spaces.")
		}
		if hasHeaderBreak(prefix) {
			return AuthResult{}, m.invalid("prefix", "The OAuth 2.0 token prefix contains a line break or another control character.",
				"Remove control characters from the prefix, or from the variables it uses.")
		}
	} else if name == "" {
		name = projectfile.DefaultOAuth2QueryParam
	}
	req, err := oauth2.NewTokenRequest(cfg)
	if err != nil {
		return AuthResult{}, m.detail(err.(*errs.Error), "grantType")
	}
	p := &OAuth2Pending{Token: req, Key: req.Key(), In: in, Name: name, Prefix: prefix, IDToken: a.IDToken}
	return AuthResult{oauth2: p, Secrets: req.Secrets()}, nil
}

// UseOAuth2Token adds token, an access (or ID) token of b's oauth2 auth, to the
// request: in the header (with the prefix) unless an enabled row of that
// name overrides it or settings.omitHeaders leaves it out (both listed in
// Suppressed), or in the query string unless the URL has a parameter of
// that name. The token joins AuthSecrets. It does nothing without a
// pending oauth2 auth, or once a token was added.
func (b *HTTPBuild) UseOAuth2Token(token string) {
	if b.OAuth2 == nil || b.OAuth2.Applied {
		return
	}
	// A copy: builds copied before the token was added share the pending
	// auth, and each gets its own token (a stream's reconnection).
	c := *b.OAuth2
	c.Applied = true
	p := &c
	b.OAuth2 = p
	// The slices are clipped so that appending never writes into an array
	// another copy of the build shares.
	b.AuthSecrets = append(slices.Clip(b.AuthSecrets), token)
	b.Request.Headers = slices.Clip(b.Request.Headers)
	b.HeaderSources = slices.Clip(b.HeaderSources)
	b.Suppressed = slices.Clip(b.Suppressed)
	if p.In == projectfile.APIKeyInQuery {
		b.Request.URL = WithAuthQuery(b.Request.URL, []QueryParam{{Name: p.Name, Value: token}})
		return
	}
	value := token
	if p.Prefix != "" {
		value = p.Prefix + " " + token
	}
	switch {
	case hasHeader(b.Request.Headers, p.Name):
		b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: p.Name, Value: value, Source: HeaderFromAuth, Reason: SuppressedOverridden})
	case omitSet(p.omit)[strings.ToLower(p.Name)]:
		b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: p.Name, Value: value, Source: HeaderFromAuth, Reason: SuppressedOmitted})
	default:
		b.addAuthHeader(p.Name, value)
	}
}

// UseOAuth2Placeholder adds OAuth2Placeholder in place of the token, for
// a copy or preview made without one.
func (b *HTTPBuild) UseOAuth2Placeholder() {
	if b.OAuth2 == nil || b.OAuth2.Applied {
		return
	}
	b.UseOAuth2Token(OAuth2Placeholder)
	b.OAuth2.Placeholder = true
	// The placeholder is not a secret.
	b.AuthSecrets = b.AuthSecrets[:len(b.AuthSecrets)-1]
}

// OAuth2Settings are the settings of the token request of b: the
// request's timeouts, HTTP version and URL encoding (redirects are not
// followed; oauth2.TokenRequest.HTTP).
func (b *HTTPBuild) OAuth2Settings() httpclient.Settings {
	s := httpclient.DefaultSettings()
	s.Timeout = b.Request.Settings.Timeout
	s.ResponseHeaderTimeout = b.Request.Settings.ResponseHeaderTimeout
	s.HTTPVersion = b.Request.Settings.HTTPVersion
	s.SkipSendingCookies, s.SkipStoringCookies = true, true
	return s
}
