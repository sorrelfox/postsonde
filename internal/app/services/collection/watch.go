package collection

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fswatch"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// External changes (STO-05): the open project's folder is polled
// (core.Scan with an fswatch.Watcher, WithWatch); when files changed, the
// tree is refreshed in place (Tree.Refresh, which compares file hashes, so
// the app's own writes do not count) and collection:external-change tells
// the UI what changed. A request or folder with an unsaved edit whose file
// changed on disk is a conflict: the edit is set aside, not written, until
// the UI resolves it. See docs/collection.md#external-changes.

// DefaultWatchInterval is the polling interval services.go uses.
const DefaultWatchInterval = time.Second

// WithWatch makes the service poll the open project's files every interval
// for changes made outside PostSonde (zero means DefaultWatchInterval).
// Without it nothing is watched, as in most tests; Reload still reads
// external changes on demand.
func WithWatch(interval time.Duration) Option {
	return func(o *options) {
		if interval <= 0 {
			interval = DefaultWatchInterval
		}
		o.watchInterval = interval
	}
}

// watchState is the service's part of external change detection. Guarded
// by s.mu.
type watchState struct {
	interval time.Duration
	clock    fswatch.Clock
	watcher  *fswatch.Watcher
	// ws is the workspace the watcher polls.
	ws *coreworkspace.Workspace
	// conflicts are the IDs of requests and folders of ws whose unsaved
	// edit was set aside because their file changed on disk; edits of them
	// are refused until ResolveConflict.
	conflicts map[string]bool
	// envSums are the hashes of the environment files as last seen, so
	// only their changes are reported.
	envSums map[string][sha256.Size]byte
}

// startWatchLocked makes the watcher follow ws, just loaded into a new
// tree: a new watcher for another workspace, otherwise a new reference
// snapshot, since the tree has just read everything. s.mu must be held.
func (s *CollectionService) startWatchLocked(ws *coreworkspace.Workspace) {
	w := &s.watch
	if w.ws != ws {
		w.conflicts = nil
	}
	w.envSums = environmentSums(ws.Root)
	if w.interval <= 0 {
		w.ws = ws
		return
	}
	if w.watcher != nil && w.ws == ws {
		w.watcher.Rebase()
		return
	}
	s.stopWatchLocked()
	w.ws = ws
	root := ws.Root
	w.watcher = fswatch.New(fswatch.Options{
		Interval: w.interval,
		Clock:    w.clock,
		Scan:     func() (fswatch.Snapshot, error) { return core.Scan(root) },
		OnChange: func(fswatch.Changes) { s.checkExternal(ws) },
	})
	w.watcher.Start()
	if s.allHiddenLocked() {
		w.watcher.Pause()
	}
}

// stopWatchLocked stops polling. s.mu must be held.
func (s *CollectionService) stopWatchLocked() {
	if s.watch.watcher != nil {
		s.watch.watcher.Stop()
		s.watch.watcher = nil
	}
}

// pokeWatchLocked asks the watcher to poll now; false when nothing is
// watched. s.mu must be held.
func (s *CollectionService) pokeWatchLocked() bool {
	if s.watch.watcher == nil {
		return false
	}
	s.watch.watcher.Poke()
	return true
}

// checkExternal is the watcher's OnChange: files of ws changed on disk.
func (s *CollectionService) checkExternal(ws *coreworkspace.Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		return
	}
	if cur := s.manager.Current(); cur != ws || s.ws != ws || s.tree == nil {
		// Another project is open (or none): the next load watches it.
		if s.watch.ws == ws {
			s.stopWatchLocked()
		}
		return
	}
	s.refreshLocked(ws, s.tree)
}

// SetWatching tells the service that the calling window was hidden
// (active false) or shown. Polling pauses while every window is hidden
// (WithWindowCount) and resumes, with a poll right away, when one is shown
// again.
func (s *CollectionService) SetWatching(ctx context.Context, active bool) {
	win := windowctx.ID(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if active {
		delete(s.edits.hidden, win)
	} else {
		if s.edits.hidden == nil {
			s.edits.hidden = map[uint]bool{}
		}
		s.edits.hidden[win] = true
	}
	s.syncWatchLocked()
}

// ResolveConflict ends the conflict of request or folder id: its file
// changed on disk while it had unsaved edits (collection:external-change,
// conflicts). Edits of it are accepted again: the UI sends its version to
// keep it, or reloads the file's to take that. Resolving an ID without a
// conflict does nothing.
func (s *CollectionService) ResolveConflict(_ context.Context, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.watch.conflicts, id)
}

// conflictLocked returns the error for an edit of id while its conflict is
// open, or nil.
func (s *CollectionService) conflictLocked(id string) error {
	if !s.watch.conflicts[id] {
		return nil
	}
	return errs.New(errs.IO, "The file was changed outside PostSonde while it had unsaved edits.").
		WithCode(core.CodeChangedOnDisk).
		WithHint("Keep your version or take the one on disk first.").
		WithDetail("id", id)
}

