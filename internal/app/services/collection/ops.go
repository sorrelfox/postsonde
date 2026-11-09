package collection

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Tree operations (COL-04). Each runs under the service lock on the
// cached tree, returns the tree after the operation and emits
// collection:changed. When one fails the cached tree is dropped, so the
// next call reads the files again. Pending auto-save edits are flushed
// before rename, move and duplicate and dropped by delete and restore
// (autosave.go). See docs/collection.md.

// TrashDirName is the directory of the project's trash in its local side
// files (docs/workspace.md).
const TrashDirName = "trash"

// CreateFolder creates a folder and returns the tree with its ID.
func (s *CollectionService) CreateFolder(ctx context.Context, in CreateFolderInputDTO) (NodeResultDTO, error) {
	return s.nodeOp(ctx, "create", "Creating the folder", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		id, err := t.CreateFolder(in.ParentID, in.Name, in.Index)
		return id, "", err
	})
}

// CreateRequest creates an empty http, graphql, tcp, websocket or grpc
// request and returns the tree with its ID.
func (s *CollectionService) CreateRequest(ctx context.Context, in CreateRequestInputDTO) (NodeResultDTO, error) {
	return s.nodeOp(ctx, "create", "Creating the request", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		id, err := t.CreateRequest(in.ParentID, in.Name, projectfile.Kind(in.Kind), in.Index)
		return id, "", err
	})
}

// Rename renames a folder or request and its file. Pending edits of the
// item are written first, so the new name is the last write; when that
// fails, nothing is renamed.
//
// A request's version moves on (windows.go): a draft made before the
// rename would write the old name back. The result carries the new
// version.
func (s *CollectionService) Rename(ctx context.Context, in RenameInputDTO) (NodeResultDTO, error) {
	version := 0
	res, err := s.nodeOp(ctx, "rename", "Renaming", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		if err := s.flushPendingLocked(t, in.ID); err != nil {
			return "", "", err
		}
		if err := t.Rename(in.ID, in.Name); err != nil {
			return "", "", err
		}
		// nodeOp holds s.mu and has just set s.ws to the open workspace.
		if n := t.Node(in.ID); n != nil && n.Kind == core.KindRequest {
			version = s.bumpLocked(s.ws, in.ID)
		}
		return in.ID, "", nil
	})
	res.Version = version
	return res, err
}

// Duplicate copies a folder or request next to it and returns the tree
// with the copy's ID. Pending edits of the item are written first, so the
// copy has them; when that fails, nothing is copied. The locally stored
// auth secrets of the copied requests are copied to the new IDs.
func (s *CollectionService) Duplicate(ctx context.Context, id string) (NodeResultDTO, error) {
	return s.nodeOp(ctx, "duplicate", "Duplicating", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		if err := s.flushPendingLocked(t, id); err != nil {
			return "", "", err
		}
		var before map[string]string
		if n := t.Node(id); n != nil {
			before = requestPaths(n)
		}
		copyID, err := t.Duplicate(id)
		if err == nil {
			// nodeOp holds s.mu and has just set s.ws to the open workspace.
			s.copySecretsLocked(s.ws.LocalDir, t, before, copyID)
		}
		return copyID, "", err
	})
}

// Move moves an item into a folder at a position, or reorders it. Pending
// edits of the item are written first; when that fails, nothing moves.
func (s *CollectionService) Move(ctx context.Context, in MoveInputDTO) (NodeResultDTO, error) {
	return s.nodeOp(ctx, "move", "Moving", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		if err := s.flushPendingLocked(t, in.ID); err != nil {
			return "", "", err
		}
		return in.ID, "", t.Move(in.ID, in.ParentID, in.Index)
	})
}

// Delete moves an item into the project's local trash. The returned entry
// undoes it with Restore. Pending edits of the item and everything in it
// are dropped, not written, once it is in the trash.
func (s *CollectionService) Delete(ctx context.Context, id string) (DeleteResultDTO, error) {
	var entry core.TrashEntry
	res, err := s.nodeOp(ctx, "delete", "Deleting", func(tr *core.Trash, t *core.Tree) (string, string, error) {
		ids := subtreeIDs(t, id)
		var abs string
		if n := t.Node(id); n != nil && n.Kind != core.KindRoot {
			abs = t.Abs(n.Path)
		}
		var err error
		entry, err = t.Delete(id, tr)
		// A delete that failed after moving the item keeps nothing to write
		// to either: writing the edits would recreate the file.
		if err == nil || (abs != "" && !exists(abs)) {
			s.discardPendingIDsLocked(t, ids)
		}
		return id, entry.ID, err
	})
	if err != nil {
		return DeleteResultDTO{}, err
	}
	return DeleteResultDTO{Tree: res.Tree, Entry: toTrashEntryDTO(entry)}, nil
}

