package tcpclient

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// tlsServer is a TLS echo server's certificates.
type tlsServer struct {
	ca     *tlstest.CA
	server *tlstest.Cert
}

func newTLSServer(t *testing.T) tlsServer {
	t.Helper()
	ca := tlstest.NewCA(t, "tcpclient test CA")
	return tlsServer{ca: ca, server: ca.Issue(t, tlstest.CertOptions{})}
}

// listenTLS starts a loopback TLS server with cfg that runs handle for
// each connection once its handshake succeeded, and returns its address.
func listenTLS(t *testing.T, cfg *tls.Config, handle func(net.Conn)) string {
	t.Helper()
	return listen(t, func(c net.Conn) {
		tc := tls.Server(c, cfg)
		_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
		if err := tc.Handshake(); err != nil {
			return
		}
		// TLS 1.3 servers verify the client certificate after the client's
		// handshake is done; the first read reports it (and sends the alert).
		handle(tc)
	})
}

func (s tlsServer) config() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.server.TLS}}
}

func clientCert(c *tlstest.Cert) *httpclient.ClientCert {
	return &httpclient.ClientCert{Certificate: &c.TLS, Description: "test client"}
}

func TestTLSEcho(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t)
	cfg := srv.config()
	var mu sync.Mutex
	var sni []string
	cfg.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		sni = append(sni, h.ServerName)
		mu.Unlock()
		return nil, nil
	}
	addr := listenTLS(t, cfg, echo)
	_, port, _ := net.SplitHostPort(addr)

	for _, address := range []string{addr, "localhost:" + port} {
		rec := newRecorder()
		o := rec.options(address)
		o.TLS = &TLSOptions{RootCAs: srv.ca.Pool()}
		c, err := Connect(t.Context(), o)
		if err != nil {
			t.Fatalf("%s: %v", address, err)
		}
		if info := c.TLS(); info == nil || !strings.HasPrefix(info.Version, "TLS 1.") || info.CipherSuite == "" {
			t.Errorf("TLS info %+v", info)
		}
		msg := "hello over TLS\n"
		if err := c.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		if got := rec.waitReceived(t, len(msg)); string(got) != msg {
			t.Errorf("received %q", got)
		}
		_ = c.Close()
		rec.mu.Lock()
		open := rec.changes[1]
		rec.mu.Unlock()
		if open.State != StateOpen || open.TLS == nil {
			t.Errorf("open change %+v", open)
		}
		if info := c.CloseInfo(); info.InitiatedBy != InitiatorLocal || info.Err != nil || info.BytesSent != int64(len(msg)) {
			t.Errorf("close info %+v", info)
		}
	}
	// No SNI for an IP address; the host name otherwise.
	mu.Lock()
	defer mu.Unlock()
	if len(sni) != 2 || sni[0] != "" || sni[1] != "localhost" {
		t.Errorf("SNI %q", sni)
	}
}

