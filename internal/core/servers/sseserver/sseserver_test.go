package sseserver_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sse"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
)

type events struct {
	mu   sync.Mutex
	list []servers.Event
}

func (e *events) add(ev servers.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) all() []servers.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

// waitFor polls until an event satisfies ok and returns it.
func (e *events) waitFor(t *testing.T, what string, ok func(servers.Event) bool) servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, ev := range e.all() {
			if ok(ev) {
				return ev
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s event among %d", what, len(e.all()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	m    *servers.Manager
	id   string
	base string
	rec  *events
}

func start(t *testing.T, cfg sseserver.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{sseserver.New(sseserver.Options{KeepAliveUnit: 20 * time.Millisecond})},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: sseserver.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, id: s.ID, base: "http://127.0.0.1:" + strconv.Itoa(s.Port()), rec: rec}
}

func (f *fixture) hub(t *testing.T) sseserver.Hub {
	t.Helper()
	inst, err := f.m.Running(f.id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.(sseserver.Hub)
}

// waitSubscribers polls until the server lists n subscribers.
func (f *fixture) waitSubscribers(t *testing.T, n int) []sseserver.Subscriber {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		subs := f.hub(t).Subscribers()
		if len(subs) == n {
			return subs
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %+v, want %d", subs, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// eventSource follows the EventSource processing model of the WHATWG HTML
// standard ("Server-sent events"): GET with Accept: text/event-stream, no
// caching and Last-Event-ID once an event carried an ID; fail on a status
// other than 200 or a content type other than text/event-stream; parse
// the stream (the repo's sse parser), update the last event ID on every
// dispatch, and reconnect after the retry time when the stream ends. It
// reports what it sees on channels, so a test waits for events instead of
// sleeping.
type eventSource struct {
	url    string
	origin string

	events  chan sse.Event
	opens   chan http.Header
	failure chan error

	cancel context.CancelFunc
	done   chan struct{}
}

func openEventSource(t *testing.T, url, origin string) *eventSource {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	es := &eventSource{
		url: url, origin: origin,
		events: make(chan sse.Event, 100), opens: make(chan http.Header, 10), failure: make(chan error, 1),
		cancel: cancel, done: make(chan struct{}),
	}
	go es.run(ctx)
	t.Cleanup(es.close)
	return es
}

func (es *eventSource) close() {
	es.cancel()
	<-es.done
}

func (es *eventSource) run(ctx context.Context) {
	defer close(es.done)
	lastID := ""
	retry := time.Second // the user agent's default reconnection time; the server sets it
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, es.url, nil)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
		if es.origin != "" {
			req.Header.Set("Origin", es.origin)
		}
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				es.failure <- err
			}
			return
		}
		media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if resp.StatusCode != http.StatusOK || media != "text/event-stream" {
			resp.Body.Close()
			es.failure <- errors.New("fail the connection: " + resp.Status + " " + media)
			return
		}
		es.opens <- resp.Header
		p := sse.NewParser(sse.Options{LastEventID: lastID})
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				evs, perr := p.Feed(buf[:n])
				for _, ev := range evs {
					es.events <- ev
				}
				if perr != nil {
					err = perr
				}
			}
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		lastID = p.LastEventID()
		if d, ok := p.Retry(); ok {
			retry = d
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry): // reestablish the connection
		}
	}
}

