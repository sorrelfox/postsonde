// Package workspace is the bound service for opening and creating projects
// (STO-01) and their local side-file directories (STO-04). The logic
// is internal/core/workspace; this service exposes it to the UI, shows the
// native folder dialog and tells the UI when the open workspace changes.
// See docs/workspace.md.
package workspace

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// CodeInvalidPurpose: PickFolder was called with an unknown purpose (kind
// invalid_argument).
const CodeInvalidPurpose = "workspace.invalid_purpose"

// CodeRevealFailed: Reveal could not hand the folder to the file manager.
// The kind comes from the cause. Details: path.
const CodeRevealFailed = "workspace.reveal_failed"

// FolderDialog describes the native dialog PickFolder (a folder) and
// PickBodyFile (a file, ChooseFile set) show.
type FolderDialog struct {
	Title      string
	Message    string
	ButtonText string
	// ChooseFile asks for one file instead of a folder.
	ChooseFile bool
}

// FolderPicker shows a native folder (or file) dialog and returns the
// chosen path, or "" when the user cancels. The app layer supplies it;
// tests fake it.
type FolderPicker func(ctx context.Context, dialog FolderDialog) (string, error)

// WorkspaceService opens, creates and closes workspaces and lists the
// recent ones. It emits workspace:changed.
type WorkspaceService struct {
	manager *core.Manager
	pick    FolderPicker
	reveal  func(path string) error
	emit    func(name string, data any)
	logger  *slog.Logger
	// otherWindows counts the app's windows other than the one a call came
	// from (WithWindows); nil is one window.
	otherWindows func(ctx context.Context) int

	// mu serialises the changes and their events, so the last event always
	// carries the workspace that is open.
	mu sync.Mutex
}

// Option configures a WorkspaceService.
type Option func(*WorkspaceService)

// WithReveal sets how Reveal shows a folder in the file manager (Finder,
// Explorer). Without it Reveal fails with kind unsupported.
func WithReveal(reveal func(path string) error) Option {
	return func(s *WorkspaceService) { s.reveal = reveal }
}

// WithWindows tells the service how many windows other than the calling
// one are open (APP-06, docs/windows.md). Every window shows the open
// project, so while others are open, Open of another folder, Create and
// Close are refused with workspace.shared_by_windows: switching the
// project under a window that may still be sending edits could lose them.
func WithWindows(others func(ctx context.Context) int) Option {
	return func(s *WorkspaceService) { s.otherWindows = others }
}

// New returns a WorkspaceService over manager. pick shows the folder dialog
// (nil makes PickFolder fail); emit sends an event to the UI (nil drops
// events). A nil logger means slog.Default().
func New(manager *core.Manager, pick FolderPicker, emit func(name string, data any), logger *slog.Logger, opts ...Option) *WorkspaceService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &WorkspaceService{manager: manager, pick: pick, emit: emit, logger: logger.With("service", Domain)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *WorkspaceService) ServiceName() string { return Domain }

// ServiceStartup reopens the workspace that was open when the app last
// quit. A folder that cannot be opened is logged and the app starts on the
// welcome screen; it never fails the startup.
func (s *WorkspaceService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.reopenLast()
	return nil
}

func (s *WorkspaceService) reopenLast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.manager.ReopenLast()
	if err != nil {
		s.logger.Warn("could not reopen the last workspace", "err", err)
		return
	}
	if w == nil {
		return
	}
	s.opened("reopened", w)
}

// Current returns the open workspace; its workspace is null when none is.
func (s *WorkspaceService) Current(ctx context.Context) (CurrentDTO, error) {
	if err := ctx.Err(); err != nil {
		return CurrentDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the project was interrupted.")
	}
	return toCurrentDTO(s.manager.Current()), nil
}

// Open opens the project in the folder at path (absolute) and makes it the
// open workspace. A folder without postsonde.json is a not_found error with
// code workspace.not_a_project; the UI offers Create for it. While other
// windows are open only the open project can be opened again
// (workspace.shared_by_windows, WithWindows).
func (s *WorkspaceService) Open(ctx context.Context, path string) (WorkspaceDTO, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceDTO{}, errs.Wrap(err, errs.KindOf(err), "Opening the project was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkShared(ctx, path); err != nil {
		return WorkspaceDTO{}, err
	}
	w, err := s.manager.Open(path)
	if err != nil {
		return WorkspaceDTO{}, err
	}
	return s.opened("opened", w), nil
}

// Create makes the folder at in.Path a new project, writing only its
// postsonde.json, and opens it. A folder that is already a project is an
// already_exists error with code workspace.already_a_project.
func (s *WorkspaceService) Create(ctx context.Context, in CreateInputDTO) (WorkspaceDTO, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceDTO{}, errs.Wrap(err, errs.KindOf(err), "Creating the project was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkShared(ctx, ""); err != nil {
		return WorkspaceDTO{}, err
	}
	w, err := s.manager.Create(in.Path, in.Name)
	if err != nil {
		return WorkspaceDTO{}, err
	}
	return s.opened("created", w), nil
}

