package oauth2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// MaxResponseBytes is how much of a token response is read.
const MaxResponseBytes = 1 << 20

// ExpiryLeeway is how long before its expiry a token counts as expired
// (Token.Expired), so a token is not sent in its last seconds.
const ExpiryLeeway = 30 * time.Second

// Token is a token response (section 5.1).
type Token struct {
	// AccessToken is the token sent with requests.
	AccessToken string
	// TokenType is the token_type the server sent ("Bearer"), as sent.
	TokenType string
	// ExpiresIn is the lifetime the server gave in seconds, 0 when it gave
	// none; Expiry is when the token expires (the time of the response
	// plus ExpiresIn), zero when unknown.
	ExpiresIn int64
	Expiry    time.Time
	// RefreshToken is sent by some servers (not for client credentials,
	// section 4.4.3); it gets a new token once this one expires
	// (RefreshRequest).
	RefreshToken string
	// Scope is the scope granted, when the server says (section 3.3).
	Scope string
	// IDToken is an OpenID Connect ID token, when the server sends one
	// (checked on arrival, CheckIDToken); an auth can send it instead of
	// the access token (AUTH-14).
	IDToken string
	// Obtained is when the response arrived.
	Obtained time.Time
}

// Expired reports whether t has expired at now, ExpiryLeeway early. A
// token without an expiry never expires.
//
// An expired token with a refresh token is refreshed (RefreshRequest)
// rather than got again, so the leeway is also how long before its expiry
// a token is refreshed.
func (t Token) Expired(now time.Time) bool {
	return !t.Expiry.IsZero() && !now.Before(t.Expiry.Add(-ExpiryLeeway))
}

// Secrets returns the values of t to mask wherever they are shown: the
// access, refresh and ID tokens that are not "".
func (t Token) Secrets() []string {
	var out []string
	for _, s := range []string{t.AccessToken, t.RefreshToken, t.IDToken} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// tokenJSON is a token or error response's members as JSON.
type tokenJSON struct {
	AccessToken      *json.RawMessage `json:"access_token"`
	TokenType        *json.RawMessage `json:"token_type"`
	ExpiresIn        *json.RawMessage `json:"expires_in"`
	RefreshToken     *json.RawMessage `json:"refresh_token"`
	Scope            *json.RawMessage `json:"scope"`
	IDToken          *json.RawMessage `json:"id_token"`
	Error            *json.RawMessage `json:"error"`
	ErrorDescription *json.RawMessage `json:"error_description"`
	ErrorURI         *json.RawMessage `json:"error_uri"`
}

// fields is a response's members as text, from JSON or a form.
type fields map[string]string

// ParseResponse reads the response to a token request: status is its
// status code, contentType its Content-Type, body its body, and now the
// time it arrived (Token.Obtained, the base of Token.Expiry).
//
//   - The body is JSON (section 5.1), whatever the Content-Type says, or a
//     form (application/x-www-form-urlencoded, which some servers still
//     send, or text/plain holding one).
//   - A 2xx answer is a token: access_token is required (a string without
//     control characters or spaces), token_type, refresh_token, scope and
//     id_token are strings when present, and expires_in a number or a
//     string of digits. Anything else is CodeMalformedResponse, as is a
//     body that is not a JSON object.
//   - Another status with an "error" member is an error response (section
//     5.2): CodeTokenError with the error code, description and URI.
//   - Another status without one is CodeTokenHTTPStatus.
//
// Values of the response never appear in errors, except the error code,
// description and URI of an error response.
func ParseResponse(status int, contentType string, body []byte, now time.Time) (Token, error) {
	ok := status >= 200 && status < 300
	f, perr := parseBody(contentType, body)
	if !ok {
		if perr == nil && f["error"] != "" {
			return Token{}, tokenError(status, f)
		}
		return Token{}, errs.New(errs.Protocol, "The authorization server answered the token request with "+strconv.Itoa(status)+" and no OAuth 2.0 error.").
			WithCode(CodeTokenHTTPStatus).
			WithHint("Check the token URL. The Timeline tab shows the server's response.").
			WithDetail("status", strconv.Itoa(status))
	}
	if perr != nil {
		return Token{}, malformed(status, "The token response is not a JSON object.", "", perr)
	}
	if f["error"] != "" {
		// A 200 with an error: some servers do that.
		return Token{}, tokenError(status, f)
	}
	if bad, isBad := f["\x00bad"]; isBad {
		return Token{}, malformed(status, "The token response's "+bad+" is not a string.", bad, nil)
	}
	t := Token{
		AccessToken:  f["access_token"],
		TokenType:    f["token_type"],
		RefreshToken: f["refresh_token"],
		Scope:        f["scope"],
		IDToken:      f["id_token"],
		Obtained:     now,
	}
	if t.AccessToken == "" {
		return Token{}, malformed(status, "The token response has no access_token.", "access_token", nil)
	}
	for i := 0; i < len(t.AccessToken); i++ {
		if c := t.AccessToken[i]; c <= ' ' || c == 0x7f {
			return Token{}, malformed(status, "The access token contains a space or a control character, so it cannot be sent in a header.", "access_token", nil)
		}
	}
	if v, has := f["expires_in"]; has && v != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			// "3600.0": some servers send a float.
			if fl, ferr := strconv.ParseFloat(strings.TrimSpace(v), 64); ferr == nil && fl >= 0 && fl < 1e15 {
				n, err = int64(fl), nil
			}
		}
		if err != nil || n < 0 {
			return Token{}, malformed(status, "The token response's expires_in is not a number of seconds.", "expires_in", nil)
		}
		t.ExpiresIn = n
		t.Expiry = now.Add(time.Duration(min(n, int64(100*365*24*3600))) * time.Second)
	}
	return t, nil
}

