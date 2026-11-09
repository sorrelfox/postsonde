// Package oauth2 is the client side of OAuth 2.0 (RFC 6749) that an
// oauth2 auth needs (AUTH-08, AUTH-12, AUTH-33): the configuration of a
// token endpoint client, the parameters of each grant, the token request
// with client authentication in the Authorization header
// (client_secret_basic) or the body (client_secret_post), the token
// response and its errors (section 5), token expiry, and a Store for
// obtained tokens.
//
// The package sends nothing by itself: Exchange sends the token request
// through a Doer, which in the app is the app's HTTP client (its dialer
// with the user-operation guard, TLS trust, client certificates and proxy
// settings), so the exchange is recorded like any other request and shown
// in the timeline.
//
// Grants: this version runs the client credentials grant (section 4.4),
// the authorization code grant with PKCE (section 4.1, RFC 7636;
// Authorize runs its browser leg on a loopback redirect, authcode.go), the
// resource owner password credentials grant (section 4.3) and the implicit
// grant (section 4.2; AuthorizeImplicit, implicit.go, whose token comes in
// the redirect's fragment), and refreshes tokens with their refresh token
// (section 6, RefreshRequest). An OpenID Connect ID token that comes with
// a token is decoded and its claims checked (idtoken.go).
//
// Standard library only. See docs/auth.md#oauth-20-auth-08-auth-12-auth-33.
package oauth2

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Grant types, as the file format and the grant_type parameter name them.
const (
	// GrantClientCredentials is the client credentials grant (section
	// 4.4): the client authenticates and gets a token for itself.
	GrantClientCredentials = "client_credentials"
	// GrantAuthorizationCode is the authorization code grant (section
	// 4.1) with PKCE (RFC 7636): the user signs in in the browser
	// (Authorize), then the code is redeemed.
	GrantAuthorizationCode = "authorization_code"
	// GrantPassword is the resource owner password credentials grant
	// (section 4.3): the client sends the user's username and password.
	// A legacy grant, still common for internal servers.
	GrantPassword = "password"
	// GrantImplicit is the implicit grant (section 4.2): the user signs
	// in in the browser and the token comes back in the redirect's
	// fragment (AuthorizeImplicit), with no token request. Deprecated
	// (OAuth 2.0 Security BCP), for legacy servers only.
	GrantImplicit = "implicit"
	// GrantRefreshToken refreshes a token (section 6): not a grant of an
	// auth, but the token request of RefreshRequest.
	GrantRefreshToken = "refresh_token"
)

// Client authentication methods (section 2.3.1), as the file format names
// them.
const (
	// ClientAuthHeader sends the client ID and secret as HTTP Basic
	// credentials (client_secret_basic), each form-encoded first. The
	// default.
	ClientAuthHeader = "header"
	// ClientAuthBody sends client_id and client_secret in the form body
	// (client_secret_post).
	ClientAuthBody = "body"
)

// FormType is the media type of a token request's body.
const FormType = "application/x-www-form-urlencoded"

// Error codes of the package.
const (
	// CodeUnsupportedGrant: the grant type is one this version does not
	// run (kind unsupported). Details: grantType.
	CodeUnsupportedGrant = "oauth2.unsupported_grant"
	// CodeTokenError: the authorization server answered the token request
	// with an error response (section 5.2; kind permission_denied for
	// invalid_client and unauthorized_client, invalid_argument for the
	// others). Details: error, errorDescription and errorUri when sent,
	// status.
	CodeTokenError = "oauth2.token_error"
	// CodeTokenHTTPStatus: the token request was answered with a status
	// other than 2xx and without an error response (kind protocol).
	// Details: status.
	CodeTokenHTTPStatus = "oauth2.token_http_status"
	// CodeMalformedResponse: a 2xx token response that is not a token
	// response: not a JSON object (or a form), no access_token, or a
	// value of the wrong type (kind parse). Details: status, and field
	// for a member.
	CodeMalformedResponse = "oauth2.malformed_response"
	// CodeTokenRequestFailed: the token request could not be sent or its
	// response not read. The kind and code of the cause are kept when it
	// has them (a timeout, a refused connection); this code is set on
	// causes without one. Details: tokenUrl (without its query).
	CodeTokenRequestFailed = "oauth2.token_request_failed"
)

// Param is a request parameter, not encoded.
type Param struct {
	Name  string
	Value string
}

