// Package environments is the bound service for a project's environments
// (ENV-01): listing, creating, renaming and deleting them, editing
// their variables and choosing the active one. The logic is
// internal/core/environments; the open workspace comes from the workspace
// manager. See docs/environments.md.
package environments

import (
	"context"
	"log/slog"
	"sync"

	core "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the environments service.
const Domain = "environments"

// EnvironmentsService manages the environments of the open workspace. The
// UI reloads the list when workspace:changed arrives and applies each
// call's result; after every successful change it sends
// environments:changed (EventChanged), for the views that depend on the
// variables.
type EnvironmentsService struct {
	workspaces *workspace.Manager
	logger     *slog.Logger
	emit       func(name string, data any)
	// globals reads and writes postsonde.json's variables (globals.go).
	globals Globals
	// written is told after every change this service wrote, so the
	// project watcher does not take it for an outside change (STO-05).
	written func()
	// pickSave and version are for Export (export.go, WithExport).
	pickSave SavePicker
	version  string

	// mu serialises every call: a Store is not safe for concurrent use and
	// a save must not interleave with a rename of the same file.
	mu sync.Mutex
}

// Option configures an EnvironmentsService.
type Option func(*EnvironmentsService)

// WithEmit sets how events reach the UI. Without it no events are sent.
func WithEmit(emit func(name string, data any)) Option {
	return func(s *EnvironmentsService) { s.emit = emit }
}

// WithWritten sets a function called after every successful change, once
// the files are written: the collection service's watcher uses it to tell
// the app's own writes of environment files from outside edits (STO-05).
func WithWritten(fn func()) Option {
	return func(s *EnvironmentsService) { s.written = fn }
}

// New returns an EnvironmentsService over the workspace manager. A nil
// logger means slog.Default().
func New(workspaces *workspace.Manager, logger *slog.Logger, opts ...Option) *EnvironmentsService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &EnvironmentsService{workspaces: workspaces, logger: logger.With("service", Domain)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// changed sends EventChanged with the active environment of st. Callers
// hold s.mu.
func (s *EnvironmentsService) changed(st *core.Store) {
	if s.written != nil {
		s.written()
	}
	if s.emit == nil {
		return
	}
	active := ""
	if env, ok, err := st.Active(); err == nil && ok {
		active = env.ID
	}
	s.emit(EventChanged, ChangedDTO{ActiveID: active})
}

// ServiceName names the service in Wails logs.
func (s *EnvironmentsService) ServiceName() string { return Domain }

// store returns the Store of the open workspace, or workspace.none_open.
func (s *EnvironmentsService) store() (*core.Store, error) {
	w := s.workspaces.Current()
	if w == nil {
		_, err := s.workspaces.LocalDir()
		return nil, err
	}
	return core.New(w.Root, w.LocalDir, w.Newer()), nil
}

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}

// List returns every environment of the open workspace, the active one and
// the files that could not be loaded.
func (s *EnvironmentsService) List(ctx context.Context) (EnvironmentsDTO, error) {
	if err := interrupted(ctx, "Loading the environments"); err != nil {
		return EnvironmentsDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return EnvironmentsDTO{}, err
	}
	snap, err := st.List()
	if err != nil {
		return EnvironmentsDTO{}, err
	}
	for _, p := range snap.Problems {
		s.logger.Warn("skipped an environment file", "file", p.FileName, "err", p.Err)
	}
	return toEnvironmentsDTO(snap), nil
}

// Create adds an empty environment called name. A name another
// environment has (ignoring case) is an already_exists error with code
// environments.name_taken.
func (s *EnvironmentsService) Create(ctx context.Context, name string) (EnvironmentDTO, error) {
	if err := interrupted(ctx, "Creating the environment"); err != nil {
		return EnvironmentDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return EnvironmentDTO{}, err
	}
	env, err := st.Create(name)
	if err != nil {
		return EnvironmentDTO{}, err
	}
	s.logger.Info("environment created", "id", env.ID, "file", env.FileName)
	s.changed(st)
	return toEnvironmentDTO(env), nil
}

// Rename renames an environment and its file.
func (s *EnvironmentsService) Rename(ctx context.Context, in RenameInputDTO) (EnvironmentDTO, error) {
	if err := interrupted(ctx, "Renaming the environment"); err != nil {
		return EnvironmentDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return EnvironmentDTO{}, err
	}
	env, err := st.Rename(in.ID, in.Name)
	if err != nil {
		return EnvironmentDTO{}, err
	}
	s.logger.Info("environment renamed", "id", env.ID, "file", env.FileName)
	s.changed(st)
	return toEnvironmentDTO(env), nil
}

// Delete deletes an environment's file and its local secret values. When it
// was active, no environment is active afterwards.
func (s *EnvironmentsService) Delete(ctx context.Context, id string) error {
	if err := interrupted(ctx, "Deleting the environment"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return err
	}
	if err := st.Delete(id); err != nil {
		return err
	}
	s.logger.Info("environment deleted", "id", id)
	s.changed(st)
	return nil
}

// SaveVariables replaces an environment's variables. Secret values are
// kept in the local directory, never in the project file.
func (s *EnvironmentsService) SaveVariables(ctx context.Context, in SaveVariablesInputDTO) (EnvironmentDTO, error) {
	if err := interrupted(ctx, "Saving the variables"); err != nil {
		return EnvironmentDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return EnvironmentDTO{}, err
	}
	env, err := st.SaveVariables(in.ID, in.toCore())
	if err != nil {
		return EnvironmentDTO{}, err
	}
	s.changed(st)
	return toEnvironmentDTO(env), nil
}

// SetActive makes the environment with the given ID active; "" means none.
// The choice is saved in the workspace's local directory.
func (s *EnvironmentsService) SetActive(ctx context.Context, id string) error {
	if err := interrupted(ctx, "Switching the environment"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return err
	}
	if err := st.SetActive(id); err != nil {
		return err
	}
	s.logger.Info("active environment changed", "id", id)
	s.changed(st)
	return nil
}

// SetColor sets an environment's accent colour (ENV-08), "#rrggbb" or ""
// for none. It is saved in the environment file, shared with the project.
// Anything else is an invalid_argument error with code
// environments.invalid_color.
func (s *EnvironmentsService) SetColor(ctx context.Context, in SetColorInputDTO) (EnvironmentDTO, error) {
	if err := interrupted(ctx, "Saving the environment colour"); err != nil {
		return EnvironmentDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return EnvironmentDTO{}, err
	}
	env, err := st.SetColor(in.ID, in.Color)
	if err != nil {
		return EnvironmentDTO{}, err
	}
	s.logger.Info("environment colour changed", "id", env.ID, "file", env.FileName)
	s.changed(st)
	return toEnvironmentDTO(env), nil
}
