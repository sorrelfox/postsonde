package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	"github.com/sorrelfox/postsonde/internal/core/datadir"
)

func TestWebviewDataDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	portable := datadir.Resolution{Mode: datadir.ModePortable, Dirs: appdirs.Under(root)}
	got := webviewDataDir(portable)
	if want := filepath.Join(portable.Dirs.Cache, "webview2"); got != want {
		t.Errorf("portable = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("the folder was not created: %v", err)
	}
	if entries, _ := os.ReadDir(got); len(entries) != 0 {
		t.Errorf("the write probe was left behind: %v", entries)
	}
	if got := webviewDataDir(datadir.Resolution{Mode: datadir.ModeDefault, Dirs: appdirs.Under(root)}); got != "" {
		t.Errorf("default mode = %q, want WebView2's default", got)
	}
	// A cache folder that cannot be made: WebView2's default.
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := datadir.Resolution{Mode: datadir.ModeEnv, Dirs: appdirs.Dirs{Cache: filepath.Join(file, "cache")}}
	if got := webviewDataDir(blocked); got != "" {
		t.Errorf("unwritable = %q", got)
	}
}

func TestWaitForExit(t *testing.T) {
	t.Parallel()
	start := time.Now()
	waitForExit(0, time.Second)
	waitForExit(-1, time.Second)
	if time.Since(start) > 500*time.Millisecond {
		t.Error("waited for no process")
	}
	// This process does not exit: the wait ends at the timeout.
	start = time.Now()
	waitForExit(os.Getpid(), 100*time.Millisecond)
	if d := time.Since(start); d < 100*time.Millisecond || d > 5*time.Second {
		t.Errorf("waited %v for a running process, want the 100ms timeout", d)
	}
}
