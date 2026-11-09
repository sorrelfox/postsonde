package servertls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
)

func verify(t *testing.T, ca *x509.Certificate, cert *tls.Certificate, host string) error {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	_, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// TestAuthorityCreatedOnceAndKept: the CA is made on first use, stored
// with a private key file only the user can read, and the same CA comes
// back from a new Authority on the same data directory.
func TestAuthorityCreatedOnceAndKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := os.Stat(filepath.Join(dir, "servers")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("something was created before use")
	}
	a := NewAuthority(dir)
	ca, err := a.CA()
	if err != nil {
		t.Fatal(err)
	}
	if ca.Path != filepath.Join(dir, "servers", "tls", "ca.pem") || !ca.Cert.IsCA || !strings.HasPrefix(ca.Cert.Subject.CommonName, "PostSonde local CA") {
		t.Fatalf("CA = %s %v %q", ca.Path, ca.Cert.IsCA, ca.Cert.Subject.CommonName)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "servers", "tls", "ca-key.pem"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("key file = %v, %v", info, err)
		}
		info, err = os.Stat(filepath.Join(dir, "servers", "tls"))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("directory = %v, %v", info, err)
		}
	}
	data, err := os.ReadFile(ca.Path)
	if err != nil || string(data) != string(ca.PEM) || strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatalf("ca.pem = %q, %v", data, err)
	}

	again, err := NewAuthority(dir).CA()
	if err != nil {
		t.Fatal(err)
	}
	if again.SHA256() != ca.SHA256() {
		t.Error("a second Authority made a new CA")
	}
	cert, _, err := NewAuthority(dir).Issue(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(t, ca.Cert, cert, "localhost"); err != nil {
		t.Errorf("leaf of the reloaded CA: %v", err)
	}
}

// TestIssue: a leaf covers localhost and the loopback addresses, and the
// private addresses asked for; a public address is left out, and the name
// constraints keep the CA from signing for other names.
func TestIssue(t *testing.T) {
	t.Parallel()
	a := NewAuthority("")
	ca, err := a.CA()
	if err != nil {
		t.Fatal(err)
	}
	if ca.Path != "" {
		t.Errorf("in-memory CA path = %q", ca.Path)
	}
	lan, public := netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("8.8.8.8")
	cert, skipped, err := a.Issue([]netip.Addr{lan, public, netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(skipped, []netip.Addr{public}) {
		t.Errorf("skipped = %v", skipped)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "192.168.1.20"} {
		if err := verify(t, ca.Cert, cert, host); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if err := verify(t, ca.Cert, cert, "8.8.8.8"); err == nil {
		t.Error("the leaf is valid for a public address")
	}
	if len(cert.Certificate) != 2 || cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore) > LeafValidity+2*time.Hour {
		t.Errorf("chain %d, validity %v", len(cert.Certificate), cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore))
	}
	if !ca.Cert.PermittedDNSDomainsCritical || !slices.Equal(ca.Cert.PermittedDNSDomains, []string{"localhost"}) || len(ca.Cert.PermittedIPRanges) == 0 {
		t.Errorf("name constraints = %v %v", ca.Cert.PermittedDNSDomains, ca.Cert.PermittedIPRanges)
	}
	if !Permitted(netip.MustParseAddr("::ffff:10.0.0.1")) || Permitted(netip.MustParseAddr("2001:db8::1")) {
		t.Error("Permitted")
	}
}

// TestBrokenCA: a CA file that does not parse is an error naming the
// folder, and nothing overwrites it.
func TestBrokenCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := NewAuthority(dir).CA(); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "servers", "tls", "ca.pem")
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewAuthority(dir).CA()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != CodeCAInvalid || !strings.Contains(e.Hint, filepath.Join(dir, "servers", "tls")) {
		t.Fatalf("err = %#v", err)
	}
	if data, _ := os.ReadFile(certPath); string(data) != "not a certificate" {
		t.Error("the broken file was overwritten")
	}
}

// TestRenewal: a CA close to its end is replaced.
func TestRenewal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewAuthority(dir)
	old, err := a.CA()
	if err != nil {
		t.Fatal(err)
	}
	later := NewAuthority(dir)
	later.now = func() time.Time { return old.Cert.NotAfter.Add(-24 * time.Hour) }
	renewed, err := later.CA()
	if err != nil {
		t.Fatal(err)
	}
	if renewed.SHA256() == old.SHA256() {
		t.Error("the expiring CA was kept")
	}
}

// TestProviderGenerated: the provider issues for the server's address and
// warns about a public one.
func TestProviderGenerated(t *testing.T) {
	t.Parallel()
	p := &Provider{CA: NewAuthority(""), Interfaces: func() []netip.Addr {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("2001:db8::5")}
	}}
	c, err := p.Certificate(context.Background(), servers.Spec{Host: "0.0.0.0", TLS: &servers.TLS{Certificate: servers.CertGenerated}})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Generated || len(c.Warnings) != 0 || !slices.ContainsFunc(c.Certificate.Leaf.IPAddresses, func(ip net.IP) bool { return ip.Equal(net.ParseIP("10.1.2.3")) }) {
		t.Errorf("all interfaces: %+v, IPs %v", c, c.Certificate.Leaf.IPAddresses)
	}
	c, err = p.Certificate(context.Background(), servers.Spec{Host: "203.0.113.9", TLS: &servers.TLS{Certificate: servers.CertGenerated}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || c.Warnings[0].Code != CodeUnreachableAddrs || !strings.Contains(c.Warnings[0].Message, "203.0.113.9") {
		t.Errorf("warnings = %+v", c.Warnings)
	}
}

func TestSecretFile(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   servers.TLS
		want string
	}{
		{servers.TLS{PFXFile: "/a.p12"}, "/a.p12"},
		{servers.TLS{CertFile: "/c.pem", KeyFile: "/k.pem"}, "/k.pem"},
		{servers.TLS{CertFile: "/both.pem"}, "/both.pem"},
	} {
		if got := SecretFile(tt.in); got != tt.want {
			t.Errorf("SecretFile(%+v) = %q", tt.in, got)
		}
	}
}
