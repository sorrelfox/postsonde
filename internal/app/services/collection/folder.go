package collection

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Folder settings (COL-03, docs/collection.md#folder-settings): the auth,
// headers and variables of a folder's _folder.json, inherited by
// everything inside it, and its Markdown notes (description, COL-07). They are edited like a request: UpdateFolder keeps
// the edit pending and the auto-saver writes it once edits pause (the
// collection:saved and collection:save-failed events, Flush and FlushAll
// work for folders as for requests). A typed auth secret and the values of
// the secret variables go to the folder's local side file
// (<local>/secrets/requests/<folder id>.json, the variables as
// "var.<name>"), as a request's auth secret does; the _folder.json keeps
// the variable with "value": "" and "secret": true.
//
// Only auth, headers, variables and description come from the edit: the write puts them
// over the folder's current file, so its name, order, proto setup and
// unknown members stay whatever the tree has (a rename or move in between
// is kept).
//
// Sending reads the pending edits too (folderLocked): a send right after
// an edit of a folder's header or auth uses it.

// FolderDTO is the settings of a folder, as the folder editor edits them.
type FolderDTO struct {
	// ID is the folder's ID; it names the folder UpdateFolder saves.
	ID string `json:"id"`
	// Kind is always "folder".
	Kind string `json:"kind"`
	// Name is the folder's name; UpdateFolder ignores it (Rename renames).
	Name string `json:"name"`
	// Description is the folder's Markdown notes (COL-07); "" for none.
	Description string `json:"description"`
	// Auth is the auth the requests inside inherit; type "inherit" (or "")
	// passes on the auth of the folder above.
	Auth AuthDTO `json:"auth"`
	// Headers are added to every HTTP and GraphQL request inside.
	Headers []KeyValueDTO `json:"headers"`
	// Variables are the folder's variables, as key/value rows. A secret
	// variable has "secret": true in its extra members; its value is the
	// one stored on this computer ("" when it was typed on another one).
	Variables []KeyValueDTO `json:"variables"`
	// Version is the version of these settings (Folder only; windows.go):
	// pass it as UpdateFolder's BaseVersion. UpdateFolder ignores it here.
	Version int `json:"version,omitempty"`
}

// UpdateFolderInputDTO is the input of UpdateFolder.
type UpdateFolderInputDTO struct {
	Folder FolderDTO `json:"folder"`
	// Revision is the editor's auto-save revision of this content, sent
	// back in collection:saved and collection:save-failed.
	Revision int `json:"revision"`
	// BaseVersion is the version of the folder's settings the edit was
	// made from (FolderDTO.Version, or the last UpdateFolder result): an
	// older one than the service holds is refused with
	// collection.edited_elsewhere (windows.go). Null is not checked.
	BaseVersion *int `json:"baseVersion,omitempty"`
}

// InheritedFolderDTO is the headers and variables one folder above an
// item passes on.
type InheritedFolderDTO struct {
	FolderID   string        `json:"folderId"`
	FolderName string        `json:"folderName"`
	Headers    []KeyValueDTO `json:"headers"`
	Variables  []KeyValueDTO `json:"variables"`
	// Unreadable is set when the folder's _folder.json did not load: it
	// passes nothing on.
	Unreadable bool `json:"unreadable"`
}

// InheritedSettingsDTO is what an item inherits from the folders above it.
type InheritedSettingsDTO struct {
	// Folders are the folders above the item, the top-level folder first;
	// empty for a top-level item and a scratch request.
	Folders []InheritedFolderDTO `json:"folders"`
}

// Folder returns the settings of folder id, including edits that are not
// written yet, with its local auth secret merged in. It fails with
// collection.not_found for an ID that is not a folder of the open project,
// collection.read_only when its _folder.json did not load, and
// requestsecrets.local_data_failed when its local secret could not be read.
func (s *CollectionService) Folder(ctx context.Context, id string) (FolderDTO, error) {
	if err := saveInterrupted(ctx, "Loading the folder"); err != nil {
		return FolderDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return FolderDTO{}, err
	}
	n := tree.Node(id)
	if n == nil || n.Kind != core.KindFolder {
		return FolderDTO{}, errs.New(errs.NotFound, "The folder is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	f, secrets, pending := s.folderLocked(tree, n)
	if f == nil {
		return FolderDTO{}, unreadableFolder(id)
	}
	if !pending {
		if secrets, err = readSecretsFor(ws.LocalDir, id, f); err != nil {
			return FolderDTO{}, err
		}
	}
	dto := toFolderDTO(requestsecrets.MergeFile(f, secrets).(*projectfile.Folder))
	dto.Version = s.versionLocked(ws, id)
	return dto, nil
}

// UpdateFolder replaces the auth, headers, variables and description of folder
// in.Folder.ID. The new settings are used at once (Folder and sends return
// them) and written to its _folder.json after the save delay, like
// UpdateRequest; the result arrives as collection:saved or
// collection:save-failed.
//
// It fails at once, keeping nothing, for an ID that is not a folder of the
// open project (collection.not_found, collection.wrong_node), a read-only
// folder or project, or one whose _folder.json did not load
// (collection.read_only), settings that cannot be written
// (collection.invalid_request, projectfile.invalid_value), and, like
// UpdateRequest, a conflict with the file (collection.changed_on_disk) or
// an edit made from an older version (collection.edited_elsewhere). The
// result is the folder's new version.
func (s *CollectionService) UpdateFolder(ctx context.Context, in UpdateFolderInputDTO) (UpdateResultDTO, error) {
	if err := saveInterrupted(ctx, "Saving the folder"); err != nil {
		return UpdateResultDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return UpdateResultDTO{}, err
	}
	id := in.Folder.ID
	if err := tree.CheckWritable(id, core.KindFolder); err != nil {
		return UpdateResultDTO{}, err
	}
	if err := s.conflictLocked(id); err != nil {
		return UpdateResultDTO{}, err
	}
	if err := s.checkBaseLocked(ws, id, in.BaseVersion); err != nil {
		return UpdateResultDTO{}, err
	}
	current, ok := tree.Node(id).File.(*projectfile.Folder)
	if !ok {
		return UpdateResultDTO{}, unreadableFolder(id)
	}
	f, err := in.Folder.toSettings()
	if err != nil {
		return UpdateResultDTO{}, err
	}
	split, secrets := splitSecrets(f)
	f = split.(*projectfile.Folder)
	if _, err := projectfile.Marshal(withSettings(current, f)); err != nil {
		return UpdateResultDTO{}, err
	}
	win := windowctx.ID(ctx)
	s.saver.Put(saveKey{tree, id}, pendingSave{
		file:        f,
		secrets:     secrets,
		localDir:    ws.LocalDir,
		revision:    in.Revision,
		workspaceID: ws.ID(),
		window:      win,
		folder:      true,
	})
	return UpdateResultDTO{Version: s.editedLocked(ws, id, win)}, nil
}

// InheritedSettings returns the headers and variables that item id (a
// request or a folder) inherits from the folders above it, pending edits
// included, for the editors' read-only lists. It fails with
// collection.not_found for an ID that is not a request or folder of the
// open project; a scratch request inherits nothing.
func (s *CollectionService) InheritedSettings(ctx context.Context, id string) (InheritedSettingsDTO, error) {
	if err := interrupted(ctx, "Loading the inherited settings"); err != nil {
		return InheritedSettingsDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return InheritedSettingsDTO{}, err
	}
	out := InheritedSettingsDTO{Folders: make([]InheritedFolderDTO, 0)}
	n := tree.Node(id)
	switch {
	case n == nil && isScratchLocked(ws, tree, id):
		return out, nil
	case n == nil || (n.Kind != core.KindRequest && n.Kind != core.KindFolder):
		return InheritedSettingsDTO{}, errs.New(errs.NotFound, "The item is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	for p := n.Parent; p != nil && p.Kind == core.KindFolder; p = p.Parent {
		d := InheritedFolderDTO{FolderID: p.ID, FolderName: p.DisplayName(), Headers: make([]KeyValueDTO, 0), Variables: make([]KeyValueDTO, 0)}
		if f, _, _ := s.folderLocked(tree, p); f != nil {
			d.Headers = toKeyValueDTOs(f.Headers)
			d.Variables = toVariableDTOs(withoutSecretValues(f.Variables))
		} else {
			d.Unreadable = true
		}
		out.Folders = append([]InheritedFolderDTO{d}, out.Folders...)
	}
	return out, nil
}

// folderLocked returns the settings of folder node n: its pending edit put
// over its file, with the pending local secrets (pending is then true), or
// its file. f is nil when its _folder.json did not load.
func (s *CollectionService) folderLocked(tree *core.Tree, n *core.Node) (f *projectfile.Folder, secrets map[string]string, pending bool) {
	current, ok := n.File.(*projectfile.Folder)
	if !ok {
		return nil, nil, false
	}
	if p, ok := s.saver.Get(saveKey{tree, n.ID}); ok && p.folder {
		if edit, ok := p.file.(*projectfile.Folder); ok {
			return withSettings(current, edit), p.secrets, true
		}
	}
	return current, nil, false
}

// withSettings returns a copy of folder file current with the auth,
// headers, variables and description of edit.
func withSettings(current, edit *projectfile.Folder) *projectfile.Folder {
	c := *current
	c.Auth, c.Headers, c.Variables = edit.Auth, edit.Headers, edit.Variables
	c.Description = edit.Description
	return &c
}

// writeFolder writes the settings edit (a *projectfile.Folder) of folder
// id over its current _folder.json; the auto-saver's write.
func writeFolder(tree *core.Tree, id string, edit projectfile.File) error {
	if err := tree.CheckWritable(id, core.KindFolder); err != nil {
		return err
	}
	current, ok := tree.Node(id).File.(*projectfile.Folder)
	e, ok2 := edit.(*projectfile.Folder)
	if !ok || !ok2 {
		return unreadableFolder(id)
	}
	_, err := tree.SaveFolder(id, withSettings(current, e))
	return err
}

func unreadableFolder(id string) error {
	return errs.New(errs.Unsupported, "The folder's settings could not be read, so they are not overwritten.").
		WithCode(core.CodeReadOnly).
		WithHint("Fix _folder.json by hand, then reload the project.").
		WithDetail("id", id)
}

// folderAuthSecrets returns the local secret values of folder id, whose
// auth a request inherits: the pending ones when its settings are being
// edited, else those of its side file.
func (s *CollectionService) folderAuthSecrets(localDir string, tree *core.Tree, id string) (map[string]string, error) {
	if p, ok := s.saver.Get(saveKey{tree, id}); ok && p.folder {
		return p.secrets, nil
	}
	return requestsecrets.New(localDir).Read(id)
}

// folderHeaders is the header rows of the folders above request node n
// (nil for a scratch request), the top-level folder first, pending edits
// included. Folders whose _folder.json did not load pass nothing on.
func (s *CollectionService) folderHeaders(tree *core.Tree, n *core.Node) []request.FolderHeaders {
	var out []request.FolderHeaders
	for p := parentOf(n); p != nil && p.Kind == core.KindFolder; p = p.Parent {
		if f, _, _ := s.folderLocked(tree, p); f != nil && len(f.Headers) > 0 {
			out = append([]request.FolderHeaders{{FolderID: p.ID, FolderName: p.DisplayName(), Rows: f.Headers}}, out...)
		}
	}
	return out
}

// toFolderDTO maps a folder file; its lists are never null.
func toFolderDTO(f *projectfile.Folder) FolderDTO {
	return FolderDTO{
		ID:          f.ID,
		Kind:        string(projectfile.KindFolder),
		Name:        f.Name,
		Description: f.Description,
		Auth:        toAuthDTO(f.Auth),
		Headers:     toKeyValueDTOs(f.Headers),
		Variables:   toVariableDTOs(f.Variables),
	}
}

// withoutSecretValues returns vars with the values of the secret ones
// left out, for the read-only lists of inherited variables: the editor of
// the folder that has them shows them, masked.
func withoutSecretValues(vars []projectfile.Variable) []projectfile.Variable {
	if !requestsecrets.HasSecretVariables(vars) {
		return vars
	}
	out := slices.Clone(vars)
	for i := range out {
		if out[i].Secret {
			out[i].Value = ""
		}
	}
	return out
}

// secretMember is the extra member that carries a folder variable's secret
// flag through the key/value rows of FolderDTO.
const secretMember = "secret"

// toVariableDTOs maps variables to key/value rows; a secret one gets
// "secret": true in its extra members.
func toVariableDTOs(vars []projectfile.Variable) []KeyValueDTO {
	out := make([]KeyValueDTO, len(vars))
	for i, v := range vars {
		u := v.Unknown
		if v.Secret {
			u = make(projectfile.Unknown, len(v.Unknown)+1)
			for k, raw := range v.Unknown {
				u[k] = raw
			}
			u[secretMember] = json.RawMessage("true")
		}
		out[i] = KeyValueDTO{
			Name:        v.Name,
			Value:       v.Value,
			Description: v.Description,
			Enabled:     !v.Disabled,
			Extra:       extraText(u),
		}
	}
	return out
}

// variables maps key/value rows back to variables (toVariableDTOs).
func (c *converter) variables(field string, rows []KeyValueDTO) []projectfile.Variable {
	if len(rows) == 0 {
		return nil
	}
	out := make([]projectfile.Variable, len(rows))
	for i, r := range rows {
		u := c.extra(field+"["+strconv.Itoa(i)+"].extra", r.Extra)
		secret := false
		if raw, ok := u[secretMember]; ok {
			_ = json.Unmarshal(raw, &secret)
			delete(u, secretMember)
			if len(u) == 0 {
				u = nil
			}
		}
		out[i] = projectfile.Variable{
			Name:        r.Name,
			Value:       r.Value,
			Secret:      secret,
			Description: r.Description,
			Disabled:    !r.Enabled,
			Unknown:     u,
		}
	}
	return out
}

// toSettings builds the folder file holding in's auth, headers,
// variables and description (the other members come from the folder's file when it is
// written).
func (in FolderDTO) toSettings() (*projectfile.Folder, error) {
	c := converter{id: in.ID}
	f := &projectfile.Folder{
		Head:        projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindFolder},
		ID:          in.ID,
		Name:        in.Name,
		Description: in.Description,
		Auth:        c.auth(in.Auth),
		Headers:     c.keyValues("headers", in.Headers),
		Variables:   c.variables("variables", in.Variables),
	}
	if c.err != nil {
		return nil, c.err
	}
	return f, nil
}