// parseBody reads a response body as JSON, or as a form when the content
// type says so. A JSON member that is not a string (or, for expires_in, a
// number) is reported as the field "\x00bad".
func parseBody(contentType string, body []byte) (fields, error) {
	media, _, _ := mime.ParseMediaType(contentType)
	trimmed := bytes.TrimSpace(body)
	if media == FormType || (media == "text/plain" && len(trimmed) > 0 && trimmed[0] != '{') {
		q, err := url.ParseQuery(string(trimmed))
		if err != nil {
			return nil, err
		}
		out := fields{}
		for k, v := range q {
			if len(v) > 0 {
				out[k] = v[0]
			}
		}
		return out, nil
	}
	var j tokenJSON
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("not a JSON object")
	}
	if err := json.Unmarshal(trimmed, &j); err != nil {
		return nil, err
	}
	out := fields{}
	for _, m := range []struct {
		name string
		raw  *json.RawMessage
	}{
		{"access_token", j.AccessToken}, {"token_type", j.TokenType}, {"refresh_token", j.RefreshToken},
		{"scope", j.Scope}, {"id_token", j.IDToken}, {"error", j.Error},
		{"error_description", j.ErrorDescription}, {"error_uri", j.ErrorURI},
	} {
		if m.raw == nil || string(*m.raw) == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal(*m.raw, &s); err != nil {
			if _, seen := out["\x00bad"]; !seen {
				out["\x00bad"] = m.name
			}
			continue
		}
		out[m.name] = s
	}
	if j.ExpiresIn != nil && string(*j.ExpiresIn) != "null" {
		raw := *j.ExpiresIn
		var s string
		var n json.Number
		switch {
		case json.Unmarshal(raw, &s) == nil:
			out["expires_in"] = s
		case json.Unmarshal(raw, &n) == nil:
			out["expires_in"] = n.String()
		default:
			out["expires_in"] = string(raw)
		}
	}
	return out, nil
}

// ErrorHints are what to do about each error code of section 5.2.
var ErrorHints = map[string]string{
	"invalid_request":        "The server says the token request is missing a parameter or has a wrong one. Check the extra parameters, audience and resource.",
	"invalid_client":         "Check the client ID and secret, and whether the server wants the client credentials in the Authorization header or in the body.",
	"invalid_grant":          "The grant was refused: check the credentials of the grant.",
	"unauthorized_client":    "The client is not allowed to use this grant type: allow it in the authorization server's client settings.",
	"unsupported_grant_type": "The authorization server does not support this grant type.",
	"invalid_scope":          "Check the scopes: one of them is unknown to the server, or not allowed for this client.",
}

// tokenError is the error of an error response.
func tokenError(status int, f fields) error {
	code := clip(f["error"], 60)
	msg := "The authorization server refused the token request: " + code + "."
	if d := clip(f["error_description"], 300); d != "" {
		msg = "The authorization server refused the token request: " + code + " (" + d + ")."
	}
	kind := errs.InvalidArgument
	if code == "invalid_client" || code == "unauthorized_client" {
		kind = errs.PermissionDenied
	}
	hint, ok := ErrorHints[code]
	if !ok {
		hint = "The Timeline tab shows the server's response."
	}
	e := errs.New(kind, msg).WithCode(CodeTokenError).WithHint(hint).
		WithDetail("error", code).WithDetail("status", strconv.Itoa(status))
	if d := f["error_description"]; d != "" {
		e = e.WithDetail("errorDescription", clip(d, 300))
	}
	if u := f["error_uri"]; u != "" {
		e = e.WithDetail("errorUri", clip(u, 300))
	}
	return e
}

