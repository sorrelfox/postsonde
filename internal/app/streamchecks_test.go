package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	grpcservice "github.com/sorrelfox/postsonde/internal/app/services/grpc"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
)

// TEST-08: a gRPC request's assertions, edited in the
// editor (an edit saved through the collection), are checked against a
// call to the in-repo test server, as the app wires the services.
func TestStreamAssertionsAsWired(t *testing.T) {
	t.Parallel()
	data, project := t.TempDir(), filepath.Join(t.TempDir(), "proj")
	ctx := t.Context()
	var app appServices
	var rpc *grpcservice.GRPCService
	for _, s := range services(Config{DataDir: data}) {
		switch v := s.Instance().(type) {
		case *workspace.WorkspaceService:
			app.workspace = v
		case *collection.CollectionService:
			app.collection = v
		case *grpcservice.GRPCService:
			rpc = v
		}
	}
	if _, err := app.workspace.Create(ctx, workspace.CreateInputDTO{Path: project}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "echo.proto"), []byte(echoProto), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := app.collection.CreateRequest(ctx, collection.CreateRequestInputDTO{Name: "echo", Kind: "grpc", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req, err := app.collection.Request(ctx, created.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	req.Assertions = []collection.AssertionDTO{
		{Target: "message_count", Op: "equals", Expected: "1", Enabled: true},
		{Target: "status", Op: "equals", Expected: "0", Enabled: true},
		{Messages: "last", Target: "jsonpath", Expr: "$.b", Op: "equals", Expected: "aGk=", Enabled: true},
		{Messages: "nth", Index: 2, Target: "body", Op: "exists", Enabled: true},
	}
	if _, err := app.collection.UpdateRequest(ctx, collection.UpdateRequestInputDTO{Request: req}); err != nil {
		t.Fatal(err)
	}
	if back, err := app.collection.Request(ctx, created.NodeID); err != nil || len(back.Assertions) != 4 || back.Assertions[3].Messages != "nth" || back.Assertions[3].Index != 2 {
		t.Fatalf("assertions read back %+v %v", back.Assertions, err)
	}

	srv := grpctest.NewServer(t, false)
	started, err := rpc.Invoke(ctx, grpcservice.InvokeInputDTO{
		RequestID: created.NodeID, Files: []string{"echo.proto"}, Address: srv.Addr,
		Service: "grpctest.Echo", Method: "Unary", Message: `{"b": "aGk="}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []streamcheck.TestResultDTO
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err = rpc.CheckAssertions(ctx, grpcservice.CallRefDTO{OpID: started.OpID})
		if err == nil && len(got) == 4 && got[0].Outcome == "passed" && got[1].Outcome == "passed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want := []string{"passed", "passed", "passed", "failed"}
	if len(got) != len(want) {
		t.Fatalf("results %+v %v", got, err)
	}
	for i, r := range got {
		if r.Outcome != want[i] {
			t.Errorf("%d: %s %s", i, r.Outcome, r.Message)
		}
	}
	if got[3].Message != "Expected message 2's body to exist, but there was only 1 message." {
		t.Errorf("nth: %q", got[3].Message)
	}
}
