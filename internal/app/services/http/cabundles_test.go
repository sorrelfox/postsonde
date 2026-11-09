package http

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestSendCustomCA (NET-04): a server whose certificate is signed by a
// private CA is refused until the CA's bundle is added to the trusted
// ones, and refused again once it is removed.
func TestSendCustomCA(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Dev CA")
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		io.WriteString(w, "trusted")
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{}).TLS}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	bundle := tlstest.WriteFile(t, "dev-ca.pem", ca.CertPEM)

	var mu sync.Mutex
	var bundles []string
	setBundles := func(b ...string) { mu.Lock(); bundles = b; mu.Unlock() }
	trust := tlsconf.NewTrust(func() []string { mu.Lock(); defer mu.Unlock(); return bundles })
	f := newFixture(t, httpSource(srv.URL), nil, WithTrust(trust.Roots))

	sendCode := func() string {
		t.Helper()
		id := f.send(t)
		done := f.rec.wait(t, id)
		if done.Status == operation.StatusOK {
			if resp := f.rec.response(t, id); resp.Body != "trusted" || resp.InsecureTLS {
				t.Fatalf("response %q, insecure %v", resp.Body, resp.InsecureTLS)
			}
			return ""
		}
		if done.Error == nil {
			t.Fatalf("done = %+v", done)
		}
		return done.Error.Code
	}

	if code := sendCode(); code != httpclient.CodeTLSUnknownAuthority {
		t.Fatalf("without the bundle: %q, want %s", code, httpclient.CodeTLSUnknownAuthority)
	}
	setBundles(bundle)
	if code := sendCode(); code != "" {
		t.Fatalf("with the bundle: %q, want success", code)
	}
	setBundles()
	if code := sendCode(); code != httpclient.CodeTLSUnknownAuthority {
		t.Fatalf("after removing the bundle: %q, want %s", code, httpclient.CodeTLSUnknownAuthority)
	}

	// A bundle that cannot be used fails an https send at once, naming the
	// setting.
	setBundles(filepath.Join(t.TempDir(), "gone.pem"))
	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != tlsconf.CodeFileUnreadable || e.Details["field"] != "network.caBundles[0]" || e.Hint != tlsconf.HintBundle {
		t.Fatalf("broken bundle: %v", err)
	}
}

// TestSendBrokenBundlePlainHTTP: a broken CA bundle does not stop plain
// http requests, which do not use it.
func TestSendBrokenBundlePlainHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) { io.WriteString(w, "plain") }))
	t.Cleanup(srv.Close)
	gone := filepath.Join(t.TempDir(), "gone.pem")
	trust := tlsconf.NewTrust(func() []string { return []string{gone} })
	f := newFixture(t, httpSource(srv.URL), nil, WithTrust(trust.Roots))
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
}
