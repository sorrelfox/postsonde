package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportws"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// The tests run subscriptions end to end: the real HTTP service builds
// the handshake from a GraphQL request file, the app's guarded dialer
// connects, and an in-process graphql-transport-ws server written here
// (on wsframe) plays the other side.

const subWait = 5 * time.Second

// gqlPeer is the server side of one test connection.
type gqlPeer struct {
	t       *testing.T
	conn    *wsframe.Conn
	nc      net.Conn
	headers nethttp.Header
}

type gqlMsg struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
	raw     string
}

func (p *gqlPeer) tryRead() (gqlMsg, error) {
	_ = p.nc.SetReadDeadline(time.Now().Add(subWait))
	op, data, err := p.conn.ReadMessage()
	if err != nil {
		return gqlMsg{}, err
	}
	if op != wsframe.OpText {
		return gqlMsg{}, errors.New("not a text message")
	}
	var m gqlMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return gqlMsg{}, err
	}
	m.raw = string(data)
	return m, nil
}

// expect reads the next message and checks its type.
func (p *gqlPeer) expect(typ string) gqlMsg {
	p.t.Helper()
	m, err := p.tryRead()
	if err != nil || m.Type != typ {
		p.t.Errorf("server got %q (%v), want type %q", m.raw, err, typ)
		panic(nethttp.ErrAbortHandler)
	}
	return m
}

func (p *gqlPeer) send(msg string) error {
	return p.conn.WriteMessage(wsframe.OpText, []byte(msg))
}

func (p *gqlPeer) mustSend(msg string) {
	p.t.Helper()
	if err := p.send(msg); err != nil {
		p.t.Errorf("server send: %v", err)
		panic(nethttp.ErrAbortHandler)
	}
}

// drain reads until the connection ends and returns the client's close
// frame, if any.
func (p *gqlPeer) drain() *wsframe.CloseError {
	for {
		if _, err := p.tryRead(); err != nil {
			return p.conn.PeerClose()
		}
	}
}

// startGraphQLServer upgrades every request with the graphql-transport-ws
// subprotocol and runs handler on the connection.
func startGraphQLServer(t *testing.T, handler func(p *gqlPeer)) *httptest.Server {
	t.Helper()
	return startGraphQLServerWith(t, transportws.Subprotocol, handler)
}

// startGraphQLServerWith is startGraphQLServer selecting subprotocol.
func startGraphQLServerWith(t *testing.T, subprotocol string, handler func(p *gqlPeer)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nc, brw, err := w.(nethttp.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer nc.Close()
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n" +
			"Sec-WebSocket-Protocol: " + subprotocol + "\r\n\r\n"
		if _, err := nc.Write([]byte(resp)); err != nil {
			t.Errorf("write 101: %v", err)
			return
		}
		handler(&gqlPeer{
			t:       t,
			conn:    wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader}),
			nc:      nc,
			headers: r.Header.Clone(),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// subRecorder collects the events of subscription operations.
type subRecorder struct {
	mu     sync.Mutex
	items  map[string][]SubscriptionEventDTO
	done   map[string]chan operation.DoneEventDTO
	notify chan struct{}
}

func (r *subRecorder) doneChan(id string) chan operation.DoneEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.done[id]
	if !ok {
		ch = make(chan operation.DoneEventDTO, 1)
		r.done[id] = ch
	}
	return ch
}

func (r *subRecorder) emit(name string, data any) {
	switch d := data.(type) {
	case operation.DataEventDTO[SubscriptionEventDTO]:
		if name != EventSubscriptionData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
		r.mu.Unlock()
		select {
		case r.notify <- struct{}{}:
		default:
		}
	case operation.DoneEventDTO:
		if name != EventSubscriptionDone {
			panic("done event named " + name)
		}
		r.doneChan(d.OpID) <- d
	}
}

func (r *subRecorder) events(id string) []SubscriptionEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SubscriptionEventDTO(nil), r.items[id]...)
}