func (es *eventSource) next(t *testing.T) sse.Event {
	t.Helper()
	select {
	case ev := <-es.events:
		return ev
	case err := <-es.failure:
		t.Fatalf("EventSource failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return sse.Event{}
}

func (es *eventSource) open(t *testing.T) http.Header {
	t.Helper()
	select {
	case h := <-es.opens:
		return h
	case err := <-es.failure:
		t.Fatalf("EventSource failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not open")
	}
	return nil
}

// TestEventSource checks the server without a browser: a client that
// follows the EventSource model receives pushed events (types, multi-line
// data, empty data, automatic IDs), sees the CORS headers a page on
// another origin needs, and after the app disconnects it, reconnects with
// Last-Event-ID, which the subscriber list and the traffic show.
func TestEventSource(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{Path: "/events", AllowOrigin: sseserver.OriginAny, RetryMs: 30, AutoID: true})
	es := openEventSource(t, f.base+"/events?room=1", "http://app.example.test:3000")
	h := es.open(t)
	if h.Get("Access-Control-Allow-Origin") != "http://app.example.test:3000" || h.Get("Access-Control-Allow-Credentials") != "true" || h.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers = %v", h)
	}
	subs := f.waitSubscribers(t, 1)
	if s := subs[0]; s.ID != "1" || s.Target != "/events?room=1" || s.Origin != "http://app.example.test:3000" || s.LastEventID != "" {
		t.Errorf("subscriber = %+v", s)
	}
	hub := f.hub(t)

	res, err := hub.Send("", sseserver.Message{Event: "greeting", Data: "line one\r\nline two\rline three\n"})
	if err != nil || res.Sent != 1 || res.ID != "1" {
		t.Fatalf("Send = %+v, %v", res, err)
	}
	if ev := es.next(t); ev.Type != "greeting" || ev.Data != "line one\nline two\nline three\n" || ev.ID != "1" {
		t.Errorf("event = %+v", ev)
	}
	if _, err := hub.Send("1", sseserver.Message{ID: "custom-7"}); err != nil {
		t.Fatal(err)
	}
	if ev := es.next(t); ev.Type != "message" || ev.Data != "" || ev.ID != "custom-7" {
		t.Errorf("empty event = %+v", ev)
	}
	out := f.rec.waitFor(t, "out", func(ev servers.Event) bool { return ev.Direction == servers.Out && ev.Kind == servers.KindMessage })
	if out.Summary != "Event “greeting” id 1 to 1 subscriber" || out.SSE.Recipients != 1 || out.SSE.Event != "greeting" || !strings.HasPrefix(string(out.Content), "line one") {
		t.Errorf("out event = %+v %+v", out, out.SSE)
	}

	if err := hub.Disconnect("1"); err != nil {
		t.Fatal(err)
	}
	f.rec.waitFor(t, "disconnect", func(ev servers.Event) bool {
		return ev.Kind == servers.KindConnectionClose && ev.SSE != nil && ev.SSE.InitiatedBy == sseserver.InitiatedByServer && ev.Summary == "Disconnected by the server (2 events sent)"
	})
	es.open(t) // reconnected after the server's retry
	subs = f.waitSubscribers(t, 1)
	if subs[0].ID != "2" || subs[0].LastEventID != "custom-7" {
		t.Errorf("after reconnect = %+v", subs[0])
	}
	f.rec.waitFor(t, "resumed response", func(ev servers.Event) bool {
		return ev.Kind == servers.KindResponse && ev.Summary == "200 OK: subscriber 2 (Last-Event-ID “custom-7”)" && ev.SSE.LastEventID == "custom-7"
	})
	if _, err := hub.Send("", sseserver.Message{Data: "after"}); err != nil {
		t.Fatal(err)
	}
	if ev := es.next(t); ev.Data != "after" || ev.ID != "2" {
		t.Errorf("after reconnect = %+v", ev)
	}
}

