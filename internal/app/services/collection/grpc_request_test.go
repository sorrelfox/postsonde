package collection

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestGRPCRequest creates a grpc request, edits it through the auto-save
// pipeline and checks the file, the DTO shape and the round trip.
func TestGRPCRequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{ParentID: "fld_users", Name: "Get order", Kind: "grpc", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "grpc" || req.Address != "" || req.Metadata == nil || req.Proto.Files == nil || req.Proto.ImportPaths == nil || req.Proto.Inherit {
		t.Fatalf("new request = %+v", req)
	}

	tls, verify, deadline := true, false, int64(2500)
	req.Address = "localhost:50051"
	req.Proto = GRPCProtoDTO{Files: []string{"proto/shop/v1/orders.proto"}, ImportPaths: []string{"proto", "/opt/include"}, Extra: `{"x-set":1}`}
	req.Service, req.Method = "shop.v1.Orders", "GetOrder"
	req.Message = `{"id": "{{orderId}}"`
	req.Metadata = []KeyValueDTO{{Name: "x-request-id", Value: "1", Enabled: true}, {Name: "x-debug", Value: "1"}}
	req.Settings.TLS, req.Settings.VerifyTLS, req.Settings.DeadlineMs = &tls, &verify, &deadline
	req.Settings.Transport, req.Settings.Compression = "grpc-web-text", "gzip"
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "users/get-order.json")
	want := `{
  "schemaVersion": 1,
  "kind": "grpc",
  "id": "` + req.ID + `",
  "name": "Get order",
  "address": "localhost:50051",
  "proto": {
    "files": [
      "proto/shop/v1/orders.proto"
    ],
    "importPaths": [
      "proto",
      "/opt/include"
    ],
    "x-set": 1
  },
  "service": "shop.v1.Orders",
  "method": "GetOrder",
  "message": "{\"id\": \"{{orderId}}\"",
  "metadata": [
    {
      "name": "x-request-id",
      "value": "1"
    },
    {
      "name": "x-debug",
      "value": "1",
      "disabled": true
    }
  ],
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "tls": true,
    "verifyTls": false,
    "deadlineMs": 2500,
    "transport": "grpc-web-text",
    "compression": "gzip"
  },
  "x-note": "kept"
}
`
	if file != want {
		t.Errorf("file =\n%s\nwant\n%s", file, want)
	}

	// The DTO shape, with a fixed ID.
	got := getRequest(t, svc, req.ID)
	got.ID = "req_grpc"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-grpc", append(data, '\n'))

	// File -> DTO -> JSON -> DTO -> file gives the same bytes.
	f, err := projectfile.Decode([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(toRequestDTO(f))
	if err != nil {
		t.Fatal(err)
	}
	var dto RequestDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	back, err := dto.toFile()
	if err != nil {
		t.Fatal(err)
	}
	again, err := projectfile.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, []byte(file)) {
		t.Errorf("round trip =\n%s\nwant\n%s", again, file)
	}

	// Inheriting keeps the own lists; plaintext, empty lists and the
	// default transport and compression are left out.
	got.ID = req.ID
	got.Proto.Inherit, got.Proto.Extra = true, ""
	got.Metadata, got.Settings.TLS = []KeyValueDTO{}, nil
	got.Settings.Transport, got.Settings.Compression = "grpc", "identity"
	update(t, svc, got, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file = readFile(t, dir, "users/get-order.json")
	if !strings.Contains(file, `"inherit": true`) || !strings.Contains(file, "orders.proto") || strings.Contains(file, "metadata") || strings.Contains(file, `"tls"`) ||
		strings.Contains(file, `"transport"`) || strings.Contains(file, `"compression"`) {
		t.Errorf("file = %s", file)
	}

	// A bad extra member of the setup is refused.
	got.Proto.Extra = "[1]"
	_, err = svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: got, Revision: 3})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidRequest)
}

