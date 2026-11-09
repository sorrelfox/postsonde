package tlsconf

import (
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

func TestParseHostPattern(t *testing.T) {
	t.Parallel()
	good := []struct {
		in   string
		want HostPattern
	}{
		{"api.example.com", HostPattern{Host: "api.example.com"}},
		{" API.Example.COM. ", HostPattern{Host: "api.example.com"}},
		{"*.example.com", HostPattern{Host: "*.example.com"}},
		{"*", HostPattern{Host: "*"}},
		{"localhost:8443", HostPattern{Host: "localhost", Port: "8443"}},
		{"*.corp:*", HostPattern{Host: "*.corp"}},
		{"127.0.0.1:443", HostPattern{Host: "127.0.0.1", Port: "443"}},
		{"[::1]:8443", HostPattern{Host: "::1", Port: "8443"}},
		{"[::1]", HostPattern{Host: "::1"}},
		{"fe80::1", HostPattern{Host: "fe80::1"}},
		{"my_host-1", HostPattern{Host: "my_host-1"}},
	}
	for _, tt := range good {
		got, err := ParseHostPattern(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseHostPattern(%q) = %+v, %v, want %+v", tt.in, got, err, tt.want)
		}
		if again, err := ParseHostPattern(got.String()); err != nil || again != got {
			t.Errorf("round trip of %q via %q = %+v, %v", tt.in, got.String(), again, err)
		}
	}
	for _, in := range []string{"", " ", "https://api.example.com", "api.example.com/x", "user@host", "host:", "host:0",
		"host:70000", "host:08", "host:abc", "[::1", "[host]:1", "[::1]x", "a b", "héllo", "host?x", ":443"} {
		_, err := ParseHostPattern(in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeHostPatternInvalid || e.Hint != HintHostPattern || e.Details["field"] != "host" {
			t.Errorf("ParseHostPattern(%q) = %v, want %s", in, err, CodeHostPatternInvalid)
		}
	}
}

func TestHostPatternMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, host, port string
		want                bool
	}{
		{"api.example.com", "api.example.com", "443", true},
		{"api.example.com", "API.example.com.", "8443", true},
		{"api.example.com", "example.com", "443", false},
		{"*.example.com", "api.example.com", "443", true},
		{"*.example.com", "a.b.example.com", "443", true},
		{"*.example.com", "example.com", "443", false},
		{"*.example.com", "evil-example.com", "443", false},
		{"api-*.example.com", "api-eu.example.com", "443", true},
		{"api-*.example.com", "api.example.com", "443", false},
		{"*", "anything", "1", true},
		{"localhost:8443", "localhost", "8443", true},
		{"localhost:8443", "localhost", "443", false},
		{"[::1]:8443", "::1", "8443", true},
		{"[::1]:8443", "[::1]", "8443", true},
		{"*.example.*", "api.example.org", "443", true},
	}
	for _, tt := range tests {
		p, err := ParseHostPattern(tt.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Matches(tt.host, tt.port); got != tt.want {
			t.Errorf("%q.Matches(%q, %q) = %v, want %v", tt.pattern, tt.host, tt.port, got, tt.want)
		}
	}
}

func TestMatchClientCert(t *testing.T) {
	t.Parallel()
	entries := []ClientCertEntry{
		{ID: "any", Host: "*"},
		{ID: "wild", Host: "*.example.com"},
		{ID: "wildport", Host: "*.example.com:8443"},
		{ID: "exact", Host: "api.example.com"},
		{ID: "broken", Host: "https://x"},
		{ID: "exact2", Host: "api.example.com"},
	}
	tests := []struct{ host, port, want string }{
		{"api.example.com", "443", "exact"}, // exact beats wildcards; first of equals
		{"api.example.com", "8443", "exact"},
		{"web.example.com", "8443", "wildport"},
		{"web.example.com", "443", "wild"},
		{"other.org", "443", "any"},
	}
	for _, tt := range tests {
		i := MatchClientCert(entries, tt.host, tt.port)
		if i < 0 || entries[i].ID != tt.want {
			t.Errorf("MatchClientCert(%s:%s) = %d, want %s", tt.host, tt.port, i, tt.want)
		}
	}
	if i := MatchClientCert(entries[1:4], "other.org", "443"); i != -1 {
		t.Errorf("no match = %d", i)
	}
}

func TestValidateClientCertEntry(t *testing.T) {
	t.Parallel()
	abs, _ := filepath.Abs("x.pem")
	ok := []ClientCertEntry{
		{ID: "cc_1", Host: "h", CertFile: abs},
		{ID: "cc_1", Host: "h", CertFile: abs, KeyFile: abs},
		{ID: "cc_1", Host: "h", PFXFile: abs},
	}
	for _, e := range ok {
		if err := ValidateClientCertEntry(e); err != nil {
			t.Errorf("%+v: %v", e, err)
		}
	}
	bad := []struct {
		e    ClientCertEntry
		code string
	}{
		{ClientCertEntry{Host: "h", CertFile: abs}, CodeClientCertID},
		{ClientCertEntry{ID: "a/b", Host: "h", CertFile: abs}, CodeClientCertID},
		{ClientCertEntry{ID: "a", Host: "", CertFile: abs}, CodeHostPatternInvalid},
		{ClientCertEntry{ID: "a", Host: "h"}, CodeClientCertSource},
		{ClientCertEntry{ID: "a", Host: "h", KeyFile: abs}, CodeClientCertSource},
		{ClientCertEntry{ID: "a", Host: "h", CertFile: abs, PFXFile: abs}, CodeClientCertSource},
		{ClientCertEntry{ID: "a", Host: "h", KeyFile: abs, PFXFile: abs}, CodeClientCertSource},
		{ClientCertEntry{ID: "a", Host: "h", CertFile: "rel.pem"}, CodeInvalidPath},
		{ClientCertEntry{ID: "a", Host: "h", PFXFile: "rel.p12"}, CodeInvalidPath},
	}
	for _, tt := range bad {
		err := ValidateClientCertEntry(tt.e)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != tt.code || e.Kind != errs.InvalidArgument {
			t.Errorf("%+v: %v, want %s", tt.e, err, tt.code)
		}
	}
}

// encryptedKeyFile writes the fixture's encrypted PKCS#8 DER key as an
// "ENCRYPTED PRIVATE KEY" PEM file.
func encryptedKeyFile(t *testing.T, name string) string {
	t.Helper()
	der, err := os.ReadFile(tlstest.Fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return tlstest.WriteFile(t, "key.pem", pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: der}))
}

