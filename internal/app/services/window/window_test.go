package window

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// fakeHost opens numbered windows and remembers what was focused.
type fakeHost struct {
	mu      sync.Mutex
	next    uint
	opened  []uint
	focused uint
	fail    error
}

func (h *fakeHost) Open() (uint, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail != nil {
		return 0, h.fail
	}
	h.next++
	h.opened = append(h.opened, h.next)
	return h.next, nil
}

func (h *fakeHost) Focus(id uint) uint {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == 0 {
		id = h.next
	}
	h.focused = id
	return id
}

func (h *fakeHost) windows() []uint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint(nil), h.opened...)
}

type event struct {
	name string
	data any
}

type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{name, data})
}

func (r *recorder) all() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// project creates a project folder and returns it.
func project(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":1,"kind":"workspace","id":"ws_` + name + `","name":"` + name + `"}`
	if err := os.WriteFile(filepath.Join(dir, projectfile.WorkspaceFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newService(t *testing.T) (*WindowService, *fakeHost, *coreworkspace.Manager, *recorder) {
	t.Helper()
	host := &fakeHost{next: 1} // window 1 is the main window
	projects := coreworkspace.New(tempDir(t))
	rec := &recorder{}
	return New(host, projects, rec.emit, nil), host, projects, rec
}

func TestCurrentReturnsTheWindowAndItsProjectOnce(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	OpenAt(svc, 1, "/projects/shop")
	OpenAt(svc, 1, "") // nothing to open: kept
	ctx := windowctx.With(t.Context(), 1)
	got, err := svc.Current(ctx)
	if err != nil || !reflect.DeepEqual(got, WindowDTO{ID: 1, OpenPath: "/projects/shop"}) {
		t.Fatalf("Current = %+v, %v", got, err)
	}
	if got, _ := svc.Current(ctx); got.OpenPath != "" {
		t.Errorf("second Current = %+v, want no project", got)
	}
	if got, _ := svc.Current(t.Context()); got.ID != 0 {
		t.Errorf("Current from no window = %+v", got)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := svc.Current(cancelled); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("Current cancelled = %v", err)
	}
}

func TestNewWindowAndOpenInNewWindow(t *testing.T) {
	t.Parallel()
	svc, host, projects, _ := newService(t)
	ctx := t.Context()
	shop := project(t, "shop")
	billing := project(t, "billing")

	if err := svc.NewWindow(ctx); err != nil {
		t.Fatal(err)
	}
	// No project open: the new window opens the one asked for.
	if err := svc.OpenInNewWindow(ctx, shop); err != nil {
		t.Fatal(err)
	}
	if got := host.windows(); !reflect.DeepEqual(got, []uint{2, 3}) {
		t.Fatalf("opened = %v", got)
	}
	if got, _ := svc.Current(windowctx.With(ctx, 2)); got.OpenPath != "" {
		t.Errorf("window 2 = %+v, want no project", got)
	}
	if got, _ := svc.Current(windowctx.With(ctx, 3)); got.OpenPath != shop {
		t.Errorf("window 3 = %+v, want %s", got, shop)
	}

	if _, err := projects.Open(shop); err != nil {
		t.Fatal(err)
	}
	// The open project: a plain new window, which shows it.
	if err := svc.OpenInNewWindow(ctx, filepath.Join(shop, projectfile.WorkspaceFileName)); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Current(windowctx.With(ctx, 4)); got.ID != 4 || got.OpenPath != "" {
		t.Errorf("window 4 = %+v", got)
	}
	// Another project: refused, no window.
	err := svc.OpenInNewWindow(ctx, billing)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeProjectDiffers || errs.KindOf(err) != errs.InvalidArgument || e.Hint == "" || e.Details["path"] != billing {
		t.Errorf("OpenInNewWindow(other project) = %v", err)
	}
	if got := len(host.windows()); got != 3 {
		t.Errorf("windows opened = %d, want 3", got)
	}

	host.mu.Lock()
	host.fail = errors.New("no display")
	host.mu.Unlock()
	if err := svc.NewWindow(ctx); errs.KindOf(err) != errs.Internal {
		t.Errorf("NewWindow with a failing host = %v", err)
	}
}

func TestLaunch(t *testing.T) {
	t.Parallel()
	svc, host, projects, rec := newService(t)
	shop := project(t, "shop")
	billing := project(t, "billing")

	// No project given: a new window.
	Launch(svc, nil, "/")
	// A project while none is open: a new window that opens it; a relative
	// path is taken from the second process's working directory.
	Launch(svc, []string{"-psn_0_12345", filepath.Base(shop)}, filepath.Dir(shop))
	if got := host.windows(); !reflect.DeepEqual(got, []uint{2, 3}) {
		t.Fatalf("opened = %v", got)
	}
	if got, _ := svc.Current(windowctx.With(t.Context(), 3)); got.OpenPath != shop {
		t.Errorf("window 3 = %+v, want %s", got, shop)
	}

	if _, err := projects.Open(shop); err != nil {
		t.Fatal(err)
	}
	// The open project: a new window on it.
	Launch(svc, []string{shop}, "")
	if got := len(host.windows()); got != 3 {
		t.Fatalf("windows = %d, want 3", got)
	}
	if len(rec.all()) != 0 {
		t.Errorf("events = %+v", rec.all())
	}
	// Another project: the latest window is asked to open it.
	Launch(svc, []string{"--", billing}, "")
	if got := len(host.windows()); got != 3 {
		t.Errorf("windows = %d, want still 3", got)
	}
	want := []event{{EventOpenProject, OpenProjectDTO{Window: 4, Path: billing}}}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want %+v", got, want)
	}
}

func TestProjectArg(t *testing.T) {
	t.Parallel()
	abs := filepath.Join(tempDir(t), "p")
	for _, c := range []struct {
		args []string
		wd   string
		want string
	}{
		{nil, "/w", ""},
		{[]string{"-psn_0_1", "--flag"}, "/w", ""},
		{[]string{abs}, "", abs},
		{[]string{"", "-x", abs, "other"}, "", abs},
		{[]string{"rel"}, abs, filepath.Join(abs, "rel")},
		{[]string{"rel"}, "", ""},
		{[]string{"--", "-dash"}, abs, filepath.Join(abs, "-dash")},
		{[]string{abs + string(filepath.Separator) + "."}, "", abs},
	} {
		if got := ProjectArg(c.args, c.wd); got != c.want {
			t.Errorf("ProjectArg(%q, %q) = %q, want %q", c.args, c.wd, got, c.want)
		}
	}
}

func TestClosedForgetsTheProject(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newService(t)
	OpenAt(svc, 5, "/p")
	Closed(svc, 5)
	if got, _ := svc.Current(windowctx.With(t.Context(), 5)); got.OpenPath != "" {
		t.Errorf("Current after Closed = %+v", got)
	}
}
