// Package grpctest is an in-process gRPC server for tests: net/http's
// HTTP/2 server (h2c or TLS with ALPN h2, via httptest) with gRPC framing,
// status trailers and grpc-timeout deadlines, a set of echo methods for
// every call kind, and handlers that break the protocol on purpose.
//
// Servers run as h2c, TLS with a CA generated at test time, or mTLS that
// requires client certificates from a second generated CA (NewServerWith).
//
// The same handlers answer gRPC-Web (application/grpc-web and
// application/grpc-web-text, over HTTP/1.1 or HTTP/2) with the status in a
// trailer frame, and gzip: compressed requests are read, and responses are
// compressed like the request (as grpc-go does), or as the
// "x-response-encoding" metadata says.
//
// It is test support only; nothing outside tests imports it.
package grpctest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// Echo service method names.
const (
	// MethodUnary echoes its one request message.
	MethodUnary = "/grpctest.Echo/Unary"
	// MethodServerStream answers one request with "x-count" copies (default
	// 3), each followed by its index ("msg0", "msg1", ...).
	MethodServerStream = "/grpctest.Echo/ServerStream"
	// MethodClientStream answers all request messages with their
	// concatenation.
	MethodClientStream = "/grpctest.Echo/ClientStream"
	// MethodBidi echoes each message as it arrives.
	MethodBidi = "/grpctest.Echo/Bidi"
	// MethodStatus reads messages until the client closes, echoing each,
	// then ends with the status in "x-status-code", "x-status-message" and
	// "x-status-details-bin". With "x-trailers-only: 1" and no messages it
	// sends a trailers-only response.
	MethodStatus = "/grpctest.Echo/Status"
	// MethodSleep waits for "x-sleep" (a Go duration, default forever) or
	// for the call's deadline, whichever comes first, then answers
	// DEADLINE_EXCEEDED (deadline) or OK with no message.
	MethodSleep = "/grpctest.Echo/Sleep"
)

// Handler serves one call and returns its final status.
type Handler func(ss *ServerStream) transport.Status

// Server is a running test server.
type Server struct {
	// Addr is host:port to put in transport.CallOptions.Address.
	Addr string
	// TLS reports whether the server speaks TLS.
	TLS bool
	// RootCAs trusts the server's certificate (nil without TLS).
	RootCAs *x509.CertPool
	// Security is how the server accepts connections.
	Security Security
	// CA signed the server certificate (nil with H2C).
	CA *tlstest.CA
	// ClientCA verifies client certificates (MTLS only). ClientCert is a
	// valid client certificate issued by it.
	ClientCA   *tlstest.CA
	ClientCert *tlstest.Cert

	srv *httptest.Server

	mu       sync.Mutex
	handlers map[string]Handler
	raw      map[string]http.HandlerFunc
	last     *http.Request

	reflection *reflectionIndex // EnableReflection
}

// Security selects how a test server accepts connections.
type Security int

const (
	// H2C is plaintext HTTP/2 with prior knowledge (HTTP/1.1 is accepted
	// too, so an HTTP/1 client gets an answer).
	H2C Security = iota
	// TLS is TLS with ALPN h2 and a certificate from a generated CA.
	TLS
	// MTLS is TLS that also requires a client certificate signed by a
	// generated client CA.
	MTLS
)

// ServerOptions configure NewServerWith. The zero value is an h2c server.
type ServerOptions struct {
	Security Security
	// CA signs the server certificate; nil generates one.
	CA *tlstest.CA
	// ServerCert replaces the issued certificate (for localhost, 127.0.0.1
	// and ::1), e.g. for other names or an expired one.
	ServerCert *tlstest.Cert
	// ClientCA verifies client certificates with MTLS; nil generates one.
	ClientCA *tlstest.CA
	// ClientCAs, when set, replaces ClientCA's pool: e.g. the PKCS#12
	// fixtures' CAs (tlstest.FixtureClientCAs).
	ClientCAs *x509.CertPool
	// ClientAuth replaces tls.RequireAndVerifyClientCert with MTLS.
	ClientAuth tls.ClientAuthType
	// MaxVersion caps the TLS version (tls.VersionTLS12); zero is none.
	MaxVersion uint16
	// HTTP1Only serves HTTP/1.1 only: ALPN without h2 with TLS, no h2c
	// without (for gRPC-Web over HTTP/1.1).
	HTTP1Only bool
}

