package datadir

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	core "github.com/sorrelfox/postsonde/internal/core/datadir"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// startUnder starts with POSTSONDE_HOME-free defaults under a temp home,
// so nothing touches the real data directory.
func startUnder(t *testing.T, home string) core.Startup {
	t.Helper()
	env := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, "c"), "XDG_DATA_HOME": filepath.Join(home, "d"),
		"XDG_CACHE_HOME": filepath.Join(home, "k"), "XDG_STATE_HOME": filepath.Join(home, "s"),
		"APPDATA": filepath.Join(home, "r"), "LOCALAPPDATA": filepath.Join(home, "l"),
	}
	st, err := core.Start(core.Env{
		GOOS: runtime.GOOS, Getenv: func(k string) string { return env[k] }, Home: home,
		Executable: filepath.Join(home, "app", "postsonde"),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestServiceFlow(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	st := startUnder(t, home)
	target := filepath.Join(home, "picked")
	var picked, revealed string
	var restarted atomic.Bool
	done := make(chan struct{})
	s := New(core.NewStore(st), nil,
		WithFolderPicker(func(context.Context, FolderDialog) (string, error) { return picked, nil }),
		WithReveal(func(p string) error { revealed = p; return nil }),
		WithRestart(func() { restarted.Store(true); close(done) }))

	info, err := s.Info(ctx)
	if err != nil || info.Mode != "default" || info.DataDir != st.Dirs.Data || !info.CanRestart || info.ChangeBlocked != nil {
		t.Fatalf("Info = %+v, %v", info, err)
	}

	// Cancelled dialog.
	if c, err := s.Pick(ctx); err != nil || c.Path != "" {
		t.Fatalf("cancelled Pick = %+v, %v", c, err)
	}
	picked = target
	c, err := s.Pick(ctx)
	if err != nil || c.HasData || c.Exists || !c.CanMove {
		t.Fatalf("Pick = %+v, %v", c, err)
	}
	info, err = s.Schedule(ctx, ScheduleDTO{Path: c.Path, Action: "move"})
	if err != nil || info.Pending == nil || info.Pending.Action != "move" {
		t.Fatalf("Schedule = %+v, %v", info, err)
	}
	if _, err := s.Schedule(ctx, ScheduleDTO{Path: c.Path, Action: "use"}); err == nil {
		t.Error("use on an empty folder: want error")
	}
	if err := s.Reveal(ctx, "data"); err != nil || revealed != st.Dirs.Data {
		t.Errorf("Reveal = %v, revealed %q", err, revealed)
	}
	if err := s.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart not called")
	}
	info, err = s.Cancel(ctx)
	if err != nil || info.Pending != nil {
		t.Fatalf("Cancel = %+v, %v", info, err)
	}
}

func TestServiceBlockedByEnv(t *testing.T) {
	root := t.TempDir()
	st := core.Startup{Resolution: core.Resolution{Mode: core.ModeEnv, Root: root, Dirs: appdirs.Under(root)}}
	s := New(core.NewStore(st), nil, WithFolderPicker(func(context.Context, FolderDialog) (string, error) {
		t.Error("the dialog must not be shown")
		return "", nil
	}))
	info, err := s.Info(context.Background())
	if err != nil || info.ChangeBlocked == nil || info.ChangeBlocked.Code != core.CodeChangeUnavailable {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	_, err = s.Pick(context.Background())
	if e, _ := errors.AsType[*errs.Error](err); e == nil || e.Code != core.CodeChangeUnavailable {
		t.Errorf("Pick err = %v", err)
	}
}

func TestServiceWithoutStore(t *testing.T) {
	s := New(nil, nil)
	info, err := s.Info(context.Background())
	if err != nil || info.Problems == nil {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	if _, err := s.Pick(context.Background()); err == nil {
		t.Error("Pick: want error")
	}
	if err := s.Restart(context.Background()); err == nil {
		t.Error("Restart: want error")
	}
}
