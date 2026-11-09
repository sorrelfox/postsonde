package sse

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects the events of one registry.
type recorder struct {
	mu    sync.Mutex
	items map[string][]StreamItemDTO
	// arrived is signalled on every data event.
	arrived chan struct{}
	done    map[string]chan operation.DoneEventDTO
}

func newRecorder() *recorder {
	return &recorder{items: map[string][]StreamItemDTO{}, arrived: make(chan struct{}, 1), done: map[string]chan operation.DoneEventDTO{}}
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
	case operation.DataEventDTO[StreamItemDTO]:
		if name != EventData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
		r.mu.Unlock()
		select {
		case r.arrived <- struct{}{}:
		default:
		}
	case operation.DoneEventDTO:
		if name != EventDone {
			panic("done event named " + name)
		}
		r.doneChan(d.OpID) <- d
	}
}

func (r *recorder) wait(t *testing.T, id string) operation.DoneEventDTO {
	t.Helper()
	select {
	case d := <-r.doneChan(id):
		return d
	case <-time.After(10 * time.Second):
		t.Fatalf("operation %s did not finish", id)
		return operation.DoneEventDTO{}
	}
}

func (r *recorder) list(id string) []StreamItemDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StreamItemDTO(nil), r.items[id]...)
}

// waitFor waits until an item of the operation satisfies ok.
func (r *recorder) waitFor(t *testing.T, id string, ok func(StreamItemDTO) bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, it := range r.list(id) {
			if ok(it) {
				return
			}
		}
		select {
		case <-r.arrived:
		case <-deadline:
			t.Fatalf("items = %+v", r.list(id))
		}
	}
}

type fixture struct {
	svc *SSEService
	rec *recorder
	mu  sync.Mutex
	src collection.HTTPSource
}

// newFixture builds the service as the app does: over a real HTTPService
// whose loader returns src, connecting through a guarded dialer.
func newFixture(t *testing.T, src collection.HTTPSource) *fixture {
	t.Helper()
	f := &fixture{rec: newRecorder(), src: src}
	ops := operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	load := func(ctx context.Context, id string) (collection.HTTPSource, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if id != f.src.Request.ID {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request").WithCode("collection.not_found")
		}
		return f.src, nil
	}
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	httpSvc := httpservice.New(load, nil, ops, dialer, nil, nil)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	f.svc = New(func(ctx context.Context, id string, allow bool) (Target, error) {
		st, err := httpservice.NewStream(ctx, httpSvc, id, allow)
		if err != nil {
			return Target{}, err
		}
		return Target{Stream: st, Method: st.Method, URL: st.URL, Reconnect: ReconnectFrom(st.Reconnect), Checks: st.Checks, Mask: st.Mask}, nil
	}, ops, nil)
	return f
}

func sseSource(url string) collection.HTTPSource {
	return collection.HTTPSource{
		Request: projectfile.HTTPRequest{ID: "req_sse", Method: "GET", URL: url},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
		SSE:     true,
	}
}

func (f *fixture) connect(t *testing.T) string {
	t.Helper()
	started, err := f.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_sse"})
	if err != nil {
		t.Fatalf("Connect = %v", err)
	}
	return started.OpID
}

// stable blanks what changes from run to run, for the golden file.
func stable(items []StreamItemDTO, base string) []StreamItemDTO {
	out := make([]StreamItemDTO, len(items))
	for i, it := range items {
		if _, err := time.Parse(time.RFC3339Nano, it.At); err != nil {
			panic("bad time " + it.At)
		}
		it.At = ""
		it.ElapsedMs = 0
		it.URL = strings.ReplaceAll(it.URL, base, "http://server")
		var hs []HeaderDTO
		for _, h := range it.Headers {
			if h.Name != "Date" {
				hs = append(hs, h)
			}
		}
		if it.Headers != nil {
			it.Headers = append([]HeaderDTO{}, hs...)
		}
		out[i] = it
	}
	return out
}

