package grpcmock_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
)

// The server is driven by PostSonde's own gRPC transport (the client of
// gRPC requests), over h2c; TLS is in servertls. Messages are written and
// read with the repo's JSON mapping and codec against the same files.

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
			var lines []string
			for _, ev := range e.all() {
				lines = append(lines, string(ev.Kind)+": "+ev.Summary)
			}
			t.Fatalf("no %s event in:\n%s", what, strings.Join(lines, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// callEvents returns the events of call n.
func (e *events) callEvents(n uint64) []servers.Event {
	var out []servers.Event
	for _, ev := range e.all() {
		if ev.GRPC != nil && ev.GRPC.Call == n {
			out = append(out, ev)
		}
	}
	return out
}

var protoFile = func() string {
	p, err := filepath.Abs(filepath.Join("testdata", "shop", "v1", "orders.proto"))
	if err != nil {
		panic(err)
	}
	return p
}()

var importRoot = filepath.Dir(filepath.Dir(filepath.Dir(protoFile)))

type fixture struct {
	m      *servers.Manager
	id     string
	addr   string
	rec    *events
	client *transport.Client
	pool   *desc.Pool
}

func baseConfig(methods ...grpcmock.Method) grpcmock.Config {
	return grpcmock.Config{Files: []string{protoFile}, ImportPaths: []string{importRoot}, Reflection: true, Methods: methods}
}

func start(t *testing.T, cfg grpcmock.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{grpcmock.New(grpcmock.Options{})},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: grpcmock.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.New(netx.NewDirect(netx.Options{}), transport.Options{})
	t.Cleanup(client.CloseIdleConnections)
	res, err := linker.Load(t.Context(), linker.Options{Files: []string{protoFile}, ImportPaths: []string{importRoot}})
	if err != nil || res.HasErrors() {
		t.Fatalf("linking: %v %v", err, res.Err())
	}
	return &fixture{m: m, id: s.ID, addr: "127.0.0.1:" + strconv.Itoa(s.Port()), rec: rec, client: client, pool: res.Pool}
}

func (f *fixture) method(t *testing.T, path string) *desc.Method {
	t.Helper()
	svc, name, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	s := f.pool.FindService(svc)
	if s == nil || s.MethodByName(name) == nil {
		t.Fatalf("no method %s", path)
	}
	return s.MethodByName(name)
}

func (f *fixture) encode(t *testing.T, md *desc.Message, js string) []byte {
	t.Helper()
	m := dynamic.New(md)
	if err := (protojson.UnmarshalOptions{Resolver: f.pool}).Unmarshal([]byte(js), m); err != nil {
		t.Fatal(err)
	}
	b, err := dynamic.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) decode(t *testing.T, md *desc.Message, b []byte) string {
	t.Helper()
	m := dynamic.New(md)
	if err := dynamic.Unmarshal(b, m); err != nil {
		t.Fatal(err)
	}
	js, err := (protojson.MarshalOptions{Resolver: f.pool}).Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(js)
}

// result is what a call received.
type result struct {
	header   transport.Metadata
	messages []string
	status   transport.Status
	trailer  transport.Metadata
	err      error
}

// call sends the JSON messages (then half-closes) and reads everything.
func (f *fixture) call(t *testing.T, o transport.CallOptions, msgs ...string) result {
	t.Helper()
	md := f.method(t, o.Method)
	o.Address = f.addr
	s, err := f.client.Invoke(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, js := range msgs {
		if err := s.Send(f.encode(t, md.Input, js)); err != nil {
			break
		}
	}
	_ = s.CloseSend()
	var r result
	for {
		b, err := s.Recv()
		if err != nil {
			if err != io.EOF {
				r.err = err
			}
			break
		}
		r.messages = append(r.messages, f.decode(t, md.Output, b))
	}
	r.header, _ = s.Header()
	r.status, _ = s.Status()
	r.trailer = s.Trailer()
	return r
}

func TestUnary(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "GetOrder",
		Messages: []string{`{"id": "o-1", "total": {"currency": "EUR", "units": "42"}, "created": "2026-09-19T10:00:00Z", "items": ["a", "b"]}`},
		Headers:  []grpcmock.Metadata{{Enabled: true, Name: "X-Mock", Value: "yes"}, {Enabled: false, Name: "x-off", Value: "no"}},
		Trailers: []grpcmock.Metadata{{Enabled: true, Name: "x-trace-bin", Value: "AAEC"}},
	}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder", Metadata: transport.Metadata{{Key: "authorization", Value: "Bearer t"}}}, `{"id": "o-1"}`)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := `{"id":"o-1","total":{"currency":"EUR","units":"42"},"created":"2026-09-19T10:00:00Z","items":["a","b"]}`
	if len(r.messages) != 1 || r.messages[0] != want {
		t.Fatalf("messages = %q", r.messages)
	}
	if got := r.header.Get("x-mock"); !slices.Equal(got, []string{"yes"}) || len(r.header.Get("x-off")) != 0 {
		t.Errorf("header = %v", r.header)
	}
	if got := r.trailer.Get("x-trace-bin"); !slices.Equal(got, []string{"\x00\x01\x02"}) {
		t.Errorf("trailer = %v", r.trailer)
	}

	// The traffic: the call, the request as JSON, the response, the status.
	end := f.rec.waitFor(t, "end", func(e servers.Event) bool { return e.Kind == servers.KindResponse && e.GRPC != nil })
	evs := f.rec.callEvents(end.GRPC.Call)
	if len(evs) != 4 {
		t.Fatalf("events = %+v", evs)
	}
	req, in, out := evs[0], evs[1], evs[2]
	if req.Kind != servers.KindRequest || req.Summary != "Call 1: /shop.v1.Orders/GetOrder (unary)" || req.GRPC.Kind != "unary" {
		t.Errorf("request = %+v", req)
	}
	if !slices.Contains(req.HTTP.Headers, servers.Header{Name: "Authorization", Value: "Bearer t"}) {
		t.Errorf("request headers = %v", req.HTTP.Headers)
	}
	if in.Direction != servers.In || in.GRPC.Type != "shop.v1.GetOrderRequest" || string(in.Content) != "{\n  \"id\": \"o-1\"\n}" {
		t.Errorf("request message = %+v %s", in, in.Content)
	}
	if out.Direction != servers.Out || out.GRPC.Type != "shop.v1.Order" || !strings.Contains(string(out.Content), `"currency": "EUR"`) {
		t.Errorf("response message = %+v %s", out, out.Content)
	}
	if end.GRPC.CodeName != "OK" || !strings.HasPrefix(end.Summary, "OK (call 1, 1 message received, 1 message sent)") {
		t.Errorf("end = %+v", end)
	}
	if !slices.Equal(end.GRPC.Trailers, []servers.Header{{Name: "x-trace-bin", Value: "AAEC"}}) {
		t.Errorf("trailers = %v", end.GRPC.Trailers)
	}
	if !slices.Contains(end.HTTP.Headers, servers.Header{Name: "X-Mock", Value: "yes"}) {
		t.Errorf("response headers = %v", end.HTTP.Headers)
	}
}

func TestErrorStatus(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "Cancel",
		Messages:      []string{`{"id": "never sent"}`},
		Status:        int(transport.NotFound),
		StatusMessage: "no order o-9 – gone",
		Trailers:      []grpcmock.Metadata{{Enabled: true, Name: "x-reason", Value: "deleted"}},
	}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/Cancel"}, `{"id": "o-9"}`)
	if len(r.messages) != 0 {
		t.Errorf("messages = %q", r.messages)
	}
	if r.status.Code != transport.NotFound || r.status.Message != "no order o-9 – gone" {
		t.Errorf("status = %+v", r.status)
	}
	if e, ok := errors.AsType[*errs.Error](r.err); !ok || e.Code != "grpctransport.status" {
		t.Errorf("err = %v", r.err)
	}
	if got := r.trailer.Get("x-reason"); !slices.Equal(got, []string{"deleted"}) {
		t.Errorf("trailer = %v", r.trailer)
	}
	end := f.rec.waitFor(t, "end", func(e servers.Event) bool { return e.Kind == servers.KindResponse && e.GRPC != nil })
	if end.GRPC.Code != 5 || end.GRPC.Message != "no order o-9 – gone" || !strings.HasPrefix(end.Summary, "NOT_FOUND: no order o-9") {
		t.Errorf("end = %+v", end)
	}
}

func TestServerStreaming(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "ListOrders",
		Messages:   []string{`{"id": "1"}`, `{"id": "2"}`, `{"id": "3"}`},
		IntervalMs: 30,
	}))
	began := time.Now()
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/ListOrders"}, `{"pageSize": 3}`)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !slices.Equal(r.messages, []string{`{"id":"1"}`, `{"id":"2"}`, `{"id":"3"}`}) {
		t.Errorf("messages = %q", r.messages)
	}
	if d := time.Since(began); d < 60*time.Millisecond {
		t.Errorf("took %v, want the two intervals", d)
	}
}

