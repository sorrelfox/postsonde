package settings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// CheckFileAccess (PRV-10) tells a file picker whether a chosen file may
// be read and which folder to allow; AllowDirectory allows it.
func TestCheckFileAccessAndAllowDirectory(t *testing.T) {
	t.Parallel()
	project, outside, keys := realDir(t), realDir(t), realDir(t)
	for _, p := range []string{filepath.Join(project, "k.pem"), filepath.Join(outside, "k.pem"), filepath.Join(keys, "real.pem")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(outside, "link.pem")
	if err := os.Symlink(filepath.Join(keys, "real.pem"), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need privileges on Windows:", err)
		}
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), core.FileName)
	svc := New(core.Open(settingsPath, nil), nil, nil, WithProjectRoot(func() string { return project }))
	ctx := t.Context()

	for _, tc := range []struct {
		name, path, folder string
		allowed            bool
	}{
		{"project-relative", "k.pem", "", true},
		{"absolute in the project", filepath.Join(project, "k.pem"), "", true},
		{"missing in the project", "nope.pem", "", true},
		{"outside", filepath.Join(outside, "k.pem"), outside, false},
		{"a folder outside", outside, outside, false},
		{"a link: the folder of its target", link, keys, false},
		{"missing outside", filepath.Join(outside, "nope", "x.pem"), filepath.Join(outside, "nope"), false},
	} {
		got, err := svc.CheckFileAccess(ctx, tc.path)
		if err != nil || got.Allowed != tc.allowed || got.Folder != tc.folder || got.Locked {
			t.Errorf("%s: CheckFileAccess = %+v, %v; want allowed %v, folder %q", tc.name, got, err, tc.allowed, tc.folder)
		}
	}
	var e *errs.Error
	if _, err := svc.CheckFileAccess(ctx, " "); !errors.As(err, &e) || e.Code != fileaccess.CodeInvalidPath {
		t.Errorf("empty path: %v", err)
	}

	// Allowing the offered folder makes the file allowed, once.
	if list, err := svc.AllowDirectory(ctx, outside); err != nil || len(list) != 1 || list[0].Path != outside {
		t.Fatalf("AllowDirectory = %+v, %v", list, err)
	}
	if list, err := svc.AllowDirectory(ctx, outside); err != nil || len(list) != 1 {
		t.Fatalf("AllowDirectory twice = %+v, %v", list, err)
	}
	if got, err := svc.CheckFileAccess(ctx, filepath.Join(outside, "k.pem")); err != nil || !got.Allowed {
		t.Errorf("after allowing: %+v, %v", got, err)
	}
	// The link's target is still outside: its own folder is offered.
	if got, _ := svc.CheckFileAccess(ctx, link); got.Allowed || got.Folder != keys {
		t.Errorf("link after allowing its folder: %+v", got)
	}
	if loaded, _, err := core.Load(settingsPath); err != nil || !slices.Equal(loaded.Privacy.AllowedDirs, []string{outside}) {
		t.Errorf("file has %v, %v", loaded.Privacy.AllowedDirs, err)
	}
	// Only an existing, absolute folder can be allowed.
	for _, bad := range []string{filepath.Join(outside, "k.pem"), filepath.Join(outside, "gone"), "relative"} {
		if _, err := svc.AllowDirectory(ctx, bad); !errors.As(err, &e) || e.Code != CodeAllowedDirMissing {
			t.Errorf("AllowDirectory(%q) = %v", bad, err)
		}
	}
}

// Folders allowed at the same time (two windows) are all kept: the add
// runs under the list lock.
func TestAllowDirectoryConcurrent(t *testing.T) {
	t.Parallel()
	svc := New(core.Open(filepath.Join(t.TempDir(), core.FileName), nil), nil, nil)
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = realDir(t)
	}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Go(func() {
			if _, err := svc.AllowDirectory(t.Context(), d); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := svc.AllowedDirs(t.Context())
	if err != nil || len(got) != len(dirs) {
		t.Fatalf("AllowedDirs = %+v, %v", got, err)
	}
	for _, d := range dirs {
		if !slices.ContainsFunc(got, func(a AllowedDirDTO) bool { return a.Path == d }) {
			t.Errorf("%s was lost", d)
		}
	}
}

// With privacy.allowedDirs locked by the admin policy (SET-02), the
// check says so and AllowDirectory is refused.
func TestFileAccessLocked(t *testing.T) {
	t.Parallel()
	outside := realDir(t)
	svc, _, _ := policyService(t, `{"locked": {"privacy.allowedDirs": []}}`, "")
	ctx := t.Context()
	got, err := svc.CheckFileAccess(ctx, filepath.Join(outside, "k.pem"))
	if err != nil || got.Allowed || !got.Locked || got.Folder != outside {
		t.Errorf("CheckFileAccess = %+v, %v", got, err)
	}
	var e *errs.Error
	if _, err := svc.AllowDirectory(ctx, outside); !errors.As(err, &e) || e.Code != core.CodeLocked {
		t.Errorf("AllowDirectory = %v, want %s", err, core.CodeLocked)
	}
}
