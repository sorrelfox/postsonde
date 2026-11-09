package secrets

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// fakeKeychain is an in-memory keychain.KeyStore; fail makes every call
// fail with that error (a missing Secret Service, a denied access).
type fakeKeychain struct {
	mu    sync.Mutex
	keys  map[string][]byte
	fail  error
	calls int
}

func newFakeKeychain() *fakeKeychain { return &fakeKeychain{keys: map[string][]byte{}} }

func (f *fakeKeychain) Name() string { return "Test Keychain" }

func (f *fakeKeychain) LoadKey(id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	if k, ok := f.keys[id]; ok {
		return bytes.Clone(k), nil
	}
	return nil, nil
}

func (f *fakeKeychain) StoreKey(id string, key []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.keys[id] = bytes.Clone(key)
	return nil
}

func (f *fakeKeychain) DeleteKey(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	delete(f.keys, id)
	return nil
}

func (f *fakeKeychain) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

func (f *fakeKeychain) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func noService() error {
	return errs.New(errs.Unsupported, "No Secret Service (keychain) is running in this session.").
		WithCode(keychain.CodeUnavailable).WithHint("Install secret-tool.")
}

func keychainWithProblem() *Keychain {
	k := NewKeychain(newFakeKeychain(), nil)
	k.setProblem(noService())
	return k
}

// restart opens the same data directory again, as the next start of the
// app does, and runs the startup unlock to its end.
func restart(t *testing.T, f *fixture, ks keychain.KeyStore) *fixture {
	t.Helper()
	kc := NewKeychain(ks, f.settings)
	ev := &events{}
	opts := VaultOptions(f.settings, kc, Changed(ev.emit, f.settings, kc))
	opts.Iterations = 1000
	v := secretstore.Open(f.data, opts)
	t.Cleanup(secretstore.Register(v))
	svc := New(v, f.settings, nil, WithKeychain(kc), WithEmit(ev.emit))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	g := &fixture{svc: svc, vault: v, settings: f.settings, events: ev, data: f.data}
	if g.svc.startKeychainUnlock() {
		if st := g.svc.status(); !st.Keychain.Pending {
			t.Error("the startup unlock is not pending")
		}
		g.svc.keychainUnlock()
	}
	return g
}

func TestKeychainUnlocksAtStartup(t *testing.T) {
	t.Parallel()
	ks := newFakeKeychain()
	f := newKeychainFixture(t, "", nil, ks)
	ctx := t.Context()
	store := requestsecrets.New(filepath.Join(f.data, "workspaces", "w1"))

	// The default key store is the keychain: turning encryption on keeps
	// the key there.
	st, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password})
	if err != nil || st.Keychain.Mode != coresettings.SecretsKeyStoreKeychain || !st.Keychain.Available || st.Keychain.Name != "Test Keychain" || st.Keychain.Problem != nil {
		t.Fatalf("Enable = %+v, %v", st, err)
	}
	if ks.count() != 1 {
		t.Fatalf("keys stored = %d", ks.count())
	}
	if err := store.Write("req_1", map[string]string{"auth.token": "s3cret"}); err != nil {
		t.Fatal(err)
	}

	// The next start unlocks without the password.
	g := restart(t, f, ks)
	if st := g.svc.status(); st.Locked || st.Keychain.Pending || st.Keychain.Problem != nil {
		t.Fatalf("after restart = %+v", st)
	}
	if ev, _ := g.events.last(); ev.Locked || ev.Keychain.Pending {
		t.Errorf("last event = %+v", ev)
	}
	if v, err := store.Read("req_1"); err != nil || v["auth.token"] != "s3cret" {
		t.Fatalf("Read = %v, %v", v, err)
	}

	// After Lock now, the keychain unlocks again on request.
	if _, err := g.svc.Lock(ctx); err != nil {
		t.Fatal(err)
	}
	if st, err := g.svc.UnlockWithKeychain(ctx); err != nil || st.Locked {
		t.Fatalf("UnlockWithKeychain = %+v, %v", st, err)
	}

	// Turning encryption off removes the key from the keychain.
	if _, err := g.svc.Disable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	if ks.count() != 0 {
		t.Error("the key is still in the keychain")
	}
}

// Without a working keychain the secrets stay locked at startup, the
// status says why, and the master password unlocks them.
func TestKeychainFallsBackToPassword(t *testing.T) {
	t.Parallel()
	ks := newFakeKeychain()
	f := newKeychainFixture(t, "", nil, ks)
	ctx := t.Context()
	if _, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}

	ks.setFail(noService())
	g := restart(t, f, ks)
	st := g.svc.status()
	if !st.Locked || st.Keychain.Pending || st.Keychain.Problem == nil || st.Keychain.Problem.Code != keychain.CodeUnavailable {
		t.Fatalf("after restart = %+v", st)
	}
	if ev, _ := g.events.last(); !ev.Locked || ev.Keychain.Problem == nil {
		t.Errorf("the UI did not hear of it: %+v", ev)
	}
	_, err := g.svc.UnlockWithKeychain(ctx)
	wantCode(t, err, keychain.CodeUnavailable)
	// The password still works; storing the key fails quietly.
	st, err = g.svc.Unlock(ctx, PasswordInputDTO{Password: password})
	if err != nil || st.Locked || st.Keychain.Problem == nil {
		t.Fatalf("Unlock = %+v, %v", st, err)
	}

	// Once the keychain works again, the next password unlock stores the key.
	ks.setFail(nil)
	delete(ks.keys, keyIDOf(t, ks))
	_, _ = g.svc.Lock(ctx)
	_, err = g.svc.UnlockWithKeychain(ctx)
	wantCode(t, err, CodeKeychainNoKey)
	if st, err := g.svc.Unlock(ctx, PasswordInputDTO{Password: password}); err != nil || st.Keychain.Problem != nil {
		t.Fatalf("Unlock = %+v, %v", st, err)
	}
	if ks.count() != 1 {
		t.Error("the key was not stored again")
	}
}

