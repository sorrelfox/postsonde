package collection

import (
	"context"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Auto-save (STO-03): UpdateRequest keeps the edited request in
// memory and an autosave.Saver writes it with Tree.SaveRequest once edits
// pause. See docs/collection.md#auto-save.
//
// Locking: every write runs with s.mu held (the Saver's Due hook takes it),
// so the tree is never used concurrently. The order is always s.mu, then
// the Saver's locks.

// Option configures a CollectionService.
type Option func(*options)

type options struct {
	delay    time.Duration
	maxDelay time.Duration
	clock    autosave.Clock
	// Imports (import.go).
	pickFile  FilePicker
	importers *importer.Registry
	// Exports (export_postman.go).
	pickSave SavePicker
	// External changes (watch.go); zero watches nothing.
	watchInterval time.Duration
	// The number of open windows (windows.go); nil is one.
	windowCount func() int
}

// WithSaveDelay sets how long after the last edit a request is written,
// and the longest continuous editing can hold a write back. Zero keeps the
// defaults, autosave.DefaultDelay and autosave.DefaultMaxDelay.
func WithSaveDelay(delay, maxDelay time.Duration) Option {
	return func(o *options) { o.delay, o.maxDelay = delay, maxDelay }
}

// withClock replaces the clock, for tests.
func withClock(c autosave.Clock) Option {
	return func(o *options) { o.clock = c }
}

// saveKey names a pending save. It holds the tree the edit was made in, so
// a save still goes to its own project after another one is opened, and
// the node is looked up by ID when the write runs, so a rename or move in
// between is followed.
type saveKey struct {
	tree *core.Tree
	id   string
}

type pendingSave struct {
	// file is what goes to the project: typed auth secrets are split off
	// into secrets, which go to the side file in localDir (auth.go).
	file        projectfile.File
	secrets     map[string]string
	localDir    string
	revision    int
	workspaceID string
	// window is the window the edit came from (windowctx), sent back in
	// collection:saved and collection:save-failed.
	window uint
	// scratch is set for a scratch request (COL-06): file goes to the
	// scratch directory in localDir, not to the project.
	scratch bool
	// folder is set for a folder's settings (COL-03, folder.go): file is
	// a *projectfile.Folder whose auth, headers and variables are written
	// over the folder's current _folder.json (writeFolderLocked).
	folder bool
}

func (s *CollectionService) initAutosave(o options) {
	clock := o.clock
	if clock == nil {
		clock = autosave.SystemClock()
	}
	s.saver = autosave.New(autosave.Options[saveKey, pendingSave]{
		Delay:    o.delay,
		MaxDelay: o.maxDelay,
		Clock:    clock,
		Write: func(k saveKey, p pendingSave) error {
			// The secrets first: a failed project write is retried, and a
			// failed secrets write leaves the project file as it was.
			if err := requestsecrets.New(p.localDir).Write(k.id, p.secrets); err != nil {
				return err
			}
			if p.scratch {
				return core.NewScratch(scratchDir(p.localDir)).Save(k.id, p.file)
			}
			if p.folder {
				return writeFolder(k.tree, k.id, p.file)
			}
			_, err := k.tree.SaveRequest(k.id, p.file)
			return err
		},
		Due: func(k saveKey) {
			s.mu.Lock()
			defer s.mu.Unlock()
			_ = s.saver.Flush(k)
		},
		Saved: func(k saveKey, p pendingSave, at time.Time) {
			s.logger.Debug("request auto-saved", "id", k.id, "revision", p.revision)
			if !s.shuttingDown {
				s.emit(EventSaved, SavedEventDTO{
					WorkspaceID: p.workspaceID,
					ID:          k.id,
					Revision:    p.revision,
					Window:      p.window,
					SavedAt:     at.UTC().Format(time.RFC3339Nano),
				})
			}
		},
		Failed: func(k saveKey, p pendingSave, err error) {
			s.logger.Warn("request auto-save failed", "id", k.id, "revision", p.revision, "err", err)
			// The file changed on disk since it was read (STO-05): the
			// watcher's refresh, asked for now, sets the edit aside as a
			// conflict and reports it; not a failure to show. Runs with
			// s.mu held, like every write.
			if isChangedOnDisk(err) && s.pokeWatchLocked() {
				return
			}
			if !s.shuttingDown {
				s.emit(EventSaveFailed, SaveFailedEventDTO{
					WorkspaceID: p.workspaceID,
					ID:          k.id,
					Revision:    p.revision,
					Window:      p.window,
					Error:       apperr.ToDTO(err),
				})
			}
		},
	})
}

// Request returns the content of the request id in the open project,
// including edits that are not written yet, with its version (windows.go):
// an edit made from it passes the version to UpdateRequest.
func (s *CollectionService) Request(ctx context.Context, id string) (RequestDTO, error) {
	if err := saveInterrupted(ctx, "Loading the request"); err != nil {
		return RequestDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return RequestDTO{}, err
	}
	dto, err := s.requestLocked(ws, tree, id)
	if err != nil {
		return RequestDTO{}, err
	}
	dto.Version = s.versionLocked(ws, id)
	return dto, nil
}

// requestLocked returns the content of request id of tree, pending edits
// included. s.mu must be held.
func (s *CollectionService) requestLocked(ws *coreworkspace.Workspace, tree *core.Tree, id string) (RequestDTO, error) {
	if p, ok := s.saver.Get(saveKey{tree, id}); ok && !p.folder {
		return toRequestDTO(mergeSecrets(p.file, p.secrets)), nil
	}
	n := tree.Node(id)
	if n == nil && isScratchLocked(ws, tree, id) {
		it, err := scratchOf(ws).Get(id)
		if err != nil {
			return RequestDTO{}, err
		}
		secrets, err := readSecretsFor(ws.LocalDir, id, it.File)
		if err != nil {
			return RequestDTO{}, err
		}
		return toRequestDTO(mergeSecrets(it.File, secrets)), nil
	}
	if n == nil || n.Kind != core.KindRequest || n.File == nil {
		return RequestDTO{}, errs.New(errs.NotFound, "The request is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	secrets, err := readSecretsFor(ws.LocalDir, id, n.File)
	if err != nil {
		return RequestDTO{}, err
	}
	return toRequestDTO(mergeSecrets(n.File, secrets)), nil
}

// UpdateRequest replaces the content of the request in.Request.ID. The new
// content is used at once (Request returns it) and written to its file
// after the save delay; rapid updates are written once. The result of the
// write arrives as collection:saved or collection:save-failed. The result
// is the request's new version, for the next edit's BaseVersion.
//
// It fails at once, keeping nothing, for an ID that is not a request of the
// open project (collection.not_found, collection.wrong_node), a read-only
// file or project (collection.read_only), content that cannot be written
// (collection.invalid_request, projectfile.invalid_value), a request whose
// file changed on disk while it had unsaved edits
// (collection.changed_on_disk) and an edit made from an older version than
// the one held: another window changed it (collection.edited_elsewhere,
// windows.go).
func (s *CollectionService) UpdateRequest(ctx context.Context, in UpdateRequestInputDTO) (UpdateResultDTO, error) {
	if err := saveInterrupted(ctx, "Saving the request"); err != nil {
		return UpdateResultDTO{}, err
	}
	f, err := in.Request.toFile()
	if err != nil {
		return UpdateResultDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return UpdateResultDTO{}, err
	}
	// A scratch request (COL-06) is saved to the local side files instead.
	scratch := isScratchLocked(ws, tree, in.Request.ID)
	if scratch {
		err = scratchOf(ws).CheckWritable(in.Request.ID)
	} else {
		err = tree.CheckWritable(in.Request.ID, core.KindRequest)
	}
	if err != nil {
		return UpdateResultDTO{}, err
	}
	if err := s.conflictLocked(in.Request.ID); err != nil {
		return UpdateResultDTO{}, err
	}
	if err := s.checkBaseLocked(ws, in.Request.ID, in.BaseVersion); err != nil {
		return UpdateResultDTO{}, err
	}
	f, secrets := splitSecrets(f)
	if _, err := projectfile.Marshal(f); err != nil {
		return UpdateResultDTO{}, err
	}
	win := windowctx.ID(ctx)
	s.saver.Put(saveKey{tree, in.Request.ID}, pendingSave{
		file:        f,
		secrets:     secrets,
		localDir:    ws.LocalDir,
		revision:    in.Revision,
		workspaceID: ws.ID(),
		window:      win,
		scratch:     scratch,
	})
	return UpdateResultDTO{Version: s.editedLocked(ws, in.Request.ID, win)}, nil
}

// Flush writes the pending edits of the request id now, for a tab that is
// closing. It returns the write's error (also sent as
// collection:save-failed); nothing pending is not an error.
func (s *CollectionService) Flush(ctx context.Context, id string) error {
	if err := saveInterrupted(ctx, "Saving the request"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saver.FlushWhere(func(k saveKey) bool { return k.id == id })
}

// FlushAll writes every pending edit now, in every project edited since
// the app started, and returns the errors joined.
func (s *CollectionService) FlushAll(ctx context.Context) error {
	if err := saveInterrupted(ctx, "Saving the requests"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saver.FlushAll()
}

// ServiceShutdown writes every pending edit when the app quits. Failures
// are logged; no events are sent, since the windows are closing.
func (s *CollectionService) ServiceShutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shuttingDown = true
	s.stopWatchLocked()
	if err := s.saver.FlushAll(); err != nil {
		s.logger.Error("pending edits could not be saved at quit", "err", err)
		return err
	}
	return nil
}

// flushDirLocked writes the pending edits made in any tree of the project
// folder dir, before the folder is loaded again; otherwise the new tree
// would read the files without them. Failures stay pending and are
// reported as events.
func (s *CollectionService) flushDirLocked(dir string) {
	_ = s.saver.FlushWhere(func(k saveKey) bool { return k.tree.Dir() == dir })
}

// carryPendingLocked moves the edits still pending in older trees of the
// same project folder (their write failed) to next, so Request keeps
// returning them and the next attempt goes through the new tree. They are
// tried again after the save delay.
func (s *CollectionService) carryPendingLocked(next *core.Tree) {
	for _, k := range s.saver.Keys() {
		if k.tree == next || k.tree.Dir() != next.Dir() {
			continue
		}
		p, ok := s.saver.Discard(k)
		if !ok {
			continue
		}
		if p.scratch {
			// Not part of the tree: the edit follows the project.
			s.saver.Put(saveKey{next, k.id}, p)
			continue
		}
		kind := core.KindRequest
		if p.folder {
			kind = core.KindFolder
		}
		if err := next.CheckWritable(k.id, kind); err != nil {
			s.logger.Warn("dropped an unsaved edit: the item cannot be saved any more", "id", k.id, "revision", p.revision, "err", err)
			continue
		}
		s.saver.Put(saveKey{next, k.id}, p)
	}
}

// Tree operations (ops.go) and pending edits. An operation reloads the
// tree in place (the *core.Tree stays the same), so pending keys stay
// valid; these helpers make the files and the pending edits agree first.
//
//   - Rename, move and duplicate flush the item's pending edits first
//     (flushPendingLocked): the rename's name write is then the last write,
//     and a copy gets the latest content.
//   - Delete drops them without writing (discardPendingLocked), so a late
//     write cannot recreate the file.
//   - Restore drops any pending edit of the restored items
//     (discardPendingLocked after the restore), so nothing stale is written
//     over them.
//
// Both match the edits made in any tree of the same project folder, and
// every request below a folder.

// subtreeIDs returns the ID of the node id and of every node below it; nil
// when id is not in tree.
func subtreeIDs(tree *core.Tree, id string) map[string]bool {
	n := tree.Node(id)
	if n == nil {
		return nil
	}
	ids := map[string]bool{}
	var walk func(*core.Node)
	walk = func(n *core.Node) {
		ids[n.ID] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(n)
	return ids
}

// pendingIn returns a key matcher for the pending edits of ids made in any
// tree of tree's project folder.
func pendingIn(tree *core.Tree, ids map[string]bool) func(saveKey) bool {
	return func(k saveKey) bool { return ids[k.id] && k.tree.Dir() == tree.Dir() }
}

// flushPendingLocked writes the pending edits of the item id and of every
// request below it now. It returns the writes' errors (also sent as
// collection:save-failed); the failed edits stay pending.
func (s *CollectionService) flushPendingLocked(tree *core.Tree, id string) error {
	ids := subtreeIDs(tree, id)
	if len(ids) == 0 {
		return nil
	}
	return s.saver.FlushWhere(pendingIn(tree, ids))
}

// discardPendingLocked drops the pending edits of the item id and of every
// request below it without writing them.
func (s *CollectionService) discardPendingLocked(tree *core.Tree, id string) {
	s.discardPendingIDsLocked(tree, subtreeIDs(tree, id))
}

// discardPendingIDsLocked drops the pending edits of ids (from subtreeIDs,
// taken before a delete removed the nodes) without writing them.
func (s *CollectionService) discardPendingIDsLocked(tree *core.Tree, ids map[string]bool) {
	if len(ids) == 0 {
		return
	}
	match := pendingIn(tree, ids)
	for _, k := range s.saver.Keys() {
		if match(k) {
			if p, ok := s.saver.Discard(k); ok {
				s.logger.Info("dropped an unsaved edit of a deleted or restored request", "id", k.id, "revision", p.revision)
			}
		}
	}
}

func saveInterrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}
