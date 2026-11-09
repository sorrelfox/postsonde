package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestPickBodyFile(t *testing.T) {
	t.Parallel()
	project := filepath.Join(tempDir(t), "shop-api")
	outside := filepath.Join(tempDir(t), "photo.png")
	inside := filepath.Join(project, "fixtures", "avatar.png")

	var chosen string
	var dialogs []FolderDialog
	svc, _ := newService(t, tempDir(t), func(_ context.Context, d FolderDialog) (string, error) {
		dialogs = append(dialogs, d)
		return chosen, nil
	})
	ctx := t.Context()

	// Without a project, every path is absolute.
	if err := os.WriteFile(outside, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	chosen = outside
	got, err := svc.PickBodyFile(ctx)
	if err != nil || got != (BodyFileDTO{Path: outside, AbsolutePath: outside, Name: "photo.png", Size: 5, Exists: true}) {
		t.Errorf("PickBodyFile without a project = %+v, %v", got, err)
	}
	if len(dialogs) != 1 || !dialogs[0].ChooseFile || dialogs[0].Title == "" {
		t.Errorf("dialog = %+v, want a file dialog", dialogs)
	}

	if _, err := svc.Create(ctx, CreateInputDTO{Path: project, Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	chosen = inside
	got, err = svc.PickBodyFile(ctx)
	if err != nil || got != (BodyFileDTO{Path: "fixtures/avatar.png", AbsolutePath: inside, Name: "avatar.png", Size: 3, Exists: true}) {
		t.Errorf("PickBodyFile inside the project = %+v, %v", got, err)
	}
	chosen = outside
	if got, _ := svc.PickBodyFile(ctx); got.Path != outside {
		t.Errorf("PickBodyFile outside the project = %+v, want the absolute path", got)
	}
	chosen = ""
	if got, err := svc.PickBodyFile(ctx); err != nil || got != (BodyFileDTO{}) {
		t.Errorf("cancelled = %+v, %v", got, err)
	}

	for path, want := range map[string]BodyFileDTO{
		"fixtures/avatar.png": {Path: "fixtures/avatar.png", AbsolutePath: inside, Name: "avatar.png", Size: 3, Exists: true},
		"fixtures/gone.png":   {Path: "fixtures/gone.png", AbsolutePath: filepath.Join(project, "fixtures", "gone.png"), Name: "gone.png"},
		"fixtures":            {Path: "fixtures", AbsolutePath: filepath.Join(project, "fixtures"), Name: "fixtures"},
		outside:               {Path: outside, AbsolutePath: outside, Name: "photo.png", Size: 5, Exists: true},
		"":                    {},
	} {
		if got, err := svc.BodyFile(ctx, path); err != nil || got != want {
			t.Errorf("BodyFile(%q) = %+v, %v; want %+v", path, got, err, want)
		}
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.BodyFile(ctx, "fixtures/avatar.png"); got.Exists || got.AbsolutePath != "" || got.Name != "avatar.png" {
		t.Errorf("relative BodyFile without a project = %+v", got)
	}

	failing, _ := newService(t, tempDir(t), func(context.Context, FolderDialog) (string, error) {
		return "", errors.New("no window")
	})
	if _, err := failing.PickBodyFile(ctx); err == nil {
		t.Error("a failing dialog returned no error")
	}
	none, _ := newService(t, tempDir(t), nil)
	if _, err := none.PickBodyFile(ctx); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("no picker: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for name, call := range map[string]func() error{
		"PickBodyFile": func() error { _, err := svc.PickBodyFile(cancelled); return err },
		"BodyFile":     func() error { _, err := svc.BodyFile(cancelled, "x"); return err },
	} {
		if err := call(); errs.KindOf(err) != errs.Cancelled {
			t.Errorf("%s cancelled: %v", name, err)
		}
	}
}

func TestPickSchemaFile(t *testing.T) {
	t.Parallel()
	project := filepath.Join(tempDir(t), "shop-api")
	inside := filepath.Join(project, "schemas", "user.json")
	var dialogs []FolderDialog
	svc, _ := newService(t, tempDir(t), func(_ context.Context, d FolderDialog) (string, error) {
		dialogs = append(dialogs, d)
		return inside, nil
	})
	ctx := t.Context()
	if _, err := svc.Create(ctx, CreateInputDTO{Path: project, Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.PickSchemaFile(ctx)
	if err != nil || got != (BodyFileDTO{Path: "schemas/user.json", AbsolutePath: inside, Name: "user.json", Size: 2, Exists: true}) {
		t.Errorf("PickSchemaFile = %+v, %v", got, err)
	}
	if len(dialogs) != 1 || !dialogs[0].ChooseFile || dialogs[0].Title != "Choose JSON Schema" {
		t.Errorf("dialog = %+v", dialogs)
	}
}
