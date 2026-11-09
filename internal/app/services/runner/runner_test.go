package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/template"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// server records the paths it is asked for, in order. /block holds a
// request until its client goes away; /events sends two events and ends
// the stream; /forever sends one event and keeps the stream open.
type server struct {
	*httptest.Server
	mu      sync.Mutex
	paths   []string
	auth    []string
	hit     chan string
	blocked chan struct{}
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{hit: make(chan string, 1024), blocked: make(chan struct{}, 16)}
	s.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.auth = append(s.auth, r.Header.Get("X-Token"))
		s.mu.Unlock()
		select {
		case s.hit <- r.URL.Path:
		default:
		}
		switch r.URL.Path {
		case "/block":
			s.blocked <- struct{}{}
			<-r.Context().Done()
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: one\n\nevent: tick\ndata: two\n\n")
		case "/forever":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: one\n\n")
			w.(nethttp.Flusher).Flush()
			<-r.Context().Done()
		case "/missing-stream":
			nethttp.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"path": %q}`, r.URL.Path)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

// recorder keeps the events of the runs.
type recorder struct {
	mu       sync.Mutex
	results  map[string][]RunResultDTO
	progress map[string][]operation.ProgressEventDTO
	done     map[string]chan operation.DoneEventDTO
	added    chan struct{}
}

func newRecorder() *recorder {
	return &recorder{results: map[string][]RunResultDTO{}, progress: map[string][]operation.ProgressEventDTO{},
		done: map[string]chan operation.DoneEventDTO{}, added: make(chan struct{}, 1)}
}

func (r *recorder) doneChan(id string) chan operation.DoneEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.done[id]
	if !ok {
		ch = make(chan operation.DoneEventDTO, 1)
		r.done[id] = ch
	}
	return ch
}

func (r *recorder) emit(name string, data any) {
	switch d := data.(type) {
	case operation.DataEventDTO[RunResultDTO]:
		if name != EventData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.results[d.OpID] = append(r.results[d.OpID], d.Items...)
		r.mu.Unlock()
		select {
		case r.added <- struct{}{}:
		default:
		}
	case operation.ProgressEventDTO:
		if name != EventProgress {
			panic("progress event named " + name)
		}
		r.mu.Lock()
		r.progress[d.OpID] = append(r.progress[d.OpID], d)
		r.mu.Unlock()
	case operation.DoneEventDTO:
		if name != EventDone {
			panic("done event named " + name)
		}
		r.doneChan(d.OpID) <- d
	}
}

func (r *recorder) resultsOf(id string) []RunResultDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.results[id])
}

func (r *recorder) wait(t *testing.T, id string) operation.DoneEventDTO {
	t.Helper()
	select {
	case d := <-r.doneChan(id):
		return d
	case <-time.After(20 * time.Second):
		t.Fatalf("run %s did not end", id)
		return operation.DoneEventDTO{}
	}
}

func (r *recorder) waitResults(t *testing.T, id string, n int) {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for len(r.resultsOf(id)) < n {
		select {
		case <-r.added:
		case <-timeout:
			t.Fatalf("results = %d, want %d", len(r.resultsOf(id)), n)
		}
	}
}

// sleeps records the delays of a run instead of waiting them.
type sleeps struct {
	mu  sync.Mutex
	got []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.got = append(s.got, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeps) list() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// entries is a request history that keeps its entries.
type entries struct {
	mu   sync.Mutex
	list []history.Entry
}

func (e *entries) option() httpservice.Option {
	return httpservice.WithHistory(func() func(history.Entry) {
		return func(en history.Entry) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.list = append(e.list, en)
		}
	})
}

func (e *entries) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, en := range e.list {
		out = append(out, en.RequestName)
	}
	return out
}

type fixture struct {
	svc    *RunnerService
	rec    *recorder
	ops    *operation.Registry
	sleeps *sleeps
	hist   *entries
	dir    string
	// localDir is the project's local directory (the run history's).
	localDir func() (string, error)
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixture opens a project whose files are files (with {{base}} the
// server's URL) and runs its folders. opts, given the project's manager,
// add options of the HTTP service.
func newFixture(t *testing.T, base string, files map[string]string, opts ...func(*coreworkspace.Manager) httpservice.Option) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "project")
	vars, _ := json.Marshal([]map[string]string{{"name": "base", "value": base}})
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "variables": `+string(vars)+`}`)
	for rel, content := range files {
		writeFile(t, dir, rel, content)
	}
	manager := coreworkspace.New(filepath.Join(root, "data"))
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	col := collection.New(manager, nil, nil)
	t.Cleanup(func() { _ = col.ServiceShutdown() })

	f := &fixture{rec: newRecorder(), sleeps: &sleeps{}, hist: &entries{}, dir: dir, localDir: manager.LocalDir}
	f.ops = operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.ops.Shutdown(ctx)
	})
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	httpOpts := []httpservice.Option{f.hist.option()}
	for _, o := range opts {
		httpOpts = append(httpOpts, o(manager))
	}
	httpSvc := httpservice.New(func(ctx context.Context, id string) (collection.HTTPSource, error) {
		return collection.LoadHTTPSource(col, ctx, id)
	}, nil, f.ops, dialer, nil, nil, httpOpts...)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	f.svc = New(func(ctx context.Context, id string) (corerunner.Plan, error) {
		return collection.RunPlan(col, ctx, id)
	}, httpSvc, f.ops, nil)
	f.svc.sleep = f.sleeps.sleep
	return f
}