// NewServer starts a server with the echo methods registered and closes it
// when tb's test ends. withTLS picks TLS (ALPN h2) over h2c.
func NewServer(tb testing.TB, withTLS bool) *Server {
	tb.Helper()
	o := ServerOptions{}
	if withTLS {
		o.Security = TLS
	}
	return NewServerWith(tb, o)
}

// NewServerWith starts a server as NewServer does, secured as o says.
func NewServerWith(tb testing.TB, o ServerOptions) *Server {
	tb.Helper()
	withTLS := o.Security != H2C
	s := &Server{
		TLS:      withTLS,
		Security: o.Security,
		handlers: map[string]Handler{},
		raw:      map[string]http.HandlerFunc{},
	}
	s.handlers[MethodUnary] = echoUnary
	s.handlers[MethodServerStream] = echoServerStream
	s.handlers[MethodClientStream] = echoClientStream
	s.handlers[MethodBidi] = echoBidi
	s.handlers[MethodStatus] = statusHandler
	s.handlers[MethodSleep] = sleepHandler

	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serveHTTP))
	// Tests provoke handshake failures on purpose; keep the output clean.
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if withTLS {
		s.CA = o.CA
		if s.CA == nil {
			s.CA = tlstest.NewCA(tb, "grpctest server CA")
		}
		cert := o.ServerCert
		if cert == nil {
			cert = s.CA.Issue(tb, tlstest.CertOptions{})
		}
		cfg := &tls.Config{
			Certificates: []tls.Certificate{cert.TLS},
			MaxVersion:   o.MaxVersion,
		}
		if o.Security == MTLS {
			s.ClientCA = o.ClientCA
			if s.ClientCA == nil {
				s.ClientCA = tlstest.NewCA(tb, "grpctest client CA")
			}
			s.ClientCert = s.ClientCA.Issue(tb, tlstest.CertOptions{Client: true})
			cfg.ClientCAs = s.ClientCA.Pool()
			if o.ClientCAs != nil {
				cfg.ClientCAs = o.ClientCAs
			}
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
			if o.ClientAuth != tls.NoClientCert {
				cfg.ClientAuth = o.ClientAuth
			}
		}
		s.srv.TLS = cfg
		s.srv.EnableHTTP2 = !o.HTTP1Only
		s.srv.StartTLS()
		s.RootCAs = s.CA.Pool()
	} else {
		var p http.Protocols
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(!o.HTTP1Only)
		s.srv.Config.Protocols = &p
		s.srv.Start()
	}
	s.Addr = s.srv.Listener.Addr().String()
	tb.Cleanup(s.Close)
	return s
}

// PeerCertificates returns the client certificates of the latest request
// (nil without mTLS).
func (s *Server) PeerCertificates() []*x509.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil || s.last.TLS == nil {
		return nil
	}
	return s.last.TLS.PeerCertificates
}