// TestConnect reads a stream written in pieces: multi-line data, event
// types, ids that carry over, retry, comments, an ignored retry, CRLF
// line ends and an event cut off at the end. {{variables}} are rendered.
// The items are pinned by testdata/dto/stream.golden.
func TestConnect(t *testing.T) {
	t.Parallel()
	chunks := []string{
		": welcome\n\nretry: 3000\n",
		"event: price\nid: 1\ndata: {\"sym\":\"ACME\",",
		"\ndata: \"px\":12.5}\n\n",
		"data: line one\r\ndata: line two\r\n\r\n",
		"retry: soon\nid: 2\nretry: 1500\nevent: tick\ndata: t\n\n: ping\n",
		"data: dropped",
	}
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	src := sseSource(srv.URL + "/{{feed}}")
	src.FolderVariables = [][]projectfile.Variable{{{Name: "feed", Value: "prices"}}}
	f := newFixture(t, src)
	id := f.connect(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if gotPath != "/prices" {
		t.Errorf("path = %q", gotPath)
	}
	items := f.rec.list(id)
	data, err := json.MarshalIndent(stable(items, srv.URL), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/stream", append(data, '\n'))

	var events []StreamItemDTO
	for _, it := range items {
		if it.Type == ItemEvent {
			events = append(events, it)
		}
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Event != "price" || events[0].ID != "1" || events[0].Data != "{\"sym\":\"ACME\",\n\"px\":12.5}" {
		t.Errorf("event 1 = %+v", events[0])
	}
	if events[1].Event != "message" || events[1].ID != "1" || events[1].Data != "line one\nline two" {
		t.Errorf("event 2 = %+v", events[1])
	}
	if events[2].Event != "tick" || events[2].ID != "2" || events[2].RetryMs == nil || *events[2].RetryMs != 1500 {
		t.Errorf("event 3 = %+v", events[2])
	}
	end := items[len(items)-1]
	if end.Type != ItemEnd || end.Reason != EndClosed || end.Events != 3 || !end.Discarded || end.ID != "2" || end.RetryMs == nil || *end.RetryMs != 1500 {
		t.Errorf("end = %+v", end)
	}
}

// TestConnectLive: events arrive while the server keeps the stream open,
// and cancelling the operation (Stop, a closed tab) ends it as cancelled
// with an end item, and the server sees the connection go.
func TestConnectLive(t *testing.T) {
	t.Parallel()
	next := make(chan string)
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		for {
			select {
			case s := <-next:
				_, _ = io.WriteString(w, s)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				close(gone)
				return
			}
		}
	}))
	defer srv.Close()

	src := sseSource(srv.URL)
	// The request timeout covers the headers only.
	src.Request.Settings.TimeoutMs = new(int64(100))
	f := newFixture(t, src)
	id := f.connect(t)
	next <- "data: one\n\n"
	f.rec.waitFor(t, id, func(it StreamItemDTO) bool { return it.Type == ItemEvent && it.Data == "one" })
	time.Sleep(150 * time.Millisecond)
	next <- "id: 7\ndata: two\n\n"
	f.rec.waitFor(t, id, func(it StreamItemDTO) bool { return it.Type == ItemEvent && it.Data == "two" })

	ops := f.svc.ops
	if !ops.Cancel(id) {
		t.Fatal("Cancel = false")
	}
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled {
		t.Errorf("done = %+v", done)
	}
	items := f.rec.list(id)
	end := items[len(items)-1]
	if end.Type != ItemEnd || end.Reason != EndStopped || end.Events != 2 || end.ID != "7" {
		t.Errorf("end = %+v", end)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Error("the server still has the connection")
	}
}

// TestConnectFailures: a non-2xx status shows the body and fails; a
// response that is not declared as a stream is read with a notice; gzip
// is decoded; a server that is down fails with no items; undefined
// variables refuse at once.
func TestConnectFailures(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such stream", http.StatusNotFound)
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "data: x\n\n")
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, "data: zipped\n\n")
		_ = zw.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := newFixture(t, sseSource(srv.URL+"/missing"))
	id := f.connect(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != CodeBadStatus {
		t.Errorf("missing: done = %+v", done)
	}
	items := f.rec.list(id)
	if len(items) != 3 || items[0].Status != 404 || items[1].Type != ItemNotice || items[1].Text != "no such stream\n" || items[2].Reason != EndError {
		t.Errorf("missing: items = %+v", items)
	}

	f.src = sseSource(srv.URL + "/plain")
	id = f.connect(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Errorf("plain: done = %+v", done)
	}
	items = f.rec.list(id)
	if len(items) != 4 || items[1].Type != ItemNotice || !strings.Contains(items[1].Text, "text/plain") || items[2].Data != "x" {
		t.Errorf("plain: items = %+v", items)
	}

	f.src = sseSource(srv.URL + "/gzip")
	f.src.Request.Headers = []projectfile.KeyValue{{Name: "Accept-Encoding", Value: "gzip"}}
	id = f.connect(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Errorf("gzip: done = %+v", done)
	}
	if items := f.rec.list(id); len(items) != 3 || items[1].Data != "zipped" {
		t.Errorf("gzip: items = %+v", items)
	}

	f.src = sseSource("http://127.0.0.1:1/")
	id = f.connect(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusError || done.Items != 0 {
		t.Errorf("down: done = %+v", done)
	}

	f.src = sseSource(srv.URL + "/{{nope}}")
	if _, err := f.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_sse"}); errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("unresolved: err = %v", err)
	}
	if _, err := New(nil, nil, nil).Connect(t.Context(), ConnectInputDTO{}); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("unavailable: err = %v", err)
	}
}