func (f *fixture) start(t *testing.T, in RunInputDTO) string {
	t.Helper()
	started, err := f.svc.Start(t.Context(), in)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	return started.OpID
}

// httpFile is an http request file.
func httpFile(id, name, method, path, extra string) string {
	return `{"kind": "http", "id": "` + id + `", "name": "` + name + `", "method": "` + method + `", "url": "{{base}}` + path + `", "auth": {"type": "none"}` + extra + `}`
}

// apiFolder is a folder that has one of every kind of item a run meets,
// in a manual order that is not the alphabetical one, and a subfolder.
func apiFolder() map[string]string {
	return map[string]string{
		"api/_folder.json": `{"kind": "folder", "id": "fld_api", "name": "API", "order": ["create.json", "users", "list.json", "socket.json", "sub.json", "events.json", "undefined.json"]}`,
		"api/create.json": httpFile("req_create", "Create", "POST", "/create",
			`, "assertions": [{"target": "status", "op": "equals", "expected": "200"}, {"target": "jsonpath", "expr": "$.path", "op": "equals", "expected": "/create"}]`),
		"api/users/_folder.json": `{"kind": "folder", "id": "fld_users", "name": "Users"}`,
		"api/users/get.json": httpFile("req_get", "Get user", "GET", "/users/1",
			`, "assertions": [{"target": "status", "op": "equals", "expected": "201"}, {"target": "status", "op": "equals", "expected": "200", "disabled": true}]`),
		"api/list.json":      httpFile("req_list", "List", "GET", "/list", ""),
		"api/socket.json":    `{"kind": "websocket", "id": "req_socket", "name": "Socket", "url": "ws://localhost/feed"}`,
		"api/sub.json":       `{"kind": "graphql", "id": "req_sub", "name": "Ticks", "url": "{{base}}/graphql", "query": "subscription { ticks }", "auth": {"type": "none"}}`,
		"api/events.json":    `{"kind": "sse", "id": "req_events", "name": "Events", "url": "{{base}}/events", "auth": {"type": "none"}}`,
		"api/undefined.json": httpFile("req_undefined", "Undefined", "GET", "/{{nope}}", ""),
		"api/zz-broken.json": `{"kind": "http", "id": `,
		"elsewhere.json":     httpFile("req_elsewhere", "Elsewhere", "GET", "/elsewhere", ""),
	}
}