// refreshLocked reads the tree of ws again in place, sets aside the
// unsaved edits of items whose file changed, and emits
// collection:external-change when anything changed (and
// environments:changed when variables did). s.mu must be held.
func (s *CollectionService) refreshLocked(ws *coreworkspace.Workspace, tree *core.Tree) ExternalChangeDTO {
	ch := tree.Refresh()
	envChanged := s.environmentsChangedLocked(ws.Root)
	ev := ExternalChangeDTO{
		WorkspaceID: ws.ID(),
		Added:       []string{},
		Removed:     []string{},
		Modified:    []string{},
		Moved:       []string{},
		Conflicts:   []string{},
	}
	if ch.Empty() && !envChanged {
		return ev
	}
	for _, c := range ch.Added {
		ev.Added = append(ev.Added, c.ID)
	}
	for _, c := range ch.Moved {
		ev.Moved = append(ev.Moved, c.ID)
	}
	for _, c := range ch.Modified {
		n := tree.Node(c.ID)
		key := saveKey{tree, c.ID}
		p, pending := s.saver.Get(key)
		if c.Kind == core.KindFolder && n != nil && sameFolderSettings(c.Old, n.File) {
			// Only its order, name, proto setup or server changed: the
			// settings a draft edits are as they were, so a pending edit
			// is written over the new file as usual.
			ev.Moved = append(ev.Moved, c.ID)
			continue
		}
		ev.Modified = append(ev.Modified, c.ID)
		// Drafts made from the old content are older than the file now
		// (windows.go).
		s.bumpLocked(ws, c.ID)
		if pending {
			s.saver.Discard(key)
			if s.watch.conflicts == nil {
				s.watch.conflicts = map[string]bool{}
			}
			s.watch.conflicts[c.ID] = true
			ev.Conflicts = append(ev.Conflicts, c.ID)
			s.logger.Info("an unsaved edit was set aside: the file changed on disk", "id", c.ID, "revision", p.revision)
		}
	}
	for _, c := range ch.Removed {
		ev.Removed = append(ev.Removed, c.ID)
		if p, ok := s.saver.Discard(saveKey{tree, c.ID}); ok {
			s.logger.Info("dropped an unsaved edit: the file was removed on disk", "id", c.ID, "revision", p.revision)
		}
	}
	if ch.Root {
		var before, after []projectfile.Variable
		if w, ok := ch.OldRoot.(*projectfile.Workspace); ok {
			before = w.Variables
		}
		if w, ok := tree.Root().File.(*projectfile.Workspace); ok {
			after = w.Variables
		}
		ev.Globals = !reflect.DeepEqual(before, after)
	}
	ev.Environments = envChanged
	s.logger.Info("project files changed on disk", "workspace", ws.ID(),
		"added", len(ev.Added), "removed", len(ev.Removed), "modified", len(ev.Modified),
		"moved", len(ev.Moved), "conflicts", len(ev.Conflicts), "globals", ev.Globals, "environments", ev.Environments)
	s.emit(EventExternalChange, ev)
	if ev.Globals || ev.Environments {
		// What {{variables}} resolve to may have changed (ENV-11).
		s.emit(envservice.EventChanged, envservice.ChangedDTO{ActiveID: activeEnvironment(ws)})
	}
	return ev
}

// sameFolderSettings reports whether two folder files have the same
// settings a folder draft edits: auth, headers and variables.
func sameFolderSettings(a, b projectfile.File) bool {
	fa, okA := a.(*projectfile.Folder)
	fb, okB := b.(*projectfile.Folder)
	if !okA || !okB || fa == nil || fb == nil {
		return false
	}
	return reflect.DeepEqual(fa.Auth, fb.Auth) && reflect.DeepEqual(fa.Headers, fb.Headers) && reflect.DeepEqual(fa.Variables, fb.Variables)
}

// environmentsChangedLocked reports whether the environment files differ
// from when they were last seen, and remembers them as they are now.
func (s *CollectionService) environmentsChangedLocked(root string) bool {
	sums := environmentSums(root)
	changed := !reflect.DeepEqual(sums, s.watch.envSums)
	s.watch.envSums = sums
	return changed
}

// EnvironmentsWritten tells the service that the environments service
// wrote the open project's environment files, so the watcher does not
// report the app's own writes as outside changes (services.go wires it).
func EnvironmentsWritten(s *CollectionService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watch.ws != nil {
		s.watch.envSums = environmentSums(s.watch.ws.Root)
	}
}

// environmentSums hashes environments/*.json of the project root.
func environmentSums(root string) map[string][sha256.Size]byte {
	dir := filepath.Join(root, projectfile.EnvironmentsDir)
	entries, err := os.ReadDir(dir)
	out := map[string][sha256.Size]byte{}
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !strings.HasSuffix(name, projectfile.Ext) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out[name] = sha256.Sum256(data)
	}
	return out
}

// activeEnvironment returns the ID of ws's active environment, or "".
func activeEnvironment(ws *coreworkspace.Workspace) string {
	env, ok, err := environmentsOf(ws).Active()
	if err != nil || !ok {
		return ""
	}
	return env.ID
}

// isChangedOnDisk reports whether err is core.CodeChangedOnDisk.
func isChangedOnDisk(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == core.CodeChangedOnDisk
}

// RecreateRequest writes a request whose file was removed outside
// PostSonde back into the project (the "Restore file" of its tab): in.Request
// as a new file in folder in.ParentID (the top level when that folder is
// gone too) at in.Index, keeping its ID unless an item of the project has it
// by now. Typed secrets go to the local side files as on every write.
// Returns the tree with the request's ID; op "create".
func (s *CollectionService) RecreateRequest(ctx context.Context, in RecreateRequestInputDTO) (NodeResultDTO, error) {
	f, err := in.Request.toFile()
	if err != nil {
		return NodeResultDTO{}, err
	}
	return s.nodeOp(ctx, "create", "Restoring the request's file", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		parentID := in.ParentID
		if parentID != "" {
			if n := t.Node(parentID); n == nil || n.Kind != core.KindFolder {
				parentID = ""
			}
		}
		id, err := t.AddRequest(parentID, f, in.Request.Name, in.Index)
		if err == nil {
			delete(s.watch.conflicts, in.Request.ID)
		}
		return id, "", err
	})
}
