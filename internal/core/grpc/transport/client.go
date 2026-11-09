package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// Compression names for CallOptions.Compression.
const (
	CompressionIdentity = "identity"
	CompressionGzip     = "gzip"
)

// Options configure a Client.
type Options struct {
	// Transport configures the connections exactly as for httpclient: CA
	// roots, certificate verification, idle connection limits, TLS
	// handshake timeout. Transport.TLSConfig (from tlsconf.Build, or
	// NewWithTLS) adds a server name override and a client certificate.
	Transport httpclient.TransportOptions
	// MaxRecvMessageSize limits one received message (on the wire and after
	// decompression). Zero means DefaultMaxRecvMessageSize (4 MiB); a
	// negative value means no limit.
	MaxRecvMessageSize int
}

// Client makes gRPC calls over HTTP/2. Keep one and reuse it: calls to the
// same address share a connection. It is safe for concurrent use.
type Client struct {
	tr *http.Transport
	// webTr carries gRPC-Web calls: HTTP/1.1, or h2 when TLS negotiates it.
	webTr      *http.Transport
	maxRecv    int
	clientCert bool // a client certificate is configured (for error hints)
}

// New returns a Client that opens connections through d. The transport is
// httpclient.NewTransport's (no proxy from the environment, no added
// compression, TLS 1.2+) restricted to HTTP/2: ALPN offers only "h2" on
// TLS connections, and plaintext connections speak HTTP/2 with prior
// knowledge (h2c). gRPC-Web calls use a second transport of the same
// settings that speaks HTTP/1.1, and HTTP/2 where TLS ALPN picks it. It
// panics if d is nil.
func New(d netx.Dialer, o Options) *Client {
	tr := httpclient.NewTransport(d, o.Transport)
	var p http.Protocols
	p.SetHTTP2(true)
	p.SetUnencryptedHTTP2(true)
	tr.Protocols = &p
	webTr := httpclient.NewTransport(d, o.Transport)
	var wp http.Protocols
	wp.SetHTTP1(true)
	wp.SetHTTP2(true)
	webTr.Protocols = &wp
	maxRecv := o.MaxRecvMessageSize
	if maxRecv == 0 {
		maxRecv = DefaultMaxRecvMessageSize
	}
	c := &Client{tr: tr, webTr: webTr, maxRecv: maxRecv, clientCert: hasClientCert(tr.TLSClientConfig)}
	recordCertificateRequests(tr.TLSClientConfig)
	recordCertificateRequests(webTr.TLSClientConfig)
	return c
}

// NewWithTLS builds sec with tlsconf.Build (relative paths resolved
// against baseDir) and returns a Client whose TLS calls use it: trusted
// CAs, verification, server name override, client certificate for mTLS
// and minimum version. It replaces o.Transport's RootCAs,
// InsecureSkipVerify and TLSConfig. The Result carries the warnings (an
// expired client certificate, verification off) for the UI. Calls
// without CallOptions.TLS still use plaintext h2c.
func NewWithTLS(d netx.Dialer, o Options, sec tlsconf.Options, baseDir string) (*Client, *tlsconf.Result, error) {
	res, err := tlsconf.Build(sec, baseDir)
	if err != nil {
		return nil, nil, err
	}
	o.Transport.RootCAs = nil
	o.Transport.InsecureSkipVerify = false
	o.Transport.TLSConfig = res.Config
	return New(d, o), res, nil
}

func hasClientCert(c *tls.Config) bool {
	return c != nil && (len(c.Certificates) > 0 || c.GetClientCertificate != nil)
}

// certRequestKey is the context key of the *atomic.Bool a TLS call sets
// when the server asks for a client certificate during the handshake of
// the connection dialed for it. Servers rarely say why they refuse a
// certificate (TLS 1.3 alerts arrive after the handshake and net/http
// loses them), so this tells "mTLS failed" apart from other failures.
type certRequestKey struct{}