func TestPlan(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://localhost:1", apiFolder())
	plan, err := f.svc.Plan(t.Context(), "fld_api")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range plan.Items {
		got = append(got, fmt.Sprintf("%d %s %s %s %s [%s] %s", it.Position, it.RequestID, it.Name, it.Kind, it.Method, strings.Join(it.Path, "/"), it.Skip))
	}
	want := []string{
		"1 req_create Create http POST [] ",
		"2 req_get Get user http GET [Users] ",
		"3 req_list List http GET [] ",
		"4 req_socket Socket websocket  [] kind",
		"5 req_sub Ticks graphql POST [] subscription",
		"6 req_events Events sse GET [] ",
		"7 req_undefined Undefined http GET [] ",
		"8 " + plan.Items[7].RequestID + " zz-broken   [] invalid",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if plan.Runnable != 5 {
		t.Errorf("runnable = %d, want 5", plan.Runnable)
	}

	// The whole project: the folder's items, then the top-level request.
	all, err := f.svc.Plan(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(all.Items); n != 9 || all.Items[8].RequestID != "req_elsewhere" || all.Items[1].Path[0] != "API" {
		t.Fatalf("project plan = %+v", all.Items)
	}

	_, err = f.svc.Plan(t.Context(), "req_list")
	checkCode(t, err, errs.InvalidArgument, collection.CodeNotFolder)
	_, err = f.svc.Plan(t.Context(), "fld_gone")
	checkCode(t, err, errs.NotFound, "collection.not_found")
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Kind != kind || e.Code != code {
		t.Fatalf("err = %#v, want %s %s", err, kind, code)
	}
}

// TestRunOrderAndIterations: every iteration sends the folder's requests
// in the tree's order, subfolders at their place, lists every item with
// its outcome, and waits the delay between the requests it sends.
func TestRunOrderAndIterations(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, apiFolder())
	id := f.start(t, RunInputDTO{FolderID: "fld_api", Iterations: 3, DelayMs: 250, StreamMs: 5000})
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK || done.Items != 24 {
		t.Fatalf("done = %+v", done)
	}

	once := []string{"/create", "/users/1", "/list", "/events"}
	want := slices.Concat(once, once, once)
	if got := srv.got(); !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}

	results := f.rec.resultsOf(id)
	type row struct {
		iteration, index, position int
		id, outcome, skip          string
		status, passed, failed     int
	}
	var got []row
	for _, r := range results {
		got = append(got, row{r.Iteration, r.Index, r.Position, r.RequestID, r.Outcome, r.Skip, r.StatusCode, r.TestsPassed, r.TestsFailed})
	}
	var wantRows []row
	for i := range 3 {
		n := i * 8
		wantRows = append(wantRows,
			row{i + 1, n + 1, 1, "req_create", OutcomePassed, "", 200, 2, 0},
			row{i + 1, n + 2, 2, "req_get", OutcomeFailed, "", 200, 0, 1},
			row{i + 1, n + 3, 3, "req_list", OutcomePassed, "", 200, 0, 0},
			row{i + 1, n + 4, 4, "req_socket", OutcomeSkipped, "kind", 0, 0, 0},
			row{i + 1, n + 5, 5, "req_sub", OutcomeSkipped, "subscription", 0, 0, 0},
			row{i + 1, n + 6, 6, "req_events", OutcomePassed, "", 200, 0, 0},
			row{i + 1, n + 7, 7, "req_undefined", OutcomeError, "", 0, 0, 0},
			row{i + 1, n + 8, 8, results[7].RequestID, OutcomeSkipped, "invalid", 0, 0, 0},
		)
	}
	if !slices.Equal(got, wantRows) {
		t.Fatalf("results =\n%+v\nwant\n%+v", got, wantRows)
	}

	// The assertion results, per request (TEST-06).
	create, get := results[0], results[1]
	if len(create.Tests) != 2 || create.Tests[0].Outcome != "passed" || create.Tests[1].Expr != "$.path" {
		t.Errorf("create tests = %+v", create.Tests)
	}
	if len(get.Tests) != 1 || get.Tests[0].Outcome != "failed" || get.Tests[0].Index != 0 || !strings.Contains(get.Tests[0].Message, "201") {
		t.Errorf("get tests = %+v", get.Tests)
	}
	if get.Path[0] != "Users" || get.Name != "Get user" || get.Method != "GET" || get.StartedAt == 0 || get.Size == 0 {
		t.Errorf("get = %+v", get)
	}
	if ev := results[5]; ev.Events != 2 || ev.Kind != "sse" || ev.Error != nil {
		t.Errorf("events = %+v", ev)
	}
	if u := results[6]; u.Error == nil || u.Error.Code != template.CodeUnresolved || u.Error.Details["names"] != "nope" {
		t.Errorf("undefined = %+v", u)
	}
	if s := results[3]; s.StartedAt != 0 || s.Tests == nil || s.Error != nil {
		t.Errorf("skipped = %+v", s)
	}

	// The delay between every two requests sent (5 per iteration,
	// the one that could not be built included), none before the first.
	delays := f.sleeps.list()
	if len(delays) != 14 {
		t.Fatalf("delays = %v, want 14", delays)
	}
	for _, d := range delays {
		if d != 250*time.Millisecond {
			t.Fatalf("delays = %v", delays)
		}
	}

	// Progress counts the results of the whole run.
	f.rec.mu.Lock()
	progress := slices.Clone(f.rec.progress[id])
	f.rec.mu.Unlock()
	last := progress[len(progress)-1]
	if last.Done != 24 || last.Total != 24 {
		t.Errorf("last progress = %+v", last)
	}

	// One history entry per request sent, not one per send.
	if names := f.hist.names(); !slices.Equal(names, []string{"Create", "Get user", "List"}) {
		t.Errorf("history = %v", names)
	}
}

