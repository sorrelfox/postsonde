package collection

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// DTOs are the only types CollectionService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/collection.md.

// TreeDTO is the request tree of the open project, flattened: nodes by
// ID with child ID lists, the shape the UI's collection store keeps.
type TreeDTO struct {
	// WorkspaceID is the ID of the project the tree belongs to.
	WorkspaceID string `json:"workspaceId"`
	// Path is the absolute path of the project folder.
	Path string `json:"path"`
	// ReadOnly is why no project file may be written ("newer_version",
	// "invalid"), or "".
	ReadOnly string `json:"readOnly"`
	// RootIDs are the top-level items in display order.
	RootIDs []string `json:"rootIds"`
	// Nodes are every folder and item below the root, depth first in
	// display order.
	Nodes []NodeDTO `json:"nodes"`
	// Problems are what was wrong while loading, sorted by path.
	Problems []ProblemDTO `json:"problems"`
	// Scratch are the project's scratch requests (COL-06), kept in its
	// local side files: request nodes without a parent or path, sorted by
	// name. They are not among RootIDs.
	Scratch []NodeDTO `json:"scratch"`
}

// NodeDTO is a folder or an item of the tree.
type NodeDTO struct {
	// ID is stable across renames and moves; see docs/collection.md for
	// copied files and files without an id.
	ID string `json:"id"`
	// ParentID is null for a top-level item.
	ParentID *string `json:"parentId"`
	// Kind is "folder", "request", "unknown" (a kind this version does not
	// know, kept as it is) or "invalid" (a file that did not load).
	Kind string `json:"kind"`
	// Name is the display name: the file's name, or the file name without
	// .json when that is blank.
	Name string `json:"name"`
	// Path is the slash path of the file or directory relative to the
	// project folder.
	Path string `json:"path"`
	// FileKind is the file's kind member ("http", "graphql", "grpc", …), or
	// "" when it could not be read.
	FileKind string `json:"fileKind"`
	// Method is the HTTP method of a request: the method of an http
	// request, "POST" or "GET" for a graphql one; "" otherwise.
	Method string `json:"method"`
	// ChildIDs are a folder's items in display order; [] for other kinds.
	ChildIDs []string `json:"childIds"`
	// ReadOnly is why the file must not be written ("newer_version",
	// "unknown_kind", "invalid", "symlink"), or "".
	ReadOnly string `json:"readOnly"`
}

// ProblemDTO is something wrong found while loading the tree.
type ProblemDTO struct {
	// Path is the slash path of the file or directory relative to the
	// project folder.
	Path string `json:"path"`
	// NodeID is the node it concerns, or "".
	NodeID string `json:"nodeId"`
	// Error says what is wrong; its details include the absolute path.
	Error apperr.ErrorDTO `json:"error"`
}

func toTreeDTO(workspaceID string, t *core.Tree) TreeDTO {
	root := t.Root()
	dto := TreeDTO{
		WorkspaceID: workspaceID,
		Path:        t.Dir(),
		ReadOnly:    string(t.ReadOnly()),
		RootIDs:     childIDs(root),
		Nodes:       make([]NodeDTO, 0, t.Len()),
		Problems:    make([]ProblemDTO, 0),
		Scratch:     make([]NodeDTO, 0),
	}
	t.Walk(func(n *core.Node) bool {
		if n == root {
			return true
		}
		dto.Nodes = append(dto.Nodes, toNodeDTO(n, root))
		return true
	})
	for _, p := range t.Problems() {
		dto.Problems = append(dto.Problems, ProblemDTO{Path: p.Path, NodeID: p.NodeID, Error: apperr.ToDTO(p.Err)})
	}
	return dto
}

func toNodeDTO(n, root *core.Node) NodeDTO {
	dto := NodeDTO{
		ID:       n.ID,
		Kind:     string(n.Kind),
		Name:     n.DisplayName(),
		Path:     n.Path,
		FileKind: string(n.FileKind),
		ChildIDs: childIDs(n),
		ReadOnly: string(n.ReadOnly),
	}
	if n.Parent != nil && n.Parent != root {
		parent := n.Parent.ID
		dto.ParentID = &parent
	}
	switch f := n.File.(type) {
	case *projectfile.HTTPRequest:
		dto.Method = f.Method
	case *projectfile.SSERequest:
		dto.Method = f.HTTP().Method
	case *projectfile.GraphQLRequest:
		dto.Method = "POST"
		if f.Method != "" {
			dto.Method = f.Method
		}
	}
	return dto
}

func childIDs(n *core.Node) []string {
	ids := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		ids = append(ids, c.ID)
	}
	return ids
}