// recordCertificateRequests makes cfg (the transport's own clone) note
// certificate requests in the handshake context. It keeps the configured
// client certificate: an existing GetClientCertificate is called, and a
// config with only Certificates is left alone.
func recordCertificateRequests(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	get := cfg.GetClientCertificate
	if get == nil && len(cfg.Certificates) > 0 {
		return // crypto/tls's own choice among the certificates is kept
	}
	cfg.GetClientCertificate = func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if flag, ok := cri.Context().Value(certRequestKey{}).(*atomic.Bool); ok {
			flag.Store(true)
		}
		if get == nil {
			return &tls.Certificate{}, nil // send none
		}
		return get(cri)
	}
}

// CloseIdleConnections closes connections with no call in progress.
func (c *Client) CloseIdleConnections() {
	c.tr.CloseIdleConnections()
	c.webTr.CloseIdleConnections()
}

// CallOptions describe one call.
type CallOptions struct {
	// Address is the server's host:port (the port may be omitted: 80, or
	// 443 with TLS).
	Address string
	// Protocol is ProtocolGRPC (also ""), ProtocolGRPCWeb or
	// ProtocolGRPCWebText. A gRPC-Web call is half-duplex: Send collects
	// the messages and CloseSend sends the request, so Header and Recv
	// wait for CloseSend.
	Protocol Protocol
	// TLS selects TLS with ALPN h2 and the Client's TLS settings;
	// otherwise the call uses plaintext HTTP/2 with prior knowledge (h2c).
	TLS bool
	// Authority overrides the :authority pseudo-header (Address by
	// default). It does not change the TLS server name.
	Authority string
	// Method is the full method name, "/package.Service/Method".
	Method string
	// Metadata is sent as request headers. Keys must be lower case and not
	// reserved (ReservedKey); "-bin" values are binary and sent in base64.
	Metadata Metadata
	// Timeout bounds the call and is sent as grpc-timeout. Zero means no
	// timeout besides ctx's deadline, which is sent too (the earlier one
	// wins).
	Timeout time.Duration
	// ContentSubtype makes the content-type "application/grpc+<subtype>"
	// ("proto", "json"); empty sends "application/grpc". gRPC-Web calls
	// send "application/grpc-web+<subtype>" (or -web-text), "+proto" when
	// empty.
	ContentSubtype string
	// UserAgent is sent as user-agent; empty sends none.
	UserAgent string
	// Compression is "" or "identity" (none) or "gzip": messages sent are
	// compressed, and grpc-encoding and grpc-accept-encoding say so.
	// Received messages compressed with gzip are decompressed either way.
	Compression string
	// MaxRecvMessageSize overrides the Client's limit when non-zero
	// (negative: no limit).
	MaxRecvMessageSize int
}

// Invoke starts a call and returns its Stream. Nothing is waited for: the
// connection is opened and the request headers are sent in the
// background, and errors from that surface on the Stream. Invoke itself
// fails only for invalid options (kind invalid_argument, nothing sent) or
// a deadline that has already passed.
//
// For gRPC-Web the request is sent when CloseSend is called.
//
// The caller must call Recv until it returns an error, or Close.
func (c *Client) Invoke(ctx context.Context, o CallOptions) (*Stream, error) {
	req, err := c.prepare(o)
	if err != nil {
		return nil, err
	}
	var cancel context.CancelFunc
	if o.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			cancel()
			return nil, statusErr(Status{Code: DeadlineExceeded, Message: "deadline passed before the call started"}, context.DeadlineExceeded,
				CodeTimeout, "The call to "+o.Address+" exceeded its deadline before it started.", HintTimeout)
		}
		req.Header[HeaderTimeout] = []string{EncodeTimeout(left)}
	}
	sec := security{tls: o.TLS, clientCert: c.clientCert}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, transportErr(ctx, err, o.Address, sec)
	}
	if o.TLS {
		sec.certRequested = new(atomic.Bool)
		ctx = context.WithValue(ctx, certRequestKey{}, sec.certRequested)
	}

	maxRecv := c.maxRecv
	if o.MaxRecvMessageSize != 0 {
		maxRecv = o.MaxRecvMessageSize
	}
	s := &Stream{
		ctx:     ctx,
		cancel:  cancel,
		address: o.Address,
		sec:     sec,
		gzip:    o.Compression == CompressionGzip,
		maxRecv: maxRecv,
		ready:   make(chan struct{}),
	}
	if o.Protocol.Web() {
		s.web = &webCall{tr: c.webTr, req: req.WithContext(ctx), text: o.Protocol == ProtocolGRPCWebText}
		// A call cancelled before CloseSend never starts.
		context.AfterFunc(ctx, func() {
			s.web.once.Do(func() {
				s.finish(transportErr(ctx, ctx.Err(), o.Address, sec), nil)
				close(s.ready)
			})
		})
		return s, nil
	}

	pr, pw := io.Pipe()
	req.Body = pr
	req = req.WithContext(ctx)
	s.pw = pw
	stop := context.AfterFunc(ctx, func() { pr.CloseWithError(context.Cause(ctx)) })
	go s.roundTrip(c.tr, req, stop)
	return s, nil
}

