package settings

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// writePolicy writes a policy file under a temp directory, never the real
// system location, and returns its path.
func writePolicy(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), PolicyFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func problemFields(p Policy) []string {
	var out []string
	for _, e := range p.Problems {
		if e.Code != CodePolicyInvalid {
			panic("unexpected code " + e.Code)
		}
		out = append(out, e.Details["field"])
	}
	return out
}

func TestLoadPolicyMissingIsNoPolicy(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", filepath.Join(t.TempDir(), PolicyFileName)} {
		p := LoadPolicy(path)
		if p.Active() || p.Exists || len(p.Problems) != 0 || p.IsLocked(KeyVerifyTLS) {
			t.Errorf("LoadPolicy(%q) = %+v, want no policy", path, p)
		}
		if got := p.Enforce(Defaults()); !got.Equal(Defaults()) {
			t.Errorf("Enforce changed the settings: %+v", got)
		}
	}
}

func TestLoadPolicyLocksAndSkipsBadEntries(t *testing.T) {
	t.Parallel()
	corp := abs(t, "corp.pem")
	p := LoadPolicy(writePolicy(t, `{
		"version": 1,
		"locked": {
			"network.verifyTls": true,
			"network.caBundles": [`+jsonString(t, corp)+`],
			"editor.fontSize": 14,
			"ui.theme": "blue",
			"network.proxy": "http://proxy",
			"nosection": 1,
			"privacy.allowedDirs": ["relative"]
		},
		"hidden": ["ui.theme"]
	}`))
	if !p.Exists || !p.Active() || p.RequireTLSVerification {
		t.Fatalf("policy = %+v", p)
	}
	if got, want := p.LockedKeys(), []string{"editor.fontSize", "network.caBundles", "network.verifyTls"}; !slices.Equal(got, want) {
		t.Errorf("LockedKeys = %v, want %v", got, want)
	}
	// Unknown top-level member first, then the skipped entries by key.
	if got, want := problemFields(p), []string{"hidden", "network.proxy", "nosection", "privacy.allowedDirs", "ui.theme"}; !slices.Equal(got, want) {
		t.Errorf("problem fields = %v, want %v", got, want)
	}

	s := Defaults()
	s.Network.VerifyTLS = false
	s.Editor.FontSize = 20
	got := p.Enforce(s)
	if !got.Network.VerifyTLS || got.Editor.FontSize != 14 || !slices.Equal(got.Network.CABundles, []string{corp}) {
		t.Errorf("Enforce = %+v", got)
	}
	if got.UI != s.UI {
		t.Errorf("Enforce changed an unlocked setting: %+v", got.UI)
	}
}

func TestLoadPolicyRequireTLSVerification(t *testing.T) {
	t.Parallel()
	p := LoadPolicy(writePolicy(t, `{"version": 1, "requireTlsVerification": true, "locked": {"network.verifyTls": false}}`))
	if !p.RequireTLSVerification || !p.IsLocked(KeyVerifyTLS) || string(p.Locked[KeyVerifyTLS]) != "true" {
		t.Fatalf("policy = %+v, want verification required and network.verifyTls locked to true", p)
	}
	if got := problemFields(p); !slices.Equal(got, []string{KeyVerifyTLS}) {
		t.Errorf("problems = %v, want the conflicting lock", got)
	}

	if err := p.TLSVerificationRequired(false, "HTTP"); err != nil {
		t.Errorf("verified request refused: %v", err)
	}
	var e *errs.Error
	if err := p.TLSVerificationRequired(true, "gRPC"); !errors.As(err, &e) || e.Code != CodeTLSVerificationRequired ||
		e.Kind != errs.PermissionDenied || e.Details["protocol"] != "gRPC" {
		t.Errorf("insecure request: err = %v, want %s", err, CodeTLSVerificationRequired)
	}
	if err := (Policy{}).TLSVerificationRequired(true, "HTTP"); err != nil {
		t.Errorf("no policy refused an insecure request: %v", err)
	}
}

func TestLoadPolicyBrokenFileAppliesNothing(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"syntax":        `{"locked": {"network.verifyTls": true`,
		"not an object": `["network.verifyTls"]`,
	} {
		p := LoadPolicy(writePolicy(t, content))
		if p.Active() || !p.Exists || len(p.Problems) != 1 {
			t.Errorf("%s: policy = %+v, want nothing applied and one problem", name, p)
			continue
		}
		if e := p.Problems[0]; e.Code != CodePolicyInvalid || e.Kind != errs.Parse || e.Details["path"] == "" {
			t.Errorf("%s: problem = %+v", name, e)
		}
	}

	p := LoadPolicy(writePolicy(t, `{"locked": ["network.verifyTls"], "requireTlsVerification": "yes"}`))
	if p.Active() || !slices.Equal(problemFields(p), []string{"requireTlsVerification", "locked"}) {
		t.Errorf("bad members: policy = %+v", p)
	}
}

