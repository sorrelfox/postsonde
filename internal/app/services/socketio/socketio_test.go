package socketio

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	coresio "github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects the events of one registry.
type recorder struct {
	mu      sync.Mutex
	items   map[string][]ItemDTO
	arrived chan struct{}
	done    map[string]chan operation.DoneEventDTO
}

func newRecorder() *recorder {
	return &recorder{items: map[string][]ItemDTO{}, arrived: make(chan struct{}, 1), done: map[string]chan operation.DoneEventDTO{}}
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
	case operation.DataEventDTO[ItemDTO]:
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

func (r *recorder) list(id string) []ItemDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ItemDTO(nil), r.items[id]...)
}

func (r *recorder) waitFor(t *testing.T, id, what string, ok func(ItemDTO) bool) ItemDTO {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, it := range r.list(id) {
			if ok(it) {
				return it
			}
		}
		select {
		case <-r.arrived:
		case <-deadline:
			t.Fatalf("no %s item; items = %+v", what, r.list(id))
			return ItemDTO{}
		}
	}
}

type fixture struct {
	svc *SocketIOService
	ops *operation.Registry
	rec *recorder
}

// newFixture builds the service as the app does, connecting through a
// dialer that refuses dials without the user-operation mark (PRV-05).
func newFixture(t *testing.T, vars map[string]string) *fixture {
	t.Helper()
	f := &fixture{rec: newRecorder()}
	f.ops = operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.ops.Shutdown(ctx)
	})
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	scopes := func(ctx context.Context, id string) (template.Chain, error) {
		if id != "req_1" {
			return nil, nil
		}
		return template.Chain{template.Vars("Globals", vars)}, nil
	}
	f.svc = New(f.ops, dialer, nil, nil, WithScopes(scopes), withInterval(5*time.Millisecond))
	return f
}

func (f *fixture) connect(t *testing.T, in ConnectInputDTO) string {
	t.Helper()
	started, err := f.svc.Connect(t.Context(), in)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return started.OpID
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func isType(typ string) func(ItemDTO) bool { return func(it ItemDTO) bool { return it.Type == typ } }

// normalize blanks what changes from run to run, for the golden file.
func normalize(items []ItemDTO, url string) []ItemDTO {
	out := make([]ItemDTO, 0, len(items))
	for _, it := range items {
		if it.Type == ItemPing || it.Type == ItemPong {
			continue
		}
		it.At = ""
		if it.SID != "" {
			it.SID = "<sid>"
		}
		if it.SocketID != "" {
			it.SocketID = "<socket id>"
		}
		it.URL = strings.Replace(it.URL, url, "http://<server>", 1)
		if it.Type == ItemAck && it.Event != "" {
			it.ElapsedMs = 1
		}
		if strings.Contains(it.Data, `"sid"`) {
			it.Data = `{"sid":"<socket id>"}`
		}
		out = append(out, it)
	}
	return out
}

// TestConnectEmitDisconnect runs a whole connection through the service:
// {{variables}}, a namespace and auth, an emit with JSON and binary
// arguments and an ack, the echo, and a disconnect by cancelling the
// operation (SIO-01, SIO-03, SIO-05, SIO-06, SIO-07).
func TestConnectEmitDisconnect(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{Path: "/rt/", Namespaces: []string{"/admin"}, Authorize: func(nsp string, auth json.RawMessage) error {
		if string(auth) != `{"token":"t0k"}` {
			return errors.New("bad token")
		}
		return nil
	}}, t.Cleanup)
	f := newFixture(t, map[string]string{"host": strings.TrimPrefix(srv.URL, "http://"), "token": "t0k", "ev": "echo"})
	id := f.connect(t, ConnectInputDTO{
		RequestID:   "req_1",
		URL:         "http://{{host}}/admin",
		Path:        "/rt/",
		AuthPayload: `{"token": "{{token}}"}`,
		Transport:   "polling",
		Headers:     []HeaderDTO{{Name: "X-Test", Value: "{{token}}", Enabled: true}, {Name: "X-Off", Value: "{{nope}}"}},
	})
	f.rec.waitFor(t, id, "connected", isType(ItemConnected))
	res, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "{{ev}}", Args: `["hi", {"b": 2, "a": 1}, {"$hex": "00ff"}]`, Ack: true})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if res.AckID == nil || *res.AckID != 0 || res.Attachments != 1 || res.Size == 0 {
		t.Errorf("Emit = %+v", res)
	}
	f.rec.waitFor(t, id, "ack", isType(ItemAck))
	f.ops.Cancel(id)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled || done.Error != nil {
		t.Errorf("done = %+v", done)
	}
	items := normalize(f.rec.list(id), srv.URL)
	data, _ := json.MarshalIndent(items, "", "  ")
	golden.Assert(t, "dto/items", append(data, '\n'))

	for _, r := range srv.Requests() {
		if r.Header.Get("X-Test") != "t0k" || r.Header.Get("X-Off") != "" {
			t.Errorf("headers = %v", r.Header)
		}
	}
	if _, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "x"}); codeOf(err) != CodeNotFound {
		t.Errorf("Emit after the end: %v", err)
	}
}

