// Package collection is the bound service for the request tree of the open
// project (STO-02): folders as directories, one JSON file per request.
// The logic is internal/core/collection; this service loads the tree of
// the workspace that internal/core/workspace has open and gives the UI a
// flat DTO of it. See docs/collection.md.
package collection

import (
	"context"
	"log/slog"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the collection service.
const Domain = "collection"

// CollectionService gives the UI the request tree of the open project.
// The tree is loaded when first asked for after a workspace is opened, so
// it follows workspace:changed without listening to it: the UI asks for
// Tree again when that event arrives.
type CollectionService struct {
	manager *coreworkspace.Manager
	emit    func(name string, data any)
	logger  *slog.Logger

	// mu guards the cached tree; later tree operations and saves go
	// through it too, since core.Tree is not safe for concurrent use.
	mu sync.Mutex
	// ws is the workspace snapshot tree was loaded for. Every Open
	// returns a new snapshot, so reopening the same folder reloads.
	ws   *coreworkspace.Workspace
	tree *core.Tree

	// Auto-save (autosave.go): pending request edits, written through
	// saver.
	saver        *autosave.Saver[saveKey, pendingSave]
	shuttingDown bool

	// Imports (import.go): the formats read and the file dialog.
	importers *importer.Registry
	pickFile  FilePicker
	// Exports (export_postman.go): the save dialog.
	pickSave SavePicker

	// External changes (watch.go, STO-05).
	watch watchState

	// Edits from several windows (windows.go, APP-06).
	edits       editState
	windowCount func() int
}

// New returns a CollectionService for the workspaces of manager. emit
// sends an event to the UI (nil drops events). A nil logger means
// slog.Default(). The options set the auto-save delays.
func New(manager *coreworkspace.Manager, emit func(name string, data any), logger *slog.Logger, opts ...Option) *CollectionService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	s := &CollectionService{manager: manager, emit: emit, logger: logger.With("service", Domain),
		importers: o.importers, pickFile: o.pickFile, pickSave: o.pickSave, windowCount: o.windowCount}
	if s.importers == nil {
		s.importers = defaultImporters()
	}
	s.initAutosave(o)
	s.watch.interval = o.watchInterval
	if o.clock != nil {
		s.watch.clock = o.clock
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *CollectionService) ServiceName() string { return Domain }

// Tree returns the request tree of the open project, loading it if it has
// not been loaded since the project was opened. Files that did not load
// are problems in the result, not errors. Without an open project it
// fails with not_found, code workspace.none_open.
func (s *CollectionService) Tree(ctx context.Context) (TreeDTO, error) {
	return s.get(ctx, false)
}

// Reload reads the request tree of the open project from disk again, for
// changes made outside PostSonde, and returns it as Tree does. Pending
// edits are written first; what changed on disk is handled and reported
// as the watcher's changes are (collection:external-change, STO-05).
func (s *CollectionService) Reload(ctx context.Context) (TreeDTO, error) {
	return s.get(ctx, true)
}

func (s *CollectionService) get(ctx context.Context, reload bool) (TreeDTO, error) {
	if err := ctx.Err(); err != nil {
		return TreeDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the requests was interrupted.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(reload)
	if err != nil {
		return TreeDTO{}, err
	}
	return s.treeDTOLocked(ws, tree), nil
}

// currentLocked returns the open workspace and its tree, loading the tree
// when the workspace changed or reload is set. s.mu must be held.
func (s *CollectionService) currentLocked(reload bool) (*coreworkspace.Workspace, *core.Tree, error) {
	ws := s.manager.Current()
	if ws == nil {
		s.ws, s.tree = nil, nil
		s.stopWatchLocked()
		s.watch.ws, s.watch.conflicts = nil, nil
		return nil, nil, errs.New(errs.NotFound, "No project is open.").
			WithCode(coreworkspace.CodeNoWorkspace).
			WithHint("Open a project folder or create a project first.")
	}
	if reload && ws == s.ws && s.tree != nil {
		// Pending edits go to disk first; a file changed on disk refuses
		// them, and the refresh sets them aside as conflicts.
		s.flushDirLocked(ws.Root)
		s.carryPendingLocked(s.tree)
		s.refreshLocked(ws, s.tree)
		// Edits whose write failed are tried again after the save delay,
		// as when they move to a new tree.
		for _, k := range s.saver.Keys() {
			if k.tree == s.tree && s.saver.Failed(k) {
				if p, ok := s.saver.Get(k); ok {
					s.saver.Put(k, p)
				}
			}
		}
		if s.watch.watcher != nil {
			s.watch.watcher.Rebase()
		}
		return s.ws, s.tree, nil
	}
	if reload || ws != s.ws || s.tree == nil {
		// Pending edits go to disk first, so the new tree reads them.
		s.flushDirLocked(ws.Root)
		tree, problems := core.Load(ws.Root)
		// Every file the tree writes leaves its typed secrets in the local
		// side files (PRV-09).
		tree.KeepSecretsLocal(requestsecrets.New(ws.LocalDir))
		s.carryPendingLocked(tree)
		s.ws, s.tree = ws, tree
		s.startWatchLocked(ws)
		s.logger.Info("request tree loaded", "workspace", ws.ID(), "nodes", tree.Len(), "problems", len(problems))
		for _, p := range problems {
			s.logger.Warn("project file problem", "path", p.Path, "code", p.Err.Code, "err", p.Err)
		}
	}
	return s.ws, s.tree, nil
}
