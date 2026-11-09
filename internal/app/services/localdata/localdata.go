// Package localdata is the bound service behind Settings, Privacy, Local
// data (PRV-13): how much history, run history, cookies, OAuth 2.0
// tokens, cached GraphQL and gRPC schemas and runtime variables PostSonde
// keeps on this computer, for the open project or for every project, and one action
// that clears the kinds the user picks. The disk side is
// internal/core/localdata; services that keep some of it in memory are
// told first through hooks (WithHook). See
// docs/privacy.md#clearing-local-data.
package localdata

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/localdata"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the service.
const Domain = "localdata"

// Scopes: the open project, or every project the data directory holds.
const (
	ScopeProject = "project"
	ScopeAll     = "all"
)

// Error codes, besides core.CodeInvalidCategory and core.CodeClearFailed.
const (
	// CodeInvalidScope: the scope is not "project" or "all" (kind
	// invalid_argument). Details: scope.
	CodeInvalidScope = "localdata.invalid_scope"
	// CodeNothingSelected: Clear was given no category (kind
	// invalid_argument).
	CodeNothingSelected = "localdata.nothing_selected"
	// CodeUnavailable: PostSonde runs without a data directory, so it
	// keeps nothing on disk (kind unsupported).
	CodeUnavailable = "localdata.unavailable"
)

// Hook is told that category data of the project whose local directory is
// localDir is about to be removed, before its files are; open is set when
// that is the open project. A service that holds such data in memory
// drops it there, so it neither shows nor writes it again. An error stops
// that category of that project: its files are kept.
type Hook func(ctx context.Context, localDir string, open bool) error

// Option configures a LocalDataService.
type Option func(*LocalDataService)

// WithHook calls h before category c is removed from a project.
func WithHook(c core.Category, h Hook) Option {
	return func(s *LocalDataService) { s.hooks[c] = h }
}

// LocalDataService measures and clears the local data of the projects.
type LocalDataService struct {
	workspaces *workspace.Manager
	dataDir    string
	logger     *slog.Logger
	hooks      map[core.Category]Hook

	// mu serialises clears.
	mu sync.Mutex
}

