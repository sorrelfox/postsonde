package secretstore_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

const password = "correct horse battery"

// fixture is a data directory with a workspace's local directory and a
// project, and a registered vault over the data directory.
type fixture struct {
	data, local, project string
	vault                *secretstore.Vault
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	data := t.TempDir()
	f := &fixture{data: data, local: filepath.Join(data, "workspaces", "abc123"), project: t.TempDir()}
	f.open(t)
	return f
}

// open opens the vault as a new start of the app would.
func (f *fixture) open(t *testing.T) {
	t.Helper()
	f.vault = secretstore.Open(f.data, secretstore.Options{Iterations: 1000})
	t.Cleanup(secretstore.Register(f.vault))
}

// kind is one kind of secret: write stores its secret through its store,
// check reads it back through the store.
type kind struct {
	name   string
	secret string
	write  func(t *testing.T, f *fixture) error
	read   func(t *testing.T, f *fixture) (string, error)
}

func kinds() []kind {
	return []kind{
		{
			name:   "request secret",
			secret: "req-token-KIND1",
			write: func(t *testing.T, f *fixture) error {
				return requestsecrets.New(f.local).Write("req_1", map[string]string{"auth.token": "req-token-KIND1"})
			},
			read: func(t *testing.T, f *fixture) (string, error) {
				v, err := requestsecrets.New(f.local).Read("req_1")
				return v["auth.token"], err
			},
		},
		{
			name:   "environment secret",
			secret: "env-secret-KIND2",
			write: func(t *testing.T, f *fixture) error {
				s := environments.New(f.project, f.local, false)
				snap, err := s.List()
				if err != nil {
					return err
				}
				var id string
				if len(snap.Environments) > 0 {
					id = snap.Environments[0].ID
				} else {
					env, err := s.Create("Staging")
					if err != nil {
						return err
					}
					id = env.ID
				}
				_, err = s.SaveVariables(id, []environments.Variable{{Name: "apiKey", Value: "env-secret-KIND2", Secret: true}})
				return err
			},
			read: func(t *testing.T, f *fixture) (string, error) {
				snap, err := environments.New(f.project, f.local, false).List()
				if err != nil {
					return "", err
				}
				for _, e := range snap.Environments {
					for _, v := range e.Variables {
						if v.Name == "apiKey" {
							return v.Value, nil
						}
					}
				}
				t.Fatal("no environment variable apiKey")
				return "", nil
			},
		},
		{
			name:   "app secret",
			secret: "proxy-pass-KIND3",
			write: func(t *testing.T, f *fixture) error {
				return appsecrets.New(f.data).Set(appsecrets.ProxyPasswordKey, "proxy-pass-KIND3")
			},
			read: func(t *testing.T, f *fixture) (string, error) {
				return appsecrets.New(f.data).Get(appsecrets.ProxyPasswordKey)
			},
		},
		{
			name:   "runtime variable secret",
			secret: "runtime-KIND4",
			write: func(t *testing.T, f *fixture) error {
				st, err := runtimevars.Open(f.local)
				if err != nil {
					return err
				}
				return st.Set(runtimevars.Variable{Name: "session", Value: "runtime-KIND4", Secret: true, Persist: true})
			},
			read: func(t *testing.T, f *fixture) (string, error) {
				st, err := runtimevars.Open(f.local)
				if err != nil {
					return "", err
				}
				v, _ := st.Get("session")
				return v.Value, nil
			},
		},
		{
			name:   "oauth2 token",
			secret: "access-token-KIND5",
			write: func(t *testing.T, f *fixture) error {
				var err error
				s := tokenstore.New(func() (string, error) { return f.local, nil }, tokenstore.WithOnError(func(e error) { err = e }))
				s.Put("key1", oauth2.Token{AccessToken: "access-token-KIND5", Expiry: time.Now().Add(time.Hour)})
				return err
			},
			read: func(t *testing.T, f *fixture) (string, error) {
				var err error
				s := tokenstore.New(func() (string, error) { return f.local, nil }, tokenstore.WithOnError(func(e error) { err = e }))
				tok, _ := s.Get("key1")
				return tok.AccessToken, err
			},
		},
	}
}

// scan fails when a file under root holds one of secrets.
func scan(t *testing.T, root string, secrets []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s holds %q in the clear", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func secretsOf(ks []kind) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.secret)
	}
	return out
}

