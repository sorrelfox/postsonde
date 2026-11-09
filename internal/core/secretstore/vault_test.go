package secretstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// testIterations keeps PBKDF2 fast in tests; the format and code paths are
// those of the default count.
const testIterations = 1000

const password = "correct horse battery"

// newVault opens a registered vault over a fresh root.
func newVault(t *testing.T, opts Options) *Vault {
	t.Helper()
	return openVault(t, t.TempDir(), opts)
}

func openVault(t *testing.T, root string, opts Options) *Vault {
	t.Helper()
	if opts.Iterations == 0 {
		opts.Iterations = testIterations
	}
	v := Open(root, opts)
	t.Cleanup(Register(v))
	return v
}

// secretPath is a secret file of a workspace under root.
func secretPath(root, name string) string {
	return filepath.Join(root, "workspaces", "ws1", "secrets", filepath.FromSlash(name))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}

func raw(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

// plainFiles are secret files with their content, written before
// encryption is turned on.
var plainFiles = map[string]string{
	"requests/req_1.json":        `{"values":{"auth.token":"t0ken-AAAA"}}`,
	"requests/fld_2.json":        `{"values":{"var.key":"f0lder-BBBB"}}`,
	"environments/env_1.json":    `{"values":{"password":"env-CCCC"}}`,
	"runtime/variables.json":     `{"values":{"session":"runtime-DDDD"}}`,
	"oauth2/tokens.json":         `{"tokens":{"k":{"accessToken":"access-EEEE"}}}`,
	"../../../secrets/app.json":  `{"values":{"proxy.password":"proxy-FFFF"}}`,
	"requests/.req_3.json.tmp-9": `{"values":{"auth.token":"leftover-GGGG"}}`,
}

var secretMarkers = []string{"AAAA", "BBBB", "CCCC", "DDDD", "EEEE", "FFFF", "GGGG"}

func writePlainFiles(t *testing.T, root string) {
	t.Helper()
	for name, content := range plainFiles {
		p := secretPath(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A folder a project called "secrets", in the local trash: never
	// touched.
	trash := filepath.Join(root, "workspaces", "ws1", "trash", "t1", "secrets", "req.json")
	if err := os.MkdirAll(filepath.Dir(trash), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trash, []byte(`{"name":"not a secret file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// scan fails when a file under root holds one of the markers.
func scan(t *testing.T, root string, markers []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range markers {
			if bytes.Contains(data, []byte(m)) {
				t.Errorf("%s holds %s in the clear", path, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkPlainFilesReadable reads every non-temp plain file back.
func checkPlainFilesReadable(t *testing.T, root string) {
	t.Helper()
	for name, content := range plainFiles {
		if strings.Contains(name, ".tmp-") {
			continue
		}
		if got := read(t, secretPath(root, name)); got != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
}

func TestOffIsPlain(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, `{"values":{"a":"b"}}`)
	if got := string(raw(t, p)); got != `{"values":{"a":"b"}}` {
		t.Errorf("file = %q", got)
	}
	if st := v.Status(); st.Enabled || st.Locked {
		t.Errorf("status = %+v", st)
	}
	wantCode(t, v.Unlock(password), CodeNotEnabled)
}

func TestEnableEncryptsEveryFileAndRoundTrips(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlainFiles(t, root)
	var changes []Status
	var mu sync.Mutex
	v := openVault(t, root, Options{OnChange: func(s Status) { mu.Lock(); changes = append(changes, s); mu.Unlock() }})
	wantCode(t, v.Enable("short"), CodePasswordTooShort)
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	st := v.Status()
	if !st.Enabled || st.Locked || st.Migration != "" || st.KDF != KDFName || st.Iterations != testIterations {
		t.Fatalf("status = %+v", st)
	}
	scan(t, root, secretMarkers)
	for name := range plainFiles {
		p := secretPath(root, name)
		if strings.Contains(name, ".tmp-") {
			if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("leftover %s was not deleted", name)
			}
			continue
		}
		if !IsEncrypted(raw(t, p)) {
			t.Errorf("%s is not encrypted", name)
		}
	}
	trash := filepath.Join(root, "workspaces", "ws1", "trash", "t1", "secrets", "req.json")
	if IsEncrypted(raw(t, trash)) {
		t.Error("a project folder named secrets in the trash was encrypted")
	}
	checkPlainFilesReadable(t, root)
	wantCode(t, v.Enable(password), CodeAlreadyEnabled)

	// A new write is encrypted, with a new nonce each time.
	p := secretPath(root, "requests/req_9.json")
	write(t, p, `{"values":{"auth.token":"new-HHHH"}}`)
	first := raw(t, p)
	write(t, p, `{"values":{"auth.token":"new-HHHH"}}`)
	if bytes.Equal(first, raw(t, p)) {
		t.Error("two writes of the same content are the same bytes")
	}
	scan(t, root, append(secretMarkers, "HHHH"))
	if got := read(t, p); got != `{"values":{"auth.token":"new-HHHH"}}` {
		t.Errorf("read back %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) == 0 || changes[len(changes)-1].Locked {
		t.Errorf("changes = %+v", changes)
	}
}

func TestVaultFileHoldsNoKey(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(raw(t, filepath.Join(v.Root(), filepath.FromSlash(ConfigFile))), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.KDF.Name != "pbkdf2" || cfg.KDF.Hash != "sha256" || cfg.KDF.Iterations != testIterations || len(cfg.KeyID) != 16 {
		t.Errorf("config = %+v", cfg)
	}
	v.mu.Lock()
	key := string(v.key)
	v.mu.Unlock()
	if strings.Contains(string(raw(t, v.config)), key) {
		t.Error("vault.json holds the data key")
	}
	if Open(v.Root(), Options{}).opts.Iterations != DefaultIterations {
		t.Error("default iterations")
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, `{"values":{"auth.token":"s3cret"}}`)
	v.Lock()
	if st := v.Status(); !st.Locked {
		t.Fatalf("status = %+v", st)
	}
	for _, pw := range []string{"", "wrong password", password + " ", strings.ToUpper(password)} {
		wantCode(t, v.Unlock(pw), CodeWrongPassword)
		if !v.Status().Locked {
			t.Fatalf("unlocked with %q", pw)
		}
	}
	wantCode(t, v.UnlockWithKey(make([]byte, 32)), CodeWrongPassword)
	wantCode(t, v.UnlockWithKey([]byte("short")), CodeWrongPassword)
	_, err := ReadFile(p)
	wantCode(t, err, CodeLocked)
	if !IsLocked(err) || !IsError(err) {
		t.Error("IsLocked/IsError")
	}

	// A new process: the right password unlocks it.
	v2 := openVault(t, v.Root(), Options{})
	if st := v2.Status(); !st.Enabled || !st.Locked {
		t.Fatalf("status after restart = %+v", st)
	}
	wantCode(t, v2.Unlock("not it at all"), CodeWrongPassword)
	if err := v2.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != `{"values":{"auth.token":"s3cret"}}` {
		t.Errorf("read %q", got)
	}
}

func TestLockedRefusesReadsAndWrites(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, "old")
	before := raw(t, p)
	v.Lock()
	_, err := ReadFile(p)
	wantCode(t, err, CodeLocked)
	wantCode(t, WriteFile(p, []byte("new"), 0o600), CodeLocked)
	if !bytes.Equal(before, raw(t, p)) {
		t.Error("a write while locked changed the file")
	}
	// A missing file is still missing, not locked.
	if _, err := ReadFile(secretPath(v.Root(), "requests/none.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if err := v.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "old" {
		t.Errorf("read %q", got)
	}
}

func TestTamperedTruncatedAndSwappedFilesAreRejected(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	a := secretPath(v.Root(), "requests/req_a.json")
	b := secretPath(v.Root(), "requests/req_b.json")
	write(t, a, `{"values":{"auth.token":"aaaa"}}`)
	write(t, b, `{"values":{"auth.token":"bbbb"}}`)
	good := raw(t, a)

	var env envelope
	if err := json.Unmarshal(good, &env); err != nil {
		t.Fatal(err)
	}
	flip := func(s string) string {
		b := []byte(s)
		if b[10] == 'A' {
			b[10] = 'B'
		} else {
			b[10] = 'A'
		}
		return string(b)
	}
	marshal := func(e envelope) []byte { out, _ := json.Marshal(e); return out }
	tampered := env
	tampered.Data = flip(env.Data)
	badNonce := env
	badNonce.Nonce = flip(env.Nonce)
	otherCipher := env
	otherCipher.Cipher = "AES-128-CBC"

	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"tampered ciphertext", marshal(tampered), CodeCorrupt},
		{"tampered nonce", marshal(badNonce), CodeCorrupt},
		{"other cipher", marshal(otherCipher), CodeCorrupt},
		{"truncated", good[:len(good)/2], CodeCorrupt},
		{"truncated to the prefix", good[:len(envelopePrefix)+1], CodeCorrupt},
		{"swapped", raw(t, b), CodeCorrupt},
		{"plain file while every file is encrypted", []byte(`{"values":{"auth.token":"x"}}`), CodeCorrupt},
		{"other key", func() []byte { e := env; e.KeyID = "0011223344556677"; return marshal(e) }(), CodeOtherKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(a, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadFile(a)
			wantCode(t, err, c.code)
		})
	}
	if err := os.WriteFile(a, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != `{"values":{"auth.token":"aaaa"}}` {
		t.Errorf("read %q", got)
	}
}

func TestEncryptedFileWithoutVault(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, "x")
	// Copied somewhere no vault covers.
	other := filepath.Join(t.TempDir(), "secrets", "requests", "req_1.json")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, raw(t, p), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFile(other)
	wantCode(t, err, CodeNotSetUp)

	// vault.json deleted by hand.
	if err := os.Remove(v.config); err != nil {
		t.Fatal(err)
	}
	v2 := openVault(t, v.Root(), Options{})
	_, err = ReadFile(p)
	wantCode(t, err, CodeNotSetUp)
	if st := v2.Status(); st.Enabled {
		t.Errorf("status = %+v", st)
	}
}

func TestBrokenConfig(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"not json":    "{",
		"version":     `{"version":2}`,
		"iterations":  `{"version":1,"keyId":"0011223344556677","kdf":{"name":"pbkdf2","hash":"sha256","iterations":0,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="}}`,
		"kdf":         `{"version":1,"keyId":"0011223344556677","kdf":{"name":"scrypt","hash":"sha256","iterations":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="}}`,
		"key id":      `{"version":1,"keyId":"zz","kdf":{"name":"pbkdf2","hash":"sha256","iterations":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="}}`,
		"short salt":  `{"version":1,"keyId":"0011223344556677","kdf":{"name":"pbkdf2","hash":"sha256","iterations":1,"salt":"AAAA"}}`,
		"migration":   `{"version":1,"keyId":"0011223344556677","kdf":{"name":"pbkdf2","hash":"sha256","iterations":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="},"migration":"x"}`,
		"too many it": fmt.Sprintf(`{"version":1,"keyId":"0011223344556677","kdf":{"name":"pbkdf2","hash":"sha256","iterations":%d,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="}}`, MaxIterations+1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cfg := filepath.Join(root, filepath.FromSlash(ConfigFile))
			if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			v := openVault(t, root, Options{})
			st := v.Status()
			if !st.Enabled || !st.Locked || st.Problem == nil {
				t.Fatalf("status = %+v", st)
			}
			wantCode(t, v.Unlock(password), CodeConfigInvalid)
			wantCode(t, WriteFile(secretPath(root, "requests/r.json"), []byte("x"), 0o600), CodeConfigInvalid)
			if err := v.Reset(); err != nil {
				t.Fatal(err)
			}
			if st := v.Status(); st.Enabled || st.Problem != nil {
				t.Errorf("after reset = %+v", st)
			}
		})
	}
}

func TestChangePassword(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(v.Root(), "environments/env_1.json")
	write(t, p, "value")
	before := raw(t, p)
	cfgBefore := raw(t, v.config)

	wantCode(t, v.ChangePassword("wrong password", "new password 1"), CodeWrongPassword)
	wantCode(t, v.ChangePassword(password, "short"), CodePasswordTooShort)
	if !bytes.Equal(cfgBefore, raw(t, v.config)) {
		t.Fatal("a refused change rewrote vault.json")
	}
	if err := v.ChangePassword(password, "new password 1"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, raw(t, p)) {
		t.Error("changing the password rewrote a secret file")
	}
	var a, b config
	_ = json.Unmarshal(cfgBefore, &a)
	_ = json.Unmarshal(raw(t, v.config), &b)
	if a.KDF.Salt == b.KDF.Salt || a.WrappedKey == b.WrappedKey || a.KeyID != b.KeyID {
		t.Errorf("config before %+v after %+v", a, b)
	}

	v2 := openVault(t, v.Root(), Options{})
	wantCode(t, v2.Unlock(password), CodeWrongPassword)
	if err := v2.Unlock("new password 1"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "value" {
		t.Errorf("read %q", got)
	}
}

func TestChangePasswordIsAtomic(t *testing.T) {
	t.Parallel()
	v := newVault(t, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	// vault.json cannot be replaced: the old password keeps working.
	dir := filepath.Dir(v.config)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := v.ChangePassword(password, "new password 1"); err == nil {
		t.Skip("the directory is writable anyway (running as root?)")
	}
	_ = os.Chmod(dir, 0o700)
	v2 := openVault(t, v.Root(), Options{})
	if err := v2.Unlock(password); err != nil {
		t.Fatalf("old password after a failed change: %v", err)
	}
}

func TestDisable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlainFiles(t, root)
	v := openVault(t, root, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	wantCode(t, v.Disable("wrong password"), CodeWrongPassword)
	if err := v.Disable(password); err != nil {
		t.Fatal(err)
	}
	if st := v.Status(); st.Enabled || st.Locked {
		t.Errorf("status = %+v", st)
	}
	if _, err := os.Stat(v.config); !errors.Is(err, fs.ErrNotExist) {
		t.Error("vault.json is still there")
	}
	for name, content := range plainFiles {
		if strings.Contains(name, ".tmp-") {
			continue
		}
		if got := string(raw(t, secretPath(root, name))); got != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
	wantCode(t, v.Disable(password), CodeNotEnabled)
	// Writes are plain again.
	p := secretPath(root, "requests/req_1.json")
	write(t, p, "plain")
	if string(raw(t, p)) != "plain" {
		t.Error("write after disable is not plain")
	}
}

func TestRequired(t *testing.T) {
	t.Parallel()
	var req bool
	var mu sync.Mutex
	v := newVault(t, Options{Required: func() bool { mu.Lock(); defer mu.Unlock(); return req }})
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, "plain while not required")
	mu.Lock()
	req = true
	mu.Unlock()
	if !v.Status().Required {
		t.Error("status not required")
	}
	wantCode(t, WriteFile(p, []byte("x"), 0o600), CodeRequired)
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	write(t, p, "encrypted")
	wantCode(t, v.Disable(password), CodeRequired)
	if !v.Status().Enabled {
		t.Error("disabled though required")
	}
}

func TestIdleTimeout(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	timeout := 5 * time.Minute
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	var locks int
	v := newVault(t, Options{
		Now:         clock,
		IdleTimeout: func() time.Duration { return timeout },
		OnChange: func(s Status) {
			if s.Locked {
				mu.Lock()
				locks++
				mu.Unlock()
			}
		},
	})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(v.Root(), "requests/req_1.json")
	write(t, p, "x")
	if got := v.Status().IdleTimeout; got != timeout {
		t.Errorf("IdleTimeout = %v", got)
	}

	// Each use restarts the timer.
	advance(4 * time.Minute)
	read(t, p)
	advance(4 * time.Minute)
	if v.LockIfIdle() {
		t.Fatal("locked 4 minutes after the last use")
	}
	read(t, p)
	advance(5 * time.Minute)
	if !v.LockIfIdle() {
		t.Fatal("not locked after the timeout")
	}
	if v.LockIfIdle() {
		t.Error("locked twice")
	}
	_, err := ReadFile(p)
	wantCode(t, err, CodeLocked)

	// A use after the timeout finds it locked, without the timer; the
	// timer's next call reports it (a read never calls OnChange).
	if err := v.Unlock(password); err != nil {
		t.Fatal(err)
	}
	advance(6 * time.Minute)
	_, err = ReadFile(p)
	wantCode(t, err, CodeLocked)
	mu.Lock()
	if locks != 1 {
		t.Errorf("OnChange saw %d locks before the timer, want 1", locks)
	}
	mu.Unlock()
	if !v.LockIfIdle() {
		t.Error("LockIfIdle did not report the lock a read found")
	}
	if v.LockIfIdle() {
		t.Error("reported twice")
	}
	mu.Lock()
	defer mu.Unlock()
	if locks != 2 {
		t.Errorf("OnChange saw %d locks, want 2", locks)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()
	ks := newMemKeyStore()
	root := t.TempDir()
	v := openVault(t, root, Options{KeyStore: ks})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	enc := secretPath(root, "requests/req_1.json")
	write(t, enc, "secret")
	if err := v.RememberKey(); err != nil {
		t.Fatal(err)
	}
	// A plain file left by an interrupted migration stays.
	plain := secretPath(root, "requests/req_2.json")
	if err := os.WriteFile(plain, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if err := v.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(enc); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the encrypted file is still there")
	}
	if _, err := os.Stat(v.config); !errors.Is(err, fs.ErrNotExist) {
		t.Error("vault.json is still there")
	}
	if string(raw(t, plain)) != "plain" {
		t.Error("the plain file is gone")
	}
	if ks.count() != 0 {
		t.Error("the key store still has the key")
	}
	if st := v.Status(); st.Enabled || st.Locked {
		t.Errorf("status = %+v", st)
	}
	// A fresh start works.
	if err := v.Enable("another password"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, plain); got != "plain" {
		t.Errorf("read %q", got)
	}
}

type memKeyStore struct {
	mu   sync.Mutex
	keys map[string][]byte
}

func newMemKeyStore() *memKeyStore { return &memKeyStore{keys: map[string][]byte{}} }

func (m *memKeyStore) LoadKey(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.keys[id]...), nil
}

func (m *memKeyStore) StoreKey(id string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[id] = append([]byte(nil), key...)
	return nil
}

func (m *memKeyStore) DeleteKey(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, id)
	return nil
}

func (m *memKeyStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.keys)
}

// The seam for the OS keychain: a key store unlocks without the password.
func TestKeyStoreSeam(t *testing.T) {
	t.Parallel()
	ks := newMemKeyStore()
	root := t.TempDir()
	v := openVault(t, root, Options{KeyStore: ks})
	if ok, err := v.UnlockFromKeyStore(); ok || err != nil {
		t.Fatalf("off: %v %v", ok, err)
	}
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(root, "requests/req_1.json")
	write(t, p, "x")
	if err := v.RememberKey(); err != nil {
		t.Fatal(err)
	}

	v2 := openVault(t, root, Options{KeyStore: ks})
	ok, err := v2.UnlockFromKeyStore()
	if !ok || err != nil {
		t.Fatalf("UnlockFromKeyStore = %v, %v", ok, err)
	}
	if got := read(t, p); got != "x" {
		t.Errorf("read %q", got)
	}

	// A wrong key in the store is refused.
	v2.Lock()
	for id := range ks.keys {
		ks.keys[id] = make([]byte, 32)
	}
	ok, err = v2.UnlockFromKeyStore()
	if ok {
		t.Fatal("unlocked with a wrong key")
	}
	wantCode(t, err, CodeWrongPassword)

	// Without a key store the password is needed.
	v3 := openVault(t, root, Options{})
	if ok, err := v3.UnlockFromKeyStore(); ok || err != nil {
		t.Fatalf("no key store: %v %v", ok, err)
	}
	if err := v3.RememberKey(); err == nil {
		t.Error("RememberKey without a key store")
	}
	if err := v3.ForgetKey(); err != nil {
		t.Errorf("ForgetKey without a key store: %v", err)
	}

	// ForgetKey leaves the password as the only way in.
	if err := v2.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if err := v2.RememberKey(); err != nil {
		t.Fatal(err)
	}
	if err := v2.ForgetKey(); err != nil {
		t.Fatal(err)
	}
	if ks.count() != 0 {
		t.Error("the key is still stored")
	}
	if st := v2.Status(); st.Locked {
		t.Error("ForgetKey locked the vault")
	}
	v2.Lock()
	if ok, err := v2.UnlockFromKeyStore(); ok || err != nil {
		t.Fatalf("after ForgetKey: %v %v", ok, err)
	}
}

// Writes through WriteFile while encryption is turned on are never lost
// and never left plain.
func TestConcurrentWritesDuringEnable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	v := openVault(t, root, Options{})
	paths := make([]string, 4)
	for i := range paths {
		paths[i] = secretPath(root, fmt.Sprintf("requests/req_%d.json", i))
		write(t, paths[i], "v0")
	}
	var wg sync.WaitGroup
	final := make([]string, len(paths))
	for i, p := range paths {
		wg.Go(func() {
			for n := 1; n <= 10; n++ {
				val := fmt.Sprintf("v%d-IIII", n)
				if err := WriteFile(p, []byte(val), 0o600); err != nil {
					t.Error(err)
					return
				}
				if _, err := ReadFile(p); err != nil {
					t.Error(err)
					return
				}
				final[i] = val
			}
		})
	}
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for i, p := range paths {
		if got := read(t, p); got != final[i] {
			t.Errorf("%s = %q, want %q", p, got, final[i])
		}
		if !IsEncrypted(raw(t, p)) {
			t.Errorf("%s is plain", p)
		}
	}
	scan(t, root, []string{"IIII"})
}

func TestSecretName(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]string{
		filepath.FromSlash("secrets/app.json"):                         "app.json",
		filepath.FromSlash("workspaces/ws/secrets/requests/r.json"):    "requests/r.json",
		filepath.FromSlash("workspaces/other/secrets/requests/r.json"): "requests/r.json",
		"loose.json": "loose.json",
	} {
		if got := secretName(rel); got != want {
			t.Errorf("secretName(%q) = %q, want %q", rel, got, want)
		}
	}
}

// A data directory moved elsewhere (SET-03) still opens.
func TestMovedDataDirectory(t *testing.T) {
	t.Parallel()
	old := t.TempDir()
	v := Open(old, Options{Iterations: testIterations})
	unregister := Register(v)
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	p := secretPath(old, "requests/req_1.json")
	write(t, p, "moved")
	unregister()

	moved := filepath.Join(t.TempDir(), "data")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	v2 := openVault(t, moved, Options{})
	if err := v2.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if got := read(t, secretPath(moved, "requests/req_1.json")); got != "moved" {
		t.Errorf("read %q", got)
	}
}