// prepare validates o and builds the request without body or context.
func (c *Client) prepare(o CallOptions) (*http.Request, error) {
	u, err := callURL(o)
	if err != nil {
		return nil, err
	}
	if err := validateMethod(o.Method); err != nil {
		return nil, err
	}
	if err := validateMetadata(o.Metadata); err != nil {
		return nil, err
	}
	if !validToken(o.ContentSubtype) {
		return nil, errs.New(errs.InvalidArgument, "The content subtype "+strconv.Quote(o.ContentSubtype)+" may only contain lower-case letters, digits and '-', '_', '.'.").
			WithCode(CodeInvalidOption).WithDetail("field", "contentSubtype")
	}
	switch o.Protocol {
	case "", ProtocolGRPC, ProtocolGRPCWeb, ProtocolGRPCWebText:
	default:
		return nil, errs.New(errs.Unsupported, "The protocol "+strconv.Quote(string(o.Protocol))+" is not supported; use grpc, grpc-web or grpc-web-text.").
			WithCode(CodeInvalidOption).WithDetail("field", "protocol")
	}
	switch o.Compression {
	case "", CompressionIdentity, CompressionGzip:
	default:
		return nil, errs.New(errs.Unsupported, "The compression "+strconv.Quote(o.Compression)+" is not supported; use gzip or none.").
			WithCode(CodeInvalidOption).WithDetail("field", "compression")
	}
	for i := 0; i < len(o.UserAgent); i++ {
		if ch := o.UserAgent[i]; ch < 0x20 || ch > 0x7E {
			return nil, errs.New(errs.InvalidArgument, "The user agent may only contain printable ASCII characters.").
				WithCode(CodeInvalidOption).WithDetail("field", "userAgent")
		}
	}

	h := http.Header{}
	proto, major := "HTTP/2.0", 2
	if o.Protocol.Web() {
		proto, major = "HTTP/1.1", 1
		ct, accept := ContentTypeGRPCWeb, ContentTypeGRPCWeb
		if o.Protocol == ProtocolGRPCWebText {
			ct, accept = ContentTypeGRPCWebText, ContentTypeGRPCWebText
		}
		sub := o.ContentSubtype
		if sub == "" {
			sub = "proto"
		}
		h[HeaderContentType] = []string{ct + "+" + sub}
		h[HeaderAccept] = []string{accept}
		h[HeaderXGRPCWeb] = []string{"1"}
	} else {
		ct := ContentTypeGRPC
		if o.ContentSubtype != "" {
			ct += "+" + o.ContentSubtype
		}
		h[HeaderContentType] = []string{ct}
		h[HeaderTE] = []string{"trailers"}
	}
	// An empty user-agent value tells net/http to send none.
	h[HeaderUserAgent] = []string{o.UserAgent}
	if o.Compression == CompressionGzip {
		h[HeaderEncoding] = []string{CompressionGzip}
		h[HeaderAcceptEncoding] = []string{CompressionGzip}
	}
	addMetadata(h, o.Metadata)
	if o.Protocol.Web() {
		// HTTP/1.1 looks fields up by their canonical form (an empty
		// User-Agent only suppresses Go's default under that key).
		c := make(http.Header, len(h))
		for k, vv := range h {
			ck := http.CanonicalHeaderKey(k)
			c[ck] = append(c[ck], vv...)
		}
		h = c
	}

	host := o.Address
	if o.Authority != "" {
		host = o.Authority
	}
	return &http.Request{
		Method:        http.MethodPost,
		URL:           u,
		Proto:         proto,
		ProtoMajor:    major,
		Header:        h,
		Host:          host,
		ContentLength: -1,
	}, nil
}

