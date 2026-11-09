package logging

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// line returns write number i as a 40-byte line.
func line(i int) string {
	return fmt.Sprintf("line-%02d %s\n", i, strings.Repeat("x", 31))
}

func lines(nums ...int) string {
	var b strings.Builder
	for _, n := range nums {
		b.WriteString(line(n))
	}
	return b.String()
}

// readDir returns the contents of every file in dir, keyed by the suffix
// after FileName ("" for the live file, ".1" for the first backup...).
func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		suffix, ok := strings.CutPrefix(e.Name(), FileName)
		if !ok {
			t.Fatalf("unexpected file %s", e.Name())
		}
		got[suffix] = string(b)
	}
	return got
}

func TestRotatingFileRotation(t *testing.T) {
	tests := []struct {
		name       string
		maxSize    int64
		maxBackups int
		writes     int
		want       map[string]string
	}{
		{
			name:    "no rotation under the cap",
			maxSize: 1000, maxBackups: 2, writes: 7,
			want: map[string]string{"": lines(1, 2, 3, 4, 5, 6, 7)},
		},
		{
			name:    "two writes per file, oldest dropped",
			maxSize: 100, maxBackups: 2, writes: 7,
			want: map[string]string{"": lines(7), ".1": lines(5, 6), ".2": lines(3, 4)},
		},
		{
			name:    "a write reaching the cap exactly stays",
			maxSize: 80, maxBackups: 2, writes: 7,
			want: map[string]string{"": lines(7), ".1": lines(5, 6), ".2": lines(3, 4)},
		},
		{
			name:    "one byte short of two writes",
			maxSize: 79, maxBackups: 2, writes: 7,
			want: map[string]string{"": lines(7), ".1": lines(6), ".2": lines(5)},
		},
		{
			name:    "default backup count is three",
			maxSize: 40, maxBackups: 0, writes: 6,
			want: map[string]string{"": lines(6), ".1": lines(5), ".2": lines(4), ".3": lines(3)},
		},
		{
			name:    "negative backup count truncates",
			maxSize: 79, maxBackups: -1, writes: 3,
			want: map[string]string{"": lines(3)},
		},
		{
			name:    "a write larger than the cap is not split",
			maxSize: 10, maxBackups: 1, writes: 3,
			want: map[string]string{"": lines(3), ".1": lines(2)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			rf, err := OpenFile(FileOptions{Path: filepath.Join(dir, FileName), MaxSize: tt.maxSize, MaxBackups: tt.maxBackups})
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= tt.writes; i++ {
				if n, err := rf.Write([]byte(line(i))); err != nil || n != len(line(i)) {
					t.Fatalf("Write %d = %d, %v", i, n, err)
				}
			}
			if err := rf.Close(); err != nil {
				t.Fatal(err)
			}
			got := readDir(t, dir)
			if !maps.Equal(got, tt.want) {
				t.Errorf("files:\n%s\nwant:\n%s", dump(got), dump(tt.want))
			}
		})
	}
}

func dump(m map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		fmt.Fprintf(&b, "  %s%s: %q\n", FileName, k, m[k])
	}
	return b.String()
}

func TestRotatingFileReopenAppends(t *testing.T) {
	dir := t.TempDir()
	opts := FileOptions{Path: filepath.Join(dir, "sub", FileName), MaxSize: 100, MaxBackups: 1}
	for i := 1; i <= 3; i++ { // one write per open; the existing size counts
		rf, err := OpenFile(opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rf.Write([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
		if err := rf.Close(); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"": lines(3), ".1": lines(1, 2)}
	if got := readDir(t, filepath.Dir(opts.Path)); !maps.Equal(got, want) {
		t.Errorf("files:\n%s\nwant:\n%s", dump(got), dump(want))
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(opts.Path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("log file permission = %v, want 0600", perm)
		}
	}
}

func TestRotatingFileClosed(t *testing.T) {
	rf, err := OpenFile(FileOptions{Path: filepath.Join(t.TempDir(), FileName)})
	if err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if _, err := rf.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Write after Close = %v, want os.ErrClosed", err)
	}
	if _, err := OpenFile(FileOptions{}); err == nil {
		t.Error("OpenFile with empty path: want error")
	}
}

// TestRotatingFileConcurrent writes from many goroutines across many
// rotations; run it with -race. No line may be torn, lost or duplicated, and
// no file may exceed the cap.
func TestRotatingFileConcurrent(t *testing.T) {
	const (
		goroutines = 8
		perG       = 250
		lineLen    = 32
		maxSize    = 1024
	)
	dir := t.TempDir()
	// Keep enough backups to hold every line.
	rf, err := OpenFile(FileOptions{Path: filepath.Join(dir, FileName), MaxSize: maxSize, MaxBackups: 100})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for n := range perG {
				l := fmt.Sprintf("g%02d-n%04d", g, n)
				l += strings.Repeat(".", lineLen-len(l)-1) + "\n"
				if _, err := rf.Write([]byte(l)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	files := readDir(t, dir)
	for name, content := range files {
		if len(content) > maxSize {
			t.Errorf("%s%s is %d bytes, over the %d cap", FileName, name, len(content), maxSize)
		}
		sc := bufio.NewScanner(strings.NewReader(content))
		for sc.Scan() {
			l := sc.Text()
			if len(l) != lineLen-1 || !strings.HasPrefix(l, "g") {
				t.Fatalf("torn line %q in %s%s", l, FileName, name)
			}
			if seen[l] {
				t.Fatalf("duplicate line %q", l)
			}
			seen[l] = true
		}
	}
	if len(seen) != goroutines*perG {
		t.Errorf("got %d lines, want %d", len(seen), goroutines*perG)
	}
	if want := goroutines * perG * lineLen / maxSize; len(files) < want {
		t.Errorf("got %d files, want at least %d", len(files), want)
	}
}
