package oauth2

// authcode.go is the browser leg of the authorization code grant with
// PKCE (RFC 6749 section 4.1, RFC 7636, RFC 8252 for native apps): the
// authorization URL, a loopback listener for the redirect, and the code
// it brings back. The token request that follows is an ordinary
// TokenRequest (GrantParams with Config.Code). The implicit grant runs the
// same leg (signIn) with its own handler (implicit.go).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// DefaultRedirectPath is the path of the redirect URL when the auth names
// none: http://127.0.0.1:<random port>/callback.
const DefaultRedirectPath = "/callback"

// DefaultAuthorizeTimeout is how long Authorize waits for the redirect
// when AuthorizeOptions.Timeout is 0.
const DefaultAuthorizeTimeout = 5 * time.Minute

// Error codes of the authorization code grant.
const (
	// CodeInvalidRedirectURL: the redirect URL is not a loopback http URL
	// (kind invalid_argument). Details: redirectUrl.
	CodeInvalidRedirectURL = "oauth2.invalid_redirect_url"
	// CodeInvalidAuthURL: the authorization URL is not an absolute http or
	// https URL (kind invalid_argument).
	CodeInvalidAuthURL = "oauth2.invalid_auth_url"
	// CodeBrowserFailed: the system browser could not be opened (kind
	// io, or the cause's).
	CodeBrowserFailed = "oauth2.browser_failed"
	// CodeStateMismatch: the redirect's state is not the one sent, so it
	// may not answer this sign-in (a forged or stale redirect; kind
	// permission_denied). Nothing of it is used.
	CodeStateMismatch = "oauth2.state_mismatch"
	// CodeAccessDenied: the user (or the server) refused the authorization,
	// error=access_denied (kind permission_denied). Details: error,
	// errorDescription and errorUri when sent.
	CodeAccessDenied = "oauth2.access_denied"
	// CodeAuthorizationError: the redirect carries another error (section
	// 4.1.2.1), or no code (kind protocol). Details: error,
	// errorDescription and errorUri when sent.
	CodeAuthorizationError = "oauth2.authorization_error"
	// CodeAuthorizationTimeout: no redirect came within the timeout (kind
	// timeout). Details: timeoutMs.
	CodeAuthorizationTimeout = "oauth2.authorization_timeout"
)

// AuthorizationCode is what the redirect of the authorization code grant
// brought, with what the token request needs to redeem it.
type AuthorizationCode struct {
	// Code is the authorization code.
	Code string
	// RedirectURI is the redirect_uri sent with the authorization request,
	// which the token request repeats (section 4.1.3).
	RedirectURI string
	// Verifier is the PKCE code verifier whose challenge was sent.
	Verifier string
	// Nonce is the nonce sent with the authorization request (when the
	// scope asks for openid), which the ID token must repeat; "" when
	// none was sent.
	Nonce string
}

// NewVerifier returns a PKCE code verifier (RFC 7636 section 4.1): 32
// random bytes from r (nil is crypto/rand) as base64url without padding,
// 43 characters of the unreserved set.
func NewVerifier(r io.Reader) (string, error) {
	return randomString(r, 32)
}

// Challenge returns the S256 code challenge of verifier (RFC 7636
// section 4.2): BASE64URL-ENCODE(SHA256(ASCII(verifier))).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewState returns a random state value: 16 bytes from r (nil is
// crypto/rand) as base64url.
func NewState(r io.Reader) (string, error) {
	return randomString(r, 16)
}

func randomString(r io.Reader, n int) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", errs.Wrap(err, errs.Internal, "PostSonde could not make a random value for the OAuth 2.0 sign-in.")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Redirect is a parsed loopback redirect URL (RFC 8252 section 7.3).
type Redirect struct {
	// Host is as written: an IPv4 loopback address, "[::1]" or
	// "localhost".
	Host string
	// Port is the port to listen on, 0 for one the OS picks.
	Port int
	// Path is the path the code comes back to, DefaultRedirectPath when
	// the URL has none.
	Path string
}

// URI returns the redirect URI for the listener's port.
func (r Redirect) URI(port int) string {
	return "http://" + r.Host + ":" + strconv.Itoa(port) + r.Path
}

