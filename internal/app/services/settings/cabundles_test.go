package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestCABundles: AddCABundle validates the chosen file and saves it in
// network.caBundles; RemoveCABundle takes it out; CABundles reports each
// file's certificates, warnings and, once it breaks, its error.
func TestCABundles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, core.FileName)
	good := tlstest.WriteFile(t, "corp.pem", tlstest.NewCA(t, "Corp Root").CertPEM)
	old := tlstest.NewCAValid(t, "Old Root", time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	expired := tlstest.WriteFile(t, "old.pem", old.CertPEM)
	notPEM := tlstest.WriteFile(t, "ca.der", old.Cert.Raw)

	var next string // what the dialog returns
	var dialogs []FileDialog
	rec := &recorder{}
	svc := New(core.Open(settingsPath, nil), rec.emit, nil, WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		dialogs = append(dialogs, d)
		return next, nil
	}))
	ctx := t.Context()

	// Cancelling changes nothing.
	got, err := svc.AddCABundle(ctx)
	if err != nil || len(got) != 0 || len(dialogs) != 1 || dialogs[0].FilterPattern == "" {
		t.Fatalf("cancel: %+v, %v, dialogs %+v", got, err, dialogs)
	}

	// A file that is not PEM is refused.
	next = notPEM
	_, err = svc.AddCABundle(ctx)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != tlsconf.CodeCANoCertificates || e.Hint == "" {
		t.Fatalf("not PEM: %v", err)
	}

	next = good
	got, err = svc.AddCABundle(ctx)
	if err != nil || len(got) != 1 || got[0].Path != good || len(got[0].Certificates) != 1 || got[0].Error != nil ||
		!strings.Contains(got[0].Certificates[0].Subject, "Corp Root") || !got[0].Certificates[0].IsCA {
		t.Fatalf("add: %+v, %v", got, err)
	}
	// The same file again: no duplicate.
	if got, err = svc.AddCABundle(ctx); err != nil || len(got) != 1 {
		t.Fatalf("add twice: %+v, %v", got, err)
	}

	// An expired CA is added with a warning.
	next = expired
	got, err = svc.AddCABundle(ctx)
	if err != nil || len(got) != 2 || !got[1].Certificates[0].Expired || len(got[1].Warnings) != 1 || got[1].Warnings[0].Code != tlsconf.CodeCAExpired {
		t.Fatalf("expired: %+v, %v", got, err)
	}

	// Saved to the file, and announced.
	loaded, _, err := core.Load(settingsPath)
	if err != nil || !slices.Equal(loaded.Network.CABundles, []string{good, expired}) {
		t.Fatalf("file has %v, %v", loaded.Network.CABundles, err)
	}
	if ev := rec.all(); len(ev) != 2 || ev[1].Source != SourceApp {
		t.Errorf("events = %+v", ev)
	}

	// A bundle that breaks is reported, not dropped.
	if err := os.Remove(good); err != nil {
		t.Fatal(err)
	}
	got, err = svc.CABundles(ctx)
	if err != nil || len(got) != 2 || got[0].Error == nil || got[0].Error.Code != tlsconf.CodeFileUnreadable || len(got[0].Certificates) != 0 {
		t.Fatalf("broken: %+v, %v", got, err)
	}

	got, err = svc.RemoveCABundle(ctx, good)
	if err != nil || len(got) != 1 || got[0].Path != expired {
		t.Fatalf("remove: %+v, %v", got, err)
	}
	if got, err = svc.RemoveCABundle(ctx, "/not/there.pem"); err != nil || len(got) != 1 {
		t.Fatalf("remove unknown: %+v, %v", got, err)
	}
	if got, err = svc.RemoveCABundle(ctx, expired); err != nil || len(got) != 0 {
		t.Fatalf("remove last: %+v, %v", got, err)
	}
	if loaded, _, _ := core.Load(settingsPath); loaded.Network.CABundles != nil {
		t.Errorf("file still has %v", loaded.Network.CABundles)
	}
}

func TestAddCABundleWithoutPicker(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t, "")
	_, err := svc.AddCABundle(t.Context())
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodePickUnavailable {
		t.Fatalf("err = %v", err)
	}
	failing := New(core.Open("", nil), nil, nil, WithFilePicker(func(context.Context, FileDialog) (string, error) {
		return "", errors.New("no window")
	}))
	if _, err := failing.AddCABundle(t.Context()); !errors.As(err, &e) || e.Code != CodePickUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestTrustCABundle: a CA file known without a dialog (the local servers'
// CA) is added once, validated like a chosen one.
func TestTrustCABundle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	svc := New(core.Open(filepath.Join(dir, core.FileName), nil), (&recorder{}).emit, nil)
	ca := tlstest.WriteFile(t, "ca.pem", tlstest.NewCA(t, "PostSonde local CA").CertPEM)
	for range 2 {
		got, err := svc.TrustCABundle(t.Context(), ca)
		if err != nil || len(got) != 1 || got[0].Path != ca {
			t.Fatalf("trust: %+v, %v", got, err)
		}
	}
	if _, err := svc.TrustCABundle(t.Context(), filepath.Join(dir, "gone.pem")); err == nil {
		t.Error("a missing file was trusted")
	}
}
