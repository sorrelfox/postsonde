// Package snippet turns a request into code (GEN-01, EXP-01): pure
// "request → string" generators over one neutral model, Request, made from
// the request exactly as PostSonde would send it (FromBuild), or with its
// {{variable}} references kept as written (FromTemplateBuild). Curl writes
// a POSIX shell curl command; Languages lists the generators of the
// snippet panel. See docs/curl.md#copy-as-curl and docs/snippets.md.
//
// The model is built from request.HTTPBuild, so what a snippet sends is
// what Send sends: variables, dynamic values, folder headers, auth,
// settings and the cookie jar's cookies applied. Masked hides secrets in
// it before a generator runs (MaskedTemplate for a templated request), so
// every generator masks the same way.
package snippet

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
)

// Error codes of FromBuild and the generators.
const (
	// CodeBodyUnsupported: the body is of a kind the generators do not
	// know (kind unsupported).
	CodeBodyUnsupported = "snippet.body_unsupported"
	// CodeBodyTooLarge: a body held in memory is larger than MaxBody
	// (kind invalid_argument).
	CodeBodyTooLarge = "snippet.body_too_large"
	// CodeNulByte: a value that must go into a command-line argument
	// contains a NUL byte, which no argument can carry (kind
	// invalid_argument). Details: field.
	CodeNulByte = "snippet.nul_byte"
)

// MaxBody is the largest in-memory body (raw text, URL-encoded form) put
// into a snippet: 1 MiB. File bodies and file parts are referenced by
// path, whatever their size.
const MaxBody = 1 << 20

// BodyKind is the kind of a Body.
type BodyKind string

// Body kinds.
const (
	// BodyNone sends no body.
	BodyNone BodyKind = ""
	// BodyData sends Body.Data: raw text or a URL-encoded form.
	BodyData BodyKind = "data"
	// BodyFile sends the file at Body.File byte for byte.
	BodyFile BodyKind = "file"
	// BodyMultipart sends Body.Parts as multipart/form-data.
	BodyMultipart BodyKind = "multipart"
)

// Header is one header field sent, in the request's order.
type Header struct {
	Name  string
	Value string
}

// Credentials are a username and password the request authenticates
// with.
type Credentials struct {
	Username string
	Password string
}

// Part is one multipart part (request.MultipartPart).
type Part = request.MultipartPart

// Body is the request body.
type Body struct {
	Kind BodyKind
	// Data is a BodyData body.
	Data []byte
	// File is a BodyFile body's absolute path.
	File string
	// Parts are a BodyMultipart body's parts.
	Parts []Part
	// ContentType is the Content-Type the body implies (HTTP-16), sent
	// unless a Content-Type header is; "" for none. Unset for multipart,
	// whose boundary each client makes up.
	ContentType string
}

// Request is a request as PostSonde sends it, for the generators.
type Request struct {
	// Method is sent as typed.
	Method string
	// URL is the URL as sent (httpclient.SentURL): encoded in the
	// request's URL encoding mode, without fragment or userinfo.
	URL string
	// Headers are the enabled header rows, in order. The Authorization
	// row a Basic auth (or URL userinfo) makes is Basic instead.
	Headers []Header
	// Cookies are the cookie jar's cookies for the URL that no Cookie row
	// already sends, as a Cookie value ("a=1; b=2"); "" for none. With a
	// Cookie row they are appended to it instead, as Send does.
	Cookies string
	// Basic are Basic credentials, from an "Authorization: Basic" row
	// that decodes to "user:password" or from the URL's userinfo; nil for
	// none.
	Basic *Credentials
	// Digest are the credentials of Digest auth (AUTH-06); nil for none.
	Digest *Credentials
	Body   Body

	// Insecure turns TLS verification off.
	Insecure bool
	// FollowRedirects follows up to MaxRedirects redirects; false (or
	// MaxRedirects 0) follows none.
	FollowRedirects bool
	MaxRedirects    int
	// KeepMethod keeps the method and body on 301, 302 and 303.
	KeepMethod bool
	// ForwardAuthorization sends the credentials to every hop.
	ForwardAuthorization bool
	// Timeout bounds the whole exchange; zero is none.
	Timeout time.Duration
	// HTTPVersion is the HTTP version choice (HTTP-25).
	HTTPVersion httpclient.HTTPVersion
}

