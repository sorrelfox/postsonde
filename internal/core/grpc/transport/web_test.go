package transport_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
)

// webServer is a gRPC-Web test setup: the server, and the HTTP version a
// gRPC-Web call to it uses.
type webServer struct {
	name  string
	opts  grpctest.ServerOptions
	major int
}

var webServers = []webServer{
	{"plaintext http1", grpctest.ServerOptions{}, 1},
	{"tls h2", grpctest.ServerOptions{Security: grpctest.TLS}, 2},
	{"tls http1", grpctest.ServerOptions{Security: grpctest.TLS, HTTP1Only: true}, 1},
}

var webProtocols = []transport.Protocol{transport.ProtocolGRPCWeb, transport.ProtocolGRPCWebText}

// webModes runs fn for both gRPC-Web framings against every web server.
func webModes(t *testing.T, fn func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, major int)) {
	for _, ws := range webServers {
		for _, p := range webProtocols {
			t.Run(ws.name+"/"+string(p), func(t *testing.T) {
				t.Parallel()
				srv := grpctest.NewServerWith(t, ws.opts)
				fn(t, srv, newClient(t, srv, transport.Options{}), p, ws.major)
			})
		}
	}
}

func webCall(srv *grpctest.Server, p transport.Protocol, method string, md ...transport.MDEntry) transport.CallOptions {
	o := call(srv, method, md...)
	o.Protocol = p
	return o
}

func TestWebUnary(t *testing.T) {
	t.Parallel()
	webModes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, major int) {
		o := webCall(srv, p, grpctest.MethodUnary, md("x-id", "42"), md("x-blob-bin", "\x00\xff\x01"))
		o.UserAgent = "postsonde-test/1"
		res, err := c.Unary(context.Background(), o, []byte("hello web"))
		if err != nil {
			t.Fatalf("Unary: %v", err)
		}
		if string(res.Message) != "hello web" || !res.Status.OK() {
			t.Fatalf("result = %q, %+v", res.Message, res.Status)
		}
		if got := res.Header.Get("x-id"); !slices.Equal(got, []string{"42"}) {
			t.Errorf("header x-id = %q", got)
		}
		if got := res.Trailer.Get("t-x-blob-bin"); !slices.Equal(got, []string{"\x00\xff\x01"}) {
			t.Errorf("trailer t-x-blob-bin = %q", got)
		}
		if got := res.Trailer.Get("grpc-status"); !slices.Equal(got, []string{"0"}) {
			t.Errorf("trailer grpc-status = %q", got)
		}

		r := srv.LastRequest()
		ct, accept := "application/grpc-web+proto", "application/grpc-web"
		if p == transport.ProtocolGRPCWebText {
			ct, accept = "application/grpc-web-text+proto", "application/grpc-web-text"
		}
		for k, v := range map[string]string{"Content-Type": ct, "Accept": accept, "X-Grpc-Web": "1", "User-Agent": "postsonde-test/1", "X-Blob-Bin": "AP8B"} {
			if got := r.Header.Get(k); got != v {
				t.Errorf("request %s = %q, want %q", k, got, v)
			}
		}
		if _, ok := r.Header["Te"]; ok {
			t.Errorf("gRPC-Web request sent te")
		}
		if r.ProtoMajor != major || r.Method != http.MethodPost || r.URL.Path != grpctest.MethodUnary {
			t.Errorf("request = %s %s %s, want HTTP/%d", r.Proto, r.Method, r.URL.Path, major)
		}
	})
}

func TestWebServerStream(t *testing.T) {
	t.Parallel()
	webModes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, _ int) {
		s, err := c.Invoke(context.Background(), webCall(srv, p, grpctest.MethodServerStream, md("x-count", "5")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Send([]byte("msg")); err != nil {
			t.Fatal(err)
		}
		if err := s.CloseSend(); err != nil {
			t.Fatal(err)
		}
		h, err := s.Header()
		if err != nil || !slices.Equal(h.Get("x-count"), []string{"5"}) {
			t.Fatalf("headers %v, %v", h, err)
		}
		for i := range 5 {
			m, err := s.Recv()
			if err != nil || string(m) != "msg"+strconv.Itoa(i) {
				t.Fatalf("message %d = %q, %v", i, m, err)
			}
		}
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("end = %v", err)
		}
		if st, ok := s.Status(); !ok || !st.OK() {
			t.Fatalf("status %+v %v", st, ok)
		}
		if got := s.Trailer().Get("t-x-count"); !slices.Equal(got, []string{"5"}) {
			t.Errorf("trailer t-x-count = %q", got)
		}
	})
}

