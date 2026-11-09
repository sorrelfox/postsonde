package oauth2

// implicit.go is the browser leg of the implicit grant (RFC 6749 section
// 4.2; deprecated by the OAuth 2.0 Security BCP, for legacy servers
// only). The token comes back in the redirect URL's fragment, which a
// browser never sends to a server, so the loopback listener answers the
// redirect with a page whose one inline script reads location.hash,
// removes it from the address bar and the history, and posts it back to
// the listener on the same origin. The page's Content Security Policy
// allows exactly that script (by its SHA-256 hash) and connections to its
// own origin, nothing else; the listener takes the post only from that
// origin. See docs/auth.md#implicit-grant.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// ImplicitToken is what the implicit grant's redirect brought.
type ImplicitToken struct {
	// Token is the token of the redirect's fragment: the access token,
	// its type, lifetime and scope, and an ID token when the scope asks
	// for openid (checked, CheckIDToken). The implicit grant never has a
	// refresh token (section 4.2.2).
	Token Token
	// RedirectURI is the redirect_uri of the authorization request.
	RedirectURI string
	// Nonce is the nonce sent, which the ID token repeated.
	Nonce string
}

// maxFragmentBytes is how much of a posted fragment is read.
const maxFragmentBytes = 64 << 10

// AuthorizeImplicit runs the implicit grant for c: the same browser leg as
// Authorize (the loopback listener, the state, the page, the timeout),
// with response_type token ("id_token token" when the scope asks for
// openid) and always a random nonce, and no PKCE (the implicit grant has
// no token request to bind it to). The token comes in the redirect's
// fragment, which the redirect page posts back (see the file comment); a
// redirect with the parameters in its query instead (some servers send
// their errors so) is read too.
//
// The redirect is checked in this order: the state (CodeStateMismatch),
// an error (CodeAccessDenied, CodeAuthorizationError), the access token
// (CodeAuthorizationError without one, CodeMalformedResponse for a bad
// one), then an ID token's claims (CheckIDToken: its nonce must be the
// one sent). The browser's page says how it went. The token is read with
// o.Now (nil is time.Now).
func AuthorizeImplicit(ctx context.Context, c Config, o AuthorizeOptions) (ImplicitToken, error) {
	c.Grant = GrantImplicit
	l, err := signIn(ctx, c, o)
	if err != nil {
		return ImplicitToken{}, err
	}
	return ImplicitToken{Token: l.token, RedirectURI: l.redirectURI, Nonce: l.nonce}, nil
}

// implicitToken reads the token of an implicit grant's redirect q, whose
// state was checked: its access token, type, lifetime and scope as a
// token response's (ParseResponse), and its ID token checked against c
// (the nonce sent, the client, the issuer) at now.
func implicitToken(c Config, q url.Values, now time.Time) (Token, error) {
	if q.Get("access_token") == "" {
		return Token{}, errs.New(errs.Protocol, "The OAuth 2.0 redirect brought no access token.").
			WithCode(CodeAuthorizationError).
			WithHint("Check that the authorization server allows the implicit grant (response_type token) for this client.")
	}
	form := url.Values{}
	for _, k := range []string{"access_token", "token_type", "expires_in", "scope", "id_token"} {
		if v := q.Get(k); v != "" {
			form.Set(k, v)
		}
	}
	t, err := ParseResponse(http.StatusOK, FormType, []byte(form.Encode()), now)
	if err != nil {
		var e *errs.Error
		msg := "The token in the OAuth 2.0 redirect is not valid."
		if errors.As(err, &e) {
			msg = "The token in the OAuth 2.0 redirect is not valid: " + e.Message
		}
		return Token{}, errs.New(errs.Parse, msg).WithCode(CodeMalformedResponse).
			WithHint("Check the authorization server's implicit grant settings.")
	}
	if t.IDToken != "" {
		if _, err := CheckIDToken(c, t, now); err != nil {
			return Token{}, err
		}
	}
	return t, nil
}

