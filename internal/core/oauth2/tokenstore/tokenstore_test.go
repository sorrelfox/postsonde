package tokenstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
)

func token(n int) oauth2.Token {
	obtained := time.Date(2026, 9, 23, 12, 0, 0, 123456789, time.UTC)
	return oauth2.Token{
		AccessToken: fmt.Sprintf("access-%d", n), TokenType: "Bearer", ExpiresIn: 3600, Expiry: obtained.Add(time.Hour),
		RefreshToken: fmt.Sprintf("refresh-%d", n), Scope: "read", IDToken: fmt.Sprintf("id-%d", n), Obtained: obtained,
	}
}

func dirOf(dir string) LocalDir { return func() (string, error) { return dir, nil } }

// Tokens are kept by key, survive a new Store on the same directory (a
// restart) with every member, and live in the local directory's file,
// 0600; removing the last one removes the file.
func TestStorePersists(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(dirOf(local))
	s.Put("key-a", token(1))
	s.Put("key-b", token(2))
	if got, ok := s.Get("key-a"); !ok || !got.Expiry.Equal(token(1).Expiry) || got.AccessToken != "access-1" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}

	restarted := New(dirOf(local))
	for _, k := range []struct {
		key string
		n   int
	}{{"key-a", 1}, {"key-b", 2}} {
		got, ok := restarted.Get(k.key)
		want := token(k.n)
		if !ok || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.IDToken != want.IDToken ||
			got.TokenType != want.TokenType || got.Scope != want.Scope || got.ExpiresIn != want.ExpiresIn ||
			!got.Expiry.Equal(want.Expiry) || !got.Obtained.Equal(want.Obtained) {
			t.Errorf("after a restart %s = %+v, want %+v", k.key, got, want)
		}
	}
	if _, ok := restarted.Get("key-c"); ok {
		t.Error("an unknown key has a token")
	}

	path := filepath.Join(local, "secrets", "oauth2", "tokens.json")
	if s.Path() != path {
		t.Errorf("Path = %s, want %s", s.Path(), path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", info.Mode().Perm())
		}
		if d, _ := os.Stat(filepath.Dir(path)); d.Mode().Perm() != 0o700 {
			t.Errorf("directory mode = %v", d.Mode().Perm())
		}
	}

	restarted.Delete("key-a")
	restarted.Delete("key-a") // twice: nothing to do
	if _, ok := New(dirOf(local)).Get("key-a"); ok {
		t.Error("a deleted token came back")
	}
	restarted.Delete("key-b")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is left without tokens: %v", err)
	}
}

// Each workspace has its own tokens; without one they are kept in memory.
func TestStorePerWorkspace(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	current, open := a, true
	s := New(func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if !open {
			return "", errors.New("no workspace open")
		}
		return current, nil
	})
	s.Put("k", token(1))
	mu.Lock()
	current = b
	mu.Unlock()
	if _, ok := s.Get("k"); ok {
		t.Error("workspace b sees workspace a's token")
	}
	s.Put("k", token(2))
	mu.Lock()
	current = a
	mu.Unlock()
	if got, _ := s.Get("k"); got.AccessToken != "access-1" {
		t.Errorf("workspace a's token = %+v", got)
	}

	mu.Lock()
	open = false
	mu.Unlock()
	s.Put("mem", token(3))
	if got, ok := s.Get("mem"); !ok || got.AccessToken != "access-3" || s.Path() != "" {
		t.Errorf("in memory = %+v %v, path %q", got, ok, s.Path())
	}
	for _, dir := range []string{a, b} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(FileName)))
		if err != nil || strings.Contains(string(data), "access-3") {
			t.Errorf("%s: %s, %v", dir, data, err)
		}
	}
}

// A file that cannot be parsed gives no tokens and is reported; the next
// token replaces it.
func TestStoreBrokenFile(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	path := filepath.Join(local, filepath.FromSlash(FileName))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reported []error
	s := New(dirOf(local), WithOnError(func(err error) { reported = append(reported, err) }))
	if _, ok := s.Get("k"); ok {
		t.Error("a token from a broken file")
	}
	if len(reported) != 1 || errs.KindOf(reported[0]) != errs.Parse {
		t.Fatalf("reported %v", reported)
	}
	var e *errs.Error
	if !errors.As(reported[0], &e) || e.Code != CodeLocalDataFailed || e.Details["path"] != path {
		t.Errorf("error = %+v", reported[0])
	}
	s.Put("k", token(1))
	if got, ok := New(dirOf(local)).Get("k"); !ok || got.AccessToken != "access-1" {
		t.Errorf("after the rewrite = %+v, %v", got, ok)
	}
}

// A write that fails keeps the token for the session and is reported.
func TestStoreWriteFails(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	// A file where the secrets directory should be.
	if err := os.WriteFile(filepath.Join(local, "secrets"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var reported []error
	s := New(dirOf(local), WithOnError(func(err error) { reported = append(reported, err) }))
	s.Put("k", token(1))
	if got, ok := s.Get("k"); !ok || got.AccessToken != "access-1" {
		t.Errorf("Get = %+v, %v", got, ok)
	}
	// The read fails too (not a directory), then the write.
	if len(reported) == 0 || !strings.Contains(reported[len(reported)-1].Error(), "could not store") {
		t.Errorf("reported %v", reported)
	}
}

// Clear removes every token of the workspace and its file; List and
// Values list what is stored, Values every token of each.
func TestStoreClearListValues(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(dirOf(local))
	s.Put("b", token(2))
	s.Put("a", token(1))
	list := s.List()
	if len(list) != 2 || list[0].Key != "a" || list[1].Key != "b" {
		t.Errorf("List = %+v", list)
	}
	values := s.Values()
	slices.Sort(values)
	if want := []string{"access-1", "access-2", "id-1", "id-2", "refresh-1", "refresh-2"}; !slices.Equal(values, want) {
		t.Errorf("Values = %q", values)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 || len(New(dirOf(local)).List()) != 0 {
		t.Error("tokens left after Clear")
	}
	if _, err := os.Stat(filepath.Join(local, filepath.FromSlash(FileName))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file left after Clear: %v", err)
	}
}

// Safe for concurrent use (run with -race).
func TestStoreConcurrent(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(dirOf(local))
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			key := fmt.Sprintf("k%d", i%3)
			for j := range 8 {
				s.Put(key, token(j))
				s.Get(key)
				if j%3 == 0 {
					s.Delete(key)
				}
				s.Values()
			}
		})
	}
	wg.Wait()
	for _, e := range s.List() {
		if got, ok := New(dirOf(local)).Get(e.Key); !ok || got.AccessToken != e.Token.AccessToken {
			t.Errorf("%s on disk = %+v, in memory %+v", e.Key, got, e.Token)
		}
	}
}