func TestServerStreamingThenError(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "ListOrders",
		Messages: []string{`{"id": "1"}`},
		Status:   int(transport.Unavailable), StatusMessage: "try later",
	}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/ListOrders"}, `{}`)
	if !slices.Equal(r.messages, []string{`{"id":"1"}`}) || r.status.Code != transport.Unavailable {
		t.Errorf("messages = %q, status = %+v", r.messages, r.status)
	}
}

func TestClientStreaming(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "Upload",
		Messages: []string{`{"count": 2}`},
	}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/Upload"}, `{"id": "a"}`, `{"id": "b"}`)
	if r.err != nil || !slices.Equal(r.messages, []string{`{"count":2}`}) {
		t.Fatalf("messages = %q, err = %v", r.messages, r.err)
	}
	end := f.rec.waitFor(t, "end", func(e servers.Event) bool { return e.Kind == servers.KindResponse && e.GRPC != nil })
	var ins []string
	for _, ev := range f.rec.callEvents(end.GRPC.Call) {
		if ev.Kind == servers.KindMessage && ev.Direction == servers.In {
			ins = append(ins, string(ev.Content))
		}
	}
	if len(ins) != 2 || !strings.Contains(ins[1], `"b"`) {
		t.Errorf("request messages = %q", ins)
	}
}

