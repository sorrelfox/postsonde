package collection

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Scratch requests (COL-06): quick requests kept in the workspace's local
// side files (core.Scratch), not in the project, until "Save to…" moves
// one into a folder. They have ordinary request IDs, so Request,
// UpdateRequest, Flush, LoadHTTPSource and InheritedGRPCProto take them
// like any request: an ID that is not in the tree but is a scratch
// request is one. Their pending edits are keyed like the tree's (the tree
// stands for the project) and written to the scratch file. See
// docs/collection.md (Scratch requests).

// ScratchDirName is the directory of the scratch requests in the
// workspace's local side files (docs/workspace.md).
const ScratchDirName = "scratch"

// CreateScratchInputDTO is the input of CreateScratch.
type CreateScratchInputDTO struct {
	// Kind is "http", "graphql", "tcp", "udp", "websocket", "grpc", "sse" or "socketio".
	Kind string `json:"kind"`
	// Name is the request's name; "" gives "Scratch N".
	Name string `json:"name"`
}

// SaveScratchInputDTO is the input of SaveScratch.
type SaveScratchInputDTO struct {
	// ID is the scratch request.
	ID string `json:"id"`
	// ParentID is the folder to save it into; "" for the top level.
	ParentID string `json:"parentId"`
	// Name is its name in the project; "" keeps its name.
	Name string `json:"name"`
	// Index is its position among the folder's items; -1 puts it last.
	Index int `json:"index"`
}

func scratchDir(localDir string) string { return filepath.Join(localDir, ScratchDirName) }

func scratchOf(ws *coreworkspace.Workspace) *core.Scratch {
	return core.NewScratch(scratchDir(ws.LocalDir))
}

// isScratchLocked reports whether id is a scratch request of the open
// project: not an item of its tree, and in its scratch directory.
func isScratchLocked(ws *coreworkspace.Workspace, tree *core.Tree, id string) bool {
	return tree.Node(id) == nil && core.IsScratchID(id) && scratchOf(ws).Has(id)
}

// parentOf returns n's parent; nil for a nil node (a scratch request).
func parentOf(n *core.Node) *core.Node {
	if n == nil {
		return nil
	}
	return n.Parent
}