func TestWebStatusErrors(t *testing.T) {
	t.Parallel()
	webModes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, _ int) {
		for _, trailersOnly := range []bool{false, true} {
			rows := []transport.MDEntry{md("x-status-code", "5"), md("x-status-message", "no order 7 - 100% ok"), md("x-status-details-bin", "\x08\x05")}
			if trailersOnly {
				rows = append(rows, md("x-trailers-only", "1"))
			}
			res, err := c.Unary(context.Background(), webCall(srv, p, grpctest.MethodStatus, rows...), []byte("echoed"))
			st := wantStatus(t, err, transport.NotFound, transport.CodeStatus)
			if st.Message != "no order 7 - 100% ok" || !bytes.Equal(st.Details, []byte("\x08\x05")) {
				t.Errorf("trailers-only %v: status %+v", trailersOnly, st)
			}
			if got := res.Trailer.Get("t-x-status-code"); !slices.Equal(got, []string{"5"}) {
				t.Errorf("trailers-only %v: trailer %v", trailersOnly, res.Trailer)
			}
			if e, _ := errors.AsType[*errs.Error](err); e.Kind != errs.Protocol {
				t.Errorf("kind %v", e.Kind)
			}
		}
	})
}

func TestWebClientStreamIsHalfDuplex(t *testing.T) {
	t.Parallel()
	// Several messages are sent together at CloseSend; the transport does
	// not refuse them (servers behind a proxy may accept them).
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	s, err := c.Invoke(context.Background(), webCall(srv, transport.ProtocolGRPCWeb, grpctest.MethodClientStream))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, m := range []string{"a", "b", "c"} {
		if err := s.Send([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	s.CloseSend()
	if m, err := s.Recv(); err != nil || string(m) != "abc" {
		t.Fatalf("Recv = %q, %v", m, err)
	}
	if err := s.Send([]byte("late")); err == nil {
		t.Fatal("Send after CloseSend succeeded")
	}
}

func TestWebCancelBeforeSend(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	s, err := c.Invoke(context.Background(), webCall(srv, transport.ProtocolGRPCWeb, grpctest.MethodUnary))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err = s.Recv()
	wantStatus(t, err, transport.Canceled, transport.CodeCancelled)
	if _, err := s.Header(); err == nil {
		t.Fatal("Header after cancel succeeded")
	}
	if srv.LastRequest() != nil {
		t.Fatal("a cancelled call reached the server")
	}
}

func TestWebDeadline(t *testing.T) {
	t.Parallel()
	webModes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, _ int) {
		o := webCall(srv, p, grpctest.MethodSleep)
		o.Timeout = time.Second
		_, err := c.Unary(context.Background(), o, nil)
		// The server answers DEADLINE_EXCEEDED at its deadline, or the
		// client gives up first, on a slow machine even before the call
		// reaches the server; either way the status is the same.
		if st := transport.StatusFromError(err); st.Code != transport.DeadlineExceeded {
			t.Fatalf("status %v (%v)", st.Code, err)
		}
		// The deadline travels as grpc-timeout: a call that is sure to
		// reach the server shows it.
		o = webCall(srv, p, grpctest.MethodUnary)
		o.Timeout = time.Minute
		if _, err := c.Unary(context.Background(), o, nil); err != nil {
			t.Fatal(err)
		}
		if got := srv.LastRequest().Header.Get("Grpc-Timeout"); got == "" {
			t.Error("no grpc-timeout sent")
		}
	})
}

func TestWebGzip(t *testing.T) {
	t.Parallel()
	webModes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client, p transport.Protocol, _ int) {
		big := bytes.Repeat([]byte("squash"), 1000)
		// Compressed both ways: the server answers like the request.
		o := webCall(srv, p, grpctest.MethodUnary)
		o.Compression = transport.CompressionGzip
		res, err := c.Unary(context.Background(), o, big)
		if err != nil || !bytes.Equal(res.Message, big) {
			t.Fatalf("gzip call: %d bytes, %v", len(res.Message), err)
		}
		if got := res.Header.Get("grpc-encoding"); !slices.Equal(got, []string{"gzip"}) {
			t.Errorf("response grpc-encoding = %q", got)
		}
		r := srv.LastRequest()
		if r.Header.Get("Grpc-Encoding") != "gzip" || r.Header.Get("Grpc-Accept-Encoding") != "gzip" {
			t.Errorf("request headers %v", r.Header)
		}
		// A compressed response to an uncompressed request is accepted.
		o = webCall(srv, p, grpctest.MethodServerStream, md("x-response-encoding", "gzip"), md("x-count", "2"))
		s, err := c.Invoke(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.Send([]byte("z"))
		s.CloseSend()
		for i := range 2 {
			if m, err := s.Recv(); err != nil || string(m) != "z"+strconv.Itoa(i) {
				t.Fatalf("message %d = %q, %v", i, m, err)
			}
		}
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("end: %v", err)
		}
	})
}