func TestBidi(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(
		grpcmock.Method{Service: "shop.v1.Orders", Method: "Track", Messages: []string{`{"id": "x"}`, `{"id": "y"}`}},
		grpcmock.Method{Service: "shop.v1.Orders", Method: "Chat", Bidi: grpcmock.BidiEcho},
	))
	t.Run("each", func(t *testing.T) {
		r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/Track"}, `{"id": "1"}`, `{"id": "2"}`, `{"id": "3"}`)
		if r.err != nil || !slices.Equal(r.messages, []string{`{"id":"x"}`, `{"id":"y"}`, `{"id":"x"}`}) {
			t.Errorf("messages = %q, err = %v", r.messages, r.err)
		}
	})
	t.Run("echo", func(t *testing.T) {
		r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/Chat"}, `{"text": "hi"}`, `{"text": "there"}`)
		if r.err != nil || !slices.Equal(r.messages, []string{`{"text":"hi"}`, `{"text":"there"}`}) {
			t.Errorf("messages = %q, err = %v", r.messages, r.err)
		}
	})
}

// TestBidiStream: the list goes out while the client still sends; the
// status waits for its half-close.
func TestBidiStream(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{
		Service: "shop.v1.Orders", Method: "Track", Bidi: grpcmock.BidiStream,
		Messages: []string{`{"id": "x"}`, `{"id": "y"}`},
	}))
	md := f.method(t, "/shop.v1.Orders/Track")
	s, err := f.client.Invoke(t.Context(), transport.CallOptions{Address: f.addr, Method: "/shop.v1.Orders/Track"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []string{`{"id":"x"}`, `{"id":"y"}`} {
		b, err := s.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if got := f.decode(t, md.Output, b); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if err := s.Send(f.encode(t, md.Input, `{"id": "late"}`)); err != nil {
		t.Fatal(err)
	}
	_ = s.CloseSend()
	if _, err := s.Recv(); err != io.EOF {
		t.Fatalf("end = %v", err)
	}
	f.rec.waitFor(t, "late request", func(e servers.Event) bool {
		return e.Kind == servers.KindMessage && e.Direction == servers.In && strings.Contains(string(e.Content), "late")
	})
}

func TestUnimplemented(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig())
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`)
	if r.status.Code != transport.Unimplemented || !strings.Contains(r.status.Message, "no canned response") {
		t.Errorf("configured nothing: status = %+v", r.status)
	}
	s, err := f.client.Invoke(t.Context(), transport.CallOptions{Address: f.addr, Method: "/nope.Svc/Do"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Send(nil)
	_ = s.CloseSend()
	_, err = s.Recv()
	st, _ := s.Status()
	if err == nil || st.Code != transport.Unimplemented || st.Message != "unknown method /nope.Svc/Do" {
		t.Errorf("unknown method: %v %+v", err, st)
	}
	f.rec.waitFor(t, "unknown", func(e servers.Event) bool {
		return e.Kind == servers.KindResponse && e.GRPC != nil && e.GRPC.CodeName == "UNIMPLEMENTED" && e.GRPC.Kind == ""
	})
}

func TestDelayAndDeadline(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", DelayMs: 5000}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder", Timeout: 150 * time.Millisecond}, `{}`)
	if r.status.Code != transport.DeadlineExceeded {
		t.Errorf("status = %+v", r.status)
	}
	f.rec.waitFor(t, "deadline", func(e servers.Event) bool {
		return e.Kind == servers.KindResponse && e.GRPC != nil && e.GRPC.CodeName == "DEADLINE_EXCEEDED"
	})
}

func TestStopEndsCalls(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", DelayMs: 60_000}))
	done := make(chan result, 1)
	go func() { done <- f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`) }()
	f.rec.waitFor(t, "request message", func(e servers.Event) bool { return e.Kind == servers.KindMessage })
	began := time.Now()
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > time.Second {
		t.Errorf("stop took %v", d)
	}
	r := <-done
	if r.status.Code != transport.Unavailable && r.status.Code != transport.Canceled {
		t.Errorf("status = %+v, %v", r.status, r.err)
	}
	var stopped bool
	for _, e := range f.rec.all() {
		if e.Kind == servers.KindStopped {
			stopped = true
		}
		if e.Kind == servers.KindResponse && e.GRPC != nil && stopped {
			t.Errorf("end logged after stopped: %+v", e)
		}
	}
}

