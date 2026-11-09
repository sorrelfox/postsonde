package collection

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// folderProject adds nested folders with settings to the small project:
// users (header, variable, bearer auth with a local token) > admins
// (header, variable) > list (a request).
func folderProject(t *testing.T) (*CollectionService, string, *saveEvents, func(time.Duration)) {
	t.Helper()
	svc, manager, dir, clock, ev := newAutosaveService(t)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "order": ["admins"], "headers": [{"name": "X-Team", "value": "core"}, {"name": "Accept", "value": "application/json"}], "variables": [{"name": "a", "value": "1"}, {"name": "hidden", "value": "", "secret": true, "x-custom": 1}], "auth": {"type": "bearer", "token": "", "localSecrets": ["token"]}, "grpc": {"proto": {"files": ["a.proto"]}}, "x-folder": true}`)
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "headers": [{"name": "Accept", "value": "application/xml"}], "variables": [{"name": "b", "value": "2"}], "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admins/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "/admins", "headers": [{"name": "X-Team", "value": "request"}], "auth": {"type": "inherit"}}`)
	if err := requestsecrets.New(manager.Current().LocalDir).Write("fld_users", map[string]string{"auth.token": "folder-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc, dir, ev, clock.Advance
}

func getFolder(t *testing.T, svc *CollectionService, id string) FolderDTO {
	t.Helper()
	f, err := svc.Folder(t.Context(), id)
	if err != nil {
		t.Fatalf("Folder(%s) = %v", id, err)
	}
	return f
}

func TestFolder(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := folderProject(t)
	f := getFolder(t, svc, "fld_users")
	if f.ID != "fld_users" || f.Kind != "folder" || f.Name != "Users" {
		t.Errorf("folder = %+v", f)
	}
	if f.Auth.Type != "bearer" || f.Auth.Token != "folder-secret" {
		t.Errorf("auth = %+v, want the local token merged in", f.Auth)
	}
	if len(f.Headers) != 2 || f.Headers[0] != (KeyValueDTO{Name: "X-Team", Value: "core", Enabled: true}) {
		t.Errorf("headers = %+v", f.Headers)
	}
	if len(f.Variables) != 2 || f.Variables[1].Name != "hidden" || !strings.Contains(f.Variables[1].Extra, `"secret":true`) {
		t.Errorf("variables = %+v", f.Variables)
	}

	// Lists are never null.
	empty := getFolder(t, svc, "fld_admins")
	data, _ := json.Marshal(getFolder(t, svc, "fld_admins"))
	if strings.Contains(string(data), `"headers":null`) || strings.Contains(string(data), `"variables":null`) || empty.Auth.Type != "inherit" {
		t.Errorf("folder = %s", data)
	}

	_, err := svc.Folder(t.Context(), "req_list")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
}

// TestUpdateFolderAutoSaves: the edit is used at once and written after the
// save delay over the folder's file, keeping its other members; the typed
// secret goes to the side file.
func TestUpdateFolderAutoSaves(t *testing.T) {
	t.Parallel()
	svc, dir, ev, advance := folderProject(t)
	ctx := t.Context()
	before := readFile(t, dir, "users/_folder.json")

	f := getFolder(t, svc, "fld_users")
	f.Name = "Ignored"
	f.Headers = append(f.Headers, KeyValueDTO{Name: "X-New", Value: "{{a}}", Enabled: true})
	f.Variables[0].Value = "one"
	f.Auth.Token = "typed-token"
	f.Auth.LocalSecrets = nil
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f, Revision: 4}); err != nil {
		t.Fatal(err)
	}
	if got := getFolder(t, svc, "fld_users"); len(got.Headers) != 3 || got.Auth.Token != "typed-token" || got.Name != "Users" {
		t.Errorf("pending folder = %+v", got)
	}
	if readFile(t, dir, "users/_folder.json") != before {
		t.Fatal("written before the delay")
	}
	advance(autosave.DefaultDelay)

	written := readFile(t, dir, "users/_folder.json")
	for _, want := range []string{`"name": "Users"`, `"X-New"`, `"value": "one"`, `"secret": true`, `"x-custom": 1`, `"x-folder": true`, `"a.proto"`, `"admins"`} {
		if !strings.Contains(written, want) {
			t.Errorf("file lacks %s:\n%s", want, written)
		}
	}
	if strings.Contains(written, "typed-token") || !strings.Contains(written, `"localSecrets"`) {
		t.Errorf("the typed token is in the project file:\n%s", written)
	}
	if side, err := os.ReadFile(secretsFile(t, svc, "fld_users")); err != nil || !strings.Contains(string(side), "typed-token") {
		t.Error("the typed token is not in the folder's side file")
	}
	ev.mu.Lock()
	saved := append([]SavedEventDTO(nil), ev.saved...)
	ev.mu.Unlock()
	if len(saved) != 1 || saved[0].ID != "fld_users" || saved[0].Revision != 4 {
		t.Errorf("saved events = %+v", saved)
	}

	// The read after the write merges the local token back.
	if got := getFolder(t, svc, "fld_users"); got.Auth.Token != "typed-token" {
		t.Errorf("auth after the write = %+v", got.Auth)
	}
}

