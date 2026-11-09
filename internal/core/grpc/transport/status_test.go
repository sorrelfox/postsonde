package transport_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

func TestStatusInTrailers(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodStatus,
			md("x-status-code", "5"),
			md("x-status-message", "placeholder"),
			md("x-status-details-bin", "\x08\x05\x12\x03abc"),
			md("x-extra", "kept")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.Send([]byte("one"))
		s.CloseSend()
		m, err := s.Recv()
		if err != nil || string(m) != "one" {
			t.Fatalf("Recv = %q, %v", m, err)
		}
		_, err = s.Recv()
		st := wantStatus(t, err, transport.NotFound, transport.CodeStatus)
		if st.Message != "placeholder" || !bytes.Equal(st.Details, []byte("\x08\x05\x12\x03abc")) {
			t.Fatalf("status = %+v", st)
		}
		if got, ok := s.Status(); !ok || got.Code != transport.NotFound {
			t.Fatalf("Status() = %+v, %v", got, ok)
		}
		if errs.KindOf(err) != errs.Protocol {
			t.Fatalf("kind = %v", errs.KindOf(err))
		}
		if got := s.Trailer().Get("t-x-extra"); !slices.Equal(got, []string{"kept"}) {
			t.Fatalf("trailer t-x-extra = %q (trailers %q)", got, s.Trailer())
		}
		if _, err2 := s.Recv(); err2 != err {
			t.Fatalf("second Recv = %v, want the same error", err2)
		}
	})
}

func TestStatusMessagePercentEncoding(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	msg := "bad: 100% ✓\nnext"
	srv.Handle("/test.S/Fail", func(ss *grpctest.ServerStream) transport.Status {
		return transport.Status{Code: transport.InvalidArgument, Message: msg}
	})
	srv.HandleHTTP("/test.S/Raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", "3")
		w.Header().Set("Grpc-Message", "caf%C3%A9 %zz %")
	})
	c := newClient(t, srv, transport.Options{})
	res, err := c.Unary(context.Background(), call(srv, "/test.S/Fail"), nil)
	st := wantStatus(t, err, transport.InvalidArgument, transport.CodeStatus)
	if st.Message != msg || res.Status.Message != msg {
		t.Fatalf("message = %q, want %q", st.Message, msg)
	}
	if got := res.Trailer.Get("grpc-message"); len(got) != 1 || got[0] != "bad: 100%25 %E2%9C%93%0Anext" {
		t.Fatalf("raw grpc-message trailer = %q", got)
	}
	_, err = c.Unary(context.Background(), call(srv, "/test.S/Raw"), nil)
	st = wantStatus(t, err, transport.InvalidArgument, transport.CodeStatus)
	if st.Message != "café %zz %" {
		t.Fatalf("lenient decode = %q", st.Message)
	}
}

func TestTrailersOnly(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodStatus,
			md("x-trailers-only", "1"), md("x-status-code", "7"), md("x-status-message", "denied")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.CloseSend()
		h, err := s.Header()
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Get("grpc-status"); !slices.Equal(got, []string{"7"}) {
			t.Fatalf("headers of a trailers-only response = %q", h)
		}
		_, err = s.Recv()
		st := wantStatus(t, err, transport.PermissionDenied, transport.CodeStatus)
		if st.Message != "denied" {
			t.Fatalf("message = %q", st.Message)
		}
		if got := s.Trailer().Get("grpc-message"); !slices.Equal(got, []string{"denied"}) {
			t.Fatalf("trailers = %q", s.Trailer())
		}
	})
}

