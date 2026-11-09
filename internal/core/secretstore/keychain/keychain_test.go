package keychain

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

const keyID = "0123456789abcdef"

var key = bytes.Repeat([]byte{0xa5, 0x5a}, 16)

// call is one command a fake runner saw.
type call struct {
	stdin string
	name  string
	args  []string
}

// fakeRunner answers each command with the next of its outputs and
// records the calls.
type fakeRunner struct {
	calls   []call
	answer  func(c call) (Output, error)
	results []Output
}

func (f *fakeRunner) run(_ context.Context, stdin []byte, name string, args ...string) (Output, error) {
	c := call{stdin: string(stdin), name: name, args: slices.Clone(args)}
	f.calls = append(f.calls, c)
	if f.answer != nil {
		return f.answer(c)
	}
	if len(f.results) == 0 {
		return Output{}, nil
	}
	out := f.results[0]
	f.results = f.results[1:]
	return out, nil
}

// noKeyInArgs fails when the key shows up in a command line.
func noKeyInArgs(t *testing.T, calls []call) {
	t.Helper()
	hexKey := hex.EncodeToString(key)
	for _, c := range calls {
		if strings.Contains(strings.Join(c.args, " "), hexKey) {
			t.Errorf("the key is in the command line: %s %v", c.name, c.args)
		}
	}
}

func wantCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
	if e.Hint == "" {
		t.Errorf("%s: no hint", code)
	}
	return e
}

var _ secretstore.KeyStore = (*Security)(nil)
var _ KeyStore = (*SecretTool)(nil)
var _ KeyStore = (*DPAPI)(nil)

func TestSecurityCommands(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{}
	s := &Security{Run: f.run, Path: "/usr/bin/security"}

	// Store: one add-generic-password command on the standard input of
	// security -i, the key only there.
	if err := s.StoreKey(keyID, key); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.name != "/usr/bin/security" || !slices.Equal(c.args, []string{"-i"}) {
		t.Errorf("store ran %s %v", c.name, c.args)
	}
	wantStdin := `add-generic-password -U -s "io.github.sorrelfox.PostSonde.secrets" -a vault-0123456789abcdef -l "PostSonde secrets key" -w ` + hex.EncodeToString(key) + "\n"
	if c.stdin != wantStdin {
		t.Errorf("store stdin = %q, want %q", c.stdin, wantStdin)
	}

	// Load: the key from find-generic-password -w's output.
	f.results = []Output{{Stdout: []byte(hex.EncodeToString(key) + "\n")}}
	got, err := s.LoadKey(keyID)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("LoadKey = %x, %v", got, err)
	}
	if c := f.calls[1]; !slices.Equal(c.args, []string{"find-generic-password", "-s", Service, "-a", "vault-" + keyID, "-w"}) || c.stdin != "" {
		t.Errorf("load ran %v with %q", c.args, c.stdin)
	}

	// Not found (errSecItemNotFound, exit 44): no key, no error.
	f.results = []Output{{ExitCode: 44, Stderr: []byte("security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.")}}
	if got, err := s.LoadKey(keyID); got != nil || err != nil {
		t.Errorf("not found: %x, %v", got, err)
	}

	// Delete, and deleting what is not there.
	f.results = []Output{{}, {ExitCode: 44}}
	if err := s.DeleteKey(keyID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(keyID); err != nil {
		t.Errorf("delete not found: %v", err)
	}
	if c := f.calls[3]; !slices.Equal(c.args, []string{"delete-generic-password", "-s", Service, "-a", "vault-" + keyID}) {
		t.Errorf("delete ran %v", c.args)
	}
	noKeyInArgs(t, f.calls)
}

func TestSecurityErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		out  Output
		kind errs.Kind
	}{
		{"canceled", Output{ExitCode: 128, Stderr: []byte("User canceled the operation.")}, errs.PermissionDenied},
		{"interaction not allowed", Output{ExitCode: 36}, errs.PermissionDenied},
		{"other", Output{ExitCode: 1, Stderr: []byte("bad")}, errs.Internal},
	} {
		f := &fakeRunner{results: []Output{tc.out, tc.out}}
		s := &Security{Run: f.run, Path: "/x/security"}
		_, err := s.LoadKey(keyID)
		e := wantCode(t, err, CodeFailed)
		if e.Kind != tc.kind {
			t.Errorf("%s: kind %s, want %s", tc.name, e.Kind, tc.kind)
		}
		wantCode(t, s.StoreKey(keyID, key), CodeFailed)
	}
	// Not hex: invalid.
	f := &fakeRunner{results: []Output{{Stdout: []byte("not a key")}}}
	_, err := (&Security{Run: f.run, Path: "/x/security"}).LoadKey(keyID)
	wantCode(t, err, CodeInvalid)
	// The command cannot run.
	f = &fakeRunner{answer: func(call) (Output, error) { return Output{}, os.ErrNotExist }}
	_, err = (&Security{Run: f.run, Path: "/x/security"}).LoadKey(keyID)
	wantCode(t, err, CodeFailed)
	// A key ID that is not hex never reaches a command.
	f = &fakeRunner{}
	if _, err := (&Security{Run: f.run, Path: "/x/security"}).LoadKey("x -a y"); err == nil || len(f.calls) != 0 {
		t.Errorf("bad key ID: %v, %d calls", err, len(f.calls))
	}
}

func TestSecretToolCommands(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{}
	s := &SecretTool{Run: f.run, LookPath: func(string) (string, error) { return "/usr/bin/secret-tool", nil }}
	attrs := []string{"application", Service, "account", "vault-" + keyID}

	if err := s.StoreKey(keyID, key); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.name != "/usr/bin/secret-tool" || !slices.Equal(c.args, append([]string{"store", "--label=PostSonde secrets key"}, attrs...)) {
		t.Errorf("store ran %s %v", c.name, c.args)
	}
	if c.stdin != hex.EncodeToString(key) {
		t.Errorf("store stdin = %q", c.stdin)
	}

	f.results = []Output{{Stdout: []byte(hex.EncodeToString(key))}}
	got, err := s.LoadKey(keyID)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("LoadKey = %x, %v", got, err)
	}
	if c := f.calls[1]; !slices.Equal(c.args, append([]string{"lookup"}, attrs...)) {
		t.Errorf("lookup ran %v", c.args)
	}
	// secret-tool lookup exits 1 without a word when nothing matches.
	f.results = []Output{{ExitCode: 1}}
	if got, err := s.LoadKey(keyID); got != nil || err != nil {
		t.Errorf("not found: %x, %v", got, err)
	}
	f.results = []Output{{}, {ExitCode: 1}}
	if err := s.DeleteKey(keyID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(keyID); err != nil {
		t.Errorf("delete not found: %v", err)
	}
	if c := f.calls[3]; !slices.Equal(c.args, append([]string{"clear"}, attrs...)) {
		t.Errorf("clear ran %v", c.args)
	}
	noKeyInArgs(t, f.calls)
}

