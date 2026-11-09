package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
)

// hitServer counts the requests it gets; each hit is also sent to hits
// without blocking. With block set, a request is held until its client
// goes away.
type hitServer struct {
	*httptest.Server
	n     atomic.Int64
	hits  chan int64
	block bool
}

func newHitServer(t *testing.T, block bool) *hitServer {
	t.Helper()
	h := &hitServer{hits: make(chan int64, 1024), block: block}
	h.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n := h.n.Add(1)
		select {
		case h.hits <- n:
		default:
		}
		if h.block {
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, "hit %d", n)
	}))
	t.Cleanup(h.Close)
	return h
}

// waitHits waits until the server has had n requests.
func (h *hitServer) waitHits(t *testing.T, n int64) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for h.n.Load() < n {
		select {
		case <-h.hits:
		case <-timeout:
			t.Fatalf("hits = %d, want %d", h.n.Load(), n)
		}
	}
}

func (r *recorder) resultsOf(id string) []RepeatResultDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RepeatResultDTO(nil), r.results[id]...)
}

// waitResults waits until run id has listed n results.
func (r *recorder) waitResults(t *testing.T, id string, n int) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for len(r.resultsOf(id)) < n {
		select {
		case <-r.resultAdded:
		case <-timeout:
			t.Fatalf("results = %d, want %d", len(r.resultsOf(id)), n)
		}
	}
}

// historyEntries is a WithHistory target that keeps the entries.
type historyEntries struct {
	mu      sync.Mutex
	entries []history.Entry
}

func (h *historyEntries) option() Option {
	return WithHistory(func() func(history.Entry) {
		return func(e history.Entry) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.entries = append(h.entries, e)
		}
	})
}

func (h *historyEntries) list() []history.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]history.Entry(nil), h.entries...)
}

func (f *fixture) repeat(t *testing.T, count int, intervalMs int64) string {
	t.Helper()
	started, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_1", Count: count, IntervalMs: intervalMs})
	if err != nil {
		t.Fatalf("Repeat = %v", err)
	}
	return started.OpID
}

func TestRepeatCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		count      int
		intervalMs int64
	}{
		{"back to back", 5, 0},
		{"with an interval", 3, 20},
		{"once", 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newHitServer(t, false)
			var hist historyEntries
			f := newFixture(t, httpSource(srv.URL), nil, WithSaveBody(t.TempDir(), nil), hist.option())

			id := f.repeat(t, tt.count, tt.intervalMs)
			done := f.rec.wait(t, id)
			if done.Status != operation.StatusOK || done.Items != int64(tt.count) {
				t.Fatalf("done = %+v", done)
			}
			if got := srv.n.Load(); got != int64(tt.count) {
				t.Fatalf("hits = %d, want %d", got, tt.count)
			}
			results := f.rec.resultsOf(id)
			for i, r := range results {
				if r.Index != i+1 || r.StatusCode != 200 || r.Status != "200 OK" || r.Error != nil || r.Size == 0 {
					t.Fatalf("result %d = %+v", i, r)
				}
				if i > 0 && r.StartedAt-results[i-1].StartedAt < tt.intervalMs {
					t.Fatalf("result %d started %d ms after the one before, want at least %d", i, r.StartedAt-results[i-1].StartedAt, tt.intervalMs)
				}
			}
			// The first and the last carry the response, the last one's
			// body kept for the view.
			firstResp, lastResp := results[0].Response, results[len(results)-1].Response
			if firstResp == nil || lastResp == nil || lastResp.Body != fmt.Sprintf("hit %d", tt.count) || lastResp.BodyID == "" {
				t.Fatalf("responses = %+v, %+v", firstResp, lastResp)
			}
			// One history entry: the last send.
			entries := hist.list()
			if len(entries) != 1 || entries[0].Outcome != history.OutcomeOK || entries[0].StatusCode != 200 {
				t.Fatalf("history = %+v", entries)
			}
		})
	}
}

