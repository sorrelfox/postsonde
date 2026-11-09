package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// Several windows in one process (APP-06, docs/windows.md).
//
// windowHost opens the app's windows and keeps track of them: every
// window loads the same page and shows the one open project; the bound
// services are shared. It is the window service's Host, tells the
// workspace service how many other windows are open and the collection
// service how many there are, and forwards a second start of PostSonde
// (the single-instance lock, appOptions) to the window service.

// instanceIDPrefix starts the single-instance ID: the app ID (appID, also
// the bundle identifier in build/config.yml). On Linux the lock is the
// D-Bus name <ID>.SingleInstance, which a Flatpak may own only when it
// starts with the app's own ID.
const instanceIDPrefix = appID

// InstanceID is the ID of the single-instance lock for the app directories
// whose config directory is configDir: one running PostSonde per set of
// settings and data. A portable copy or a POSTSONDE_HOME run has other
// directories and so runs next to the installed app, as before. The ID
// only has letters, digits and dots, which every platform's lock accepts
// (a file name, a D-Bus name, a mutex name).
func InstanceID(configDir string) string {
	if configDir == "" {
		return instanceIDPrefix
	}
	if abs, err := filepath.Abs(configDir); err == nil {
		configDir = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(configDir)))
	return instanceIDPrefix + ".i" + hex.EncodeToString(sum[:8])
}

// singleInstanceAvailable reports whether the platform's single-instance
// lock can work. On Linux Wails uses the D-Bus session bus and ends the
// app when it cannot connect; without a session bus address (a bare X
// session, some containers) PostSonde runs without the lock instead.
func singleInstanceAvailable(goos string, getenv func(string) string, exists func(string) bool) bool {
	if goos != "linux" {
		return true
	}
	if a := getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" && a != "autolaunch:" {
		return true
	}
	run := getenv("XDG_RUNTIME_DIR")
	return run != "" && exists(filepath.Join(run, "bus"))
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// windowHost opens and tracks the app's windows. The zero app (tests of
// the services) opens none.
type windowHost struct {
	app *application.App

	// settingsPath and policy give a new window's background (theme.go).
	settingsPath string
	policy       coresettings.Policy
	// title returns the title of every window: the open project's name.
	title func() string

	mu sync.Mutex
	// started is set once the app runs and knows the OS theme.
	started bool
	// windows are the open windows, in the order they were opened.
	windows []*application.WebviewWindow
	// focused is the window focused last.
	focused uint
	// opened counts the windows opened so far, for their names.
	opened int
	// openAt makes a new window open a project once its page has loaded
	// (the window service's OpenAt).
	openAt func(id uint, path string)
	// closed is told about every closed window.
	closed []func(id uint)
	// launch handles a second start of PostSonde; launches that arrive
	// before it is set wait in pending.
	launch  func(args []string, workingDir string)
	pending []application.SecondInstanceData
}

func newWindowHost(a *application.App) *windowHost {
	return &windowHost{app: a, title: func() string { return appName }}
}

// errNoApp is Open's error without a running app (tests).
var errNoApp = errors.New("app: no application to open a window in")

// Open opens a new window (the window service's Host).
func (h *windowHost) Open() (uint, error) {
	if h.app == nil {
		return 0, errNoApp
	}
	return h.open().ID(), nil
}

// open creates a window: the first is "main", the others "window-2" and
// on. Its background is the saved theme's; a window opened after the app
// started knows the OS theme too.
func (h *windowHost) open() *application.WebviewWindow {
	h.mu.Lock()
	h.opened++
	name := "main"
	if h.opened > 1 {
		name = "window-" + strconv.Itoa(h.opened)
	}
	h.mu.Unlock()

	osDark := true
	if h.isStarted() {
		osDark = h.app.Env.IsDarkMode()
	}
	opts := mainWindowOptions(windowBackground(savedTheme(h.settingsPath, h.policy), osDark))
	opts.Name = name
	opts.Title = h.title()
	w := h.app.Window.NewWithOptions(opts)
	id := w.ID()

	h.mu.Lock()
	h.windows = append(h.windows, w)
	h.focused = id
	h.mu.Unlock()

	w.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
		h.mu.Lock()
		h.focused = id
		h.mu.Unlock()
	})
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		h.forget(id)
	})
	// Files dropped on the window go to its page only (filedrop.go).
	forwardFileDrops(w)
	return w
}