func malformed(status int, msg, field string, cause error) error {
	e := errs.Wrap(cause, errs.Parse, msg).
		WithCode(CodeMalformedResponse).
		WithHint("Check that the token URL is the server's token endpoint. The Timeline tab shows the response.").
		WithDetail("status", strconv.Itoa(status))
	if field != "" {
		e = e.WithDetail("field", field)
	}
	return e
}

// clip shortens s to at most n bytes for a message, cut between UTF-8
// sequences, and drops control characters.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// Doer sends a request: the app's *httpclient.Client.
type Doer interface {
	Send(ctx context.Context, r httpclient.Request) (*httpclient.Response, error)
}

// Result is one exchange with the token endpoint.
type Result struct {
	// Request is the token request as built; Response the answer, nil
	// when none arrived.
	Request  httpclient.Request
	Response *httpclient.Response
	// Token is the token obtained, when the exchange succeeded.
	Token Token
	// Started is when the exchange started, by the wall clock (not the
	// now of Exchange), for the timeline's offsets.
	Started time.Time
}

// Exchange sends r to its token endpoint through d with settings (the
// request's timeouts) and reads the answer (ParseResponse), now being the
// clock (nil is time.Now). An ID token in the response is checked
// against r (CheckIDToken: the client, the issuer, the expiry, the nonce
// of the sign-in); a bad one fails the exchange, with Result.Token set so
// its values can be masked. The Result is returned also with an error, with
// what was sent and received, for the timeline. A request that fails
// before an answer keeps the cause's kind and code (a timeout, a refused
// connection, TLS), with the message saying it was the token request.
func Exchange(ctx context.Context, d Doer, r TokenRequest, settings httpclient.Settings, now func() time.Time) (*Result, error) {
	if now == nil {
		now = time.Now
	}
	res := &Result{Request: r.HTTP(settings), Started: time.Now()}
	resp, err := d.Send(ctx, res.Request)
	if err != nil {
		return res, requestFailed(err, r.Config.TokenURL)
	}
	res.Response = resp
	contentType := ""
	for _, h := range resp.Headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			contentType = h.Value
			break
		}
	}
	if resp.BodyTruncated {
		return res, malformed(resp.StatusCode, "The token response is larger than 1 MiB.", "", nil)
	}
	res.Token, err = ParseResponse(resp.StatusCode, contentType, resp.Body, now())
	if err == nil && res.Token.IDToken != "" {
		// An OpenID Connect ID token is checked on arrival: a token
		// response with a bad one is refused as a whole.
		_, err = CheckIDToken(r.Config, res.Token, now())
	}
	return res, err
}

// requestFailed wraps the error of a token request that got no answer.
func requestFailed(err error, tokenURL string) error {
	var e *errs.Error
	shown := tokenURL
	if u, perr := url.Parse(tokenURL); perr == nil {
		u.RawQuery, u.Fragment, u.User = "", "", nil
		shown = u.String()
	}
	if errors.As(err, &e) {
		c := *e
		c.Message = "The OAuth 2.0 token request failed: " + e.Message
		out := &c
		if out.Code == "" {
			out = out.WithCode(CodeTokenRequestFailed)
		}
		return out.WithDetail("tokenUrl", shown)
	}
	return errs.Wrap(err, errs.KindOf(err), "The OAuth 2.0 token request failed.").
		WithCode(CodeTokenRequestFailed).
		WithHint("Check the token URL and your network connection.").
		WithDetail("tokenUrl", shown)
}

// Store keeps obtained tokens by key (TokenRequest.Key). MemoryStore keeps
// them in memory for the session; the app's token store (package
// tokenstore) keeps them in the workspace's local directory, so they
// survive a restart without ever going into a project file.
type Store interface {
	// Get returns the token stored under key.
	Get(key string) (Token, bool)
	// Put stores t under key, replacing any token there.
	Put(key string, t Token)
	// Delete removes the token under key, if any.
	Delete(key string)
}

// MemoryStore is a Store in memory, safe for concurrent use. The zero
// value is ready.
type MemoryStore struct {
	mu     sync.Mutex
	tokens map[string]Token
}

var _ Store = (*MemoryStore)(nil)

// Get implements Store.
func (s *MemoryStore) Get(key string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[key]
	return t, ok
}

// Put implements Store.
func (s *MemoryStore) Put(key string, t Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[string]Token{}
	}
	s.tokens[key] = t
}

// Delete implements Store.
func (s *MemoryStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, key)
}

// Clear removes every token.
func (s *MemoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = nil
}
