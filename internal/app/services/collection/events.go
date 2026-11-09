package collection

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
)

// The service's events. They are constants so RegisterEvent below is
// visible to the bindings generator.
const (
	// EventChanged is sent after every tree operation that succeeded
	// (create, rename, duplicate, move, delete, restore) and after the
	// trash is purged.
	EventChanged = Domain + ":changed"
	// EventSaved is sent after a pending auto-save edit was written (or
	// found equal to the file on disk). See docs/collection.md#auto-save.
	EventSaved = Domain + ":saved"
	// EventSaveFailed is sent when writing a pending edit failed. The edit
	// stays pending: the next edit, Flush or FlushAll tries again.
	EventSaveFailed = Domain + ":save-failed"
	// EventExternalChange is sent when the open project's files changed
	// outside PostSonde (STO-05, watch.go), after the tree was read again.
	EventExternalChange = Domain + ":external-change"
	// EventFilesDropped is sent when files from the OS were dropped on an
	// element of the window marked as a file drop target (IMP-11; sent by
	// internal/app, filedrop.go). The UI decides what the drop means.
	EventFilesDropped = Domain + ":files-dropped"
	// EventEdited is sent after UpdateRequest or UpdateFolder took an edit
	// (APP-06, windows.go): the other windows reload their drafts of the
	// items that have no edits of their own. Sent to every window; the
	// one that made the edit ignores it.
	EventEdited = Domain + ":edited"
)

// FilesDroppedDTO is the payload of collection:files-dropped. It is sent
// to the window the files were dropped on only.
type FilesDroppedDTO struct {
	// Paths are the dropped files' absolute paths, in the OS's order.
	Paths []string `json:"paths"`
	// Target is the value of the data-file-drop-target attribute of the
	// element the files were dropped on ("import" for the window's own
	// target), so a drop target inside the window can claim its drops.
	Target string `json:"target"`
}

// ExternalChangeDTO is the payload of collection:external-change. The
// lists hold node IDs; the UI calls Tree for the tree itself.
type ExternalChangeDTO struct {
	WorkspaceID string `json:"workspaceId"`
	// Added are items that are new in the tree.
	Added []string `json:"added"`
	// Removed are items whose file or directory is gone.
	Removed []string `json:"removed"`
	// Modified are requests and folders whose content changed: a request's
	// file, or a folder's auth, headers or variables. Open clean drafts of
	// them are reloaded.
	Modified []string `json:"modified"`
	// Moved are items renamed or moved on disk with the same content, and
	// folders whose other members (order, name, server) changed.
	Moved []string `json:"moved"`
	// Conflicts are the Modified items whose unsaved edits were set aside
	// instead of written: edits of them are refused
	// (collection.changed_on_disk) until ResolveConflict.
	Conflicts []string `json:"conflicts"`
	// Globals is set when the global variables in postsonde.json changed.
	Globals bool `json:"globals"`
	// Environments is set when an environment file changed, was added or
	// was removed.
	Environments bool `json:"environments"`
}

// SavedEventDTO is the payload of collection:saved.
type SavedEventDTO struct {
	// WorkspaceID is the project the request belongs to.
	WorkspaceID string `json:"workspaceId"`
	ID          string `json:"id"`
	// Revision is the revision UpdateRequest was given with the content
	// that was written.
	Revision int `json:"revision"`
	// Window is the window that edit came from (0: none); revisions are
	// that window's, so the others ignore the event (APP-06).
	Window uint `json:"window"`
	// SavedAt is when the write finished, RFC 3339 in UTC.
	SavedAt string `json:"savedAt"`
}

// SaveFailedEventDTO is the payload of collection:save-failed.
type SaveFailedEventDTO struct {
	WorkspaceID string `json:"workspaceId"`
	ID          string `json:"id"`
	Revision    int    `json:"revision"`
	// Window is the window the edit came from, as in SavedEventDTO.
	Window uint            `json:"window"`
	Error  apperr.ErrorDTO `json:"error"`
}

// EditedEventDTO is the payload of collection:edited.
type EditedEventDTO struct {
	WorkspaceID string `json:"workspaceId"`
	// IDs are the requests and folders edited.
	IDs []string `json:"ids"`
	// Window is the window the edit came from; it ignores the event.
	Window uint `json:"window"`
}

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedEventDTO](EventChanged)
	application.RegisterEvent[SavedEventDTO](EventSaved)
	application.RegisterEvent[SaveFailedEventDTO](EventSaveFailed)
	application.RegisterEvent[ExternalChangeDTO](EventExternalChange)
	application.RegisterEvent[FilesDroppedDTO](EventFilesDropped)
	application.RegisterEvent[EditedEventDTO](EventEdited)
}