// New returns a LocalDataService over the data directory dataDir ("" when
// PostSonde keeps nothing on disk) and its open project. A nil logger
// means slog.Default().
func New(workspaces *workspace.Manager, dataDir string, logger *slog.Logger, opts ...Option) *LocalDataService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &LocalDataService{workspaces: workspaces, dataDir: dataDir, logger: logger.With("service", Domain), hooks: map[core.Category]Hook{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *LocalDataService) ServiceName() string { return Domain }

// UsageInputDTO is what Usage takes.
type UsageInputDTO struct {
	// Scope is "project" (the open project) or "all" (every project).
	Scope string `json:"scope"`
}

// CategoryUsageDTO is how much of a kind of local data there is, or was
// removed.
type CategoryUsageDTO struct {
	// Category is "history", "runs", "cookies", "tokens", "schemas" or
	// "runtimeVariables".
	Category string `json:"category"`
	// Files counts the files; Bytes their sizes.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// UsageDTO is what Usage returns.
type UsageDTO struct {
	Scope string `json:"scope"`
	// Projects counts the projects measured: 1 for "project", every local
	// directory for "all".
	Projects int `json:"projects"`
	// Project is the open project's name, "" when none is open.
	Project string `json:"project"`
	// Categories are every category, in the order the UI lists them.
	Categories []CategoryUsageDTO `json:"categories"`
}

// ClearInputDTO is what Clear takes.
type ClearInputDTO struct {
	Scope string `json:"scope"`
	// Categories are the kinds to remove; the others are kept.
	Categories []string `json:"categories"`
}

// ClearFailureDTO is a category that could not be cleared everywhere.
type ClearFailureDTO struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	Hint     string `json:"hint"`
}

// ClearResultDTO is what Clear returns.
type ClearResultDTO struct {
	Scope    string `json:"scope"`
	Projects int    `json:"projects"`
	// Removed is what was removed, per category asked for, in the UI's
	// order.
	Removed []CategoryUsageDTO `json:"removed"`
	// Failed lists the categories that could not be removed from every
	// project; their files there are kept.
	Failed []ClearFailureDTO `json:"failed"`
}

// target is a project's local directory.
type target struct {
	dir  string
	open bool
}

// targets returns the local directories of scope and the open project's
// name.
func (s *LocalDataService) targets(scope string) ([]target, string, error) {
	if s.dataDir == "" {
		return nil, "", errs.New(errs.Unsupported, "PostSonde keeps no local data in this session.").
			WithCode(CodeUnavailable).
			WithHint("It has no data directory, so nothing is written to disk.")
	}
	var open, name string
	if w := s.workspaces.Current(); w != nil {
		open, name = filepath.Clean(w.LocalDir), w.Name()
	}
	switch scope {
	case ScopeProject:
		if open == "" {
			_, err := s.workspaces.LocalDir()
			if err == nil {
				err = errs.New(errs.NotFound, "No project is open.").WithCode(workspace.CodeNoWorkspace)
			}
			return nil, "", err
		}
		return []target{{dir: open, open: true}}, name, nil
	case ScopeAll:
		dirs, err := core.LocalDirs(s.dataDir)
		if err != nil {
			return nil, "", err
		}
		out := make([]target, 0, len(dirs)+1)
		found := false
		for _, d := range dirs {
			isOpen := open != "" && filepath.Clean(d) == open
			found = found || isOpen
			out = append(out, target{dir: d, open: isOpen})
		}
		if open != "" && !found {
			out = append(out, target{dir: open, open: true})
		}
		return out, name, nil
	}
	return nil, "", errs.New(errs.InvalidArgument, "The scope is not known.").
		WithCode(CodeInvalidScope).
		WithHint(`Use "project" or "all".`).
		WithDetail("scope", scope)
}

// Usage returns how much of each kind of local data the projects of
// in.Scope keep. Files that cannot be looked at are not counted.
//
// Errors: localdata.invalid_scope, workspace.none_open for "project"
// without an open project, localdata.unavailable without a data
// directory.
func (s *LocalDataService) Usage(ctx context.Context, in UsageInputDTO) (UsageDTO, error) {
	if err := ctx.Err(); err != nil {
		return UsageDTO{}, errs.Wrap(err, errs.KindOf(err), "Measuring the local data was interrupted.")
	}
	targets, name, err := s.targets(in.Scope)
	if err != nil {
		return UsageDTO{}, err
	}
	out := UsageDTO{Scope: in.Scope, Projects: len(targets), Project: name, Categories: []CategoryUsageDTO{}}
	for _, c := range core.All() {
		var u core.Usage
		for _, t := range targets {
			u = u.Add(core.Measure(t.dir, c))
		}
		out.Categories = append(out.Categories, CategoryUsageDTO{Category: string(c), Files: u.Files, Bytes: u.Bytes})
	}
	return out, nil
}

// Clear removes the kinds of local data in.Categories from the projects
// of in.Scope, and keeps every other kind (PRV-13). For each project and
// kind, the service that holds it in memory is told first (WithHook: the
// open project's history is cleared through HistoryService.Clear, its
// cookie jars forgotten, its tokens dropped, …), then the files are
// deleted, without being read: encrypted files go while the secrets are
// locked too. It goes on past a failure; Failed lists the kinds that are
// not gone everywhere.
//
// Errors: localdata.invalid_scope, localdata.invalid_category,
// localdata.nothing_selected, workspace.none_open for "project" without
// an open project, localdata.unavailable without a data directory.
func (s *LocalDataService) Clear(ctx context.Context, in ClearInputDTO) (ClearResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return ClearResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Clearing the local data was interrupted.")
	}
	var cats []core.Category
	for _, name := range in.Categories {
		c, err := core.Parse(name)
		if err != nil {
			return ClearResultDTO{}, err
		}
		if !slices.Contains(cats, c) {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		return ClearResultDTO{}, errs.New(errs.InvalidArgument, "No kind of local data was chosen.").
			WithCode(CodeNothingSelected).
			WithHint("Tick what to clear.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	targets, _, err := s.targets(in.Scope)
	if err != nil {
		return ClearResultDTO{}, err
	}
	out := ClearResultDTO{Scope: in.Scope, Projects: len(targets), Removed: []CategoryUsageDTO{}, Failed: []ClearFailureDTO{}}
	for _, c := range core.All() {
		if !slices.Contains(cats, c) {
			continue
		}
		var removed core.Usage
		var failure error
		for _, t := range targets {
			// Measured first: a hook may remove files itself (the history
			// and the token store clear theirs).
			before := core.Measure(t.dir, c)
			// Told once per project, even with nothing on disk: memory
			// may hold what was never written (session runtime variables,
			// cookies not saved yet).
			if h := s.hooks[c]; h != nil {
				if err := h(context.WithoutCancel(ctx), t.dir, t.open); err != nil {
					failure = firstOf(failure, err)
					continue
				}
			}
			_, err := core.Remove(t.dir, c)
			failure = firstOf(failure, err)
			after := core.Measure(t.dir, c)
			removed = removed.Add(core.Usage{Files: max(before.Files-after.Files, 0), Bytes: max(before.Bytes-after.Bytes, 0)})
		}
		out.Removed = append(out.Removed, CategoryUsageDTO{Category: string(c), Files: removed.Files, Bytes: removed.Bytes})
		if failure != nil {
			s.logger.Warn("clearing local data failed", "category", c, "err", failure)
			f := ClearFailureDTO{Category: string(c), Message: failure.Error()}
			var e *errs.Error
			if errors.As(failure, &e) {
				f.Message, f.Hint = e.Message, e.Hint
			}
			out.Failed = append(out.Failed, f)
		}
		s.logger.Info("local data cleared", "category", c, "scope", in.Scope, "projects", len(targets), "files", removed.Files, "bytes", removed.Bytes)
	}
	return out, nil
}

func firstOf(first, err error) error {
	if first != nil {
		return first
	}
	return err
}
