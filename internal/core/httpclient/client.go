package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Response is a response as received.
type Response struct {
	// Proto is "HTTP/1.1" or "HTTP/2.0".
	Proto string
	// StatusCode is the status code; Status is the status line after the
	// protocol, with the reason phrase as the server sent it on HTTP/1.x
	// ("200 OK", "299 Fine Thanks") and Go's standard text on HTTP/2.
	StatusCode int
	Status     string
	// Headers are the response headers, sorted by name, the values of one
	// name in the order received. net/http canonicalizes the names
	// ("content-type" becomes "Content-Type") and does not keep the order
	// of different names. Enabled is always true.
	Headers []Header
	// Trailers are the trailers received after the body, as Headers.
	Trailers []Header
	// Body is the response body as received: never decompressed. It is
	// cut at Settings.MaxBodyBytes; BodyTruncated then is true. Never nil.
	Body          []byte
	BodyTruncated bool
	// Size is the number of body bytes read. It is len(Body) unless the
	// body was truncated; then PostSonde stopped reading once more than
	// MaxBodyBytes had arrived.
	Size int64
	// DecodedSize is the number of body bytes kept: len(Body) for Send,
	// the bytes written, after decompression, for SendTo; 0 for
	// SendStream.
	DecodedSize int64
	// Encoding is the content coding SendTo removed while receiving
	// (EncodingGzip, EncodingDeflate), "" when none was (always for Send).
	Encoding string
	// DecodeError says why SendTo's decompression stopped part way; the
	// body then holds what was decoded until there. "" when it did not.
	DecodeError string
	// ContentLength is the body length announced by the server, or -1.
	ContentLength int64
	// URL is the URL of the response: the request's, or the last one after
	// redirects. Without userinfo and fragment.
	URL string
	// Duration is the time from the start of the send to the end of the
	// body, monotonic. It equals Timing.Total.
	Duration time.Duration
	// Timing is the phase breakdown of the final hop (DBG-03), with Total
	// covering the whole send.
	Timing Timing
	// Conn describes the connection of the final hop (DBG-04, DBG-05).
	Conn Conn
	// Redirects are the hops before the final one, in order: the
	// redirects followed and the auth challenges answered (Hop.Kind).
	// Empty when there was neither.
	Redirects []Hop
	// Events is the raw trace of the send, all hops, in order (DBG-01).
	Events []TimelineEvent
	// RequestCookie is the Cookie header the final hop sent: the Cookie
	// rows followed by the cookie jar's cookies, joined by "; ". "" when
	// none was sent.
	RequestCookie string
	// Request is the final hop's request as written: URL, header fields
	// and body after variables, auth and cookies (DBG-01). The hops before
	// it are in Redirects. Never nil.
	Request *SentRequest
}

// Client sends requests. It keeps one transport, so connections are reused
// across requests to the same host. It is safe for concurrent use.
type Client struct {
	// conns holds the transports, one per HTTPVersion, shared by the
	// copies WithCookieJar makes.
	conns *transports
	now   func() time.Time
	// jar is the cookie jar, nil for none (WithCookieJar).
	jar CookieJar
	// afterHeaders is a test hook, called once the response headers are
	// in and before the body is read.
	afterHeaders func()
	// cnonce makes the client nonces of Digest answers; newCnonce, or a
	// fixed value in tests.
	cnonce func() string
}

// New returns a Client that connects through d. It panics if d is nil.
//
// When d is a netx.ForwardProxier (the app's dialer), http:// requests
// that its proxy route sends to an HTTP proxy are written to that proxy
// in absolute form, with its credentials (NET-02); https:// requests and
// the other routes go through d's tunnels (docs/network.md).
func New(d netx.Dialer, o TransportOptions) *Client {
	fp, _ := d.(netx.ForwardProxier)
	return &Client{
		conns:  &transports{dialer: d, options: o, forward: fp, base: NewTransport(d, o), byVersion: map[HTTPVersion]*transport{}},
		now:    time.Now,
		cnonce: newCnonce,
	}
}

// CloseIdleConnections closes the connections kept for reuse.
func (c *Client) CloseIdleConnections() { c.conns.closeIdle() }