func TestLoadPolicyNewerVersionAppliesWhatItKnows(t *testing.T) {
	t.Parallel()
	p := LoadPolicy(writePolicy(t, `{"version": 7, "locked": {"network.verifyTls": true}}`))
	if !p.IsLocked(KeyVerifyTLS) || len(p.Problems) != 1 || p.Problems[0].Kind != errs.Unsupported {
		t.Errorf("policy = %+v, want the lock and a newer-version problem", p)
	}
}

func lockedStore(t *testing.T, fileContent string) (*Store, string) {
	t.Helper()
	policy := LoadPolicy(writePolicy(t, `{"version": 1, "requireTlsVerification": true, "locked": {"editor.fontSize": 14}}`))
	path := filepath.Join(t.TempDir(), FileName)
	if fileContent != "" {
		if err := os.WriteFile(path, []byte(fileContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Open(path, nil, WithPolicy(policy)), path
}

func TestStorePolicyOverridesTheFileWithoutRewritingIt(t *testing.T) {
	t.Parallel()
	content := `{"version": 1, "network": {"verifyTls": false}, "editor": {"fontSize": 20, "tabSize": 4}}`
	s, path := lockedStore(t, content)

	got := s.Settings()
	if !got.Network.VerifyTLS || got.Editor.FontSize != 14 || got.Editor.TabSize != 4 {
		t.Fatalf("settings = %+v, want the locked values and the file's tab size", got)
	}
	if len(s.State().Problems) != 0 {
		t.Errorf("problems = %v", s.State().Problems)
	}
	if data, _ := os.ReadFile(path); string(data) != content {
		t.Errorf("the file was rewritten at load:\n%s", data)
	}
	if !s.Policy().RequireTLSVerification {
		t.Error("Store.Policy lost the policy")
	}
}

func TestStorePolicyRefusesChangesToLockedSettings(t *testing.T) {
	t.Parallel()
	s, path := lockedStore(t, `{"version": 1, "network": {"verifyTls": false}, "editor": {"fontSize": 20}}`)

	off := false
	size := 16
	for name, p := range map[string]Patch{
		"verifyTls": {Network: NetworkPatch{VerifyTLS: &off}},
		"fontSize":  {Editor: EditorPatch{FontSize: &size}, UI: UIPatch{SplitRatio: new(0.3)}},
	} {
		_, changed, err := s.Update(p)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeLocked || e.Kind != errs.PermissionDenied || changed {
			t.Errorf("%s: changed %v, err %v, want %s", name, changed, err, CodeLocked)
		}
	}
	if s.Settings().UI.SplitRatio != 0.5 {
		t.Error("a refused patch applied its other fields")
	}

	// Setting a locked setting to its locked value is not a change.
	on, locked := true, 14
	if _, changed, err := s.Update(Patch{Network: NetworkPatch{VerifyTLS: &on}, Editor: EditorPatch{FontSize: &locked}}); err != nil || changed {
		t.Errorf("locked values: changed %v, err %v", changed, err)
	}
	if data, _ := os.ReadFile(path); bytes.Contains(data, []byte(`"fontSize": 14`)) {
		t.Errorf("the locked value was written:\n%s", data)
	}

	// Other settings change; the file keeps the user's own values of the
	// locked ones.
	next, changed, err := s.Update(ratio(0.3))
	if err != nil || !changed || next.UI.SplitRatio != 0.3 || !next.Network.VerifyTLS || next.Editor.FontSize != 14 {
		t.Fatalf("Update: %+v, changed %v, err %v", next, changed, err)
	}
	loaded, _, err := Load(path)
	if err != nil || loaded.Network.VerifyTLS || loaded.Editor.FontSize != 20 || loaded.UI.SplitRatio != 0.3 {
		t.Errorf("file = %+v, err %v, want the user's verifyTls false and fontSize 20 kept", loaded, err)
	}
}

func TestStorePolicyIgnoresHandEditsOfLockedSettings(t *testing.T) {
	t.Parallel()
	s, path := lockedStore(t, `{"version": 1}`)
	before := s.Settings()

	if err := os.WriteFile(path, []byte(`{"version": 1, "network": {"verifyTls": false}, "editor": {"fontSize": 30}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Reload() {
		t.Error("Reload reported a change for locked settings only")
	}
	if got := s.Settings(); !got.Equal(before) {
		t.Errorf("settings = %+v, want %+v", got, before)
	}

	if err := os.WriteFile(path, []byte(`{"version": 1, "network": {"verifyTls": false}, "editor": {"tabSize": 8}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Reload() {
		t.Fatal("Reload missed the change of an unlocked setting")
	}
	if got := s.Settings(); got.Editor.TabSize != 8 || !got.Network.VerifyTLS || got.Editor.FontSize != 14 {
		t.Errorf("settings = %+v", got)
	}

	// Deleting the file resets the user's settings; the locks stay.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if got := s.Settings(); got.Editor.FontSize != 14 || got.Editor.TabSize != 2 {
		t.Errorf("after delete: %+v", got)
	}
}

func TestStorePolicyCreateWritesTheUsersSettings(t *testing.T) {
	t.Parallel()
	s, path := lockedStore(t, "")
	if err := s.Create(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), `"fontSize": 14`) || !strings.Contains(string(data), `"fontSize": 12`) {
		t.Errorf("created file (err %v):\n%s", err, data)
	}
}

func TestStorePolicyLocksListsAndOmittedSections(t *testing.T) {
	t.Parallel()
	// Settings added by later features (privacy.allowedDirs,
	// ui.customTheme) are lockable without changes to the policy code.
	corp, secrets := abs(t, "ca.pem"), abs(t, "secrets")
	policy := LoadPolicy(writePolicy(t, `{"locked": {"privacy.allowedDirs": [], "network.caBundles": [`+jsonString(t, corp)+`], "ui.customTheme": "corp.json"}}`))
	if got := policy.LockedKeys(); len(got) != 3 {
		t.Fatalf("locked = %v, problems %v", got, policy.Problems)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"ui": {"customTheme": "mine.json"}, "privacy": {"allowedDirs": [`+jsonString(t, secrets)+`]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, nil, WithPolicy(policy))
	got := s.Settings()
	if len(got.Privacy.AllowedDirs) != 0 || !slices.Equal(got.Network.CABundles, []string{corp}) || got.UI.CustomTheme != "corp.json" {
		t.Fatalf("settings = %+v", got)
	}
	none := ""
	if _, _, err := s.Update(Patch{UI: UIPatch{CustomTheme: &none}}); err == nil {
		t.Error("turning the locked custom theme off was not refused")
	}
	dirs := []string{abs(t, "tmp")}
	if _, _, err := s.Update(Patch{Privacy: PrivacyPatch{AllowedDirs: &dirs}}); err == nil {
		t.Error("adding an allowed directory was not refused")
	}
	if _, changed, err := s.Update(theme(ThemeDark)); err != nil || !changed {
		t.Fatalf("unlocked update: changed %v, err %v", changed, err)
	}
	loaded, _, _ := Load(path)
	if !slices.Equal(loaded.Privacy.AllowedDirs, []string{secrets}) || len(loaded.Network.CABundles) != 0 ||
		loaded.UI.CustomTheme != "mine.json" || loaded.UI.Theme != ThemeDark {
		t.Errorf("file = %+v, want the user's lists kept", loaded)
	}
}

func TestStorePolicyLocksLocale(t *testing.T) {
	t.Parallel()
	// ui.locale is lockable like any setting: the locked value is
	// in effect, the user's own stays in the file.
	policy := LoadPolicy(writePolicy(t, `{"locked": {"ui.locale": "en"}}`))
	if got := policy.LockedKeys(); len(got) != 1 {
		t.Fatalf("locked = %v, problems %v", got, policy.Problems)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"ui": {"locale": "system"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, nil, WithPolicy(policy))
	if got := s.Settings().UI.Locale; got != "en" {
		t.Fatalf("locale in effect = %q, want the locked en", got)
	}
	pseudo := "en-XA"
	if _, _, err := s.Update(Patch{UI: UIPatch{Locale: &pseudo}}); err == nil {
		t.Error("changing the locked locale was not refused")
	}
	if _, changed, err := s.Update(theme(ThemeDark)); err != nil || !changed {
		t.Fatalf("unlocked update: changed %v, err %v", changed, err)
	}
	if loaded, _, _ := Load(path); loaded.UI.Locale != LocaleSystem {
		t.Errorf("file locale = %q, want the user's system kept", loaded.UI.Locale)
	}
}
