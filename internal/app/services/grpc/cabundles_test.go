package grpc

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestInvokeCustomCA (NET-04): a TLS server whose certificate is signed by
// a private CA is refused until the CA's bundle is trusted; plaintext
// calls ignore the bundles, even a broken one.
func TestInvokeCustomCA(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, true)
	bundle := tlstest.WriteFile(t, "dev-ca.pem", srv.CA.CertPEM)

	var mu sync.Mutex
	var bundles []string
	setBundles := func(b ...string) { mu.Lock(); bundles = b; mu.Unlock() }
	trust := tlsconf.NewTrust(func() []string { mu.Lock(); defer mu.Unlock(); return bundles })
	s, rec := newCallService(t, WithTrust(trust.Roots))

	call := func() string {
		t.Helper()
		in := input(srv.Addr, "Echo")
		in.TLS = true
		started, err := s.Invoke(t.Context(), in)
		if err != nil {
			var e *errs.Error
			if errors.As(err, &e) {
				return e.Code
			}
			t.Fatalf("Invoke: %v", err)
		}
		_, done := rec.wait(t, started.OpID)
		if done.Status == operation.StatusOK {
			return ""
		}
		if done.Error == nil {
			t.Fatalf("done = %+v", done)
		}
		return done.Error.Code
	}

	if code := call(); code != transport.CodeTLSUnknownAuthority {
		t.Fatalf("without the bundle: %q, want %s", code, transport.CodeTLSUnknownAuthority)
	}
	setBundles(bundle)
	if code := call(); code != "" {
		t.Fatalf("with the bundle: %q, want success", code)
	}

	setBundles(filepath.Join(t.TempDir(), "gone.pem"))
	if code := call(); code != tlsconf.CodeFileUnreadable {
		t.Fatalf("broken bundle: %q, want %s", code, tlsconf.CodeFileUnreadable)
	}
	plain := newCallServer(t, false)
	if _, done := invoke(t, s, rec, input(plain.Addr, "Echo")); done.Status != operation.StatusOK {
		t.Fatalf("h2c call with a broken bundle: %+v", done)
	}
}