// TestTransports: the service connects over every transport.
func TestTransports(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	f := newFixture(t, nil)
	for _, tr := range []string{"", "auto", "websocket", "polling", "upgrade"} {
		id := f.connect(t, ConnectInputDTO{URL: srv.URL, Transport: tr})
		c := f.rec.waitFor(t, id, "connected", isType(ItemConnected))
		want := "websocket"
		if tr == "polling" {
			want = "polling"
		}
		if c.Transport != want {
			t.Errorf("%q: transport = %q", tr, c.Transport)
		}
		if tr == "upgrade" {
			f.rec.waitFor(t, id, "upgrade", isType(ItemUpgrade))
		}
		f.ops.Cancel(id)
		f.rec.wait(t, id)
	}
}

// TestConnectFailures: a refused auth, a refused handshake, undefined
// variables and bad options.
func TestConnectFailures(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{Authorize: func(string, json.RawMessage) error { return errors.New("nope") }}, t.Cleanup)
	f := newFixture(t, map[string]string{"host": strings.TrimPrefix(srv.URL, "http://")})

	id := f.connect(t, ConnectInputDTO{URL: srv.URL, AuthPayload: `{"token":"x"}`})
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != sioclient.CodeConnectError ||
		done.Error.Details["message"] != "nope" {
		t.Errorf("done = %+v", done)
	}
	closed := f.rec.waitFor(t, id, "closed", isType(ItemClosed))
	if closed.Reason != sioclient.ReasonConnectFailed || closed.Error == nil {
		t.Errorf("closed = %+v", closed)
	}
	f.rec.waitFor(t, id, "CONNECT_ERROR", func(it ItemDTO) bool {
		return it.PacketType == "CONNECT_ERROR" && it.Direction == "received" && strings.Contains(it.Data, "nope")
	})

	_, err := f.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_1", URL: "http://{{host}}/{{ns}}", AuthPayload: `{"t":"{{tok}}"}`})
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != template.CodeUnresolved || e.Details["names"] != "ns,tok" {
		t.Errorf("unresolved: %#v", err)
	}
	for _, in := range []ConnectInputDTO{
		{URL: "ftp://h"},
		{URL: "http://h", AuthPayload: "[1]"},
		{URL: "http://h", Transport: "pigeon"},
	} {
		if _, err := f.svc.Connect(t.Context(), in); errs.KindOf(err) != errs.InvalidArgument || !strings.HasPrefix(codeOf(err), "sioclient.") {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

// TestServerDisconnect: the server ending the connection ends the
// operation with "ok", and it does not reconnect.
func TestServerDisconnect(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	f := newFixture(t, nil)
	id := f.connect(t, ConnectInputDTO{URL: srv.URL, Transport: "websocket"})
	f.rec.waitFor(t, id, "connected", isType(ItemConnected))
	if _, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "bye"}); err != nil {
		t.Fatal(err)
	}
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Errorf("done = %+v", done)
	}
	closed := f.rec.waitFor(t, id, "closed", isType(ItemClosed))
	if closed.Reason != sioclient.ReasonServerDisconnect || closed.Error != nil {
		t.Errorf("closed = %+v", closed)
	}
	n := len(srv.Requests())
	time.Sleep(100 * time.Millisecond)
	if len(srv.Requests()) != n {
		t.Errorf("the client reconnected")
	}
}

