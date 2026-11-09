package app

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	grpcservice "github.com/sorrelfox/postsonde/internal/app/services/grpc"
	socketioservice "github.com/sorrelfox/postsonde/internal/app/services/socketio"
	"github.com/sorrelfox/postsonde/internal/app/services/websocket"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
)

const echoProto = `syntax = "proto3";

package grpctest;

message M {
  bytes b = 1;
}

service Echo {
  rpc Unary(M) returns (M);
}
`

// AUTH-31: the gRPC, WebSocket and Socket.IO test
// servers all receive the credentials of one auth config: a folder's
// bearer auth, typed in the Auth tab (so kept on this computer only),
// that the three requests inside inherit. The token is never written to
// the project folder.
func TestHandshakesSendTheFolderAuth(t *testing.T) {
	t.Parallel()
	const token = "folder-handshake-token-5e1b"
	data, project := t.TempDir(), filepath.Join(t.TempDir(), "proj")
	ctx := t.Context()

	var ws *websocket.WebSocketService
	var sio *socketioservice.SocketIOService
	var rpc *grpcservice.GRPCService
	var app appServices
	for _, s := range services(Config{DataDir: data}) {
		switch v := s.Instance().(type) {
		case *workspace.WorkspaceService:
			app.workspace = v
		case *collection.CollectionService:
			app.collection = v
		case *websocket.WebSocketService:
			ws = v
		case *socketioservice.SocketIOService:
			sio = v
		case *grpcservice.GRPCService:
			rpc = v
		}
	}
	if app.workspace == nil || app.collection == nil || ws == nil || sio == nil || rpc == nil {
		t.Fatal("a service is not bound")
	}
	if _, err := app.workspace.Create(ctx, workspace.CreateInputDTO{Path: project}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "echo.proto"), []byte(echoProto), 0o644); err != nil {
		t.Fatal(err)
	}
	folder, err := app.collection.CreateFolder(ctx, collection.CreateFolderInputDTO{Name: "Realtime", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	f, err := app.collection.Folder(ctx, folder.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	f.Auth = collection.AuthDTO{Type: "bearer", Token: token}
	if _, err := app.collection.UpdateFolder(ctx, collection.UpdateFolderInputDTO{Folder: f}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, kind := range []string{"websocket", "socketio", "grpc"} {
		created, err := app.collection.CreateRequest(ctx, collection.CreateRequestInputDTO{ParentID: folder.NodeID, Name: kind, Kind: kind, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		ids[kind] = created.NodeID
		req, err := app.collection.Request(ctx, created.NodeID)
		if err != nil || req.Auth.Type != "inherit" {
			t.Fatalf("%s: auth %+v, %v", kind, req.Auth, err)
		}
	}
	want := "Bearer " + token

	// WebSocket: the upgrade request.
	seen := make(chan string, 1)
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(wsSrv.Close)
	if _, err := ws.Connect(ctx, websocket.ConnectInputDTO{RequestID: ids["websocket"], URL: "ws" + strings.TrimPrefix(wsSrv.URL, "http")}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got != want {
			t.Errorf("WebSocket server saw Authorization %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the WebSocket server got no handshake")
	}

	// Socket.IO: every transport request.
	sioSrv := siotest.Start(siotest.Config{Authorize: func(string, json.RawMessage) error { return nil }}, t.Cleanup)
	if _, err := sio.Connect(ctx, socketioservice.ConnectInputDTO{RequestID: ids["socketio"], URL: sioSrv.URL, Transport: "polling"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(sioSrv.Requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reqs := sioSrv.Requests()
	if len(reqs) == 0 {
		t.Fatal("the Socket.IO server got no request")
	}
	for _, r := range reqs {
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("Socket.IO server saw Authorization %q", got)
		}
	}

	// gRPC: the call's metadata.
	rpcSrv := grpctest.NewServer(t, false)
	in := grpcservice.InvokeInputDTO{RequestID: ids["grpc"], Files: []string{"echo.proto"}, Address: rpcSrv.Addr, Service: "grpctest.Echo", Method: "Unary"}
	if _, err := rpc.Invoke(ctx, in); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for rpcSrv.LastRequest() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r := rpcSrv.LastRequest(); r == nil || r.Header.Get("Authorization") != want {
		t.Errorf("gRPC server saw %v", r)
	}

	// The typed token stays on this computer.
	if err := app.collection.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	scanned := 0
	err = filepath.WalkDir(project, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if strings.Contains(string(b), token) {
			t.Errorf("%s holds the token", path)
		}
		return nil
	})
	if err != nil || scanned < 5 {
		t.Fatalf("scanned %d project files: %v", scanned, err)
	}
}