func TestUnimplementedMethod(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	res, err := c.Unary(context.Background(), call(srv, "/no.Such/Method"), []byte("x"))
	wantStatus(t, err, transport.Unimplemented, transport.CodeStatus)
	if res == nil || res.Status.Code != transport.Unimplemented {
		t.Fatalf("result = %+v", res)
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	for status, code := range map[int]transport.Code{
		http.StatusNotFound:            transport.Unimplemented,
		http.StatusServiceUnavailable:  transport.Unavailable,
		http.StatusUnauthorized:        transport.Unauthenticated,
		http.StatusInternalServerError: transport.Unknown,
	} {
		method := "/http.Status/S" + strconv.Itoa(status)
		srv.HandleHTTP(method, grpctest.HTTPStatus(status))
		res, err := c.Unary(context.Background(), call(srv, method), nil)
		wantStatus(t, err, code, transport.CodeHTTPStatus)
		e, _ := errors.AsType[*errs.Error](err)
		if e.Details["http_status"] != strconv.Itoa(status) {
			t.Errorf("%d: details = %v", status, e.Details)
		}
		if res == nil || len(res.Header.Get("content-type")) == 0 {
			t.Errorf("%d: response headers not kept: %+v", status, res)
		}
	}
	if errs.KindOf(func() error {
		_, err := c.Unary(context.Background(), call(srv, "/http.Status/S503"), nil)
		return err
	}()) != errs.Network {
		t.Error("UNAVAILABLE is not kind network")
	}
}

func TestProtocolViolations(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	frame := func(s string) []byte { return transport.AppendFrame(nil, false, []byte(s)) }
	gz := func(s string) []byte { return transport.AppendFrame(nil, true, grpctest.Gzip([]byte(s))) }
	tests := []struct {
		name    string
		handler http.HandlerFunc
		max     int
		code    transport.Code
		errCode string
		msgs    []string
	}{
		{"bad content-type", grpctest.BadContentType(), 0, transport.Unknown, transport.CodeNotGRPC, nil},
		{"missing trailers", grpctest.Frames(frame("a"), 0, nil, nil), 0, transport.Internal, transport.CodeMissingStatus, []string{"a"}},
		{"trailers without status", grpctest.Frames(frame("a"), 0, nil, map[string]string{"X-Other": "1"}), 0, transport.Unknown, transport.CodeMissingStatus, []string{"a"}},
		{"oversize frame", grpctest.Frames(append(grpctest.FrameHeader(0, 1<<20), make([]byte, 1<<20)...), 0, nil, grpctest.TrailersOK()), 1000, transport.ResourceExhausted, transport.CodeRecvTooLarge, nil},
		{"default limit", grpctest.Frames(grpctest.FrameHeader(0, 5<<20), 0, nil, grpctest.TrailersOK()), 0, transport.ResourceExhausted, transport.CodeRecvTooLarge, nil},
		{"truncated frame", grpctest.Frames(append(grpctest.FrameHeader(0, 10), "abc"...), 0, nil, grpctest.TrailersOK()), 0, transport.Internal, transport.CodeMalformedFrame, nil},
		{"invalid flag", grpctest.Frames(append(grpctest.FrameHeader(4, 1), 'x'), 0, nil, grpctest.TrailersOK()), 0, transport.Internal, transport.CodeMalformedFrame, nil},
		{"compressed without encoding", grpctest.Frames(gz("zipped"), 0, nil, grpctest.TrailersOK()), 0, transport.Internal, transport.CodeCompression, nil},
		{"unknown encoding", grpctest.Frames(gz("zipped"), 0, map[string]string{"Grpc-Encoding": "snappy"}, grpctest.TrailersOK()), 0, transport.Unimplemented, transport.CodeUnsupportedEncoding, nil},
		{"corrupt gzip", grpctest.Frames(transport.AppendFrame(nil, true, []byte("not gzip")), 0, map[string]string{"Grpc-Encoding": "gzip"}, grpctest.TrailersOK()), 0, transport.Internal, transport.CodeCompression, nil},
		{"gzip bomb over limit", grpctest.Frames(gz(string(make([]byte, 5000))), 0, map[string]string{"Grpc-Encoding": "gzip"}, grpctest.TrailersOK()), 1000, transport.ResourceExhausted, transport.CodeRecvTooLarge, nil},
		{"malformed grpc-status", grpctest.Frames(nil, 0, nil, map[string]string{"Grpc-Status": "oops"}), 0, transport.Internal, transport.CodeStatus, nil},
	}
	for i, tt := range tests {
		method := "/bad.S/M" + strconv.Itoa(i)
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv.HandleHTTP(method, tt.handler)
			c := newClient(t, srv, transport.Options{MaxRecvMessageSize: tt.max})
			s, err := c.Invoke(context.Background(), call(srv, method))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.CloseSend()
			var got []string
			for {
				m, err := s.Recv()
				if err != nil {
					wantStatus(t, err, tt.code, tt.errCode)
					break
				}
				got = append(got, string(m))
			}
			if !slices.Equal(got, tt.msgs) {
				t.Fatalf("messages = %q, want %q", got, tt.msgs)
			}
		})
	}
}

