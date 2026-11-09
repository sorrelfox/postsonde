package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// TestWindowsChangeSettingsAtOnce (APP-06, docs/windows.md): every window
// shares the one settings file. Windows that change different settings
// and add to the same list at the same time all keep their change: a
// patch applies to the settings as they are, and a list is read and
// written under one lock. Run with -race.
func TestWindowsChangeSettingsAtOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, core.FileName)
	const windows, rounds = 6, 5
	folder := func(w uint, i int) string { return filepath.Join(dir, fmt.Sprintf("w%d-%d", w, i)) }
	for w := uint(1); w <= windows; w++ {
		for i := range rounds {
			if err := os.Mkdir(folder(w, i), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	var (
		picks sync.Mutex
		next  = map[uint]int{}
	)
	// Each window's dialog returns that window's next folder.
	pick := func(ctx context.Context, _ FileDialog) (string, error) {
		w := windowctx.ID(ctx)
		picks.Lock()
		defer picks.Unlock()
		i := next[w]
		next[w]++
		return folder(w, i), nil
	}
	rec := &recorder{}
	svc := New(core.Open(settingsPath, nil), rec.emit, nil, WithFilePicker(pick))

	var wg sync.WaitGroup
	for w := uint(1); w <= windows; w++ {
		wg.Go(func() {
			ctx := windowctx.With(t.Context(), w)
			for i := range rounds {
				if _, err := svc.AddAllowedDir(ctx); err != nil {
					t.Error(err)
				}
				// Every window its own setting: window 1 the theme, the
				// others the editor font size.
				patch := SettingsPatchDTO{Editor: &EditorSettingsPatchDTO{FontSize: ptr(10 + int(w))}}
				if w == 1 {
					patch = SettingsPatchDTO{UI: &UISettingsPatchDTO{Theme: ptr([]string{"dark", "light"}[i%2])}}
				}
				if _, err := svc.Update(ctx, patch); err != nil {
					t.Error(err)
				}
				if _, err := svc.Get(ctx); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()

	got, err := svc.AllowedDirs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != windows*rounds {
		t.Fatalf("%d allowed directories, want %d: an add was lost", len(got), windows*rounds)
	}
	loaded, r, err := core.Load(settingsPath)
	if err != nil || len(r.Problems) != 0 {
		t.Fatalf("file: %v, %v", r.Problems, err)
	}
	for w := uint(1); w <= windows; w++ {
		for i := range rounds {
			if !slices.Contains(loaded.Privacy.AllowedDirs, folder(w, i)) {
				t.Errorf("the file lost %s", folder(w, i))
			}
		}
	}
	if loaded.UI.Theme != "light" && loaded.UI.Theme != "dark" {
		t.Errorf("theme = %q", loaded.UI.Theme)
	}

	// Removing from two windows at once removes both.
	var rm sync.WaitGroup
	for w := uint(1); w <= 2; w++ {
		rm.Go(func() {
			if _, err := svc.RemoveAllowedDir(windowctx.With(t.Context(), w), folder(w, 0)); err != nil {
				t.Error(err)
			}
		})
	}
	rm.Wait()
	if got, _ := svc.AllowedDirs(t.Context()); len(got) != windows*rounds-2 {
		t.Errorf("%d allowed directories after removing two, want %d", len(got), windows*rounds-2)
	}
}