// ParseRedirectURL reads the redirect URL of an auth: "" is
// http://127.0.0.1:<random port>/callback; otherwise an http URL whose
// host is a loopback address (127.0.0.0/8, [::1]) or localhost, with an
// optional port (none or 0: a random one) and path, and no query or
// fragment. Anything else fails with CodeInvalidRedirectURL: the code
// comes back to a listener on this computer, never to a web page.
func ParseRedirectURL(s string) (Redirect, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Redirect{Host: "127.0.0.1", Path: DefaultRedirectPath}, nil
	}
	bad := func(msg string) (Redirect, error) {
		return Redirect{}, errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidRedirectURL).
			WithHint("Leave it empty for http://127.0.0.1:<random port>/callback, or use http://127.0.0.1:<port>/<path> (or localhost) as registered with the authorization server.").
			WithDetail("redirectUrl", clip(s, 200))
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return bad("The OAuth 2.0 redirect URL must be an http:// URL on this computer.")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return bad("The OAuth 2.0 redirect URL must not have a query or a fragment.")
	}
	host := u.Hostname()
	shown := host
	switch {
	case strings.EqualFold(host, "localhost"):
		shown = "localhost"
	default:
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return bad("The OAuth 2.0 redirect URL must point to this computer (127.0.0.1, [::1] or localhost).")
		}
		if ip.To4() == nil {
			shown = "[" + host + "]"
		}
	}
	port := 0
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 65535 {
			return bad("The OAuth 2.0 redirect URL has an invalid port.")
		}
		port = n
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		path = DefaultRedirectPath
	}
	return Redirect{Host: shown, Port: port, Path: path}, nil
}

// ResponseType returns the response_type of c's authorization request:
// "code" for the authorization code grant; for the implicit grant
// "token", or "id_token token" when the scope asks for openid (OpenID
// Connect's implicit flow, Core section 3.2.2.1, which also sends the ID
// token).
func ResponseType(c Config) string {
	if c.GrantType() != GrantImplicit {
		return "code"
	}
	if HasOpenIDScope(c.Scope) {
		return "id_token token"
	}
	return "token"
}

// HasOpenIDScope reports whether the space-separated scope asks for
// openid, which makes the sign-in an OpenID Connect one.
func HasOpenIDScope(scope string) bool {
	for _, s := range strings.Fields(scope) {
		if s == "openid" {
			return true
		}
	}
	return false
}

// WantsNonce reports whether a sign-in of c sends a nonce (OpenID Connect
// Core sections 3.1.2.1 and 3.2.2.1): always for the implicit grant, and
// for the authorization code grant when the scope asks for openid. An ID
// token must then repeat it (CheckIDToken).
func WantsNonce(c Config) bool {
	return c.GrantType() == GrantImplicit || HasOpenIDScope(c.Scope)
}

// AuthorizationURL returns the URL the browser opens (section 4.1.1 with
// RFC 7636 section 4.3, or section 4.2.1): c's AuthURL with its own query
// kept, then response_type (ResponseType), client_id, redirect_uri,
// scope, state, code_challenge and code_challenge_method=S256 (when
// challenge is not ""), nonce (c.Nonce, when set), and audience and
// resource when set (Auth0 and Okta take the audience here; RFC 8707 the
// resource).
func AuthorizationURL(c Config, redirectURI, state, challenge string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(c.AuthURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errs.New(errs.InvalidArgument, "The OAuth 2.0 authorization URL is not an http or https URL.").
			WithCode(CodeInvalidAuthURL).
			WithHint("Enter the authorization server's authorization endpoint, e.g. https://auth.example.com/authorize.")
	}
	params := []Param{{"response_type", ResponseType(c)}, {"client_id", c.ClientID}, {"redirect_uri", redirectURI}}
	if c.Scope != "" {
		params = append(params, Param{"scope", c.Scope})
	}
	params = append(params, Param{"state", state})
	if challenge != "" {
		params = append(params, Param{"code_challenge", challenge}, Param{"code_challenge_method", "S256"})
	}
	for _, p := range []Param{{"nonce", c.Nonce}, {"audience", c.Audience}, {"resource", c.Resource}} {
		if p.Value != "" {
			params = append(params, p)
		}
	}
	q := Encode(params)
	if u.RawQuery != "" {
		q = u.RawQuery + "&" + q
	}
	u.RawQuery = q
	u.Fragment = ""
	return u.String(), nil
}

