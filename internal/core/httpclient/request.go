package httpclient

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Request is an HTTP request as the user configured it. Send turns it into
// exactly this request on the wire, within the limits of net/http listed in
// docs/http-client.md.
type Request struct {
	// Method is sent as typed: "GET", a lowercase "get", or any custom
	// token such as "PROPFIND" or "QUERY".
	Method string
	// URL is an absolute http:// or https:// URL. The path and query are
	// percent-encoded per Settings.URLEncoding, never decoded; the bytes
	// that cannot appear in a request line (control characters, space,
	// non-ASCII) are encoded in every mode. The fragment is not sent.
	URL string
	// Headers are the request's header rows, in the user's order. Names are
	// sent as spelled (on HTTP/1.1), duplicates as separate fields; rows
	// with Enabled false are skipped.
	Headers []Header
	// Body is the request body, or nil for none.
	Body Body
	// Settings are the per-request timeouts, redirect policy, response
	// capture limit, URL encoding, HTTP version and cookie switches.
	Settings Settings
	// Digest, when set, answers a 401 Digest challenge (RFC 7616, AUTH-06):
	// the request is first sent without credentials, then again with the
	// Authorization field computed from the challenge. An Authorization
	// header row (or URL userinfo) wins: the request is then sent as it
	// is. See authTransport.
	Digest *DigestCredentials
}

// Header is one header row. The zero Enabled is false: a row is sent only
// when Enabled is true. Received headers (Response.Headers) are always
// Enabled.
type Header struct {
	Name    string
	Value   string
	Enabled bool
}

// Body is a request body. RawBody holds bytes (raw text, URL-encoded
// forms); the multipart and file bodies of package request stream from
// disk.
//
// Open may be called more than once: a 307 or 308 redirect sends the body
// again. It returns a reader positioned at the start and the length in
// bytes, or -1 when unknown (then the body is sent chunked on HTTP/1.1).
// ContentType is the media type the body implies, sent as the Content-Type
// header unless the request has an enabled Content-Type row, or "".
type Body interface {
	Open() (io.ReadCloser, int64, error)
	ContentType() string
}

// RawBody is a body of raw bytes, sent byte for byte.
type RawBody struct {
	// Data is the body.
	Data []byte
	// Type is the Content-Type to send unless a header row sets one
	// ("application/json"); "" sends none.
	Type string
}

var _ Body = RawBody{}

// Open returns a reader over Data and its length.
func (b RawBody) Open() (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(b.Data)), int64(len(b.Data)), nil
}

// ContentType returns Type.
func (b RawBody) ContentType() string { return b.Type }

// Defaults for zero Settings fields and DefaultSettings.
const (
	// DefaultMaxRedirects is the redirect limit when following redirects,
	// Postman's and Go's default.
	DefaultMaxRedirects = 10
	// DefaultMaxBodyBytes caps the response body kept in memory (100 MiB).
	DefaultMaxBodyBytes int64 = 100 << 20
)

