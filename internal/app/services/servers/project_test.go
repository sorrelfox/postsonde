package servers

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

const (
	projectA = "ws_aaaaaaaaaaaaaaaa"
	projectB = "ws_bbbbbbbbbbbbbbbb"
)

// mocksFolder is a folder with a saved mock server on any free port.
const mocksFolder = `{"schemaVersion": 1, "kind": "folder", "id": "fld_mocks", "name": "Mocks", "auth": {"type": "inherit"}, "x-kept": 1,
  "server": {"type": "mock_http", "name": "Users mock", "mock": {"routes": [
    {"id": "rt_users", "method": "GET", "path": "/users/:id", "status": 200, "body": {"mode": "raw", "type": "json", "text": "{\"saved\":true}"}}
  ]}}}`

type projectEnv struct {
	t         *testing.T
	manager   *coreworkspace.Manager
	dataDir   string
	dirA      string
	dirB      string
	collect   *collection.CollectionService
	svc       *ServersService
	recording *recorder
}

func writeProjectFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readProjectFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newProjectEnv makes two projects, A (a mocks folder with a saved mock
// server, an empty chat folder) and B (no servers), opens A, and starts a
// servers service wired to a collection service.
func newProjectEnv(t *testing.T) *projectEnv {
	t.Helper()
	root := t.TempDir()
	e := &projectEnv{t: t, dataDir: filepath.Join(root, "data"), dirA: filepath.Join(root, "a"), dirB: filepath.Join(root, "b")}
	writeProjectFile(t, e.dirA, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "`+projectA+`", "name": "A"}`)
	writeProjectFile(t, e.dirA, "mocks/_folder.json", mocksFolder)
	writeProjectFile(t, e.dirA, "chat/_folder.json", `{"schemaVersion": 1, "kind": "folder", "id": "fld_chat", "name": "Chat", "auth": {"type": "inherit"}}`)
	writeProjectFile(t, e.dirB, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "`+projectB+`", "name": "B"}`)
	e.restart()
	e.open(e.dirA)
	return e
}

// restart is a new app run over the same data directory and projects.
func (e *projectEnv) restart() {
	t := e.t
	if e.svc != nil {
		_ = e.svc.ServiceShutdown()
	}
	e.manager = coreworkspace.New(e.dataDir)
	e.collect = collection.New(e.manager, func(string, any) {}, slog.New(slog.DiscardHandler))
	e.recording = &recorder{}
	e.svc = newService(e.recording.emit, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second}, WithProject(CollectionProject(e.collect)))
	svc := e.svc
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
}

func (e *projectEnv) open(dir string) {
	if _, err := e.manager.Open(dir); err != nil {
		e.t.Fatal(err)
	}
}

func (e *projectEnv) sync() ProjectServersDTO {
	e.t.Helper()
	res, err := e.svc.SyncProject(e.t.Context())
	if err != nil {
		e.t.Fatalf("SyncProject: %v", err)
	}
	return res
}

