package workspace

import (
	"encoding/json"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// DTOs are the only types WorkspaceService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/workspace.md.

// WorkspaceDTO is an open project.
type WorkspaceDTO struct {
	// ID is the workspace ID from postsonde.json.
	ID string `json:"id"`
	// Name is the display name (the folder name when the file has none).
	Name string `json:"name"`
	// Path is the absolute path of the project folder.
	Path string `json:"path"`
	// LocalDir is the local side-file directory in the app data directory.
	LocalDir string `json:"localDir"`
	// LocalStatus is how LocalDir was found: "new", "existing", "moved" (the
	// project was moved from PreviousPath) or "copy" (PreviousPath holds the
	// same workspace; this folder has local data of its own).
	LocalStatus string `json:"localStatus"`
	// PreviousPath is set for "moved" and "copy", else "".
	PreviousPath string `json:"previousPath"`
	// NewerFormat is true when postsonde.json is from a newer PostSonde;
	// project files must then not be written.
	NewerFormat bool `json:"newerFormat"`
	// OpenedAt is when it was opened, RFC 3339 in UTC.
	OpenedAt string `json:"openedAt"`
	// UIState is the view state the UI saved for this workspace with
	// SaveUIState (open tabs, expanded folders), by key, each value a JSON
	// text. Read from the local directory when the DTO is made (STO-06).
	UIState map[string]string `json:"uiState"`
}

// SaveUIStateInputDTO is the input of SaveUIState.
type SaveUIStateInputDTO struct {
	// WorkspaceID and Path name the workspace: the open one or one opened
	// earlier since the app started.
	WorkspaceID string `json:"workspaceId"`
	Path        string `json:"path"`
	// Key names the value: lower-case letters, digits, "." and "-".
	Key string `json:"key"`
	// Value is a JSON text; "null" removes the key.
	Value string `json:"value"`
}

// CurrentDTO is the open workspace, if any. Current returns it and the
// workspace:changed event carries it.
type CurrentDTO struct {
	// Workspace is null when no workspace is open.
	Workspace *WorkspaceDTO `json:"workspace"`
}

// CreateInputDTO is the input of Create.
type CreateInputDTO struct {
	// Path is the absolute path of the folder; it is created if missing.
	Path string `json:"path"`
	// Name is the display name; blank uses the folder name.
	Name string `json:"name"`
}

// RecentWorkspaceDTO is an entry of the recent-workspaces list.
type RecentWorkspaceDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is the project folder.
	Path string `json:"path"`
	// OpenedAt is when it was last opened, RFC 3339 in UTC.
	OpenedAt string `json:"openedAt"`
	// Available is false when the folder or its postsonde.json is gone.
	Available bool `json:"available"`
}

// Values of PickFolder's purpose.
const (
	PurposeOpen   = "open"
	PurposeCreate = "create"
)

func toWorkspaceDTO(w *core.Workspace) WorkspaceDTO {
	return WorkspaceDTO{
		ID:           w.ID(),
		Name:         w.Name(),
		Path:         w.Root,
		LocalDir:     w.LocalDir,
		LocalStatus:  string(w.LocalStatus),
		PreviousPath: w.PreviousPath,
		NewerFormat:  w.Newer(),
		OpenedAt:     formatTime(w.OpenedAt),
		UIState:      toUIState(core.ReadUIState(w.LocalDir)),
	}
}

func toUIState(values map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = string(v)
	}
	return out
}

func toCurrentDTO(w *core.Workspace) CurrentDTO {
	if w == nil {
		return CurrentDTO{}
	}
	dto := toWorkspaceDTO(w)
	return CurrentDTO{Workspace: &dto}
}

func toRecentDTOs(entries []core.Recent) []RecentWorkspaceDTO {
	out := make([]RecentWorkspaceDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, RecentWorkspaceDTO{
			ID:        e.ID,
			Name:      e.Name,
			Path:      e.Path,
			OpenedAt:  formatTime(e.OpenedAt),
			Available: e.Available,
		})
	}
	return out
}

// formatTime renders t for a DTO: RFC 3339 in UTC; the zero time is "".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// BodyFileDTO is a file sent as a request body or a multipart part
// (HTTP-14, HTTP-15), as PickBodyFile and BodyFile describe it.
type BodyFileDTO struct {
	// Path is the path to store in the request: relative to the project
	// root with "/" separators when the file is inside the open project,
	// else absolute. "" when the dialog was cancelled.
	Path string `json:"path"`
	// AbsolutePath is where the file is on disk; "" when a relative path
	// cannot be resolved (no project is open).
	AbsolutePath string `json:"absolutePath"`
	// Name is the file's base name.
	Name string `json:"name"`
	// Size is the file's size in bytes, 0 when it does not exist.
	Size int64 `json:"size"`
	// Exists is false when there is no regular file at the path.
	Exists bool `json:"exists"`
}
