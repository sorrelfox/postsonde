package tlsconf

import (
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

func TestInspectBundle(t *testing.T) {
	a, b := tlstest.NewCA(t, "Corp Root"), tlstest.NewCA(t, "Dev CA")
	path := tlstest.WriteFile(t, "bundle.pem", append(append([]byte("comment\n"), a.CertPEM...), b.CertPEM...))
	got, err := InspectBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != path || len(got.Certs) != 2 || len(got.Warnings) != 0 {
		t.Fatalf("bundle = %+v", got)
	}
	c := got.Certs[0]
	if !strings.Contains(c.Subject, "CN=Corp Root") || !c.IsCA || !c.NotAfter.Equal(a.Cert.NotAfter) || len(c.SHA256) != 64 {
		t.Errorf("cert = %+v", c)
	}
}

func TestInspectBundleWarnings(t *testing.T) {
	now := time.Now()
	expired := tlstest.NewCAValid(t, "Old CA", now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	future := tlstest.NewCAValid(t, "Future CA", now.Add(24*time.Hour), now.Add(48*time.Hour))
	leaf := tlstest.NewCA(t, "Other").Issue(t, tlstest.CertOptions{CommonName: "server.local"})
	data := slices.Concat(expired.CertPEM, future.CertPEM, leaf.CertPEM, leaf.KeyPEM)
	got, err := inspectBundle(tlstest.WriteFile(t, "mixed.pem", data), now)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, w := range got.Warnings {
		codes = append(codes, w.Code)
		if w.Message == "" || w.Hint == "" {
			t.Errorf("warning %+v lacks a message or hint", w)
		}
	}
	want := []string{CodeCAExpired, CodeCANotYet, CodeCANotForCA, CodeCAHasKey}
	if !slices.Equal(codes, want) {
		t.Fatalf("warnings %v, want %v", codes, want)
	}
	if !strings.Contains(got.Warnings[0].Message, `"Old CA" expired on`) {
		t.Errorf("message %q", got.Warnings[0].Message)
	}
}

func TestInspectBundleErrors(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "ca.der")
	if err := os.WriteFile(notPEM, tlstest.NewCA(t, "x").Cert.Raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keyOnly := tlstest.WriteFile(t, "key.pem", tlstest.NewCA(t, "y").Issue(t, tlstest.CertOptions{}).KeyPEM)
	broken := tlstest.WriteFile(t, "broken.pem", []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"))
	tests := []struct {
		name, path string
		kind       errs.Kind
		code       string
	}{
		{"not PEM", notPEM, errs.Parse, CodeCANoCertificates},
		{"no certificate", keyOnly, errs.Parse, CodeCANoCertificates},
		{"invalid certificate", broken, errs.Parse, CodeCAInvalid},
		{"missing", filepath.Join(dir, "nope.pem"), errs.NotFound, CodeFileUnreadable},
		{"relative", "ca.pem", errs.InvalidArgument, CodeInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InspectBundle(tt.path)
			e := wantCode(t, err, tt.kind, tt.code, "caBundle")
			if tt.code == CodeCANoCertificates && e.Hint != HintPEM {
				t.Errorf("hint %q", e.Hint)
			}
		})
	}
}

func TestTrustRoots(t *testing.T) {
	ca := tlstest.NewCA(t, "Dev CA")
	server := ca.Issue(t, tlstest.CertOptions{})
	path := tlstest.WriteFile(t, "ca.pem", ca.CertPEM)

	var mu sync.Mutex
	var files []string
	tr := NewTrust(func() []string { mu.Lock(); defer mu.Unlock(); return files })
	tr.system = func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }
	set := func(f ...string) { mu.Lock(); files = f; mu.Unlock() }

	// No bundles: nil, the system roots.
	if pool, err := tr.Roots(); pool != nil || err != nil {
		t.Fatalf("no bundles: %v, %v", pool, err)
	}

	set(path)
	pool, err := tr.Roots()
	if err != nil || pool == nil {
		t.Fatalf("Roots = %v, %v", pool, err)
	}
	if _, err := server.Cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		t.Fatalf("the bundle's CA is not trusted: %v", err)
	}
	// Unchanged: the same pool, so clients keyed on it are reused.
	if again, _ := tr.Roots(); again != pool {
		t.Error("the pool was rebuilt although nothing changed")
	}

	// The file changes: a new pool with the new CA.
	other := tlstest.NewCA(t, "Other CA")
	if err := os.WriteFile(path, slices.Concat(other.CertPEM, ca.CertPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	next, err := tr.Roots()
	if err != nil || next == pool {
		t.Fatalf("after a change: %v (same %v), %v", next, next == pool, err)
	}
	if _, err := other.Issue(t, tlstest.CertOptions{}).Cert.Verify(x509.VerifyOptions{Roots: next, DNSName: "localhost"}); err != nil {
		t.Errorf("the added CA is not trusted: %v", err)
	}

	// A bundle that is gone is an error naming the setting.
	set(path, filepath.Join(t.TempDir(), "gone.pem"))
	_, err = tr.Roots()
	e := wantCode(t, err, errs.NotFound, CodeFileUnreadable, "network.caBundles[1]")
	if e.Hint != HintBundle {
		t.Errorf("hint %q", e.Hint)
	}
	// So is one that is no longer PEM.
	junk := tlstest.WriteFile(t, "junk.pem", []byte("junk"))
	set(junk)
	_, err = tr.Roots()
	wantCode(t, err, errs.Parse, CodeCANoCertificates, "network.caBundles[0]")

	// Back to none.
	set()
	if pool, err := tr.Roots(); pool != nil || err != nil {
		t.Fatalf("cleared: %v, %v", pool, err)
	}

	// System roots that cannot be loaded.
	tr = NewTrust(func() []string { return []string{path} })
	tr.system = func() (*x509.CertPool, error) { return nil, errors.New("no roots") }
	_, err = tr.Roots()
	wantCode(t, err, errs.Unsupported, CodeSystemRoots, "network.caBundles")

	var nilTrust *Trust
	if pool, err := nilTrust.Roots(); pool != nil || err != nil {
		t.Errorf("nil Trust: %v, %v", pool, err)
	}
}

// TestTrustAddsToSystemRoots: the bundles are trusted on top of the
// system's certificate authorities, never instead of them.
func TestTrustAddsToSystemRoots(t *testing.T) {
	sys := tlstest.NewCA(t, "System CA")
	custom := tlstest.NewCA(t, "Custom CA")
	tr := NewTrust(func() []string { return []string{tlstest.WriteFile(t, "ca.pem", custom.CertPEM)} })
	tr.system = func() (*x509.CertPool, error) { return sys.Pool(), nil }
	pool, err := tr.Roots()
	if err != nil {
		t.Fatal(err)
	}
	for _, ca := range []*tlstest.CA{sys, custom} {
		if _, err := ca.Issue(t, tlstest.CertOptions{}).Cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
			t.Errorf("%s: %v", ca.Cert.Subject.CommonName, err)
		}
	}
}