// TestTimer: a timer sends its event to every subscriber at its interval,
// with dynamic values rendered at each tick; turning it off stops it.
func TestTimer(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{Timers: []sseserver.Timer{
		{Enabled: true, Event: "tick", Data: `{"at": {{$timestamp}}}`, IntervalMs: 100},
		{Enabled: false, Event: "off", Data: "never", IntervalMs: 100},
	}})
	a := openEventSource(t, f.base+"/events", "")
	b := openEventSource(t, f.base+"/events", "")
	f.waitSubscribers(t, 2)
	for _, es := range []*eventSource{a, b} {
		for range 2 {
			ev := es.next(t)
			at, ok := strings.CutPrefix(ev.Data, `{"at": `)
			if _, err := strconv.ParseInt(strings.TrimSuffix(at, "}"), 10, 64); ev.Type != "tick" || !ok || err != nil {
				t.Errorf("tick = %+v", ev)
			}
		}
	}
	tick := f.rec.waitFor(t, "tick", func(ev servers.Event) bool { return ev.SSE != nil && ev.SSE.Source == "timer 1" })
	if !strings.HasSuffix(tick.Summary, "to 2 subscribers (timer 1)") && !strings.HasSuffix(tick.Summary, "to 1 subscriber (timer 1)") {
		t.Errorf("tick summary = %q", tick.Summary)
	}
	if _, err := f.m.UpdateConfig(f.id, sseserver.Config{}); err != nil {
		t.Fatal(err)
	}
	ticks := func() int {
		n := 0
		for _, ev := range f.rec.all() {
			if ev.SSE != nil && ev.SSE.Source != "" {
				n++
			}
		}
		return n
	}
	before := ticks()
	if _, err := f.hub(t).Send("", sseserver.Message{Data: "marker"}); err != nil {
		t.Fatal(err)
	}
	// Every tick sent before the marker is drained; none follows it.
	for _, es := range []*eventSource{a, b} {
		for ev := es.next(t); ev.Data != "marker"; ev = es.next(t) {
		}
	}
	if after := ticks(); after != before {
		t.Errorf("%d ticks after the timers were removed", after-before)
	}
}

// TestKeepAliveAndRetry reads the raw stream: the retry field first, then
// keep-alive comments.
func TestKeepAliveAndRetry(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{KeepAliveSec: 1, RetryMs: 2500})
	resp, err := http.Get(f.base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("headers = %v", resp.Header)
	}
	r := bufio.NewReader(resp.Body)
	var lines []string
	for len(lines) < 4 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if lines[0] != "retry: 2500\n" || lines[1] != "\n" || lines[2] != ": keep-alive\n" || lines[3] != "\n" {
		t.Errorf("stream = %q", lines)
	}
}

// TestRequests: a CORS preflight, another path, another method, each
// answered and logged.
func TestRequests(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{AllowOrigin: "http://localhost:3000/"})
	req, _ := http.NewRequest(http.MethodOptions, f.base+"/events", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "last-event-id")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" || resp.Header.Get("Access-Control-Allow-Headers") != "last-event-id" {
		t.Errorf("preflight = %d %v", resp.StatusCode, resp.Header)
	}
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/other", http.StatusNotFound},
		{http.MethodPost, "/events", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(tt.method, f.base+tt.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.status || len(body) == 0 {
			t.Errorf("%s %s = %d %q", tt.method, tt.path, resp.StatusCode, body)
		}
	}
	f.rec.waitFor(t, "405", func(ev servers.Event) bool {
		return ev.Kind == servers.KindResponse && ev.Summary == "405 Method Not Allowed (subscribe with GET)"
	})
	f.rec.waitFor(t, "preflight", func(ev servers.Event) bool {
		return ev.Kind == servers.KindResponse && ev.Summary == "204 No Content (CORS preflight)"
	})
}

// TestStop: every stream ends and is logged before the stopped event; the
// client sees the end of the response.
func TestStop(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{})
	resp, err := http.Get(f.base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	f.waitSubscribers(t, 1)
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Errorf("reading to the end: %v", err)
	}
	all := f.rec.all()
	var closed, stopped int
	for n, ev := range all {
		if ev.Kind == servers.KindConnectionClose && ev.SSE != nil {
			closed = n
			if ev.Summary != "Closed: the server is stopping (0 events sent)" {
				t.Errorf("close = %q", ev.Summary)
			}
		}
		if ev.Kind == servers.KindStopped {
			stopped = n
		}
	}
	if closed == 0 || stopped < closed {
		t.Errorf("close at %d, stopped at %d", closed, stopped)
	}
}