// AuthorizeOptions are what Authorize needs besides the configuration.
type AuthorizeOptions struct {
	// Open opens a URL in the system browser: the app's Browser.OpenURL,
	// or a test that follows the redirect itself. Required.
	Open func(url string) error
	// Timeout is how long to wait for the redirect; 0 is
	// DefaultAuthorizeTimeout.
	Timeout time.Duration
	// Rand is the randomness of the verifier, the state and the nonce;
	// nil is crypto/rand.
	Rand io.Reader
	// Now is the clock the implicit grant's token and ID token are read
	// with; nil is time.Now.
	Now func() time.Time
}

// Authorize runs the browser leg of the authorization code grant with
// PKCE for c: it listens on the loopback redirect URL (c.RedirectURL,
// through listen.Listen, so on this computer only), opens the
// authorization URL with o.Open, and waits for the redirect. The
// redirect's state must be the one sent (c.State, or a random one when it
// is ""): any other is refused (CodeStateMismatch) and nothing of it is
// used. The browser gets a short page saying how it went. The listener is
// closed before Authorize returns. When the scope asks for openid a random
// nonce is sent too, which the ID token must repeat (WantsNonce).
//
// It returns the code with the redirect URI, the verifier and the nonce
// the token request needs (TokenRequest.WithCode). It fails with
// CodeAccessDenied when the user refused, CodeAuthorizationError for
// another error or a redirect without a code, CodeAuthorizationTimeout
// after o.Timeout, and a cancelled error when ctx ends first.
func Authorize(ctx context.Context, c Config, o AuthorizeOptions) (AuthorizationCode, error) {
	c.Grant = GrantAuthorizationCode
	l, err := signIn(ctx, c, o)
	if err != nil {
		return AuthorizationCode{}, err
	}
	return AuthorizationCode{Code: l.values.Get("code"), RedirectURI: l.redirectURI, Verifier: l.verifier, Nonce: l.nonce}, nil
}

// leg is what a sign-in in the browser brought.
type leg struct {
	// values are the redirect's parameters (its query, or for the
	// implicit grant its fragment), the state checked.
	values url.Values
	// token is the implicit grant's token, read and checked.
	token       Token
	redirectURI string
	verifier    string
	nonce       string
}

// signIn runs the browser leg of c's grant (authorization code or
// implicit): the listener, the authorization URL opened with o.Open, and
// the wait for the first redirect.
func signIn(ctx context.Context, c Config, o AuthorizeOptions) (leg, error) {
	if o.Open == nil {
		return leg{}, errs.New(errs.Unsupported, "PostSonde cannot open a browser here for the OAuth 2.0 sign-in.").
			WithCode(CodeBrowserFailed)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	implicit := c.GrantType() == GrantImplicit
	redirect, err := ParseRedirectURL(c.RedirectURL)
	if err != nil {
		return leg{}, err
	}
	if _, err := AuthorizationURL(c, "", "", ""); err != nil {
		return leg{}, err
	}
	state := c.State
	if state == "" {
		if state, err = NewState(o.Rand); err != nil {
			return leg{}, err
		}
	}
	var out leg
	challenge := ""
	if !implicit {
		if out.verifier, err = NewVerifier(o.Rand); err != nil {
			return leg{}, err
		}
		challenge = Challenge(out.verifier)
	}
	c.Nonce = ""
	if WantsNonce(c) {
		if out.nonce, err = NewState(o.Rand); err != nil {
			return leg{}, err
		}
		c.Nonce = out.nonce
	}

	ln, info, err := listen.Listen(ctx, listen.Spec{Host: redirect.Host, Port: redirect.Port, DualStack: redirect.Host == "localhost"})
	if err != nil {
		return leg{}, err
	}
	out.redirectURI = redirect.URI(info.Port())
	results := make(chan redirectResult, 1)
	var handler http.Handler
	if implicit {
		read := func(q url.Values) (Token, error) { return implicitToken(c, q, o.Now()) }
		handler = implicitHandler(redirect.Path, redirect.Host+":"+strconv.Itoa(info.Port()), state, read, results)
	} else {
		handler = redirectHandler(redirect.Path, state, results)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	defer func() {
		// The page is written before the result is sent, so a short
		// grace lets it reach the browser; then the listener is gone.
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			_ = srv.Close()
		}
		<-served
	}()

	authURL, err := AuthorizationURL(c, out.redirectURI, state, challenge)
	if err != nil {
		return leg{}, err
	}
	if err := o.Open(authURL); err != nil {
		return leg{}, errs.Wrap(err, errs.IO, "PostSonde could not open the browser for the OAuth 2.0 sign-in.").
			WithCode(CodeBrowserFailed).
			WithHint("Check that a default web browser is set up.")
	}

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultAuthorizeTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-results:
		if r.err != nil {
			return leg{}, r.err
		}
		out.values, out.token = r.values, r.token
		return out, nil
	case <-timer.C:
		return leg{}, errs.New(errs.Timeout, "The OAuth 2.0 sign-in in the browser did not finish in time.").
			WithCode(CodeAuthorizationTimeout).
			WithHint("Sign in within "+timeout.String()+", or check that the authorization server redirects to "+out.redirectURI+".").
			WithDetail("timeoutMs", strconv.FormatInt(timeout.Milliseconds(), 10))
	case <-ctx.Done():
		return leg{}, errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "The OAuth 2.0 sign-in was cancelled.")
	}
}

