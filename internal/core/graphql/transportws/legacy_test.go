package transportws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// The legacy subscriptions-transport-ws protocol (GQL-31), against a test
// server speaking its server side.

var legacyServer = serverOptions{subprotocol: LegacySubprotocol}

func legacyOptions(t *testing.T, so serverOptions, handler func(p *peer)) (Options, *events) {
	t.Helper()
	ev := &events{}
	o := options(startServer(t, so, handler), ev)
	o.Protocol = ProtocolLegacy
	return o, ev
}

func TestLegacyHappyPath(t *testing.T) {
	type seen struct {
		protocol          string
		init, start, term serverMsg
		close             *wsframe.CloseError
	}
	serverDone := make(chan seen, 1)
	o, ev := legacyOptions(t, legacyServer, func(p *peer) {
		var got seen
		got.protocol = p.protocol
		got.init = p.expect("connection_init", "")
		p.send(`{"type":"connection_ack"}`)
		got.start = p.expect("start", "")
		p.send(`{"type":"ka"}`)
		id := got.start.ID
		p.send(`{"id":"` + id + `","type":"data","payload":{"data":{"n":1}}}`)
		p.send(`{"type":"ka"}`)
		p.send(`{"id":"` + id + `","type":"data","payload":{"data":null,"errors":[{"message":"partial"}]}}`)
		p.send(`{"id":"` + id + `","type":"complete"}`)
		got.term = p.expect("connection_terminate", "")
		got.close = p.drain()
		serverDone <- got
	})
	o.ConnectionParams = json.RawMessage(`{"authToken":"t"}`)
	s := dial(t, o)
	if s.Client().Subprotocol() != LegacySubprotocol {
		t.Errorf("subprotocol %q", s.Client().Subprotocol())
	}
	sub, err := s.Subscribe(context.Background(), SubscribeRequest{
		Query:         "subscription N { n }",
		OperationName: "N",
		Variables:     json.RawMessage(`{"a":1}`),
		Extensions:    json.RawMessage(`{"persistedQuery":{"version":1}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := next(t, sub); r.Kind != ResultNext || string(r.Payload) != `{"data":{"n":1}}` || r.HasErrors {
		t.Errorf("first %+v", r)
	}
	if r := next(t, sub); r.Kind != ResultNext || !r.HasErrors || len(r.Errors) != 1 || r.Errors[0].Message != "partial" {
		t.Errorf("second %+v", r)
	}
	if r := next(t, sub); r.Kind != ResultComplete {
		t.Errorf("third %+v", r)
	}
	expectClosedC(t, sub)
	// After complete, Unsubscribe sends no stop.
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background(), wsframe.CloseNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	got := <-serverDone
	if got.protocol != LegacySubprotocol {
		t.Errorf("offered %q", got.protocol)
	}
	if got.init.raw != `{"type":"connection_init","payload":{"authToken":"t"}}` {
		t.Errorf("init %s", got.init.raw)
	}
	wantStart := `{"type":"start","id":"1","payload":{"query":"subscription N { n }","operationName":"N","variables":{"a":1},"extensions":{"persistedQuery":{"version":1}}}}`
	if got.start.raw != wantStart {
		t.Errorf("start = %s\nwant    %s", got.start.raw, wantStart)
	}
	if got.term.raw != `{"type":"connection_terminate"}` {
		t.Errorf("terminate %s", got.term.raw)
	}
	if got.close == nil || got.close.Code != wsframe.CloseNormalClosure {
		t.Errorf("close %v", got.close)
	}
	want := "sent:connection_init received:connection_ack sent:start#1 received:ka received:data#1 received:ka received:data#1 received:complete#1 sent:connection_terminate"
	if s := ev.summary(); s != want {
		t.Errorf("events:\n got %s\nwant %s", s, want)
	}
}

func TestLegacyStop(t *testing.T) {
	gotStop := make(chan serverMsg, 1)
	o, ev := legacyOptions(t, legacyServer, func(p *peer) {
		p.init()
		id := p.expect("start", "").ID
		p.send(`{"type":"data","id":"` + id + `","payload":{"data":{"n":1}}}`)
		gotStop <- p.expect("stop", id)
		// Crossing the stop: ignored.
		p.send(`{"type":"data","id":"` + id + `","payload":{"data":{"n":2}}}`)
		p.send(`{"type":"ka"}`)
		p.expect("connection_terminate", "")
		p.drain()
	})
	s := dial(t, o)
	sub := subscribe(t, s, "subscription { n }")
	if r := next(t, sub); r.Kind != ResultNext {
		t.Fatalf("result %+v", r)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	expectClosedC(t, sub)
	if m := <-gotStop; m.raw != `{"type":"stop","id":"1"}` {
		t.Errorf("stop = %s", m.raw)
	}
	waitEvents(t, ev, "received:ka")
	if err := s.Close(context.Background(), wsframe.CloseNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	want := "sent:connection_init received:connection_ack sent:start#1 received:data#1 sent:stop#1 received:data#1(unknown_id) received:ka sent:connection_terminate"
	if got := ev.summary(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if s.Err() != nil {
		t.Errorf("Err = %v", s.Err())
	}
}

func TestLegacyErrors(t *testing.T) {
	o, ev := legacyOptions(t, legacyServer, func(p *peer) {
		p.init()
		a := p.expect("start", "").ID
		b := p.expect("start", "").ID
		// An error for no operation (a message the server could not read).
		p.send(`{"type":"error","payload":{"message":"Message must be JSON-parseable"}}`)
		p.send(`{"type":"error","id":"` + a + `","payload":{"message":"Cannot query field \"x\"","locations":[{"line":1,"column":16}]}}`)
		p.send(`{"type":"error","id":"` + b + `","payload":[{"message":"one"},{"message":"two"}]}`)
		p.drain()
	})
	s := dial(t, o)
	a := subscribe(t, s, "subscription { x }")
	b := subscribe(t, s, "subscription { y }")
	r := next(t, a)
	if r.Kind != ResultError || len(r.Errors) != 1 || r.Errors[0].Message != `Cannot query field "x"` || len(r.Errors[0].Locations) != 1 {
		t.Errorf("a: %+v", r)
	}
	expectClosedC(t, a)
	r = next(t, b)
	if r.Kind != ResultError || len(r.Errors) != 2 || r.Errors[1].Message != "two" {
		t.Errorf("b: %+v", r)
	}
	want := "sent:connection_init received:connection_ack sent:start#1 sent:start#2 received:error(unknown_id) received:error#1 received:error#2"
	if got := ev.summary(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if s.State() != StateReady {
		t.Errorf("state %s", s.State())
	}
}

func TestLegacyConnectionError(t *testing.T) {
	serverSaw := make(chan *wsframe.CloseError, 1)
	o, ev := legacyOptions(t, legacyServer, func(p *peer) {
		p.expect("connection_init", "")
		p.send(`{"type":"connection_error","payload":{"message":"Invalid token."}}`)
		serverSaw <- p.drain()
	})
	s := dial(t, o)
	_, err := s.Subscribe(context.Background(), SubscribeRequest{Query: "subscription { a }"})
	if errCode(err) != CodeConnectionError || !errors.Is(err, &errs.Error{Kind: errs.PermissionDenied}) {
		t.Errorf("Subscribe = %v", err)
	}
	waitDone(t, s)
	if e := s.Err(); errCode(e) != CodeConnectionError || e.Error() == "" {
		t.Errorf("Err = %v", e)
	}
	if e, _ := errors.AsType[*errs.Error](s.Err()); e == nil || e.Message != "The server refused the connection: Invalid token." {
		t.Errorf("message %v", s.Err())
	}
	if cf := <-serverSaw; cf == nil || cf.Code != wsframe.CloseNormalClosure {
		t.Errorf("server saw %v", cf)
	}
	if got := ev.summary(); got != "sent:connection_init received:connection_error(err)" {
		t.Errorf("events %s", got)
	}
}

func TestLegacySubprotocolNotSelected(t *testing.T) {
	// A graphql-transport-ws only server does not select graphql-ws.
	o, _ := legacyOptions(t, serverOptions{}, func(p *peer) { p.drain() })
	s, err := Dial(context.Background(), o)
	if s != nil || errCode(err) != CodeSubprotocolNotSelected {
		t.Fatalf("Dial = %v, %v", s, err)
	}
	if e, _ := errors.AsType[*errs.Error](err); e == nil || e.Message != "The server did not select the graphql-ws subprotocol." {
		t.Errorf("error %v", err)
	}
}

func TestLegacyInvalidMessages(t *testing.T) {
	for name, msg := range map[string]string{
		"ping":             `{"type":"ping"}`,
		"next":             `{"type":"next","id":"1","payload":{"data":{}}}`,
		"start":            `{"type":"start","id":"1","payload":{}}`,
		"data without id":  `{"type":"data","payload":{"data":{}}}`,
		"data not object":  `{"type":"data","id":"1","payload":[]}`,
		"error no message": `{"type":"error","id":"1","payload":{"msg":"x"}}`,
		"unknown":          `{"type":"subscription_data","id":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			serverSaw := make(chan *wsframe.CloseError, 1)
			o, _ := legacyOptions(t, legacyServer, func(p *peer) {
				p.init()
				p.expect("start", "1")
				p.send(msg)
				serverSaw <- p.drain()
			})
			s := dial(t, o)
			sub := subscribe(t, s, "subscription { a }")
			if r := next(t, sub); r.Kind != ResultClosed || errCode(r.Err) != CodeInvalidMessage {
				t.Errorf("result %+v", r)
			}
			if cf := <-serverSaw; cf == nil || cf.Code != CloseBadRequest {
				t.Errorf("server saw %v", cf)
			}
		})
	}
}

func TestLegacyPing(t *testing.T) {
	o, _ := legacyOptions(t, legacyServer, func(p *peer) {
		p.init()
		p.drain()
	})
	o.PingInterval = 1 // ignored: the protocol has no ping
	s := dial(t, o)
	if err := s.Ping(nil); errCode(err) != CodeInvalidArgument {
		t.Errorf("Ping = %v", err)
	}
	o.Protocol = "graphql-over-carrier-pigeon"
	if _, err := Dial(context.Background(), o); errCode(err) != CodeInvalidArgument {
		t.Errorf("Dial with unknown protocol = %v", err)
	}
}

func TestParseLegacy(t *testing.T) {
	for _, g := range []string{
		`{"type":"connection_ack"}`,
		`{"type":"ka"}`,
		`{"type":"connection_error","payload":{"message":"no"}}`,
		`{"type":"data","id":"1","payload":{"data":null}}`,
		`{"type":"error","id":"1","payload":{"message":"m"}}`,
		`{"type":"error","payload":[{"message":"m"}]}`,
		`{"type":"complete","id":"1"}`,
	} {
		if _, problem := parse([]byte(g), ProtocolLegacy); problem != "" {
			t.Errorf("parse(%s): %s", g, problem)
		}
	}
	for _, b := range []string{`{"type":"complete"}`, `{"type":"pong"}`, `{"type":"connection_terminate"}`, `{"type":"stop","id":"1"}`, `{"type":"connection_ack","payload":1}`} {
		if _, problem := parse([]byte(b), ProtocolLegacy); problem == "" {
			t.Errorf("parse(%s) accepted", b)
		}
	}
}