// TestRunNoDelay: without a delay the run never waits.
func TestRunNoDelay(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, map[string]string{
		"a/_folder.json": `{"kind": "folder", "id": "fld_a", "name": "A"}`,
		"a/one.json":     httpFile("req_one", "One", "GET", "/one", ""),
		"a/two.json":     httpFile("req_two", "Two", "GET", "/two", ""),
	})
	id := f.start(t, RunInputDTO{FolderID: "fld_a", Iterations: 2})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK || done.Items != 4 {
		t.Fatalf("done = %+v", done)
	}
	if got := srv.got(); !slices.Equal(got, []string{"/one", "/two", "/one", "/two"}) {
		t.Fatalf("paths = %v", got)
	}
	if d := f.sleeps.list(); len(d) != 0 {
		t.Fatalf("delays = %v", d)
	}
}

// TestRunSelection: a run sends only the requests chosen, in the order
// chosen, in every iteration, and the folder keeps its order (RUN-02).
func TestRunSelection(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, apiFolder())
	id := f.start(t, RunInputDTO{FolderID: "fld_api", Iterations: 2, DelayMs: 100,
		RequestIDs: []string{"req_list", "req_socket", "req_get", "req_create"}})
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK || done.Items != 8 {
		t.Fatalf("done = %+v", done)
	}
	once := []string{"/list", "/users/1", "/create"}
	if got := srv.got(); !slices.Equal(got, slices.Concat(once, once)) {
		t.Fatalf("paths = %v", got)
	}
	var got []string
	for _, r := range f.rec.resultsOf(id) {
		got = append(got, fmt.Sprintf("%d.%d #%d %s %s", r.Iteration, r.Position, r.Index, r.RequestID, r.Outcome))
	}
	want := []string{
		"1.1 #1 req_list passed", "1.2 #2 req_socket skipped", "1.3 #3 req_get failed", "1.4 #4 req_create passed",
		"2.1 #5 req_list passed", "2.2 #6 req_socket skipped", "2.3 #7 req_get failed", "2.4 #8 req_create passed",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("results =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Three requests sent per iteration: five delays between six sends.
	if d := f.sleeps.list(); len(d) != 5 {
		t.Errorf("delays = %v", d)
	}
	// The folder's own order is unchanged.
	plan, err := f.svc.Plan(t.Context(), "fld_api")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].RequestID != "req_create" || plan.Items[2].RequestID != "req_list" {
		t.Errorf("plan = %+v", plan.Items)
	}
	// The history follows the order the requests were sent in.
	if names := f.hist.names(); !slices.Equal(names, []string{"List", "Get user", "Create"}) {
		t.Errorf("history = %v", names)
	}
}

// TestRunStopOnFailure: with StopOnFailure the run ends after the first
// result that failed or is an error and sends nothing after it; skipped
// items do not stop it; without it the run goes on (RUN-04).
func TestRunStopOnFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		ids    []string
		bail   bool
		paths  []string
		last   string
		stages int
	}{
		{"at a failed assertion", []string{"req_socket", "req_create", "req_get", "req_list"}, true,
			[]string{"/create", "/users/1"}, "req_get failed", 3},
		{"at an error", []string{"req_create", "req_undefined", "req_list"}, true,
			[]string{"/create"}, "req_undefined error", 2},
		{"never without a failure", []string{"req_create", "req_list"}, true,
			[]string{"/create", "/list", "/create", "/list"}, "req_list passed", 4},
		{"goes on without bail", []string{"req_get", "req_list"}, false,
			[]string{"/users/1", "/list", "/users/1", "/list"}, "req_list passed", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t)
			f := newFixture(t, srv.URL, apiFolder())
			id := f.start(t, RunInputDTO{FolderID: "fld_api", Iterations: 2, RequestIDs: tt.ids, StopOnFailure: tt.bail})
			done := f.rec.wait(t, id)
			if done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			if got := srv.got(); !slices.Equal(got, tt.paths) {
				t.Fatalf("paths = %v, want %v", got, tt.paths)
			}
			results := f.rec.resultsOf(id)
			if len(results) != tt.stages {
				t.Fatalf("results = %d, want %d", len(results), tt.stages)
			}
			last := results[len(results)-1]
			if got := last.RequestID + " " + last.Outcome; got != tt.last {
				t.Fatalf("last result = %s, want %s", got, tt.last)
			}
			f.rec.mu.Lock()
			progress := slices.Clone(f.rec.progress[id])
			f.rec.mu.Unlock()
			p := progress[len(progress)-1]
			stopped := len(results) < 2*len(tt.ids)
			if stopped {
				if p.Message != StageStopped || p.Done != int64(len(results)) || p.Total != int64(2*len(tt.ids)) {
					t.Fatalf("last progress = %+v", p)
				}
			} else if p.Message == StageStopped || p.Done != p.Total {
				t.Fatalf("last progress = %+v", p)
			}
		})
	}
}

