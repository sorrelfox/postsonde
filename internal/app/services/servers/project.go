package servers

import (
	"context"
	"errors"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	corecollection "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/serverdef"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Saved server configs (SRV-04, docs/servers.md#saved-server-configs): a
// folder of the open project can define one server in its _folder.json.
// The service keeps such a server in the Manager like any other, stopped
// until the user starts it, with Spec.Owner naming the project and the
// folder, and writes every change of its definition back to the file:
// UpdateConfig, UpdateServer and Remove save first through Project, and a
// change the file does not take is undone. SyncProject brings the Manager
// in line with the open project; Start reads the definition again, so a
// server starts from what the file holds.

// Project reads and writes the saved server configs of the open project.
// The app wires it to the collection service (WithProject).
type Project interface {
	// FolderServers returns the ID of the open project ("" when none is
	// open) and its folders that define a server; unreadable lists the
	// folders whose _folder.json did not load.
	FolderServers(ctx context.Context) (workspaceID string, folders []ProjectFolder, unreadable []string, err error)
	// FolderServer returns folder folderID of project workspaceID with its
	// server (nil when it has none). An error with code
	// collection.not_found means the project is not open or the folder is
	// gone.
	FolderServer(ctx context.Context, workspaceID, folderID string) (ProjectFolder, error)
	// SaveFolderServer writes def as the folder's server; nil removes it.
	SaveFolderServer(ctx context.Context, workspaceID, folderID string, def *projectfile.FolderServer) error
}

// ProjectFolder is a folder and its saved server.
type ProjectFolder struct {
	ID     string
	Name   string
	Server *projectfile.FolderServer
}

// codeFolderGone is the collection's code for a folder or project that is
// not there (collection.not_found).
const codeFolderGone = corecollection.CodeNotFound

// CollectionProject is the Project of the collection service: the saved
// server configs of the project it has open.
func CollectionProject(c *collection.CollectionService) Project {
	return collectionProject{c}
}

type collectionProject struct{ c *collection.CollectionService }

func (p collectionProject) FolderServers(ctx context.Context) (string, []ProjectFolder, []string, error) {
	id, folders, unreadable, err := collection.FolderServers(p.c, ctx)
	if errCode(err) == workspace.CodeNoWorkspace {
		return "", nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	out := make([]ProjectFolder, 0, len(folders))
	for _, f := range folders {
		out = append(out, ProjectFolder{ID: f.FolderID, Name: f.FolderName, Server: f.Server})
	}
	return id, out, unreadable, nil
}

func (p collectionProject) FolderServer(ctx context.Context, workspaceID, folderID string) (ProjectFolder, error) {
	f, err := collection.LoadFolderServer(p.c, ctx, workspaceID, folderID)
	if errCode(err) == workspace.CodeNoWorkspace {
		return ProjectFolder{}, errs.Wrap(err, errs.NotFound, "The folder's project is no longer open.").WithCode(codeFolderGone)
	}
	return ProjectFolder{ID: f.FolderID, Name: f.FolderName, Server: f.Server}, err
}

func (p collectionProject) SaveFolderServer(ctx context.Context, workspaceID, folderID string, def *projectfile.FolderServer) error {
	err := collection.SaveFolderServer(p.c, ctx, workspaceID, folderID, def)
	if errCode(err) == workspace.CodeNoWorkspace {
		return errs.Wrap(err, errs.NotFound, "The folder's project is no longer open.").WithCode(codeFolderGone)
	}
	return err
}

// Error codes of the saved server configs.
const (
	// CodeProjectUnavailable: the service was built without a project.
	CodeProjectUnavailable = "servers.project_unavailable"
	// CodeFolderHasServer: the folder already defines a server.
	CodeFolderHasServer = "servers.folder_has_server"
)

// WithProject lets the service read and write saved server configs;
// without it SyncProject finds none and CreateFolderServer fails with
// servers.project_unavailable.
func WithProject(p Project) Option {
	return func(s *ServersService) { s.project = p }
}

// SyncProject brings the servers in line with the open project's saved
// server configs, and is called when a project is opened or its tree was
// loaded again. Every folder that defines a server gets one, stopped (a
// server is never started without the user). A stopped server of the open
// project takes its definition from the file again, and one whose
// definition is gone is removed; one that is running keeps running with
// its address until it stops. Servers of another project are removed when
// they are stopped and keep running otherwise (docs/servers.md). A
// definition the Manager refuses (an unknown type, a bad port) is
// reported in Problems.
func (s *ServersService) SyncProject(ctx context.Context) (ProjectServersDTO, error) {
	out := ProjectServersDTO{Problems: []FolderServerProblemDTO{}}
	if err := interrupted(ctx, "Loading the project's servers"); err != nil {
		return out, err
	}
	s.projMu.Lock()
	defer s.projMu.Unlock()
	var folders []ProjectFolder
	var unreadable []string
	if s.project != nil {
		var err error
		out.WorkspaceID, folders, unreadable, err = s.project.FolderServers(ctx)
		if err != nil {
			return out, err
		}
	}
	defined := map[string]ProjectFolder{}
	for _, f := range folders {
		defined[f.ID] = f
	}
	keep := map[string]bool{}
	for _, id := range unreadable {
		keep[id] = true // cannot tell; leave its server as it is
	}
	linked := map[string]core.Server{}
	for _, srv := range s.manager.List() {
		owner := srv.Spec.Owner
		switch {
		case owner.IsZero():
		case owner.Workspace != out.WorkspaceID || out.WorkspaceID == "":
			if !srv.State.Active() {
				_ = s.manager.Remove(ctx, srv.ID)
			}
		case keep[owner.Folder]:
			linked[owner.Folder] = srv
		default:
			if _, ok := defined[owner.Folder]; ok && linked[owner.Folder].ID == "" {
				linked[owner.Folder] = srv
			} else if !srv.State.Active() {
				_ = s.manager.Remove(ctx, srv.ID) // its definition is gone (or a duplicate)
			}
		}
	}
	for _, f := range folders {
		spec := serverdef.ToSpec(f.Server)
		spec.Owner = core.Owner{Workspace: out.WorkspaceID, Folder: f.ID, Label: f.Name}
		var err error
		if srv, ok := linked[f.ID]; ok {
			err = s.redefineFromFile(srv, spec)
		} else {
			_, err = s.manager.Add(spec)
		}
		if err != nil {
			out.Problems = append(out.Problems, FolderServerProblemDTO{FolderID: f.ID, FolderName: f.Name, Error: apperr.ToDTO(err)})
		}
	}
	return out, nil
}

// redefineFromFile gives srv the definition spec read from its folder. A
// running server whose address changed keeps it and takes the rest; a
// type that changed replaces the server.
func (s *ServersService) redefineFromFile(srv core.Server, spec core.Spec) error {
	if spec.Type != srv.Spec.Type {
		if srv.State.Active() {
			return nil // takes the new kind once it stops and the project is loaded again
		}
		if err := s.manager.Remove(context.Background(), srv.ID); err != nil {
			return err
		}
		_, err := s.manager.Add(spec)
		return err
	}
	if srv.State.Active() {
		spec.Host, spec.Port, spec.AllowLAN = srv.Spec.Host, srv.Spec.Port, srv.Spec.AllowLAN
	}
	_, err := s.manager.Redefine(srv.ID, spec)
	return err
}

// CreateFolderServer defines a server in folder in.FolderID of project
// in.WorkspaceID (the open one): it is saved in the folder's _folder.json
// and returned stopped. A folder that has one already is
// servers.folder_has_server; the definition is checked like Create's.
func (s *ServersService) CreateFolderServer(ctx context.Context, in FolderServerInputDTO) (ServerDTO, error) {
	if err := interrupted(ctx, "Creating the server"); err != nil {
		return ServerDTO{}, err
	}
	s.projMu.Lock()
	defer s.projMu.Unlock()
	if s.project == nil {
		return ServerDTO{}, projectUnavailable()
	}
	folder, err := s.project.FolderServer(ctx, in.WorkspaceID, in.FolderID)
	if err != nil {
		return ServerDTO{}, err
	}
	if folder.Server != nil || s.linked(in.WorkspaceID, in.FolderID).ID != "" {
		return ServerDTO{}, errs.New(errs.AlreadyExists, "This folder already has a server.").
			WithCode(CodeFolderHasServer).WithDetail("folderId", in.FolderID)
	}
	spec := in.Server.toCore()
	spec.Owner = core.Owner{Workspace: in.WorkspaceID, Folder: in.FolderID, Label: folder.Name}
	var pass *string
	if spec.TLS, pass, err = s.prepareTLS(in.Server.TLS); err != nil {
		return ServerDTO{}, err
	}
	spec, err = s.manager.Check(spec)
	if err != nil {
		return ServerDTO{}, err
	}
	if err := s.storePassphrase(spec.TLS, pass); err != nil {
		return ServerDTO{}, err
	}
	if err := s.project.SaveFolderServer(ctx, in.WorkspaceID, in.FolderID, serverdef.FromSpec(nil, spec, s.typeLabel(spec.Type))); err != nil {
		return ServerDTO{}, err
	}
	srv, err := s.manager.Add(spec)
	if err != nil {
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// UpdateServer changes the name, host, port, LAN access and TLS of the
// server id, keeping its ID, configuration and traffic. The address and
// TLS cannot change while it runs (servers.active). Certificate files are
// read now, and a passphrase given is kept in the local secret storage
// (never in the folder file). A saved server's folder file is written
// too; when that fails (a read-only folder, a project that is not open)
// nothing changes.
func (s *ServersService) UpdateServer(ctx context.Context, id string, in ServerSettingsDTO) (ServerDTO, error) {
	if err := interrupted(ctx, "Updating the server"); err != nil {
		return ServerDTO{}, err
	}
	s.projMu.Lock()
	defer s.projMu.Unlock()
	old, err := s.manager.Get(id)
	if err != nil {
		return ServerDTO{}, err
	}
	spec := old.Spec
	spec.Name, spec.Host, spec.Port, spec.AllowLAN = in.Name, in.Host, in.Port, in.AllowLAN
	var pass *string
	if spec.TLS, pass, err = s.prepareTLS(in.TLS); err != nil {
		return ServerDTO{}, err
	}
	srv, err := s.manager.Redefine(id, spec)
	if err != nil {
		return ServerDTO{}, err
	}
	if err := s.persist(ctx, srv.Spec); err != nil {
		_, _ = s.manager.Redefine(id, old.Spec)
		return ServerDTO{}, err
	}
	if err := s.storePassphrase(srv.Spec.TLS, pass); err != nil {
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// persist writes spec to its folder's saved server config; an ad-hoc
// server has nothing to write.
func (s *ServersService) persist(ctx context.Context, spec core.Spec) error {
	owner := spec.Owner
	if owner.IsZero() {
		return nil
	}
	if s.project == nil {
		return projectUnavailable()
	}
	folder, err := s.project.FolderServer(ctx, owner.Workspace, owner.Folder)
	if err != nil {
		return err
	}
	return s.project.SaveFolderServer(ctx, owner.Workspace, owner.Folder, serverdef.FromSpec(folder.Server, spec, s.typeLabel(spec.Type)))
}

// reloadSaved gives a saved server of the open project the definition its
// folder holds now, before it starts. A folder or project that is not
// there leaves it as it is.
func (s *ServersService) reloadSaved(ctx context.Context, id string) error {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	srv, err := s.manager.Get(id)
	if err != nil || srv.Spec.Owner.IsZero() || srv.State.Active() || s.project == nil {
		return err
	}
	owner := srv.Spec.Owner
	folder, err := s.project.FolderServer(ctx, owner.Workspace, owner.Folder)
	if err != nil {
		if errCode(err) == codeFolderGone {
			return nil
		}
		return err
	}
	if folder.Server == nil {
		return nil
	}
	spec := serverdef.ToSpec(folder.Server)
	spec.Owner = core.Owner{Workspace: owner.Workspace, Folder: owner.Folder, Label: folder.Name}
	return s.redefineFromFile(srv, spec)
}

// forget removes the saved config of server srv from its folder, before
// the server is removed. A folder or project that is not there has nothing
// to remove.
func (s *ServersService) forget(ctx context.Context, srv core.Server) error {
	owner := srv.Spec.Owner
	if owner.IsZero() || s.project == nil {
		return nil
	}
	folder, err := s.project.FolderServer(ctx, owner.Workspace, owner.Folder)
	if err != nil {
		if errCode(err) == codeFolderGone {
			return nil
		}
		return err
	}
	if folder.Server == nil {
		return nil
	}
	return s.project.SaveFolderServer(ctx, owner.Workspace, owner.Folder, nil)
}

// linked returns the server of folder folderID of project workspaceID, or
// a zero Server.
func (s *ServersService) linked(workspaceID, folderID string) core.Server {
	for _, srv := range s.manager.List() {
		if srv.Spec.Owner.Workspace == workspaceID && srv.Spec.Owner.Folder == folderID {
			return srv
		}
	}
	return core.Server{}
}

func (s *ServersService) typeLabel(name string) string {
	for _, t := range s.manager.Types() {
		if t.Name() == name {
			return t.Label()
		}
	}
	return ""
}

func projectUnavailable() *errs.Error {
	return errs.New(errs.Internal, "Servers cannot be saved in the project here.").WithCode(CodeProjectUnavailable)
}

func errCode(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}
