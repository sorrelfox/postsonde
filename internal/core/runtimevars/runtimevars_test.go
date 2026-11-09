package runtimevars

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func code(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestStoreSessionAndPersisted(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	st, err := Open(local)
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	st.onChange = func(*Store) { changes++ }
	for _, v := range []Variable{
		{Name: "session", Value: "s1"},
		{Name: "kept", Value: "k1", Persist: true},
		{Name: "token", Value: "t0k3n-s3cret", Secret: true, Persist: true},
		{Name: "sessionSecret", Value: "sess-s3cret", Secret: true},
	} {
		if err := st.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if changes != 4 {
		t.Errorf("changes = %d, want 4", changes)
	}
	names := []string{}
	for _, v := range st.List() {
		names = append(names, v.Name)
	}
	if want := []string{"kept", "session", "sessionSecret", "token"}; !reflect.DeepEqual(names, want) {
		t.Errorf("List = %v, want %v", names, want)
	}
	sc := st.Scope()
	if sc.Name() != ScopeName {
		t.Errorf("scope name = %q", sc.Name())
	}
	if v, ok := sc.Lookup("token"); !ok || v.Value != "t0k3n-s3cret" || !v.Secret {
		t.Errorf("token = %+v, %v", v, ok)
	}
	if got := st.Secrets(); !reflect.DeepEqual(got, []string{"sess-s3cret", "t0k3n-s3cret"}) {
		t.Errorf("Secrets = %v", got)
	}

	// The list holds no secret value; the secrets file does.
	list, err := os.ReadFile(filepath.Join(local, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(list); strings.Contains(s, "s3cret") || strings.Contains(s, "session") || !strings.Contains(s, `"kept"`) {
		t.Errorf("%s = %s", FileName, s)
	}
	secrets, err := os.ReadFile(filepath.Join(local, "secrets", "runtime", SecretsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(secrets); !strings.Contains(s, "t0k3n-s3cret") || strings.Contains(s, "sess-s3cret") {
		t.Errorf("secrets file = %s", s)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{filepath.Join(local, FileName), filepath.Join(local, "secrets", "runtime", SecretsFileName)} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v %v", p, fi.Mode(), err)
			}
		}
	}

	// Opened again: the persisted ones only.
	again, err := Open(local)
	if err != nil {
		t.Fatal(err)
	}
	want := []Variable{
		{Name: "kept", Value: "k1", Persist: true},
		{Name: "token", Value: "t0k3n-s3cret", Secret: true, Persist: true},
	}
	if got := again.List(); !reflect.DeepEqual(got, want) {
		t.Errorf("reopened = %+v, want %+v", got, want)
	}

	// Making a persisted one a session one takes it out of the files.
	if err := again.Set(Variable{Name: "token", Value: "t0k3n-s3cret", Secret: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local, "secrets", "runtime", SecretsFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("secrets file after the last persisted secret went: %v", err)
	}
	// Renaming keeps it one variable.
	if err := again.Replace("kept", Variable{Name: "renamed", Value: "k2", Persist: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("kept"); ok {
		t.Error("the old name is still set")
	}
	third, err := Open(local)
	if err != nil {
		t.Fatal(err)
	}
	if got := third.List(); !reflect.DeepEqual(got, []Variable{{Name: "renamed", Value: "k2", Persist: true}}) {
		t.Errorf("after rename = %+v", got)
	}

	if err := third.Delete("missing"); code(err) != CodeNotFound {
		t.Errorf("Delete(missing) = %v", err)
	}
	if err := third.Delete("renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file after the last persisted variable went: %v", err)
	}
}

func TestStoreClearAndBrokenFile(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	st, err := Open(local)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Set(Variable{Name: "a", Value: "1", Persist: true})
	_ = st.Set(Variable{Name: "b", Value: "2"})
	if err := st.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(st.List()) != 0 {
		t.Errorf("after Clear: %+v", st.List())
	}
	if _, err := os.Stat(filepath.Join(local, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file after Clear: %v", err)
	}

	if err := os.WriteFile(filepath.Join(local, FileName), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(local); code(err) != CodeLocalDataFailed || errs.KindOf(err) != errs.Parse {
		t.Errorf("Open(broken) = %v", err)
	}
	// A secret listed without its value is left out, not "".
	if err := os.WriteFile(filepath.Join(local, FileName), []byte(`{"version":1,"variables":[{"name":"gone","value":"","secret":true},{"name":"x","value":"1"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = Open(local)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.List(); len(got) != 1 || got[0].Name != "x" {
		t.Errorf("List = %+v", got)
	}
}

func TestCheckName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"token", "auth.token", "$mine", "a b", "ünï"} {
		if err := CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", " x", "x ", "a}}b", "a{{b", strings.Repeat("x", MaxNameLength+1)} {
		if err := CheckName(name); code(err) != CodeInvalidName {
			t.Errorf("CheckName(%q) = %v", name, err)
		}
	}
	st, _ := Open(t.TempDir())
	if err := st.Set(Variable{Name: "a}}"}); code(err) != CodeInvalidName {
		t.Errorf("Set = %v", err)
	}
}

// TestRegistrySessions: a session is one opening of a project. Opening it
// again (a new Workspace value) drops the session variables and keeps the
// persisted ones; nothing is written into the project folder.
func TestRegistrySessions(t *testing.T) {
	t.Parallel()
	manager := workspace.New(t.TempDir())
	ws, err := manager.Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	reg := NewRegistry(manager.Current, func(*Store) { changes++ })
	st, err := reg.Current()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := reg.For(ws); again != st {
		t.Error("For returned another store for the same session")
	}
	_ = st.Set(Variable{Name: "session", Value: "s"})
	_ = st.Set(Variable{Name: "kept", Value: "k", Persist: true})
	_ = st.Set(Variable{Name: "secret", Value: "sh-s3cret", Secret: true})
	if changes != 3 {
		t.Errorf("changes = %d", changes)
	}
	if got := reg.Secrets(); !reflect.DeepEqual(got, []string{"sh-s3cret"}) {
		t.Errorf("Secrets = %v", got)
	}

	// A request loaded for the old workspace does not end the new session.
	reopened, err := manager.Open(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := reg.For(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if stale, err := reg.For(ws); err != nil || stale == st2 {
		t.Fatalf("For(stale) = %p, %v", stale, err)
	}
	if got := st2.List(); !reflect.DeepEqual(got, []Variable{{Name: "kept", Value: "k", Persist: true}}) {
		t.Errorf("after reopening: %+v", got)
	}
	if again, _ := reg.Current(); again != st2 {
		t.Error("Current is not the reopened project's store")
	}
	if reg.Secrets() != nil {
		t.Errorf("Secrets after reopening = %v", reg.Secrets())
	}

	// Clear works on an unreadable file too.
	if err := os.WriteFile(filepath.Join(reopened.LocalDir, FileName), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := manager.Open(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.For(third); code(err) != CodeLocalDataFailed {
		t.Errorf("For(broken) = %v", err)
	}
	if err := reg.Clear(third); err != nil {
		t.Fatal(err)
	}
	if st3, err := reg.Current(); err != nil || len(st3.List()) != 0 {
		t.Errorf("after Clear: %v", err)
	}

	// The project folder holds postsonde.json only.
	entries, err := os.ReadDir(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("project folder: %v", entries)
	}

	if _, err := reg.For(nil); code(err) != workspace.CodeNoWorkspace {
		t.Errorf("For(nil) = %v", err)
	}
	var _ template.Scope = st.Scope()
}