// implicitHandler answers the redirect of the implicit grant to path, on
// the listener at hostport (host:port as the redirect URI names it):
//
//   - GET without a query: the page that posts the fragment back
//     (writeFragmentPage). It sends no result: the wait goes on.
//   - GET with a query: a server that answered in the query; read like
//     the fragment.
//   - POST from the page (its origin and Host must be the listener's, the
//     body a form): the fragment, read with read after the state and error
//     checks; answered with a short text the page shows. A post from
//     anywhere else is 403 and does not end the wait.
//
// Other paths are 404, other methods 405. The first result is sent.
func implicitHandler(path, hostport, state string, read func(url.Values) (Token, error), results chan<- redirectResult) http.Handler {
	finish := func(q url.Values) redirectResult {
		res := redirectResult{values: q, err: checkRedirect(q, state)}
		if res.err == nil {
			res.token, res.err = read(q)
		}
		return res
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != path {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			if len(q) == 0 {
				writeFragmentPage(w)
				return
			}
			res := finish(q)
			writePage(w, res.err)
			send(results, res)
		case http.MethodPost:
			if !strings.EqualFold(r.Host, hostport) || !strings.EqualFold(r.Header.Get("Origin"), "http://"+hostport) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media != FormType {
				http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxFragmentBytes))
			var res redirectResult
			q, perr := url.ParseQuery(string(body))
			if err != nil || perr != nil {
				res.err = errs.New(errs.Protocol, "The OAuth 2.0 redirect's answer could not be read.").
					WithCode(CodeAuthorizationError).
					WithHint("Get a new token to sign in again.")
			} else {
				res = finish(q)
			}
			writeResult(w, res.err)
			send(results, res)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// fragmentScript is the redirect page's only script: it takes the
// fragment, removes it from the address bar and the history entry, posts
// it to the listener (the same origin and path, no credentials, no
// referrer), and shows the answer as text.
const fragmentScript = `(function(){` +
	`var f=location.hash.slice(1),p=location.pathname,h=document.getElementById("h"),m=document.getElementById("m");` +
	`history.replaceState(null,"",p);` +
	`fetch(p,{method:"POST",headers:{"Content-Type":"application/x-www-form-urlencoded"},body:f,credentials:"omit",cache:"no-store",referrerPolicy:"no-referrer"})` +
	`.then(function(r){h.textContent=r.ok?"Signed in":"Sign-in failed";return r.text()})` +
	`.then(function(t){m.textContent=t})` +
	`.catch(function(){h.textContent="Sign-in failed";m.textContent="PostSonde did not get the answer. Go back to PostSonde and get a new token."})` +
	`})();`

// FragmentScriptHash is the CSP source of fragmentScript: 'sha256-…' of
// its text, the only script the redirect page may run.
var FragmentScriptHash = func() string {
	sum := sha256.Sum256([]byte(fragmentScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// FragmentPageCSP is the Content Security Policy of the redirect page:
// nothing but its own style, its one script (by hash) and a connection
// to its own origin.
var FragmentPageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src " + FragmentScriptHash +
	"; connect-src 'self'; base-uri 'none'; form-action 'none'"

// writeFragmentPage answers the implicit grant's redirect with the page
// that posts its fragment back. It holds no value of the redirect.
func writeFragmentPage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", FragmentPageCSP)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">`+
		`<title>PostSonde: Signing in</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32em;margin:4em auto;padding:0 1em}</style></head>`+
		`<body><h1 id="h">Signing in</h1><p id="m">PostSonde is reading the answer of the sign-in.</p>`+
		`<noscript><p>This page needs JavaScript to hand the token to PostSonde.</p></noscript>`+
		`<script>`+fragmentScript+`</script></body></html>`)
}

// writeResult answers the page's post: the text it shows.
func writeResult(w http.ResponseWriter, failure error) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	text, status := "PostSonde got the token. You can close this tab and go back to PostSonde.", http.StatusOK
	if failure != nil {
		text, status = pageText(failure), http.StatusBadRequest
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}
