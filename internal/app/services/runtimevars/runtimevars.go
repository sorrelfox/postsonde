// Package runtimevars is the bound service for the open project's runtime
// variables (ENV-15): listing them, setting, editing and deleting
// one, and clearing them all, from the Runtime section of the Environments
// view. The logic is internal/core/runtimevars; the open workspace comes
// from the workspace manager. See docs/environments.md#runtime-variables.
package runtimevars

import (
	"context"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the runtime variables service.
const Domain = "runtimevars"

// EventChanged is sent after any change to the open project's runtime
// variables, whoever made it (this service, or extraction from a
// response): the Runtime section reloads its list and the editors reload
// what their {{variables}} resolve to. The app sends it from the
// registry's change hook (Changed).
const EventChanged = Domain + ":changed"

// ChangedDTO is the payload of EventChanged.
type ChangedDTO struct {
	// Count is the number of runtime variables after the change.
	Count int `json:"count"`
}

func init() {
	application.RegisterEvent[ChangedDTO](EventChanged)
}

// Changed returns the registry's change hook (core.NewRegistry): it sends
// EventChanged through emit with the number of runtime variables. A nil
// emit sends nothing.
func Changed(emit func(name string, data any)) func(*core.Store) {
	return func(st *core.Store) {
		if emit != nil {
			emit(EventChanged, ChangedDTO{Count: len(st.List())})
		}
	}
}

// RuntimeVarsService manages the runtime variables of the open workspace.
// Every call answers with the whole list, which the UI shows as it is.
type RuntimeVarsService struct {
	workspaces *workspace.Manager
	registry   *core.Registry
	logger     *slog.Logger
}

// New returns a RuntimeVarsService over the registry the senders render
// with. A nil logger means slog.Default().
func New(workspaces *workspace.Manager, registry *core.Registry, logger *slog.Logger) *RuntimeVarsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimeVarsService{workspaces: workspaces, registry: registry, logger: logger.With("service", Domain)}
}

// ServiceName names the service in Wails logs.
func (s *RuntimeVarsService) ServiceName() string { return Domain }

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}

// store returns the open project's runtime variables, workspace.none_open
// without one.
func (s *RuntimeVarsService) store() (*core.Store, error) {
	w := s.workspaces.Current()
	if w == nil {
		_, err := s.workspaces.LocalDir()
		return nil, err
	}
	return s.registry.For(w)
}

// List returns the runtime variables of the open project, sorted by name.
func (s *RuntimeVarsService) List(ctx context.Context) (RuntimeVariablesDTO, error) {
	if err := interrupted(ctx, "Loading the runtime variables"); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	st, err := s.store()
	if err != nil {
		return RuntimeVariablesDTO{}, err
	}
	return toDTO(st), nil
}

// Set sets a runtime variable. With PreviousName set to another name it
// replaces that one (a rename). Fails with runtimevars.invalid_name for a
// name a {{reference}} cannot spell, and runtimevars.local_data_failed
// when a persisted one cannot be saved (nothing changes then).
func (s *RuntimeVarsService) Set(ctx context.Context, in SetInputDTO) (RuntimeVariablesDTO, error) {
	if err := interrupted(ctx, "Saving the runtime variable"); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	st, err := s.store()
	if err != nil {
		return RuntimeVariablesDTO{}, err
	}
	if err := st.Replace(in.PreviousName, in.toCore()); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	s.logger.Info("runtime variable set", "secret", in.Secret, "persist", in.Persist)
	return toDTO(st), nil
}

// Delete removes the runtime variable called name (runtimevars.not_found
// when there is none).
func (s *RuntimeVarsService) Delete(ctx context.Context, name string) (RuntimeVariablesDTO, error) {
	if err := interrupted(ctx, "Deleting the runtime variable"); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	st, err := s.store()
	if err != nil {
		return RuntimeVariablesDTO{}, err
	}
	if err := st.Delete(name); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	return toDTO(st), nil
}

// Clear removes every runtime variable of the open project, session and
// persisted, also when the persisted ones cannot be read.
func (s *RuntimeVarsService) Clear(ctx context.Context) (RuntimeVariablesDTO, error) {
	if err := interrupted(ctx, "Clearing the runtime variables"); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	w := s.workspaces.Current()
	if w == nil {
		_, err := s.workspaces.LocalDir()
		return RuntimeVariablesDTO{}, err
	}
	if err := s.registry.Clear(w); err != nil {
		return RuntimeVariablesDTO{}, err
	}
	s.logger.Info("runtime variables cleared")
	st, err := s.registry.For(w)
	if err != nil {
		return RuntimeVariablesDTO{}, err
	}
	return toDTO(st), nil
}