// Settings control how one request is sent. The zero value sends the
// request without a time limit, does not follow redirects and keeps up to
// DefaultMaxBodyBytes of the response body. The editor's defaults are
// DefaultSettings. docs/http-client.md explains the choices.
type Settings struct {
	// Timeout bounds the whole exchange: connecting, sending, the response
	// headers and the body. Zero means no limit besides cancellation.
	Timeout time.Duration
	// ResponseHeaderTimeout bounds the wait for the response headers after
	// the request has been written, per redirect hop. Zero means no limit.
	ResponseHeaderTimeout time.Duration
	// FollowRedirects follows 3xx responses with a Location. When false,
	// the 3xx response is returned.
	FollowRedirects bool
	// MaxRedirects is the number of redirects followed before Send fails
	// with CodeTooManyRedirects. Zero follows none: the first 3xx
	// response is returned, as with FollowRedirects false (HTTP-22).
	// DefaultSettings sets DefaultMaxRedirects.
	MaxRedirects int
	// KeepMethod keeps the original method and body on a 301, 302 or 303
	// redirect, which otherwise turn a method other than GET and HEAD
	// into a GET without a body (307 and 308 always keep them).
	KeepMethod bool
	// ForwardAuthorization sends the Authorization header to every hop,
	// also to another origin. By default it is sent only while the
	// redirects stay on the request's origin (PRV-11). The other
	// credential headers (Proxy-Authorization) are dropped either way.
	ForwardAuthorization bool
	// MaxBodyBytes is how much of the response body is kept. A longer body
	// is cut there and Response.BodyTruncated is set. Zero means
	// DefaultMaxBodyBytes; a negative value means no limit.
	MaxBodyBytes int64
	// URLEncoding is how the path and query are percent-encoded before
	// they are sent (HTTP-24). The zero value, URLEncodingAsTyped, encodes
	// only what a request line cannot carry; DefaultSettings uses
	// URLEncodingWHATWG, as browsers do.
	URLEncoding URLEncoding
	// HTTPVersion restricts the protocol versions offered (HTTP-25). The
	// zero value, HTTPVersionAuto, offers HTTP/2 and HTTP/1.1 through ALPN
	// on https and sends HTTP/1.1 on plain http.
	HTTPVersion HTTPVersion
	// SkipSendingCookies leaves the cookie jar's cookies out of every hop
	// (HTTP-26); Cookie header rows are still sent.
	SkipSendingCookies bool
	// SkipStoringCookies keeps the Set-Cookie values of every hop out of
	// the cookie jar (HTTP-26); they are still in the response headers.
	SkipStoringCookies bool
}

// URLEncoding is a URL encoding mode (HTTP-24): which characters of the
// path and query are percent-encoded before they are sent. Escapes already
// in the URL are never decoded.
type URLEncoding string

// URL encoding modes. docs/http-client.md has the character sets.
const (
	// URLEncodingAsTyped sends the path and query as typed, except the
	// bytes an HTTP request line cannot carry: control characters, space,
	// DEL and non-ASCII bytes. The file format calls it "none".
	URLEncodingAsTyped URLEncoding = ""
	// URLEncodingWHATWG encodes as the WHATWG URL Standard (browsers)
	// does for http and https URLs: besides the bytes above, " < > ` ^ { }
	// in the path and " < > ' in the query. The file format's "auto".
	URLEncodingWHATWG URLEncoding = "whatwg"
	// URLEncodingRFC3986 encodes every character RFC 3986 does not allow
	// in a path or query ([ ] | \ ^ " < > ` { } included), and a "%" that
	// does not start a valid escape, as %25.
	URLEncodingRFC3986 URLEncoding = "rfc3986"
)

// HTTPVersion is the HTTP version choice of a request (HTTP-25).
type HTTPVersion string

// HTTP version choices.
const (
	// HTTPVersionAuto offers h2 and http/1.1 through ALPN on https (the
	// server picks), and sends HTTP/1.1 on plain http.
	HTTPVersionAuto HTTPVersion = ""
	// HTTPVersion1 sends HTTP/1.1 only: ALPN offers only http/1.1.
	HTTPVersion1 HTTPVersion = "http1"
	// HTTPVersion2 sends HTTP/2 only: ALPN offers only h2 on https, and a
	// plain http URL is sent as h2c with prior knowledge (RFC 9113
	// section 3.3). A server without HTTP/2 fails the send with
	// CodeHTTP2Unsupported.
	HTTPVersion2 HTTPVersion = "http2"
)

// DefaultSettings returns the settings a new request starts with: no time
// limit, redirects followed up to DefaultMaxRedirects, response bodies
// kept up to DefaultMaxBodyBytes, the URL encoded as browsers do
// (URLEncodingWHATWG), HTTP/2 or HTTP/1.1 as the server prefers, and the
// cookie jar, if any, used both ways.
func DefaultSettings() Settings {
	return Settings{
		FollowRedirects: true,
		MaxRedirects:    DefaultMaxRedirects,
		MaxBodyBytes:    DefaultMaxBodyBytes,
		URLEncoding:     URLEncodingWHATWG,
	}
}

