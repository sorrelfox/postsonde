package collection

import (
	"context"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// CodeNotHTTP: the request to send is not an HTTP, GraphQL or SSE request (kind
// invalid_argument). Details: id, kind.
const CodeNotHTTP = "collection.not_http"

// HTTPSource is everything sending an HTTP request needs from the project
// (HTTP-20): the request as the editor has it, the auth it sends, the
// variables of its folders and the workspace it belongs to. A GraphQL
// request (GQL-02) is sent over HTTP too: GraphQL is set instead of
// Request. So is an SSE request (SSE-01): Request holds it as the HTTP
// request it is sent as, and SSE is set.
type HTTPSource struct {
	// Request is the request file, with the edits not written yet and its
	// local auth secret merged in. It is empty when GraphQL is set.
	Request projectfile.HTTPRequest
	// GraphQL is the GraphQL request file, likewise, when the request is
	// one; nil for an HTTP request.
	GraphQL *projectfile.GraphQLRequest
	// SSE is set when the request is an SSE request, read as an event
	// stream; Request then holds it (projectfile.SSERequest.HTTP).
	SSE bool
	// Auth is the auth the request sends: its own, or the closest folder's
	// for "inherit" (AUTH-01), with that item's local secret merged in.
	Auth projectfile.Auth
	// AuthFolderName names the folder Auth is inherited from; "" for the
	// request's own.
	AuthFolderName string
	// FolderVariables are the variables of the folders above the request,
	// the top-level folder first, secret ones with their local value
	// ("" when this computer has none).
	FolderVariables [][]projectfile.Variable
	// Folders names the folder of each FolderVariables entry (same
	// length), for the scope labels; see VariablesSource.
	Folders []FolderScope
	// Globals are the project's global variables; see VariablesSource.
	Globals []projectfile.Variable
	// FolderHeaders are the header rows of the folders above the request,
	// the top-level folder first (COL-03), for request.HTTPOptions.
	FolderHeaders []request.FolderHeaders
	// Handshake is the kind of a websocket, socketio or grpc request
	// loaded by LoadAuthSource for its auth only (AUTH-31): Request then
	// holds just its URL (for grpc, its address), and it cannot be sent
	// over HTTP. "" for an HTTP, GraphQL or SSE request.
	Handshake projectfile.Kind
	// Workspace is the open workspace the request is in.
	Workspace *coreworkspace.Workspace
}

// LoadHTTPSource returns what sending the HTTP, GraphQL or SSE request id needs, pending
// edits included, so a send right after an edit sends that edit. It is not
// a method, so it is not bound: the HTTP service calls it.
//
// It fails with collection.not_found for an ID that is not a request of the
// open project, collection.not_http for a request of another type,
// request.auth_inherit_failed when a folder whose auth is inherited could
// not be read, and requestsecrets.local_data_failed when a local secret
// could not be read.
func LoadHTTPSource(s *CollectionService, ctx context.Context, id string) (HTTPSource, error) {
	return loadSource(s, ctx, id, false)
}

// LoadAuthSource is LoadHTTPSource for what needs only the auth of a
// request and its variables: the Auth tab's tools (the JWT preview, the
// netrc check, the OAuth 2.0 token) and the handshakes of websocket,
// socketio and grpc requests (AUTH-31). It takes those kinds too: their
// source has Handshake set to the kind, Request.URL to the request's
// URL (a grpc request's address) and Request.Assertions to its assertions
// (TEST-08), with its auth, own or inherited, like any other. Like LoadHTTPSource it is not bound.
func LoadAuthSource(s *CollectionService, ctx context.Context, id string) (HTTPSource, error) {
	return loadSource(s, ctx, id, true)
}

func loadSource(s *CollectionService, ctx context.Context, id string, handshake bool) (HTTPSource, error) {
	if err := interrupted(ctx, "Loading the request"); err != nil {
		return HTTPSource{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return HTTPSource{}, err
	}
	n := tree.Node(id)
	var file projectfile.File
	switch {
	case n != nil && n.Kind == core.KindRequest && n.File != nil:
		file = n.File
	case n == nil && isScratchLocked(ws, tree, id):
		// A scratch request (COL-06): no folders above it, so only the
		// project's variables and no inherited auth.
		it, err := scratchOf(ws).Get(id)
		if err != nil {
			return HTTPSource{}, err
		}
		file = it.File
	default:
		return HTTPSource{}, errs.New(errs.NotFound, "The request is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	var secrets map[string]string
	if p, ok := s.saver.Get(saveKey{tree, id}); ok {
		file, secrets = p.file, p.secrets
	} else if secrets, err = readSecretsFor(ws.LocalDir, id, file); err != nil {
		return HTTPSource{}, err
	}
	file = mergeSecrets(file, secrets)
	var out HTTPSource
	var own projectfile.Auth
	switch r := file.(type) {
	case *projectfile.HTTPRequest:
		out.Request, own = *r, r.Auth
	case *projectfile.GraphQLRequest:
		c := *r
		out.GraphQL, own = &c, r.Auth
	case *projectfile.SSERequest:
		out.Request, out.SSE, own = r.HTTP(), true, r.Auth
	case *projectfile.WebSocketRequest:
		if !handshake {
			return HTTPSource{}, notHTTP(id, file)
		}
		out.Request, out.Handshake, own = projectfile.HTTPRequest{ID: r.ID, Name: r.Name, URL: r.URL, Assertions: r.Assertions}, projectfile.KindWebSocket, projectfile.HandshakeAuthOf(r.Auth)
	case *projectfile.SocketIORequest:
		if !handshake {
			return HTTPSource{}, notHTTP(id, file)
		}
		out.Request, out.Handshake, own = projectfile.HTTPRequest{ID: r.ID, Name: r.Name, URL: r.URL, Assertions: r.Assertions}, projectfile.KindSocketIO, projectfile.HandshakeAuthOf(r.Auth)
	case *projectfile.GRPCRequest:
		if !handshake {
			return HTTPSource{}, notHTTP(id, file)
		}
		out.Request, out.Handshake, own = projectfile.HTTPRequest{ID: r.ID, Name: r.Name, URL: r.Address, Assertions: r.Assertions}, projectfile.KindGRPC, projectfile.HandshakeAuthOf(r.Auth)
	default:
		return HTTPSource{}, notHTTP(id, file)
	}

	auth, from, err := request.ResolveAuth(own, s.authParents(tree, n))
	if err != nil {
		return HTTPSource{}, err
	}
	if from != nil && auth.SecretIsLocal() {
		values, err := s.folderAuthSecrets(ws.LocalDir, tree, from.ID)
		if err != nil {
			return HTTPSource{}, err
		}
		auth = requestsecrets.Merge(auth, values)
	}
	if from != nil {
		out.AuthFolderName = from.Name
	}

	vars, folders, err := s.folderVariables(ws.LocalDir, tree, parentOf(n))
	if err != nil {
		return HTTPSource{}, err
	}
	globals, err := globalVariablesLocked(ws.LocalDir, tree)
	if err != nil {
		return HTTPSource{}, err
	}
	out.Auth, out.FolderVariables, out.Folders, out.Globals, out.Workspace = auth, vars, folders, globals, ws
	out.FolderHeaders = s.folderHeaders(tree, n)
	return out, nil
}

func notHTTP(id string, file projectfile.File) error {
	return errs.New(errs.InvalidArgument, "Only HTTP, GraphQL and SSE requests can be sent this way.").
		WithCode(CodeNotHTTP).
		WithDetail("id", id).
		WithDetail("kind", string(file.FileKind()))
}

// folderVariables is the variables of folder node n and the folders above
// it, the top-level folder first, pending edits included (nil for a nil
// n or the root), and which folder each entry comes from. Folders without
// variables, and those whose _folder.json did not load, have no entry.
// Secret variables carry their local value (from the pending edit or the
// folder's side file), "" when this computer has none; it fails with
// requestsecrets.local_data_failed when a side file cannot be read.
func (s *CollectionService) folderVariables(localDir string, tree *core.Tree, n *core.Node) ([][]projectfile.Variable, []FolderScope, error) {
	var folders [][]projectfile.Variable
	var scopes []FolderScope
	for p := n; p != nil && p.Kind == core.KindFolder; p = p.Parent {
		f, secrets, pending := s.folderLocked(tree, p)
		if f == nil || len(f.Variables) == 0 {
			continue
		}
		vars := f.Variables
		if requestsecrets.HasSecretVariables(vars) {
			if !pending {
				var err error
				if secrets, err = requestsecrets.New(localDir).Read(p.ID); err != nil {
					return nil, nil, err
				}
			}
			vars = requestsecrets.MergeVariables(vars, secrets)
		}
		folders = append([][]projectfile.Variable{vars}, folders...)
		scope := FolderScope{ID: p.ID, Name: p.DisplayName(), Collection: p.Parent != nil && p.Parent.Kind == core.KindRoot}
		scopes = append([]FolderScope{scope}, scopes...)
	}
	return folders, scopes, nil
}

// FolderScope names the folder a scope of folder variables comes from.
type FolderScope struct {
	ID   string
	Name string
	// Collection is set for a top-level folder: its variables are the
	// collection variables (ENV-03), below those of the folders inside
	// it (ENV-04).
	Collection bool
}

// VariablesSource is what rendering the {{variables}} of a request needs,
// in the fixed precedence of ENV-06 (docs/templates.md#scopes-and-precedence):
// global < environment < collection < folder < runtime. The global
// variables and the workspace (whose active environment is read on every
// render) come first, then the variables of its folders from the top down:
// the top-level folder's are the collection variables (ENV-03), the
// deeper folders' the folder variables (ENV-04), the nearest one last.
type VariablesSource struct {
	// Globals are the project's global variables (postsonde.json, ENV-02),
	// secret ones with their local value. nil means "not read": the
	// renderer then takes them from Workspace.File (a source built by
	// hand, as tests do); the loaders always set it.
	Globals []projectfile.Variable
	// FolderVariables are the variables of the folders above the request,
	// the top-level folder first, secret ones with their local value
	// ("" when this computer has none).
	FolderVariables [][]projectfile.Variable
	// Folders names the folder of each FolderVariables entry (same
	// length), for the scope labels "Collection: <name>" and
	// "Folder: <name>". Without it every entry is labelled "Folder".
	Folders []FolderScope
	// Workspace is the open workspace the request is in.
	Workspace *coreworkspace.Workspace
}

// LoadVariablesSource returns the variable scopes of request id, of any
// kind (a websocket request's URL, headers and messages, WS-11, and the
// editor's chips of every kind), or of folder id: its own variables come
// last, as for the requests inside it (the folder editor's chips, COL-03).
// Pending edits of the folders are included. Like LoadHTTPSource it is not
// a method, so it is not bound. It fails with collection.not_found for an
// ID that is not a request or folder of the open project.
func LoadVariablesSource(s *CollectionService, ctx context.Context, id string) (VariablesSource, error) {
	if err := interrupted(ctx, "Loading the variables"); err != nil {
		return VariablesSource{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return VariablesSource{}, err
	}
	n := tree.Node(id)
	switch {
	case n != nil && n.Kind == core.KindRequest:
		n = n.Parent
	case n != nil && n.Kind == core.KindFolder:
	case n == nil && isScratchLocked(ws, tree, id):
		// A scratch request: no folders above it.
	default:
		return VariablesSource{}, errs.New(errs.NotFound, "The request is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	vars, folders, err := s.folderVariables(ws.LocalDir, tree, n)
	if err != nil {
		return VariablesSource{}, err
	}
	globals, err := globalVariablesLocked(ws.LocalDir, tree)
	if err != nil {
		return VariablesSource{}, err
	}
	return VariablesSource{Globals: globals, FolderVariables: vars, Folders: folders, Workspace: ws}, nil
}
