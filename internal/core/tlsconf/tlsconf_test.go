package tlsconf

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

func wantCode(t *testing.T, err error, kind errs.Kind, code, field string) *errs.Error {
	t.Helper()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("error %v (%T) is not an *errs.Error; want %s", err, err, code)
	}
	if e.Code != code || e.Kind != kind || e.Details["field"] != field {
		t.Fatalf("error %q kind %v code %q field %q; want %v %q %q", e.Message, e.Kind, e.Code, e.Details["field"], kind, code, field)
	}
	return e
}

func TestDefaults(t *testing.T) {
	res, err := Build(Options{}, "")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.MinVersion != tls.VersionTLS12 || c.InsecureSkipVerify || c.RootCAs != nil || c.ServerName != "" ||
		c.GetClientCertificate != nil || len(c.Certificates) != 0 || len(res.Warnings) != 0 || res.ClientCert != nil {
		t.Fatalf("zero Options gave %+v, %+v", c, res)
	}
}

func TestOptions(t *testing.T) {
	res, err := Build(Options{MinVersion: "1.3", ServerName: "grpc.internal", InsecureSkipVerify: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.MinVersion != tls.VersionTLS13 || res.Config.ServerName != "grpc.internal" || !res.Config.InsecureSkipVerify {
		t.Fatalf("config = %+v", res.Config)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != CodeVerificationDisabled {
		t.Fatalf("warnings = %+v", res.Warnings)
	}

	for _, v := range []string{"1.1", "TLS1.3", "1.4"} {
		_, err := Build(Options{MinVersion: v}, "")
		wantCode(t, err, errs.InvalidArgument, CodeInvalidOption, "minVersion")
	}
	for _, n := range []string{"host:443", "https://host", "a b", "[::1]:1"} {
		_, err := Build(Options{ServerName: n}, "")
		wantCode(t, err, errs.InvalidArgument, CodeInvalidOption, "serverName")
	}
	for _, n := range []string{"localhost", "127.0.0.1", "::1", "a.b-c.example"} {
		if _, err := Build(Options{ServerName: n}, ""); err != nil {
			t.Errorf("server name %q: %v", n, err)
		}
	}
}

func TestCustomCAs(t *testing.T) {
	ca1 := tlstest.NewCA(t, "one")
	ca2 := tlstest.NewCA(t, "two")
	ca3 := tlstest.NewCA(t, "three")
	dir := t.TempDir()
	bundle := append(append([]byte("comment before\n"), ca1.CertPEM...), ca2.CertPEM...)
	if err := os.WriteFile(filepath.Join(dir, "bundle.pem"), bundle, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Build(Options{CAFiles: []string{"bundle.pem"}, CAPEM: string(ca3.CertPEM), ReplaceSystemRoots: true}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CACerts) != 3 || !res.CACerts[0].Equal(ca1.Cert) || !res.CACerts[2].Equal(ca3.Cert) {
		t.Fatalf("CA certs = %v", res.CACerts)
	}
	server := ca2.Issue(t, tlstest.CertOptions{})
	if _, err := server.Cert.Verify(x509.VerifyOptions{Roots: res.Config.RootCAs, DNSName: "localhost"}); err != nil {
		t.Fatalf("verify with the built pool: %v", err)
	}
	stranger := tlstest.NewCA(t, "stranger").Issue(t, tlstest.CertOptions{})
	if _, err := stranger.Cert.Verify(x509.VerifyOptions{Roots: res.Config.RootCAs, DNSName: "localhost"}); err == nil {
		t.Fatal("a certificate from an unknown CA verified with ReplaceSystemRoots")
	}

	// Added to the system roots.
	res, err = Build(Options{CAPEM: string(ca1.CertPEM)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.RootCAs == nil || len(res.CACerts) != 1 {
		t.Fatalf("pool %v, CACerts %v", res.Config.RootCAs, res.CACerts)
	}

	// Absolute paths ignore the base directory.
	if _, err := Build(Options{CAFiles: []string{filepath.Join(dir, "bundle.pem")}}, "/elsewhere"); err != nil {
		t.Fatal(err)
	}
}

func TestCAErrors(t *testing.T) {
	ca := tlstest.NewCA(t, "ca")
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keyOnly := write("key.pem", ca.Issue(t, tlstest.CertOptions{}).KeyPEM)
	der := write("ca.der", ca.Cert.Raw)
	big := write("big.pem", make([]byte, MaxFileSize+1))
	broken := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})

	tests := []struct {
		name  string
		o     Options
		base  string
		kind  errs.Kind
		code  string
		field string
	}{
		{"pem without certificates", Options{CAPEM: "hello"}, "", errs.Parse, CodeCANoCertificates, "caPem"},
		{"file with only a key", Options{CAFiles: []string{keyOnly}}, "", errs.Parse, CodeCANoCertificates, "caFiles[0]"},
		{"DER file", Options{CAFiles: []string{write("ok.pem", ca.CertPEM), der}}, "", errs.Parse, CodeCANoCertificates, "caFiles[1]"},
		{"broken certificate", Options{CAPEM: string(broken)}, "", errs.Parse, CodeCAInvalid, "caPem"},
		{"missing file", Options{CAFiles: []string{filepath.Join(dir, "nope.pem")}}, "", errs.NotFound, CodeFileUnreadable, "caFiles[0]"},
		{"relative without base", Options{CAFiles: []string{"ca.pem"}}, "", errs.InvalidArgument, CodeInvalidPath, "caFiles[0]"},
		{"empty path", Options{CAFiles: []string{" "}}, dir, errs.InvalidArgument, CodeInvalidPath, "caFiles[0]"},
		{"too large", Options{CAFiles: []string{big}}, "", errs.InvalidArgument, CodeFileTooLarge, "caFiles[0]"},
		{"directory", Options{CAFiles: []string{dir}}, "", errs.IO, CodeFileUnreadable, "caFiles[0]"},
		{"replace with nothing", Options{ReplaceSystemRoots: true}, "", errs.InvalidArgument, CodeCARequired, "replaceSystemRoots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.o, tt.base)
			e := wantCode(t, err, tt.kind, tt.code, tt.field)
			if e.Message == "" || strings.ToUpper(e.Message[:1]) != e.Message[:1] {
				t.Errorf("message %q", e.Message)
			}
		})
	}
	_, err := Build(Options{CAFiles: []string{der}}, "")
	if e, _ := errors.AsType[*errs.Error](err); e == nil || e.Hint != HintPEM || e.Details["path"] != der {
		t.Errorf("DER: %v", e)
	}
}

func TestUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("file modes")
	}
	p := tlstest.WriteFile(t, "ca.pem", tlstest.NewCA(t, "ca").CertPEM)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	_, err := Build(Options{CAFiles: []string{p}}, "")
	wantCode(t, err, errs.PermissionDenied, CodeFileUnreadable, "caFiles[0]")
}

func TestClientCert(t *testing.T) {
	ca := tlstest.NewCA(t, "clients")
	c := ca.Issue(t, tlstest.CertOptions{Client: true})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.crt"), c.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.key"), c.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	combined := append(append([]byte{}, c.CertPEM...), c.KeyPEM...)
	if err := os.WriteFile(filepath.Join(dir, "combined.pem"), combined, 0o600); err != nil {
		t.Fatal(err)
	}
	chain := append(append([]byte{}, c.CertPEM...), ca.CertPEM...)

	for name, o := range map[string]Options{
		"files":         {ClientCertFile: "c.crt", ClientKeyFile: "c.key"},
		"pem":           {ClientCertPEM: string(c.CertPEM), ClientKeyPEM: string(c.KeyPEM)},
		"combined file": {ClientCertFile: "combined.pem"},
		"combined pem":  {ClientCertPEM: string(combined)},
		"file and pem":  {ClientCertFile: "c.crt", ClientKeyPEM: string(c.KeyPEM)},
		"chain":         {ClientCertPEM: string(chain), ClientKeyFile: "c.key"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := Build(o, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !res.ClientCert.Equal(c.Cert) || len(res.Warnings) != 0 {
				t.Fatalf("client cert %v, warnings %+v", res.ClientCert, res.Warnings)
			}
			got, err := res.Config.GetClientCertificate(&tls.CertificateRequestInfo{})
			if err != nil || len(got.Certificate) == 0 || got.PrivateKey == nil {
				t.Fatalf("GetClientCertificate = %v, %v", got, err)
			}
			if name == "chain" && len(got.Certificate) != 2 {
				t.Fatalf("chain length %d", len(got.Certificate))
			}
		})
	}
}