// TestGzipBothDirections checks native gRPC calls: the request's frames
// carry the compressed flag, and a gzip response is decompressed.
func TestGzipBothDirections(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		big := bytes.Repeat([]byte("squash"), 1000)
		o := call(srv, grpctest.MethodServerStream, md("x-count", "2"))
		o.Compression = transport.CompressionGzip
		s, err := c.Invoke(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.Send(big)
		s.CloseSend()
		h, err := s.Header()
		if err != nil || !slices.Equal(h.Get("grpc-encoding"), []string{"gzip"}) {
			t.Fatalf("headers %v, %v", h, err)
		}
		for i := range 2 {
			if m, err := s.Recv(); err != nil || !bytes.Equal(m, append(slices.Clone(big), strconv.Itoa(i)...)) {
				t.Fatalf("message %d: %d bytes, %v", i, len(m), err)
			}
		}
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("end: %v", err)
		}

		// The wire: a compressed flag and a gzip payload.
		const raw = "/grpctest.Raw/Inspect"
		flags := make(chan byte, 1)
		srv.HandleHTTP(raw, func(w http.ResponseWriter, r *http.Request) {
			f, err := transport.NewFrameReader(r.Body, -1).Next()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			flag := byte(0)
			if f.Compressed && bytes.HasPrefix(f.Payload, []byte{0x1f, 0x8b}) {
				flag = 1
			}
			flags <- flag
			grpctest.Frames(transport.AppendFrame(nil, true, grpctest.Gzip([]byte("zipped"))), 0, map[string]string{"Grpc-Encoding": "gzip"}, grpctest.TrailersOK())(w, r)
		})
		o = call(srv, raw)
		o.Compression = transport.CompressionGzip
		res, err := c.Unary(context.Background(), o, big)
		if err != nil || string(res.Message) != "zipped" {
			t.Fatalf("raw call: %q, %v", res.Message, err)
		}
		if <-flags != 1 {
			t.Fatal("the request message was not gzip-compressed")
		}

		// Accepting compression without compressing the request.
		o = call(srv, grpctest.MethodUnary, md("x-response-encoding", "gzip"))
		res, err = c.Unary(context.Background(), o, []byte("plain request"))
		if err != nil || string(res.Message) != "plain request" || !slices.Equal(res.Header.Get("grpc-encoding"), []string{"gzip"}) {
			t.Fatalf("gzip response: %q, %v, %v", res.Message, res.Header, err)
		}
		if r := srv.LastRequest(); r.Header.Get("Grpc-Encoding") != "" || r.Header.Get("Grpc-Accept-Encoding") != "" {
			t.Errorf("uncompressed call sent %v", r.Header)
		}
	})
}