func (e *projectEnv) list() []ServerDTO {
	e.t.Helper()
	list, err := e.svc.List(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func (e *projectEnv) folderServer(folderID string) (ServerDTO, bool) {
	for _, s := range e.list() {
		if s.Folder != nil && s.Folder.FolderID == folderID {
			return s, true
		}
	}
	return ServerDTO{}, false
}

// TestSavedServerStartsFromFile: opening a project defines its saved
// servers, stopped; one starts from the saved config and serves its
// routes; edits are written to the folder's file; after a restart the
// same definition comes back.
func TestSavedServerStartsFromFile(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	ctx := t.Context()

	res := e.sync()
	if res.WorkspaceID != projectA || len(res.Problems) != 0 {
		t.Fatalf("SyncProject = %+v", res)
	}
	srv, ok := e.folderServer("fld_mocks")
	if !ok || srv.State != "stopped" || srv.Name != "Users mock" || srv.Folder.WorkspaceID != projectA || srv.Folder.FolderName != "Mocks" {
		t.Fatalf("saved server = %+v", srv)
	}
	if len(e.list()) != 1 {
		t.Errorf("servers = %+v, want only the saved one", e.list())
	}
	// A second sync changes nothing.
	e.sync()
	if again, _ := e.folderServer("fld_mocks"); again.ID != srv.ID || len(e.list()) != 1 {
		t.Errorf("second sync: %+v", e.list())
	}

	running, err := e.svc.Start(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get(t, running.BoundPort, "/users/1"); status != 200 || body != `{"saved":true}` {
		t.Errorf("GET /users/1 = %d %s", status, body)
	}

	// A route edit applies at once and is saved.
	cfg := *running.Config.MockHTTP
	cfg.Routes = append([]MockRouteDTO(nil), cfg.Routes...)
	cfg.Routes[0].Body.Text = `{"edited":true}`
	if _, err := e.svc.UpdateConfig(ctx, srv.ID, ConfigDTO{MockHTTP: &cfg}); err != nil {
		t.Fatal(err)
	}
	if status, body := get(t, running.BoundPort, "/users/1"); status != 200 || body != `{"edited":true}` {
		t.Errorf("GET /users/1 after the edit = %d %s", status, body)
	}
	file := readProjectFile(t, e.dirA, "mocks/_folder.json")
	if !strings.Contains(file, `{\"edited\":true}`) || !strings.Contains(file, `"x-kept": 1`) || !strings.Contains(file, `"name": "Users mock"`) {
		t.Errorf("file after the edit:\n%s", file)
	}

	// The address cannot change while it runs; stopped, it can.
	if _, err := e.svc.UpdateServer(ctx, srv.ID, ServerSettingsDTO{Name: "Users mock", Port: 18123}); codeOf(err) != core.CodeActive {
		t.Errorf("UpdateServer while running = %v, want %s", err, core.CodeActive)
	}
	if _, err := e.svc.Stop(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := e.svc.UpdateServer(ctx, srv.ID, ServerSettingsDTO{Name: "", Host: "localhost", Port: 18123})
	if err != nil || updated.Port != 18123 || updated.Name != "Mock HTTP" {
		t.Fatalf("UpdateServer = %+v, %v", updated, err)
	}
	file = readProjectFile(t, e.dirA, "mocks/_folder.json")
	if !strings.Contains(file, `"port": 18123`) || !strings.Contains(file, `"host": "localhost"`) || strings.Contains(file, `"name": "`+"Mock HTTP") {
		t.Errorf("file after UpdateServer:\n%s", file)
	}

	// A new app run: nothing listens until the user starts it, and the
	// definition is the one saved.
	e.restart()
	e.open(e.dirA)
	if len(e.list()) != 0 {
		t.Fatal("servers before the project's sync")
	}
	e.sync()
	back, ok := e.folderServer("fld_mocks")
	if !ok || back.State != "stopped" || back.Port != 18123 || back.Host != "localhost" ||
		back.Config.MockHTTP == nil || back.Config.MockHTTP.Routes[0].Body.Text != `{"edited":true}` {
		t.Errorf("after a restart = %+v", back)
	}
}

// TestSavedServerHandEdit: Start reads the folder file again, so a hand
// edit made after the project was loaded is what starts.
func TestSavedServerHandEdit(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	ctx := t.Context()
	e.sync()
	srv, _ := e.folderServer("fld_mocks")
	writeProjectFile(t, e.dirA, "mocks/_folder.json", strings.Replace(mocksFolder, `{\"saved\":true}`, `{\"hand\":true}`, 1))
	if _, err := e.collect.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	running, err := e.svc.Start(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get(t, running.BoundPort, "/users/1"); status != 200 || body != `{"hand":true}` {
		t.Errorf("GET /users/1 = %d %s", status, body)
	}
}

// TestCreateAndRemoveFolderServer: a server defined on a folder is saved
// in its _folder.json; a folder has at most one; removing it removes the
// definition.
func TestCreateAndRemoveFolderServer(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	ctx := t.Context()
	e.sync()

	in := FolderServerInputDTO{WorkspaceID: projectA, FolderID: "fld_chat", Server: CreateInputDTO{
		Type: wsserver.TypeName, Port: 0,
		Config: ConfigDTO{WebSocket: &WebSocketConfigDTO{Path: "/ws", Subprotocols: []string{"chat"}, Echo: true,
			Rules: []WSRuleDTO{{Enabled: true, Match: "contains", Pattern: "ping", Reply: "pong"}}}},
	}}
	created, err := e.svc.CreateFolderServer(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "stopped" || created.Folder == nil || created.Folder.FolderID != "fld_chat" || created.Folder.FolderName != "Chat" {
		t.Errorf("created = %+v", created)
	}
	file := readProjectFile(t, e.dirA, "chat/_folder.json")
	for _, want := range []string{`"type": "websocket"`, `"path": "/ws"`, `"echo": true`, `"chat"`, `"match": "contains"`, `"reply": "pong"`} {
		if !strings.Contains(file, want) {
			t.Errorf("file lacks %s:\n%s", want, file)
		}
	}
	if strings.Contains(file, `"name"`+": \"WebSocket\"") {
		t.Errorf("the default name was written:\n%s", file)
	}
	if _, err := e.svc.CreateFolderServer(ctx, in); codeOf(err) != CodeFolderHasServer {
		t.Errorf("second CreateFolderServer = %v", err)
	}
	in.WorkspaceID = projectB
	if _, err := e.svc.CreateFolderServer(ctx, in); err == nil {
		t.Error("CreateFolderServer in a project that is not open succeeded")
	}
	bad := FolderServerInputDTO{WorkspaceID: projectA, FolderID: "fld_mocks", Server: CreateInputDTO{Type: "tcp", Port: 70000}}
	if _, err := e.svc.CreateFolderServer(ctx, bad); err == nil {
		t.Error("CreateFolderServer accepted a bad port")
	}

	if err := e.svc.Remove(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if file := readProjectFile(t, e.dirA, "chat/_folder.json"); strings.Contains(file, `"server"`) {
		t.Errorf("file after Remove:\n%s", file)
	}
	if _, ok := e.folderServer("fld_chat"); ok {
		t.Error("the server is still listed")
	}
}

// TestSavedServerProjectSwitch: switching projects removes the stopped
// saved servers of the old project and keeps a running one running (it
// cannot be edited then), until it stops; reopening the project brings
// them back from its files.
func TestSavedServerProjectSwitch(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	ctx := t.Context()
	e.sync()
	mock, _ := e.folderServer("fld_mocks")
	chat, err := e.svc.CreateFolderServer(ctx, FolderServerInputDTO{WorkspaceID: projectA, FolderID: "fld_chat", Server: CreateInputDTO{Type: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	adhoc, err := e.svc.Create(ctx, CreateInputDTO{Type: "echo_http"})
	if err != nil {
		t.Fatal(err)
	}
	running, err := e.svc.Start(ctx, mock.ID)
	if err != nil {
		t.Fatal(err)
	}

	e.open(e.dirB)
	if res := e.sync(); res.WorkspaceID != projectB {
		t.Errorf("SyncProject = %+v", res)
	}
	ids := map[string]string{}
	for _, s := range e.list() {
		ids[s.ID] = s.State
	}
	if ids[mock.ID] != "running" || ids[adhoc.ID] != "stopped" || ids[chat.ID] != "" || len(ids) != 2 {
		t.Errorf("servers after the switch = %v", ids)
	}
	if status, _ := get(t, running.BoundPort, "/users/1"); status != 200 {
		t.Errorf("the running server stopped serving: %d", status)
	}
	// Its project is not open: it cannot be changed, only stopped.
	if _, err := e.svc.UpdateConfig(ctx, mock.ID, ConfigDTO{MockHTTP: running.Config.MockHTTP}); codeOf(err) != codeFolderGone {
		t.Errorf("UpdateConfig of another project's server = %v", err)
	}
	if _, err := e.svc.Stop(ctx, mock.ID); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if _, err := e.svc.manager.Get(mock.ID); err == nil {
		t.Error("the stopped server of the closed project is still listed")
	}

	e.open(e.dirA)
	e.sync()
	if _, ok := e.folderServer("fld_mocks"); !ok {
		t.Error("the mock server did not come back")
	}
	if s, ok := e.folderServer("fld_chat"); !ok || s.Type != "tcp" {
		t.Errorf("the TCP server did not come back: %+v", s)
	}
}

// TestSavedServerProblems: a saved server the app cannot run is reported
// and not defined; its file is left alone.
func TestSavedServerProblems(t *testing.T) {
	t.Parallel()
	e := newProjectEnv(t)
	writeProjectFile(t, e.dirA, "chat/_folder.json", `{"schemaVersion": 1, "kind": "folder", "id": "fld_chat", "name": "Chat", "auth": {"type": "inherit"}, "server": {"type": "mqtt", "port": 9}}`)
	res := e.sync()
	if len(res.Problems) != 1 || res.Problems[0].FolderID != "fld_chat" || res.Problems[0].Error.Code != core.CodeUnknownType {
		t.Errorf("problems = %+v", res.Problems)
	}
	if _, ok := e.folderServer("fld_chat"); ok {
		t.Error("an unknown type was defined")
	}
	if _, ok := e.folderServer("fld_mocks"); !ok {
		t.Error("the valid server is missing")
	}
	if !strings.Contains(readProjectFile(t, e.dirA, "chat/_folder.json"), `"mqtt"`) {
		t.Error("the file was changed")
	}
}