func TestClientKeyFormats(t *testing.T) {
	ca := tlstest.NewCA(t, "clients")
	c := ca.Issue(t, tlstest.CertOptions{Client: true})

	// EC key in SEC 1 form.
	ecKey := c.TLS.PrivateKey.(*ecdsa.PrivateKey)
	sec1, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})
	if _, err := Build(Options{ClientCertPEM: string(c.CertPEM), ClientKeyPEM: string(ecPEM)}, ""); err != nil {
		t.Fatalf("SEC 1: %v", err)
	}

	// RSA key in PKCS#1 form, with a matching self-signed certificate.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: c.Cert.SerialNumber, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rsaKey.PublicKey, rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	rsaPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if _, err := Build(Options{ClientCertPEM: string(rsaCert), ClientKeyPEM: string(rsaPEM)}, ""); err != nil {
		t.Fatalf("PKCS#1: %v", err)
	}
}

func TestClientCertErrors(t *testing.T) {
	ca := tlstest.NewCA(t, "clients")
	c := ca.Issue(t, tlstest.CertOptions{Client: true})
	other := ca.Issue(t, tlstest.CertOptions{Client: true})
	dir := t.TempDir()
	certFile := filepath.Join(dir, "c.crt")
	if err := os.WriteFile(certFile, c.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	encrypted := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x00}})
	legacy := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00"}, Bytes: []byte{1}})
	badKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})
	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")})
	cert := string(c.CertPEM)

	tests := []struct {
		name  string
		o     Options
		kind  errs.Kind
		code  string
		field string
	}{
		{"key without cert", Options{ClientKeyPEM: string(c.KeyPEM)}, errs.InvalidArgument, CodeClientCertMissing, "clientCertFile"},
		{"cert without key", Options{ClientCertPEM: cert}, errs.InvalidArgument, CodeClientKeyMissing, "clientKeyFile"},
		{"cert file and pem", Options{ClientCertFile: certFile, ClientCertPEM: cert}, errs.InvalidArgument, CodeInvalidOption, "clientCertPem"},
		{"key file and pem", Options{ClientCertPEM: cert, ClientKeyFile: certFile, ClientKeyPEM: string(c.KeyPEM)}, errs.InvalidArgument, CodeInvalidOption, "clientKeyPem"},
		{"no pem cert", Options{ClientCertPEM: "MIIB...", ClientKeyPEM: string(c.KeyPEM)}, errs.Parse, CodeClientCertInvalid, "clientCertPem"},
		{"broken cert", Options{ClientCertPEM: string(badCert), ClientKeyPEM: string(c.KeyPEM)}, errs.Parse, CodeClientCertInvalid, "clientCertPem"},
		{"broken intermediate", Options{ClientCertPEM: cert + string(badCert), ClientKeyPEM: string(c.KeyPEM)}, errs.Parse, CodeClientCertInvalid, "clientCertPem"},
		{"key file holds a cert", Options{ClientCertPEM: cert, ClientKeyFile: certFile}, errs.Parse, CodeClientKeyInvalid, "clientKeyFile"},
		{"broken key", Options{ClientCertPEM: cert, ClientKeyPEM: string(badKey)}, errs.Parse, CodeClientKeyInvalid, "clientKeyPem"},
		{"encrypted pkcs8", Options{ClientCertPEM: cert, ClientKeyPEM: string(encrypted)}, errs.Unsupported, CodeClientKeyEncrypted, "clientKeyPem"},
		{"encrypted legacy", Options{ClientCertFile: certFile, ClientKeyPEM: string(legacy)}, errs.Unsupported, CodeClientKeyEncrypted, "clientKeyPem"},
		{"mismatch", Options{ClientCertPEM: cert, ClientKeyPEM: string(other.KeyPEM)}, errs.InvalidArgument, CodeClientKeyMismatch, "clientKeyPem"},
		{"missing key file", Options{ClientCertPEM: cert, ClientKeyFile: filepath.Join(dir, "nope.key")}, errs.NotFound, CodeFileUnreadable, "clientKeyFile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.o, "")
			e := wantCode(t, err, tt.kind, tt.code, tt.field)
			// No error text may contain key material.
			for _, secret := range []string{string(c.KeyPEM), "junk"} {
				if strings.Contains(e.Error(), secret) {
					t.Fatalf("error contains key material: %v", e)
				}
			}
		})
	}
	_, err := Build(Options{ClientCertPEM: cert, ClientKeyPEM: string(encrypted)}, "")
	if e, _ := errors.AsType[*errs.Error](err); e.Hint != HintEncrypted {
		t.Errorf("encrypted hint = %q", e.Hint)
	}
}

