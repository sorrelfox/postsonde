package runtimevars

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func checkCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code || e.Hint == "" {
		t.Errorf("err = %v, want code %q with a hint", err, code)
	}
}

type events struct {
	mu     sync.Mutex
	counts []int
}

func (e *events) emit(name string, data any) {
	if name != EventChanged {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counts = append(e.counts, data.(ChangedDTO).Count)
}

func (e *events) list() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.counts...)
}

func names(d RuntimeVariablesDTO) []string {
	out := []string{}
	for _, v := range d.Variables {
		out = append(out, v.Name)
	}
	return out
}

func TestNoWorkspace(t *testing.T) {
	t.Parallel()
	m := workspace.New(t.TempDir())
	svc := New(m, core.NewRegistry(m.Current, nil), nil)
	_, err := svc.List(t.Context())
	checkCode(t, err, workspace.CodeNoWorkspace)
	_, err = svc.Set(t.Context(), SetInputDTO{Name: "a"})
	checkCode(t, err, workspace.CodeNoWorkspace)
	_, err = svc.Clear(t.Context())
	checkCode(t, err, workspace.CodeNoWorkspace)
}

// TestFlow: set, rename, delete and clear through the service, one event
// per change; session variables are gone when the project is opened
// again, persisted ones are back; nothing is written into the project.
func TestFlow(t *testing.T) {
	t.Parallel()
	m := workspace.New(t.TempDir())
	ws, err := m.Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	var ev events
	svc := New(m, core.NewRegistry(m.Current, Changed(ev.emit)), nil)
	ctx := t.Context()

	list, err := svc.List(ctx)
	if err != nil || len(list.Variables) != 0 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	for _, in := range []SetInputDTO{
		{Name: "session", Value: "s1"},
		{Name: "kept", Value: "k1", Persist: true},
		{Name: "token", Value: "tok-s3cret", Secret: true, Persist: true},
	} {
		if list, err = svc.Set(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"kept", "session", "token"}; !reflect.DeepEqual(names(list), want) {
		t.Errorf("names = %v", names(list))
	}
	if tok := list.Variables[2]; tok.Value != "tok-s3cret" || !tok.Secret || !tok.Persist {
		t.Errorf("token = %+v", tok)
	}
	// Rename, keeping the flags given.
	if list, err = svc.Set(ctx, SetInputDTO{PreviousName: "session", Name: "renamed", Value: "s2"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"kept", "renamed", "token"}; !reflect.DeepEqual(names(list), want) {
		t.Errorf("after rename = %v", names(list))
	}
	_, err = svc.Set(ctx, SetInputDTO{Name: "bad}}name"})
	checkCode(t, err, core.CodeInvalidName)
	_, err = svc.Delete(ctx, "missing")
	checkCode(t, err, core.CodeNotFound)
	if got := ev.list(); !reflect.DeepEqual(got, []int{1, 2, 3, 3}) {
		t.Errorf("events = %v", got)
	}

	// The project folder holds postsonde.json only.
	err = filepath.WalkDir(ws.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), "k1") || filepath.Base(path) != "postsonde.json" {
			t.Errorf("project file %s: %s", path, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Opening the project again ends the session.
	if _, err := m.Open(ws.Root); err != nil {
		t.Fatal(err)
	}
	if list, err = svc.List(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"kept", "token"}; !reflect.DeepEqual(names(list), want) || list.Variables[1].Value != "tok-s3cret" {
		t.Errorf("after reopening = %+v", list)
	}

	if list, err = svc.Delete(ctx, "kept"); err != nil || !reflect.DeepEqual(names(list), []string{"token"}) {
		t.Errorf("Delete = %+v, %v", list, err)
	}
	if list, err = svc.Clear(ctx); err != nil || len(list.Variables) != 0 {
		t.Errorf("Clear = %+v, %v", list, err)
	}
	if _, err := m.Open(ws.Root); err != nil {
		t.Fatal(err)
	}
	if list, err = svc.List(ctx); err != nil || len(list.Variables) != 0 {
		t.Errorf("after Clear and reopening = %+v, %v", list, err)
	}
}

// TestClearUnreadable: a broken file of persisted variables fails List
// with runtimevars.local_data_failed, and Clear removes it.
func TestClearUnreadable(t *testing.T) {
	t.Parallel()
	m := workspace.New(t.TempDir())
	ws, err := m.Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.LocalDir, core.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(m, core.NewRegistry(m.Current, nil), nil)
	_, err = svc.List(t.Context())
	checkCode(t, err, core.CodeLocalDataFailed)
	if list, err := svc.Clear(t.Context()); err != nil || len(list.Variables) != 0 {
		t.Fatalf("Clear = %+v, %v", list, err)
	}
	if list, err := svc.List(t.Context()); err != nil || len(list.Variables) != 0 {
		t.Errorf("List = %+v, %v", list, err)
	}
}