// Tree operations.

// CreateFolderInputDTO is the input of CreateFolder.
type CreateFolderInputDTO struct {
	// ParentID is the folder to create it in; "" for the top level.
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
	// Index is its position among the folder's items; -1 (or any index
	// out of range) puts it last.
	Index int `json:"index"`
}

// CreateRequestInputDTO is the input of CreateRequest.
type CreateRequestInputDTO struct {
	// ParentID is the folder to create it in; "" for the top level.
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
	// Kind is "http", "graphql", "tcp", "udp", "websocket", "grpc", "sse" or "socketio".
	Kind string `json:"kind"`
	// Index is its position among the folder's items; -1 puts it last.
	Index int `json:"index"`
}

// RenameInputDTO is the input of Rename.
type RenameInputDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MoveInputDTO is the input of Move.
type MoveInputDTO struct {
	ID string `json:"id"`
	// ParentID is the folder to move it into; "" for the top level.
	ParentID string `json:"parentId"`
	// Index is its position among the target folder's items after the
	// move, not counting itself; -1 puts it last.
	Index int `json:"index"`
}

// NodeResultDTO is the result of an operation on one item.
type NodeResultDTO struct {
	// Tree is the whole tree after the operation.
	Tree TreeDTO `json:"tree"`
	// NodeID is the item created, renamed, copied (the copy) or moved.
	NodeID string `json:"nodeId"`
	// Version is a renamed request's new version (windows.go), for its
	// draft's next edit; 0 otherwise, and then left out.
	Version int `json:"version,omitempty"`
}

// DeleteResultDTO is the result of Delete.
type DeleteResultDTO struct {
	Tree TreeDTO `json:"tree"`
	// Entry is the trash entry to pass to Restore to undo the delete.
	Entry TrashEntryDTO `json:"entry"`
}

// RestoreResultDTO is the result of Restore.
type RestoreResultDTO struct {
	Tree TreeDTO `json:"tree"`
	// NodeID is the restored item: its old ID unless another item has that
	// ID now; "" if the restored file is no longer an item.
	NodeID string `json:"nodeId"`
	// ToRoot is true when the item's folder no longer exists, so it was
	// restored to the top level.
	ToRoot bool `json:"toRoot"`
}

// TrashEntryDTO is a deleted item in the project's local trash.
type TrashEntryDTO struct {
	// ID is what Restore takes.
	ID     string `json:"id"`
	NodeID string `json:"nodeId"`
	// Kind is "folder", "request", "unknown" or "invalid".
	Kind     string `json:"kind"`
	FileKind string `json:"fileKind"`
	Name     string `json:"name"`
	// Path is the slash path it had in the project.
	Path string `json:"path"`
	// ParentID is the folder it was in, null for the top level; Index its
	// position there.
	ParentID *string `json:"parentId"`
	Index    int     `json:"index"`
	// DeletedAt is RFC 3339 in UTC.
	DeletedAt string `json:"deletedAt"`
}

// RecreateRequestInputDTO is the argument of RecreateRequest: the
// content of a request whose file was removed outside PostSonde, and where
// it was.
type RecreateRequestInputDTO struct {
	// ParentID is the folder it was in, "" for the top level.
	ParentID string     `json:"parentId"`
	Index    int        `json:"index"`
	Request  RequestDTO `json:"request"`
}

// ChangedEventDTO is the payload of collection:changed.
type ChangedEventDTO struct {
	WorkspaceID string `json:"workspaceId"`
	// Op is "create", "rename", "duplicate", "move", "delete", "restore"
	// "purge_trash", "update_folder" (UpdateFolderGRPCProto),
	// "create_scratch", "delete_scratch" or "save_scratch" (scratch.go).
	Op string `json:"op"`
	// NodeID is the item the operation concerned (the copy for
	// "duplicate"), or "".
	NodeID string `json:"nodeId"`
	// TrashID is the trash entry a delete created or a restore used, or "".
	TrashID string `json:"trashId"`
}

func toTrashEntryDTO(e core.TrashEntry) TrashEntryDTO {
	dto := TrashEntryDTO{
		ID:       e.ID,
		NodeID:   e.NodeID,
		Kind:     string(e.Kind),
		FileKind: string(e.FileKind),
		Name:     e.Name,
		Path:     e.Path,
		Index:    e.Index,
	}
	if e.ParentID != "" {
		parent := e.ParentID
		dto.ParentID = &parent
	}
	if !e.DeletedAt.IsZero() {
		dto.DeletedAt = e.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return dto
}
