package collection

import (
	"context"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Proto setups of gRPC requests (GRPC-01, GRPC-02, docs/grpc.md). A grpc
// request either lists its own .proto files and import paths, or inherits
// the setup of the nearest folder above it that has one (the grpc.proto
// member of _folder.json).

// OpUpdateFolder is the collection:changed op after a folder's settings
// were saved.
const OpUpdateFolder = "update_folder"

// InheritedGRPCProtoDTO is the proto setup a grpc request with inherit set
// uses, and where it comes from.
type InheritedGRPCProtoDTO struct {
	// FolderID and FolderName name the nearest folder above the request
	// with a proto setup; "" when none has one.
	FolderID   string `json:"folderId"`
	FolderName string `json:"folderName"`
	// Files and ImportPaths are that folder's setup; empty when none.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// ParentID and ParentName name the folder the request is in, where a
	// shared setup can be created when no folder has one; "" at the top
	// level.
	ParentID   string `json:"parentId"`
	ParentName string `json:"parentName"`
}

// UpdateFolderGRPCProtoInputDTO is the input of UpdateFolderGRPCProto.
type UpdateFolderGRPCProtoInputDTO struct {
	FolderID    string   `json:"folderId"`
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
}

// InheritedGRPCProto returns the proto setup request id would inherit from
// its folders, whether or not it inherits it. Folders whose _folder.json
// could not be read are skipped. It fails with not_found for an ID that is
// not a request.
func (s *CollectionService) InheritedGRPCProto(ctx context.Context, id string) (InheritedGRPCProtoDTO, error) {
	if err := interrupted(ctx, "Loading the folder's proto setup"); err != nil {
		return InheritedGRPCProtoDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return InheritedGRPCProtoDTO{}, err
	}
	n := tree.Node(id)
	if n == nil && isScratchLocked(ws, tree, id) {
		// A scratch request (COL-06) has no folders to inherit from.
		return InheritedGRPCProtoDTO{Files: make([]string, 0), ImportPaths: make([]string, 0)}, nil
	}
	if n == nil || n.Kind != core.KindRequest {
		return InheritedGRPCProtoDTO{}, errs.New(errs.NotFound, "The request is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	out := InheritedGRPCProtoDTO{Files: make([]string, 0), ImportPaths: make([]string, 0)}
	if p := n.Parent; p != nil && p.Kind == core.KindFolder {
		out.ParentID, out.ParentName = p.ID, p.DisplayName()
	}
	if folder := protoFolder(n); folder != nil {
		setup := folder.File.(*projectfile.Folder).GRPC.Proto
		out.FolderID, out.FolderName = folder.ID, folder.DisplayName()
		out.Files = append(out.Files, setup.Files...)
		out.ImportPaths = append(out.ImportPaths, setup.ImportPaths...)
	}
	return out, nil
}

// protoFolder returns the nearest folder above n with a proto setup, or
// nil.
func protoFolder(n *core.Node) *core.Node {
	for p := n.Parent; p != nil && p.Kind == core.KindFolder; p = p.Parent {
		if f, ok := p.File.(*projectfile.Folder); ok && hasProtoSetup(f.GRPC.Proto) {
			return p
		}
	}
	return nil
}

func hasProtoSetup(p projectfile.GRPCProto) bool {
	return len(p.Files) > 0 || len(p.ImportPaths) > 0
}

// UpdateFolderGRPCProto sets the proto setup of folder in.FolderID and
// writes its _folder.json at once. Empty lists remove the setup. Unknown
// members of the folder and of its grpc object are kept. It sends
// collection:changed with op "update_folder". It fails with
// collection.not_found, collection.wrong_node or collection.read_only, and
// with the write's error.
func (s *CollectionService) UpdateFolderGRPCProto(ctx context.Context, in UpdateFolderGRPCProtoInputDTO) error {
	if err := interrupted(ctx, "Saving the folder's proto setup"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return err
	}
	if err := tree.CheckWritable(in.FolderID, core.KindFolder); err != nil {
		return err
	}
	current, ok := tree.Node(in.FolderID).File.(*projectfile.Folder)
	if !ok {
		return errs.New(errs.Unsupported, "The folder's settings could not be read, so they are not overwritten.").
			WithCode(core.CodeReadOnly).
			WithHint("Fix _folder.json by hand, then reload the project.").
			WithDetail("id", in.FolderID)
	}
	c := *current
	c.GRPC.Proto = projectfile.GRPCProto{Unknown: current.GRPC.Proto.Unknown}
	if len(in.Files) > 0 {
		c.GRPC.Proto.Files = append([]string(nil), in.Files...)
	}
	if len(in.ImportPaths) > 0 {
		c.GRPC.Proto.ImportPaths = append([]string(nil), in.ImportPaths...)
	}
	if _, err := tree.SaveFolder(in.FolderID, &c); err != nil {
		s.logger.Warn("saving the folder failed", "workspace", ws.ID(), "node", in.FolderID, "err", err)
		return err
	}
	s.emit(EventChanged, ChangedEventDTO{WorkspaceID: ws.ID(), Op: OpUpdateFolder, NodeID: in.FolderID})
	return nil
}
