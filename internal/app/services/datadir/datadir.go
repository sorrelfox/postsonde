// Package datadir is the bound service for the data directory setting
// (SET-03) and portable mode (APP-04): where the data is, choosing another
// folder, and restarting to switch to it. The rules and the move itself
// are internal/core/datadir. See docs/settings.md (Data directory).
package datadir

import (
	"context"
	"log/slog"

	"github.com/sorrelfox/postsonde/internal/app/apperr"

	core "github.com/sorrelfox/postsonde/internal/core/datadir"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Domain names the service.
const Domain = "datadir"

// CodeUnavailable: the service has no data directory state, no dialog, no
// file manager or no way to restart (tests, headless runs).
const CodeUnavailable = "datadir.unavailable"

// FolderDialog describes the native dialog for choosing a folder.
type FolderDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Directory is where the dialog starts ("" for the OS default).
	Directory string
}

// FolderPicker shows a native folder dialog and returns the chosen absolute
// path, or "" when the user cancels. The app layer supplies it.
type FolderPicker func(ctx context.Context, d FolderDialog) (string, error)

// DataDirService reads and changes the data directory setting.
type DataDirService struct {
	store   *core.Store
	pick    FolderPicker
	reveal  func(path string) error
	restart func()
	logger  *slog.Logger
}

// An Option configures New.
type Option func(*DataDirService)

// WithFolderPicker sets the dialog Pick shows.
func WithFolderPicker(pick FolderPicker) Option {
	return func(s *DataDirService) { s.pick = pick }
}

// WithReveal sets how Reveal shows a folder in the file manager.
func WithReveal(reveal func(path string) error) Option {
	return func(s *DataDirService) { s.reveal = reveal }
}

// WithRestart sets how Restart quits and starts the app again.
func WithRestart(restart func()) Option {
	return func(s *DataDirService) { s.restart = restart }
}

// New returns the service over store. A nil store (tests of other
// services) makes every method report that there is no data directory.
func New(store *core.Store, logger *slog.Logger, opts ...Option) *DataDirService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &DataDirService{store: store, logger: logger.With("service", Domain)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *DataDirService) ServiceName() string { return Domain }

func unavailable(what string) error {
	return errs.New(errs.Unsupported, what+" is not available here.").WithCode(CodeUnavailable)
}

// Info returns where the data directory is, how it was chosen, the change
// scheduled for the next start and the problems found at start.
func (s *DataDirService) Info(ctx context.Context) (InfoDTO, error) {
	if err := ctx.Err(); err != nil {
		return InfoDTO{}, errs.Wrap(err, errs.KindOf(err), "Reading the data directory setting was interrupted.")
	}
	if s.store == nil {
		return InfoDTO{Mode: string(core.ModeDefault), Problems: []apperr.ErrorDTO{}}, nil
	}
	return toInfoDTO(s.store.Info(), s.restart != nil), nil
}

// Pick shows the folder dialog and checks the chosen folder (see
// the core Store.Check); Path is "" when the user cancels. The error is the
// reason the folder cannot be used, for example datadir.not_empty or
// datadir.invalid_path (inside a project, nested in the current data
// directory).
func (s *DataDirService) Pick(ctx context.Context) (CandidateDTO, error) {
	if s.store == nil || s.pick == nil {
		return CandidateDTO{}, unavailable("Choosing a data directory")
	}
	if b := s.store.Info().ChangeBlocked; b != nil {
		return CandidateDTO{}, b
	}
	path, err := s.pick(ctx, FolderDialog{
		Title:      "Choose the data directory",
		Message:    "Choose an empty folder for PostSonde's data, or one that already holds it.",
		ButtonText: "Choose",
	})
	if err != nil {
		return CandidateDTO{}, errs.Wrap(err, errs.KindOf(err), "The folder dialog could not be shown.").WithCode(CodeUnavailable)
	}
	if path == "" {
		return CandidateDTO{}, nil
	}
	return s.Check(ctx, path)
}

