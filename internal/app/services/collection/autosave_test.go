package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// saveEvents records the auto-save events the service emits.
type saveEvents struct {
	mu     sync.Mutex
	saved  []SavedEventDTO
	failed []SaveFailedEventDTO
}

func (e *saveEvents) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch name {
	case EventSaved:
		e.saved = append(e.saved, data.(SavedEventDTO))
	case EventSaveFailed:
		e.failed = append(e.failed, data.(SaveFailedEventDTO))
	}
}

func (e *saveEvents) counts() (saved, failed int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.saved), len(e.failed)
}

var clockStart = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// newAutosaveService opens the small project with a manual clock and an
// event recorder.
func newAutosaveService(t *testing.T) (*CollectionService, *coreworkspace.Manager, string, *fakeclock.Clock, *saveEvents) {
	t.Helper()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	ev := &saveEvents{}
	svc := New(manager, ev.emit, nil, withClock(clock))
	return svc, manager, dir, clock, ev
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func getRequest(t *testing.T, svc *CollectionService, id string) RequestDTO {
	t.Helper()
	req, err := svc.Request(t.Context(), id)
	if err != nil {
		t.Fatalf("Request(%s) = %v", id, err)
	}
	return req
}

func update(t *testing.T, svc *CollectionService, req RequestDTO, revision int) {
	t.Helper()
	if _, err := svc.UpdateRequest(t.Context(), UpdateRequestInputDTO{Request: req, Revision: revision}); err != nil {
		t.Fatalf("UpdateRequest(%s, %d) = %v", req.ID, revision, err)
	}
}

// errOf is the error of a call that returns a value and an error.
func errOf[T any](_ T, err error) error { return err }

func TestRequestDTO(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newAutosaveService(t)
	for _, id := range []string{"req_health", "req_me"} {
		got, err := json.MarshalIndent(getRequest(t, svc, id), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, "dto/request-"+strings.TrimPrefix(id, "req_"), append(got, '\n'))
	}
}

// TestRequestDTORoundTrip maps every request of the core sample project
// to a DTO and back: the file encodes to the same bytes, so opening and
// auto-saving a request without changes writes nothing, and unknown
// members survive.
func TestRequestDTORoundTrip(t *testing.T) {
	t.Parallel()
	tree, _ := core.Load(filepath.Join("..", "..", "..", "core", "collection", "testdata", "sample"))
	n := 0
	tree.Walk(func(node *core.Node) bool {
		if node.Kind != core.KindRequest || node.ReadOnly != core.Writable {
			return true
		}
		n++
		want, err := projectfile.Marshal(node.File)
		if err != nil {
			t.Fatalf("%s: %v", node.Path, err)
		}
		// Through JSON too, as the UI sends it.
		data, err := json.Marshal(toRequestDTO(node.File))
		if err != nil {
			t.Fatal(err)
		}
		var dto RequestDTO
		if err := json.Unmarshal(data, &dto); err != nil {
			t.Fatal(err)
		}
		f, err := dto.toFile()
		if err != nil {
			t.Fatalf("%s: toFile = %v", node.Path, err)
		}
		got, err := projectfile.Marshal(f)
		if err != nil {
			t.Fatalf("%s: %v", node.Path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s changed in the round trip:\n%s\nwant:\n%s", node.Path, got, want)
		}
		return true
	})
	if n < 5 {
		t.Fatalf("only %d requests in the sample", n)
	}
}

func TestUpdateRequestCoalescesAndWritesWithinDelay(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	before := readFile(t, dir, "health.json")

	req := getRequest(t, svc, "req_health")
	for i, url := range []string{"/h", "/he", "/health/live"} {
		req.URL = url
		update(t, svc, req, i+1)
		clock.Advance(100 * time.Millisecond)
	}
	// Reads see the edit at once; the file is not written yet.
	if got := getRequest(t, svc, "req_health").URL; got != "/health/live" {
		t.Errorf("Request().URL = %q before the write", got)
	}
	if readFile(t, dir, "health.json") != before {
		t.Fatal("written before the delay")
	}
	clock.Advance(autosave.DefaultDelay - 100*time.Millisecond)
	if !strings.Contains(readFile(t, dir, "health.json"), `"url": "/health/live"`) {
		t.Fatalf("file after the delay:\n%s", readFile(t, dir, "health.json"))
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if len(ev.saved) != 1 || len(ev.failed) != 0 {
		t.Fatalf("events: saved %+v, failed %+v; want one save", ev.saved, ev.failed)
	}
	want := SavedEventDTO{WorkspaceID: "ws_0123456789abcdef", ID: "req_health", Revision: 3, SavedAt: "2026-09-16T12:00:00.6Z"}
	if ev.saved[0] != want {
		t.Errorf("saved = %+v, want %+v", ev.saved[0], want)
	}
}

// TestEditorEditWritesFile follows what the request editor sends:
// the whole DTO from Request with the method, URL and rows changed. After a
// flush the file has exactly those changes; the body and unknown members
// are untouched.
func TestEditorEditWritesFile(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, ev := newAutosaveService(t)
	writeFile(t, dir, "list.json", `{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_list",
  "name": "List products",
  "method": "PUT",
  "url": "{{baseUrl}}/v1/products?limit=20",
  "params": [
    {"name": "limit", "value": "20", "description": "Page size", "x-row": true}
  ],
  "headers": [
    {"name": "Accept", "value": "application/json"}
  ],
  "body": {"mode": "raw", "type": "json", "text": "{\"a\": 1}", "x-body": "kept"},
  "auth": {"type": "inherit"},
  "x-custom": {"nested": [1, 2]}
}
`)

	// Through JSON, as the UI gets and sends it.
	data, err := json.Marshal(getRequest(t, svc, "req_list"))
	if err != nil {
		t.Fatal(err)
	}
	var req RequestDTO
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	req.Method = "POST"
	update(t, svc, req, 1)
	req.URL = "{{baseUrl}}/v1/products?limit=50&sort=name"
	req.Params[0].Value = "50"
	req.Params = append(req.Params, KeyValueDTO{Name: "sort", Value: "name", Enabled: true})
	req.Headers[0].Enabled = false
	update(t, svc, req, 2)

	if err := svc.Flush(t.Context(), "req_list"); err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_list",
  "name": "List products",
  "method": "POST",
  "url": "{{baseUrl}}/v1/products?limit=50&sort=name",
  "params": [
    {
      "name": "limit",
      "value": "50",
      "description": "Page size",
      "x-row": true
    },
    {
      "name": "sort",
      "value": "name"
    }
  ],
  "headers": [
    {
      "name": "Accept",
      "value": "application/json",
      "disabled": true
    }
  ],
  "body": {
    "mode": "raw",
    "type": "json",
    "text": "{\"a\": 1}",
    "x-body": "kept"
  },
  "auth": {
    "type": "inherit"
  },
  "x-custom": {
    "nested": [
      1,
      2
    ]
  }
}
`
	if got := readFile(t, dir, "list.json"); got != want {
		t.Errorf("list.json:\n%s\nwant:\n%s", got, want)
	}
	if saved, failed := ev.counts(); saved != 1 || failed != 0 {
		t.Errorf("events: %d saved, %d failed; want 1, 0", saved, failed)
	}
}

func TestUpdateRequestChangesKind(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_me")
	req.Kind, req.Method, req.Body.Mode = "http", "POST", "raw"
	update(t, svc, req, 1)
	clock.Advance(autosave.DefaultDelay)
	if got := readFile(t, dir, "users/me.json"); !strings.Contains(got, `"kind": "http"`) {
		t.Fatalf("file:\n%s", got)
	}
	tree, err := svc.Tree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tree.Nodes {
		if n.ID == "req_me" && (n.FileKind != "http" || n.Method != "POST") {
			t.Errorf("node after the save = %+v", n)
		}
	}
}

func TestFlushAndFlushAll(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()

	health := getRequest(t, svc, "req_health")
	health.Name = "Health check"
	update(t, svc, health, 1)
	me := getRequest(t, svc, "req_me")
	me.Query = "{ me { id name } }"
	update(t, svc, me, 1)

	// A closing tab flushes its request only.
	if err := svc.Flush(ctx, "req_health"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, dir, "health.json"), "Health check") {
		t.Error("Flush did not write health.json")
	}
	if strings.Contains(readFile(t, dir, "users/me.json"), "name }") {
		t.Error("Flush(req_health) wrote users/me.json")
	}
	if err := svc.Flush(ctx, "req_health"); err != nil {
		t.Errorf("Flush with nothing pending = %v", err)
	}

	if err := svc.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, dir, "users/me.json"), "{ me { id name } }") {
		t.Error("FlushAll did not write users/me.json")
	}
	// The timers were stopped: nothing is written twice.
	clock.Advance(time.Minute)
	if saved, failed := ev.counts(); saved != 2 || failed != 0 {
		t.Errorf("events: %d saved, %d failed; want 2, 0", saved, failed)
	}
}

func TestShutdownFlushesWithoutEvents(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Description = "Checked by the load balancer."
	update(t, svc, req, 7)
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, dir, "health.json"), "Checked by the load balancer.") {
		t.Error("the edit was lost at quit")
	}
	clock.Advance(time.Minute)
	if saved, failed := ev.counts(); saved != 0 || failed != 0 {
		t.Errorf("events during shutdown: %d saved, %d failed", saved, failed)
	}
}

func TestFailedWriteEmitsAndStaysPending(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not stop writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()
	users := filepath.Join(dir, "users")

	req := getRequest(t, svc, "req_me")
	req.Name = "Me"
	update(t, svc, req, 4)
	// The atomic writer creates a temporary file next to the target: a
	// read-only directory makes it fail.
	if err := os.Chmod(users, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(users, 0o755) })
	clock.Advance(autosave.DefaultDelay)

	ev.mu.Lock()
	failed := append([]SaveFailedEventDTO(nil), ev.failed...)
	ev.mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("save-failed events = %+v, want one", failed)
	}
	f := failed[0]
	if f.ID != "req_me" || f.Revision != 4 || f.WorkspaceID != "ws_0123456789abcdef" ||
		f.Error.Code != projectfile.CodeSaveFailed || f.Error.Kind != string(errs.PermissionDenied) {
		t.Errorf("save-failed = %+v", f)
	}
	// Still pending: reads keep the edit, and a flush retries.
	if got := getRequest(t, svc, "req_me").Name; got != "Me" {
		t.Errorf("Request().Name after the failure = %q", got)
	}
	err := svc.FlushAll(ctx)
	checkCode(t, err, errs.PermissionDenied, projectfile.CodeSaveFailed)

	if err := os.Chmod(users, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Flush(ctx, "req_me"); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if !strings.Contains(readFile(t, dir, "users/me.json"), `"name": "Me"`) {
		t.Error("the retry did not write the edit")
	}
	if saved, _ := ev.counts(); saved != 1 {
		t.Errorf("saved events after the retry = %d", saved)
	}
}

func TestUpdateRequestRefusals(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, ev := newAutosaveService(t)
	ctx := t.Context()
	good := getRequest(t, svc, "req_health")
	// users/broken.json has no readable id: find the one it was given.
	tree, err := svc.Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	brokenID := ""
	for _, n := range tree.Nodes {
		if n.Path == "users/broken.json" {
			brokenID = n.ID
		}
	}

	with := func(change func(*RequestDTO)) RequestDTO {
		r := good
		change(&r)
		return r
	}
	tests := []struct {
		name string
		req  RequestDTO
		kind errs.Kind
		code string
	}{
		{"unknown kind file", with(func(r *RequestDTO) { r.ID = "req_ws" }), errs.Unsupported, core.CodeReadOnly},
		{"invalid file", with(func(r *RequestDTO) { r.ID = brokenID }), errs.Unsupported, core.CodeReadOnly},
		{"folder", with(func(r *RequestDTO) { r.ID = "fld_users" }), errs.InvalidArgument, core.CodeWrongNode},
		{"missing", with(func(r *RequestDTO) { r.ID = "req_gone" }), errs.NotFound, core.CodeNotFound},
		{"bad kind", with(func(r *RequestDTO) { r.Kind = "mqtt" }), errs.InvalidArgument, CodeInvalidRequest},
		{"bad extra", with(func(r *RequestDTO) { r.Headers = []KeyValueDTO{{Name: "A", Extra: "[1]"}} }), errs.InvalidArgument, CodeInvalidRequest},
		{"no method", with(func(r *RequestDTO) { r.Method = " " }), errs.InvalidArgument, projectfile.CodeInvalidValue},
	}
	for _, tt := range tests {
		_, err := svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: tt.req, Revision: 1})
		t.Run(tt.name, func(t *testing.T) { checkCode(t, err, tt.kind, tt.code) })
	}
	var e *errs.Error
	if _, err := svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: tests[0].req}); errors.As(err, &e) && e.Details["reason"] != string(core.ReadOnlyUnknownKind) {
		t.Errorf("read-only reason = %q", e.Details["reason"])
	}

	clock.Advance(time.Minute)
	if saved, failed := ev.counts(); saved != 0 || failed != 0 {
		t.Errorf("a refused update was scheduled: %d saved, %d failed", saved, failed)
	}
	if !strings.Contains(readFile(t, dir, "users/stream.json"), "mqtt") {
		t.Error("the unknown file was changed")
	}
}

func TestUpdateRequestNoWorkspaceAndCancelled(t *testing.T) {
	t.Parallel()
	svc, manager, _, _, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, err := range map[string]error{
		"UpdateRequest": errOf(svc.UpdateRequest(ctx, UpdateRequestInputDTO{Request: req})),
		"Flush":         svc.Flush(ctx, "req_health"),
		"FlushAll":      svc.FlushAll(ctx),
	} {
		if !errors.Is(err, context.Canceled) || errs.KindOf(err) != errs.Cancelled {
			t.Errorf("%s with a cancelled context = %v", name, err)
		}
	}
	if _, err := svc.Request(ctx, "req_health"); !errors.Is(err, context.Canceled) {
		t.Errorf("Request with a cancelled context = %v", err)
	}

	if _, err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := svc.UpdateRequest(t.Context(), UpdateRequestInputDTO{Request: req})
	checkCode(t, err, errs.NotFound, coreworkspace.CodeNoWorkspace)
}

func TestReloadWritesPendingEditsFirst(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, ev := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Name = "Renamed in the editor"
	update(t, svc, req, 2)

	tree, err := svc.Reload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, dir, "health.json"), "Renamed in the editor") {
		t.Error("Reload did not write the pending edit")
	}
	for _, n := range tree.Nodes {
		if n.ID == "req_health" && n.Name != "Renamed in the editor" {
			t.Errorf("reloaded node name = %q", n.Name)
		}
	}
	if saved, _ := ev.counts(); saved != 1 {
		t.Errorf("saved events = %d", saved)
	}
}

func TestReloadKeepsFailedEdits(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs directory permissions that stop writes")
	}
	svc, _, dir, clock, _ := newAutosaveService(t)
	ctx := t.Context()
	users := filepath.Join(dir, "users")
	req := getRequest(t, svc, "req_me")
	req.Name = "Kept"
	update(t, svc, req, 1)
	if err := os.Chmod(users, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(users, 0o755) })

	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, "req_me").Name; got != "Kept" {
		t.Errorf("Request().Name after a reload with a failed write = %q", got)
	}
	if err := os.Chmod(users, 0o755); err != nil {
		t.Fatal(err)
	}
	clock.Advance(autosave.DefaultDelay)
	if !strings.Contains(readFile(t, dir, "users/me.json"), `"name": "Kept"`) {
		t.Error("the carried edit was not written")
	}
}

func TestPendingEditFollowsItsProject(t *testing.T) {
	t.Parallel()
	svc, manager, dir, clock, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.URL = "/before-switch"
	update(t, svc, req, 1)

	other := filepath.Join(tempDir(t), "other")
	writeFile(t, other, "postsonde.json", `{"kind": "workspace", "id": "ws_other"}`)
	writeFile(t, other, "health.json", `{"kind": "http", "id": "req_health", "name": "Other", "method": "GET", "url": "/other"}`)
	if _, err := manager.Open(other); err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, "req_health").URL; got != "/other" {
		t.Errorf("the other project's request shows %q", got)
	}
	clock.Advance(autosave.DefaultDelay)
	if !strings.Contains(readFile(t, dir, "health.json"), "/before-switch") {
		t.Error("the edit did not reach the first project")
	}
	if strings.Contains(readFile(t, other, "health.json"), "/before-switch") {
		t.Error("the edit was written to the other project")
	}
}
