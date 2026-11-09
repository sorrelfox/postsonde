// Package window is the bound service for the app's windows (APP-06): a
// new window, the calling window's ID, and what a launch of a second
// PostSonde process asks for. Every window shows the one open project; see
// docs/windows.md for what is shared and what each window keeps.
//
// The Wails side (creating windows, focusing them) is the Host the app
// layer passes in (internal/app/windows.go); tests fake it.
package window

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// CodeProjectDiffers: OpenInNewWindow was given a project other than the
// open one (kind invalid_argument). Every window shows the same project.
// Details: path.
const CodeProjectDiffers = "window.project_differs"

// Host opens and focuses the app's windows.
type Host interface {
	// Open opens a new window and returns its ID.
	Open() (uint, error)
	// Focus brings window id to the front (0: the most recently focused
	// one) and returns its ID, or 0 when no window is open.
	Focus(id uint) uint
}

// WindowService opens windows and tells each window who it is. It emits
// window:open-project.
type WindowService struct {
	host     Host
	projects *coreworkspace.Manager
	emit     func(name string, data any)
	logger   *slog.Logger

	// mu serialises opening windows with Current, so a new window's page
	// never asks for its start project before it is recorded.
	mu sync.Mutex
	// openPaths are the project folders windows were opened for, by
	// window ID, until the window's page takes them (Current).
	openPaths map[uint]string
}

// New returns a WindowService opening windows through host on the
// projects of manager. emit sends an event to the UI (nil drops events). A
// nil logger means slog.Default().
func New(host Host, projects *coreworkspace.Manager, emit func(name string, data any), logger *slog.Logger) *WindowService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	return &WindowService{host: host, projects: projects, emit: emit, logger: logger.With("service", Domain), openPaths: map[uint]string{}}
}

// ServiceName names the service in Wails logs.
func (s *WindowService) ServiceName() string { return Domain }

// Current returns the calling window's ID and, the first time only, the
// project folder the window was opened for ("" for none): the page opens
// it when it has loaded, like a project chosen in the UI, so a failure
// shows there.
func (s *WindowService) Current(ctx context.Context) (WindowDTO, error) {
	if err := ctx.Err(); err != nil {
		return WindowDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the window was interrupted.")
	}
	id := windowctx.ID(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.openPaths[id]
	delete(s.openPaths, id)
	return WindowDTO{ID: id, OpenPath: path}, nil
}

// NewWindow opens another window. It shows the open project, or the
// welcome screen when none is open.
func (s *WindowService) NewWindow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Opening a window was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.openLocked("")
	return err
}

// OpenInNewWindow opens a new window on the project in the folder at path.
// With no project open the new window opens it (and every window then
// shows it); the open project gets a new window. Another project is
// refused with window.project_differs: every window shows the same
// project.
func (s *WindowService) OpenInNewWindow(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Opening a window was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.TrimSpace(path) == "" || s.projects.IsCurrent(path):
		_, err := s.openLocked("")
		return err
	case s.projects.Current() == nil:
		_, err := s.openLocked(path)
		return err
	}
	return errs.New(errs.InvalidArgument, "Every PostSonde window shows the same project, and another project is open.").
		WithCode(CodeProjectDiffers).
		WithHint("Close the other PostSonde windows, then switch to this project in the project menu.").
		WithDetail("path", path)
}

// openLocked opens a window that opens the project at path once its page
// has loaded ("" for none). s.mu must be held.
func (s *WindowService) openLocked(path string) (uint, error) {
	id, err := s.host.Open()
	if err != nil {
		s.logger.Error("could not open a window", "err", err)
		return 0, errs.Wrap(err, errs.Internal, "The window could not be opened.")
	}
	if path != "" {
		s.openPaths[id] = path
	}
	s.logger.Info("window opened", "window", id, "project", path)
	return id, nil
}

// OpenAt makes window id open the project at path when its page has
// loaded: the folder PostSonde was started with (ProjectArg). Not a bound
// method.
func OpenAt(s *WindowService, id uint, path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openPaths[id] = path
}

// Closed forgets what window id was opened for. Not a bound method.
func Closed(s *WindowService, id uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.openPaths, id)
}

// Launch handles a second start of PostSonde while this one runs (the
// single-instance lock; args are its command line without the program,
// workingDir its working directory). It opens a window in this process
// instead:
//
//   - no project folder given, or the open project: a new window on it;
//   - a project while none is open: a new window that opens it;
//   - another project: the most recent window is brought to the front and
//     asked to open it (window:open-project). Its UI switches projects as
//     if the user had chosen it, which the workspace service refuses while
//     other windows are open.
//
// Not a bound method.
func Launch(s *WindowService, args []string, workingDir string) {
	path := ProjectArg(args, workingDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Info("PostSonde started again", "project", path)
	switch {
	case path == "" || s.projects.IsCurrent(path):
		_, _ = s.openLocked("")
		return
	case s.projects.Current() == nil:
		_, _ = s.openLocked(path)
		return
	}
	id := s.host.Focus(0)
	if id == 0 {
		_, _ = s.openLocked(path)
		return
	}
	s.emit(EventOpenProject, OpenProjectDTO{Window: id, Path: path})
}

// ProjectArg returns the project folder a command line names: its first
// argument that is not an option (options start with "-", like macOS's
// -psn_… process serial number; "--" ends the options), made absolute
// against workingDir. "" when there is none.
func ProjectArg(args []string, workingDir string) string {
	options := true
	for _, a := range args {
		if options && a == "--" {
			options = false
			continue
		}
		if a == "" || (options && strings.HasPrefix(a, "-")) {
			continue
		}
		if !filepath.IsAbs(a) {
			if workingDir == "" {
				return ""
			}
			a = filepath.Join(workingDir, a)
		}
		return filepath.Clean(a)
	}
	return ""
}
