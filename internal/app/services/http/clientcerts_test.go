package http

import (
	"io"
	"log"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestSendClientCertificates (AUTH-30): a server requiring mTLS refuses a
// send with a clear error until a client certificate is set for its host,
// then accepts it as PEM files and as a PKCS#12 file with a passphrase.
func TestSendClientCertificates(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "mTLS server CA")
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = tlstest.MTLSConfig(ca.Issue(t, tlstest.CertOptions{}), tlstest.FixtureClientCAs(t), 0, 0)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	bundle := tlstest.WriteFile(t, "ca.pem", ca.CertPEM)
	trust := tlsconf.NewTrust(func() []string { return []string{bundle} })

	var mu sync.Mutex
	var entries []tlsconf.ClientCertEntry
	pass := map[string]string{"pfx": tlstest.FixturePassword}
	set := func(e ...tlsconf.ClientCertEntry) { mu.Lock(); entries = e; mu.Unlock() }
	certs := tlsconf.NewClientCerts(func() []tlsconf.ClientCertEntry { mu.Lock(); defer mu.Unlock(); return entries },
		func(id string) (string, error) { return pass[id], nil })
	pick := func(addr string) (*httpclient.ClientCert, error) {
		c, err := certs.ForAddress(addr)
		if err != nil || c == nil {
			return nil, err
		}
		return &httpclient.ClientCert{Certificate: c.Certificate, Description: c.Describe()}, nil
	}
	f := newFixture(t, httpSource(srv.URL), nil, WithTrust(trust.Roots), WithClientCerts(pick))

	send := func() (string, string) {
		t.Helper()
		id := f.send(t)
		done := f.rec.wait(t, id)
		if done.Status == operation.StatusOK {
			return f.rec.response(t, id).Body, ""
		}
		if done.Error == nil {
			t.Fatalf("done = %+v", done)
		}
		return "", done.Error.Code
	}

	if _, code := send(); code != netx.CodeTLSClientCertRequired {
		t.Fatalf("without a certificate: %q, want %s", code, netx.CodeTLSClientCertRequired)
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	set(tlsconf.ClientCertEntry{ID: "pem", Host: host, CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "rsa.key")})
	if body, code := send(); code != "" || body != "PostSonde Test Client rsa" {
		t.Fatalf("pem: %q, %q", body, code)
	}
	set(tlsconf.ClientCertEntry{ID: "pfx", Host: "127.0.0.*", PFXFile: tlstest.Fixture(t, "ec-aes128.p12")})
	if body, code := send(); code != "" || body != "PostSonde Test Client ec" {
		t.Fatalf("pkcs12: %q, %q", body, code)
	}
}