// markStarted records that the app runs (ApplicationStarted).
func (h *windowHost) markStarted() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = true
}

func (h *windowHost) isStarted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started
}

// openMain opens the first window and returns it; it opens the project at
// path once its page has loaded ("" for none: the last project is reopened
// as before).
func (h *windowHost) openMain(path string) *application.WebviewWindow {
	w := h.open()
	h.mu.Lock()
	openAt := h.openAt
	h.mu.Unlock()
	if openAt != nil && path != "" {
		openAt(w.ID(), path)
	}
	return w
}

// forget drops a closed window and tells the services.
func (h *windowHost) forget(id uint) {
	h.mu.Lock()
	h.windows = slices.DeleteFunc(h.windows, func(w *application.WebviewWindow) bool { return w.ID() == id })
	if h.focused == id {
		h.focused = 0
		if n := len(h.windows); n > 0 {
			h.focused = h.windows[n-1].ID()
		}
	}
	closed := slices.Clone(h.closed)
	h.mu.Unlock()
	for _, fn := range closed {
		fn(id)
	}
}

// Focus brings window id (0: the one focused last) to the front and
// returns its ID, or 0 when no window is open (the window service's Host).
func (h *windowHost) Focus(id uint) uint {
	h.mu.Lock()
	if id == 0 {
		id = h.focused
	}
	var target *application.WebviewWindow
	for _, w := range h.windows {
		if w.ID() == id {
			target = w
		}
	}
	if target == nil && len(h.windows) > 0 {
		target = h.windows[len(h.windows)-1]
	}
	h.mu.Unlock()
	if target == nil {
		return 0
	}
	if target.IsMinimised() {
		target.UnMinimise()
	}
	target.Show()
	target.Focus()
	return target.ID()
}

// Count is the number of open windows (the collection service's
// WithWindowCount). At least one: the services run for a window.
func (h *windowHost) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return max(1, len(h.windows))
}

// others is the number of open windows other than the one ctx's call came
// from (the workspace service's WithWindows).
func (h *windowHost) others(ctx context.Context) int {
	caller := windowctx.ID(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, w := range h.windows {
		if w.ID() != caller {
			n++
		}
	}
	return n
}

// retitle sets every window's title (the open project changed).
func (h *windowHost) retitle() {
	title := h.title()
	h.mu.Lock()
	windows := slices.Clone(h.windows)
	h.mu.Unlock()
	for _, w := range windows {
		w.SetTitle(title)
	}
}

// each calls fn for every open window.
func (h *windowHost) each(fn func(*application.WebviewWindow)) {
	h.mu.Lock()
	windows := slices.Clone(h.windows)
	h.mu.Unlock()
	for _, w := range windows {
		fn(w)
	}
}

// onClosed adds fn to what is told about a closed window.
func (h *windowHost) onClosed(fn func(id uint)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, fn)
}

// setOpenAt sets how a new window is told the project to open.
func (h *windowHost) setOpenAt(fn func(id uint, path string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.openAt = fn
}

// setLaunch sets the handler of a second start of PostSonde and hands it
// the ones that arrived before.
func (h *windowHost) setLaunch(fn func(args []string, workingDir string)) {
	h.mu.Lock()
	h.launch = fn
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	for _, d := range pending {
		fn(launchArgs(d.Args), d.WorkingDir)
	}
}

// secondInstance is the single-instance lock's OnSecondInstanceLaunch:
// PostSonde was started again while this one runs.
func (h *windowHost) secondInstance(d application.SecondInstanceData) {
	h.mu.Lock()
	launch := h.launch
	if launch == nil {
		h.pending = append(h.pending, d)
	}
	h.mu.Unlock()
	if launch != nil {
		launch(launchArgs(d.Args), d.WorkingDir)
	}
}

// launchArgs drops the program from a second start's command line.
func launchArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return args[1:]
}

// windowTitle is the windows' title while project (its name, "" for none)
// is open.
func windowTitle(project string) string {
	if project == "" {
		return appName
	}
	return project + " - " + appName
}
