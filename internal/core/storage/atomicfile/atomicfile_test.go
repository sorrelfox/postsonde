package atomicfile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

const targetName = "request.json"

// entries returns the sorted names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// assertOnly fails unless dir holds exactly the named entries, which in
// particular means no temporary files were left behind.
func assertOnly(t *testing.T, dir string, want ...string) {
	t.Helper()
	if got := entries(t, dir); !slices.Equal(got, want) {
		t.Errorf("directory holds %q, want %q", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteFile(t *testing.T) {
	tests := []struct {
		name     string
		existing *string // nil: the target does not exist yet
		data     string
	}{
		{name: "new file", data: "{\n  \"a\": 1\n}\n"},
		{name: "overwrite", existing: ptr("old content\n"), data: "new content\n"},
		{name: "shorter than before", existing: ptr(strings.Repeat("x", 10000)), data: "y"},
		{name: "empty data", existing: ptr("old"), data: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, targetName)
			if tt.existing != nil {
				if err := os.WriteFile(path, []byte(*tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteFile(path, []byte(tt.data), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if got := readFile(t, path); got != tt.data {
				t.Errorf("content = %q, want %q", got, tt.data)
			}
			assertOnly(t, dir, targetName)
		})
	}
}

func TestWriteFileErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("missing directory", func(t *testing.T) {
		err := WriteFile(filepath.Join(dir, "missing", targetName), []byte("x"), 0o644)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("target is a directory", func(t *testing.T) {
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(sub, []byte("x"), 0o644); err == nil {
			t.Error("WriteFile over a directory succeeded")
		}
		if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
			t.Errorf("directory was changed: %v, %v", fi, err)
		}
	})
	assertOnly(t, dir, "sub")
}

// TestWriteSameDataTwice: writing the same data twice produces
// byte-identical files.
func TestWriteSameDataTwice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, targetName)
	v := map[string]any{
		"url":     "https://example.com/?a=1&b=<2>",
		"headers": map[string]string{"b": "2", "a": "1", "c": "3"},
		"timeout": 1.5,
	}
	var first []byte
	for i := range 3 {
		if err := WriteJSON(path, v, 0o644); err != nil {
			t.Fatalf("WriteJSON #%d: %v", i, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
			continue
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("write #%d differs:\n%s\nvs\n%s", i, first, got)
		}
	}
	assertOnly(t, dir, targetName)
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, targetName)
	v := struct {
		Name string            `json:"name"`
		Vars map[string]string `json:"vars"`
	}{Name: "<x> & y", Vars: map[string]string{"z": "1", "a": "2"}}

	if err := WriteJSON(path, v, 0o644); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want, err := stablejson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(want) {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}

	// An unencodable value writes nothing and keeps the old file.
	if err := WriteJSON(path, map[string]any{"c": make(chan int)}, 0o644); err == nil {
		t.Error("WriteJSON of a channel succeeded")
	}
	if got := readFile(t, path); got != string(want) {
		t.Errorf("file changed after a failed WriteJSON:\n%s", got)
	}
	assertOnly(t, dir, targetName)
}

var errInjected = errors.New("injected failure")

// TestWriteFileInterrupted fails the write at each step and checks that the
// target is untouched and no temporary file remains. It sets the package
// hooks, so it and its subtests must not run in parallel.
func TestWriteFileInterrupted(t *testing.T) {
	stages := []struct {
		name string
		set  func()
	}{
		{name: "partial write", set: func() {
			hooks.write = func(f *os.File, data []byte) error {
				if _, err := f.Write(data[:len(data)/2]); err != nil {
					return err
				}
				return errInjected
			}
		}},
		{name: "before sync", set: func() { hooks.beforeSync = func() error { return errInjected } }},
		{name: "before rename", set: func() { hooks.beforeRename = func() error { return errInjected } }},
	}
	for _, st := range stages {
		for _, existing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/existing=%v", st.name, existing), func(t *testing.T) {
				st.set()
				t.Cleanup(resetHooks)

				dir := t.TempDir()
				path := filepath.Join(dir, targetName)
				const original = "{\n  \"original\": true\n}\n"
				if existing {
					if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				err := WriteFile(path, []byte(strings.Repeat("new data ", 1000)), 0o644)
				if !errors.Is(err, errInjected) {
					t.Fatalf("WriteFile error = %v, want the injected failure", err)
				}
				if existing {
					if got := readFile(t, path); got != original {
						t.Errorf("target changed to %q", got)
					}
					assertOnly(t, dir, targetName)
				} else {
					assertOnly(t, dir)
				}
			})
		}
	}
}

func resetHooks() {
	hooks.write, hooks.beforeSync, hooks.beforeRename = nil, nil, nil
}

// TestWriteFileKilled kills a real process in the middle of WriteFile, as a
// crash would, and checks that the target still holds its old content. A
// crash can leave the temporary file behind; it must use the documented
// ".<base>.tmp-*" name so that loaders can skip it.
func TestWriteFileKilled(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	for _, stage := range []string{"write", "rename"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, targetName)
			const original = "{\n  \"original\": true\n}\n"
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestKillHelper$")
			cmd.Env = append(os.Environ(), "ATOMICFILE_KILL_TARGET="+path, "ATOMICFILE_KILL_STAGE="+stage)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := false
			var out []string
			for sc := bufio.NewScanner(stdout); sc.Scan(); {
				if sc.Text() == "ready" {
					ready = true
					break
				}
				out = append(out, sc.Text())
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if !ready {
				t.Fatalf("helper exited before reaching %q:\n%s\n%s", stage, strings.Join(out, "\n"), stderr.String())
			}

			if got := readFile(t, path); got != original {
				t.Errorf("target changed to %q after the kill", got)
			}
			for _, name := range entries(t, dir) {
				if name != targetName && !strings.HasPrefix(name, "."+targetName+".tmp-") {
					t.Errorf("unexpected file %q in the directory", name)
				}
			}
		})
	}
}