// normalize validates s and fills in the defaults of zero fields.
func (s Settings) normalize() (Settings, error) {
	bad := func(field, msg string) error {
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidSetting).
			WithHint("Enter zero or a positive value.").
			WithDetail("field", field)
	}
	switch {
	case s.Timeout < 0:
		return s, bad("timeout", "The request timeout cannot be negative.")
	case s.ResponseHeaderTimeout < 0:
		return s, bad("responseHeaderTimeout", "The response header timeout cannot be negative.")
	case s.MaxRedirects < 0:
		return s, bad("maxRedirects", "The maximum number of redirects cannot be negative.")
	}
	switch s.URLEncoding {
	case URLEncodingAsTyped, URLEncodingWHATWG, URLEncodingRFC3986:
	default:
		return s, errs.New(errs.InvalidArgument, "The URL encoding "+strconv.Quote(string(s.URLEncoding))+" is not known.").
			WithCode(CodeInvalidSetting).
			WithHint("Choose automatic, RFC 3986 or no encoding.").
			WithDetail("field", "urlEncoding")
	}
	switch s.HTTPVersion {
	case HTTPVersionAuto, HTTPVersion1, HTTPVersion2:
	default:
		return s, errs.New(errs.InvalidArgument, "The HTTP version "+strconv.Quote(string(s.HTTPVersion))+" is not known.").
			WithCode(CodeInvalidSetting).
			WithHint("Choose automatic, HTTP/1.1 or HTTP/2.").
			WithDetail("field", "httpVersion")
	}
	if s.MaxBodyBytes == 0 {
		s.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return s, nil
}

// prepared is a Request converted for net/http.
type prepared struct {
	req      *http.Request // without a context yet
	host     string        // the URL's host[:port], for messages
	settings Settings      // normalized
	// keepReferer is true when the user set a header spelled exactly
	// "Referer", whose value net/http keeps on redirects. Otherwise the
	// Referer net/http adds on redirects is removed.
	keepReferer bool
}