// TestRunResponses: every HTTP and GraphQL result of a run opens its
// request and response until the run is released (RUN-07).
func TestRunResponses(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, apiFolder())
	id := f.start(t, RunInputDTO{FolderID: "fld_api", Iterations: 2, StreamMs: 5000})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	results := f.rec.resultsOf(id)
	for _, r := range results {
		want := r.Kind == "http" && r.Outcome != OutcomeError || r.Kind == "graphql" && r.Outcome != OutcomeSkipped
		if r.HasResponse != want {
			t.Errorf("result %d %s hasResponse = %v", r.Index, r.RequestID, r.HasResponse)
		}
		resp, err := f.svc.Response(t.Context(), id, r.Index)
		if !want {
			checkCode(t, err, errs.NotFound, CodeResponseGone)
			continue
		}
		if err != nil {
			t.Fatalf("Response(%d) = %v", r.Index, err)
		}
		if resp.StatusCode != r.StatusCode || len(resp.Tests) != len(r.Tests) || resp.BodyID != "" {
			t.Errorf("response %d = %+v", r.Index, resp)
		}
	}
	// The second iteration's Create: the body, and the request as sent in
	// the timeline.
	create, err := f.svc.Response(t.Context(), id, 9)
	if err != nil {
		t.Fatal(err)
	}
	if create.Method != "POST" || create.URL != srv.URL+"/create" || !strings.Contains(create.Body, `"/create"`) ||
		len(create.Timeline.Hops) == 0 || create.Timeline.Hops[0].Request.Method != "POST" || len(create.Tests) != 2 {
		t.Fatalf("create = %+v", create)
	}

	f.svc.Release(t.Context(), id)
	_, err = f.svc.Response(t.Context(), id, 9)
	checkCode(t, err, errs.NotFound, CodeResponseGone)
	_, err = f.svc.Response(t.Context(), "op_unknown", 1)
	checkCode(t, err, errs.NotFound, CodeResponseGone)
}

// TestRunCancel: a run stops during a request (not listed, and nothing
// after it is sent) and during a delay.
func TestRunCancel(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"a/_folder.json": `{"kind": "folder", "id": "fld_a", "name": "A", "order": ["first.json", "block.json", "never.json"]}`,
		"a/first.json":   httpFile("req_first", "First", "GET", "/first", ""),
		"a/block.json":   httpFile("req_block", "Block", "GET", "/block", ""),
		"a/never.json":   httpFile("req_never", "Never", "GET", "/never", ""),
	}

	t.Run("during a request", func(t *testing.T) {
		t.Parallel()
		srv := newServer(t)
		f := newFixture(t, srv.URL, files)
		id := f.start(t, RunInputDTO{FolderID: "fld_a", Iterations: 5})
		select {
		case <-srv.blocked:
		case <-time.After(20 * time.Second):
			t.Fatal("the blocking request was not sent")
		}
		if !f.ops.Cancel(id) {
			t.Fatal("Cancel = false")
		}
		done := f.rec.wait(t, id)
		if done.Status != operation.StatusCancelled {
			t.Fatalf("done = %+v", done)
		}
		results := f.rec.resultsOf(id)
		if len(results) != 1 || results[0].RequestID != "req_first" {
			t.Fatalf("results = %+v", results)
		}
		if got := srv.got(); !slices.Equal(got, []string{"/first", "/block"}) {
			t.Fatalf("paths = %v", got)
		}
		// The cut-short send is recorded only because it is the only
		// one of its request.
		if names := f.hist.names(); !slices.Equal(names, []string{"First", "Block"}) {
			t.Errorf("history = %v", names)
		}
	})

	t.Run("during a delay", func(t *testing.T) {
		t.Parallel()
		srv := newServer(t)
		f := newFixture(t, srv.URL, files)
		f.svc.sleep = corerunner.Sleep // a real hour-long wait
		id := f.start(t, RunInputDTO{FolderID: "fld_a", Iterations: 1, DelayMs: 3_600_000})
		f.rec.waitResults(t, id, 1)
		f.ops.Cancel(id)
		done := f.rec.wait(t, id)
		if done.Status != operation.StatusCancelled || done.Items != 1 {
			t.Fatalf("done = %+v", done)
		}
		if got := srv.got(); !slices.Equal(got, []string{"/first"}) {
			t.Fatalf("paths = %v", got)
		}
	})
}