// Config is how a client talks to a token endpoint: the fields of an
// oauth2 auth, rendered.
type Config struct {
	// Grant is the grant type (Grant*); "" is GrantClientCredentials.
	Grant string
	// TokenURL is the token endpoint, an absolute http or https URL.
	TokenURL string
	// ClientID and ClientSecret are the client's credentials. An empty
	// secret sends none (a public client).
	ClientID     string
	ClientSecret string
	// ClientAuth is ClientAuthHeader (also "") or ClientAuthBody.
	ClientAuth string
	// Scope is the space-separated scope list; "" sends none.
	Scope string
	// Audience and Resource are sent when not "": audience as many
	// providers (Auth0, Okta) take it, resource as RFC 8707 defines it.
	Audience string
	Resource string
	// State is sent with the authorization request of the redirect grants
	// and checked on the way back ("" sends a random one); the client
	// credentials grant has no redirect and does not send it.
	State string
	// AuthURL is the authorization endpoint of the redirect grants
	// (authorization code, implicit); RedirectURL their loopback redirect
	// URL ("" is http://127.0.0.1:<random port>/callback,
	// ParseRedirectURL).
	AuthURL     string
	RedirectURL string
	// Code is what the authorization code grant's redirect brought
	// (Authorize), which its token request redeems; nil before.
	Code *AuthorizationCode
	// Username and Password are the resource owner's credentials of the
	// password grant (section 4.3.2).
	Username string
	Password string
	// Issuer is the issuer an ID token must name (its iss claim), ""
	// for any non-empty one (CheckIDToken).
	Issuer string
	// Nonce is the nonce sent with the authorization request of this
	// sign-in, which an ID token must repeat; "" when none was sent
	// (the grants without a browser, a refresh).
	Nonce string
	// Params are extra parameters of the token request, sent after the
	// others, in order.
	Params []Param
}

// GrantType returns c's grant type, GrantClientCredentials for "".
func (c Config) GrantType() string {
	if c.Grant == "" {
		return GrantClientCredentials
	}
	return c.Grant
}

// Supported reports whether this version runs grant ("" is client
// credentials).
func Supported(grant string) bool {
	switch grant {
	case "", GrantClientCredentials, GrantAuthorizationCode, GrantPassword, GrantImplicit:
		return true
	}
	return false
}

// HasTokenRequest reports whether grant gets its token from the token
// endpoint: every grant but the implicit one, whose token comes in the
// redirect (AuthorizeImplicit).
func HasTokenRequest(grant string) bool { return grant != GrantImplicit }