// FromBuild returns the request b sends. cookies are the cookie jar's
// cookies for its URL (nil for none); they are left out when the
// request's settings do not send jar cookies (HTTP-26). A multipart or
// file body is described by its parts and paths, not read; a raw body
// larger than MaxBody fails with CodeBodyTooLarge.
func FromBuild(b request.HTTPBuild, cookies []*http.Cookie) (Request, error) {
	return fromBuild(b, cookies, false)
}

// fromBuild is FromBuild, and with templated FromTemplateBuild.
func fromBuild(b request.HTTPBuild, cookies []*http.Cookie, templated bool) (Request, error) {
	r := b.Request
	s := r.Settings
	out := Request{
		Method:               r.Method,
		Insecure:             b.InsecureSkipVerify,
		FollowRedirects:      s.FollowRedirects && s.MaxRedirects > 0,
		MaxRedirects:         s.MaxRedirects,
		KeepMethod:           s.KeepMethod,
		ForwardAuthorization: s.ForwardAuthorization,
		Timeout:              s.Timeout,
		HTTPVersion:          s.HTTPVersion,
	}
	if !out.FollowRedirects {
		out.MaxRedirects = 0
	}
	u, user, err := httpclient.SentURL(r.URL, s.URLEncoding)
	switch {
	case err == nil && templated:
		out.URL = restoreReferences(u)
	case err == nil:
		out.URL = u
	case templated:
		// A {{variable}} in the scheme or host: the URL as written.
		out.URL, user = templateURL(r.URL)
	default:
		return Request{}, err
	}

	hasAuth := false
	for _, h := range r.Headers {
		if !h.Enabled {
			continue
		}
		if strings.EqualFold(h.Name, "Authorization") {
			hasAuth = true
		}
		out.Headers = append(out.Headers, Header{Name: h.Name, Value: h.Value})
	}
	out.Headers, out.Basic = basicFromHeaders(out.Headers)
	if user != nil && !hasAuth {
		pass, _ := user.Password()
		out.Basic = &Credentials{Username: user.Username(), Password: pass}
	}
	if r.Digest != nil && out.Basic == nil && !hasAuth {
		out.Digest = &Credentials{Username: r.Digest.Username, Password: r.Digest.Password}
	}
	if !s.SkipSendingCookies && !templated {
		out.Headers, out.Cookies = addCookies(out.Headers, cookies)
	}

	if out.Body, err = body(r.Body); err != nil {
		return Request{}, err
	}
	return out, nil
}

// basicFromHeaders moves the one Authorization row that holds Basic
// credentials of the form "user:password" (valid UTF-8, encoded exactly as
// Basic auth encodes them) out of headers. With more than one
// Authorization row, or another value, headers are left as they are.
func basicFromHeaders(headers []Header) ([]Header, *Credentials) {
	at := -1
	for i, h := range headers {
		if strings.EqualFold(h.Name, "Authorization") {
			if at >= 0 {
				return headers, nil
			}
			at = i
		}
	}
	if at < 0 {
		return headers, nil
	}
	scheme, cred, ok := strings.Cut(headers[at].Value, " ")
	if !ok || scheme != "Basic" {
		return headers, nil
	}
	raw, err := base64.StdEncoding.DecodeString(cred)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != cred || !utf8.Valid(raw) {
		return headers, nil
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return headers, nil
	}
	out := append(append([]Header(nil), headers[:at]...), headers[at+1:]...)
	return out, &Credentials{Username: user, Password: pass}
}