// TestFolderSecretVariables: a secret folder variable's value goes to the
// folder's side file, never to _folder.json; the folder editor gets it
// back, the send-time sources get it, the inherited lists do not.
func TestFolderSecretVariables(t *testing.T) {
	t.Parallel()
	svc, dir, _, advance := folderProject(t)
	ctx := t.Context()

	f := getFolder(t, svc, "fld_users")
	f.Variables[1].Value = "folder-var-s3cret"
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	// Pending: already used, not yet written.
	if got := getFolder(t, svc, "fld_users"); got.Variables[1].Value != "folder-var-s3cret" {
		t.Errorf("pending variables = %+v", got.Variables)
	}
	src, err := LoadVariablesSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.FolderVariables) != 2 || src.FolderVariables[0][1].Value != "folder-var-s3cret" || !src.FolderVariables[0][1].Secret {
		t.Errorf("pending sources = %+v", src.FolderVariables)
	}
	advance(autosave.DefaultDelay)

	written := readFile(t, dir, "users/_folder.json")
	if strings.Contains(written, "folder-var-s3cret") || !strings.Contains(written, `"secret": true`) {
		t.Errorf("_folder.json =\n%s", written)
	}
	side, err := requestsecrets.New(svc.manager.Current().LocalDir).Read("fld_users")
	if err != nil || side["var.hidden"] != "folder-var-s3cret" || side["auth.token"] != "folder-secret" {
		t.Errorf("side file = %v, %v", side, err)
	}

	// Written: read back from the side file.
	if got := getFolder(t, svc, "fld_users"); got.Variables[1].Value != "folder-var-s3cret" || got.Auth.Token != "folder-secret" {
		t.Errorf("folder = %+v", got)
	}
	src, err = LoadVariablesSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if src.FolderVariables[0][1].Value != "folder-var-s3cret" {
		t.Errorf("sources = %+v", src.FolderVariables)
	}
	hs, err := LoadHTTPSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if hs.FolderVariables[0][1].Value != "folder-var-s3cret" {
		t.Errorf("http sources = %+v", hs.FolderVariables)
	}

	// The read-only lists of the subfolder do not show the value.
	inh, err := svc.InheritedSettings(ctx, "fld_admins")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(inh); strings.Contains(string(data), "folder-var-s3cret") {
		t.Errorf("inherited settings show the secret: %s", data)
	}

	// Making it a plain variable again writes the value to the project.
	f = getFolder(t, svc, "fld_users")
	f.Variables[1].Extra = `{"x-custom":1}`
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f, Revision: 2}); err != nil {
		t.Fatal(err)
	}
	advance(autosave.DefaultDelay)
	if written := readFile(t, dir, "users/_folder.json"); !strings.Contains(written, "folder-var-s3cret") {
		t.Errorf("the plain value is not in _folder.json:\n%s", written)
	}
	if side, _ := requestsecrets.New(svc.manager.Current().LocalDir).Read("fld_users"); side["var.hidden"] != "" {
		t.Errorf("side file keeps the value: %v", side)
	}
}

func TestUpdateFolderRefusals(t *testing.T) {
	t.Parallel()
	svc, dir, _, _ := folderProject(t)
	writeFile(t, dir, "broken/_folder.json", `{"kind": "folder", "id": `)
	ctx := t.Context()
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	_, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: FolderDTO{ID: "req_list", Kind: "folder"}})
	checkCode(t, err, errs.InvalidArgument, core.CodeWrongNode)
	_, err = svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: FolderDTO{ID: "fld_missing", Kind: "folder"}})
	checkCode(t, err, errs.NotFound, core.CodeNotFound)

	f := getFolder(t, svc, "fld_admins")
	f.Headers = []KeyValueDTO{{Name: "A", Enabled: true, Extra: "[1]"}}
	_, err = svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidRequest)

	var broken string
	tree, err := svc.Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tree.Nodes {
		if n.Path == "broken" {
			broken = n.ID
		}
	}
	if broken == "" {
		t.Fatal("no broken folder in the tree")
	}
	_, err = svc.Folder(ctx, broken)
	checkCode(t, err, errs.Unsupported, core.CodeReadOnly)
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: FolderDTO{ID: broken, Kind: "folder"}}); err == nil {
		t.Error("the unreadable folder was overwritten")
	}
}