func TestInvalidCannedMessage(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"nope": 1}`}}))
	f.rec.waitFor(t, "problem", func(e servers.Event) bool {
		return e.Kind == servers.KindError && strings.Contains(e.Summary, `message 1 is not a valid shop.v1.Order: unknown field "nope"`)
	})
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`)
	if r.status.Code != transport.Internal || !strings.Contains(r.status.Message, "PostSonde mock: message 1") {
		t.Errorf("status = %+v", r.status)
	}
}

func TestReconfigure(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "old"}`}}))
	if r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`); !slices.Equal(r.messages, []string{`{"id":"old"}`}) {
		t.Fatalf("before: %q", r.messages)
	}
	if _, err := f.m.UpdateConfig(f.id, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "new"}`}})); err != nil {
		t.Fatal(err)
	}
	if r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`); !slices.Equal(r.messages, []string{`{"id":"new"}`}) {
		t.Errorf("after: %q", r.messages)
	}
	// Files that no longer load keep the old definitions.
	bad := baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "newer"}`}})
	bad.Files = []string{filepath.Join(t.TempDir(), "missing.proto")}
	if _, err := f.m.UpdateConfig(f.id, bad); err != nil {
		t.Fatal(err)
	}
	f.rec.waitFor(t, "link error", func(e servers.Event) bool {
		return e.Kind == servers.KindError && strings.Contains(e.Summary, "keeps serving the definitions")
	})
	if r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder"}, `{}`); !slices.Equal(r.messages, []string{`{"id":"newer"}`}) {
		t.Errorf("after a bad file: %q", r.messages)
	}
}

func TestGRPCWeb(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(
		grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "web"}`}, Trailers: []grpcmock.Metadata{{Enabled: true, Name: "x-t", Value: "1"}}},
		grpcmock.Method{Service: "shop.v1.Orders", Method: "ListOrders", Messages: []string{`{"id": "1"}`, `{"id": "2"}`}},
	))
	for _, p := range []transport.Protocol{transport.ProtocolGRPCWeb, transport.ProtocolGRPCWebText} {
		r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder", Protocol: p}, `{}`)
		if r.err != nil || !slices.Equal(r.messages, []string{`{"id":"web"}`}) || !slices.Equal(r.trailer.Get("x-t"), []string{"1"}) {
			t.Errorf("%s unary: %q %v %v", p, r.messages, r.trailer, r.err)
		}
		r = f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/ListOrders", Protocol: p}, `{}`)
		if r.err != nil || len(r.messages) != 2 {
			t.Errorf("%s stream: %q %v", p, r.messages, r.err)
		}
	}
	f.rec.waitFor(t, "web call", func(e servers.Event) bool {
		return e.Kind == servers.KindRequest && e.GRPC != nil && e.GRPC.Web && strings.HasSuffix(e.Summary, "over gRPC-Web")
	})
}

