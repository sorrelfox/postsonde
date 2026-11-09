package localdata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/localdata"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code || e.Hint == "" {
		t.Errorf("err = %#v, want kind %s code %s with a hint", err, kind, code)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	data := t.TempDir()
	svc := New(workspace.New(data), data, nil)
	_, err := svc.Usage(ctx, UsageInputDTO{Scope: "everything"})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidScope)
	_, err = svc.Clear(ctx, ClearInputDTO{Scope: ScopeAll})
	checkCode(t, err, errs.InvalidArgument, CodeNothingSelected)
	_, err = svc.Clear(ctx, ClearInputDTO{Scope: ScopeAll, Categories: []string{"settings"}})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidCategory)
	// No project open: "project" is refused, "all" works.
	_, err = svc.Usage(ctx, UsageInputDTO{Scope: ScopeProject})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != workspace.CodeNoWorkspace {
		t.Errorf("project scope without a project: %v", err)
	}
	if u, err := svc.Usage(ctx, UsageInputDTO{Scope: ScopeAll}); err != nil || u.Projects != 0 || u.Project != "" || len(u.Categories) != len(core.All()) {
		t.Errorf("Usage(all) = %+v, %v", u, err)
	}
	// Without a data directory nothing is kept.
	_, err = New(workspace.New(""), "", nil).Usage(ctx, UsageInputDTO{Scope: ScopeAll})
	checkCode(t, err, errs.Unsupported, CodeUnavailable)
}

// A hook runs before the files go, for every project in the scope, and
// its failure keeps that project's files and is reported.
func TestHooks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	data := t.TempDir()
	m := workspace.New(data)
	b, err := m.Create(filepath.Join(t.TempDir(), "b"), "B")
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create(filepath.Join(t.TempDir(), "a"), "A")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{a.LocalDir, b.LocalDir} {
		if err := os.MkdirAll(filepath.Join(dir, "graphql-schemas"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "graphql-schemas", "x.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	type call struct {
		dir  string
		open bool
	}
	var calls []call
	svc := New(m, data, nil, WithHook(core.Schemas, func(_ context.Context, dir string, open bool) error {
		calls = append(calls, call{dir, open})
		if dir == b.LocalDir {
			return errs.New(errs.IO, "The schemas could not be dropped.").WithHint("Try again.")
		}
		return nil
	}))
	res, err := svc.Clear(ctx, ClearInputDTO{Scope: ScopeAll, Categories: []string{"schemas", "schemas"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || res.Projects != 2 || len(res.Removed) != 1 || res.Removed[0].Files != 1 {
		t.Errorf("calls %+v, result %+v", calls, res)
	}
	for _, c := range calls {
		if c.open != (c.dir == a.LocalDir) {
			t.Errorf("call %+v: open is wrong", c)
		}
	}
	if len(res.Failed) != 1 || res.Failed[0].Category != "schemas" || res.Failed[0].Message != "The schemas could not be dropped." || res.Failed[0].Hint != "Try again." {
		t.Errorf("failed = %+v", res.Failed)
	}
	if core.Measure(b.LocalDir, core.Schemas).Files != 1 || core.Measure(a.LocalDir, core.Schemas).Files != 0 {
		t.Error("the failed project's files were removed, or the other's kept")
	}
}