// A run without a count goes on until it is stopped, then sends nothing
// more.
func TestRepeatUntilStopped(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t, false)
	f := newFixture(t, httpSource(srv.URL), nil)

	id := f.repeat(t, 0, 1)
	srv.waitHits(t, 4)
	f.ops.Cancel(id)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled {
		t.Fatalf("done = %+v", done)
	}
	results := int64(len(f.rec.resultsOf(id)))
	hits := srv.n.Load()
	// A send cut short by the stop reached the server but is not listed.
	if results < 3 || hits < results || hits > results+1 {
		t.Fatalf("results = %d, hits = %d", results, hits)
	}
	// The run has returned: nothing can send any more.
	if again := f.ops.Cancel(id); again {
		t.Fatal("Cancel after done = true")
	}
}

// Stopping a run during a send cancels the send at once.
func TestRepeatStopDuringSend(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t, true)
	var hist historyEntries
	f := newFixture(t, httpSource(srv.URL), nil, hist.option())

	id := f.repeat(t, 3, 0)
	srv.waitHits(t, 1)
	f.ops.Cancel(id)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled || done.Items != 0 {
		t.Fatalf("done = %+v", done)
	}
	if got := srv.n.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
	// With no send finished, the cancelled one is the run's entry.
	if entries := hist.list(); len(entries) != 1 || entries[0].Outcome != history.OutcomeCancelled {
		t.Fatalf("history = %+v", entries)
	}
}

// Stopping a run while it waits for the next send ends it at once, not
// when the interval is over.
func TestRepeatStopDuringWait(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t, false)
	var hist historyEntries
	f := newFixture(t, httpSource(srv.URL), nil, hist.option())

	id := f.repeat(t, 0, MaxRepeatIntervalMs)
	f.rec.waitResults(t, id, 1)
	f.ops.Cancel(id)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled || done.Items != 1 {
		t.Fatalf("done = %+v", done)
	}
	if got := srv.n.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
	if entries := hist.list(); len(entries) != 1 || entries[0].Outcome != history.OutcomeOK {
		t.Fatalf("history = %+v", entries)
	}
}

// A second send stopped part way leaves the first as the history entry.
func TestRepeatStopDuringLaterSend(t *testing.T) {
	t.Parallel()
	var n atomic.Int64
	entered := make(chan struct{}, 8)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if n.Add(1) > 1 {
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "first")
	}))
	defer srv.Close()
	var hist historyEntries
	f := newFixture(t, httpSource(srv.URL), nil, hist.option())

	id := f.repeat(t, 5, 0)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the second send did not arrive")
	}
	f.ops.Cancel(id)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled || done.Items != 1 {
		t.Fatalf("done = %+v", done)
	}
	if entries := hist.list(); len(entries) != 1 || entries[0].Outcome != history.OutcomeOK {
		t.Fatalf("history = %+v", entries)
	}
}

// Failed sends are listed with their error and the run goes on.
func TestRepeatFailuresContinue(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.NotFoundHandler())
	url := srv.URL
	srv.Close()
	f := newFixture(t, httpSource(url), nil)

	id := f.repeat(t, 3, 0)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK || done.Items != 3 {
		t.Fatalf("done = %+v", done)
	}
	for i, r := range f.rec.resultsOf(id) {
		if r.Index != i+1 || r.StatusCode != 0 || r.Error == nil || r.Error.Message == "" || r.Response != nil {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
}

func TestRepeatInvalid(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t, false)
	f := newFixture(t, httpSource(srv.URL), nil)
	tests := []struct {
		name       string
		count      int
		intervalMs int64
		field      string
	}{
		{"negative count", -1, 0, "count"},
		{"count too large", MaxRepeatCount + 1, 0, "count"},
		{"negative interval", 2, -1, "intervalMs"},
		{"interval too long", 2, MaxRepeatIntervalMs + 1, "intervalMs"},
		{"until stopped without an interval", 0, 0, "intervalMs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_1", Count: tt.count, IntervalMs: tt.intervalMs})
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			checkErr(t, e, errs.InvalidArgument, CodeRepeatInvalid)
			if e.Details["field"] != tt.field {
				t.Fatalf("details = %v", e.Details)
			}
		})
	}
	if _, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_nope", Count: 2}); errs.KindOf(err) != errs.NotFound {
		t.Fatalf("unknown request: err = %v", err)
	}
	if got := srv.n.Load(); got != 0 {
		t.Fatalf("hits = %d, want 0", got)
	}
}