// TestInheritedGRPCProto resolves the nearest folder with a proto setup and
// edits a folder's setup.
func TestInheritedGRPCProto(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	var changed atomic.Int32
	svc := New(manager, func(name string, data any) {
		if ev, ok := data.(ChangedEventDTO); ok && name == EventChanged && ev.Op == OpUpdateFolder {
			changed.Add(1)
		}
	}, nil)
	ctx := t.Context()
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}, "grpc": {"reflection": true}}`)
	writeFile(t, dir, "users/admins/list.json", `{"kind": "grpc", "id": "req_list", "name": "List", "address": "localhost:1", "proto": {"inherit": true}}`)
	writeFile(t, dir, "top.json", `{"kind": "grpc", "id": "req_top", "name": "Top", "address": "localhost:1"}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	// No folder has a setup yet.
	got, err := svc.InheritedGRPCProto(ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if got.FolderID != "" || got.ParentID != "fld_admins" || got.ParentName != "Admins" || got.Files == nil || len(got.Files) != 0 {
		t.Errorf("InheritedGRPCProto = %+v", got)
	}
	top, err := svc.InheritedGRPCProto(ctx, "req_top")
	if err != nil || top.ParentID != "" || top.FolderID != "" {
		t.Errorf("top = %+v, %v", top, err)
	}

	// A setup on the grandparent is inherited.
	if err := svc.UpdateFolderGRPCProto(ctx, UpdateFolderGRPCProtoInputDTO{FolderID: "fld_users", Files: []string{"proto/a.proto"}, ImportPaths: []string{"proto"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, dir, "users/_folder.json"), `"grpc": {`) {
		t.Errorf("_folder.json = %s", readFile(t, dir, "users/_folder.json"))
	}
	got, err = svc.InheritedGRPCProto(ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if got.FolderID != "fld_users" || got.FolderName != "Users" || !slices.Equal(got.Files, []string{"proto/a.proto"}) || !slices.Equal(got.ImportPaths, []string{"proto"}) {
		t.Errorf("InheritedGRPCProto = %+v", got)
	}
	if n := changed.Load(); n != 1 {
		t.Errorf("changed events = %d", n)
	}

	// The nearer folder wins; its unknown grpc members are kept.
	if err := svc.UpdateFolderGRPCProto(ctx, UpdateFolderGRPCProtoInputDTO{FolderID: "fld_admins", ImportPaths: []string{"/opt/include"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.InheritedGRPCProto(ctx, "req_list")
	if got.FolderID != "fld_admins" || len(got.Files) != 0 || !slices.Equal(got.ImportPaths, []string{"/opt/include"}) {
		t.Errorf("InheritedGRPCProto = %+v", got)
	}
	if admins := readFile(t, dir, "users/admins/_folder.json"); !strings.Contains(admins, `"reflection": true`) {
		t.Errorf("admins = %s", admins)
	}

	// Empty lists remove the setup.
	if err := svc.UpdateFolderGRPCProto(ctx, UpdateFolderGRPCProtoInputDTO{FolderID: "fld_admins"}); err != nil {
		t.Fatal(err)
	}
	if admins := readFile(t, dir, "users/admins/_folder.json"); strings.Contains(admins, "proto") {
		t.Errorf("admins = %s", admins)
	}
	got, _ = svc.InheritedGRPCProto(ctx, "req_list")
	if got.FolderID != "fld_users" {
		t.Errorf("InheritedGRPCProto = %+v", got)
	}

	// Errors.
	_, err = svc.InheritedGRPCProto(ctx, "fld_users")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
	err = svc.UpdateFolderGRPCProto(ctx, UpdateFolderGRPCProtoInputDTO{FolderID: "req_list"})
	checkCode(t, err, errs.InvalidArgument, core.CodeWrongNode)
	err = svc.UpdateFolderGRPCProto(ctx, UpdateFolderGRPCProtoInputDTO{FolderID: "fld_gone"})
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
}
