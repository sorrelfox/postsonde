package collection

import (
	"context"
	"slices"
	"strings"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Auth secrets (docs/auth.md). A request's typed password, token or
// API key never reaches its project file:
//
//   - UpdateRequest splits the secret off (splitSecrets) and keeps both
//     parts pending; the auto-save writes the local side file
//     (<local>/secrets/requests/<id>.json), then the project file.
//   - Request merges the local value back (mergeSecrets), from the pending
//     edit or from the side file.
//   - Rename and move keep the side file (it is keyed by ID); Duplicate
//     copies it to the copies' new IDs; Delete keeps it, so Restore brings
//     the credentials back.
//   - A folder's secret variables go to the folder's side file the same
//     way (folder.go), and every file the tree writes goes through
//     requestsecrets.Store.MoveOut (Tree.KeepSecretsLocal, set in
//     currentLocked), so a secret typed into a project file by hand leaves
//     it on the next write of that file (PRV-09).

// InheritedAuthDTO is the auth a request with auth "inherit" uses.
type InheritedAuthDTO struct {
	// Type is the auth type of the folder that supplies it, or "none" when
	// no folder above the request sets one.
	Type string `json:"type"`
	// Disabled is set when that folder's auth is disabled.
	Disabled bool `json:"disabled"`
	// FolderID and FolderName name that folder; "" when none does.
	FolderID   string `json:"folderId"`
	FolderName string `json:"folderName"`
	// Auth is that folder's auth as its file (or pending edit) has it, for
	// the Auth tab to show which fields, and which {{variables}}, the item
	// sends (AUTH-01, AUTH-32). A secret member is kept only when it is
	// {{references}}; a typed secret is "" (localSecrets says it is
	// stored on this computer), so no credential leaves the folder's own
	// editor. Type "none" when no folder sets auth.
	Auth AuthDTO `json:"auth"`
}

// InheritedAuth returns the auth request or folder id would inherit from
// the folders above it (AUTH-01, COL-03), whatever its own auth type is,
// for the Auth tab's note. It fails with not_found for an ID that is not a
// request or folder, and with
// request.auth_inherit_failed when a folder on the way could not be read.
func (s *CollectionService) InheritedAuth(ctx context.Context, id string) (InheritedAuthDTO, error) {
	if err := interrupted(ctx, "Loading the inherited auth"); err != nil {
		return InheritedAuthDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return InheritedAuthDTO{}, err
	}
	n := tree.Node(id)
	switch {
	case n == nil && isScratchLocked(ws, tree, id):
		// A scratch request (COL-06) has no folders: inherit sends nothing.
	case n == nil || (n.Kind != core.KindRequest && n.Kind != core.KindFolder):
		return InheritedAuthDTO{}, errs.New(errs.NotFound, "The item is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	auth, from, err := request.ResolveAuth(projectfile.Auth{Type: projectfile.AuthInherit}, s.authParents(tree, n))
	if err != nil {
		return InheritedAuthDTO{}, err
	}
	out := InheritedAuthDTO{Type: auth.Type, Disabled: auth.Disabled, Auth: toAuthDTO(withoutTypedSecret(auth))}
	if from != nil {
		out.FolderID, out.FolderName = from.ID, from.Name
	}
	return out, nil
}

// withoutTypedSecret returns a with each secret member "" unless that
// member is {{references}} only (requestsecrets.IsReference): a reference
// may be shown anywhere, a typed value only in its own item's editor. A
// typed value (one typed into the file by hand) is marked in LocalSecrets,
// as a value stored on this computer is, so the note says it is hidden.
func withoutTypedSecret(a projectfile.Auth) projectfile.Auth {
	for _, member := range a.SecretMembers() {
		v := a.SecretOf(member)
		if v == "" || requestsecrets.IsReference(v) {
			continue
		}
		a = a.WithSecretOf(member, "")
		if !slices.Contains(a.LocalSecrets, member) {
			a.LocalSecrets = append(slices.Clone(a.LocalSecrets), member)
		}
	}
	return a
}

// authParents returns the folders above n, closest first, as auth sources,
// with the pending edits of their settings (folderLocked); none for a nil
// n (a scratch request). The project root has no auth and ends the list.
func (s *CollectionService) authParents(tree *core.Tree, n *core.Node) []request.AuthSource {
	var out []request.AuthSource
	for p := parentOf(n); p != nil && p.Kind == core.KindFolder; p = p.Parent {
		src := request.AuthSource{ID: p.ID, Name: p.DisplayName()}
		if f, _, _ := s.folderLocked(tree, p); f != nil {
			src.Auth = f.Auth
		} else {
			src.Unreadable = true
		}
		out = append(out, src)
	}
	return out
}

// fileAuth returns the auth of a request file; nil for another type.
func fileAuth(f projectfile.File) *projectfile.Auth {
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		return &r.Auth
	case *projectfile.GraphQLRequest:
		return &r.Auth
	case *projectfile.SSERequest:
		return &r.Auth
	case *projectfile.WebSocketRequest:
		return &r.Auth
	case *projectfile.SocketIORequest:
		return &r.Auth
	case *projectfile.GRPCRequest:
		return &r.Auth
	}
	return nil
}

// splitSecrets returns f (a request or a folder's settings) as it may be
// written to the project, and the values for its local side file (nil
// when there are none): its typed auth secret and, for a folder, the
// values of its secret variables (requestsecrets.SplitFile).
func splitSecrets(f projectfile.File) (projectfile.File, map[string]string) {
	return requestsecrets.SplitFile(f)
}

// mergeSecrets returns f with the local secret values put back.
func mergeSecrets(f projectfile.File, values map[string]string) projectfile.File {
	if !requestsecrets.NeedsLocal(f) {
		return f
	}
	return requestsecrets.MergeFile(f, values)
}

// readSecretsFor reads the side file of item id only when its file marks
// an auth secret as local or has secret variables.
func readSecretsFor(localDir, id string, f projectfile.File) (map[string]string, error) {
	if !requestsecrets.NeedsLocal(f) {
		return nil, nil
	}
	return requestsecrets.New(localDir).Read(id)
}

// requestPaths maps the path of every request and folder at or below n
// (the items that can have local secrets), relative to n's own path, to
// its ID.
func requestPaths(n *core.Node) map[string]string {
	out := map[string]string{}
	var walk func(*core.Node)
	walk = func(c *core.Node) {
		if c.Kind == core.KindRequest || c.Kind == core.KindFolder {
			out[strings.TrimPrefix(c.Path, n.Path)] = c.ID
		}
		for _, child := range c.Children {
			walk(child)
		}
	}
	walk(n)
	return out
}

// copySecretsLocked copies the side files of the requests of the original
// item (before, from requestPaths) to the requests of its copy copyID. A
// failure is logged: the copy then asks for the secret again.
func (s *CollectionService) copySecretsLocked(localDir string, tree *core.Tree, before map[string]string, copyID string) {
	n := tree.Node(copyID)
	if n == nil {
		return
	}
	store := requestsecrets.New(localDir)
	for rel, newID := range requestPaths(n) {
		oldID, ok := before[rel]
		if !ok {
			continue
		}
		if err := store.Copy(oldID, newID); err != nil {
			s.logger.Warn("the local secrets of a duplicated request were not copied", "from", oldID, "to", newID, "err", err)
		}
	}
}