// Close closes the open workspace, so it is not reopened on the next start.
// Closing when none is open does nothing. While other windows are open it
// is refused (workspace.shared_by_windows, WithWindows), as Create is.
func (s *WorkspaceService) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Closing the project was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkShared(ctx, ""); err != nil {
		return err
	}
	wasOpen, err := s.manager.Close()
	if wasOpen {
		s.logger.Info("workspace closed")
		s.emit(EventChanged, CurrentDTO{})
	}
	if err != nil {
		// The workspace is closed; only "do not reopen" was not saved.
		s.logger.Warn("could not update the recent workspaces", "err", err)
	}
	return nil
}

// Recent returns the recently opened workspaces, most recent first.
func (s *WorkspaceService) Recent(ctx context.Context) ([]RecentWorkspaceDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the recent projects was interrupted.")
	}
	entries, err := s.manager.Recent()
	if err != nil {
		return nil, err
	}
	return toRecentDTOs(entries), nil
}

// RemoveRecent removes path from the recent list. The folder and its local
// data are not touched.
func (s *WorkspaceService) RemoveRecent(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Updating the recent projects was interrupted.")
	}
	return s.manager.RemoveRecent(path)
}

// ClearRecent empties the recent list, except for the open project, which
// stays so it is reopened on the next start. Folders and their local data
// are not touched.
func (s *WorkspaceService) ClearRecent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Updating the recent projects was interrupted.")
	}
	return s.manager.ClearRecent()
}

// Reveal shows the project folder at path in the file manager. path must
// be the open project or a recent one (workspace.invalid_path otherwise).
func (s *WorkspaceService) Reveal(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Showing the folder was interrupted.")
	}
	if !s.manager.Known(path) {
		return errs.New(errs.InvalidArgument, "This folder is not a known project.").
			WithCode(core.CodeInvalidPath).
			WithHint("Only the open project and recent projects can be shown.").
			WithDetail("path", path)
	}
	if s.reveal == nil {
		return errs.New(errs.Unsupported, "Showing folders is not available.")
	}
	if err := s.reveal(path); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The folder could not be shown in the file manager.").
			WithCode(CodeRevealFailed).
			WithHint("Check that the folder still exists.").
			WithDetail("path", path)
	}
	return nil
}

// SaveUIState saves a value of the UI's view state (open tabs, expanded
// folders) for a workspace, in its local directory (STO-06). The workspace
// is named by ID and folder and must be the open one or one opened earlier
// since the app started, so the UI can save the state of the project it is
// leaving. Bad keys or values fail with workspace.invalid_ui_state.
func (s *WorkspaceService) SaveUIState(ctx context.Context, in SaveUIStateInputDTO) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Saving the view state was interrupted.")
	}
	return s.manager.SaveUIState(in.WorkspaceID, in.Path, in.Key, json.RawMessage(in.Value))
}

// PickFolder shows the native folder dialog and returns the chosen folder,
// or "" when the user cancels. purpose is "open" or "create" and sets the
// dialog's wording. The dialog cannot be cancelled from the UI.
func (s *WorkspaceService) PickFolder(ctx context.Context, purpose string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "Choosing a folder was interrupted.")
	}
	var dialog FolderDialog
	switch purpose {
	case PurposeOpen:
		dialog = FolderDialog{Title: "Open Project", Message: "Choose a folder with a postsonde.json file.", ButtonText: "Open"}
	case PurposeCreate:
		dialog = FolderDialog{Title: "Create Project", Message: "Choose or create an empty folder for the new project.", ButtonText: "Create Here"}
	default:
		return "", errs.New(errs.InvalidArgument, "Unknown folder dialog.").
			WithCode(CodeInvalidPurpose).
			WithDetail("purpose", purpose)
	}
	if s.pick == nil {
		return "", errs.New(errs.Unsupported, "Folder dialogs are not available.")
	}
	path, err := s.pick(ctx, dialog)
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The folder dialog could not be shown.")
	}
	return path, nil
}

// checkShared refuses to leave the open project while other windows show
// it (WithWindows): path is the folder being opened ("" for Create and
// Close); opening the open project again is allowed. Nothing is refused
// while no project is open.
func (s *WorkspaceService) checkShared(ctx context.Context, path string) error {
	if s.otherWindows == nil || s.manager.Current() == nil {
		return nil
	}
	if path != "" && s.manager.IsCurrent(path) {
		return nil
	}
	n := s.otherWindows(ctx)
	if n <= 0 {
		return nil
	}
	e := errs.New(errs.InvalidArgument, "Every PostSonde window shows the same project, so it cannot change while other windows are open.").
		WithCode(core.CodeSharedByWindows).
		WithHint("Close the other PostSonde windows first, then switch or close the project in this one.").
		WithDetail("windows", strconv.Itoa(n))
	if path != "" {
		e = e.WithDetail("path", path)
	}
	return e
}

// opened logs w, emits workspace:changed and returns its DTO. Callers hold
// s.mu.
func (s *WorkspaceService) opened(verb string, w *core.Workspace) WorkspaceDTO {
	s.logger.Info("workspace "+verb, "id", w.ID(), "path", w.Root, "local", w.LocalDir, "localStatus", w.LocalStatus)
	if w.RecentErr != nil {
		s.logger.Warn("could not update the recent workspaces", "err", w.RecentErr)
	}
	current := toCurrentDTO(w)
	s.emit(EventChanged, current)
	return *current.Workspace
}
