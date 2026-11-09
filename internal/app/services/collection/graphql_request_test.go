package collection

import (
	"reflect"
	"strings"
	"testing"
)

// TestGraphQLRequestExtensionsAndFiles edits a graphql request's
// extensions (GQL-06) and files (GQL-07) through the auto-save pipeline:
// the file stores them, the DTO reads them back, and a request without
// them leaves both out of the file and the DTO.
func TestGraphQLRequestExtensionsAndFiles(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Upload", Kind: "graphql", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Extensions != "" || len(req.Files) != 0 {
		t.Fatalf("new request = %+v", req)
	}
	file := readFile(t, dir, "upload.json")
	if strings.Contains(file, `"extensions"`) || strings.Contains(file, `"files"`) {
		t.Errorf("new file = %s", file)
	}

	files := []PartDTO{
		{Name: "variables.file", File: "fixtures/a.png", Enabled: true},
		{Name: "variables.docs.0", File: "/abs/b.pdf", ContentType: "application/pdf", Description: "second", Enabled: false, Extra: `{"x-kept":1}`},
	}
	req.Extensions = `{"persistedQuery": {"version": 1}}`
	req.Files = files
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file = readFile(t, dir, "upload.json")
	for _, want := range []string{
		`"extensions": "{\"persistedQuery\": {\"version\": 1}}"`,
		`"name": "variables.docs.0"`,
		`"file": "/abs/b.pdf"`,
		`"disabled": true`,
		`"x-kept": 1`,
	} {
		if !strings.Contains(file, want) {
			t.Errorf("file lacks %s:\n%s", want, file)
		}
	}
	got := getRequest(t, svc, res.NodeID)
	if got.Extensions != req.Extensions || !reflect.DeepEqual(got.Files, files) {
		t.Errorf("read back: extensions %q, files %+v", got.Extensions, got.Files)
	}
}

// TestGraphQLRequestTransport saves the transport per operation type
// (GQL-35) through the auto-save pipeline: the file's settings store it
// with its unknown members, the DTO reads it back, and a request without
// it has none in the file or the DTO.
func TestGraphQLRequestTransport(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Ticker", Kind: "graphql", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Settings.GraphQLTransport != nil || strings.Contains(readFile(t, dir, "ticker.json"), "graphqlTransport") {
		t.Fatalf("new request has a transport: %+v", req.Settings.GraphQLTransport)
	}

	want := &GraphQLTransportDTO{Query: "ws", Subscription: "sse", Extra: `{"x-kept":true}`}
	req.Settings.GraphQLTransport = want
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "ticker.json")
	if !strings.Contains(file, "\"graphqlTransport\": {\n      \"query\": \"ws\",\n      \"subscription\": \"sse\",\n      \"x-kept\": true\n    }") {
		t.Errorf("file:\n%s", file)
	}
	if got := getRequest(t, svc, res.NodeID); !reflect.DeepEqual(got.Settings.GraphQLTransport, want) {
		t.Errorf("read back %+v", got.Settings.GraphQLTransport)
	}

	// Back to the defaults: the member goes.
	req.Settings.GraphQLTransport = nil
	update(t, svc, req, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if file := readFile(t, dir, "ticker.json"); strings.Contains(file, "graphqlTransport") {
		t.Errorf("file:\n%s", file)
	}
}

// TestGraphQLRequestSchemaRefresh saves a request's schema auto-refresh
// (GQL-19) through the auto-save pipeline: stored in the file's settings
// with its unknown members, read back, and left out again when both
// triggers are off.
func TestGraphQLRequestSchemaRefresh(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Users", Kind: "graphql", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Settings.GraphQLSchemaRefresh != nil || strings.Contains(readFile(t, dir, "users.json"), "graphqlSchemaRefresh") {
		t.Fatalf("new request refreshes its schema: %+v", req.Settings.GraphQLSchemaRefresh)
	}

	want := &GraphQLSchemaRefreshDTO{OnSend: true, IntervalMinutes: 15, Extra: `{"x-kept":true}`}
	req.Settings.GraphQLSchemaRefresh = want
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "users.json")
	if !strings.Contains(file, "\"graphqlSchemaRefresh\": {\n      \"onSend\": true,\n      \"intervalMinutes\": 15,\n      \"x-kept\": true\n    }") {
		t.Errorf("file:\n%s", file)
	}
	if got := getRequest(t, svc, res.NodeID); !reflect.DeepEqual(got.Settings.GraphQLSchemaRefresh, want) {
		t.Errorf("read back %+v", got.Settings.GraphQLSchemaRefresh)
	}

	// Both off: the member goes.
	req.Settings.GraphQLSchemaRefresh = &GraphQLSchemaRefreshDTO{}
	update(t, svc, req, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if file := readFile(t, dir, "users.json"); strings.Contains(file, "graphqlSchemaRefresh") {
		t.Errorf("file:\n%s", file)
	}
	if got := getRequest(t, svc, res.NodeID); got.Settings.GraphQLSchemaRefresh != nil {
		t.Errorf("read back %+v", got.Settings.GraphQLSchemaRefresh)
	}
}