func TestUnsupportedEncoding(t *testing.T) {
	t.Parallel()
	body := transport.AppendFrame(nil, true, []byte("zstd bytes"))
	for _, p := range []transport.Protocol{transport.ProtocolGRPC, transport.ProtocolGRPCWeb} {
		srv := grpctest.NewServer(t, false)
		c := newClient(t, srv, transport.Options{})
		h := grpctest.Frames(body, 0, map[string]string{"Grpc-Encoding": "zstd"}, grpctest.TrailersOK())
		if p.Web() {
			h = webFrames(body, "zstd", false)
		}
		srv.HandleHTTP(grpctest.MethodUnary, h)
		_, err := c.Unary(context.Background(), webCall(srv, p, grpctest.MethodUnary), []byte("x"))
		wantStatus(t, err, transport.Unimplemented, transport.CodeUnsupportedEncoding)
		e, _ := errors.AsType[*errs.Error](err)
		if e.Details["encoding"] != "zstd" || e.Hint == "" {
			t.Errorf("%s: error %+v", p, e)
		}
	}
}

// webFrames answers a gRPC-Web call with body (and nothing else: no
// trailer frame unless body has one), base64 for text.
func webFrames(body []byte, encoding string, text bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		ct := "application/grpc-web+proto"
		if text {
			ct = "application/grpc-web-text+proto"
			body = []byte(base64.StdEncoding.EncodeToString(body))
		}
		w.Header().Set("Content-Type", ct)
		if encoding != "" {
			w.Header().Set("Grpc-Encoding", encoding)
		}
		w.Write(body)
	}
}

func TestWebMalformedResponses(t *testing.T) {
	t.Parallel()
	msg := transport.AppendFrame(nil, false, []byte("m"))
	okTrailer := func() http.Header { h := http.Header{}; h.Set("Grpc-Status", "0"); return h }
	tests := []struct {
		name    string
		handler http.HandlerFunc
		code    transport.Code
		errCode string
	}{
		{"gRPC content type", grpctest.Frames(msg, 0, nil, grpctest.TrailersOK()), transport.Unknown, transport.CodeNotGRPC},
		{"text/plain", grpctest.BadContentType(), transport.Unknown, transport.CodeNotGRPC},
		{"no trailer frame", webFrames(msg, "", false), transport.Internal, transport.CodeMissingStatus},
		{"trailer frame without status", webFrames(append(slices.Clone(msg), transport.AppendTrailerFrame(nil, http.Header{"X-A": {"1"}})...), "", false), transport.Unknown, transport.CodeMissingStatus},
		{"malformed trailer frame", webFrames(append(slices.Clone(msg), 0x80, 0, 0, 0, 3, 'b', 'a', 'd'), "", false), transport.Internal, transport.CodeMalformedFrame},
		{"invalid base64", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/grpc-web-text")
			w.Write([]byte("AAAA!!!!"))
		}, transport.Internal, transport.CodeMalformedFrame},
		{"truncated", webFrames(msg[:4], "", true), transport.Internal, transport.CodeMalformedFrame},
		{"http 404", grpctest.HTTPStatus(http.StatusNotFound), transport.Unimplemented, transport.CodeHTTPStatus},
		{"ok with messages after the trailer frame", webFrames(slices.Concat(msg, transport.AppendTrailerFrame(nil, okTrailer()), msg), "", false), transport.OK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := grpctest.NewServer(t, false)
			c := newClient(t, srv, transport.Options{})
			srv.HandleHTTP(grpctest.MethodUnary, tt.handler)
			res, err := c.Unary(context.Background(), webCall(srv, transport.ProtocolGRPCWeb, grpctest.MethodUnary), []byte("x"))
			if tt.code == transport.OK {
				if err != nil || string(res.Message) != "m" {
					t.Fatalf("%q, %v", res.Message, err)
				}
				return
			}
			wantStatus(t, err, tt.code, tt.errCode)
			if tt.errCode == transport.CodeNotGRPC {
				if e, _ := errors.AsType[*errs.Error](err); e.Hint != transport.HintWeb {
					t.Errorf("hint %q", e.Hint)
				}
			}
		})
	}
}

func TestInvalidProtocol(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	_, err := c.Invoke(context.Background(), webCall(srv, "grpc-websocket", grpctest.MethodUnary))
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != transport.CodeInvalidOption || e.Details["field"] != "protocol" {
		t.Fatalf("error %v", err)
	}
}