// keyIDOf returns the only key ID in ks, or "" when it is empty.
func keyIDOf(t *testing.T, ks *fakeKeychain) string {
	t.Helper()
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for id := range ks.keys {
		return id
	}
	return ""
}

// Switching between the keychain and the master password moves only the
// key: the secrets and the password stay as they are.
func TestSetKeyStore(t *testing.T) {
	t.Parallel()
	ks := newFakeKeychain()
	f := newKeychainFixture(t, "", nil, ks)
	ctx := t.Context()
	store := requestsecrets.New(filepath.Join(f.data, "workspaces", "w1"))
	if _, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("req_1", map[string]string{"auth.token": "s3cret"}); err != nil {
		t.Fatal(err)
	}

	st, err := f.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: coresettings.SecretsKeyStorePassword})
	if err != nil || st.Keychain.Mode != coresettings.SecretsKeyStorePassword || f.settings.Settings().Privacy.SecretsKeyStore != "password" {
		t.Fatalf("SetKeyStore(password) = %+v, %v", st, err)
	}
	if ks.count() != 0 {
		t.Error("the key is still in the keychain")
	}
	if ev, _ := f.events.last(); ev.Keychain.Mode != "password" {
		t.Errorf("event = %+v", ev.Keychain)
	}
	// The next start asks for the password; nothing reads the keychain.
	calls := ks.calls
	g := restart(t, f, ks)
	if st := g.svc.status(); !st.Locked {
		t.Fatal("unlocked without the keychain")
	}
	if ks.calls != calls {
		t.Error("the keychain was used in password mode")
	}
	_, err = g.svc.UnlockWithKeychain(ctx)
	wantCode(t, err, CodeNoKeychain)
	if _, err := g.svc.Unlock(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	if ks.count() != 0 {
		t.Error("a password unlock stored the key in password mode")
	}

	// Back to the keychain: the unlocked key is stored at once.
	if st, err := g.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "keychain"}); err != nil || st.Keychain.Mode != "keychain" {
		t.Fatalf("SetKeyStore(keychain) = %+v, %v", st, err)
	}
	if ks.count() != 1 {
		t.Error("the key was not stored")
	}
	if v, err := store.Read("req_1"); err != nil || v["auth.token"] != "s3cret" {
		t.Fatalf("Read = %v, %v", v, err)
	}
	h := restart(t, g, ks)
	if st := h.svc.status(); st.Locked {
		t.Fatal("not unlocked from the keychain")
	}

	// A keychain that refuses keeps the setting as it was.
	if _, err := h.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "password"}); err != nil {
		t.Fatal(err)
	}
	ks.setFail(noService())
	_, err = h.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "keychain"})
	wantCode(t, err, keychain.CodeUnavailable)
	if got := h.settings.Settings().Privacy.SecretsKeyStore; got != "password" {
		t.Errorf("setting = %q after a refused keychain", got)
	}
	_, err = h.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "vault"})
	wantCode(t, err, coresettings.CodeInvalidValue)
}

func TestSetKeyStoreWithoutKeychain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "", nil)
	ctx := t.Context()
	st, err := f.svc.Status(ctx)
	if err != nil || st.Keychain.Available || st.Keychain.Mode != "password" {
		t.Fatalf("Status = %+v, %v", st.Keychain, err)
	}
	_, err = f.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "keychain"})
	wantCode(t, err, CodeNoKeychain)
	if _, err := f.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "password"}); err != nil {
		t.Fatal(err)
	}
	if f.svc.startKeychainUnlock() {
		t.Error("a startup unlock without a keychain")
	}
}

func TestSetKeyStoreLockedByPolicy(t *testing.T) {
	t.Parallel()
	ks := newFakeKeychain()
	f := newKeychainFixture(t, `{"version":1,"locked":{"privacy.secretsKeyStore":"password"}}`, nil, ks)
	ctx := t.Context()
	st, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password})
	if err != nil || !st.Keychain.ModeLocked || st.Keychain.Mode != "password" {
		t.Fatalf("Enable = %+v, %v", st.Keychain, err)
	}
	if ks.count() != 0 {
		t.Error("the key was stored although the policy says password")
	}
	_, err = f.svc.SetKeyStore(ctx, KeyStoreInputDTO{Mode: "keychain"})
	wantCode(t, err, coresettings.CodeLocked)
}

// The startup unlock runs in the background from ServiceStartup.
func TestServiceStartupUnlocks(t *testing.T) {
	t.Parallel()
	ks := newFakeKeychain()
	f := newKeychainFixture(t, "", nil, ks)
	if _, err := f.svc.Enable(context.Background(), PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()
	if err := f.svc.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.vault.Status().Locked {
		if time.Now().After(deadline) {
			t.Fatal("not unlocked from the keychain")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