func TestLoadClientCert(t *testing.T) {
	t.Parallel()
	rsaLeaf := tlstest.FixtureLeaf(t, "rsa.crt")
	ecLeaf := tlstest.FixtureLeaf(t, "ec.crt")

	good := []struct {
		name  string
		e     ClientCertEntry
		pass  string
		leaf  string
		chain int
	}{
		{"pem", ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "rsa.key")}, "", rsaLeaf.Subject.CommonName, 0},
		{"pem ed25519", ClientCertEntry{CertFile: tlstest.Fixture(t, "ed25519.crt"), KeyFile: tlstest.Fixture(t, "ed25519.key")}, "", "PostSonde Test Client ed25519", 0},
		{"encrypted pkcs8", ClientCertEntry{CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: encryptedKeyFile(t, "ec-pbes2-sha512prf.p8")}, tlstest.FixturePassword, ecLeaf.Subject.CommonName, 0},
		{"pkcs12 windows", ClientCertEntry{PFXFile: tlstest.Fixture(t, "windows-aes256-sha256.p12")}, tlstest.FixturePassword, rsaLeaf.Subject.CommonName, 2},
		{"pkcs12 macos legacy", ClientCertEntry{PFXFile: tlstest.Fixture(t, "macos-keychain.p12")}, tlstest.FixturePassword, rsaLeaf.Subject.CommonName, 2},
		{"pkcs12 ec", ClientCertEntry{PFXFile: tlstest.Fixture(t, "ec-aes128.p12")}, tlstest.FixturePassword, ecLeaf.Subject.CommonName, 2},
		{"pkcs12 empty password", ClientCertEntry{PFXFile: tlstest.Fixture(t, "empty-password.p12")}, "", rsaLeaf.Subject.CommonName, 2},
	}
	for _, tt := range good {
		tt.e.ID, tt.e.Host = "cc_1", "localhost"
		c, err := LoadClientCert(tt.e, tt.pass)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if c.Leaf.Subject.CommonName != tt.leaf || len(c.Chain) != tt.chain || len(c.Warnings) != 0 ||
			c.Certificate.PrivateKey == nil || len(c.Certificate.Certificate) != tt.chain+1 {
			t.Errorf("%s: leaf %q, chain %d, warnings %+v", tt.name, c.Leaf.Subject.CommonName, len(c.Chain), c.Warnings)
		}
		if d := c.Describe(); !strings.Contains(d, tt.leaf) || !strings.Contains(d, "localhost") {
			t.Errorf("%s: Describe = %q", tt.name, d)
		}
	}

	bad := []struct {
		name  string
		e     ClientCertEntry
		pass  string
		code  string
		field string
	}{
		{"wrong pkcs12 password", ClientCertEntry{PFXFile: tlstest.Fixture(t, "openssl-default.p12")}, "wrong", pkcs12.CodeWrongPassword, "pfxFile"},
		{"no pkcs12 password", ClientCertEntry{PFXFile: tlstest.Fixture(t, "openssl-default.p12")}, "", pkcs12.CodeWrongPassword, "pfxFile"},
		{"pkcs12 without key", ClientCertEntry{PFXFile: tlstest.Fixture(t, "certs-only.p12")}, tlstest.FixturePassword, pkcs12.CodeNoPrivateKey, "pfxFile"},
		{"pem as pkcs12", ClientCertEntry{PFXFile: tlstest.Fixture(t, "rsa.crt")}, "", pkcs12.CodeInvalid, "pfxFile"},
		{"mismatched key", ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "ec.key")}, "", CodeClientKeyMismatch, "keyFile"},
		{"no key", ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt")}, "", CodeClientKeyMissing, "keyFile"},
		{"encrypted key without passphrase", ClientCertEntry{CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: encryptedKeyFile(t, "ec-pbes2-sha512prf.p8")}, "", CodeClientKeyEncrypted, "keyFile"},
		{"encrypted key wrong passphrase", ClientCertEntry{CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: encryptedKeyFile(t, "ec-pbes2-sha512prf.p8")}, "nope", pkcs12.CodeWrongPassword, "keyFile"},
		{"missing file", ClientCertEntry{PFXFile: filepath.Join(t.TempDir(), "gone.p12")}, "", CodeFileUnreadable, "pfxFile"},
	}
	for _, tt := range bad {
		tt.e.ID, tt.e.Host = "cc_1", "localhost"
		_, err := LoadClientCert(tt.e, tt.pass)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != tt.code || e.Details["field"] != tt.field {
			t.Errorf("%s: %v (details %v), want %s field %s", tt.name, err, detailsOf(err), tt.code, tt.field)
		}
	}
}