// treeDTOLocked returns the tree DTO with the scratch requests of the
// project, their pending edits included (a new name shows at once).
func (s *CollectionService) treeDTOLocked(ws *coreworkspace.Workspace, tree *core.Tree) TreeDTO {
	dto := toTreeDTO(ws.ID(), tree)
	items, problems := scratchOf(ws).List()
	for _, p := range problems {
		s.logger.Warn("scratch request problem", "path", p.Path, "code", p.Err.Code, "err", p.Err)
	}
	for _, it := range items {
		// A save into the project that stopped before the scratch file was
		// removed leaves both: the project's wins.
		if tree.Node(it.ID) != nil {
			continue
		}
		f := it.File
		if p, ok := s.saver.Get(saveKey{tree, it.ID}); ok {
			f = p.file
		}
		dto.Scratch = append(dto.Scratch, toScratchNodeDTO(it.ID, f, it.ReadOnly))
	}
	// Sorted again: a pending edit may have renamed one.
	slices.SortStableFunc(dto.Scratch, func(a, b NodeDTO) int {
		return cmp.Or(cmp.Compare(core.FoldName(a.Name), core.FoldName(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return dto
}

func toScratchNodeDTO(id string, f projectfile.File, readOnly core.ReadOnlyReason) NodeDTO {
	dto := NodeDTO{
		ID:       id,
		Kind:     string(core.KindRequest),
		Name:     core.ScratchName(f),
		FileKind: string(f.FileKind()),
		ChildIDs: make([]string, 0),
		ReadOnly: string(readOnly),
	}
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		dto.Method = r.Method
	case *projectfile.SSERequest:
		dto.Method = r.HTTP().Method
	case *projectfile.GraphQLRequest:
		dto.Method = "POST"
		if r.Method != "" {
			dto.Method = r.Method
		}
	}
	return dto
}

// CreateScratch creates an empty scratch request of a kind and returns the
// tree with its ID. It is written to the local side files at once.
func (s *CollectionService) CreateScratch(ctx context.Context, in CreateScratchInputDTO) (NodeResultDTO, error) {
	if err := interrupted(ctx, "Creating the scratch request"); err != nil {
		return NodeResultDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return NodeResultDTO{}, err
	}
	f, err := scratchOf(ws).Create(projectfile.Kind(in.Kind), strings.TrimSpace(in.Name))
	if err != nil {
		return NodeResultDTO{}, err
	}
	id := core.RequestFileID(f)
	s.logger.Info("scratch request created", "workspace", ws.ID(), "node", id)
	s.emit(EventChanged, ChangedEventDTO{WorkspaceID: ws.ID(), Op: "create_scratch", NodeID: id})
	return NodeResultDTO{Tree: s.treeDTOLocked(ws, tree), NodeID: id}, nil
}

// DeleteScratch removes a scratch request for good, with its pending edits
// and its locally stored auth secret, and returns the tree. A scratch
// request that is not there is not an error.
func (s *CollectionService) DeleteScratch(ctx context.Context, id string) (TreeDTO, error) {
	if err := interrupted(ctx, "Deleting the scratch request"); err != nil {
		return TreeDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return TreeDTO{}, err
	}
	if tree.Node(id) == nil && core.IsScratchID(id) {
		s.discardPendingIDsLocked(tree, map[string]bool{id: true})
		if err := scratchOf(ws).Delete(id); err != nil {
			return TreeDTO{}, err
		}
		if err := requestsecrets.New(ws.LocalDir).Remove(id); err != nil {
			s.logger.Warn("the local secrets of a deleted scratch request were not removed", "id", id, "err", err)
		}
		s.logger.Info("scratch request deleted", "workspace", ws.ID(), "node", id)
		s.emit(EventChanged, ChangedEventDTO{WorkspaceID: ws.ID(), Op: "delete_scratch", NodeID: id})
	}
	return s.treeDTOLocked(ws, tree), nil
}

// SaveScratch moves a scratch request into the project: into the folder
// in.ParentID at in.Index, called in.Name (or its own name), as an
// ordinary request file. It keeps its ID, so its open tab, draft and
// local auth secret stay; only when an item of the project already has
// that ID does it get a new one, and the secret moves with it. Its pending
// edits are written first, so the new file has them. The scratch file is
// removed once the request file is written. Returns the tree with the
// request's ID.
func (s *CollectionService) SaveScratch(ctx context.Context, in SaveScratchInputDTO) (NodeResultDTO, error) {
	return s.nodeOp(ctx, "save_scratch", "Saving the scratch request", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		ws := s.ws // nodeOp holds s.mu and has just set it to the open workspace
		scratch := scratchOf(ws)
		if !isScratchLocked(ws, t, in.ID) {
			return "", "", errs.New(errs.NotFound, "The scratch request no longer exists.").
				WithCode(core.CodeNotFound).
				WithHint("It may have been saved into the project or deleted already.").
				WithDetail("id", in.ID)
		}
		if err := s.saver.FlushWhere(func(k saveKey) bool { return k.id == in.ID && k.tree.Dir() == t.Dir() }); err != nil {
			return "", "", err
		}
		it, err := scratch.Get(in.ID)
		if err != nil {
			return "", "", err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = it.Name()
		}
		id, err := t.AddRequest(in.ParentID, it.File, name, in.Index)
		if err != nil {
			return "", "", err
		}
		if id != in.ID {
			secrets := requestsecrets.New(ws.LocalDir)
			if err := secrets.Copy(in.ID, id); err != nil {
				s.logger.Warn("the local secrets of a saved scratch request were not copied", "from", in.ID, "to", id, "err", err)
			} else {
				_ = secrets.Remove(in.ID)
			}
		}
		if err := scratch.Delete(in.ID); err != nil {
			// The request is in the project; the leftover scratch file is
			// hidden while the project has its ID.
			s.logger.Warn("a saved scratch request's file was not removed", "id", in.ID, "err", err)
		}
		return id, "", nil
	})
}