// transports are a Client's connection pools: the base transport for
// HTTPVersionAuto and, made on first use with the same dialer and options,
// one restricted to one protocol for each other HTTPVersion (HTTP-25). A
// connection negotiated for one choice is never reused for another.
type transports struct {
	dialer  netx.Dialer
	options TransportOptions
	// forward, when not nil, names the HTTP proxy of http:// hops sent in
	// absolute form.
	forward netx.ForwardProxier
	base    *http.Transport

	mu        sync.Mutex
	byVersion map[HTTPVersion]*transport
}

// transport is one pool and the http.Client that sends through it.
type transport struct {
	tr     *http.Transport
	client *http.Client
	// certs holds the pools of per-host client certificates, nil without
	// ClientCerts.
	certs *certRouter
	// proxies holds the pools of absolute-form requests to HTTP proxies,
	// nil without a netx.ForwardProxier.
	proxies *forwardRouter
}

// get returns the pool of version v.
func (t *transports) get(v HTTPVersion) *transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.byVersion[v]; ok {
		return p
	}
	tr := t.base
	if v != HTTPVersionAuto {
		tr = NewTransport(t.dialer, t.options)
		var p http.Protocols
		switch v {
		case HTTPVersion1:
			p.SetHTTP1(true)
		case HTTPVersion2:
			// h2 through ALPN on https, h2c with prior knowledge on http.
			p.SetHTTP2(true)
			p.SetUnencryptedHTTP2(true)
		}
		tr.Protocols = &p
	}
	p := &transport{tr: tr}
	var hop http.RoundTripper = tr
	if t.options.ClientCerts != nil {
		protocols := tr.Protocols
		p.certs = &certRouter{
			pick: t.options.ClientCerts,
			base: tr,
			newTransport: func(c *ClientCert) *http.Transport {
				o := t.options
				o.ClientCertificate = c
				nt := NewTransport(t.dialer, o)
				nt.Protocols = protocols
				return nt
			},
		}
		hop = p.certs
	}
	// h2c with prior knowledge (HTTPVersion2) is not sent to a proxy in
	// absolute form; it goes through a CONNECT tunnel.
	if t.forward != nil && v != HTTPVersion2 {
		p.proxies = &forwardRouter{
			lookup: t.forward.ForwardProxy,
			base:   hop,
			newTransport: func(proxy *netx.HTTPProxy) *http.Transport {
				return newForwardTransport(t.dialer, t.options, proxy)
			},
		}
		hop = p.proxies
	}
	// No overall timeout: timeouts are per request. The cookie jar is not
	// http.Client's but the Client's own (WithCookieJar), applied per hop
	// by cookieTransport. authTransport answers Digest challenges around
	// each hop. forwardRouter sends http:// hops to an HTTP proxy in
	// absolute form; certRouter picks the connection pool of the hop's
	// client certificate (ClientCerts).
	p.client = &http.Client{Transport: &authTransport{base: &cookieTransport{base: hop}}, CheckRedirect: checkRedirect}
	t.byVersion[v] = p
	return p
}

func (t *transports) closeIdle() {
	t.base.CloseIdleConnections()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.byVersion {
		p.tr.CloseIdleConnections()
		if p.certs != nil {
			p.certs.closeIdle()
		}
		if p.proxies != nil {
			p.proxies.closeIdle()
		}
	}
}

// stage is how far a send got, for error messages.
type stage int32

const (
	stageBeforeSend stage = iota
	stageConnecting
	stageWaiting
	stageBody
)

var stageNames = [...]string{"before_send", "connecting", "waiting", "reading_body"}

// String returns the stage's name, as in the "stage" error detail.
func (s stage) String() string { return stageNames[s] }

// Causes of the cancellations Send makes itself.
var (
	errRequestTimeout = errors.New("httpclient: request timeout")
	errHeaderTimeout  = errors.New("httpclient: response header timeout")
)

// sendState is one Send's state, reachable from redirect checks and trace
// hooks through the request context.
type sendState struct {
	// certRequested is set when a server asked this send's connection for
	// a client certificate (ClientCerts), to tell a TLS 1.2 "handshake
	// failure" about a missing or refused certificate from others.
	certRequested atomic.Bool
	// certSent is set when a client certificate was sent to it.
	certSent    atomic.Bool
	settings    Settings
	keepReferer bool
	stage       atomic.Int32
	redirects   atomic.Int32
	rec         *recorder
	jar         CookieJar
	// origin is the URL of the request as sent first; Digest challenges
	// of other origins are not answered unless ForwardAuthorization.
	origin *url.URL
	// digest are the request's Digest credentials, nil for none.
	digest        *DigestCredentials
	digestSession digestSession
	cnonce        func() string

	cookieMu sync.Mutex
	// cookieHeaders is the Cookie header sent by each hop, in order.
	cookieHeaders []string
}

