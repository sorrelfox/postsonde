package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// TestAllowedDirs: AddAllowedDir shows the folder dialog and saves the
// chosen folder in privacy.allowedDirs; RemoveAllowedDir takes it out;
// AllowedDirs flags a folder that is gone. The list survives a reload of
// the settings file.
func TestAllowedDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, core.FileName)
	fixtures := filepath.Join(dir, "fixtures")
	keys := filepath.Join(dir, "keys")
	file := filepath.Join(dir, "a.txt")
	for _, d := range []string{fixtures, keys} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var next string
	var dialogs []FileDialog
	rec := &recorder{}
	svc := New(core.Open(settingsPath, nil), rec.emit, nil, WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		dialogs = append(dialogs, d)
		return next, nil
	}))
	ctx := t.Context()

	// Cancelling changes nothing; the dialog asks for a folder.
	got, err := svc.AddAllowedDir(ctx)
	if err != nil || len(got) != 0 || len(dialogs) != 1 || !dialogs[0].Folder {
		t.Fatalf("cancel: %+v, %v, dialogs %+v", got, err, dialogs)
	}

	// A file is refused.
	next = file
	_, err = svc.AddAllowedDir(ctx)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeAllowedDirMissing {
		t.Fatalf("file: %v", err)
	}

	next = fixtures
	if got, err = svc.AddAllowedDir(ctx); err != nil || len(got) != 1 || got[0].Path != fixtures || got[0].Error != nil {
		t.Fatalf("add: %+v, %v", got, err)
	}
	if got, err = svc.AddAllowedDir(ctx); err != nil || len(got) != 1 {
		t.Fatalf("add twice: %+v, %v", got, err)
	}
	next = keys
	if got, err = svc.AddAllowedDir(ctx); err != nil || len(got) != 2 {
		t.Fatalf("add second: %+v, %v", got, err)
	}

	// Saved to the file, announced, and read back by a new store.
	loaded, _, err := core.Load(settingsPath)
	if err != nil || !slices.Equal(loaded.Privacy.AllowedDirs, []string{fixtures, keys}) {
		t.Fatalf("file has %v, %v", loaded.Privacy.AllowedDirs, err)
	}
	if ev := rec.all(); len(ev) != 2 || ev[1].Source != SourceApp {
		t.Errorf("events = %+v", ev)
	}
	reopened := New(core.Open(settingsPath, nil), nil, nil)
	if got, err := reopened.AllowedDirs(ctx); err != nil || len(got) != 2 || got[1].Path != keys {
		t.Errorf("after reload: %+v, %v", got, err)
	}

	// A folder that is gone is listed with an error.
	if err := os.Remove(keys); err != nil {
		t.Fatal(err)
	}
	got, err = svc.AllowedDirs(ctx)
	if err != nil || len(got) != 2 || got[0].Error != nil || got[1].Error == nil || got[1].Error.Code != CodeAllowedDirMissing {
		t.Fatalf("gone: %+v, %v", got, err)
	}

	// Remove takes it out; an unknown path changes nothing.
	if got, err = svc.RemoveAllowedDir(ctx, keys); err != nil || len(got) != 1 || got[0].Path != fixtures {
		t.Fatalf("remove: %+v, %v", got, err)
	}
	if got, err = svc.RemoveAllowedDir(ctx, "/nowhere"); err != nil || len(got) != 1 {
		t.Fatalf("remove unknown: %+v, %v", got, err)
	}
	if got, err = svc.RemoveAllowedDir(ctx, fixtures); err != nil || len(got) != 0 {
		t.Fatalf("remove last: %+v, %v", got, err)
	}
	if data, err := os.ReadFile(settingsPath); err != nil || strings.Contains(string(data), "privacy") {
		t.Errorf("file = %s, %v", data, err)
	}

	// Without a picker.
	if _, err := New(core.Open(filepath.Join(dir, "other.json"), nil), nil, nil).AddAllowedDir(ctx); !errors.As(err, &e) || e.Code != CodePickUnavailable {
		t.Errorf("no picker: %v", err)
	}
}