// Close stops the server, cutting open connections.
func (s *Server) Close() {
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// Handle registers h for method ("/pkg.Service/Method").
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// HandleHTTP registers a raw HTTP handler for method, bypassing the gRPC
// layer (for protocol violations).
func (s *Server) HandleHTTP(method string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw[method] = h
}

// LastRequest returns the most recent request received (headers only).
func (s *Server) LastRequest() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.last = r.Clone(context.Background())
	raw := s.raw[r.URL.Path]
	h := s.handlers[r.URL.Path]
	s.mu.Unlock()

	if raw != nil {
		raw(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	web, text := transport.WebContentType(ct)
	if !web && ct != "application/grpc" && !strings.HasPrefix(ct, "application/grpc+") && !strings.HasPrefix(ct, "application/grpc;") {
		http.Error(w, "invalid gRPC request content-type", http.StatusUnsupportedMediaType)
		return
	}
	ss := newServerStream(w, r)
	if web {
		ss.webType = ct
		ss.webText = text
		// gRPC-Web is half-duplex, and an HTTP/1.1 handler cannot read the
		// request body once it has written the response: read it all now.
		body := io.Reader(r.Body)
		if text {
			body = transport.NewWebTextReader(body)
		}
		all, err := io.ReadAll(body)
		if err != nil {
			ss.finish(transport.Status{Code: transport.Internal, Message: "reading the gRPC-Web request: " + err.Error()})
			ss.cancel()
			return
		}
		ss.fr = transport.NewFrameReader(bytes.NewReader(all), -1)
	}
	defer ss.cancel()
	if h == nil {
		ss.finish(transport.Status{Code: transport.Unimplemented, Message: "unknown method " + r.URL.Path})
		return
	}
	if ss.timeoutErr != nil {
		ss.finish(transport.Status{Code: transport.Internal, Message: "malformed grpc-timeout: " + ss.timeoutErr.Error()})
		return
	}
	st := h(ss)
	if st.OK() && errors.Is(ss.ctx.Err(), context.DeadlineExceeded) {
		st = transport.Status{Code: transport.DeadlineExceeded, Message: "deadline exceeded"}
	}
	ss.finish(st)
}

// ServerStream is the server side of one call.
type ServerStream struct {
	// Request is the HTTP request; its body is read by Recv.
	Request *http.Request
	// Metadata holds the request headers as metadata ("-bin" decoded).
	Metadata transport.Metadata
	// Timeout is the parsed grpc-timeout; HasTimeout is false without one.
	Timeout    time.Duration
	HasTimeout bool

	timeoutErr error
	ctx        context.Context
	cancel     context.CancelFunc
	w          http.ResponseWriter
	rc         *http.ResponseController
	fr         *transport.FrameReader
	gzip       bool
	// sendGzip compresses response messages.
	sendGzip bool
	// webType is the gRPC-Web content type of the request (answered with
	// the same); "" for gRPC. webText is set for grpc-web-text.
	webType string
	webText bool

	headerSent bool
	header     transport.Metadata
	trailer    transport.Metadata
}

func newServerStream(w http.ResponseWriter, r *http.Request) *ServerStream {
	ss := &ServerStream{
		Request: r,
		w:       w,
		rc:      http.NewResponseController(w),
		fr:      transport.NewFrameReader(r.Body, -1),
		gzip:    strings.EqualFold(r.Header.Get("Grpc-Encoding"), "gzip"),
	}
	ss.sendGzip = ss.gzip
	switch strings.ToLower(r.Header.Get("X-Response-Encoding")) {
	case "gzip":
		ss.sendGzip = true
	case "identity":
		ss.sendGzip = false
	}
	for k, vv := range r.Header {
		key := strings.ToLower(k)
		for _, v := range vv {
			if transport.IsBinaryKey(key) {
				if b, err := transport.DecodeBinaryValue(v); err == nil {
					v = string(b)
				}
			}
			ss.Metadata = append(ss.Metadata, transport.MDEntry{Key: key, Value: v})
		}
	}
	ctx := r.Context()
	if v := r.Header.Get("Grpc-Timeout"); v != "" {
		d, err := transport.ParseTimeout(v)
		if err != nil {
			ss.timeoutErr = err
		} else {
			ss.Timeout, ss.HasTimeout = d, true
			ss.ctx, ss.cancel = context.WithTimeout(ctx, d)
			return ss
		}
	}
	ss.ctx, ss.cancel = context.WithCancel(ctx)
	return ss
}

// Context is done when the client cancels or the grpc-timeout passes.
func (ss *ServerStream) Context() context.Context { return ss.ctx }

// Get returns the first value of a request metadata key, or "".
func (ss *ServerStream) Get(key string) string {
	if vv := ss.Metadata.Get(key); len(vv) > 0 {
		return vv[0]
	}
	return ""
}

// Recv reads the next request message; io.EOF when the client is done.
func (ss *ServerStream) Recv() ([]byte, error) {
	if err := ss.ctx.Err(); err != nil {
		return nil, err
	}
	f, err := ss.fr.Next()
	if err != nil {
		return nil, err
	}
	if !f.Compressed {
		return f.Payload, nil
	}
	if !ss.gzip {
		return nil, errors.New("compressed message without grpc-encoding")
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.Payload))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// SetHeader adds response header metadata; it has no effect once headers
// were sent.
func (ss *ServerStream) SetHeader(md ...transport.MDEntry) { ss.header = append(ss.header, md...) }

// SetTrailer adds trailer metadata.
func (ss *ServerStream) SetTrailer(md ...transport.MDEntry) { ss.trailer = append(ss.trailer, md...) }

// SendHeader sends the response headers now.
func (ss *ServerStream) SendHeader() error {
	if ss.headerSent {
		return nil
	}
	ss.headerSent = true
	h := ss.w.Header()
	ss.setContentType(h)
	if ss.sendGzip {
		h.Set("Grpc-Encoding", "gzip")
	}
	writeMetadata(h, "", ss.header)
	ss.w.WriteHeader(http.StatusOK)
	return ss.rc.Flush()
}

func (ss *ServerStream) setContentType(h http.Header) {
	if ss.webType != "" {
		h.Set("Content-Type", ss.webType)
		return
	}
	h.Set("Content-Type", "application/grpc")
}

// Web reports whether the call is gRPC-Web, and whether it is the text
// (base64) variant.
func (ss *ServerStream) Web() (web, text bool) { return ss.webType != "", ss.webText }

// Send writes one message and flushes it; compressed with gzip when the
// response is (the request was, or x-response-encoding says so).
func (ss *ServerStream) Send(msg []byte) error {
	if ss.sendGzip {
		return ss.SendRaw(transport.AppendFrame(nil, true, Gzip(msg)))
	}
	return ss.SendRaw(transport.AppendFrame(nil, false, msg))
}

// SendRaw writes bytes to the response body as they are, after the
// headers, and flushes them. For grpc-web-text they are base64-encoded
// first, each write padded on its own.
func (ss *ServerStream) SendRaw(b []byte) error {
	if err := ss.SendHeader(); err != nil {
		return err
	}
	if ss.webText {
		b = []byte(base64.StdEncoding.EncodeToString(b))
	}
	if _, err := ss.w.Write(b); err != nil {
		return err
	}
	return ss.rc.Flush()
}

// finish writes the status: in trailers, or as a trailers-only response
// when no headers were sent. gRPC-Web puts the trailers in a trailer
// frame at the end of the body.
func (ss *ServerStream) finish(st transport.Status) {
	if ss.webType != "" && ss.headerSent {
		t := http.Header{}
		t.Set("Grpc-Status", strconv.FormatUint(uint64(st.Code), 10))
		if st.Message != "" {
			t.Set("Grpc-Message", transport.EncodeStatusMessage(st.Message))
		}
		if st.Details != nil {
			t.Set("Grpc-Status-Details-Bin", transport.EncodeBinaryValue(st.Details))
		}
		writeMetadata(t, "", ss.trailer)
		ss.SendRaw(transport.AppendTrailerFrame(nil, t))
		return
	}
	h := ss.w.Header()
	prefix := http.TrailerPrefix
	if !ss.headerSent {
		prefix = ""
		ss.setContentType(h)
		writeMetadata(h, "", ss.header)
	}
	h.Set(prefix+"Grpc-Status", strconv.FormatUint(uint64(st.Code), 10))
	if st.Message != "" {
		h.Set(prefix+"Grpc-Message", transport.EncodeStatusMessage(st.Message))
	}
	if st.Details != nil {
		h.Set(prefix+"Grpc-Status-Details-Bin", transport.EncodeBinaryValue(st.Details))
	}
	writeMetadata(h, prefix, ss.trailer)
	if !ss.headerSent {
		ss.headerSent = true
		ss.w.WriteHeader(http.StatusOK)
	}
}

func writeMetadata(h http.Header, prefix string, md transport.Metadata) {
	for _, e := range md {
		v := e.Value
		if transport.IsBinaryKey(e.Key) {
			v = transport.EncodeBinaryValue([]byte(v))
		}
		h.Add(prefix+e.Key, v)
	}
}

// echoMetadata copies the request's "x-" metadata into the response
// headers, and into the trailers with a "t-" prefix.
func echoMetadata(ss *ServerStream) {
	for _, e := range ss.Metadata {
		if strings.HasPrefix(e.Key, "x-") {
			ss.SetHeader(e)
			ss.SetTrailer(transport.MDEntry{Key: "t-" + e.Key, Value: e.Value})
		}
	}
}

func errStatus(err error) transport.Status {
	if errors.Is(err, context.DeadlineExceeded) {
		return transport.Status{Code: transport.DeadlineExceeded, Message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return transport.Status{Code: transport.Canceled, Message: err.Error()}
	}
	return transport.Status{Code: transport.Internal, Message: err.Error()}
}

func echoUnary(ss *ServerStream) transport.Status {
	echoMetadata(ss)
	msg, err := ss.Recv()
	if err != nil {
		return errStatus(err)
	}
	if _, err := ss.Recv(); err != io.EOF {
		return transport.Status{Code: transport.Internal, Message: "unary call with more than one message"}
	}
	if err := ss.Send(msg); err != nil {
		return errStatus(err)
	}
	return transport.Status{}
}

func echoServerStream(ss *ServerStream) transport.Status {
	echoMetadata(ss)
	msg, err := ss.Recv()
	if err != nil {
		return errStatus(err)
	}
	n := 3
	if v := ss.Get("x-count"); v != "" {
		if n, err = strconv.Atoi(v); err != nil {
			return transport.Status{Code: transport.InvalidArgument, Message: "bad x-count"}
		}
	}
	for i := range n {
		if err := ss.Send(append(append([]byte(nil), msg...), strconv.Itoa(i)...)); err != nil {
			return errStatus(err)
		}
	}
	return transport.Status{}
}

func echoClientStream(ss *ServerStream) transport.Status {
	echoMetadata(ss)
	var all []byte
	for {
		msg, err := ss.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errStatus(err)
		}
		all = append(all, msg...)
	}
	if err := ss.Send(all); err != nil {
		return errStatus(err)
	}
	return transport.Status{}
}

func echoBidi(ss *ServerStream) transport.Status {
	echoMetadata(ss)
	if err := ss.SendHeader(); err != nil {
		return errStatus(err)
	}
	for {
		msg, err := ss.Recv()
		if err == io.EOF {
			return transport.Status{}
		}
		if err != nil {
			return errStatus(err)
		}
		if err := ss.Send(msg); err != nil {
			return errStatus(err)
		}
	}
}

func statusHandler(ss *ServerStream) transport.Status {
	echoMetadata(ss)
	if ss.Get("x-trailers-only") == "" {
		for {
			msg, err := ss.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return errStatus(err)
			}
			if err := ss.Send(msg); err != nil {
				return errStatus(err)
			}
		}
	}
	code, _ := strconv.ParseUint(ss.Get("x-status-code"), 10, 32)
	st := transport.Status{Code: transport.Code(code), Message: ss.Get("x-status-message")}
	if d := ss.Metadata.Get("x-status-details-bin"); len(d) > 0 {
		st.Details = []byte(d[0])
	}
	return st
}

func sleepHandler(ss *ServerStream) transport.Status {
	var wait <-chan time.Time
	if v := ss.Get("x-sleep"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return transport.Status{Code: transport.InvalidArgument, Message: "bad x-sleep"}
		}
		wait = time.After(d)
	}
	select {
	case <-wait:
		return transport.Status{}
	case <-ss.ctx.Done():
		return errStatus(ss.ctx.Err())
	}
}