// waitFor waits until the events of id satisfy ok.
func (r *subRecorder) waitFor(t *testing.T, id string, ok func([]SubscriptionEventDTO) bool) []SubscriptionEventDTO {
	t.Helper()
	deadline := time.After(subWait)
	for {
		if evs := r.events(id); ok(evs) {
			return evs
		}
		select {
		case <-r.notify:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("events so far: %s", summary(r.events(id)))
		}
	}
}

func (r *subRecorder) waitDone(t *testing.T, id string) operation.DoneEventDTO {
	t.Helper()
	select {
	case d := <-r.doneChan(id):
		return d
	case <-time.After(subWait):
		t.Fatalf("the subscription did not end; events: %s", summary(r.events(id)))
		return operation.DoneEventDTO{}
	}
}

// summary is "connecting open sent:connection_init received:connection_ack ...".
func summary(evs []SubscriptionEventDTO) string {
	parts := make([]string, 0, len(evs))
	for _, ev := range evs {
		s := ev.Kind
		if ev.Kind == EventKindMessage {
			s = ev.Direction + ":" + ev.Type
			if ev.ID != "" {
				s += "#" + ev.ID
			}
		}
		if ev.Kind == EventKindClosed && ev.Close != nil {
			s += ":" + strconv.Itoa(ev.Close.Code)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

type subFixture struct {
	svc *GraphQLService
	rec *subRecorder
}

// newSubFixture is a GraphQL service over a real HTTP service whose one
// request, "gql", is file with {{base}} the server's URL and {{token}}
// "tok".
func newSubFixture(t *testing.T, srvURL string, file projectfile.GraphQLRequest) *subFixture {
	t.Helper()
	rec := &subRecorder{items: map[string][]SubscriptionEventDTO{}, done: map[string]chan operation.DoneEventDTO{}, notify: make(chan struct{}, 1)}
	ops := operation.NewRegistry(rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), subWait)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	file.ID = "gql"
	load := func(_ context.Context, id string) (collection.HTTPSource, error) {
		if id != "gql" {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request").WithCode("collection.not_found")
		}
		f := file
		return collection.HTTPSource{
			GraphQL:         &f,
			Auth:            projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"},
			FolderVariables: [][]projectfile.Variable{{{Name: "base", Value: srvURL}, {Name: "token", Value: "tok"}}},
		}, nil
	}
	// The app's dialer: connections without a user operation are refused.
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	httpSvc := httpservice.New(load, nil, ops, dialer, nil, nil)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	svc := New(nil, nil, nil, ops, nil,
		WithSubscriptions(func(ctx context.Context, id string, allowUnresolved bool) (SubscriptionTarget, error) {
			sub, err := httpservice.NewGraphQLSubscription(ctx, httpSvc, id, allowUnresolved)
			if err != nil {
				return SubscriptionTarget{}, err
			}
			// As internal/app hands it over (graphQLSubscriptionTarget).
			t := SubscriptionTarget{Transport: sub.Transport, OperationType: sub.OperationType}
			if sub.SSE != nil {
				t.SSE = &SSETarget{Method: sub.SSE.Method, URL: sub.SSE.URL, Body: sub.SSEBody,
					Open: func(ctx context.Context, fn httpclient.StreamFunc) (*httpclient.Response, error) {
						return sub.SSE.Open(ctx, "", fn)
					}}
				return t, nil
			}
			t.WS = sub.WS
			t.ConnectionParams = sub.Build.ConnectionParams
			t.Request = transportws.SubscribeRequest{
				Query:         sub.Build.Query,
				OperationName: sub.Build.OperationName,
				Variables:     sub.Build.Variables,
				Extensions:    sub.Build.Extensions,
			}
			return t, nil
		}),
		withSubscriptionTimeouts(2*time.Second, 2*time.Second))
	return &subFixture{svc: svc, rec: rec}
}

func (f *subFixture) subscribe(t *testing.T) string {
	t.Helper()
	started, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"})
	if err != nil {
		t.Fatalf("Subscribe = %v", err)
	}
	return started.OpID
}

const eventsQuery = "subscription Events($room: ID!) { events(room: $room) { n } }"

func eventsFile() projectfile.GraphQLRequest {
	return projectfile.GraphQLRequest{
		URL:              "{{base}}/graphql",
		Query:            eventsQuery,
		Variables:        `{"room": "{{token}}-room"}`,
		ConnectionParams: `{"authToken": "{{token}}"}`,
	}
}

// A subscription streams its events into the log (GQL-34): connection
// params (GQL-33), the handshake built like a send, every protocol
// message with a timestamp, next payloads with their error counts, and
// the end of the connection.
func TestSubscribeStreamsEvents(t *testing.T) {
	var gotInit, gotSub gqlMsg
	var gotAuth string
	var closeFrame *wsframe.CloseError
	srvDone := make(chan struct{})
	srv := startGraphQLServer(t, func(p *gqlPeer) {
		defer close(srvDone)
		gotAuth = p.headers.Get("Authorization")
		gotInit = p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack","payload":{"server":"test"}}`)
		gotSub = p.expect("subscribe")
		id := gotSub.ID
		p.mustSend(`{"type":"ping"}`)
		p.expect("pong")
		p.mustSend(`{"type":"next","id":"` + id + `","payload":{"data":{"events":{"n":1}}}}`)
		p.mustSend(`{"type":"next","id":"` + id + `","payload":{"data":null,"errors":[{"message":"a"},{"message":"b"}]}}`)
		p.mustSend(`{"type":"next","id":"` + id + `","payload":{"data":{"events":{"n":3}}}}`)
		p.mustSend(`{"type":"complete","id":"` + id + `"}`)
		closeFrame = p.drain()
	})
	f := newSubFixture(t, srv.URL, eventsFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	<-srvDone

	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if string(gotInit.Payload) != `{"authToken":"tok"}` {
		t.Errorf("connection_init payload = %s", gotInit.Payload)
	}
	wantSub := `{"query":"` + eventsQuery + `","operationName":"Events","variables":{"room":"tok-room"}}`
	if string(gotSub.Payload) != wantSub {
		t.Errorf("subscribe payload = %s\nwant %s", gotSub.Payload, wantSub)
	}
	if closeFrame == nil || closeFrame.Code != wsframe.CloseNormalClosure {
		t.Errorf("client close = %+v, want 1000", closeFrame)
	}

	evs := f.rec.events(id)
	want := "connecting open sent:connection_init received:connection_ack sent:subscribe#1 received:ping sent:pong " +
		"received:next#1 received:next#1 received:next#1 received:complete#1 closed:1000"
	if got := summary(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if !strings.HasPrefix(evs[0].URL, "ws://127.0.0.1:") || !strings.HasSuffix(evs[0].URL, "/graphql") {
		t.Errorf("connecting URL = %q", evs[0].URL)
	}
	if evs[1].Subprotocol != transportws.Subprotocol {
		t.Errorf("open subprotocol = %q", evs[1].Subprotocol)
	}
	if evs[3].Payload != `{"server":"test"}` {
		t.Errorf("ack payload = %q", evs[3].Payload)
	}
	if evs[7].Payload != `{"data":{"events":{"n":1}}}` || evs[7].ErrorCount != 0 || evs[7].Size == 0 {
		t.Errorf("first next = %+v", evs[7])
	}
	if evs[8].ErrorCount != 2 {
		t.Errorf("next with errors: errorCount = %d", evs[8].ErrorCount)
	}
	last := evs[len(evs)-1]
	if last.Close == nil || !last.Close.WasClean || last.Close.InitiatedBy != "local" || last.Close.CodeName != "normal closure" || last.Error != nil {
		t.Errorf("closed = %+v (error %+v)", last.Close, last.Error)
	}
	var prev time.Time
	for i, ev := range evs {
		at, err := time.Parse(time.RFC3339Nano, ev.At)
		if err != nil {
			t.Fatalf("event %d at %q: %v", i, ev.At, err)
		}
		if at.Before(prev) {
			t.Errorf("event %d at %s is before the previous one", i, ev.At)
		}
		prev = at
	}
}

// Stop sends complete, closes with 1000 and ends the operation
// "cancelled"; subscribing again starts over on a new connection (GQL-34).
func TestSubscribeStopAndRestart(t *testing.T) {
	type result struct {
		complete gqlMsg
		close    *wsframe.CloseError
	}
	results := make(chan result, 2)
	srv := startGraphQLServer(t, func(p *gqlPeer) {
		p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack"}`)
		id := p.expect("subscribe").ID
		// Stream until the client completes.
		completed := make(chan gqlMsg, 1)
		go func() {
			for n := 1; ; n++ {
				select {
				case <-completed:
					return
				case <-time.After(5 * time.Millisecond):
				}
				if p.send(`{"type":"next","id":"`+id+`","payload":{"data":{"n":`+strconv.Itoa(n)+`}}}`) != nil {
					return
				}
			}
		}()
		var r result
		for {
			m, err := p.tryRead()
			if err != nil {
				break
			}
			if m.Type == "complete" {
				r.complete = m
				close(completed)
			}
		}
		if r.complete.Type == "" {
			close(completed)
		}
		r.close = p.conn.PeerClose()
		results <- r
	})
	f := newSubFixture(t, srv.URL, eventsFile())

	for run := 1; run <= 2; run++ {
		id := f.subscribe(t)
		f.rec.waitFor(t, id, func(evs []SubscriptionEventDTO) bool {
			n := 0
			for _, ev := range evs {
				if ev.Type == "next" {
					n++
				}
			}
			return n >= 3
		})
		stopped, err := f.svc.StopSubscription(t.Context(), StopSubscriptionInputDTO{OpID: id})
		if err != nil || !stopped {
			t.Fatalf("run %d: StopSubscription = %v, %v", run, stopped, err)
		}
		done := f.rec.waitDone(t, id)
		if done.Status != operation.StatusCancelled {
			t.Errorf("run %d: done = %+v", run, done)
		}
		var r result
		select {
		case r = <-results:
		case <-time.After(subWait):
			t.Fatalf("run %d: the server did not see the end", run)
		}
		if r.complete.raw != `{"type":"complete","id":"1"}` {
			t.Errorf("run %d: complete = %q", run, r.complete.raw)
		}
		if r.close == nil || r.close.Code != wsframe.CloseNormalClosure {
			t.Errorf("run %d: close = %+v", run, r.close)
		}
		evs := f.rec.events(id)
		if s := summary(evs); !strings.Contains(s, "sent:complete#1") || !strings.HasSuffix(s, "closed:1000") {
			t.Errorf("run %d: events %s", run, s)
		}
		if again, _ := f.svc.StopSubscription(t.Context(), StopSubscriptionInputDTO{OpID: id}); again {
			t.Errorf("run %d: a second stop reported a running subscription", run)
		}
	}
}

// An error message ends the operation with CodeSubscriptionError; the log
// has the errors.
func TestSubscribeErrorMessage(t *testing.T) {
	srv := startGraphQLServer(t, func(p *gqlPeer) {
		p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack"}`)
		id := p.expect("subscribe").ID
		p.mustSend(`{"type":"error","id":"` + id + `","payload":[{"message":"Cannot query field \"x\""},{"message":"second"}]}`)
		p.drain()
	})
	f := newSubFixture(t, srv.URL, eventsFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != CodeSubscriptionError {
		t.Fatalf("done = %+v", done)
	}
	if !strings.Contains(done.Error.Message, `Cannot query field "x"`) || done.Error.Details["count"] != "2" {
		t.Errorf("error = %+v", done.Error)
	}
	evs := f.rec.events(id)
	var errEv *SubscriptionEventDTO
	for i := range evs {
		if evs[i].Type == "error" {
			errEv = &evs[i]
		}
	}
	if errEv == nil || errEv.ErrorCount != 2 || errEv.Direction != "received" {
		t.Errorf("error event = %+v; events %s", errEv, summary(evs))
	}
}

// The server rejecting the connection params (4403) fails the operation
// with the close code in the log.
func TestSubscribeServerCloses(t *testing.T) {
	srv := startGraphQLServer(t, func(p *gqlPeer) {
		p.expect("connection_init")
		if _, err := p.conn.WriteClose(transportws.CloseForbidden, "Forbidden"); err != nil {
			t.Errorf("close: %v", err)
		}
		p.drain()
	})
	f := newSubFixture(t, srv.URL, eventsFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != transportws.CodeClosedByServer || done.Error.Kind != string(errs.PermissionDenied) {
		t.Fatalf("done = %+v", done.Error)
	}
	evs := f.rec.events(id)
	last := evs[len(evs)-1]
	if last.Kind != EventKindClosed || last.Close == nil || last.Close.Code != 4403 || last.Close.CodeName != "forbidden" ||
		last.Close.Reason != "Forbidden" || last.Close.InitiatedBy != "remote" {
		t.Errorf("closed = %+v; events %s", last.Close, summary(evs))
	}
}

// A handshake that fails ends the operation with its error.
func TestSubscribeConnectFails(t *testing.T) {
	srv := httptest.NewServer(nethttp.NotFoundHandler())
	t.Cleanup(srv.Close)
	f := newSubFixture(t, srv.URL, eventsFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	if done.Status != operation.StatusError || done.Error == nil || !strings.HasPrefix(done.Error.Code, "wsclient.") {
		t.Fatalf("done = %+v", done.Error)
	}
	if s := summary(f.rec.events(id)); s != "connecting closed:1006" {
		t.Errorf("events = %s", s)
	}
}

// Undefined {{variables}}, in the connection params too, refuse to start
// unless allowed (ENV-11); invalid params refuse with their code.
func TestSubscribeRefusesBadInput(t *testing.T) {
	file := eventsFile()
	file.ConnectionParams = `{"authToken": "{{missing}}"}`
	f := newSubFixture(t, "http://127.0.0.1:1", file)
	_, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || e.Details["names"] != "missing" {
		t.Fatalf("Subscribe = %v", err)
	}
	started, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql", AllowUnresolved: true})
	if err != nil {
		t.Fatalf("Subscribe anyway = %v", err)
	}
	f.rec.waitDone(t, started.OpID) // nothing listens on port 1

	file.ConnectionParams = `["tok"]`
	f = newSubFixture(t, "http://127.0.0.1:1", file)
	if _, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"}); !errors.As(err, &e) || e.Code != "request.graphql_invalid_connection_params" {
		t.Fatalf("Subscribe with params not an object = %v", err)
	}
	if _, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "other"}); err == nil {
		t.Fatal("Subscribe of an unknown request succeeded")
	}
}

func TestStopSubscriptionUnknown(t *testing.T) {
	f := newSubFixture(t, "http://127.0.0.1:1", eventsFile())
	if ok, err := f.svc.StopSubscription(t.Context(), StopSubscriptionInputDTO{OpID: "op_unknown"}); ok || err != nil {
		t.Errorf("StopSubscription = %v, %v", ok, err)
	}
	svc := New(nil, nil, nil, nil, nil)
	if _, err := svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"}); err == nil {
		t.Error("Subscribe without subscriptions succeeded")
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://u:p@h:1/g?t=1": "wss://h:1/g?t=1",
		"ws://h/a@b":          "ws://h/a@b",
		"ws://h":              "ws://h",
	} {
		if got := redactUserinfo(in); got != want {
			t.Errorf("redactUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
	if got := redactURL("wss://u:p@h:1/g?t=1"); got != "wss://h:1" {
		t.Errorf("redactURL = %q", got)
	}
}
