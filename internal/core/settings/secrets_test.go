package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretSettings: privacy.secretsLockMinutes and
// privacy.requireSecretEncryption are read, validated, patched,
// compared, written and lockable by the admin policy.
func TestSecretSettings(t *testing.T) {
	s, r, err := Load(writeSettings(t, `{"privacy":{"secretsLockMinutes":15,"requireSecretEncryption":true}}`))
	if err != nil || len(r.Problems) != 0 || s.Privacy.SecretsLockMinutes != 15 || !s.Privacy.RequireSecretEncryption {
		t.Fatalf("Load = %+v, %+v, %v", s.Privacy, r.Problems, err)
	}
	for _, bad := range []string{`-1`, `1441`, `1.5`, `"15"`} {
		s, r, err := Load(writeSettings(t, `{"privacy":{"secretsLockMinutes":`+bad+`}}`))
		if err != nil || s.Privacy.SecretsLockMinutes != 0 || len(r.Problems) != 1 || r.Problems[0].Details["field"] != "privacy.secretsLockMinutes" {
			t.Errorf("%s: Load = %+v, %+v, %v", bad, s.Privacy, r.Problems, err)
		}
	}
	if s, r, _ := Load(writeSettings(t, `{"privacy":{"requireSecretEncryption":"yes"}}`)); s.Privacy.RequireSecretEncryption || len(r.Problems) != 1 {
		t.Errorf("requireSecretEncryption not a bool: %+v, %+v", s.Privacy, r.Problems)
	}

	minutes, on := 30, true
	next, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{SecretsLockMinutes: &minutes, RequireSecretEncryption: &on}})
	if err != nil || next.Privacy.SecretsLockMinutes != 30 || !next.Privacy.RequireSecretEncryption {
		t.Fatalf("Apply = %+v, %v", next.Privacy, err)
	}
	if next.Equal(Defaults()) {
		t.Error("Equal ignores the secret settings")
	}
	if err := next.Validate(); err != nil {
		t.Error(err)
	}
	for _, bad := range []int{-5, MaxSecretsLockMinutes + 1} {
		if _, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{SecretsLockMinutes: &bad}}); err == nil {
			t.Errorf("Apply(%d) accepted", bad)
		}
	}

	data, err := Marshal(next, nil)
	if err != nil || !strings.Contains(string(data), `"secretsLockMinutes": 30`) || !strings.Contains(string(data), `"requireSecretEncryption": true`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
	data, err = Marshal(Defaults(), nil)
	if err != nil || strings.Contains(string(data), `"privacy"`) {
		t.Errorf("defaults write a privacy section: %s", data)
	}

	// The admin policy locks them like any setting.
	policy := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policy, []byte(`{"version":1,"locked":{"privacy.requireSecretEncryption":true,"privacy.secretsLockMinutes":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := LoadPolicy(policy)
	locked := p.Enforce(Defaults())
	if !locked.Privacy.RequireSecretEncryption || locked.Privacy.SecretsLockMinutes != 5 || len(p.Problems) != 0 {
		t.Errorf("policy = %+v, problems %+v", locked.Privacy, p.Problems)
	}
}

// TestSecretsKeyStoreSetting: privacy.secretsKeyStore defaults to
// the keychain, which is left out of the file, and only takes its values.
func TestSecretsKeyStoreSetting(t *testing.T) {
	if got := Defaults().Privacy.SecretsKeyStore; got != SecretsKeyStoreKeychain {
		t.Fatalf("default = %q", got)
	}
	s, r, err := Load(writeSettings(t, `{"privacy":{"secretsKeyStore":"password"}}`))
	if err != nil || len(r.Problems) != 0 || s.Privacy.SecretsKeyStore != SecretsKeyStorePassword {
		t.Fatalf("Load = %+v, %+v, %v", s.Privacy, r.Problems, err)
	}
	for _, bad := range []string{`"os"`, `""`, `1`} {
		s, r, err := Load(writeSettings(t, `{"privacy":{"secretsKeyStore":`+bad+`}}`))
		if err != nil || s.Privacy.SecretsKeyStore != SecretsKeyStoreKeychain || len(r.Problems) != 1 {
			t.Errorf("%s: Load = %+v, %+v, %v", bad, s.Privacy, r.Problems, err)
		}
	}
	pw := SecretsKeyStorePassword
	next, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{SecretsKeyStore: &pw}})
	if err != nil || next.Privacy.SecretsKeyStore != pw || next.Equal(Defaults()) || next.Validate() != nil {
		t.Fatalf("Apply = %+v, %v", next.Privacy, err)
	}
	bad := "vault"
	if _, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{SecretsKeyStore: &bad}}); err == nil {
		t.Error("Apply accepted an unknown key store")
	}
	data, err := Marshal(next, nil)
	if err != nil || !strings.Contains(string(data), `"secretsKeyStore": "password"`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
	if data, _ := Marshal(Defaults(), nil); strings.Contains(string(data), "secretsKeyStore") {
		t.Errorf("the default is written: %s", data)
	}
	policy := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policy, []byte(`{"version":1,"locked":{"privacy.secretsKeyStore":"password"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := LoadPolicy(policy)
	if got := p.Enforce(Defaults()); got.Privacy.SecretsKeyStore != pw || len(p.Problems) != 0 || !p.IsLocked("privacy.secretsKeyStore") {
		t.Errorf("policy = %+v, problems %+v", got.Privacy, p.Problems)
	}
}