func (s *sendState) addCookieHeader(h string) {
	s.cookieMu.Lock()
	defer s.cookieMu.Unlock()
	s.cookieHeaders = append(s.cookieHeaders, h)
}

// cookieHeaderOf returns the Cookie header hop i sent.
func (s *sendState) cookieHeaderOf(i int) string {
	s.cookieMu.Lock()
	defer s.cookieMu.Unlock()
	if i < len(s.cookieHeaders) {
		return s.cookieHeaders[i]
	}
	return ""
}

type stateKey struct{}

// Send sends r and reads the response.
//
// The context cancels the request at any stage: connecting, waiting for
// the response headers or reading the body; the error then has kind
// cancelled. Hooks in ctx see the request: a netx.Trace (netx.WithTrace)
// sees each dial, and a net/http/httptrace ClientTrace sees DNS, connect,
// TLS, the written request and the first response byte, including whether
// the connection was reused.
//
// Errors are *errs.Error values; docs/http-client.md lists the codes. A
// response of any status, 4xx and 5xx included, is not an error.
func (c *Client) Send(ctx context.Context, r Request) (*Response, error) {
	return c.send(ctx, r, nil, nil)
}

// send is Send, SendStream when stream is not nil, or SendTo when target
// is not nil.
func (c *Client) send(ctx context.Context, r Request, stream StreamFunc, target *BodyTarget) (*Response, error) {
	p, err := prepare(r)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, mapError(ctx, err, stageBeforeSend, p.host)
	}
	s := p.settings
	start := c.now()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopTimeout := func() {}
	switch {
	case s.Timeout > 0 && stream != nil:
		// A stream's timeout ends with the response headers.
		t := time.AfterFunc(s.Timeout, func() { cancel(errRequestTimeout) })
		defer t.Stop()
		stopTimeout = func() { t.Stop() }
	case s.Timeout > 0:
		var stop context.CancelFunc
		ctx, stop = context.WithTimeoutCause(ctx, s.Timeout, errRequestTimeout)
		defer stop()
	}
	rec := newRecorder(c.now, start)
	defer rec.close()
	st := &sendState{settings: s, keepReferer: p.keepReferer, rec: rec, jar: c.jar, origin: p.req.URL, cnonce: c.cnonce}
	if r.Digest != nil {
		cred := *r.Digest
		st.digest = &cred
	}
	st.stage.Store(int32(stageConnecting))
	hdr := &headerTimer{d: s.ResponseHeaderTimeout, fire: func() { cancel(errHeaderTimeout) }}
	defer hdr.stop()

	ctx = context.WithValue(ctx, stateKey{}, st)
	// The recorder's hooks go on each hop's request (cookieTransport).
	// Both compose with the traces the caller put in ctx; ours run first.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn:              func(string) { st.stage.Store(int32(stageConnecting)) },
		GotConn:              func(httptrace.GotConnInfo) { st.stage.Store(int32(stageWaiting)) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { hdr.start() },
		GotFirstResponseByte: hdr.pause,
	})

	if detail := optionsDetail(s, c.jar != nil); detail != "" {
		rec.record(EventRequestOptions, detail, nil)
	}
	resp, err := c.conns.get(s.HTTPVersion).client.Do(p.req.WithContext(ctx))
	if err != nil {
		e := mapError(ctx, err, stage(st.stage.Load()), p.host)
		if s.HTTPVersion == HTTPVersion2 && noHTTP2(err, e) {
			e = errs.Wrap(err, errs.Protocol, "The server at "+p.host+" does not speak HTTP/2.").
				WithCode(CodeHTTP2Unsupported).
				WithHint("Set the HTTP version to Automatic or HTTP/1.1 in the request's Settings tab.").
				WithDetail("stage", e.Details["stage"])
		}
		return nil, e
	}
	defer resp.Body.Close()
	hdr.stop()
	st.stage.Store(int32(stageBody))
	if c.afterHeaders != nil {
		c.afterHeaders()
	}

	var body []byte
	var size, decoded int64
	var truncated bool
	var streamErr error
	var encoding, decodeErr string
	switch {
	case target != nil:
		w, err := target.Open(streamHead(resp))
		if err != nil {
			return nil, err
		}
		res, readErr, writeErr := copyBody(w, resp, target, s.MaxBodyBytes)
		switch {
		case readErr != nil:
			return nil, mapError(ctx, readErr, stageBody, p.host)
		case writeErr != nil:
			return nil, writeFailed(writeErr)
		}
		body, size, decoded, truncated = []byte{}, res.size, res.written, res.truncated
		encoding, decodeErr = res.encoding, res.decodeErr
	case stream != nil:
		stopTimeout()
		if err := ctx.Err(); err != nil {
			// Cancelled, or timed out, as the headers came in.
			return nil, mapError(ctx, err, stageWaiting, p.host)
		}
		size, streamErr = c.stream(ctx, resp, p.host, stream)
		body = []byte{}
	default:
		body, size, truncated, err = readBody(resp.Body, resp.ContentLength, s.MaxBodyBytes)
		if err != nil {
			return nil, mapError(ctx, err, stageBody, p.host)
		}
		decoded = int64(len(body))
	}
	timing, conn, hops, events, sent := rec.finish(resp, size, truncated)
	for i := range hops {
		hops[i].RequestCookie = st.cookieHeaderOf(i)
	}
	return &Response{
		Proto:         resp.Proto,
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Headers:       headerList(resp.Header),
		Trailers:      headerList(resp.Trailer),
		Body:          body,
		BodyTruncated: truncated,
		Size:          size,
		DecodedSize:   decoded,
		Encoding:      encoding,
		DecodeError:   decodeErr,
		ContentLength: resp.ContentLength,
		URL:           displayURL(resp.Request.URL),
		Duration:      timing.Total,
		Timing:        timing,
		Conn:          conn,
		Redirects:     hops,
		Events:        events,
		RequestCookie: st.cookieHeaderOf(len(hops)),
		Request:       sent,
	}, streamErr
}