// prepare validates r and builds the http.Request that sends it. Nothing
// is added that the user did not configure, except what docs/http-client.md
// lists (Host from the URL, Content-Length, Content-Type from the body,
// Authorization from userinfo in the URL).
func prepare(r Request) (*prepared, error) {
	if err := checkMethod(r.Method); err != nil {
		return nil, err
	}
	settings, err := r.Settings.normalize()
	if err != nil {
		return nil, err
	}
	u, err := parseURL(r.URL, settings.URLEncoding)
	if err != nil {
		return nil, err
	}

	h := make(http.Header, len(r.Headers)+2)
	var hosts, agents, lengths, encodings []Header
	var hasContentType, hasAuthorization, keepReferer bool
	for i, hd := range r.Headers {
		if !hd.Enabled {
			continue
		}
		if err := checkHeader(i, hd); err != nil {
			return nil, err
		}
		// net/http writes these itself (HTTP/1.1) or reads them only in
		// canonical spelling, so they are mapped onto its fields.
		switch strings.ToLower(hd.Name) {
		case "host":
			hosts = append(hosts, hd)
			continue
		case "user-agent":
			agents = append(agents, hd)
			continue
		case "content-length":
			lengths = append(lengths, hd)
			continue
		case "transfer-encoding":
			encodings = append(encodings, hd)
			continue
		case "trailer":
			return nil, errs.New(errs.Unsupported, "Sending trailers is not supported.").
				WithCode(CodeHeaderUnsupported).
				WithHint("Disable the "+hd.Name+" header.").
				WithDetail("header", hd.Name)
		case "content-type":
			hasContentType = true
		case "authorization":
			hasAuthorization = true
		case "referer":
			keepReferer = keepReferer || hd.Name == "Referer"
		}
		h[hd.Name] = append(h[hd.Name], hd.Value)
	}
	if r.Method == MethodQuery {
		markIdempotent(h)
	}

	req := &http.Request{
		Method:     r.Method,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     h,
	}

	switch len(hosts) {
	case 0:
	case 1:
		if !validHost(hosts[0].Value) {
			return nil, errs.New(errs.InvalidArgument, "The Host header is not a valid host name.").
				WithCode(CodeInvalidHeader).
				WithHint("Enter a host name with an optional port, or disable the Host header.").
				WithDetail("header", hosts[0].Name)
		}
		req.Host = hosts[0].Value
	default:
		return nil, duplicateHeader(hosts[1].Name)
	}

	switch len(agents) {
	case 0:
		// An empty value suppresses Go's default "Go-http-client/1.1".
		h["User-Agent"] = []string{""}
	case 1:
		a := agents[0]
		h[a.Name] = []string{a.Value}
		if a.Name != "User-Agent" {
			// Go writes "User-Agent" itself; the empty canonical entry
			// suppresses its default so the user's spelling is sent.
			h["User-Agent"] = []string{""}
		}
	default:
		return nil, duplicateHeader(agents[1].Name)
	}

	chunked := false
	switch len(encodings) {
	case 0:
	case 1:
		if !strings.EqualFold(strings.TrimSpace(encodings[0].Value), "chunked") {
			return nil, errs.New(errs.Unsupported, "Only \"Transfer-Encoding: chunked\" can be sent.").
				WithCode(CodeHeaderUnsupported).
				WithHint("Set the value to chunked, or disable the header.").
				WithDetail("header", encodings[0].Name)
		}
		chunked = true
	default:
		return nil, duplicateHeader(encodings[1].Name)
	}

	bodyLen := int64(0)
	if r.Body != nil {
		rc, n, err := r.Body.Open()
		if err != nil {
			return nil, errs.Wrap(err, errs.KindOf(err), "The request body could not be read.").
				WithCode(CodeBodyUnavailable)
		}
		bodyLen = n
		body := r.Body
		req.GetBody = func() (io.ReadCloser, error) {
			rc, _, err := body.Open()
			return rc, err
		}
		if ct := body.ContentType(); ct != "" && !hasContentType {
			h["Content-Type"] = []string{ct}
		}
		if n == 0 {
			rc.Close()
			req.Body = http.NoBody
		} else {
			req.Body = rc
			req.ContentLength = max(n, -1)
		}
	}

	for _, l := range lengths {
		v, err := strconv.ParseInt(strings.TrimSpace(l.Value), 10, 64)
		if err != nil || v != bodyLen || bodyLen < 0 {
			return nil, errs.New(errs.InvalidArgument, "The Content-Length header does not match the body length.").
				WithCode(CodeContentLengthMismatch).
				WithHint("Disable the Content-Length header; PostSonde computes it from the body.").
				WithDetail("header", l.Name).
				WithDetail("bodyLength", strconv.FormatInt(bodyLen, 10))
		}
	}

	if chunked {
		if len(lengths) > 0 {
			return nil, errs.New(errs.InvalidArgument, "Content-Length and Transfer-Encoding cannot be sent together.").
				WithCode(CodeInvalidHeader).
				WithHint("Disable one of the two headers.")
		}
		if req.Body == nil || req.Body == http.NoBody {
			empty := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nil)), nil }
			req.Body, _ = empty()
			req.GetBody = empty
		}
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	}

	// Userinfo is never part of the request line. Go would turn it into
	// Basic auth on its own; do it here, visibly and only when no
	// Authorization row is enabled, as browsers and curl do.
	if u.User != nil {
		if !hasAuthorization {
			pass, _ := u.User.Password()
			cred := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pass))
			h["Authorization"] = []string{"Basic " + cred}
		}
		u.User = nil
	}

	return &prepared{req: req, host: u.Host, settings: settings, keepReferer: keepReferer}, nil
}

