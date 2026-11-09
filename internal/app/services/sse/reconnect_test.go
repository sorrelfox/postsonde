package sse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// fakeClock replaces time.After: every wait is reported on asked, and
// ends when the test sends on fire.
type fakeClock struct {
	asked chan time.Duration
	fire  chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{asked: make(chan time.Duration, 16), fire: make(chan time.Time)}
}

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	c.asked <- d
	return c.fire
}

// next waits for the stream to ask for a wait and returns its length.
func (c *fakeClock) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.asked:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not wait to reconnect")
		return 0
	}
}

// release ends the current wait.
func (c *fakeClock) release(t *testing.T) {
	t.Helper()
	select {
	case c.fire <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("nobody waits to reconnect")
	}
}

// reconnecting is an sse source whose reconnection policy is on.
func reconnecting(url string, maxAttempts *int, delayMs *int64) collection.HTTPSource {
	src := sseSource(url)
	on := true
	src.Request.Settings.Reconnect = &projectfile.SSEReconnect{Enabled: &on, MaxAttempts: maxAttempts, DelayMs: delayMs}
	return src
}

// headerLog records the Last-Event-ID of every request a server gets.
type headerLog struct {
	mu  sync.Mutex
	ids []string
}

func (l *headerLog) add(r *http.Request) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids = append(l.ids, r.Header.Get("Last-Event-ID"))
	return len(l.ids)
}

func (l *headerLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ids...)
}

func itemsOf(items []StreamItemDTO, typ string) []StreamItemDTO {
	var out []StreamItemDTO
	for _, it := range items {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out
}

// TestReconnectResumes (SSE-03): a connection dropped mid-stream is opened
// again after the server's retry with Last-Event-ID, and the stream goes on
// from the last event; one the server ends normally is resumed too; a 204
// stops reconnecting and ends the stream as closed.
func TestReconnectResumes(t *testing.T) {
	t.Parallel()
	var log headerLog
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := log.add(r)
		switch n {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "retry: 1234\nid: 1\ndata: a\n\nid: 2\ndata: b\n\ndata: part")
			w.(http.Flusher).Flush()
			// Drop the connection in the middle of an event.
			panic(http.ErrAbortHandler)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: c\n\nid: 3\ndata: d\n\n")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	f := newFixture(t, reconnecting(srv.URL, nil, nil))
	clock := newFakeClock()
	f.svc.after = clock.after
	id := f.connect(t)
	if d := clock.next(t); d != 1234*time.Millisecond {
		t.Errorf("first wait = %v, want the server's retry", d)
	}
	clock.release(t)
	if d := clock.next(t); d != 1234*time.Millisecond {
		t.Errorf("second wait = %v, want the server's retry", d)
	}
	clock.release(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if got := log.list(); len(got) != 3 || got[0] != "" || got[1] != "2" || got[2] != "3" {
		t.Errorf("Last-Event-ID sent = %q", got)
	}

	items := f.rec.list(id)
	events := itemsOf(items, ItemEvent)
	var data, ids []string
	for _, ev := range events {
		data, ids = append(data, ev.Data), append(ids, ev.ID)
	}
	if len(events) != 4 || data[2] != "c" || ids[2] != "2" || ids[3] != "3" || events[3].Index != 4 {
		t.Errorf("events: data %q, ids %q", data, ids)
	}
	re := itemsOf(items, ItemReconnect)
	if len(re) != 2 {
		t.Fatalf("reconnect items = %+v", re)
	}
	if re[0].Reason != EndError || re[0].Attempt != 1 || re[0].DelayMs != 1234 || re[0].ID != "2" || re[0].MaxAttempts != DefaultReconnectAttempts || re[0].Text == "" {
		t.Errorf("reconnect 1 = %+v", re[0])
	}
	if re[1].Reason != EndClosed || re[1].Attempt != 1 || re[1].ID != "3" {
		t.Errorf("reconnect 2 = %+v", re[1])
	}
	if opens := itemsOf(items, ItemOpen); len(opens) != 3 || opens[2].Status != http.StatusNoContent {
		t.Errorf("opens = %+v", opens)
	}
	notices := itemsOf(items, ItemNotice)
	if len(notices) != 1 || notices[0].Text != finalNoContent {
		t.Errorf("notices = %+v", notices)
	}
	end := items[len(items)-1]
	if end.Type != ItemEnd || end.Reason != EndClosed || end.Reconnects != 2 || end.Events != 4 || end.ID != "3" || end.RetryMs == nil || *end.RetryMs != 1234 {
		t.Errorf("end = %+v", end)
	}
}

// TestReconnectBackoff: attempts that deliver nothing wait twice as long
// each time, from the policy's delay, and the stream gives up after
// maxAttempts of them with sse.reconnect_failed.
func TestReconnectBackoff(t *testing.T) {
	t.Parallel()
	var log headerLog
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	f := newFixture(t, reconnecting(srv.URL, new(3), new(int64(100))))
	clock := newFakeClock()
	f.svc.after = clock.after
	id := f.connect(t)
	for i, want := range []time.Duration{100, 200, 400} {
		if d := clock.next(t); d != want*time.Millisecond {
			t.Errorf("wait %d = %v, want %v", i+1, d, want*time.Millisecond)
		}
		clock.release(t)
	}
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != CodeReconnectFailed || done.Error.Details["attempts"] != "3" {
		t.Fatalf("done = %+v", done)
	}
	if n := len(log.list()); n != 4 {
		t.Errorf("requests = %d, want 4", n)
	}
	items := f.rec.list(id)
	end := items[len(items)-1]
	if end.Reason != EndError || end.Reconnects != 3 {
		t.Errorf("end = %+v", end)
	}
	if re := itemsOf(items, ItemReconnect); len(re) != 3 || re[2].Attempt != 3 || re[2].DelayMs != 400 {
		t.Errorf("reconnect items = %+v", re)
	}
}

// TestReconnectStops: Stop while waiting to reconnect ends the stream as
// stopped at once; a non-2xx answer to a reconnect is not reconnected; a
// manual resume sends its Last-Event-ID on the first connection.
func TestReconnectStops(t *testing.T) {
	t.Parallel()
	var log headerLog
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := log.add(r)
		if r.URL.Path == "/fails" && n >= 3 {
			http.Error(w, "gone", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 5\ndata: x\n\n")
	}))
	defer srv.Close()

	f := newFixture(t, reconnecting(srv.URL, nil, nil))
	clock := newFakeClock()
	f.svc.after = clock.after
	started, err := f.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_sse", LastEventID: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if d := clock.next(t); d != DefaultReconnectDelay {
		t.Errorf("wait = %v, want the default", d)
	}
	if !f.svc.ops.Cancel(started.OpID) {
		t.Fatal("Cancel = false")
	}
	done := f.rec.wait(t, started.OpID)
	if done.Status != operation.StatusCancelled {
		t.Errorf("done = %+v", done)
	}
	items := f.rec.list(started.OpID)
	if end := items[len(items)-1]; end.Reason != EndStopped || end.Reconnects != 0 || end.ID != "5" {
		t.Errorf("end = %+v", end)
	}
	if got := log.list(); len(got) != 1 || got[0] != "4" {
		t.Errorf("Last-Event-ID sent = %q", got)
	}

	f.src = reconnecting(srv.URL+"/fails", nil, nil)
	id := f.connect(t)
	clock.next(t)
	clock.release(t)
	done = f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != CodeBadStatus {
		t.Errorf("fails: done = %+v", done)
	}
	if got := log.list(); len(got) != 3 || got[1] != "" || got[2] != "5" {
		t.Errorf("fails: Last-Event-ID sent = %q", got)
	}
}