// checkRedirect applies the request's redirect settings. net/http has
// already copied the headers (dropping Authorization and cookies for
// another host that is not a subdomain) and added a Referer. Credentials
// are dropped here for any other origin, subdomains, ports and schemes
// included (PRV-11), unless Settings.ForwardAuthorization puts
// Authorization back. Settings.KeepMethod undoes the switch to GET of a
// 301, 302 or 303 (HTTP-22).
func checkRedirect(req *http.Request, via []*http.Request) error {
	st, _ := req.Context().Value(stateKey{}).(*sendState)
	if st == nil || !st.settings.FollowRedirects || st.settings.MaxRedirects == 0 {
		return http.ErrUseLastResponse
	}
	if n := st.settings.MaxRedirects; len(via) > n {
		return errs.New(errs.Protocol, "The server redirected more than "+strconv.Itoa(n)+" times.").
			WithCode(CodeTooManyRedirects).
			WithHint("Increase the maximum number of redirects, or stop following redirects to see the redirect response.").
			WithDetail("maxRedirects", strconv.Itoa(n))
	}
	// A query-only Location ("?page=2") resolved against an Opaque URL
	// loses the host.
	if req.URL.Host == "" {
		req.URL.Host = via[len(via)-1].URL.Host
	}
	if !st.keepReferer {
		delete(req.Header, "Referer")
	}
	if st.settings.KeepMethod || queryKeepsMethod(req, via[0]) {
		if err := keepMethod(req, via[0]); err != nil {
			return err
		}
	}
	if !sameOrigin(via[0].URL, req.URL) {
		dropCredentials(req.Header)
	}
	if st.settings.ForwardAuthorization {
		// net/http keeps it dropped once a hop left the host, even back
		// at the origin.
		copyHeader(req.Header, via[0].Header, "Authorization")
	}
	st.redirects.Store(int32(len(via)))
	if st.rec != nil && req.Response != nil {
		st.rec.redirect(req.Response, req)
	}
	return nil
}

