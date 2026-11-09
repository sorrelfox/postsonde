package collection

import (
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

const testWorkspace = "ws_0123456789abcdef"

// TestSaveFolderServer: a folder's saved server (SRV-04) is written at
// once into its _folder.json, keeping every other member, survives a
// reload, lives beside a settings edit waiting for the auto-saver, and is
// removed with nil.
func TestSaveFolderServer(t *testing.T) {
	t.Parallel()
	svc, dir, _, advance := folderProject(t)
	ctx := t.Context()

	id, folders, unreadable, err := FolderServers(svc, ctx)
	if err != nil || id != testWorkspace || len(folders) != 0 || len(unreadable) != 0 {
		t.Fatalf("FolderServers = %q, %+v, %v, %v", id, folders, unreadable, err)
	}

	// A settings edit waits for the auto-saver while the server is saved.
	f := getFolder(t, svc, "fld_users")
	f.Headers = append(f.Headers, KeyValueDTO{Name: "X-Pending", Value: "1", Enabled: true})
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	def := &projectfile.FolderServer{Type: "websocket", Port: 8081, WebSocket: &projectfile.ServerWebSocket{Path: "/ws", Echo: true}}
	if err := SaveFolderServer(svc, ctx, testWorkspace, "fld_users", def); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, dir, "users/_folder.json")
	for _, want := range []string{`"server": {`, `"type": "websocket"`, `"port": 8081`, `"echo": true`, `"x-folder": true`, `"a.proto"`, `"X-Team"`} {
		if !strings.Contains(written, want) {
			t.Errorf("file lacks %s:\n%s", want, written)
		}
	}
	if strings.Contains(written, "X-Pending") {
		t.Error("the pending settings edit was written with the server")
	}
	advance(autosave.DefaultDelay)
	written = readFile(t, dir, "users/_folder.json")
	if !strings.Contains(written, "X-Pending") || !strings.Contains(written, `"server": {`) {
		t.Errorf("after the auto-save:\n%s", written)
	}

	// A reload (a restart reads the same file) finds it.
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_, folders, _, err = FolderServers(svc, ctx)
	if err != nil || len(folders) != 1 || folders[0].FolderID != "fld_users" || folders[0].FolderName != "Users" ||
		folders[0].Server.Type != "websocket" || folders[0].Server.WebSocket.Path != "/ws" {
		t.Fatalf("FolderServers after reload = %+v, %v", folders, err)
	}
	got, err := LoadFolderServer(svc, ctx, testWorkspace, "fld_users")
	if err != nil || got.Server == nil || got.Server.Port != 8081 {
		t.Errorf("LoadFolderServer = %+v, %v", got, err)
	}
	if got, err := LoadFolderServer(svc, ctx, testWorkspace, "fld_admins"); err != nil || got.Server != nil {
		t.Errorf("LoadFolderServer of a folder without one = %+v, %v", got, err)
	}

	// Another project, a request and a missing folder are not found.
	_, err = LoadFolderServer(svc, ctx, "ws_other", "fld_users")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	err = SaveFolderServer(svc, ctx, "ws_other", "fld_users", nil)
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	err = SaveFolderServer(svc, ctx, testWorkspace, "req_list", def)
	checkCode(t, err, errs.NotFound, core.CodeNotFound)

	if err := SaveFolderServer(svc, ctx, testWorkspace, "fld_users", nil); err != nil {
		t.Fatal(err)
	}
	if written := readFile(t, dir, "users/_folder.json"); strings.Contains(written, `"server"`) || !strings.Contains(written, "X-Pending") {
		t.Errorf("after removing the server:\n%s", written)
	}
}

// TestSaveFolderServerReadOnly: a folder from a newer version is not
// written, and an unreadable one is reported.
func TestSaveFolderServerReadOnly(t *testing.T) {
	t.Parallel()
	svc, dir, _, _ := folderProject(t)
	ctx := t.Context()
	writeFile(t, dir, "users/admins/_folder.json", `{"schemaVersion": 99, "kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "broken/_folder.json", `{"kind": "folder", "id": `)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	err := SaveFolderServer(svc, ctx, testWorkspace, "fld_admins", &projectfile.FolderServer{Type: "tcp"})
	checkCode(t, err, errs.Unsupported, core.CodeReadOnly)
	_, _, unreadable, err := FolderServers(svc, ctx)
	if err != nil || len(unreadable) != 1 {
		t.Errorf("unreadable = %v, %v", unreadable, err)
	}
}