func TestFramesSplitAcrossReads(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	build := func(msgs ...string) []byte {
		var body []byte
		for _, m := range msgs {
			body = transport.AppendFrame(body, false, []byte(m))
		}
		// A compressed message last.
		return transport.AppendFrame(body, true, grpctest.Gzip([]byte("zipped")))
	}
	small := []string{"", "first", "a somewhat longer second message", "last"}
	big := []string{"", string(bytes.Repeat([]byte("big"), 30_000)), "last"}
	gzipHeader := map[string]string{"Grpc-Encoding": "gzip"}
	srv.HandleHTTP("/split.S/Three", grpctest.Frames(build(small...), 3, gzipHeader, grpctest.TrailersOK()))
	srv.HandleHTTP("/split.S/Big", grpctest.Frames(build(big...), 7777, gzipHeader, grpctest.TrailersOK()))
	c := newClient(t, srv, transport.Options{})
	for method, want := range map[string][]string{
		"/split.S/Three": append(small, "zipped"),
		"/split.S/Big":   append(big, "zipped"),
	} {
		s, err := c.Invoke(context.Background(), call(srv, method))
		if err != nil {
			t.Fatal(err)
		}
		s.CloseSend()
		var got []string
		for {
			m, err := s.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			got = append(got, string(m))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %d messages, want %d", method, len(got), len(want))
		}
		s.Close()
	}
}

func TestMaxRecvMessageSize(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{MaxRecvMessageSize: 100})
	o := call(srv, grpctest.MethodUnary)
	if _, err := c.Unary(context.Background(), o, make([]byte, 100)); err != nil {
		t.Fatalf("message at the limit: %v", err)
	}
	_, err := c.Unary(context.Background(), o, make([]byte, 101))
	st := wantStatus(t, err, transport.ResourceExhausted, transport.CodeRecvTooLarge)
	if st.Message == "" {
		t.Fatal("empty status message")
	}
	o.MaxRecvMessageSize = -1
	if _, err := c.Unary(context.Background(), o, make([]byte, 5<<20)); err != nil {
		t.Fatalf("no limit per call: %v", err)
	}
}

func TestGzipCompression(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	o := call(srv, grpctest.MethodClientStream)
	o.Compression = transport.CompressionGzip
	res, err := c.Unary(context.Background(), o, bytes.Repeat([]byte("squash"), 1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Message) != 6000 {
		t.Fatalf("echo of %d bytes", len(res.Message))
	}
	r := srv.LastRequest()
	if r.Header.Get("Grpc-Encoding") != "gzip" || r.Header.Get("Grpc-Accept-Encoding") != "gzip" {
		t.Fatalf("request headers = %v", r.Header)
	}
}

func TestDeadlineExceeded(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		// The handler ignores grpc-timeout and waits for the client to reset
		// the stream, so the client-side deadline is what ends the call.
		srv.Handle("/slow.S/M", func(ss *grpctest.ServerStream) transport.Status {
			<-ss.Request.Context().Done()
			return transport.Status{Code: transport.Unknown, Message: "unreachable"}
		})
		o := call(srv, "/slow.S/M")
		o.Timeout = 150 * time.Millisecond
		start := time.Now()
		_, err := c.Unary(context.Background(), o, []byte("x"))
		wantStatus(t, err, transport.DeadlineExceeded, transport.CodeTimeout)
		if errs.KindOf(err) != errs.Timeout {
			t.Fatalf("kind = %v", errs.KindOf(err))
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("took %v", elapsed)
		}
		// On a slow machine the client can give up before the call above
		// reaches the server, so the grpc-timeout header is checked on a
		// call that is sure to arrive.
		o = call(srv, grpctest.MethodUnary)
		o.Timeout = time.Minute
		if _, err := c.Unary(context.Background(), o, []byte("x")); err != nil {
			t.Fatal(err)
		}
		r := srv.LastRequest()
		d, perr := transport.ParseTimeout(r.Header.Get("Grpc-Timeout"))
		if perr != nil || d <= 0 || d > time.Minute {
			t.Fatalf("grpc-timeout %q = %v, %v", r.Header.Get("Grpc-Timeout"), d, perr)
		}
	})
}

func TestServerHonoursDeadline(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	o := call(srv, grpctest.MethodSleep, md("x-sleep", "1ms"))
	o.Timeout = time.Minute
	if _, err := c.Unary(context.Background(), o, nil); transport.StatusFromError(err).Code != transport.Internal {
		// MethodSleep sends no message: a unary call sees a cardinality error.
		t.Fatalf("short sleep: %v", err)
	}
	// Without x-sleep the handler waits for its grpc-timeout deadline; the
	// status comes from the server or the client, whichever is first.
	o = call(srv, grpctest.MethodSleep)
	o.Timeout = 50 * time.Millisecond
	if _, err := c.Unary(context.Background(), o, nil); transport.StatusFromError(err).Code != transport.DeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}
}