// TestKillHelper is the subprocess of TestWriteFileKilled. It blocks inside
// WriteFile and waits to be killed.
func TestKillHelper(t *testing.T) {
	path := os.Getenv("ATOMICFILE_KILL_TARGET")
	if path == "" {
		t.Skip("helper process for TestWriteFileKilled")
	}
	block := func() {
		fmt.Println("ready")
		time.Sleep(time.Hour)
	}
	switch stage := os.Getenv("ATOMICFILE_KILL_STAGE"); stage {
	case "write":
		hooks.write = func(f *os.File, data []byte) error {
			if _, err := f.Write(data[:len(data)/2]); err != nil {
				return err
			}
			if err := f.Sync(); err != nil {
				return err
			}
			block()
			return nil
		}
	case "rename":
		hooks.beforeRename = func() error { block(); return nil }
	default:
		t.Fatalf("unknown stage %q", stage)
	}
	err := WriteFile(path, bytes.Repeat([]byte("new "), 1<<18), 0o644)
	t.Fatalf("WriteFile returned (%v) instead of being killed", err)
}

// TestWriteFileConcurrent runs many writers on one file; the result must be
// exactly one of the written versions, and no temporary files may remain.
func TestWriteFileConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, targetName)

	const writers, rounds = 8, 20
	payloads := make([][]byte, writers)
	for i := range payloads {
		// Large enough that a torn write would be visible.
		payloads[i] = append(bytes.Repeat([]byte{byte('a' + i)}, 64<<10), '\n')
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for i := range writers {
		wg.Go(func() {
			for range rounds {
				errs <- WriteFile(path, payloads[i], 0o644)
			}
		})
	}
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case runtime.GOOS == "windows":
			// Renaming over a file another writer is renaming at the same
			// moment can fail with "access denied" on Windows; the target
			// is still intact, which is checked below.
		default:
			t.Errorf("WriteFile: %v", err)
		}
	}
	if succeeded == 0 {
		t.Fatal("no write succeeded")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(payloads, func(p []byte) bool { return bytes.Equal(p, got) }) {
		t.Errorf("final content (%d bytes, starting %q) is not one complete version", len(got), got[:min(len(got), 16)])
	}
	assertOnly(t, dir, targetName)
}

func TestWriteFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows")
	}
	tests := []struct {
		name     string
		existing fs.FileMode // 0: the target does not exist yet
		perm     fs.FileMode
		want     fs.FileMode
	}{
		{name: "new file gets perm", perm: 0o644, want: 0o644},
		{name: "new private file", perm: 0o600, want: 0o600},
		{name: "non-permission bits are dropped", perm: fs.ModeSetuid | 0o640, want: 0o640},
		{name: "existing mode is kept", existing: 0o600, perm: 0o644, want: 0o600},
		{name: "existing executable mode is kept", existing: 0o755, perm: 0o644, want: 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, targetName)
			if tt.existing != 0 {
				if err := os.WriteFile(path, []byte("old"), tt.existing); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tt.existing); err != nil { // bypass the umask
					t.Fatal(err)
				}
			}
			if err := WriteFile(path, []byte("new"), tt.perm); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode(); got != tt.want {
				t.Errorf("mode = %v, want %v", got, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