func TestGzipRequest(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "z"}`}}))
	r := f.call(t, transport.CallOptions{Method: "/shop.v1.Orders/GetOrder", Compression: transport.CompressionGzip}, `{"id": "compressed"}`)
	if r.err != nil || !slices.Equal(r.messages, []string{`{"id":"z"}`}) {
		t.Fatalf("%q %v", r.messages, r.err)
	}
	f.rec.waitFor(t, "decompressed request", func(e servers.Event) bool {
		return e.Kind == servers.KindMessage && strings.Contains(string(e.Content), "compressed")
	})
}

func TestReflection(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig(grpcmock.Method{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "r"}`}}))
	for _, v := range []reflection.Version{reflection.V1, reflection.V1Alpha} {
		snap, err := reflection.NewClient(f.client).Fetch(t.Context(), reflection.FetchOptions{Call: transport.CallOptions{Address: f.addr}, Version: v})
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if !slices.Equal(snap.Services, []string{"shop.v1.Orders"}) || len(snap.Problems) != 0 {
			t.Errorf("%s: services = %v, problems = %+v", v, snap.Services, snap.Problems)
		}
		schema, err := reflection.Link(t.Context(), snap)
		if err != nil {
			t.Fatal(err)
		}
		m := schema.FindMethod("/shop.v1.Orders/ListOrders")
		if m == nil || !m.ServerStreaming || m.Output.FullName != "shop.v1.Order" || m.Output.FieldByName("total").Message.FullName != "shop.v1.Money" {
			t.Errorf("%s: method = %+v", v, m)
		}
	}

	off := baseConfig()
	off.Reflection = false
	g := start(t, off)
	_, err := reflection.NewClient(g.client).Fetch(t.Context(), reflection.FetchOptions{Call: transport.CallOptions{Address: g.addr}})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != "grpcreflection.unsupported" {
		t.Errorf("reflection off: %v", err)
	}
}