func TestContextDeadlineSent(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	o := call(srv, grpctest.MethodUnary)
	o.Timeout = 2 * time.Hour // the context's earlier deadline wins
	if _, err := c.Unary(ctx, o, nil); err != nil {
		t.Fatal(err)
	}
	d, err := transport.ParseTimeout(srv.LastRequest().Header.Get("Grpc-Timeout"))
	if err != nil || d > time.Hour || d < 59*time.Minute {
		t.Fatalf("grpc-timeout = %v, %v", d, err)
	}

	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	_, err = c.Invoke(expired, o)
	wantStatus(t, err, transport.DeadlineExceeded, transport.CodeTimeout)
}

func TestCancelMidStream(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		serverDone := make(chan error, 1)
		srv.Handle("/cancel.S/M", func(ss *grpctest.ServerStream) transport.Status {
			ss.Send([]byte("first"))
			_, err := ss.Recv() // blocks until the client goes away
			serverDone <- err
			return transport.Status{}
		})
		ctx, cancel := context.WithCancel(context.Background())
		s, err := c.Invoke(ctx, call(srv, "/cancel.S/M"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if m, err := s.Recv(); err != nil || string(m) != "first" {
			t.Fatalf("Recv = %q, %v", m, err)
		}
		recvErr := make(chan error, 1)
		go func() {
			_, err := s.Recv()
			recvErr <- err
		}()
		time.Sleep(20 * time.Millisecond)
		cancel()
		select {
		case err := <-recvErr:
			wantStatus(t, err, transport.Canceled, transport.CodeCancelled)
			if errs.KindOf(err) != errs.Cancelled || !errors.Is(err, context.Canceled) {
				t.Fatalf("kind = %v, errors.Is(Canceled) = %v", errs.KindOf(err), errors.Is(err, context.Canceled))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Recv did not return after cancel")
		}
		if err := s.Send([]byte("late")); err != io.EOF {
			t.Fatalf("Send after cancel = %v, want io.EOF", err)
		}
		select {
		case err := <-serverDone:
			if err == nil {
				t.Fatal("server Recv succeeded after cancel")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server did not see the reset")
		}
	})
}

func TestCloseUnblocksSend(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	release := make(chan struct{})
	srv.Handle("/block.S/M", func(ss *grpctest.ServerStream) transport.Status {
		select { // never reads: flow control fills up
		case <-release:
		case <-ss.Context().Done():
		}
		return transport.Status{}
	})
	defer close(release)
	c := newClient(t, srv, transport.Options{})
	s, err := c.Invoke(context.Background(), call(srv, "/block.S/M"))
	if err != nil {
		t.Fatal(err)
	}
	sendErr := make(chan error, 1)
	go func() {
		big := make([]byte, 1<<20)
		for {
			if err := s.Send(big); err != nil {
				sendErr <- err
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	s.Close()
	select {
	case err := <-sendErr:
		if err != io.EOF {
			t.Fatalf("Send = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send still blocked after Close")
	}
	_, err = s.Recv()
	wantStatus(t, err, transport.Canceled, transport.CodeCancelled)
}

func TestUnaryCardinality(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	_, err := c.Unary(context.Background(), call(srv, grpctest.MethodStatus, md("x-trailers-only", "1")), nil) // no messages, OK
	wantStatus(t, err, transport.Internal, transport.CodeCardinality)
	o := call(srv, grpctest.MethodServerStream, md("x-count", "2"))
	res, err := c.Unary(context.Background(), o, []byte("m"))
	wantStatus(t, err, transport.Internal, transport.CodeCardinality)
	if string(res.Message) != "m0" {
		t.Fatalf("first message = %q", res.Message)
	}
}

func TestConnectionErrors(t *testing.T) {
	t.Parallel()
	// A closed port: the dial error keeps its netx code and carries
	// UNAVAILABLE.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{})
	s, err := c.Invoke(context.Background(), transport.CallOptions{Address: addr, Method: "/a.B/C"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]byte("x")); err != nil && err != io.EOF {
		t.Fatalf("Send = %v", err)
	}
	_, err = s.Recv()
	if st := transport.StatusFromError(err); st.Code != transport.Unavailable {
		t.Fatalf("status = %+v (%v)", st, err)
	}
	e, _ := errors.AsType[*errs.Error](err)
	if e == nil || e.Kind != errs.Network || len(e.Code) < 5 || e.Code[:5] != "netx." {
		t.Fatalf("error = %#v", e)
	}
	if _, herr := s.Header(); herr != err {
		t.Fatalf("Header() error = %v, want %v", herr, err)
	}

	// TLS to a server whose CA is not trusted.
	tlsSrv := grpctest.NewServer(t, true)
	c2 := transport.New(netx.NewDirect(netx.Options{}), transport.Options{})
	_, err = c2.Unary(context.Background(), call(tlsSrv, grpctest.MethodUnary), nil)
	wantStatus(t, err, transport.Unavailable, transport.CodeTLSUnknownAuthority)
	if errs.KindOf(err) != errs.TLS {
		t.Fatalf("kind = %v", errs.KindOf(err))
	}

	// Plaintext h2c to a TLS port, and TLS to an h2c port.
	h2cSrv := grpctest.NewServer(t, false)
	for _, o := range []transport.CallOptions{
		{Address: tlsSrv.Addr, Method: grpctest.MethodUnary},
		{Address: h2cSrv.Addr, TLS: true, Method: grpctest.MethodUnary},
	} {
		c3 := transport.New(netx.NewDirect(netx.Options{}), transport.Options{Transport: httpclient.TransportOptions{RootCAs: tlsSrv.RootCAs}})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c3.Unary(ctx, o, nil)
		cancel()
		if st := transport.StatusFromError(err); st.Code != transport.Unavailable {
			t.Errorf("TLS %v to the wrong port: status %+v (%v)", o.TLS, st, err)
		}
	}
}

func TestStreamReset(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	srv.Handle("/reset.S/M", func(ss *grpctest.ServerStream) transport.Status {
		ss.Send([]byte("before"))
		panic(http.ErrAbortHandler) // net/http resets the stream
	})
	c := newClient(t, srv, transport.Options{})
	s, err := c.Invoke(context.Background(), call(srv, "/reset.S/M"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.CloseSend()
	if m, err := s.Recv(); err != nil || string(m) != "before" {
		t.Fatalf("Recv = %q, %v", m, err)
	}
	_, err = s.Recv()
	wantStatus(t, err, transport.Internal, transport.CodeStreamReset)
}

func TestNotHTTP2(t *testing.T) {
	t.Parallel()
	// TLS without h2 in ALPN, and h2c to an HTTP/1-only server.
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tlsSrv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(tlsSrv.Certificate())
	h1Srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer h1Srv.Close()

	c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{Transport: httpclient.TransportOptions{RootCAs: pool}})
	defer c.CloseIdleConnections()
	_, err := c.Unary(context.Background(), transport.CallOptions{Address: tlsSrv.Listener.Addr().String(), TLS: true, Method: "/a.B/C"}, nil)
	wantStatus(t, err, transport.Unavailable, transport.CodeNotHTTP2)
	// Over h2c, net/http reports the HTTP/1 answer either as a framing error
	// (not_http2) or as a connection that could not be established.
	_, err = c.Unary(context.Background(), transport.CallOptions{Address: h1Srv.Listener.Addr().String(), Method: "/a.B/C"}, nil)
	if st := transport.StatusFromError(err); st.Code != transport.Unavailable {
		t.Fatalf("h2c to HTTP/1: %v", err)
	}
}

func TestInvalidOptions(t *testing.T) {
	t.Parallel()
	c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{})
	base := transport.CallOptions{Address: "127.0.0.1:1", Method: "/a.B/C"}
	tests := []struct {
		name string
		edit func(*transport.CallOptions)
		code string
	}{
		{"empty address", func(o *transport.CallOptions) { o.Address = "" }, transport.CodeInvalidAddress},
		{"scheme in address", func(o *transport.CallOptions) { o.Address = "http://x:1" }, transport.CodeInvalidAddress},
		{"bad port", func(o *transport.CallOptions) { o.Address = "x:port" }, transport.CodeInvalidAddress},
		{"method", func(o *transport.CallOptions) { o.Method = "a.B/C" }, transport.CodeInvalidMethod},
		{"reserved metadata", func(o *transport.CallOptions) { o.Metadata = transport.Metadata{{Key: "grpc-timeout", Value: "1S"}} }, transport.CodeReservedMetadata},
		{"subtype", func(o *transport.CallOptions) { o.ContentSubtype = "Proto Buf" }, transport.CodeInvalidOption},
		{"compression", func(o *transport.CallOptions) { o.Compression = "zstd" }, transport.CodeInvalidOption},
		{"user agent", func(o *transport.CallOptions) { o.UserAgent = "a\nb" }, transport.CodeInvalidOption},
		{"authority", func(o *transport.CallOptions) { o.Authority = "a/b" }, transport.CodeInvalidOption},
	}
	for _, tt := range tests {
		o := base
		tt.edit(&o)
		s, err := c.Invoke(context.Background(), o)
		e, ok := errors.AsType[*errs.Error](err)
		if s != nil || !ok || e.Code != tt.code {
			t.Errorf("%s: Invoke = %v, %v; want code %s", tt.name, s, err, tt.code)
		}
	}
}