// redirectResult is what a redirect brought: its parameters (and the
// implicit grant's token), or why there are none.
type redirectResult struct {
	values url.Values
	token  Token
	err    error
}

// checkRedirect checks a redirect's parameters q: the state first (a
// redirect with another state is refused, and nothing of it is used),
// then an error (sections 4.1.2.1, 4.2.2.1).
func checkRedirect(q url.Values, state string) error {
	switch {
	case q.Get("state") != state:
		return errs.New(errs.PermissionDenied, "The OAuth 2.0 redirect was refused: its state is not the one PostSonde sent.").
			WithCode(CodeStateMismatch).
			WithHint("The redirect may come from another sign-in or another site. Get a new token to sign in again.")
	case q.Get("error") != "":
		return authorizationError(q)
	}
	return nil
}

// redirectHandler answers the redirect of the authorization code grant to
// path: it checks the state first, then an error, then the code, answers
// the browser with a page, and sends the first result. Other paths are
// 404, other methods 405.
func redirectHandler(path, state string, results chan<- redirectResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		res := redirectResult{values: q, err: checkRedirect(q, state)}
		if res.err == nil && q.Get("code") == "" {
			res.err = errs.New(errs.Protocol, "The OAuth 2.0 redirect brought no authorization code.").
				WithCode(CodeAuthorizationError).
				WithHint("Check that the authorization URL is the server's authorization endpoint.")
		}
		writePage(w, res.err)
		send(results, res)
	})
}

// send sends res unless a result was sent already.
func send(results chan<- redirectResult, res redirectResult) {
	select {
	case results <- res:
	default: // a result was sent already
	}
}

// authorizationError is the error of a redirect with an error (section
// 4.1.2.1).
func authorizationError(q url.Values) error {
	code := clip(q.Get("error"), 60)
	msg := "The authorization server refused the sign-in: " + code + "."
	if d := clip(q.Get("error_description"), 300); d != "" {
		msg = "The authorization server refused the sign-in: " + code + " (" + d + ")."
	}
	var e *errs.Error
	if code == "access_denied" {
		e = errs.New(errs.PermissionDenied, msg).WithCode(CodeAccessDenied).
			WithHint("The sign-in was declined in the browser. Get a new token to try again.")
	} else {
		e = errs.New(errs.Protocol, msg).WithCode(CodeAuthorizationError).
			WithHint("Check the client ID, the scope and the redirect URL registered with the authorization server.")
	}
	e = e.WithDetail("error", code)
	if d := q.Get("error_description"); d != "" {
		e = e.WithDetail("errorDescription", clip(d, 300))
	}
	if u := q.Get("error_uri"); u != "" {
		e = e.WithDetail("errorUri", clip(u, 300))
	}
	return e
}

// writePage answers the browser: the sign-in worked, or why not. Nothing
// is loaded from anywhere (CSP default-src 'none'), and the page is not
// kept or sent on.
func writePage(w http.ResponseWriter, failure error) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	title, text := "Signed in", "PostSonde got the authorization. You can close this tab and go back to PostSonde."
	status := http.StatusOK
	if failure != nil {
		title, status, text = "Sign-in failed", http.StatusBadRequest, pageText(failure)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">`+
		`<title>PostSonde: `+title+`</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32em;margin:4em auto;padding:0 1em}</style></head>`+
		`<body><h1>`+title+`</h1><p>`+html.EscapeString(text)+`</p></body></html>`)
}

// pageText is what the browser is told about failure: its message, never
// a value of the redirect.
func pageText(failure error) string {
	text := failure.Error()
	var e *errs.Error
	if errors.As(failure, &e) {
		text = e.Message
	}
	return text + " You can close this tab and go back to PostSonde."
}
