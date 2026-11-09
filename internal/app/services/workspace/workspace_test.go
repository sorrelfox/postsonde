package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	core "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects emitted events.
type recorder struct {
	mu     sync.Mutex
	events []CurrentDTO
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != EventChanged {
		panic("unexpected event " + name)
	}
	r.events = append(r.events, data.(CurrentDTO))
}

func (r *recorder) all() []CurrentDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CurrentDTO(nil), r.events...)
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newService(t *testing.T, data string, pick FolderPicker) (*WorkspaceService, *recorder) {
	t.Helper()
	rec := &recorder{}
	return New(core.New(data), pick, rec.emit, nil), rec
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *errs.Error", err)
	}
	if errs.KindOf(err) != kind || e.Code != code || e.Hint == "" {
		t.Errorf("err = kind %s code %q hint %q, want kind %s code %q and a hint", errs.KindOf(err), e.Code, e.Hint, kind, code)
	}
}

func TestCreateOpenClose(t *testing.T) {
	t.Parallel()
	svc, rec := newService(t, tempDir(t), nil)
	ctx := t.Context()
	project := filepath.Join(tempDir(t), "shop-api")

	cur, err := svc.Current(ctx)
	if err != nil || cur.Workspace != nil {
		t.Fatalf("Current before opening = %+v, %v", cur, err)
	}

	created, err := svc.Create(ctx, CreateInputDTO{Path: project, Name: "Shop API"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Shop API" || created.Path != project || created.LocalStatus != "new" || created.NewerFormat || created.OpenedAt == "" {
		t.Errorf("Create = %+v", created)
	}
	if entries, _ := os.ReadDir(project); len(entries) != 1 || entries[0].Name() != projectfile.WorkspaceFileName {
		t.Errorf("project folder = %v, want only postsonde.json", entries)
	}

	opened, err := svc.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if opened.ID != created.ID || opened.LocalDir != created.LocalDir || opened.LocalStatus != "existing" {
		t.Errorf("Open = %+v, want the created workspace", opened)
	}
	if cur, _ := svc.Current(ctx); cur.Workspace == nil || !reflect.DeepEqual(*cur.Workspace, opened) {
		t.Errorf("Current = %+v, want %+v", cur.Workspace, opened)
	}

	recent, err := svc.Recent(ctx)
	if err != nil || len(recent) != 1 || recent[0].Path != project || !recent[0].Available || recent[0].Name != "Shop API" {
		t.Errorf("Recent = %+v, %v", recent, err)
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if cur, _ := svc.Current(ctx); cur.Workspace != nil {
		t.Errorf("Current after Close = %+v", cur.Workspace)
	}

	events := rec.all()
	if len(events) != 3 {
		t.Fatalf("events = %+v, want create, open, close", events)
	}
	if !reflect.DeepEqual(*events[0].Workspace, created) || !reflect.DeepEqual(*events[1].Workspace, opened) || events[2].Workspace != nil {
		t.Errorf("events = %+v", events)
	}

	if err := svc.RemoveRecent(ctx, project); err != nil {
		t.Fatal(err)
	}
	if recent, _ := svc.Recent(ctx); recent == nil || len(recent) != 0 {
		t.Errorf("Recent after RemoveRecent = %#v, want an empty, non-nil slice", recent)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	svc, rec := newService(t, tempDir(t), nil)
	ctx := t.Context()
	plain := tempDir(t)

	_, err := svc.Open(ctx, plain)
	checkCode(t, err, errs.NotFound, core.CodeNotAProject)

	if _, err := svc.Create(ctx, CreateInputDTO{Path: plain}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, CreateInputDTO{Path: plain, Name: "Again"})
	checkCode(t, err, errs.AlreadyExists, core.CodeAlreadyAProject)

	if n := len(rec.all()); n != 1 {
		t.Errorf("%d events, want 1 (failed calls emit nothing)", n)
	}
}

func TestStartupReopensLast(t *testing.T) {
	t.Parallel()
	data := tempDir(t)
	project := filepath.Join(tempDir(t), "api")
	first, _ := newService(t, data, nil)
	created, err := first.Create(t.Context(), CreateInputDTO{Path: project})
	if err != nil {
		t.Fatal(err)
	}

	// Next start.
	svc, rec := newService(t, data, nil)
	if err := svc.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	cur, _ := svc.Current(t.Context())
	if cur.Workspace == nil || cur.Workspace.ID != created.ID || cur.Workspace.Path != project {
		t.Fatalf("after startup Current = %+v", cur.Workspace)
	}
	if events := rec.all(); len(events) != 1 || events[0].Workspace == nil {
		t.Errorf("events = %+v", events)
	}

	// Closed before quitting, then the folder vanishes: neither reopens
	// nor fails the startup.
	if err := svc.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Open(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(project); err != nil {
		t.Fatal(err)
	}
	again, rec := newService(t, data, nil)
	if err := again.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatalf("startup failed: %v", err)
	}
	if cur, _ := again.Current(t.Context()); cur.Workspace != nil || len(rec.all()) != 0 {
		t.Errorf("reopened a missing folder: %+v", cur.Workspace)
	}
}

func TestPickFolder(t *testing.T) {
	t.Parallel()
	var got []FolderDialog
	pick := func(_ context.Context, d FolderDialog) (string, error) {
		got = append(got, d)
		if d.ButtonText == "Open" {
			return "/chosen", nil
		}
		return "", nil // cancelled
	}
	svc, _ := newService(t, tempDir(t), pick)

	tests := []struct {
		purpose string
		want    string
	}{
		{PurposeOpen, "/chosen"},
		{PurposeCreate, ""},
	}
	for _, tt := range tests {
		path, err := svc.PickFolder(t.Context(), tt.purpose)
		if err != nil || path != tt.want {
			t.Errorf("PickFolder(%q) = %q, %v; want %q", tt.purpose, path, err, tt.want)
		}
	}
	if len(got) != 2 || got[0].Title == "" || got[1].Title == got[0].Title {
		t.Errorf("dialogs = %+v", got)
	}

	_, err := svc.PickFolder(t.Context(), "delete")
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidPurpose {
		t.Errorf("unknown purpose: %v", err)
	}

	failing, _ := newService(t, tempDir(t), func(context.Context, FolderDialog) (string, error) {
		return "", errors.New("no window")
	})
	if _, err := failing.PickFolder(t.Context(), PurposeOpen); err == nil {
		t.Error("a failing dialog returned no error")
	}
	none, _ := newService(t, tempDir(t), nil)
	if _, err := none.PickFolder(t.Context(), PurposeOpen); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("no picker: %v", err)
	}
}

func TestCancelled(t *testing.T) {
	t.Parallel()
	svc, rec := newService(t, tempDir(t), nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	project := tempDir(t)

	calls := map[string]error{}
	_, calls["Current"] = svc.Current(ctx)
	_, calls["Open"] = svc.Open(ctx, project)
	_, calls["Create"] = svc.Create(ctx, CreateInputDTO{Path: project})
	calls["Close"] = svc.Close(ctx)
	_, calls["Recent"] = svc.Recent(ctx)
	calls["RemoveRecent"] = svc.RemoveRecent(ctx, project)
	_, calls["PickFolder"] = svc.PickFolder(ctx, PurposeOpen)
	for name, err := range calls {
		if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
			t.Errorf("%s: err = %v, want cancelled", name, err)
		}
	}
	if entries, _ := os.ReadDir(project); len(entries) != 0 || len(rec.all()) != 0 {
		t.Error("a cancelled call had an effect")
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, the contract with the
// generated TypeScript models.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	opened := time.Date(2026, 9, 16, 9, 30, 0, 0, time.UTC)
	w := &core.Workspace{
		Root:         "/Users/zoe/Projects/shop-api",
		File:         &projectfile.Workspace{ID: "ws_7c1e4b9a2f3d5e60", Name: "Shop API"},
		LocalDir:     "/Users/zoe/Library/Application Support/PostSonde/workspaces/ws_7c1e4b9a2f3d5e60@0123456789abcdef",
		LocalStatus:  core.LocalCopy,
		PreviousPath: "/Users/zoe/Projects/shop-api-main",
		OpenedAt:     opened,
	}
	tests := []struct {
		name string
		dto  any
	}{
		{"current", toCurrentDTO(w)},
		{"current_none", toCurrentDTO(nil)},
		{"recent", toRecentDTOs([]core.Recent{
			{ID: "ws_7c1e4b9a2f3d5e60", Name: "Shop API", Path: "/Users/zoe/Projects/shop-api", OpenedAt: opened, Available: true},
			{ID: "ws_0b8d2e61c4a9f375", Name: "Old", Path: "/Volumes/usb/old", OpenedAt: opened.Add(-time.Hour)},
		})},
		{"recent_empty", toRecentDTOs(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}

// TestUIStateClearRevealAndSwitch: view state saved for a workspace comes
// back in its DTO when it is opened again, also after switching to
// another project (STO-06); ClearRecent keeps the open project; Reveal only
// shows known project folders.
func TestUIStateClearRevealAndSwitch(t *testing.T) {
	t.Parallel()
	var revealed []string
	rec := &recorder{}
	svc := New(core.New(tempDir(t)), nil, rec.emit, nil, WithReveal(func(p string) error {
		revealed = append(revealed, p)
		return nil
	}))
	ctx := t.Context()
	dir := tempDir(t)

	a, err := svc.Create(ctx, CreateInputDTO{Path: filepath.Join(dir, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.UIState) != 0 {
		t.Errorf("new workspace UIState = %v", a.UIState)
	}
	save := func(w WorkspaceDTO, key, value string) {
		t.Helper()
		if err := svc.SaveUIState(ctx, SaveUIStateInputDTO{WorkspaceID: w.ID, Path: w.Path, Key: key, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	save(a, "tabs", `{"order":["r1"],"activeId":"r1"}`)
	b, err := svc.Create(ctx, CreateInputDTO{Path: filepath.Join(dir, "b")})
	if err != nil {
		t.Fatal(err)
	}
	// Saving the project that was just left still works.
	save(a, "tree.expanded", `["f1"]`)
	save(b, "tabs", `{"order":["r2"]}`)

	reopened, err := svc.Open(ctx, a.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"tabs": `{"order":["r1"],"activeId":"r1"}`, "tree.expanded": `["f1"]`}
	if !reflect.DeepEqual(reopened.UIState, want) {
		t.Errorf("A UIState = %v, want %v", reopened.UIState, want)
	}
	if cur, _ := svc.Current(ctx); !reflect.DeepEqual(cur.Workspace.UIState, want) {
		t.Errorf("Current UIState = %v", cur.Workspace.UIState)
	}
	back, err := svc.Open(ctx, b.Path)
	if err != nil {
		t.Fatal(err)
	}
	if back.UIState["tabs"] != `{"order":["r2"]}` {
		t.Errorf("B UIState = %v", back.UIState)
	}

	err = svc.SaveUIState(ctx, SaveUIStateInputDTO{WorkspaceID: a.ID, Path: a.Path, Key: "tabs", Value: "{"})
	checkCode(t, withHint(err), errs.InvalidArgument, core.CodeInvalidUIState)

	if err := svc.Reveal(ctx, a.Path); err != nil {
		t.Fatal(err)
	}
	checkCode(t, svc.Reveal(ctx, dir), errs.InvalidArgument, core.CodeInvalidPath)
	if len(revealed) != 1 || revealed[0] != a.Path {
		t.Errorf("revealed = %v", revealed)
	}

	if err := svc.ClearRecent(ctx); err != nil {
		t.Fatal(err)
	}
	recent, _ := svc.Recent(ctx)
	if len(recent) != 1 || recent[0].Path != b.Path {
		t.Errorf("Recent after ClearRecent = %+v", recent)
	}

	noReveal, _ := newService(t, tempDir(t), nil)
	if err := noReveal.Reveal(ctx, "/x"); errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("Reveal of an unknown folder = %v", err)
	}
}

// withHint gives err a hint when it has none, for checkCode on errors that
// need none.
func withHint(err error) error {
	var e *errs.Error
	if errors.As(err, &e) && e.Hint == "" {
		return e.WithHint("-")
	}
	return err
}

// TestSharedByWindows (APP-06, docs/windows.md): while other windows are
// open, the open project can be opened again (a new window does that) but
// not switched, replaced by a new one or closed; with no project open, or
// once the other windows are closed, everything works as before.
func TestSharedByWindows(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	others := 0
	setOthers := func(n int) {
		mu.Lock()
		defer mu.Unlock()
		others = n
	}
	rec := &recorder{}
	svc := New(core.New(tempDir(t)), nil, rec.emit, nil, WithWindows(func(context.Context) int {
		mu.Lock()
		defer mu.Unlock()
		return others
	}))
	ctx := t.Context()
	first := filepath.Join(tempDir(t), "first")
	second := filepath.Join(tempDir(t), "second")
	if _, err := svc.Create(ctx, CreateInputDTO{Path: second, Name: "Second"}); err != nil {
		t.Fatal(err)
	}

	// No project open: a second window may open or create one.
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	setOthers(1)
	if _, err := svc.Create(ctx, CreateInputDTO{Path: first, Name: "First"}); err != nil {
		t.Fatalf("Create with nothing open and another window: %v", err)
	}

	// The open project again: allowed, spelled differently too.
	if _, err := svc.Open(ctx, filepath.Join(first, projectfile.WorkspaceFileName)); err != nil {
		t.Fatalf("Open of the open project with another window: %v", err)
	}
	before := len(rec.all())

	_, err := svc.Open(ctx, second)
	checkCode(t, err, errs.InvalidArgument, core.CodeSharedByWindows)
	var e *errs.Error
	if errors.As(err, &e) && (e.Details["path"] != second || e.Details["windows"] != "1") {
		t.Errorf("details = %v", e.Details)
	}
	_, err = svc.Create(ctx, CreateInputDTO{Path: filepath.Join(tempDir(t), "third")})
	checkCode(t, err, errs.InvalidArgument, core.CodeSharedByWindows)
	checkCode(t, svc.Close(ctx), errs.InvalidArgument, core.CodeSharedByWindows)
	if cur, _ := svc.Current(ctx); cur.Workspace == nil || cur.Workspace.Path != first {
		t.Errorf("Current after refusals = %+v, want the first project", cur.Workspace)
	}
	if got := len(rec.all()); got != before {
		t.Errorf("refusals sent %d events", got-before)
	}

	// The other window closed: switching works again.
	setOthers(0)
	if _, err := svc.Open(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