// TestReconnectPolicy: the file's settings map onto the policy, missing
// and negative members taking the defaults; the backoff is capped.
func TestReconnectPolicy(t *testing.T) {
	t.Parallel()
	if got := ReconnectFrom(nil); got.Enabled || got.MaxAttempts != DefaultReconnectAttempts || got.Delay != DefaultReconnectDelay {
		t.Errorf("nil = %+v", got)
	}
	on := true
	got := ReconnectFrom(&projectfile.SSEReconnect{Enabled: &on, MaxAttempts: new(0), DelayMs: new(int64(250))})
	if !got.Enabled || got.MaxAttempts != 0 || got.Delay != 250*time.Millisecond {
		t.Errorf("set = %+v", got)
	}
	got = ReconnectFrom(&projectfile.SSEReconnect{MaxAttempts: new(-1), DelayMs: new(int64(-5))})
	if got.Enabled || got.MaxAttempts != DefaultReconnectAttempts || got.Delay != DefaultReconnectDelay {
		t.Errorf("negative = %+v", got)
	}

	c := &conn{policy: Reconnect{Delay: 10 * time.Second}}
	for n, want := range map[int]time.Duration{1: 10 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second, 4: time.Minute, 9: time.Minute} {
		if d := c.delay(n); d != want {
			t.Errorf("delay(%d) = %v, want %v", n, d, want)
		}
	}
	long := 2 * time.Minute
	c.retry = &long
	if d := c.delay(3); d != long {
		t.Errorf("delay with a long retry = %v", d)
	}
	zero := time.Duration(0)
	c.retry = &zero
	if d := c.delay(2); d != 0 {
		t.Errorf("delay with retry 0 = %v", d)
	}
}