// TestEmitChecks: emits the service refuses, received events, and an ack
// that never comes.
func TestEmitChecks(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	f := newFixture(t, map[string]string{"x": "1"})
	ackTimeout := int64(50)
	id := f.connect(t, ConnectInputDTO{RequestID: "req_1", URL: srv.URL, AckTimeoutMs: &ackTimeout})
	f.rec.waitFor(t, id, "connected", isType(ItemConnected))
	ctx := t.Context()
	if _, err := f.svc.Emit(ctx, EmitInputDTO{OpID: "nope", Event: "x"}); codeOf(err) != CodeNotFound {
		t.Errorf("unknown op: %v", err)
	}
	if _, err := f.svc.Emit(ctx, EmitInputDTO{OpID: id, Event: " "}); codeOf(err) != CodeInvalidEvent {
		t.Errorf("no name: %v", err)
	}
	_, err := f.svc.Emit(ctx, EmitInputDTO{OpID: id, Event: "e", Args: `[1, }`})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeInvalidArgs || e.Details["offset"] != "4" {
		t.Errorf("bad args: %#v", err)
	}
	if _, err := f.svc.Emit(ctx, EmitInputDTO{OpID: id, Event: "e{{y}}", Args: `["{{z}}"]`}); codeOf(err) != template.CodeUnresolved {
		t.Errorf("unresolved: %v", err)
	}
	if _, err := f.svc.Emit(ctx, EmitInputDTO{OpID: id, Event: "connect"}); codeOf(err) != coresio.CodeReservedEvent {
		t.Errorf("reserved: %v", err)
	}
	if _, err := f.svc.Emit(ctx, EmitInputDTO{OpID: id, Event: "noack", Args: `"{{x}}{{y}}"`, Ack: true, AllowUnresolved: true}); err != nil {
		t.Fatal(err)
	}
	sent := f.rec.waitFor(t, id, "noack", func(it ItemDTO) bool { return it.Type == ItemEvent && it.Event == "noack" })
	if sent.Args != `["1{{y}}"]` || sent.Direction != "sent" || sent.AckID == nil {
		t.Errorf("sent = %+v", sent)
	}
	to := f.rec.waitFor(t, id, "ack timeout", isType(ItemAckTimeout))
	if to.Event != "noack" || *to.AckID != *sent.AckID {
		t.Errorf("timeout = %+v", to)
	}

	for _, s := range srv.Sessions() {
		s.Emit("/", "news", map[string]any{"a": 1}, []byte{1, 2})
	}
	news := f.rec.waitFor(t, id, "news", func(it ItemDTO) bool { return it.Event == "news" })
	if news.Direction != "received" || news.Args != `[{"a":1},{"$base64":"AQI="}]` || news.Attachments != 1 {
		t.Errorf("news = %+v", news)
	}

	OnWorkspaceChanged(f.svc)
	if done := f.rec.wait(t, id); done.Status != operation.StatusCancelled {
		t.Errorf("done = %+v", done)
	}
}

// TestParseArgs: the forms of the args text.
func TestParseArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []any
		code string
	}{
		{"", nil, ""},
		{"  \n", nil, ""},
		{`"one"`, []any{json.RawMessage(`"one"`)}, ""},
		{`[1, {"b": 2, "a": 1}]`, []any{json.RawMessage(`1`), json.RawMessage(`{"b":2,"a":1}`)}, ""},
		{`[[1, 2]]`, []any{json.RawMessage(`[1,2]`)}, ""},
		{`[{"$base64": "AAE="}, {"$hex": "ff 00"}]`, []any{[]byte{0, 1}, []byte{0xff, 0}}, ""},
		{`{"file": {"$base64": "_-8"}, "n": 1.50}`, []any{map[string]any{"file": []byte{0xff, 0xef}, "n": json.Number("1.50")}}, ""},
		{`{"$base64": "AA", "x": 1}`, []any{map[string]any{"$base64": "AA", "x": json.Number("1")}}, ""},
		{`[1,`, nil, CodeInvalidArgs},
		{`1 2`, nil, CodeInvalidArgs},
		{`{"$hex": "zz"}`, nil, CodeInvalidArgs},
		{`{"$base64": "!!"}`, nil, CodeInvalidArgs},
	}
	for _, tt := range tests {
		got, err := parseArgs(tt.in)
		if codeOf(err) != tt.code {
			t.Errorf("%q: err = %v", tt.in, err)
			continue
		}
		if tt.code == "" && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

// TestSocketIOv2 (SIO-02): the service connects to a socket.io v2
// reference server with version "v2": the client's pings are sent items
// and the server's pongs received ones; an emit with an ack goes both
// ways. An auth payload is refused with v2.
func TestSocketIOv2(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{V2: true, PingInterval: 30 * time.Millisecond, PingTimeout: time.Second}, t.Cleanup)
	f := newFixture(t, nil)
	for _, tr := range []string{"websocket", "polling", "upgrade"} {
		id := f.connect(t, ConnectInputDTO{URL: srv.URL, Transport: tr, Version: "v2"})
		f.rec.waitFor(t, id, "connected", isType(ItemConnected))
		f.rec.waitFor(t, id, "ping sent", func(it ItemDTO) bool { return it.Type == ItemPing && it.Direction == "sent" })
		f.rec.waitFor(t, id, "pong received", func(it ItemDTO) bool { return it.Type == ItemPong && it.Direction == "received" })
		if _, err := f.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "echo", Args: `[{"$hex": "00ff"}]`, Ack: true}); err != nil {
			t.Fatal(err)
		}
		f.rec.waitFor(t, id, "ack", func(it ItemDTO) bool { return it.Type == ItemAck && it.Event == "echo" })
		f.ops.Cancel(id)
		f.rec.wait(t, id)
	}
	if _, err := f.svc.Connect(t.Context(), ConnectInputDTO{URL: srv.URL, Version: "v2", AuthPayload: `{"a":1}`}); codeOf(err) != sioclient.CodeInvalidAuth {
		t.Errorf("auth with v2: %v", err)
	}
}
