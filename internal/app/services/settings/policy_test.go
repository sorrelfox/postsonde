package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// policyService is a service over a settings file and an admin policy,
// both in temp directories (never the real system location).
func policyService(t *testing.T, policy, content string, opts ...Option) (*SettingsService, *recorder, string) {
	t.Helper()
	dir := t.TempDir()
	policyPath := filepath.Join(dir, core.PolicyFileName)
	if err := os.WriteFile(policyPath, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, core.FileName)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	store := core.Open(path, nil, core.WithPolicy(core.LoadPolicy(policyPath)))
	return New(store, rec.emit, nil, opts...), rec, path
}

func TestPolicyLocksSettingsInTheService(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, rec, path := policyService(t,
		`{"version": 1, "requireTlsVerification": true, "locked": {"ui.theme": "dark", "network.nope": 1}}`,
		`{"version": 1, "ui": {"theme": "light"}, "network": {"verifyTls": false}}`)

	got, err := svc.Get(ctx)
	if err != nil || got.UI.Theme != core.ThemeDark || !got.Network.VerifyTLS {
		t.Fatalf("Get = %+v, %v; want the locked theme and verification", got, err)
	}

	pol, err := svc.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pol.Active || !pol.Exists || !pol.RequireTLSVerification || !slices.Equal(pol.Locked, []string{"network.verifyTls", "ui.theme"}) {
		t.Errorf("Policy = %+v", pol)
	}
	if len(pol.Problems) != 1 || pol.Problems[0].Code != core.CodePolicyInvalid || pol.Problems[0].Details["field"] != "network.nope" {
		t.Errorf("Policy problems = %+v", pol.Problems)
	}

	for name, patch := range map[string]SettingsPatchDTO{
		"theme":     {UI: &UISettingsPatchDTO{Theme: ptr(core.ThemeLight)}},
		"verifyTls": {Network: &NetworkSettingsPatchDTO{VerifyTLS: ptr(false)}},
	} {
		_, err := svc.Update(ctx, patch)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != core.CodeLocked || e.Kind != errs.PermissionDenied {
			t.Errorf("%s: Update err = %v, want %s", name, err, core.CodeLocked)
		}
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("refused updates emitted %d events", n)
	}

	// Other settings still change; the file keeps the user's own theme.
	next, err := svc.Update(ctx, SettingsPatchDTO{Editor: &EditorSettingsPatchDTO{TabSize: ptr(4)}})
	if err != nil || next.Editor.TabSize != 4 || next.UI.Theme != core.ThemeDark {
		t.Fatalf("Update = %+v, %v", next, err)
	}
	if loaded, _, _ := core.Load(path); loaded.UI.Theme != core.ThemeLight || loaded.Network.VerifyTLS {
		t.Errorf("file = %+v, want the user's own locked values kept", loaded.UI)
	}
}

func TestPolicyRefusesLockedListsBeforeTheDialog(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	picked := false
	pick := func(context.Context, FileDialog) (string, error) {
		picked = true
		return "", nil
	}
	svc, _, _ := policyService(t,
		`{"locked": {"network.caBundles": [], "privacy.allowedDirs": [], "network.clientCerts": []}}`, "",
		WithFilePicker(pick))

	check := func(name string, err error) {
		t.Helper()
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != core.CodeLocked {
			t.Errorf("%s: err = %v, want %s", name, err, core.CodeLocked)
		}
	}
	_, err := svc.AddCABundle(ctx)
	check("AddCABundle", err)
	_, err = svc.AddAllowedDir(ctx)
	check("AddAllowedDir", err)
	_, err = svc.AddClientCert(ctx, ClientCertInputDTO{Host: "example.com", CertFile: "/nope.pem"})
	check("AddClientCert", err)
	if picked {
		t.Error("a dialog was shown for a locked setting")
	}
}

func TestPolicyWithoutFileIsInactive(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t, "")
	pol, err := svc.Policy(t.Context())
	if err != nil || pol.Active || pol.Exists || pol.Locked == nil || len(pol.Locked) != 0 || pol.Problems == nil {
		t.Errorf("Policy = %+v, %v; want an inactive policy with empty lists", pol, err)
	}
}