// Without secret-tool or a Secret Service, every call is CodeUnavailable,
// so the app falls back to the master password.
func TestSecretToolUnavailable(t *testing.T) {
	t.Parallel()
	missing := &SecretTool{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, err := missing.LoadKey(keyID)
	e := wantCode(t, err, CodeUnavailable)
	if !strings.Contains(e.Hint, "libsecret-tools") {
		t.Errorf("hint = %q", e.Hint)
	}
	wantCode(t, missing.StoreKey(keyID, key), CodeUnavailable)

	for _, stderr := range []string{
		"secret-tool: Cannot autolaunch D-Bus without X11 $DISPLAY",
		"secret-tool: The name org.freedesktop.secrets was not provided by any .service files",
	} {
		f := &fakeRunner{results: []Output{{ExitCode: 1, Stderr: []byte(stderr)}, {ExitCode: 1, Stderr: []byte(stderr)}}}
		s := &SecretTool{Run: f.run, LookPath: func(string) (string, error) { return "/bin/secret-tool", nil }}
		_, err := s.LoadKey(keyID)
		wantCode(t, err, CodeUnavailable)
		wantCode(t, s.StoreKey(keyID, key), CodeUnavailable)
	}

	// A refused unlock of the keyring.
	f := &fakeRunner{results: []Output{{ExitCode: 1, Stderr: []byte("secret-tool: Prompt dismissed")}}}
	s := &SecretTool{Run: f.run, LookPath: func(string) (string, error) { return "/bin/secret-tool", nil }}
	if e := wantCode(t, s.StoreKey(keyID, key), CodeFailed); e.Kind != errs.PermissionDenied {
		t.Errorf("kind = %s", e.Kind)
	}
}

// fakeSeal stands for DPAPI in tests: reversible, bound to the entropy.
func fakeSeal(data, entropy []byte) ([]byte, error) {
	out := append([]byte("sealed:"+string(entropy)+":"), data...)
	return out, nil
}

func fakeUnseal(blob, entropy []byte) ([]byte, error) {
	prefix := "sealed:" + string(entropy) + ":"
	if !bytes.HasPrefix(blob, []byte(prefix)) {
		return nil, errors.New("bad entropy")
	}
	return bytes.Clone(blob[len(prefix):]), nil
}

func TestDPAPIFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := &DPAPI{Dir: dir, Protect: fakeSeal, Unprotect: fakeUnseal}
	if got, err := d.LoadKey(keyID); got != nil || err != nil {
		t.Fatalf("empty: %x, %v", got, err)
	}
	if err := d.StoreKey(keyID, key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keychain", "vault-"+keyID+".dpapi")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, key) {
		t.Error("the file holds the raw key")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", st.Mode())
		}
	}
	got, err := d.LoadKey(keyID)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("LoadKey = %x, %v", got, err)
	}
	// A blob sealed for another key ID is refused.
	other := "fedcba9876543210"
	if err := os.WriteFile(filepath.Join(dir, "keychain", "vault-"+other+".dpapi"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = d.LoadKey(other)
	wantCode(t, err, CodeFailed)

	if err := d.DeleteKey(keyID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteKey(keyID); err != nil {
		t.Errorf("delete twice: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the key file is still there")
	}
	// No data directory: unavailable.
	_, err = (&DPAPI{Protect: fakeSeal, Unprotect: fakeUnseal}).LoadKey(keyID)
	wantCode(t, err, CodeUnavailable)
}

// Off Windows the real DPAPI is unavailable; on Windows it round-trips.
func TestDPAPIReal(t *testing.T) {
	t.Parallel()
	d := &DPAPI{Dir: t.TempDir()}
	err := d.StoreKey(keyID, key)
	if runtime.GOOS != "windows" {
		wantCode(t, err, CodeUnavailable)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadKey(keyID)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("LoadKey = %x, %v", got, err)
	}
}

func TestForOS(t *testing.T) {
	t.Parallel()
	for goos, want := range map[string]string{"darwin": "macOS Keychain", "windows": "Windows DPAPI", "linux": "Secret Service", "freebsd": "Secret Service"} {
		if got := forOS(goos, "/data").Name(); got != want {
			t.Errorf("%s: %s, want %s", goos, got, want)
		}
	}
}

// TestRun runs a real command through Run: standard input in, output and
// exit status out.
func TestRun(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	out, err := Run(context.Background(), []byte("hello"), "sh", "-c", "cat; echo oops >&2; exit 3")
	if err != nil || string(out.Stdout) != "hello" || strings.TrimSpace(string(out.Stderr)) != "oops" || out.ExitCode != 3 {
		t.Errorf("Run = %+v, %v", out, err)
	}
	if _, err := Run(context.Background(), nil, "/no/such/command"); err == nil {
		t.Error("a missing command ran")
	}
}

// TestSecurityRoundTrip stores, reads and deletes a key in the real macOS
// keychain, under a throwaway service name. It runs only with
// POSTSONDE_KEYCHAIN_TEST=1, since macOS may ask for access.
func TestSecurityRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("POSTSONDE_KEYCHAIN_TEST") != "1" {
		t.Skip("set POSTSONDE_KEYCHAIN_TEST=1 on macOS to use the real keychain")
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	s := &Security{Service: "io.github.sorrelfox.PostSonde.test-" + hex.EncodeToString(id)}
	t.Cleanup(func() {
		if err := s.DeleteKey(keyID); err != nil {
			t.Errorf("cleanup: %v (delete the item %q by hand)", err, s.Service)
		}
	})
	if got, err := s.LoadKey(keyID); got != nil || err != nil {
		t.Fatalf("before: %x, %v", got, err)
	}
	if err := s.StoreKey(keyID, key); err != nil {
		t.Fatal(err)
	}
	// Storing again replaces it.
	other := bytes.Repeat([]byte{7}, 32)
	if err := s.StoreKey(keyID, other); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadKey(keyID)
	if err != nil || !bytes.Equal(got, other) {
		t.Fatalf("LoadKey = %x, %v", got, err)
	}
	if err := s.DeleteKey(keyID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadKey(keyID); got != nil || err != nil {
		t.Fatalf("after delete: %x, %v", got, err)
	}
}