// TestClientLeaves: a subscriber that goes away is logged as left.
func TestClientLeaves(t *testing.T) {
	t.Parallel()
	f := start(t, sseserver.Config{})
	es := openEventSource(t, f.base+"/events", "")
	es.open(t)
	f.waitSubscribers(t, 1)
	es.close()
	f.rec.waitFor(t, "left", func(ev servers.Event) bool {
		return ev.Kind == servers.KindConnectionClose && ev.Summary == "Subscriber 1 left (0 events sent)" && ev.SSE.InitiatedBy == sseserver.InitiatedByClient
	})
	f.waitSubscribers(t, 0)
	_, err := f.hub(t).Send("1", sseserver.Message{Data: "x"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != sseserver.CodeSubscriberNotFound {
		t.Errorf("Send to a gone subscriber: %v", err)
	}
	if res, err := f.hub(t).Send("", sseserver.Message{Data: "x"}); err != nil || res.Sent != 0 {
		t.Errorf("Send to nobody = %+v, %v", res, err)
	}
}

// TestFrame: what Frame writes, parsed back with the sse parser, is the
// event sent, however the data breaks its lines.
func TestFrame(t *testing.T) {
	t.Parallel()
	for _, m := range []sseserver.Message{
		{Data: "plain"},
		{Event: "update", ID: "42", RetryMs: 1500, Data: "a\r\nb\rc\nd"},
		{Data: ""},
		{Data: "\n\n"},
		{Data: "  leading spaces"},
		{Data: "data: looks like a field\n: looks like a comment"},
	} {
		frame := sseserver.Frame(m)
		p := sse.NewParser(sse.Options{})
		evs, err := p.Feed(frame)
		if err != nil || len(evs) != 1 {
			t.Fatalf("%q: %d events, %v", frame, len(evs), err)
		}
		want := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(m.Data)
		wantType := m.Event
		if wantType == "" {
			wantType = "message"
		}
		if ev := evs[0]; ev.Data != want || ev.Type != wantType || ev.ID != m.ID {
			t.Errorf("%q parsed as %+v", frame, ev)
		}
		if d, ok := p.Retry(); m.RetryMs > 0 && (!ok || d != time.Duration(m.RetryMs)*time.Millisecond) {
			t.Errorf("retry = %v %v", d, ok)
		}
	}
	if got := string(sseserver.Frame(sseserver.Message{Event: "e", ID: "1", Data: "x\ny"})); got != "event: e\nid: 1\ndata: x\ndata: y\n\n" {
		t.Errorf("frame = %q", got)
	}
}

// TestConfig: bad configurations and messages are refused with the field.
func TestConfig(t *testing.T) {
	t.Parallel()
	typ := sseserver.New(sseserver.Options{})
	c, err := typ.NormalizeConfig(nil)
	if err != nil || c.(sseserver.Config).Path != "/events" || c.(sseserver.Config).KeepAliveSec != 15 {
		t.Errorf("default = %+v, %v", c, err)
	}
	for _, tt := range []struct {
		cfg          sseserver.Config
		field, timer string
	}{
		{sseserver.Config{Path: "events"}, "path", ""},
		{sseserver.Config{AllowOrigin: "localhost"}, "allowOrigin", ""},
		{sseserver.Config{KeepAliveSec: -1}, "keepAliveSec", ""},
		{sseserver.Config{RetryMs: sseserver.MaxRetryMs + 1}, "retryMs", ""},
		{sseserver.Config{Timers: []sseserver.Timer{{IntervalMs: 50}}}, "timers", "intervalMs"},
		{sseserver.Config{Timers: []sseserver.Timer{{IntervalMs: 1000, Event: "a\nb"}}}, "timers", "event"},
		{sseserver.Config{Timers: []sseserver.Timer{{IntervalMs: 1000, ID: "a\x00"}}}, "timers", "id"},
	} {
		_, err := typ.NormalizeConfig(tt.cfg)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != sseserver.CodeInvalidConfig || e.Details["field"] != tt.field || e.Details["timerField"] != tt.timer {
			t.Errorf("%+v: %v", tt.cfg, err)
		}
	}
	err = sseserver.CheckMessage(sseserver.Message{Data: "\xff"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != sseserver.CodeInvalidMessage || e.Details["field"] != "data" {
		t.Errorf("CheckMessage: %v", err)
	}
}
