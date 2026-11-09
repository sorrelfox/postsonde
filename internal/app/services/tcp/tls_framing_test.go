package tcp

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/framing"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// messagesOf returns the chunks (messages) in direction dir of connection
// id; e.mu must be held.
func (e *events) messagesOf(t *testing.T, id, dir string) []ChunkDTO {
	var out []ChunkDTO
	for _, ev := range e.data {
		if ev.ConnID != id {
			continue
		}
		for _, c := range ev.Chunks {
			if c.Direction == dir {
				d, err := base64.StdEncoding.DecodeString(c.Data)
				if err != nil {
					t.Error(err)
				}
				c.Data = string(d)
				out = append(out, c)
			}
		}
	}
	return out
}

func texts(chunks []ChunkDTO) string {
	s := make([]string, len(chunks))
	for i, c := range chunks {
		s[i] = c.Data
		if c.Incomplete {
			s[i] += "(incomplete)"
		}
	}
	return strings.Join(s, "|")
}

// listenTLS starts a TLS server with cfg that runs handle after the
// handshake.
func listenTLS(t *testing.T, cfg *tls.Config, handle func(net.Conn)) string {
	t.Helper()
	return listen(t, func(c net.Conn) {
		tc := tls.Server(c, cfg)
		_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
		if tc.Handshake() == nil {
			handle(tc)
		}
	})
}

func newTLSService(t *testing.T, roots RootsFunc, pick httpclient.ClientCertFunc) (*TCPService, *events) {
	t.Helper()
	ev := newEvents()
	s := New(netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), ev.emit, nil,
		withInterval(5*time.Millisecond), WithTrust(roots), WithClientCerts(pick))
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, ev
}

// TestTLSWithClientCertificate connects to an mTLS echo server with the
// app's trust and the client certificate picked for its address.
func TestTLSWithClientCertificate(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "service CA")
	server := ca.Issue(t, tlstest.CertOptions{})
	clientCA := tlstest.NewCA(t, "client CA")
	client := clientCA.Issue(t, tlstest.CertOptions{Client: true})
	addr := listenTLS(t, tlstest.MTLSConfig(server, clientCA.Pool(), tls.VersionTLS12, 0), func(c net.Conn) { _, _ = io.Copy(c, c) })

	var picked []string
	withCert := true
	pick := func(a string) (*httpclient.ClientCert, error) {
		picked = append(picked, a)
		if !withCert {
			return nil, nil
		}
		return &httpclient.ClientCert{Certificate: &client.TLS, Description: "client"}, nil
	}
	s, ev := newTLSService(t, func() (*x509.CertPool, error) { return ca.Pool(), nil }, pick)
	ctx := t.Context()

	conn, err := s.Connect(ctx, ConnectInputDTO{RequestID: "req_tls", Address: addr, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: "secure", LineEnding: "lf"}); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "the echo", func(e *events) bool { return e.bytesOf(t, id, "received") == "secure\n" })
	ev.mu.Lock()
	open := ev.states[1]
	ev.mu.Unlock()
	if open.TLS == nil || !strings.HasPrefix(open.TLS.Version, "TLS 1.") || open.TLS.PeerSubject == "" {
		t.Errorf("open = %+v, tls %+v", open, open.TLS)
	}
	if err := s.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(picked) != 1 || picked[0] != addr {
		t.Errorf("picked for %q", picked)
	}

	// Without a certificate the server refuses (TLS 1.3: after the
	// handshake, so the connection closes with the error).
	withCert = false
	conn, err = s.Connect(ctx, ConnectInputDTO{Address: addr, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(conn.ConnID), "closed") })
	if closed := ev.closed(conn.ConnID); closed.Error == nil || closed.Error.Code != netx.CodeTLSClientCertRequired {
		t.Errorf("closed = %+v, error %+v", closed, closed.Error)
	}
}

func TestTLSSettings(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "untrusted CA")
	addr := listenTLS(t, &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{}).TLS}}, func(c net.Conn) { _, _ = io.Copy(c, c) })
	ctx := t.Context()

	// Not trusted: the connection fails with the netx code.
	s, ev := newTLSService(t, func() (*x509.CertPool, error) { return nil, nil }, nil)
	conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "closed", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
	if closed := ev.closed(conn.ConnID); closed.Error == nil || closed.Error.Code != netx.CodeTLSUnknownAuthority {
		t.Errorf("closed error %+v", closed.Error)
	}

	// verifyTls false: accepted, and the trust is not even read.
	broken := func() (*x509.CertPool, error) {
		return nil, errs.New(errs.InvalidArgument, "bundle broken").WithCode("tlsconf.ca_invalid")
	}
	s, ev = newTLSService(t, broken, nil)
	off := false
	conn, err = s.Connect(ctx, ConnectInputDTO{Address: addr, TLS: true, VerifyTLS: &off})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,open" })

	// A broken CA bundle fails a verified TLS connect at once, but not a
	// plain one.
	_, err = s.Connect(ctx, ConnectInputDTO{Address: addr, TLS: true})
	checkCode(t, err, errs.InvalidArgument, "tlsconf.ca_invalid")
	if _, err := s.Connect(ctx, ConnectInputDTO{Address: addr}); err != nil {
		t.Errorf("plain connect: %v", err)
	}

	// A client certificate that cannot be loaded fails at once.
	s, _ = newTLSService(t, nil, func(string) (*httpclient.ClientCert, error) {
		return nil, errs.New(errs.InvalidArgument, "no passphrase").WithCode("tlsconf.client_key_encrypted")
	})
	_, err = s.Connect(ctx, ConnectInputDTO{Address: addr, TLS: true})
	checkCode(t, err, errs.InvalidArgument, "tlsconf.client_key_encrypted")
}