// Check validates path as the new data directory, as Pick does.
func (s *DataDirService) Check(ctx context.Context, path string) (CandidateDTO, error) {
	if err := ctx.Err(); err != nil {
		return CandidateDTO{}, errs.Wrap(err, errs.KindOf(err), "Checking the folder was interrupted.")
	}
	if s.store == nil {
		return CandidateDTO{}, unavailable("Choosing a data directory")
	}
	c, err := s.store.Check(path)
	if err != nil {
		return CandidateDTO{}, err
	}
	return toCandidateDTO(c), nil
}

// Schedule records a change of the data directory for the next start:
// "move" copies the data to the folder, checks the copy and switches,
// keeping the old folder; "fresh" switches to the empty folder; "use"
// switches to a folder that already holds PostSonde data. Nothing changes
// until the app restarts (Restart).
func (s *DataDirService) Schedule(ctx context.Context, req ScheduleDTO) (InfoDTO, error) {
	if err := ctx.Err(); err != nil {
		return InfoDTO{}, errs.Wrap(err, errs.KindOf(err), "Changing the data directory was interrupted.")
	}
	if s.store == nil {
		return InfoDTO{}, unavailable("Choosing a data directory")
	}
	info, err := s.store.Schedule(req.Path, core.Action(req.Action))
	if err != nil {
		return InfoDTO{}, err
	}
	s.logger.Info("data directory change scheduled for the next start", "action", req.Action, "path", req.Path)
	return toInfoDTO(info, s.restart != nil), nil
}

// Cancel drops the scheduled change.
func (s *DataDirService) Cancel(ctx context.Context) (InfoDTO, error) {
	return s.update(ctx, "Cancelling the change", (*core.Store).Cancel)
}

// DeletePrevious deletes the copy of the data a move left in the old
// folder. The UI asks the user first.
func (s *DataDirService) DeletePrevious(ctx context.Context) (InfoDTO, error) {
	info, err := s.update(ctx, "Deleting the old data", (*core.Store).DeletePrevious)
	if err == nil {
		s.logger.Info("old data directory copy deleted")
	}
	return info, err
}

// ForgetPrevious keeps the old folder and stops mentioning it.
func (s *DataDirService) ForgetPrevious(ctx context.Context) (InfoDTO, error) {
	return s.update(ctx, "Keeping the old data", (*core.Store).ForgetPrevious)
}

func (s *DataDirService) update(ctx context.Context, what string, do func(*core.Store) (core.Info, error)) (InfoDTO, error) {
	if err := ctx.Err(); err != nil {
		return InfoDTO{}, errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	if s.store == nil {
		return InfoDTO{}, unavailable(what)
	}
	info, err := do(s.store)
	if err != nil {
		return InfoDTO{}, err
	}
	return toInfoDTO(info, s.restart != nil), nil
}

// Reveal shows a folder in the file manager: which is "data" (the data
// directory in use) or "previous" (the old folder of a move).
func (s *DataDirService) Reveal(ctx context.Context, which string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Showing the folder was interrupted.")
	}
	if s.store == nil || s.reveal == nil {
		return unavailable("Showing the folder")
	}
	info := s.store.Info()
	var path string
	switch which {
	case "data":
		path = info.DataDir
	case "previous":
		if info.Previous != nil {
			path = info.Previous.DataDir
		}
	default:
		return errs.New(errs.InvalidArgument, "Unknown folder.").WithCode(core.CodeInvalidPath).WithDetail("which", which)
	}
	if path == "" {
		return errs.New(errs.NotFound, "There is no such folder.").WithCode(core.CodeNotFound).WithDetail("which", which)
	}
	if err := s.reveal(path); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The folder could not be shown.").
			WithHint("Open it yourself: "+path).WithDetail("path", path)
	}
	return nil
}

// Restart quits PostSonde and starts it again, which carries out a
// scheduled change. The UI saves pending edits first; the services save
// theirs when the app quits, as on any quit.
func (s *DataDirService) Restart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Restarting was interrupted.")
	}
	if s.restart == nil {
		return unavailable("Restarting PostSonde")
	}
	s.logger.Info("restarting")
	go s.restart() // let this call return to the UI first
	return nil
}