// Misbehaving handlers for HandleHTTP.

// HTTPStatus answers with a plain HTTP status and no gRPC headers.
func HTTPStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, http.StatusText(code), code)
	}
}

// BadContentType answers 200 with a text/plain body.
func BadContentType() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "not grpc")
	}
}

// Frames answers with valid headers, writes body in chunks of chunk bytes
// (each flushed, so it arrives as separate DATA frames), then the given
// trailers. A nil trailer map sends no trailers at all.
func Frames(body []byte, chunk int, header, trailer map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		h := w.Header()
		h.Set("Content-Type", "application/grpc")
		for k, v := range header {
			h.Set(k, v)
		}
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		rc.Flush()
		if chunk <= 0 {
			chunk = len(body)
		}
		for b := body; len(b) > 0; {
			n := min(chunk, len(b))
			w.Write(b[:n])
			rc.Flush()
			b = b[n:]
		}
		for k, v := range trailer {
			h.Set(http.TrailerPrefix+k, v)
		}
	}
}

// FrameHeader returns a 5-byte message prefix with an arbitrary flag and
// length, for building malformed bodies.
func FrameHeader(flag byte, length uint32) []byte {
	b := []byte{flag}
	return binary.BigEndian.AppendUint32(b, length)
}

// Gzip compresses b.
func Gzip(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// TrailersOK are trailers with an OK status.
func TrailersOK() map[string]string { return map[string]string{"Grpc-Status": "0"} }
