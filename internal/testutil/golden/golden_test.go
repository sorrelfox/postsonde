package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssert shows the project convention: a table of cases, each compared
// against testdata/<name>.golden. Regenerate with:
//
//	go test ./internal/testutil/golden -run TestAssert -update
func TestAssert(t *testing.T) {
	tests := []struct {
		name  string
		input []string
	}{
		{name: "single_line", input: []string{"hello"}},
		{name: "multi_line", input: []string{"GET /users HTTP/1.1", "Host: localhost", "Accept: */*"}},
		{name: "nested/empty", input: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			Assert(t, tt.name, render(tt.input))
		})
	}
}

// render stands in for the code under test.
func render(lines []string) []byte {
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%d: %s\n", i+1, l)
	}
	return []byte(b.String())
}

func TestAssertFile(t *testing.T) {
	tests := []struct {
		name       string
		existing   *string // nil: no golden file
		got        string
		update     bool
		wantFailed bool
		wantMsg    string
		wantFile   *string // nil: file must not exist afterwards
	}{
		{name: "match", existing: ptr("a\nb\n"), got: "a\nb\n", wantFile: ptr("a\nb\n")},
		{name: "mismatch", existing: ptr("a\nb\n"), got: "a\nc\n", wantFailed: true,
			wantMsg: `line 2:` + "\n" + `  want: "b"` + "\n" + `  got:  "c"`, wantFile: ptr("a\nb\n")},
		{name: "extra line", existing: ptr("a"), got: "a\nb", wantFailed: true,
			wantMsg: "<end of file>", wantFile: ptr("a")},
		{name: "missing file", got: "a", wantFailed: true, wantMsg: "-update"},
		{name: "update overwrites", existing: ptr("old"), got: "new", update: true, wantFile: ptr("new")},
		{name: "update creates dirs", got: "new", update: true, wantFile: ptr("new")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "testdata", "sub", "case.golden")
			if tt.existing != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			ft := &fakeTB{}
			assertFile(ft, path, []byte(tt.got), tt.update)

			if ft.failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v (msg: %q)", ft.failed, tt.wantFailed, ft.msg)
			}
			if !strings.Contains(ft.msg, tt.wantMsg) {
				t.Errorf("message %q does not contain %q", ft.msg, tt.wantMsg)
			}
			data, err := os.ReadFile(path)
			switch {
			case tt.wantFile == nil && err == nil:
				t.Errorf("golden file was created: %q", data)
			case tt.wantFile != nil && err != nil:
				t.Errorf("read golden file: %v", err)
			case tt.wantFile != nil && string(data) != *tt.wantFile:
				t.Errorf("golden file = %q, want %q", data, *tt.wantFile)
			}
		})
	}
}

func TestPath(t *testing.T) {
	tests := []struct{ name, want string }{
		{"simple", filepath.Join("testdata", "simple.golden")},
		{"group/case", filepath.Join("testdata", "group", "case.golden")},
	}
	for _, tt := range tests {
		if got := Path(tt.name); got != tt.want {
			t.Errorf("Path(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// fakeTB records failures instead of failing the real test.
type fakeTB struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	// The real Fatalf stops the goroutine; assertFile returns right after
	// each Fatalf so the fake does not need to.
	f.Errorf(format, args...)
}

func ptr(s string) *string { return &s }