// Every kind of secret round-trips through its store, and after
// encryption is turned on no secret byte is left in the local folder.
func TestEveryKindOfSecret(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ks := kinds()
	// Written plain, before encryption is on.
	for _, k := range ks {
		if err := k.write(t, f); err != nil {
			t.Fatalf("%s: write: %v", k.name, err)
		}
	}
	if err := f.vault.Enable(password); err != nil {
		t.Fatal(err)
	}
	scan(t, f.data, secretsOf(ks))
	for _, k := range ks {
		got, err := k.read(t, f)
		if err != nil || got != k.secret {
			t.Errorf("%s after enable: %q, %v", k.name, got, err)
		}
	}

	// Written again while encryption is on: still encrypted, and back
	// after a restart and an unlock.
	for _, k := range ks {
		if err := k.write(t, f); err != nil {
			t.Fatalf("%s: write while on: %v", k.name, err)
		}
	}
	scan(t, f.data, secretsOf(ks))
	f.open(t)
	if st := f.vault.Status(); !st.Locked {
		t.Fatalf("status after restart = %+v", st)
	}
	if err := f.vault.Unlock(password); err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		got, err := k.read(t, f)
		if err != nil || got != k.secret {
			t.Errorf("%s after restart: %q, %v", k.name, got, err)
		}
	}
}

// While locked, every store refuses with secretstore.locked (never
// garbage, never an empty value), and writes leave the files as they
// were.
func TestEveryStoreWhileLocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.vault.Enable(password); err != nil {
		t.Fatal(err)
	}
	ks := kinds()
	for _, k := range ks {
		if err := k.write(t, f); err != nil {
			t.Fatalf("%s: %v", k.name, err)
		}
	}
	snapshot := map[string][]byte{}
	_ = filepath.WalkDir(f.data, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			snapshot[path], _ = os.ReadFile(path)
		}
		return nil
	})
	f.vault.Lock()
	for _, k := range ks {
		_, err := k.read(t, f)
		if k.name == "oauth2 token" {
			// The token store never fails a send: a locked file has no
			// tokens (the send gets a new one).
			if err != nil {
				t.Errorf("%s: %v", k.name, err)
			}
		} else if !secretstore.IsLocked(err) {
			t.Errorf("%s: read while locked: %v", k.name, err)
		}
		err = k.write(t, f)
		if !secretstore.IsLocked(err) {
			t.Errorf("%s: write while locked: %v", k.name, err)
		}
		var e *errs.Error
		if errors.As(err, &e) && e.Hint == "" {
			t.Errorf("%s: no hint", k.name)
		}
	}
	for path, data := range snapshot {
		now, _ := os.ReadFile(path)
		if !bytes.Equal(now, data) {
			t.Errorf("%s changed while locked", path)
		}
	}
}

// OnChange may call a store that writes secrets under its own lock (the
// app drops the token store's memory there): a store writing while the
// vault changes state never deadlocks with it.
func TestOnChangeMayCallAStore(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	local := filepath.Join(data, "workspaces", "abc123")
	var tokens *tokenstore.Store
	var clock sync.Mutex
	now := time.Now()
	vault := secretstore.Open(data, secretstore.Options{
		Iterations:  1000,
		OnChange:    func(secretstore.Status) { tokens.Forget() },
		IdleTimeout: func() time.Duration { return time.Minute },
		Now:         func() time.Time { clock.Lock(); defer clock.Unlock(); return now },
	})
	t.Cleanup(secretstore.Register(vault))
	tokens = tokenstore.New(func() (string, error) { return local, nil })

	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tokens.Put("key", oauth2.Token{AccessToken: "tok-" + strconv.Itoa(i)})
			tokens.Get("key")
		}
	}()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for range 3 {
			if err := vault.Enable(password); err != nil {
				t.Error(err)
				return
			}
			vault.Lock()
			if err := vault.Unlock(password); err != nil {
				t.Error(err)
				return
			}
			if err := vault.ChangePassword(password, password+"!"); err != nil {
				t.Error(err)
				return
			}
			clock.Lock()
			now = now.Add(2 * time.Minute)
			clock.Unlock()
			vault.LockIfIdle()
			if err := vault.Unlock(password + "!"); err != nil {
				t.Error(err)
				return
			}
			if err := vault.Disable(password + "!"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock: the vault's changes did not finish")
	}
	close(stop)
	<-done
}

// Turning encryption off writes every kind back plain.
func TestEveryKindAfterDisable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.vault.Enable(password); err != nil {
		t.Fatal(err)
	}
	ks := kinds()
	for _, k := range ks {
		if err := k.write(t, f); err != nil {
			t.Fatalf("%s: %v", k.name, err)
		}
	}
	if err := f.vault.Disable(password); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	for _, k := range ks {
		got, err := k.read(t, f)
		if err != nil || got != k.secret {
			t.Errorf("%s after disable: %q, %v", k.name, got, err)
		}
	}
}
