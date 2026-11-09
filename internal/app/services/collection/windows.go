package collection

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Several windows (APP-06, docs/windows.md#edits). Every window shows the
// open project and keeps its own drafts of the requests and folders it has
// open; this service holds the one pending version of each item, so a
// write never races another write. What must not happen is a window
// sending a draft made from content another window has changed since:
// Go would take it as the newer version and the other window's edit would
// be lost without anyone seeing it.
//
// So every item has a version: Request and Folder return it, and
// UpdateRequest and UpdateFolder take the version the draft was made from
// (BaseVersion) and return the new one. An accepted edit, a change of the
// item's file on disk (refreshLocked) and a rename of a request move the
// version on. An edit made from an older version is refused with
// collection.edited_elsewhere, which the UI shows like a change on disk:
// the draft keeps the edit, and the user keeps it or takes the other
// version. After every accepted edit collection:edited tells the other
// windows, which reload their drafts of the item that have no edits of
// their own.
//
// Versions count from 0 (never changed since the project was opened) and
// are kept per project folder: opening another project starts again.

// CodeEditedElsewhere: an edit was made from an older version of the
// request or folder than the one the service holds: another window changed
// it since (kind io). Details: id, version (the current one).
const CodeEditedElsewhere = "collection.edited_elsewhere"

// editState is the service's part of the edits of several windows.
// Guarded by s.mu.
type editState struct {
	// root is the project folder versions are of.
	root string
	// versions are the items' versions, by ID; missing is 0.
	versions map[string]int
	// hidden are the windows whose page is hidden (SetWatching).
	hidden map[uint]bool
}

// WithWindowCount tells the service how many windows are open, so the
// polling for outside changes pauses only while every window is hidden
// (SetWatching). Without it there is one window.
func WithWindowCount(count func() int) Option {
	return func(o *options) { o.windowCount = count }
}

// versionsLocked returns the versions of ws's items, starting again when
// another project was opened. s.mu must be held.
func (s *CollectionService) versionsLocked(ws *coreworkspace.Workspace) map[string]int {
	if s.edits.versions == nil || s.edits.root != ws.Root {
		s.edits.root = ws.Root
		s.edits.versions = map[string]int{}
	}
	return s.edits.versions
}

// versionLocked returns the version of item id of ws. s.mu must be held.
func (s *CollectionService) versionLocked(ws *coreworkspace.Workspace, id string) int {
	return s.versionsLocked(ws)[id]
}

// bumpLocked moves the version of item id of ws on and returns the new
// one. s.mu must be held.
func (s *CollectionService) bumpLocked(ws *coreworkspace.Workspace, id string) int {
	v := s.versionsLocked(ws)
	v[id]++
	return v[id]
}

// checkBaseLocked refuses an edit of item id made from version base when
// the item has moved on. A nil base (a caller that does not track
// versions) is not checked. s.mu must be held.
func (s *CollectionService) checkBaseLocked(ws *coreworkspace.Workspace, id string, base *int) error {
	if base == nil {
		return nil
	}
	current := s.versionLocked(ws, id)
	if *base == current {
		return nil
	}
	return errs.New(errs.IO, "This item was changed in another PostSonde window after this window loaded it.").
		WithCode(CodeEditedElsewhere).
		WithHint("Keep your version or take the other window's first.").
		WithDetail("id", id).
		WithDetail("version", strconv.Itoa(current))
}

// editedLocked records an accepted edit of item id of ws by window win:
// the version moves on, and collection:edited tells the other windows.
// It returns the new version. s.mu must be held.
func (s *CollectionService) editedLocked(ws *coreworkspace.Workspace, id string, win uint) int {
	v := s.bumpLocked(ws, id)
	s.emit(EventEdited, EditedEventDTO{WorkspaceID: ws.ID(), IDs: []string{id}, Window: win})
	return v
}

// allHiddenLocked reports whether every window is hidden, so polling for
// outside changes pauses. s.mu must be held.
func (s *CollectionService) allHiddenLocked() bool {
	if len(s.edits.hidden) == 0 {
		return false
	}
	count := 1
	if s.windowCount != nil {
		count = s.windowCount()
	}
	return len(s.edits.hidden) >= count
}

// syncWatchLocked pauses or resumes polling after a window was shown,
// hidden or closed. s.mu must be held.
func (s *CollectionService) syncWatchLocked() {
	if s.watch.watcher == nil {
		return
	}
	if s.allHiddenLocked() {
		s.watch.watcher.Pause()
	} else {
		s.watch.watcher.Resume()
	}
}

// WindowClosed forgets window id: its page is gone, so it counts neither
// as hidden nor as shown. Not a bound method.
func WindowClosed(s *CollectionService, id uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.edits.hidden, id)
	s.syncWatchLocked()
}