func detailsOf(err error) map[string]string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Details
	}
	return nil
}

// TestLoadClientCertWarnings: an expired certificate or one not for
// client authentication loads with a warning.
func TestLoadClientCertWarnings(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Warn CA")
	expired := ca.Issue(t, tlstest.CertOptions{Client: true, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	server := ca.Issue(t, tlstest.CertOptions{})
	for _, tt := range []struct {
		cert *tlstest.Cert
		code string
	}{{expired, CodeClientCertExpired}, {server, CodeClientCertUsage}} {
		e := ClientCertEntry{ID: "w", Host: "h", CertFile: tlstest.WriteFile(t, "c.pem", tt.cert.CertPEM), KeyFile: tlstest.WriteFile(t, "k.pem", tt.cert.KeyPEM)}
		c, err := LoadClientCert(e, "")
		if err != nil || len(c.Warnings) != 1 || c.Warnings[0].Code != tt.code {
			t.Errorf("%s: %+v, %v", tt.code, c, err)
		}
	}
}

func TestClientCerts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	copyFixture := func(name string) string {
		data, err := os.ReadFile(tlstest.Fixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pfx := copyFixture("windows-aes256-sha256.p12")
	entries := []ClientCertEntry{
		{ID: "pfx", Host: "*.example.com", PFXFile: pfx},
		{ID: "pem", Host: "localhost:8443", CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: tlstest.Fixture(t, "ec.key")},
	}
	passphrases := map[string]string{"pfx": tlstest.FixturePassword}
	cc := NewClientCerts(func() []ClientCertEntry { return entries }, func(id string) (string, error) { return passphrases[id], nil })

	c1, err := cc.ForAddress("api.example.com:443")
	if err != nil || c1 == nil || c1.Entry.ID != "pfx" {
		t.Fatalf("ForAddress = %+v, %v", c1, err)
	}
	c2, err := cc.ForURL(&url.URL{Scheme: "https", Host: "web.example.com"})
	if err != nil || c2 == nil || c2.Certificate != c1.Certificate {
		t.Fatalf("second lookup = %+v, %v; want the cached certificate", c2, err)
	}
	if c, err := cc.ForURL(&url.URL{Scheme: "wss", Host: "localhost:8443"}); err != nil || c == nil || c.Entry.ID != "pem" {
		t.Fatalf("pem = %+v, %v", c, err)
	}
	if c, err := cc.ForAddress("localhost:443"); err != nil || c != nil {
		t.Fatalf("no match = %+v, %v", c, err)
	}

	// A changed file is read again.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(pfx, later, later); err != nil {
		t.Fatal(err)
	}
	c3, err := cc.ForAddress("api.example.com:443")
	if err != nil || c3.Certificate == c1.Certificate {
		t.Fatalf("after a change: %v, same pointer %v", err, c3 != nil && c3.Certificate == c1.Certificate)
	}

	// A wrong passphrase fails the connection with the settings hint.
	passphrases["pfx"] = "wrong"
	_, err = cc.ForAddress("api.example.com:443")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != pkcs12.CodeWrongPassword || e.Hint != HintClientCertFix ||
		e.Details["host"] != "*.example.com" || e.Details["clientCert"] != "pfx" {
		t.Fatalf("wrong passphrase: %v %+v", err, detailsOf(err))
	}
	// So does a moved file.
	passphrases["pfx"] = tlstest.FixturePassword
	if err := os.Remove(pfx); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.ForAddress("api.example.com:443"); !errors.As(err, &e) || e.Code != CodeFileUnreadable || e.Hint != HintClientCertFix {
		t.Fatalf("moved file: %v", err)
	}
	// And a secret storage that cannot be read.
	broken := NewClientCerts(func() []ClientCertEntry { return entries[1:] }, func(string) (string, error) { return "", errors.New("disk") })
	if _, err := broken.ForAddress("localhost:8443"); !errors.As(err, &e) || e.Code != CodePassphraseFailed {
		t.Fatalf("passphrase error: %v", err)
	}

	var none *ClientCerts
	if c, err := none.ForAddress("x:1"); c != nil || err != nil {
		t.Errorf("nil ClientCerts = %+v, %v", c, err)
	}
}