// Restore puts a deleted item back from the trash: into its folder at its
// old place, or at the end of the top level when the folder is gone. The
// restored items come back as they were deleted: any pending edit left for
// their IDs is dropped, not written over them.
func (s *CollectionService) Restore(ctx context.Context, trashID string) (RestoreResultDTO, error) {
	var r core.Restored
	res, err := s.nodeOp(ctx, "restore", "Restoring", func(tr *core.Trash, t *core.Tree) (string, string, error) {
		var err error
		r, err = t.Restore(tr, trashID)
		if err == nil {
			s.discardPendingLocked(t, r.NodeID)
		}
		return r.NodeID, trashID, err
	})
	if err != nil {
		return RestoreResultDTO{}, err
	}
	return RestoreResultDTO{Tree: res.Tree, NodeID: r.NodeID, ToRoot: r.ToRoot}, nil
}

// ListTrash returns the deleted items of the open project, most recently
// deleted first.
func (s *CollectionService) ListTrash(ctx context.Context) ([]TrashEntryDTO, error) {
	if err := interrupted(ctx, "Loading the trash"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.workspace()
	if err != nil {
		return nil, err
	}
	list, err := trashOf(ws).List()
	if err != nil {
		return nil, err
	}
	out := make([]TrashEntryDTO, 0, len(list))
	for _, e := range list {
		out = append(out, toTrashEntryDTO(e))
	}
	return out, nil
}

// PurgeTrash permanently removes the trash entries deleted more than
// olderThanMs milliseconds ago (0: all of them) and returns how many it
// removed. Nothing is ever purged automatically.
func (s *CollectionService) PurgeTrash(ctx context.Context, olderThanMs int) (int, error) {
	if err := interrupted(ctx, "Emptying the trash"); err != nil {
		return 0, err
	}
	if olderThanMs < 0 {
		return 0, errs.New(errs.InvalidArgument, "The age of the deleted items to remove cannot be negative.").
			WithHint("This is a bug in PostSonde.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.workspace()
	if err != nil {
		return 0, err
	}
	now := time.Now()
	before := now.Add(-time.Duration(olderThanMs) * time.Millisecond)
	if olderThanMs == 0 {
		before = now.Add(time.Second) // everything, whatever the clock says
	}
	n, err := trashOf(ws).Purge(before)
	if n > 0 {
		s.logger.Info("trash purged", "workspace", ws.ID(), "entries", n)
		s.emit(EventChanged, ChangedEventDTO{WorkspaceID: ws.ID(), Op: "purge_trash"})
	}
	return n, err
}

// nodeOp runs a tree operation on the open project's tree. fn returns the
// item and the trash entry the operation concerned.
func (s *CollectionService) nodeOp(ctx context.Context, op, what string, fn func(*core.Trash, *core.Tree) (string, string, error)) (NodeResultDTO, error) {
	if err := interrupted(ctx, what); err != nil {
		return NodeResultDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return NodeResultDTO{}, err
	}
	id, trashID, err := fn(trashOf(ws), tree)
	if err != nil {
		// The files may be partly changed: read them again next time.
		s.tree = nil
		s.logger.Warn("tree operation failed", "op", op, "workspace", ws.ID(), "err", err)
		return NodeResultDTO{}, err
	}
	s.logger.Info("tree operation", "op", op, "workspace", ws.ID(), "node", id, "trash", trashID)
	// An import may have written environment files: the app's own writes.
	s.watch.envSums = environmentSums(ws.Root)
	s.emit(EventChanged, ChangedEventDTO{WorkspaceID: ws.ID(), Op: op, NodeID: id, TrashID: trashID})
	return NodeResultDTO{Tree: s.treeDTOLocked(ws, tree), NodeID: id}, nil
}

// workspace returns the open workspace, or fails with workspace.none_open.
func (s *CollectionService) workspace() (*coreworkspace.Workspace, error) {
	if ws := s.manager.Current(); ws != nil {
		return ws, nil
	}
	_, err := s.manager.LocalDir()
	return nil, err
}

func trashOf(ws *coreworkspace.Workspace) *core.Trash {
	return core.NewTrash(filepath.Join(ws.LocalDir, TrashDirName))
}

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}

// exists reports whether path is there, a symbolic link included.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