func duplicateHeader(name string) error {
	return errs.New(errs.Unsupported, "Only one "+name+" header can be sent.").
		WithCode(CodeHeaderUnsupported).
		WithHint("Disable all but one "+name+" header.").
		WithDetail("header", name)
}

// checkMethod accepts any method token (RFC 9110 section 9) except CONNECT,
// which opens a tunnel rather than sending a request.
func checkMethod(m string) error {
	if !isToken(m) {
		msg := "The method " + strconv.Quote(m) + " is not valid."
		if m == "" {
			msg = "The request has no method."
		}
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidMethod).
			WithHint("Use a method such as GET or POST; custom methods may contain letters, digits and !#$%&'*+-.^_`|~.")
	}
	if m == http.MethodConnect {
		return errs.New(errs.Unsupported, "CONNECT requests are not supported.").
			WithCode(CodeMethodUnsupported).
			WithHint("To reach a server through a proxy, configure the proxy instead.")
	}
	return nil
}

// checkHeader validates an enabled header row. The value is never put in
// the error: it may be a secret.
func checkHeader(i int, h Header) error {
	if !isToken(h.Name) {
		return errs.New(errs.InvalidArgument, "Header "+strconv.Itoa(i+1)+" has an invalid name "+strconv.Quote(h.Name)+".").
			WithCode(CodeInvalidHeader).
			WithHint("Header names may contain only letters, digits and !#$%&'*+-.^_`|~.").
			WithDetail("header", h.Name).
			WithDetail("index", strconv.Itoa(i))
	}
	for j := 0; j < len(h.Value); j++ {
		if c := h.Value[j]; (c < ' ' && c != '\t') || c == 0x7f {
			return errs.New(errs.InvalidArgument, "The value of the "+h.Name+" header contains a line break or control character.").
				WithCode(CodeInvalidHeader).
				WithHint("Remove line breaks and control characters from the value.").
				WithDetail("header", h.Name).
				WithDetail("index", strconv.Itoa(i))
		}
	}
	return nil
}

