package servers

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/servertls"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

func ptr[T any](v T) *T { return &v }

func newTLSService(t *testing.T, dataDir string, bundles []string, options ...Option) *ServersService {
	t.Helper()
	options = append(options,
		WithTLS(servertls.NewAuthority(dataDir), appsecrets.New(dataDir)),
		WithTrustedBundles(func() []string { return bundles }))
	svc := newService(nil, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second}, options...)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return svc
}

// TestGeneratedCertificateService: a server created with the generated
// certificate serves HTTPS that a client trusting LocalCA's ca.pem (the
// CA bundles setting) accepts; the DTO shows the certificate while it
// runs.
func TestGeneratedCertificateService(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	caPath := filepath.Join(dataDir, "servers", "tls", "ca.pem")
	svc := newTLSService(t, dataDir, []string{caPath})
	ctx := t.Context()

	srv, err := svc.Create(ctx, CreateInputDTO{Type: echo.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertGenerated, CertFile: "/ignored.pem"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.TLS == nil || srv.TLS.Certificate != core.CertGenerated || srv.TLS.CertFile != "" || srv.Certificate != nil {
		t.Fatalf("created: %+v %+v", srv.TLS, srv.Certificate)
	}
	srv, err = svc.Start(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Certificate == nil || !strings.HasPrefix(srv.Certificate.Subject, "CN=localhost,") || !strings.Contains(strings.Join(srv.Certificate.Names, " "), "::1") {
		t.Fatalf("running certificate = %+v", srv.Certificate)
	}

	ca, err := svc.LocalCA(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ca.Path != caPath || !ca.Trusted || !strings.HasPrefix(ca.PEM, "-----BEGIN CERTIFICATE-----") || len(ca.SHA256) != 64 {
		t.Fatalf("LocalCA = %+v", ca)
	}
	roots, err := tlsconf.NewTrust(func() []string { return []string{ca.Path} }).Roots()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	res, err := client.Get("https://localhost:" + strconv.Itoa(srv.BoundPort) + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status %d", res.StatusCode)
	}

	// TLS is part of the address: it cannot change while running.
	_, err = svc.UpdateServer(ctx, srv.ID, ServerSettingsDTO{Name: srv.Name, Host: srv.Host})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != core.CodeActive {
		t.Errorf("turning TLS off while running: %v", err)
	}

	untrusted := newTLSService(t, t.TempDir(), nil)
	if ca, err := untrusted.LocalCA(ctx); err != nil || ca.Trusted {
		t.Errorf("LocalCA without the bundle = %+v, %v", ca, err)
	}
}

// TestUserCertificateService: files are read when the server is saved; a
// wrong passphrase is refused and nothing is created; the passphrase goes
// to the local secret storage, never to a DTO; UpdateServer keeps it when
// none is given.
func TestUserCertificateService(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	pfx := tlstest.Fixture(t, "windows-aes256-sha256.p12")
	svc := newTLSService(t, dataDir, nil, WithAllowedDirs(func() []string { return []string{filepath.Dir(pfx)} }))
	ctx := t.Context()

	_, err := svc.Create(ctx, CreateInputDTO{Type: echo.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, PFXFile: pfx, Passphrase: ptr("wrong")}})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != pkcs12.CodeWrongPassword {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if list, _ := svc.List(ctx); len(list) != 0 {
		t.Fatalf("a server was created: %+v", list)
	}
	_, err = svc.Create(ctx, CreateInputDTO{Type: echo.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, CertFile: "relative.pem"}})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != tlsconf.CodeServerCertSource {
		t.Fatalf("relative path: %v", err)
	}

	srv, err := svc.Create(ctx, CreateInputDTO{Type: echo.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, PFXFile: pfx, Passphrase: ptr(tlstest.FixturePassword)}})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.TLS.HasPassphrase || srv.TLS.PFXFile != pfx {
		t.Fatalf("TLS = %+v", srv.TLS)
	}
	secrets, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(appsecrets.FileName)))
	if err != nil || !strings.Contains(string(secrets), tlstest.FixturePassword) || strings.Contains(string(secrets), pfx) {
		t.Fatalf("secrets file = %s, %v", secrets, err)
	}

	// Renaming keeps the stored passphrase (null), and the server starts
	// with it.
	srv, err = svc.UpdateServer(ctx, srv.ID, ServerSettingsDTO{Name: "Renamed", TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, PFXFile: pfx}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err = svc.Start(ctx, srv.ID)
	if err != nil || srv.Certificate == nil {
		t.Fatalf("start: %+v, %v", srv.Certificate, err)
	}
	conn, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(srv.BoundPort), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// TestSavedServerTLS: a saved server's TLS goes to its _folder.json with
// paths only; the passphrase stays in the data directory, and a restart
// brings both back.
func TestSavedServerTLS(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	ca := tlstest.NewCA(t, "Project CA")
	leaf := ca.Issue(t, tlstest.CertOptions{CommonName: "chat.local"})
	certFile := tlstest.WriteFile(t, "chat.pem", leaf.CertPEM)
	keyFile := tlstest.WriteFile(t, "chat-key.pem", leaf.KeyPEM)
	// The files are outside the project: their folders are allowed
	// (PRV-10).
	allowed := WithAllowedDirs(func() []string { return []string{filepath.Dir(certFile), filepath.Dir(keyFile)} })
	use := func() {
		_ = e.svc.ServiceShutdown()
		e.manager = coreworkspace.New(e.dataDir)
		e.collect = collection.New(e.manager, func(string, any) {}, slog.New(slog.DiscardHandler))
		e.svc = newTLSService(t, e.dataDir, nil, WithProject(CollectionProject(e.collect)), allowed)
		e.open(e.dirA)
	}
	use()
	e.sync()
	ctx := t.Context()

	_, err := e.svc.CreateFolderServer(ctx, FolderServerInputDTO{WorkspaceID: projectA, FolderID: "fld_chat", Server: CreateInputDTO{
		Type: "websocket", TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, CertFile: certFile, KeyFile: keyFile, Passphrase: ptr("unused secret")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file := readProjectFile(t, e.dirA, "chat/_folder.json")
	for _, want := range []string{`"tls": {`, `"certificate": "files"`, certFile, keyFile} {
		if !strings.Contains(file, want) {
			t.Errorf("folder file lacks %s:\n%s", want, file)
		}
	}
	if strings.Contains(file, "unused secret") || strings.Contains(strings.ToLower(file), "passphrase") {
		t.Errorf("the passphrase is in the project:\n%s", file)
	}

	use()
	e.sync()
	got, ok := e.folderServer("fld_chat")
	if !ok || got.TLS == nil || got.TLS.CertFile != certFile || !got.TLS.HasPassphrase || got.State != "stopped" {
		t.Fatalf("after restart: %+v %+v", got, got.TLS)
	}
	got, err = e.svc.Start(ctx, got.ID)
	if err != nil || got.Certificate == nil || !strings.Contains(got.Certificate.Subject, "chat.local") {
		t.Fatalf("start: %+v, %v", got.Certificate, err)
	}
	_, _ = e.svc.Stop(context.Background(), got.ID)
}

// TestPickTLSFile: the dialog of each kind, and an unknown kind.
func TestPickTLSFile(t *testing.T) {
	t.Parallel()
	var seen []FileDialog
	svc := newTLSService(t, "", nil, WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		seen = append(seen, d)
		return "/chosen", nil
	}))
	for _, kind := range []string{"cert", "key", "pfx"} {
		if p, err := svc.PickTLSFile(t.Context(), kind); err != nil || p != "/chosen" {
			t.Errorf("%s: %q, %v", kind, p, err)
		}
	}
	if len(seen) != 3 || !strings.Contains(seen[2].FilterPattern, "p12") {
		t.Errorf("dialogs = %+v", seen)
	}
	if _, err := svc.PickTLSFile(t.Context(), "der"); err == nil {
		t.Error("unknown kind accepted")
	}
	if err := svc.RevealLocalCA(t.Context()); err == nil {
		t.Error("reveal without a file manager or a file")
	}
}

// PRV-10: the certificate files of a server are read from the project
// folder or an allowed directory only. Saving a server with a file
// outside is refused with fileaccess.not_allowed, and so is starting one
// whose folder was removed from the allowlist since.
func TestServerCertFilesAllowlist(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Allowlist CA")
	leaf := ca.Issue(t, tlstest.CertOptions{CommonName: "localhost"})
	project, allowed, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{project, allowed, outside} {
		for name, data := range map[string][]byte{"cert.pem": leaf.CertPEM, "key.pem": leaf.KeyPEM} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(outside, "key.pem"), filepath.Join(project, "escape.pem")); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	dirs := []string{allowed}
	svc := newTLSService(t, t.TempDir(), nil, WithProjectRoot(func() string { return project }), WithAllowedDirs(func() []string { return dirs }))
	ctx := t.Context()
	create := func(cert, key string) (ServerDTO, error) {
		return svc.Create(ctx, CreateInputDTO{Type: echo.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertFiles, CertFile: cert, KeyFile: key}})
	}
	for _, tc := range []struct {
		name, cert, key, field string
	}{
		{"outside", filepath.Join(outside, "cert.pem"), filepath.Join(outside, "key.pem"), "certFile"},
		{"key outside", filepath.Join(project, "cert.pem"), filepath.Join(outside, "key.pem"), "keyFile"},
		{"link escaping", filepath.Join(project, "cert.pem"), filepath.Join(project, "escape.pem"), "keyFile"},
	} {
		_, err := create(tc.cert, tc.key)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != fileaccess.CodeNotAllowed || e.Details["field"] != tc.field ||
			!strings.HasPrefix(e.Message, "The server certificate file ") || e.Hint != fileaccess.HintAllow {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, err := create(filepath.Join(project, "cert.pem"), filepath.Join(project, "key.pem")); err != nil {
		t.Errorf("in the project: %v", err)
	}
	srv, err := create(filepath.Join(allowed, "cert.pem"), filepath.Join(allowed, "key.pem"))
	if err != nil {
		t.Fatalf("allowed directory: %v", err)
	}
	dirs = nil
	_, err = svc.Start(ctx, srv.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != fileaccess.CodeNotAllowed {
		t.Errorf("start after the folder was removed: %v", err)
	}
}
