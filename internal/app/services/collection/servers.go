package collection

import (
	"context"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Saved server configs (SRV-04, docs/servers.md#saved-server-configs): the
// "server" member of a folder's _folder.json. The servers service reads
// and writes them through these functions (the app wires them in); they
// are not bound to the UI, which edits a folder's server through
// ServersService.

// FolderServer is a folder of the open project and its saved server.
type FolderServer struct {
	FolderID   string
	FolderName string
	// Server is the definition, nil when the folder has none.
	Server *projectfile.FolderServer
}

// FolderServers returns the ID of the open project and its folders that
// define a server, in tree order. Folders whose _folder.json did not load
// are left out, and so are unreadable reports: unreadable lists their IDs,
// so a caller does not take a folder it cannot read for one without a
// server.
func FolderServers(s *CollectionService, ctx context.Context) (workspaceID string, folders []FolderServer, unreadable []string, err error) {
	if err := interrupted(ctx, "Loading the project's servers"); err != nil {
		return "", nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return "", nil, nil, err
	}
	tree.Walk(func(n *core.Node) bool {
		if n.Kind != core.KindFolder {
			return true
		}
		f, ok := n.File.(*projectfile.Folder)
		switch {
		case !ok:
			unreadable = append(unreadable, n.ID)
		case f.Server != nil:
			folders = append(folders, FolderServer{FolderID: n.ID, FolderName: n.DisplayName(), Server: f.Server})
		}
		return true
	})
	return ws.ID(), folders, unreadable, nil
}

// LoadFolderServer returns folder id of the open project with its saved
// server (nil when it has none). It fails with collection.not_found when
// workspaceID is not the open project or id is not one of its folders,
// and collection.read_only when its _folder.json did not load.
func LoadFolderServer(s *CollectionService, ctx context.Context, workspaceID, id string) (FolderServer, error) {
	if err := interrupted(ctx, "Loading the folder's server"); err != nil {
		return FolderServer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, n, err := s.serverFolderLocked(workspaceID, id)
	if err != nil {
		return FolderServer{}, err
	}
	f, ok := n.File.(*projectfile.Folder)
	if !ok {
		return FolderServer{}, unreadableFolder(id)
	}
	return FolderServer{FolderID: n.ID, FolderName: n.DisplayName(), Server: f.Server}, nil
}

// SaveFolderServer makes def the saved server of folder id (nil removes
// it) and writes its _folder.json at once. Every other member stays as the
// file has it, and settings edits waiting for the auto-saver are written
// over it later without touching the server. It fails like
// LoadFolderServer, and for a folder or project that is read-only
// (collection.read_only) or a file that cannot be written.
func SaveFolderServer(s *CollectionService, ctx context.Context, workspaceID, id string, def *projectfile.FolderServer) error {
	if err := saveInterrupted(ctx, "Saving the folder's server"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tree, n, err := s.serverFolderLocked(workspaceID, id)
	if err != nil {
		return err
	}
	if err := tree.CheckWritable(id, core.KindFolder); err != nil {
		return err
	}
	current, ok := n.File.(*projectfile.Folder)
	if !ok {
		return unreadableFolder(id)
	}
	c := *current
	c.Server = def
	_, err = tree.SaveFolder(id, &c)
	return err
}

// serverFolderLocked returns the tree and the folder node id of project
// workspaceID, which must be the open one.
func (s *CollectionService) serverFolderLocked(workspaceID, id string) (*core.Tree, *core.Node, error) {
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return nil, nil, err
	}
	if ws.ID() != workspaceID {
		return nil, nil, errs.New(errs.NotFound, "The folder's project is no longer open.").
			WithCode(core.CodeNotFound).
			WithHint("Open that project to change its servers.").
			WithDetail("id", id)
	}
	n := tree.Node(id)
	if n == nil || n.Kind != core.KindFolder {
		return nil, nil, errs.New(errs.NotFound, "The folder is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	return tree, n, nil
}