// isToken reports whether s is a non-empty RFC 9110 token.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHost mirrors net/http's check of the Host header.
func validHost(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!$%&'()*+,-.:;=[]_~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// parseURL splits raw into scheme, authority, path and query, keeping the
// path and query as typed apart from the encoding of mode enc
// (escapeTarget).
//
// The path is sent through URL.Opaque, which net/http writes to the request
// line unchanged; Path and RawPath are set as well so relative redirects
// resolve. A path starting with "//" cannot go in Opaque (it would read as
// a network path), so it is sent through RawPath, where Go's own encoding
// applies.
func parseURL(raw string, enc URLEncoding) (*url.URL, error) {
	invalid := func(msg, hint string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidURL).WithHint(hint)
	}
	i := strings.Index(raw, "://")
	if i <= 0 {
		if raw == "" {
			return nil, invalid("The request has no URL.", "Enter a URL such as https://api.example.com/items.")
		}
		return nil, invalid("The URL has no scheme.", "Start the URL with http:// or https://.")
	}
	scheme := strings.ToLower(raw[:i])
	if scheme != "http" && scheme != "https" {
		return nil, errs.New(errs.Unsupported, "Only http:// and https:// URLs can be sent as HTTP requests.").
			WithCode(CodeSchemeUnsupported).
			WithHint("Start the URL with http:// or https://.").
			WithDetail("scheme", scheme)
	}
	rest := raw[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, target := rest[:end], rest[end:]

	// The parse error is not kept as the cause: its text repeats the URL,
	// which may hold a password.
	u, err := url.Parse(scheme + "://" + authority)
	if err != nil || u.Hostname() == "" {
		return nil, invalid("The URL's host is not valid.", "Check the host name and port, e.g. https://api.example.com:8443/.")
	}

	if j := strings.IndexByte(target, '#'); j >= 0 {
		target = target[:j]
	}
	path, query, hasQuery := strings.Cut(target, "?")
	if path == "" {
		path = "/"
	}
	path = escapeTarget(path, enc, false)
	u.RawQuery = escapeTarget(query, enc, true)
	u.ForceQuery = hasQuery && query == ""

	unescaped, err := url.PathUnescape(path)
	if strings.HasPrefix(path, "//") {
		if err != nil {
			return nil, invalid("The URL's path is not validly percent-encoded.", "A path starting with // must use valid %XX escapes.")
		}
		u.Path, u.RawPath = unescaped, path
		return u, nil
	}
	u.Opaque = path
	if err == nil {
		u.Path, u.RawPath = unescaped, path
	} else {
		u.Path = path
	}
	return u, nil
}

// escapeTarget percent-encodes one part of the request target, the path
// (query false) or the query, in encoding mode enc (HTTP-24):
//
//   - every mode encodes the bytes that cannot appear in an HTTP request
//     line: control characters, space, DEL and non-ASCII (UTF-8) bytes;
//   - URLEncodingWHATWG also encodes the rest of the WHATWG URL Standard's
//     path percent-encode set (" < > ` ^ { }) or special-query
//     percent-encode set (" < > ');
//   - URLEncodingRFC3986 keeps only what RFC 3986 allows unencoded in a
//     path (unreserved, sub-delims, ":", "@", "/") or query (the same and
//     "?"), and valid %XX escapes; a stray "%" becomes %25.
//
// Existing escapes are never decoded or re-encoded, and nothing else,
// reserved characters included, is changed.
func escapeTarget(s string, enc URLEncoding, query bool) string {
	const hex = "0123456789ABCDEF"
	encode := func(i int) bool {
		c := s[i]
		if c <= ' ' || c >= 0x7f {
			return true
		}
		switch enc {
		case URLEncodingWHATWG:
			if query {
				return strings.IndexByte(`"<>'`, c) >= 0
			}
			return strings.IndexByte("\"<>`^{}", c) >= 0
		case URLEncodingRFC3986:
			switch {
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
				return false
			case c == '%':
				return i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2])
			case c == '?':
				return !query
			}
			return strings.IndexByte("-._~!$&'()*+,;=:@/", c) < 0
		}
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if encode(i) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	b := make([]byte, 0, len(s)+2*n)
	for i := 0; i < len(s); i++ {
		if c := s[i]; encode(i) {
			b = append(b, '%', hex[c>>4], hex[c&15])
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// MethodQuery is the QUERY method (HTTP-03, draft-ietf-httpbis-safe-method-w-body):
// safe and idempotent like GET, with a body like POST.
const MethodQuery = "QUERY"

// markIdempotent marks a QUERY request as idempotent for net/http, which
// otherwise retries only GET, HEAD, OPTIONS and TRACE when a kept-alive
// connection turns out to be closed: an Idempotency-Key entry without
// values is never written (net/http's documented convention). A row that
// sends the key marks it already.
func markIdempotent(h http.Header) {
	if _, ok := h["Idempotency-Key"]; ok {
		return
	}
	if _, ok := h["X-Idempotency-Key"]; ok {
		return
	}
	h["Idempotency-Key"] = nil
}

// SentURL returns raw as Send puts it on the wire: the path and query
// percent-encoded in mode enc, without the fragment and without userinfo,
// which is returned apart (Send turns it into Basic credentials unless an
// Authorization row is sent). It fails as Send does for a URL it cannot
// send. The code generators (internal/core/snippet) write the URL with it.
func SentURL(raw string, enc URLEncoding) (string, *url.Userinfo, error) {
	u, err := parseURL(raw, enc)
	if err != nil {
		return "", nil, err
	}
	return displayURL(u), u.User, nil
}

// displayURL renders u as sent, without userinfo. url.URL.String would
// drop the host of a URL with Opaque set.
func displayURL(u *url.URL) string {
	if u.Opaque == "" {
		c := *u
		c.User = nil
		return c.String()
	}
	s := u.Scheme + "://" + u.Host + u.Opaque
	if u.ForceQuery || u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	return s
}
