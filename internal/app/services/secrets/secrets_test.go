package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

const password = "correct horse battery"

type events struct {
	mu   sync.Mutex
	list []StatusDTO
}

func (e *events) emit(name string, data any) {
	if name != EventChanged {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, data.(StatusDTO))
}

func (e *events) last() (StatusDTO, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.list) == 0 {
		return StatusDTO{}, 0
	}
	return e.list[len(e.list)-1], len(e.list)
}

type fixture struct {
	svc      *SecretsService
	vault    *secretstore.Vault
	settings *coresettings.Store
	events   *events
	data     string
}

func newFixture(t *testing.T, policy string, now func() time.Time) *fixture {
	t.Helper()
	return newKeychainFixture(t, policy, now, nil)
}

// newKeychainFixture is newFixture with a keychain (nil: none).
func newKeychainFixture(t *testing.T, policy string, now func() time.Time, ks keychain.KeyStore) *fixture {
	t.Helper()
	data := t.TempDir()
	var p coresettings.Policy
	if policy != "" {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
		p = coresettings.LoadPolicy(path)
	}
	st := coresettings.Open(filepath.Join(t.TempDir(), coresettings.FileName), nil, coresettings.WithPolicy(p))
	ev := &events{}
	var kc *Keychain
	if ks != nil {
		kc = NewKeychain(ks, st)
	}
	opts := VaultOptions(st, kc, Changed(ev.emit, st, kc))
	opts.Iterations = 1000
	opts.Now = now
	v := secretstore.Open(data, opts)
	t.Cleanup(secretstore.Register(v))
	svc := New(v, st, nil, WithKeychain(kc), WithEmit(ev.emit))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return &fixture{svc: svc, vault: v, settings: st, events: ev, data: data}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "", nil)
	ctx := t.Context()
	local := filepath.Join(f.data, "workspaces", "w1")
	store := requestsecrets.New(local)
	if err := store.Write("req_1", map[string]string{"auth.token": "s3cret"}); err != nil {
		t.Fatal(err)
	}

	st, err := f.svc.Status(ctx)
	if err != nil || !st.Available || st.Enabled || st.Locked || st.MinPasswordLength != secretstore.MinPasswordLength {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	_, err = f.svc.Enable(ctx, PasswordInputDTO{Password: "short"})
	wantCode(t, err, secretstore.CodePasswordTooShort)
	st, err = f.svc.Enable(ctx, PasswordInputDTO{Password: password})
	if err != nil || !st.Enabled || st.Locked || st.KDF != secretstore.KDFName {
		t.Fatalf("Enable = %+v, %v", st, err)
	}
	if ev, _ := f.events.last(); !ev.Enabled {
		t.Errorf("event after enable = %+v", ev)
	}

	st, err = f.svc.Lock(ctx)
	if err != nil || !st.Locked {
		t.Fatalf("Lock = %+v, %v", st, err)
	}
	if ev, _ := f.events.last(); !ev.Locked {
		t.Errorf("event after lock = %+v", ev)
	}
	_, err = store.Read("req_1")
	wantCode(t, err, secretstore.CodeLocked)

	st, err = f.svc.Unlock(ctx, PasswordInputDTO{Password: "wrong password"})
	wantCode(t, err, secretstore.CodeWrongPassword)
	if !st.Locked {
		t.Error("unlocked by a wrong password")
	}
	if st, err = f.svc.Unlock(ctx, PasswordInputDTO{Password: password}); err != nil || st.Locked {
		t.Fatalf("Unlock = %+v, %v", st, err)
	}
	if v, err := store.Read("req_1"); err != nil || v["auth.token"] != "s3cret" {
		t.Fatalf("Read = %v, %v", v, err)
	}

	_, err = f.svc.ChangePassword(ctx, ChangePasswordInputDTO{OldPassword: "wrong password", NewPassword: "new password 2"})
	wantCode(t, err, secretstore.CodeWrongPassword)
	if _, err = f.svc.ChangePassword(ctx, ChangePasswordInputDTO{OldPassword: password, NewPassword: "new password 2"}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.svc.Lock(ctx)
	_, err = f.svc.Unlock(ctx, PasswordInputDTO{Password: password})
	wantCode(t, err, secretstore.CodeWrongPassword)

	_, err = f.svc.Disable(ctx, PasswordInputDTO{Password: password})
	wantCode(t, err, secretstore.CodeWrongPassword)
	if st, err = f.svc.Disable(ctx, PasswordInputDTO{Password: "new password 2"}); err != nil || st.Enabled {
		t.Fatalf("Disable = %+v, %v", st, err)
	}
	if v, err := store.Read("req_1"); err != nil || v["auth.token"] != "s3cret" {
		t.Fatalf("Read after disable = %v, %v", v, err)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "", nil)
	ctx := t.Context()
	store := requestsecrets.New(filepath.Join(f.data, "workspaces", "w1"))
	if _, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("req_1", map[string]string{"auth.token": "s3cret"}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.svc.Lock(ctx)
	_, err := f.svc.Reset(ctx, ResetInputDTO{})
	wantCode(t, err, CodeConfirmRequired)
	st, err := f.svc.Reset(ctx, ResetInputDTO{Confirm: true})
	if err != nil || st.Enabled || st.Locked {
		t.Fatalf("Reset = %+v, %v", st, err)
	}
	if v, err := store.Read("req_1"); err != nil || len(v) != 0 {
		t.Errorf("after reset = %v, %v", v, err)
	}
}

func TestIdleLock(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f := newFixture(t, "", clock)
	ctx := t.Context()
	st, err := f.svc.SetIdleLock(ctx, 10)
	if err != nil || st.IdleLockMinutes != 10 || f.settings.Settings().Privacy.SecretsLockMinutes != 10 {
		t.Fatalf("SetIdleLock = %+v, %v", st, err)
	}
	_, err = f.svc.SetIdleLock(ctx, 5000)
	wantCode(t, err, coresettings.CodeInvalidValue)
	if _, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	f.svc.startIdleTimer(time.Millisecond)

	mu.Lock()
	now = now.Add(11 * time.Minute)
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ev, _ := f.events.last(); ev.Locked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle timer did not lock the secrets")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st, _ := f.svc.Status(ctx); !st.Locked {
		t.Errorf("status = %+v", st)
	}
	// 0 turns it off.
	if st, err := f.svc.SetIdleLock(ctx, 0); err != nil || st.IdleLockMinutes != 0 {
		t.Fatalf("SetIdleLock(0) = %+v, %v", st, err)
	}
}

func TestPolicy(t *testing.T) {
	t.Parallel()
	f := newFixture(t, `{"version":1,"locked":{"privacy.requireSecretEncryption":true,"privacy.secretsLockMinutes":5}}`, nil)
	ctx := t.Context()
	st, err := f.svc.Status(ctx)
	if err != nil || !st.Required || !st.IdleLockLocked || st.IdleLockMinutes != 5 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	_, err = f.svc.SetIdleLock(ctx, 30)
	wantCode(t, err, coresettings.CodeLocked)
	// No secret is stored while encryption is off.
	err = requestsecrets.New(filepath.Join(f.data, "workspaces", "w1")).Write("req_1", map[string]string{"auth.token": "x"})
	wantCode(t, err, secretstore.CodeRequired)
	if _, err := f.svc.Enable(ctx, PasswordInputDTO{Password: password}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Disable(ctx, PasswordInputDTO{Password: password})
	wantCode(t, err, secretstore.CodeRequired)
}

// TestDTOJSON pins the JSON shape of the DTOs (the generated TypeScript
// models): camelCase names, no omitted fields.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	problem := errs.New(errs.Parse, "PostSonde's key file for encrypted secrets cannot be used.").WithCode(secretstore.CodeConfigInvalid)
	tests := []struct {
		name string
		dto  any
	}{
		{"status_off", toStatusDTO(secretstore.Status{}, nil, nil)},
		{"status_locked", toStatusDTO(secretstore.Status{Enabled: true, Locked: true, Migration: secretstore.MigrationEncrypting,
			Required: true, IdleTimeout: 15 * time.Minute, KDF: secretstore.KDFName, Iterations: secretstore.DefaultIterations}, nil, nil)},
		{"status_problem", toStatusDTO(secretstore.Status{Enabled: true, Locked: true, Problem: problem}, nil, nil)},
		{"status_keychain", toStatusDTO(secretstore.Status{Enabled: true, Locked: true}, nil, keychainWithProblem())},
		{"key_store_input", KeyStoreInputDTO{Mode: "password"}},
		{"change_password_input", ChangePasswordInputDTO{OldPassword: "a", NewPassword: "b"}},
		{"reset_input", ResetInputDTO{Confirm: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}

func TestUnavailable(t *testing.T) {
	t.Parallel()
	svc := New(nil, nil, nil)
	ctx := t.Context()
	st, err := svc.Status(ctx)
	if err != nil || st.Available || st.Enabled {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	_, err = svc.Enable(ctx, PasswordInputDTO{Password: password})
	wantCode(t, err, CodeUnavailable)
	if _, err := svc.Lock(ctx); err != nil {
		t.Error(err)
	}
	if err := svc.ServiceShutdown(); err != nil {
		t.Error(err)
	}
}