func TestClientCertWarnings(t *testing.T) {
	ca := tlstest.NewCA(t, "clients")
	now := time.Now()
	expired := ca.Issue(t, tlstest.CertOptions{Client: true, NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-24 * time.Hour)})
	future := ca.Issue(t, tlstest.CertOptions{Client: true, NotBefore: now.Add(24 * time.Hour), NotAfter: now.Add(48 * time.Hour)})
	server := ca.Issue(t, tlstest.CertOptions{})

	for _, tt := range []struct {
		cert  *tlstest.Cert
		codes []string
	}{
		{expired, []string{CodeClientCertExpired}},
		{future, []string{CodeClientCertNotYet}},
		{server, []string{CodeClientCertUsage}},
	} {
		res, err := build(Options{ClientCertPEM: string(tt.cert.CertPEM), ClientKeyPEM: string(tt.cert.KeyPEM)}, "", now)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, w := range res.Warnings {
			got = append(got, w.Code)
			if w.Message == "" || w.Hint == "" {
				t.Errorf("warning %+v", w)
			}
		}
		if strings.Join(got, ",") != strings.Join(tt.codes, ",") {
			t.Errorf("%s: warnings %v, want %v", tt.cert.Cert.Subject.CommonName, got, tt.codes)
		}
	}
}