// TestFolderSettingsAtSendTime: a request inherits its folders' headers
// (nearer folder first, its own rows win in request.BuildHTTP), variables
// and auth, pending folder edits included.
func TestFolderSettingsAtSendTime(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := folderProject(t)
	ctx := t.Context()

	src, err := LoadHTTPSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.FolderHeaders) != 2 || src.FolderHeaders[0].FolderID != "fld_users" || src.FolderHeaders[1].FolderID != "fld_admins" ||
		src.FolderHeaders[0].FolderName != "Users" || src.FolderHeaders[1].Rows[0].Value != "application/xml" {
		t.Errorf("folder headers = %+v", src.FolderHeaders)
	}
	if src.Auth.Token != "folder-secret" {
		t.Errorf("auth = %+v", src.Auth)
	}

	// Pending edits of a folder are what the next send uses.
	f := getFolder(t, svc, "fld_admins")
	f.Headers = []KeyValueDTO{{Name: "X-Pending", Value: "yes", Enabled: true}}
	f.Variables = []KeyValueDTO{{Name: "b", Value: "pending", Enabled: true}}
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: f, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	u := getFolder(t, svc, "fld_users")
	u.Auth.Token = "pending-token"
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: u, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	src, err = LoadHTTPSource(svc, ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.FolderHeaders) != 2 || src.FolderHeaders[1].Rows[0].Name != "X-Pending" {
		t.Errorf("pending folder headers = %+v", src.FolderHeaders)
	}
	if len(src.FolderVariables) != 2 || src.FolderVariables[1][0].Value != "pending" {
		t.Errorf("pending folder variables = %+v", src.FolderVariables)
	}
	if src.Auth.Token != "pending-token" {
		t.Errorf("pending auth = %+v", src.Auth)
	}
}

func TestInheritedSettings(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := folderProject(t)
	ctx := t.Context()

	got, err := svc.InheritedSettings(ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 2 || got.Folders[0].FolderID != "fld_users" || got.Folders[1].FolderName != "Admins" ||
		len(got.Folders[0].Headers) != 2 || got.Folders[1].Variables[0].Name != "b" {
		t.Errorf("InheritedSettings(req_list) = %+v", got)
	}
	got, err = svc.InheritedSettings(ctx, "fld_admins")
	if err != nil || len(got.Folders) != 1 || got.Folders[0].FolderID != "fld_users" {
		t.Errorf("InheritedSettings(fld_admins) = %+v, %v", got, err)
	}
	got, err = svc.InheritedSettings(ctx, "req_health")
	if err != nil || got.Folders == nil || len(got.Folders) != 0 {
		t.Errorf("InheritedSettings(req_health) = %+v, %v", got, err)
	}
	_, err = svc.InheritedSettings(ctx, "fld_missing")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
}

// TestFolderSecretsFollowDuplicate: a duplicated folder's copy gets the
// folder's local secret too.
func TestFolderSecretsFollowDuplicate(t *testing.T) {
	t.Parallel()
	svc, dir, _, _ := folderProject(t)
	writeFile(t, dir, "team/_folder.json", `{"kind": "folder", "id": "fld_team", "name": "Team", "auth": {"type": "bearer", "token": "", "localSecrets": ["token"]}}`)
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := requestsecrets.New(svc.manager.Current().LocalDir).Write("fld_team", map[string]string{"auth.token": "folder-secret"}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Duplicate(t.Context(), "fld_team")
	if err != nil {
		t.Fatal(err)
	}
	if got := getFolder(t, svc, res.NodeID); got.Auth.Token != "folder-secret" {
		t.Errorf("copy's auth = %+v", got.Auth)
	}
}

func TestFolderVariablesRoundTrip(t *testing.T) {
	t.Parallel()
	vars := []projectfile.Variable{
		{Name: "a", Value: "1", Description: "d"},
		{Name: "s", Secret: true, Disabled: true, Unknown: projectfile.Unknown{"x": json.RawMessage("2")}},
	}
	data, err := json.Marshal(toVariableDTOs(vars))
	if err != nil {
		t.Fatal(err)
	}
	var rows []KeyValueDTO
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	c := converter{id: "fld"}
	back := c.variables("variables", rows)
	if c.err != nil || len(back) != 2 || back[0].Description != "d" || !back[1].Secret || !back[1].Disabled ||
		string(back[1].Unknown["x"]) != "2" || len(back[1].Unknown) != 1 || back[0].Unknown != nil {
		t.Errorf("round trip = %+v, %v", back, c.err)
	}
}
