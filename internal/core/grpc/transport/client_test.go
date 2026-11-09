package transport_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// modes runs a test against an h2c and a TLS server.
func modes(t *testing.T, fn func(t *testing.T, srv *grpctest.Server, c *transport.Client)) {
	for _, withTLS := range []bool{false, true} {
		name := "h2c"
		if withTLS {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := grpctest.NewServer(t, withTLS)
			fn(t, srv, newClient(t, srv, transport.Options{}))
		})
	}
}

func newClient(t *testing.T, srv *grpctest.Server, o transport.Options) *transport.Client {
	t.Helper()
	o.Transport = httpclient.TransportOptions{RootCAs: srv.RootCAs}
	c := transport.New(netx.NewDirect(netx.Options{}), o)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func call(srv *grpctest.Server, method string, md ...transport.MDEntry) transport.CallOptions {
	return transport.CallOptions{Address: srv.Addr, TLS: srv.TLS, Method: method, Metadata: md}
}

func md(k, v string) transport.MDEntry { return transport.MDEntry{Key: k, Value: v} }

// wantStatus checks that err carries code (via StatusFromError), is an
// *errs.Error with the transport error code, and matches the stream's
// Status.
func wantStatus(t *testing.T, err error, code transport.Code, errCode string) transport.Status {
	t.Helper()
	if err == nil || err == io.EOF {
		t.Fatalf("error = %v, want status %v", err, code)
	}
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("error %T %v is not an *errs.Error", err, err)
	}
	st := transport.StatusFromError(err)
	if st.Code != code || e.Code != errCode {
		t.Fatalf("status %v, code %q (%v); want %v, %q", st.Code, e.Code, err, code, errCode)
	}
	return st
}

func TestUnary(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		o := call(srv, grpctest.MethodUnary, md("x-id", "42"), md("x-blob-bin", "\x00\xff\x01"), md("x-id", "43"))
		o.ContentSubtype = "proto"
		o.UserAgent = "postsonde-test/1"
		res, err := c.Unary(context.Background(), o, []byte("hello"))
		if err != nil {
			t.Fatalf("Unary: %v", err)
		}
		if string(res.Message) != "hello" || !res.Status.OK() {
			t.Fatalf("result = %q, %+v", res.Message, res.Status)
		}
		if got := res.Header.Get("x-id"); !slices.Equal(got, []string{"42", "43"}) {
			t.Errorf("header x-id = %q", got)
		}
		if got := res.Header.Get("x-blob-bin"); !slices.Equal(got, []string{"\x00\xff\x01"}) {
			t.Errorf("header x-blob-bin = %q", got)
		}
		if got := res.Header.Get("content-type"); !slices.Equal(got, []string{"application/grpc"}) {
			t.Errorf("header content-type = %q", got)
		}
		if got := res.Trailer.Get("t-x-blob-bin"); !slices.Equal(got, []string{"\x00\xff\x01"}) {
			t.Errorf("trailer t-x-blob-bin = %q", got)
		}
		if got := res.Trailer.Get("grpc-status"); !slices.Equal(got, []string{"0"}) {
			t.Errorf("trailer grpc-status = %q", got)
		}

		r := srv.LastRequest()
		wantHeaders := map[string]string{
			"Content-Type": "application/grpc+proto",
			"Te":           "trailers",
			"User-Agent":   "postsonde-test/1",
			"X-Blob-Bin":   "AP8B", // unpadded base64
		}
		for k, v := range wantHeaders {
			if got := r.Header.Get(k); got != v {
				t.Errorf("request %s = %q, want %q", k, got, v)
			}
		}
		if r.ProtoMajor != 2 || r.Method != http.MethodPost || r.URL.Path != grpctest.MethodUnary {
			t.Errorf("request = %s %s %s", r.Proto, r.Method, r.URL.Path)
		}
		if _, ok := r.Header["Grpc-Timeout"]; ok {
			t.Errorf("grpc-timeout sent without a deadline")
		}
	})
}

