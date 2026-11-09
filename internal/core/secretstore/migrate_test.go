package secretstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errCrash = errors.New("simulated crash")

// migrationSteps runs fn (Enable or Disable on a fresh vault over a copy
// of the plain files) once to count its steps, and returns them in order.
func migrationSteps(t *testing.T, prepare func(t *testing.T, root string) *Vault, fn func(*Vault) error) []string {
	t.Helper()
	root := t.TempDir()
	v := prepare(t, root)
	var steps []string
	v.hook = func(step, path string) error {
		steps = append(steps, step+" "+filepath.Base(path))
		return nil
	}
	if err := fn(v); err != nil {
		t.Fatal(err)
	}
	return steps
}

// A crash at every step of turning encryption on loses nothing: after a
// restart the plain files still read, the vault is locked with the
// "encrypting" mark, and the next unlock finishes the job.
func TestEnableInterruptedAtEachStep(t *testing.T) {
	t.Parallel()
	prepare := func(t *testing.T, root string) *Vault {
		writePlainFiles(t, root)
		return openVault(t, root, Options{})
	}
	enable := func(v *Vault) error { return v.Enable(password) }
	steps := migrationSteps(t, prepare, enable)
	if len(steps) < 3 || steps[0] != "config vault.json" || steps[len(steps)-1] != "finish vault.json" {
		t.Fatalf("steps = %q", steps)
	}
	for i, step := range steps {
		t.Run(fmt.Sprintf("%d %s", i, step), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			v := prepare(t, root)
			n := 0
			v.hook = func(string, string) error {
				if n == i {
					return errCrash
				}
				n++
				return nil
			}
			if err := enable(v); !errors.Is(err, errCrash) {
				t.Fatalf("Enable = %v", err)
			}

			// Restart: a new vault over the same files.
			v2 := openVault(t, root, Options{})
			st := v2.Status()
			if !st.Enabled || !st.Locked || st.Migration != MigrationEncrypting {
				t.Fatalf("status after the crash = %+v", st)
			}
			// Whatever is still plain reads while locked; what is
			// encrypted says it is locked. Nothing is lost.
			for name := range plainFiles {
				if strings.Contains(name, ".tmp-") {
					continue
				}
				p := secretPath(root, name)
				if _, err := ReadFile(p); err != nil && !IsLocked(err) {
					t.Errorf("%s: %v", name, err)
				}
			}
			wantCode(t, v2.Unlock("wrong password"), CodeWrongPassword)
			if err := v2.Unlock(password); err != nil {
				t.Fatal(err)
			}
			if st := v2.Status(); st.Migration != "" || st.Locked {
				t.Fatalf("status after unlock = %+v", st)
			}
			scan(t, root, secretMarkers)
			checkPlainFilesReadable(t, root)
		})
	}
}

// The same for turning it off: after a crash the vault is locked with the
// "decrypting" mark, and the next unlock finishes, leaving plain files.
func TestDisableInterruptedAtEachStep(t *testing.T) {
	t.Parallel()
	prepare := func(t *testing.T, root string) *Vault {
		writePlainFiles(t, root)
		v := openVault(t, root, Options{})
		if err := v.Enable(password); err != nil {
			t.Fatal(err)
		}
		return v
	}
	disable := func(v *Vault) error { return v.Disable(password) }
	steps := migrationSteps(t, prepare, disable)
	if len(steps) < 3 || steps[0] != "config vault.json" || steps[len(steps)-1] != "finish vault.json" {
		t.Fatalf("steps = %q", steps)
	}
	for i, step := range steps {
		t.Run(fmt.Sprintf("%d %s", i, step), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			v := prepare(t, root)
			n := 0
			v.hook = func(string, string) error {
				if n == i {
					return errCrash
				}
				n++
				return nil
			}
			if err := disable(v); !errors.Is(err, errCrash) {
				t.Fatalf("Disable = %v", err)
			}
			v2 := openVault(t, root, Options{})
			st := v2.Status()
			if !st.Enabled || !st.Locked || st.Migration != MigrationDecrypting {
				t.Fatalf("status after the crash = %+v", st)
			}
			if err := v2.Unlock(password); err != nil {
				t.Fatal(err)
			}
			if st := v2.Status(); st.Enabled {
				t.Fatalf("status after unlock = %+v", st)
			}
			if _, err := os.Stat(v2.config); !errors.Is(err, fs.ErrNotExist) {
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
		})
	}
}

// A crash inside an atomic write of a plain file (before encryption was
// on) leaves a temp file with the secret in the clear; turning encryption
// on deletes it. So does a crash while writing an encrypted file, whose
// leftover is encrypted anyway.
func TestLeftoverTempFilesAreDeleted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	v := openVault(t, root, Options{})
	p := secretPath(root, "environments/.env_1.json.tmp-12345")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"values":{"x":"torn-JJJJ"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the leftover temp file is still there")
	}
	scan(t, root, []string{"JJJJ"})
}

// A file that cannot be decrypted stops turning encryption off, with the
// mark kept; deleting the file and unlocking again finishes it.
func TestDisableWithADamagedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	v := openVault(t, root, Options{})
	if err := v.Enable(password); err != nil {
		t.Fatal(err)
	}
	good := secretPath(root, "requests/req_1.json")
	bad := secretPath(root, "requests/req_2.json")
	write(t, good, "good")
	write(t, bad, "bad")
	data := raw(t, bad)
	if err := os.WriteFile(bad, data[:len(data)-10], 0o600); err != nil {
		t.Fatal(err)
	}
	wantCode(t, v.Disable(password), CodeCorrupt)
	if st := v.Status(); !st.Enabled || st.Migration != MigrationDecrypting {
		t.Fatalf("status = %+v", st)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if err := v.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if st := v.Status(); st.Enabled {
		t.Fatalf("status = %+v", st)
	}
	if string(raw(t, good)) != "good" {
		t.Error("good file")
	}
}