// TestRunStreams: an SSE request is read until the server ends it or the
// window passes; a status other than 2xx fails it, and so does a failed
// assertion on its events (TEST-08).
func TestRunStreams(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	f := newFixture(t, srv.URL, map[string]string{
		"s/_folder.json": `{"kind": "folder", "id": "fld_s", "name": "S", "order": ["ends.json", "open.json", "missing.json", "checked.json"]}`,
		"s/checked.json": `{"kind": "sse", "id": "req_checked", "name": "Checked", "url": "{{base}}/events", "auth": {"type": "none"}, "assertions": [` +
			`{"target": "message_count", "op": "equals", "expected": "2"}, {"messages": "last", "target": "header", "expr": "Event", "op": "equals", "expected": "tick"},` +
			`{"messages": "nth", "index": 1, "target": "body", "op": "equals", "expected": "two"}]}`,
		"s/ends.json":    `{"kind": "sse", "id": "req_ends", "name": "Ends", "url": "{{base}}/events", "auth": {"type": "none"}}`,
		"s/open.json":    `{"kind": "sse", "id": "req_open", "name": "Open", "url": "{{base}}/forever", "auth": {"type": "none"}}`,
		"s/missing.json": `{"kind": "sse", "id": "req_missing", "name": "Missing", "url": "{{base}}/missing-stream", "auth": {"type": "none"}}`,
	})
	id := f.start(t, RunInputDTO{FolderID: "fld_s", Iterations: 1, StreamMs: 200})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	results := f.rec.resultsOf(id)
	if len(results) != 4 {
		t.Fatalf("results = %+v", results)
	}
	if r := results[0]; r.Outcome != OutcomePassed || r.Events != 2 || r.StatusCode != 200 {
		t.Errorf("ends = %+v", r)
	}
	if r := results[1]; r.Outcome != OutcomePassed || r.Events != 1 || r.Error != nil {
		t.Errorf("open = %+v", r)
	}
	if r := results[2]; r.Outcome != OutcomeFailed || r.StatusCode != 404 || r.Error != nil {
		t.Errorf("missing = %+v", r)
	}
	if r := results[3]; r.Outcome != OutcomeFailed || r.TestsPassed != 2 || r.TestsFailed != 1 || len(r.Tests) != 3 ||
		r.Tests[1].Expr != "last: Event" || r.Tests[2].Expr != "message 1:" ||
		r.Tests[2].Message != `Expected message 1's body to equal "two", but it was "one".` {
		t.Errorf("checked = %+v", r)
	}
	if r := results[0]; len(r.Tests) != 0 || r.TestsPassed != 0 {
		t.Errorf("ends tests = %+v", r.Tests)
	}
	// Streams are not recorded in the history.
	if names := f.hist.names(); len(names) != 0 {
		t.Errorf("history = %v", names)
	}
}

