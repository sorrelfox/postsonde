package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestClientCerts (AUTH-30): AddClientCert checks the files (decode, key
// matches, passphrase) before saving; the passphrase goes to the local
// secret storage, never to settings.json; UpdateClientCert keeps or
// replaces it; RemoveClientCert drops both.
func TestClientCerts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "config", core.FileName)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	secrets := appsecrets.New(filepath.Join(dir, "data"))
	rec := &recorder{}
	svc := New(core.Open(settingsPath, nil), rec.emit, nil, WithSecrets(secrets))
	ctx := t.Context()
	pfx := tlstest.Fixture(t, "windows-aes256-sha256.p12")

	// Refused, nothing saved: a bad host, a wrong passphrase, a key that
	// is not the certificate's.
	var e *errs.Error
	if _, err := svc.AddClientCert(ctx, ClientCertInputDTO{Host: "https://api.example.com", PFXFile: pfx}); !errors.As(err, &e) || e.Code != tlsconf.CodeHostPatternInvalid {
		t.Fatalf("bad host: %v", err)
	}
	if _, err := svc.AddClientCert(ctx, ClientCertInputDTO{Host: "api.example.com", PFXFile: pfx, Passphrase: ptr("wrong")}); !errors.As(err, &e) || e.Code != pkcs12.CodeWrongPassword {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := svc.AddClientCert(ctx, ClientCertInputDTO{Host: "api.example.com", CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "ec.key")}); !errors.As(err, &e) || e.Code != tlsconf.CodeClientKeyMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	if got, _ := svc.ClientCerts(ctx); len(got) != 0 {
		t.Fatalf("saved after errors: %+v", got)
	}

	got, err := svc.AddClientCert(ctx, ClientCertInputDTO{Host: " *.example.com ", PFXFile: pfx, Passphrase: ptr(tlstest.FixturePassword)})
	if err != nil || len(got) != 1 {
		t.Fatalf("add pkcs12: %+v, %v", got, err)
	}
	p := got[0]
	if p.Host != "*.example.com" || p.Kind != ClientCertPKCS12 || !p.HasPassphrase || p.Error != nil || p.ChainLength != 2 ||
		!strings.Contains(p.Subject, "PostSonde Test Client rsa") || p.Expired || len(p.SHA256) != 64 {
		t.Fatalf("pkcs12 entry: %+v", p)
	}
	// The same host pattern twice is refused.
	if _, err := svc.AddClientCert(ctx, ClientCertInputDTO{Host: "*.EXAMPLE.com", CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: tlstest.Fixture(t, "ec.key")}); !errors.As(err, &e) || e.Code != CodeClientCertDuplicate {
		t.Fatalf("duplicate: %v", err)
	}

	// An expired certificate is added, with a warning.
	ca := tlstest.NewCA(t, "Old CA")
	old := ca.Issue(t, tlstest.CertOptions{Client: true, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	got, err = svc.AddClientCert(ctx, ClientCertInputDTO{Host: "localhost:8443",
		CertFile: tlstest.WriteFile(t, "old.crt", old.CertPEM), KeyFile: tlstest.WriteFile(t, "old.key", old.KeyPEM)})
	if err != nil || len(got) != 2 || !got[1].Expired || len(got[1].Warnings) != 1 || got[1].Warnings[0].Code != tlsconf.CodeClientCertExpired || got[1].HasPassphrase {
		t.Fatalf("expired: %+v, %v", got, err)
	}

	// settings.json has the paths, never the passphrase; the secret
	// storage has it.
	data, err := os.ReadFile(settingsPath)
	if err != nil || !strings.Contains(string(data), `"clientCerts"`) || strings.Contains(string(data), tlstest.FixturePassword) {
		t.Fatalf("settings.json = %s, %v", data, err)
	}
	if v, err := secrets.Get(appsecrets.ClientCertPassphraseKey(p.ID)); err != nil || v != tlstest.FixturePassword {
		t.Fatalf("stored passphrase = %q, %v", v, err)
	}
	if ev := rec.all(); len(ev) != 2 || ev[0].Source != SourceApp {
		t.Errorf("events = %+v", ev)
	}

	// Update with a null passphrase keeps the stored one.
	got, err = svc.UpdateClientCert(ctx, p.ID, ClientCertInputDTO{Host: "api.example.com", PFXFile: pfx})
	if err != nil || got[0].Host != "api.example.com" || !got[0].HasPassphrase || got[0].Error != nil {
		t.Fatalf("update keeping the passphrase: %+v, %v", got, err)
	}
	// A wrong new passphrase is refused and the old one kept.
	if _, err := svc.UpdateClientCert(ctx, p.ID, ClientCertInputDTO{Host: "api.example.com", PFXFile: pfx, Passphrase: ptr("nope")}); !errors.As(err, &e) || e.Code != pkcs12.CodeWrongPassword {
		t.Fatalf("update wrong passphrase: %v", err)
	}
	if v, _ := secrets.Get(appsecrets.ClientCertPassphraseKey(p.ID)); v != tlstest.FixturePassword {
		t.Fatalf("passphrase after a refused update = %q", v)
	}
	// Switching to PEM with "" drops the passphrase.
	got, err = svc.UpdateClientCert(ctx, p.ID, ClientCertInputDTO{Host: "api.example.com", CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: tlstest.Fixture(t, "ec.key"), Passphrase: ptr("")})
	if err != nil || got[0].Kind != ClientCertPEM || got[0].HasPassphrase || got[0].PFXFile != "" {
		t.Fatalf("update to pem: %+v, %v", got, err)
	}
	if _, err := svc.UpdateClientCert(ctx, "cc_missing", ClientCertInputDTO{Host: "x", PFXFile: pfx}); !errors.As(err, &e) || e.Code != CodeClientCertNotFound {
		t.Fatalf("update unknown: %v", err)
	}

	// A certificate that breaks is reported, not dropped.
	if err := os.Remove(got[1].CertFile); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.ClientCerts(ctx)
	if len(got) != 2 || got[1].Error == nil || got[1].Error.Code != tlsconf.CodeFileUnreadable {
		t.Fatalf("broken: %+v", got)
	}

	// Remove drops the entry and its passphrase.
	if err := secrets.Set(appsecrets.ClientCertPassphraseKey(got[1].ID), "x"); err != nil {
		t.Fatal(err)
	}
	got, err = svc.RemoveClientCert(ctx, got[1].ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("remove: %+v, %v", got, err)
	}
	if got, err = svc.RemoveClientCert(ctx, "cc_unknown"); err != nil || len(got) != 1 {
		t.Fatalf("remove unknown: %+v, %v", got, err)
	}
	if _, err := svc.RemoveClientCert(ctx, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secrets.Path()); !os.IsNotExist(err) {
		t.Errorf("secret file left: %v", err)
	}
	if loaded, _, _ := core.Load(settingsPath); loaded.Network.ClientCerts != nil {
		t.Errorf("file still has %+v", loaded.Network.ClientCerts)
	}
}

func TestPickClientCertFile(t *testing.T) {
	t.Parallel()
	var dialogs []FileDialog
	svc := New(core.Open("", nil), nil, nil, WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		dialogs = append(dialogs, d)
		return "/chosen/file", nil
	}))
	for _, kind := range []string{"cert", "key", "pfx"} {
		if p, err := svc.PickClientCertFile(t.Context(), kind); err != nil || p != "/chosen/file" {
			t.Fatalf("%s: %q, %v", kind, p, err)
		}
	}
	if len(dialogs) != 3 || !strings.Contains(dialogs[2].FilterPattern, "*.p12") {
		t.Errorf("dialogs = %+v", dialogs)
	}
	var e *errs.Error
	if _, err := svc.PickClientCertFile(t.Context(), "other"); !errors.As(err, &e) || e.Code != CodeClientCertKind {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, err := New(core.Open("", nil), nil, nil).PickClientCertFile(t.Context(), "cert"); !errors.As(err, &e) || e.Code != CodePickUnavailable {
		t.Fatalf("no picker: %v", err)
	}
}