// addCookies adds the jar's cookies as Send does (httpclient's
// addJarCookies): a cookie a Cookie row already names is left out; the
// rest are appended to the first Cookie row, or returned as the value of
// a Cookie field of their own.
func addCookies(headers []Header, cookies []*http.Cookie) ([]Header, string) {
	if len(cookies) == 0 {
		return headers, ""
	}
	first := -1
	names := map[string]bool{}
	for i, h := range headers {
		if !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		if first < 0 {
			first = i
		}
		for pair := range strings.SplitSeq(h.Value, ";") {
			if n, _, _ := strings.Cut(pair, "="); strings.TrimSpace(n) != "" {
				names[strings.TrimSpace(n)] = true
			}
		}
	}
	var b strings.Builder
	for _, c := range cookies {
		if names[c.Name] {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		if c.Name != "" {
			b.WriteString(c.Name + "=")
		}
		b.WriteString(c.Value)
	}
	value := b.String()
	if value == "" || first < 0 {
		return headers, value
	}
	out := append([]Header(nil), headers...)
	if strings.Trim(out[first].Value, "; \t") == "" {
		out[first].Value = value
	} else {
		out[first].Value = strings.TrimRight(out[first].Value, "; \t") + "; " + value
	}
	return out, ""
}

// body describes b.
func body(b httpclient.Body) (Body, error) {
	switch b := b.(type) {
	case nil:
		return Body{}, nil
	case *request.MultipartBody:
		return Body{Kind: BodyMultipart, Parts: b.Parts()}, nil
	case *request.FileBody:
		return Body{Kind: BodyFile, File: b.Path(), ContentType: b.ContentType()}, nil
	case httpclient.RawBody:
		if len(b.Data) > MaxBody {
			return Body{}, tooLarge()
		}
		return Body{Kind: BodyData, Data: b.Data, ContentType: b.Type}, nil
	}
	// Another in-memory body: read it, within the limit.
	rc, _, err := b.Open()
	if err != nil {
		return Body{}, errs.Wrap(err, errs.KindOf(err), "The request body could not be read.")
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxBody+1))
	if err != nil {
		return Body{}, errs.Wrap(err, errs.KindOf(err), "The request body could not be read.")
	}
	if len(data) > MaxBody {
		return Body{}, tooLarge()
	}
	return Body{Kind: BodyData, Data: data, ContentType: b.ContentType()}, nil
}

func tooLarge() error {
	return errs.New(errs.InvalidArgument, "The request body is larger than 1 MiB, too large for a command.").
		WithCode(CodeBodyTooLarge).
		WithHint("Send a large body from a file (the Binary body type), which the command references by its path.")
}

// Mask is what Masked puts in place of a secret: httpclient.Mask, as the
// timeline shows it.
const Mask = httpclient.Mask

// Masked returns r with its secrets replaced by Mask, and whether anything
// was replaced (PRV-09, GEN-13). It masks as the timeline does
// (httpclient.SecretMasker over secrets, the values of the secret
// variables used and those the auth adds): each secret in the URL, header
// values, body text and multipart names and values, also URL-escaped, and
// the credentials of every Authorization and Proxy-Authorization row
// after the scheme. The Basic and Digest passwords are always masked;
// usernames only when they are secrets. File paths and cookies are kept
// (cookies are not secrets, docs/privacy.md).
func (r Request) Masked(secrets []string) (Request, bool) {
	return r.masked(secrets, false)
}

// masked is Masked, and with templated MaskedTemplate.
func (r Request) masked(secrets []string, templated bool) (Request, bool) {
	m := httpclient.NewSecretMasker(secrets)
	// keep says whether a credential is shown as it is: in a templated
	// request, one made only of {{variable}} references.
	keep := func(v string) bool { return templated && requestsecrets.IsReference(v) }
	out := r
	changed := false
	text := func(s string) string {
		t := m.MaskText(s)
		changed = changed || t != s
		return t
	}
	out.URL = text(r.URL)
	out.Headers = make([]Header, len(r.Headers))
	for i, h := range r.Headers {
		v := m.MaskHeader(h.Name, h.Value)
		if isCredentialHeader(h.Name) && keep(credentialOf(h.Value)) {
			v = m.MaskText(h.Value)
		}
		changed = changed || v != h.Value
		out.Headers[i] = Header{Name: h.Name, Value: v}
	}
	cred := func(c *Credentials) *Credentials {
		if c == nil {
			return nil
		}
		pass := ""
		switch {
		case keep(c.Password):
			pass = text(c.Password)
		case c.Password != "":
			changed = true
			pass = Mask
		}
		return &Credentials{Username: text(c.Username), Password: pass}
	}
	out.Basic, out.Digest = cred(r.Basic), cred(r.Digest)
	if len(r.Body.Data) > 0 {
		out.Body.Data = []byte(text(string(r.Body.Data)))
	}
	if r.Body.Parts != nil {
		out.Body.Parts = make([]Part, len(r.Body.Parts))
		for i, p := range r.Body.Parts {
			p.Name = text(p.Name)
			if p.File == "" {
				p.Value = text(p.Value)
			}
			out.Body.Parts[i] = p
		}
	}
	return out, changed
}

// nulError is the CodeNulByte error of field.
func nulError(field string) error {
	return errs.New(errs.InvalidArgument, "The "+field+" contains a NUL byte, which a shell command cannot carry.").
		WithCode(CodeNulByte).
		WithHint("Send such a body from a file (the Binary body type), which the command references by its path.").
		WithDetail("field", field)
}