// TestRunPrompts: the {{$prompt}} values of all the requests are asked
// for once, before the run, and used by every request.
func TestRunPrompts(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	token := `, "headers": [{"name": "X-Token", "value": "{{$prompt Token}}"}]`
	f := newFixture(t, srv.URL, map[string]string{
		"p/_folder.json": `{"kind": "folder", "id": "fld_p", "name": "P", "order": ["one.json", "two.json"]}`,
		"p/one.json":     httpFile("req_one", "One", "GET", "/one", token),
		"p/two.json":     httpFile("req_two", "Two", "GET", `/{{$prompt Path \"two\"}}`, token),
	})
	_, err := f.svc.Start(t.Context(), RunInputDTO{FolderID: "fld_p", Iterations: 1})
	checkCode(t, err, errs.InvalidArgument, template.CodePrompt)
	e, _ := errors.AsType[*errs.Error](err)
	var prompts []template.Prompt
	if err := json.Unmarshal([]byte(e.Details["prompts"]), &prompts); err != nil {
		t.Fatal(err)
	}
	want := []template.Prompt{{Label: "Token"}, {Label: "Path", Default: "two"}}
	if !slices.Equal(prompts, want) {
		t.Fatalf("prompts = %+v, want %+v", prompts, want)
	}
	if got := srv.got(); len(got) != 0 {
		t.Fatalf("sent before the prompts were answered: %v", got)
	}

	id := f.start(t, RunInputDTO{FolderID: "fld_p", Iterations: 2, Prompts: map[string]string{"Token": "s3cret", "Path": "two"}})
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK || done.Items != 4 {
		t.Fatalf("done = %+v", done)
	}
	srv.mu.Lock()
	paths, auth := slices.Clone(srv.paths), slices.Clone(srv.auth)
	srv.mu.Unlock()
	if !slices.Equal(paths, []string{"/one", "/two", "/one", "/two"}) || !slices.Equal(auth, []string{"s3cret", "s3cret", "s3cret", "s3cret"}) {
		t.Fatalf("paths = %v, tokens = %v", paths, auth)
	}
}

func TestStartRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://localhost:1", map[string]string{
		"empty/_folder.json": `{"kind": "folder", "id": "fld_empty", "name": "Empty"}`,
		"a/_folder.json":     `{"kind": "folder", "id": "fld_a", "name": "A"}`,
		"a/one.json":         httpFile("req_one", "One", "GET", "/one", ""),
	})
	tests := []struct {
		name string
		in   RunInputDTO
		kind errs.Kind
		code string
	}{
		{"no iterations", RunInputDTO{FolderID: "fld_a"}, errs.InvalidArgument, corerunner.CodeInvalidOptions},
		{"too many iterations", RunInputDTO{FolderID: "fld_a", Iterations: corerunner.MaxIterations + 1}, errs.InvalidArgument, corerunner.CodeInvalidOptions},
		{"negative delay", RunInputDTO{FolderID: "fld_a", Iterations: 1, DelayMs: -1}, errs.InvalidArgument, corerunner.CodeInvalidOptions},
		{"huge delay", RunInputDTO{FolderID: "fld_a", Iterations: 1, DelayMs: 1 << 62}, errs.InvalidArgument, corerunner.CodeInvalidOptions},
		{"long window", RunInputDTO{FolderID: "fld_a", Iterations: 1, StreamMs: 3_600_000}, errs.InvalidArgument, corerunner.CodeInvalidOptions},
		{"empty folder", RunInputDTO{FolderID: "fld_empty", Iterations: 1}, errs.InvalidArgument, CodeEmpty},
		{"a request", RunInputDTO{FolderID: "req_one", Iterations: 1}, errs.InvalidArgument, collection.CodeNotFolder},
		{"unknown folder", RunInputDTO{FolderID: "fld_gone", Iterations: 1}, errs.NotFound, "collection.not_found"},
		{"nothing chosen", RunInputDTO{FolderID: "fld_a", Iterations: 1, RequestIDs: []string{}}, errs.InvalidArgument, CodeEmpty},
		{"a request of another folder", RunInputDTO{FolderID: "fld_a", Iterations: 1, RequestIDs: []string{"req_one", "req_other"}}, errs.InvalidArgument, corerunner.CodeInvalidSelection},
		{"a request twice", RunInputDTO{FolderID: "fld_a", Iterations: 1, RequestIDs: []string{"req_one", "req_one"}}, errs.InvalidArgument, corerunner.CodeInvalidSelection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.Start(t.Context(), tt.in)
			checkCode(t, err, tt.kind, tt.code)
		})
	}

	_, err := New(nil, nil, nil, nil).Start(t.Context(), RunInputDTO{FolderID: "fld_a", Iterations: 1})
	checkCode(t, err, errs.Unsupported, CodeUnavailable)
	_, err = New(nil, nil, nil, nil).Plan(t.Context(), "fld_a")
	checkCode(t, err, errs.Unsupported, CodeUnavailable)
}