// GrantParams returns the parameters of c's grant for the token request,
// grant_type first: for client credentials grant_type, scope, audience
// and resource (those not ""); for the password grant grant_type,
// username, password, then scope, audience and resource (section 4.3.2);
// for the authorization code grant grant_type, then the code,
// redirect_uri and code_verifier of c.Code (section 4.1.3; only
// grant_type while c.Code is nil). The implicit grant has no token
// request: grant_type only, never sent. A grant this version does not
// run fails with CodeUnsupportedGrant.
func GrantParams(c Config) ([]Param, error) {
	grant := c.GrantType()
	if !Supported(grant) {
		return nil, errs.New(errs.Unsupported, "PostSonde cannot get OAuth 2.0 tokens with the "+grantLabel(grant)+" yet.").
			WithCode(CodeUnsupportedGrant).
			WithHint("Use the client credentials, authorization code, password or implicit grant, or get the token by hand and send it as a bearer token.").
			WithDetail("grantType", grant)
	}
	out := []Param{{"grant_type", grant}}
	switch grant {
	case GrantImplicit:
		return out, nil
	case GrantPassword:
		out = append(out, Param{"username", c.Username}, Param{"password", c.Password})
	}
	if grant == GrantAuthorizationCode {
		if c.Code != nil {
			out = append(out, Param{"code", c.Code.Code}, Param{"redirect_uri", c.Code.RedirectURI}, Param{"code_verifier", c.Code.Verifier})
		}
		return out, nil
	}
	for _, p := range []Param{{"scope", c.Scope}, {"audience", c.Audience}, {"resource", c.Resource}} {
		if p.Value != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// grantLabel names a grant for messages.
func grantLabel(grant string) string {
	switch grant {
	case GrantAuthorizationCode:
		return "authorization code grant"
	case GrantPassword:
		return "password grant"
	case GrantImplicit:
		return "implicit grant"
	case GrantRefreshToken:
		return "refresh token grant"
	}
	if len(grant) > 40 {
		grant = grant[:40] + "…"
	}
	return "grant “" + grant + "”"
}

// RefreshRequest returns the request that refreshes a token of c with its
// refresh token (section 6): grant_type=refresh_token and the refresh
// token, the client authenticating as for c's grant. The scope is left
// out, so the server grants the scope of the token being refreshed, and
// so are c's extra parameters, which belong to the grant.
func RefreshRequest(c Config, refreshToken string) TokenRequest {
	c.Params = nil
	return TokenRequest{Config: c, Grant: []Param{{"grant_type", GrantRefreshToken}, {"refresh_token", refreshToken}}}
}

// TokenRequest is a request to the token endpoint (section 3.2).
type TokenRequest struct {
	Config Config
	// Grant are the grant's parameters (GrantParams), grant_type first.
	Grant []Param
}

// NewTokenRequest returns the token request of c's grant (GrantParams).
func NewTokenRequest(c Config) (TokenRequest, error) {
	grant, err := GrantParams(c)
	if err != nil {
		return TokenRequest{}, err
	}
	return TokenRequest{Config: c, Grant: grant}, nil
}

// WithCode returns r redeeming code, what the authorization code grant's
// redirect brought (Authorize): its grant parameters with the code,
// redirect_uri and code_verifier, and the nonce an ID token must repeat.
// Its Key does not change.
func (r TokenRequest) WithCode(code AuthorizationCode) TokenRequest {
	r.Config.Code = &code
	r.Config.Nonce = code.Nonce
	r.Grant, _ = GrantParams(r.Config)
	return r
}

// Form returns the parameters of the body, in order: the grant's, then
// client_id (and client_secret) with ClientAuthBody or for a public client
// (no secret), then Config.Params. With ClientAuthHeader the client ID and
// secret go in the Authorization header instead (HTTP).
func (r TokenRequest) Form() []Param {
	out := append([]Param(nil), r.Grant...)
	if r.Config.ClientAuth == ClientAuthBody || r.Config.ClientSecret == "" {
		if r.Config.ClientID != "" {
			out = append(out, Param{"client_id", r.Config.ClientID})
		}
		if r.Config.ClientSecret != "" {
			out = append(out, Param{"client_secret", r.Config.ClientSecret})
		}
	}
	return append(out, r.Config.Params...)
}

// Encode returns params as an application/x-www-form-urlencoded body
// (names and values encoded as url.QueryEscape does, space as "+").
func Encode(params []Param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Name))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

// BasicCredentials returns the Authorization value of client_secret_basic
// for id and secret: "Basic " and base64 of the form-encoded id, ":", and
// the form-encoded secret (section 2.3.1).
func BasicCredentials(id, secret string) string {
	raw := url.QueryEscape(id) + ":" + url.QueryEscape(secret)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

// HTTP returns the request that asks for the token, with settings as its
// settings (timeouts; redirects are never followed, so the client
// credentials never go to another address): a POST of the form to the
// token URL, with Accept: application/json and, with ClientAuthHeader, a
// client ID and a secret, the Basic credentials. A public client (no
// secret) does not authenticate: it sends its client_id in the form
// (section 4.1.3).
func (r TokenRequest) HTTP(settings httpclient.Settings) httpclient.Request {
	headers := []httpclient.Header{{Name: "Accept", Value: "application/json", Enabled: true}}
	if r.Config.ClientAuth != ClientAuthBody && r.Config.ClientID != "" && r.Config.ClientSecret != "" {
		headers = append(headers, httpclient.Header{Name: "Authorization", Value: BasicCredentials(r.Config.ClientID, r.Config.ClientSecret), Enabled: true})
	}
	settings.FollowRedirects = false
	settings.MaxRedirects = 0
	if settings.MaxBodyBytes == 0 {
		settings.MaxBodyBytes = MaxResponseBytes
	}
	return httpclient.Request{
		Method:   "POST",
		URL:      r.Config.TokenURL,
		Headers:  headers,
		Body:     httpclient.RawBody{Data: []byte(Encode(r.Form())), Type: FormType},
		Settings: settings,
	}
}

// Secrets returns the values of r to mask wherever it is shown: the
// password of the password grant, and the client secret, as typed and as
// the Basic credentials carry it.
func (r TokenRequest) Secrets() []string {
	var out []string
	if r.Config.Password != "" && r.Config.GrantType() == GrantPassword {
		out = append(out, r.Config.Password)
	}
	if r.Config.ClientSecret == "" {
		return out
	}
	out = append(out, r.Config.ClientSecret)
	if r.Config.ClientAuth != ClientAuthBody && r.Config.ClientID != "" {
		v := BasicCredentials(r.Config.ClientID, r.Config.ClientSecret)
		out = append(out, strings.TrimPrefix(v, "Basic "))
	}
	return out
}

// Key returns the key of r's tokens in a Store: a hash of everything that
// makes the token (grant, endpoints, redirect URL, client, secret, scope,
// audience, resource, the password grant's username and password, the
// expected issuer, parameters), so a changed field gets a new token.
// The secrets are part of the hash only, never of the key's text. What one
// sign-in brings (the code, the state, the nonce) is not part of it.
func (r TokenRequest) Key() string {
	h := sha256.New()
	write := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	c := r.Config
	write(c.GrantType())
	write(c.TokenURL)
	write(c.ClientID)
	write(c.ClientSecret)
	write(c.ClientAuth)
	write(c.Scope)
	write(c.Audience)
	write(c.Resource)
	write(c.AuthURL)
	write(c.RedirectURL)
	if c.GrantType() == GrantPassword {
		write(c.Username)
		write(c.Password)
	}
	if c.Issuer != "" {
		// Written only when set, so the keys of tokens stored before
		// the field existed stay the same.
		write("\x00issuer")
		write(c.Issuer)
	}
	for _, p := range c.Params {
		write(p.Name)
		write(p.Value)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