func TestTLSServerNameOverride(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "override CA")
	srv := ca.Issue(t, tlstest.CertOptions{DNSNames: []string{"echo.test"}})
	addr := listenTLS(t, &tls.Config{Certificates: []tls.Certificate{srv.TLS}}, echo)

	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{RootCAs: ca.Pool()}
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.TLS, netx.CodeTLSHostnameMismatch)

	o.TLS.ServerName = "echo.test"
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestTLSClientCertificate(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t)
	clientCA := tlstest.NewCA(t, "client CA")
	good := clientCA.Issue(t, tlstest.CertOptions{Client: true})
	stranger := tlstest.NewCA(t, "other CA").Issue(t, tlstest.CertOptions{Client: true})

	for _, v := range []struct {
		name string
		max  uint16
	}{{"TLS1.2", tls.VersionTLS12}, {"TLS1.3", tls.VersionTLS13}} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			addr := listenTLS(t, tlstest.MTLSConfig(srv.server, clientCA.Pool(), tls.VersionTLS12, v.max), echo)

			// With the certificate the server trusts: an echo.
			rec := newRecorder()
			o := rec.options(addr)
			o.TLS = &TLSOptions{RootCAs: srv.ca.Pool(), ClientCertificate: clientCert(good)}
			c, err := Connect(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Send([]byte("mtls")); err != nil {
				t.Fatal(err)
			}
			if got := rec.waitReceived(t, 4); string(got) != "mtls" {
				t.Errorf("received %q", got)
			}
			_ = c.Close()

			// Without one, and with one the server does not trust. TLS 1.2
			// fails the handshake; TLS 1.3 finishes the client's side and the
			// server's alert closes the open connection.
			for _, tc := range []struct {
				cert  *httpclient.ClientCert
				codes []string
			}{
				{nil, []string{netx.CodeTLSClientCertRequired}},
				{clientCert(stranger), []string{netx.CodeTLSClientCertRejected}},
			} {
				rec := newRecorder()
				o := rec.options(addr)
				o.TLS = &TLSOptions{RootCAs: srv.ca.Pool(), ClientCertificate: tc.cert}
				c, err := Connect(t.Context(), o)
				if err == nil {
					select {
					case <-c.Done():
					case <-time.After(5 * time.Second):
						t.Fatal("not closed by the server")
					}
					err = c.Err()
					if info := c.CloseInfo(); info.InitiatedBy != InitiatorNone {
						t.Errorf("close info %+v", info)
					}
				}
				e, ok := errors.AsType[*errs.Error](err)
				// A TLS 1.2 server answers a missing certificate with a
				// generic alert, which reads as a refusal.
				if !ok || e.Kind != errs.TLS || (e.Code != tc.codes[0] && e.Code != netx.CodeTLSClientCertRejected) {
					t.Errorf("cert %v: %v (%#v)", tc.cert != nil, err, e)
				}
				if v.max == tls.VersionTLS13 && tc.cert == nil && (e == nil || e.Code != netx.CodeTLSClientCertRequired) {
					t.Errorf("TLS 1.3 without certificate: %v", err)
				}
			}
		})
	}
}

func TestTLSUntrustedAndInsecure(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t)
	addr := listenTLS(t, srv.config(), echo)

	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{}
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.TLS, netx.CodeTLSUnknownAuthority)
	if got := rec.states(); got != "connecting,closed" {
		t.Errorf("states %s", got)
	}

	o.TLS = &TLSOptions{InsecureSkipVerify: true}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestTLSToPlainServer(t *testing.T) {
	t.Parallel()
	// A server that greets in plain text.
	addr := listen(t, func(c net.Conn) {
		_, _ = c.Write([]byte("220 ready\r\n"))
		_, _ = io.Copy(io.Discard, c)
	})
	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{}
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.TLS, netx.CodeTLSServerNotTLS)
	if e, _ := errors.AsType[*errs.Error](err); e.Hint != HintServerNotTLS || e.Details["address"] != addr {
		t.Errorf("error %#v", e)
	}

	// A plain echo server sends the ClientHello back: a TLS failure too.
	addr = listen(t, echo)
	o = rec.options(addr)
	o.TLS = &TLSOptions{}
	_, err = Connect(t.Context(), o)
	if errs.KindOf(err) != errs.TLS {
		t.Errorf("echo: %v", err)
	}
}

func TestTLSHandshakeTimeout(t *testing.T) {
	t.Parallel()
	// Accepts and never answers the ClientHello.
	addr := listen(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{}
	o.ConnectTimeout = 100 * time.Millisecond
	start := time.Now()
	_, err := Connect(t.Context(), o)
	checkCode(t, err, errs.Timeout, netx.CodeTimeout)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestTLSCloseWrite(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t)
	// Echoes until the client's end of stream, then says bye and closes.
	addr := listenTLS(t, srv.config(), func(c net.Conn) {
		_, _ = io.Copy(c, c)
		_, _ = c.Write([]byte("bye"))
	})
	rec := newRecorder()
	o := rec.options(addr)
	o.TLS = &TLSOptions{RootCAs: srv.ca.Pool()}
	c, err := Connect(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not closed")
	}
	if got := string(rec.bytes(Received)); got != "xbye" {
		t.Errorf("received %q", got)
	}
	if info := c.CloseInfo(); info.InitiatedBy != InitiatorRemote || info.Err != nil {
		t.Errorf("close info %+v", info)
	}
}