// queryKeepsMethod reports whether req, the redirect of a QUERY request
// orig, keeps the method and body (HTTP-03): on a 301 or 302, which may
// change only a POST to GET (RFC 9110 sections 15.4.2 and 15.4.3), unlike
// net/http, which changes every method but GET and HEAD. A 303 is answered
// with a GET, as for any method (draft-ietf-httpbis-safe-method-w-body);
// 307 and 308 keep them anyway.
func queryKeepsMethod(req, orig *http.Request) bool {
	if orig.Method != MethodQuery || req.Response == nil {
		return false
	}
	code := req.Response.StatusCode
	return code == http.StatusMovedPermanently || code == http.StatusFound
}

// bodyHeaders are the headers net/http drops with the body when a 301,
// 302 or 303 redirect turns the request into a GET.
var bodyHeaders = []string{"Content-Encoding", "Content-Language", "Content-Location", "Content-Type"}

// keepMethod undoes net/http's switch to GET on a 301, 302 or 303
// redirect (Settings.KeepMethod): req gets orig's method, a fresh copy of
// its body and the body headers net/http removed. A 307 or 308 redirect
// has them already.
func keepMethod(req, orig *http.Request) error {
	if req.Method == orig.Method {
		return nil
	}
	req.Method = orig.Method
	if orig.GetBody != nil {
		body, err := orig.GetBody()
		if err != nil {
			return err
		}
		req.Body, req.GetBody, req.ContentLength = body, orig.GetBody, orig.ContentLength
	}
	for _, h := range bodyHeaders {
		copyHeader(req.Header, orig.Header, h)
	}
	return nil
}

// copyHeader copies the values of name, in any spelling, from src to dst,
// unless dst has it already.
func copyHeader(dst, src http.Header, name string) {
	for k := range dst {
		if strings.EqualFold(k, name) {
			return
		}
	}
	for k, v := range src {
		if strings.EqualFold(k, name) {
			dst[k] = slices.Clone(v)
		}
	}
}

// credentialHeaders are never sent to an origin other than the one the
// user sent the request to (PRV-11).
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "WWW-Authenticate"}

// dropCredentials removes the credential headers from h, in any spelling.
// net/http drops them itself only when the host changes to one that is not
// a subdomain, and keeps them across a scheme or port change; PostSonde,
// like curl and browsers, keeps them only for the same scheme, host and
// port.
func dropCredentials(h http.Header) {
	for name := range h {
		for _, c := range credentialHeaders {
			if strings.EqualFold(name, c) {
				delete(h, name)
			}
		}
	}
}

// sameOrigin reports whether a and b have the same scheme, host and port
// (the default port of the scheme when none is given).
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		portOf(a) == portOf(b)
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// readBody reads up to max bytes of r (no limit if max < 0) and reports
// whether there was more.
func readBody(r io.Reader, contentLength, limit int64) (body []byte, size int64, truncated bool, err error) {
	const preallocMax = 16 << 20
	var buf bytes.Buffer
	if n := contentLength; n > 0 {
		if limit >= 0 {
			n = min(n, limit)
		}
		buf.Grow(int(min(n, preallocMax)))
	}
	src := r
	if limit >= 0 && limit < math.MaxInt64 {
		src = io.LimitReader(r, limit+1)
	}
	size, err = buf.ReadFrom(src)
	body = buf.Bytes()
	if body == nil {
		body = []byte{}
	}
	if limit >= 0 && size > limit {
		return body[:limit], size, true, nil
	}
	return body, size, false, err
}

// headerList flattens h into a list sorted by name.
func headerList(h http.Header) []Header {
	out := make([]Header, 0, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[name] {
			out = append(out, Header{Name: name, Value: v, Enabled: true})
		}
	}
	return out
}

// headerTimer implements Settings.ResponseHeaderTimeout: it runs from each
// written request to the first response byte.
type headerTimer struct {
	d    time.Duration
	fire func()

	mu      sync.Mutex
	t       *time.Timer
	stopped bool
}

func (h *headerTimer) start() {
	if h.d <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.stopped:
	case h.t == nil:
		h.t = time.AfterFunc(h.d, h.fire)
	default:
		h.t.Reset(h.d)
	}
}

func (h *headerTimer) pause() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.t != nil {
		h.t.Stop()
	}
}

func (h *headerTimer) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	if h.t != nil {
		h.t.Stop()
	}
}
