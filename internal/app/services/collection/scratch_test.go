package collection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
)

func scratchFile(t *testing.T, svc *CollectionService, id string) string {
	t.Helper()
	return filepath.Join(svc.manager.Current().LocalDir, ScratchDirName, id+".json")
}

func createScratch(t *testing.T, svc *CollectionService, kind, name string) NodeResultDTO {
	t.Helper()
	res, err := svc.CreateScratch(t.Context(), CreateScratchInputDTO{Kind: kind, Name: name})
	if err != nil {
		t.Fatalf("CreateScratch(%s, %q) = %v", kind, name, err)
	}
	return res
}

func scratchNames(tree TreeDTO) []string {
	var names []string
	for _, n := range tree.Scratch {
		names = append(names, n.Name)
	}
	return names
}

func TestScratchRequests(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()

	first := createScratch(t, svc, "http", "")
	second := createScratch(t, svc, "graphql", "  ")
	if got := scratchNames(second.Tree); strings.Join(got, ",") != "Scratch 1,Scratch 2" {
		t.Fatalf("scratch names = %v", got)
	}
	if !core.IsScratchID(first.NodeID) || first.NodeID == second.NodeID {
		t.Fatalf("ids %q %q", first.NodeID, second.NodeID)
	}
	node := second.Tree.Scratch[0]
	if node.ID != first.NodeID || node.Kind != "request" || node.FileKind != "http" || node.Method != "GET" || node.ParentID != nil || node.Path != "" {
		t.Errorf("scratch node = %+v", node)
	}
	for _, id := range second.Tree.RootIDs {
		if id == first.NodeID {
			t.Error("a scratch request is among the root ids")
		}
	}
	// Not in the project folder: in the local side files, 0600.
	info, err := os.Stat(scratchFile(t, svc, first.NodeID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if strings.HasPrefix(scratchFile(t, svc, first.NodeID), dir) {
		t.Error("the scratch file is inside the project")
	}

	// Edited, auto-saved and sent like any request.
	req := getRequest(t, svc, first.NodeID)
	req.Name, req.URL = "Try it", "https://example.com/try"
	req.Auth = AuthDTO{Type: "bearer", Token: "typed-token"}
	update(t, svc, req, 1)
	tree, err := svc.Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := scratchNames(tree); strings.Join(got, ",") != "Scratch 2,Try it" {
		t.Errorf("names with the pending edit = %v", got)
	}
	src, err := LoadHTTPSource(svc, ctx, first.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Request.URL != "https://example.com/try" || src.Auth.Token != "typed-token" || len(src.FolderVariables) != 0 {
		t.Errorf("source = %+v", src)
	}
	clock.Advance(autosave.DefaultDelay)
	if saved, failed := ev.counts(); saved != 1 || failed != 0 {
		t.Fatalf("saved %d, failed %d", saved, failed)
	}
	data, err := os.ReadFile(scratchFile(t, svc, first.NodeID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"url": "https://example.com/try"`) || strings.Contains(string(data), "typed-token") {
		t.Errorf("scratch file:\n%s", data)
	}
	if !fileExists(secretsFile(t, svc, first.NodeID)) {
		t.Error("the typed token is not in the local secrets")
	}

	// A new service (a restart) finds them again.
	restarted := New(svc.manager, nil, nil)
	tree, err = restarted.Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := scratchNames(tree); strings.Join(got, ",") != "Scratch 2,Try it" {
		t.Errorf("after a restart = %v", got)
	}
	if got := getRequest(t, restarted, first.NodeID); got.URL != "https://example.com/try" || got.Auth.Token != "typed-token" {
		t.Errorf("after a restart: %+v", got)
	}
	grpc, err := restarted.InheritedGRPCProto(ctx, first.NodeID)
	if err != nil || grpc.FolderID != "" {
		t.Errorf("InheritedGRPCProto = %+v, %v", grpc, err)
	}
	inherited, err := restarted.InheritedAuth(ctx, first.NodeID)
	if err != nil || inherited.FolderID != "" {
		t.Errorf("InheritedAuth = %+v, %v", inherited, err)
	}

	// Save to…: a request file in the folder, same ID, the scratch file gone.
	req = getRequest(t, restarted, first.NodeID)
	req.URL = "https://example.com/pending"
	update(t, restarted, req, 2)
	res, err := restarted.SaveScratch(ctx, SaveScratchInputDTO{ID: first.NodeID, ParentID: "fld_users", Name: "Try users", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodeID != first.NodeID {
		t.Errorf("saved id = %q, want %q", res.NodeID, first.NodeID)
	}
	if got := scratchNames(res.Tree); strings.Join(got, ",") != "Scratch 2" {
		t.Errorf("scratch after the save = %v", got)
	}
	saved := readFile(t, dir, "users/try-users.json")
	for _, want := range []string{`"id": "` + first.NodeID + `"`, `"name": "Try users"`, `"url": "https://example.com/pending"`} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved file lacks %s:\n%s", want, saved)
		}
	}
	if strings.Contains(saved, "typed-token") {
		t.Error("the typed token went into the project")
	}
	if fileExists(scratchFile(t, restarted, first.NodeID)) {
		t.Error("the scratch file is still there")
	}
	if got := getRequest(t, restarted, first.NodeID); got.Auth.Token != "typed-token" || got.Name != "Try users" {
		t.Errorf("saved request = %+v", got)
	}
	src, err = LoadHTTPSource(restarted, ctx, first.NodeID)
	if err != nil || len(src.FolderVariables) != 0 || src.Request.URL != "https://example.com/pending" {
		t.Errorf("source after the save = %+v, %v", src, err)
	}
	_, err = restarted.SaveScratch(ctx, SaveScratchInputDTO{ID: first.NodeID, Index: -1})
	checkCode(t, err, errs.NotFound, core.CodeNotFound)

	// Delete: gone for good, with its pending edit and secret.
	req = getRequest(t, restarted, second.NodeID)
	req.Auth = AuthDTO{Type: "bearer", Token: "other"}
	update(t, restarted, req, 1)
	if err := restarted.Flush(ctx, second.NodeID); err != nil {
		t.Fatal(err)
	}
	req.URL = "/late"
	update(t, restarted, req, 2)
	tree, err = restarted.DeleteScratch(ctx, second.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Scratch) != 0 || fileExists(scratchFile(t, restarted, second.NodeID)) || fileExists(secretsFile(t, restarted, second.NodeID)) {
		t.Errorf("after delete: scratch %v", tree.Scratch)
	}
	if err := restarted.FlushAll(ctx); err != nil {
		t.Errorf("FlushAll after delete = %v (a dropped edit must not be written)", err)
	}
	if fileExists(scratchFile(t, restarted, second.NodeID)) {
		t.Error("a late write brought the deleted scratch request back")
	}
	_, err = restarted.Request(ctx, second.NodeID)
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	if _, err := restarted.DeleteScratch(ctx, second.NodeID); err != nil {
		t.Errorf("deleting again = %v", err)
	}
}

func TestScratchRefusals(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newAutosaveService(t)
	ctx := t.Context()

	_, err := svc.CreateScratch(ctx, CreateScratchInputDTO{Kind: "folder"})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidKind)

	// IDs that are not scratch requests are not looked up as files.
	for _, id := range []string{"../../postsonde", "req_0000000000000000", ""} {
		_, err = svc.Request(ctx, id)
		checkCode(t, err, errs.NotFound, core.CodeNotFound)
		_, err = svc.SaveScratch(ctx, SaveScratchInputDTO{ID: id, Index: -1})
		checkCode(t, err, errs.NotFound, core.CodeNotFound)
	}

	// Saving into a missing folder fails and keeps the scratch request.
	res := createScratch(t, svc, "tcp", "Raw")
	_, err = svc.SaveScratch(ctx, SaveScratchInputDTO{ID: res.NodeID, ParentID: "fld_nope", Index: -1})
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	if !fileExists(scratchFile(t, svc, res.NodeID)) {
		t.Error("a failed save removed the scratch request")
	}

	// A scratch file from a newer PostSonde can be read, not changed.
	id := "req_00000000000000aa"
	path := scratchFile(t, svc, id)
	if err := os.WriteFile(path, []byte(`{"schemaVersion": 99, "kind": "http", "id": "`+id+`", "name": "New", "method": "GET"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, id)
	_, err = svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: req, Revision: 1})
	checkCode(t, err, errs.Unsupported, core.CodeReadOnly)

	// A secret side file is not needed for a request without auth.
	if _, err := requestsecrets.New(svc.manager.Current().LocalDir).Read(res.NodeID); err != nil {
		t.Error(err)
	}
}