func TestNotGRPC(t *testing.T) {
	t.Parallel()
	f := start(t, baseConfig())
	res, err := http.Get("http://" + f.addr + "/shop.v1.Orders/GetOrder")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "POST" {
		t.Errorf("GET: %d %v", res.StatusCode, res.Header)
	}
	res, err = http.Post("http://"+f.addr+"/x", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("JSON POST: %d", res.StatusCode)
	}
	f.rec.waitFor(t, "405", func(e servers.Event) bool {
		return e.Kind == servers.KindResponse && strings.HasPrefix(e.Summary, "405 Method Not Allowed (gRPC calls are POST requests)")
	})
}

func TestProtoErrorsFailStart(t *testing.T) {
	t.Parallel()
	bad := filepath.Join(t.TempDir(), "bad.proto")
	m := servers.NewManager(servers.Options{Types: []servers.Type{grpcmock.New(grpcmock.Options{})}, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: grpcmock.TypeName, Config: grpcmock.Config{Files: []string{bad}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start(t.Context(), s.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != grpcmock.CodeProtoFailed {
		t.Fatalf("err = %v", err)
	}
	s, _ = m.Get(s.ID)
	if s.State != servers.StateFailed {
		t.Errorf("state = %s", s.State)
	}

	// Relative paths need Resolve (a project).
	s, err = m.Add(servers.Spec{Type: grpcmock.TypeName, Config: grpcmock.Config{Files: []string{"api/x.proto"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start(t.Context(), s.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != grpcmock.CodeRelativePath {
		t.Fatalf("relative: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	c, err := grpcmock.Normalize(grpcmock.Config{
		Files: []string{" a.proto ", ""},
		Methods: []grpcmock.Method{{
			Service: ".pkg.Svc", Method: "Do",
			Headers: []grpcmock.Metadata{{Enabled: true, Name: " X-Up ", Value: "v"}, {}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := c.Methods[0]
	if !slices.Equal(c.Files, []string{"a.proto"}) || m.Service != "pkg.Svc" || m.Bidi != grpcmock.BidiEach ||
		len(m.Headers) != 1 || m.Headers[0].Name != "x-up" || m.Messages == nil {
		t.Errorf("normalized = %+v", c)
	}
	cases := []struct {
		name  string
		m     grpcmock.Method
		field string
	}{
		{"service", grpcmock.Method{Service: "a b", Method: "M"}, "service"},
		{"method", grpcmock.Method{Service: "s", Method: "1x"}, "method"},
		{"status", grpcmock.Method{Service: "s", Method: "M", Status: 17}, "status"},
		{"reserved", grpcmock.Method{Service: "s", Method: "M", Trailers: []grpcmock.Metadata{{Name: "grpc-status", Value: "0"}}}, "trailers"},
		{"value", grpcmock.Method{Service: "s", Method: "M", Headers: []grpcmock.Metadata{{Name: "x", Value: "a\nb"}}}, "headers"},
		{"bin", grpcmock.Method{Service: "s", Method: "M", Headers: []grpcmock.Metadata{{Name: "x-bin", Value: "!!"}}}, "headers"},
		{"delay", grpcmock.Method{Service: "s", Method: "M", DelayMs: -1}, "delayMs"},
		{"interval", grpcmock.Method{Service: "s", Method: "M", IntervalMs: 300_001}, "intervalMs"},
		{"bidi", grpcmock.Method{Service: "s", Method: "M", Bidi: "loop"}, "bidi"},
	}
	for _, tc := range cases {
		_, err := grpcmock.Normalize(grpcmock.Config{Methods: []grpcmock.Method{{Service: "ok", Method: "First"}, tc.m}})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != grpcmock.CodeInvalidConfig || e.Details["methodField"] != tc.field || e.Details["methodIndex"] != "1" {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	_, err = grpcmock.Normalize(grpcmock.Config{Methods: []grpcmock.Method{{Service: "s", Method: "M"}, {Service: "s", Method: "M"}}})
	if err == nil || !strings.Contains(err.Error(), "has a canned response already") {
		t.Errorf("duplicate: %v", err)
	}
}
