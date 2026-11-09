package tlsconf

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestLoadServerCert: PEM files (with an encrypted key), a key inside the
// certificate file and PKCS#12 load; errors keep their codes and fields
// and speak of a server certificate.
func TestLoadServerCert(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Server CA")
	leaf := ca.Issue(t, tlstest.CertOptions{CommonName: "dev.local"})
	certFile := tlstest.WriteFile(t, "cert.pem", leaf.CertPEM)
	keyFile := tlstest.WriteFile(t, "key.pem", leaf.KeyPEM)
	both := tlstest.WriteFile(t, "both.pem", append(append([]byte{}, leaf.CertPEM...), leaf.KeyPEM...))

	for _, tt := range []struct {
		name                string
		cert, key, pfx, pwd string
		cn                  string
		warnings            int
	}{
		{"pem", certFile, keyFile, "", "", "dev.local", 0},
		{"key in the certificate file", both, "", "", "", "dev.local", 0},
		{"encrypted key", tlstest.Fixture(t, "ec.crt"), encryptedKeyFile(t, "ec-pbes2-sha512prf.p8"), "", tlstest.FixturePassword, "", 1},
		{"pkcs12", "", "", tlstest.Fixture(t, "windows-aes256-sha256.p12"), tlstest.FixturePassword, "", 1},
	} {
		c, err := LoadServerCert(tt.cert, tt.key, tt.pfx, tt.pwd)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if (tt.cn != "" && c.Leaf.Subject.CommonName != tt.cn) || len(c.Warnings) != tt.warnings || c.Certificate.PrivateKey == nil {
			t.Errorf("%s: %q, warnings %+v", tt.name, c.Leaf.Subject.CommonName, c.Warnings)
		}
		// The fixtures are client certificates.
		if tt.warnings == 1 && c.Warnings[0].Code != CodeServerCertUsage {
			t.Errorf("%s: warning %+v", tt.name, c.Warnings[0])
		}
	}

	for _, tt := range []struct {
		name                string
		cert, key, pfx, pwd string
		code, field         string
	}{
		{"wrong pkcs12 passphrase", "", "", tlstest.Fixture(t, "openssl-default.p12"), "wrong", pkcs12.CodeWrongPassword, "pfxFile"},
		{"mismatched key", certFile, tlstest.Fixture(t, "ec.key"), "", "", CodeClientKeyMismatch, "keyFile"},
		{"no key", certFile, "", "", "", CodeClientKeyMissing, "keyFile"},
		{"both kinds", certFile, keyFile, tlstest.Fixture(t, "openssl-default.p12"), "", CodeServerCertSource, "pfxFile"},
		{"nothing", "", "", "", "", CodeServerCertSource, "certFile"},
		{"relative", "cert.pem", "", "", "", CodeServerCertSource, "certFile"},
		{"missing", certFile + ".gone", keyFile, "", "", CodeFileUnreadable, "certFile"},
	} {
		_, err := LoadServerCert(tt.cert, tt.key, tt.pfx, tt.pwd)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != tt.code || e.Details["field"] != tt.field {
			t.Errorf("%s: %v (details %v), want %s field %s", tt.name, err, detailsOf(err), tt.code, tt.field)
			continue
		}
		if strings.Contains(e.Message+e.Hint, "client") {
			t.Errorf("%s: %q / %q speaks of a client certificate", tt.name, e.Message, e.Hint)
		}
	}
}

func TestLoadServerCertExpired(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Old CA")
	old := ca.Issue(t, tlstest.CertOptions{NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	c, err := LoadServerCert(tlstest.WriteFile(t, "c.pem", old.CertPEM), tlstest.WriteFile(t, "k.pem", old.KeyPEM), "", "")
	if err != nil || len(c.Warnings) != 1 || c.Warnings[0].Code != CodeServerCertExpired {
		t.Errorf("%+v, %v", c, err)
	}
}