func callURL(o CallOptions) (*url.URL, error) {
	bad := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidAddress).WithDetail("field", "address")
	}
	if o.Address == "" {
		return nil, bad("The server address is empty.")
	}
	if strings.ContainsAny(o.Address, "/?#@ \t\r\n") {
		return nil, bad("The server address " + strconv.Quote(o.Address) + " must be host:port, without a scheme or path.")
	}
	scheme := "http"
	if o.TLS {
		scheme = "https"
	}
	u, err := url.Parse(scheme + "://" + o.Address)
	if err != nil || u.Host != o.Address || u.Hostname() == "" {
		return nil, bad("The server address " + strconv.Quote(o.Address) + " is not a valid host:port.")
	}
	if strings.ContainsAny(o.Authority, "/?#@ \t\r\n") {
		return nil, errs.New(errs.InvalidArgument, "The authority "+strconv.Quote(o.Authority)+" must be host or host:port.").
			WithCode(CodeInvalidOption).WithDetail("field", "authority")
	}
	u.Path = o.Method
	return u, nil
}

// validateMethod checks "/service/method": a leading slash, two non-empty
// parts, printable ASCII without spaces, '?', '#' or '%'.
func validateMethod(m string) error {
	bad := errs.New(errs.InvalidArgument, "The method "+strconv.Quote(m)+" must be /package.Service/Method.").
		WithCode(CodeInvalidMethod).WithDetail("field", "method")
	rest, ok := strings.CutPrefix(m, "/")
	if !ok {
		return bad
	}
	svc, name, ok := strings.Cut(rest, "/")
	if !ok || svc == "" || name == "" || strings.Contains(name, "/") {
		return bad
	}
	for i := 0; i < len(m); i++ {
		if c := m[i]; c <= 0x20 || c > 0x7E || c == '?' || c == '#' || c == '%' {
			return bad
		}
	}
	return nil
}

func validToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// UnaryResult is the outcome of a unary call that reached the server.
type UnaryResult struct {
	Header  Metadata
	Message []byte
	Trailer Metadata
	Status  Status
}

// Unary sends one message and reads one. The result is non-nil once the
// call started, also with an error (then Status is the error's status and
// Header/Trailer hold whatever arrived). A non-OK status is an error; so
// is an OK status with zero or several response messages (Internal, as in
// grpc-go).
func (c *Client) Unary(ctx context.Context, o CallOptions, msg []byte) (*UnaryResult, error) {
	s, err := c.Invoke(ctx, o)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := s.Send(msg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	s.CloseSend()

	res := &UnaryResult{}
	res.Header, _ = s.Header()
	finish := func(err error) (*UnaryResult, error) {
		res.Trailer = s.Trailer()
		res.Status = StatusFromError(err)
		return res, err
	}
	m, err := s.Recv()
	if errors.Is(err, io.EOF) {
		st := Status{Code: Internal, Message: "cardinality violation: no response message for a unary call"}
		return finish(statusErr(st, nil, CodeCardinality, "The server sent no response message.", ""))
	}
	if err != nil {
		return finish(err)
	}
	res.Message = m
	if _, err := s.Recv(); err == nil {
		st := Status{Code: Internal, Message: "cardinality violation: several response messages for a unary call"}
		return finish(statusErr(st, nil, CodeCardinality, "The server sent more than one response message.", ""))
	} else if !errors.Is(err, io.EOF) {
		return finish(err)
	}
	return finish(nil)
}