func TestNoDefaultHeaders(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	if _, err := c.Unary(context.Background(), call(srv, grpctest.MethodUnary), []byte("x")); err != nil {
		t.Fatal(err)
	}
	r := srv.LastRequest()
	var names []string
	for k := range r.Header {
		names = append(names, k)
	}
	slices.Sort(names)
	if want := []string{"Content-Type", "Te"}; !slices.Equal(names, want) {
		t.Fatalf("request headers = %q, want %q", names, want)
	}
	if r.Host != srv.Addr {
		t.Fatalf(":authority = %q, want %q", r.Host, srv.Addr)
	}
}

func TestAuthority(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, true)
	c := newClient(t, srv, transport.Options{})
	o := call(srv, grpctest.MethodUnary)
	o.Authority = "api.example.test:8443"
	if _, err := c.Unary(context.Background(), o, nil); err != nil {
		t.Fatal(err)
	}
	if got := srv.LastRequest().Host; got != o.Authority {
		t.Fatalf(":authority = %q, want %q", got, o.Authority)
	}
}

func TestServerStreaming(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodServerStream, md("x-count", "5")))
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
		if _, ok := s.Status(); ok {
			t.Fatal("status available before the end")
		}
		var got []string
		for {
			m, err := s.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			got = append(got, string(m))
		}
		if want := []string{"msg0", "msg1", "msg2", "msg3", "msg4"}; !slices.Equal(got, want) {
			t.Fatalf("messages = %q, want %q", got, want)
		}
		if st, ok := s.Status(); !ok || !st.OK() {
			t.Fatalf("Status = %+v, %v", st, ok)
		}
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("Recv after end = %v, want io.EOF again", err)
		}
		if got := s.Trailer().Get("t-x-count"); !slices.Equal(got, []string{"5"}) {
			t.Fatalf("trailer t-x-count = %q", got)
		}
		if err := s.Send([]byte("late")); err == nil {
			t.Fatal("Send after CloseSend succeeded")
		}
	})
}

func TestClientStreaming(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodClientStream))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for _, p := range []string{"a", "", "bc", strings.Repeat("d", 100_000)} {
			if err := s.Send([]byte(p)); err != nil {
				t.Fatalf("Send: %v", err)
			}
		}
		s.CloseSend()
		m, err := s.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if want := "abc" + strings.Repeat("d", 100_000); string(m) != want {
			t.Fatalf("message of %d bytes, want %d", len(m), len(want))
		}
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("end = %v", err)
		}
	})
}

func TestBidiStreaming(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, srv *grpctest.Server, c *transport.Client) {
		s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodBidi, md("x-mode", "bidi")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		h, err := s.Header()
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Get("x-mode"); !slices.Equal(got, []string{"bidi"}) {
			t.Fatalf("header x-mode = %q", got)
		}
		// Interleave: each reply must arrive before the next send.
		for i := range 20 {
			want := fmt.Sprintf("ping %d", i)
			if err := s.Send([]byte(want)); err != nil {
				t.Fatalf("Send %d: %v", i, err)
			}
			m, err := s.Recv()
			if err != nil || string(m) != want {
				t.Fatalf("Recv %d = %q, %v", i, m, err)
			}
		}
		s.CloseSend()
		if _, err := s.Recv(); err != io.EOF {
			t.Fatalf("end = %v", err)
		}
	})
}

func TestBidiConcurrent(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	c := newClient(t, srv, transport.Options{})
	s, err := c.Invoke(context.Background(), call(srv, grpctest.MethodBidi))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const n = 200
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range n {
			if err := s.Send(bytes.Repeat([]byte{byte(i)}, 1000)); err != nil {
				t.Errorf("Send %d: %v", i, err)
				return
			}
		}
		s.CloseSend()
	})
	for i := range n {
		m, err := s.Recv()
		if err != nil || len(m) != 1000 || m[0] != byte(i) {
			t.Fatalf("Recv %d = %d bytes, %v", i, len(m), err)
		}
	}
	if _, err := s.Recv(); err != io.EOF {
		t.Fatalf("end = %v", err)
	}
	wg.Wait()
}