// TestFramingThroughEvents: newline and length framing split what the
// server sends in odd pieces into messages, and frame what is sent.
func TestFramingThroughEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	trickle := func(c net.Conn, b []byte) {
		for i := range b {
			if _, err := c.Write(b[i : i+1]); err != nil {
				return
			}
			time.Sleep(200 * time.Microsecond)
		}
	}

	t.Run("newline", func(t *testing.T) {
		got := make(chan string, 1)
		addr := listen(t, func(c net.Conn) {
			var all []byte
			buf := make([]byte, 64)
			for !strings.HasSuffix(string(all), "A\r\n") {
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				all = append(all, buf[:n]...)
			}
			got <- string(all)
			trickle(c, []byte("one\r\ntwo\n\nthr"))
		})
		// Tiny reads, so the lines really are split across reads.
		ev := newEvents()
		s := New(netx.NewDirect(netx.Options{}), ev.emit, nil, withInterval(5*time.Millisecond),
			withClientOptions(func(o *tcpclient.Options) { o.ReadBufferSize = 2 }))
		t.Cleanup(func() { _ = s.ServiceShutdown() })
		conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr, Framing: &FramingDTO{Mode: "newline", Delimiter: "crlf"}})
		if err != nil {
			t.Fatal(err)
		}
		id := conn.ConnID
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
		// The line ending is not added: the framing adds CRLF. Empty is a
		// message.
		for _, text := range []string{"PING", ""} {
			if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: text, LineEnding: "lf"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "hex", Text: "41"}); err != nil {
			t.Fatal(err)
		}
		if g := <-got; g != "PING\r\n\r\nA\r\n" {
			t.Errorf("server got %q", g)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(id), "closed") })
		ev.mu.Lock()
		defer ev.mu.Unlock()
		if r := texts(ev.messagesOf(t, id, "received")); r != "one|two||thr(incomplete)" {
			t.Errorf("received %s", r)
		}
		if r := texts(ev.messagesOf(t, id, "sent")); r != "PING||A" {
			t.Errorf("sent %s", r)
		}
	})

	t.Run("length", func(t *testing.T) {
		addr := listen(t, func(c net.Conn) {
			// Echo byte by byte.
			buf := make([]byte, 1)
			for {
				if _, err := c.Read(buf); err != nil {
					return
				}
				trickle(c, buf)
			}
		})
		s, ev := newService(t)
		limit := int64(10)
		conn, err := s.Connect(ctx, ConnectInputDTO{Address: addr, Framing: &FramingDTO{Mode: "length", PrefixBytes: 2, ByteOrder: "little", MaxMessageBytes: &limit}})
		if err != nil {
			t.Fatal(err)
		}
		id := conn.ConnID
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
		res, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: "hello"})
		if err != nil || res.Bytes != 5 {
			t.Fatalf("Send = %+v, %v", res, err)
		}
		ev.waitFor(t, "the echo", func(e *events) bool { return texts(e.messagesOf(t, id, "received")) == "hello" })
		// Longer than the maximum: the echo fails the connection.
		if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Mode: "text", Text: "far too long"}); err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(id), "closed") })
		closed := ev.closed(id)
		if closed.Error == nil || closed.Error.Code != framing.CodeMessageTooLarge || closed.Close.InitiatedBy != "none" ||
			closed.Close.BytesSent != 2+5+2+12 || closed.Close.ChunksReceived != 1 {
			t.Errorf("closed = %+v, close %+v, error %+v", closed, closed.Close, closed.Error)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		s, _ := newService(t)
		_, err := s.Connect(ctx, ConnectInputDTO{Address: "localhost:7", Framing: &FramingDTO{Mode: "length", PrefixBytes: 3}})
		checkCode(t, err, errs.InvalidArgument, framing.CodeInvalidOptions)
		zero := int64(0)
		_, err = s.Connect(ctx, ConnectInputDTO{Address: "localhost:7", Framing: &FramingDTO{Mode: "newline", MaxMessageBytes: &zero}})
		checkCode(t, err, errs.InvalidArgument, framing.CodeInvalidOptions)
	})
}