func TestAlwaysSendsConfiguredCert(t *testing.T) {
	// The certificate is returned even when the server lists other CAs.
	c := tlstest.NewCA(t, "clients").Issue(t, tlstest.CertOptions{Client: true})
	res, err := Build(Options{ClientCertPEM: string(c.CertPEM), ClientKeyPEM: string(c.KeyPEM)}, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := res.Config.GetClientCertificate(&tls.CertificateRequestInfo{AcceptableCAs: [][]byte{[]byte("other")}})
	if err != nil || got.Leaf == nil || !got.Leaf.Equal(c.Cert) {
		t.Fatalf("GetClientCertificate = %v, %v", got, err)
	}
}

func TestJSON(t *testing.T) {
	// Key material never serializes; everything else round-trips.
	o := Options{
		InsecureSkipVerify: true, ServerName: "s", CAFiles: []string{"ca.pem"}, CAPEM: "ca",
		ReplaceSystemRoots: true, ClientCertFile: "c.crt", ClientCertPEM: "cert",
		ClientKeyFile: "c.key", ClientKeyPEM: "SECRET", MinVersion: "1.3",
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("key PEM serialized: %s", b)
	}
	var back Options
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	o.ClientKeyPEM = ""
	if back.ServerName != o.ServerName || back.CAPEM != o.CAPEM || back.ClientKeyFile != o.ClientKeyFile || back.MinVersion != o.MinVersion || !back.ReplaceSystemRoots {
		t.Fatalf("round trip %+v", back)
	}
	if b, _ := json.Marshal(Options{}); string(b) != "{}" {
		t.Fatalf("zero Options = %s", b)
	}
}
