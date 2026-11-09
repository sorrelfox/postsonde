package transportws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

func TestHappyPath(t *testing.T) {
	serverDone := make(chan *wsframe.CloseError, 1)
	var initMsg, subMsg serverMsg
	var protocol string
	srv := startServer(t, serverOptions{}, func(p *peer) {
		protocol = p.protocol
		initMsg = p.expect("connection_init", "")
		p.send(`{"type":"connection_ack","payload":{"server":"test"}}`)
		subMsg = p.expect("subscribe", "")
		for i := 1; i <= 3; i++ {
			p.send(fmt.Sprintf(`{"id":%q,"type":"next","payload":{"data":{"count":%d}}}`, subMsg.ID, i))
		}
		p.send(`{"id":"` + subMsg.ID + `","type":"complete"}`)
		serverDone <- p.drain()
	})
	ev := &events{}
	o := options(srv, ev)
	o.ConnectionParams = json.RawMessage(` {"authToken":"<secret>"} `)
	o.WS.Subprotocols = []string{"ignored"}
	s := dial(t, o)

	sub, err := s.Subscribe(context.Background(), SubscribeRequest{
		Query:         "subscription Count($n: Int) { count(n: $n) }",
		OperationName: "Count",
		Variables:     json.RawMessage(`{"n":3}`),
		Extensions:    json.RawMessage(`{"persistedQuery":null}`),
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub.ID() != "1" {
		t.Errorf("id = %q, want 1", sub.ID())
	}
	for i := 1; i <= 3; i++ {
		r := next(t, sub)
		want := fmt.Sprintf(`{"data":{"count":%d}}`, i)
		if r.Kind != ResultNext || string(r.Payload) != want || r.HasErrors || r.At.IsZero() {
			t.Errorf("result %d = %+v, want next %s", i, r, want)
		}
	}
	if r := next(t, sub); r.Kind != ResultComplete {
		t.Errorf("last result = %+v, want complete", r)
	}
	expectClosedC(t, sub)
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("Unsubscribe after complete: %v", err)
	}
	if s.State() != StateReady {
		t.Errorf("state = %s", s.State())
	}
	if string(s.AckPayload()) != `{"server":"test"}` {
		t.Errorf("ack payload = %s", s.AckPayload())
	}

	if err := s.Close(context.Background(), wsframe.CloseNormalClosure, "bye"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cf := <-serverDone; cf == nil || cf.Code != 1000 || cf.Reason != "bye" {
		t.Errorf("server saw close %v", cf)
	}
	if s.Err() != nil || s.State() != StateClosed {
		t.Errorf("after Close: err %v, state %s", s.Err(), s.State())
	}

	if protocol != Subprotocol {
		t.Errorf("offered subprotocols %q", protocol)
	}
	if initMsg.raw != `{"type":"connection_init","payload":{"authToken":"<secret>"}}` {
		t.Errorf("connection_init = %s", initMsg.raw)
	}
	wantSub := `{"type":"subscribe","id":"1","payload":{"query":"subscription Count($n: Int) { count(n: $n) }","operationName":"Count","variables":{"n":3},"extensions":{"persistedQuery":null}}}`
	if subMsg.raw != wantSub {
		t.Errorf("subscribe = %s\nwant        %s", subMsg.raw, wantSub)
	}
	want := "sent:connection_init received:connection_ack sent:subscribe#1 received:next#1 received:next#1 received:next#1 received:complete#1"
	if got := ev.summary(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	for _, e := range ev.all() {
		if e.At.IsZero() || len(e.Data) == 0 {
			t.Errorf("event without time or data: %+v", e)
		}
	}
}

func TestMultiplexedSubscriptions(t *testing.T) {
	const n = 5
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		var ids []string
		for range n {
			ids = append(ids, p.expect("subscribe", "").ID)
		}
		// Interleave results of all subscriptions, then complete them in
		// reverse order.
		for round := 0; round < 3; round++ {
			for _, id := range ids {
				p.send(fmt.Sprintf(`{"type":"next","id":%q,"payload":{"data":{"id":%q,"round":%d}}}`, id, id, round))
			}
		}
		for i := len(ids) - 1; i >= 0; i-- {
			p.send(`{"type":"complete","id":"` + ids[i] + `"}`)
		}
		p.drain()
	})
	s := dial(t, options(srv, &events{}))

	subs := make([]*Subscription, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { subs[i] = subscribe(t, s, "subscription { tick }") })
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, sub := range subs {
		if sub == nil {
			t.FailNow()
		}
		if seen[sub.ID()] {
			t.Fatalf("duplicate id %s", sub.ID())
		}
		seen[sub.ID()] = true
	}
	for _, sub := range subs {
		wg.Go(func() {
			for round := 0; round < 3; round++ {
				r := next(t, sub)
				want := fmt.Sprintf(`{"data":{"id":%q,"round":%d}}`, sub.ID(), round)
				if string(r.Payload) != want {
					t.Errorf("sub %s: got %s, want %s", sub.ID(), r.Payload, want)
				}
			}
			if r := next(t, sub); r.Kind != ResultComplete {
				t.Errorf("sub %s: %+v, want complete", sub.ID(), r)
			}
			expectClosedC(t, sub)
		})
	}
	wg.Wait()
}

func TestUnsubscribeMidStream(t *testing.T) {
	gotComplete := make(chan serverMsg, 1)
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		id := p.expect("subscribe", "").ID
		p.send(`{"type":"next","id":"` + id + `","payload":{"data":{"n":1}}}`)
		gotComplete <- p.expect("complete", id)
		// A result crossing the client's complete, for the now unknown id.
		p.send(`{"type":"next","id":"` + id + `","payload":{"data":{"n":2}}}`)
		p.send(`{"type":"ping"}`)
		p.expect("pong", "")
		p.drain()
	})
	ev := &events{}
	s := dial(t, options(srv, ev))
	sub := subscribe(t, s, "subscription { n }")
	if r := next(t, sub); string(r.Payload) != `{"data":{"n":1}}` {
		t.Fatalf("first result %+v", r)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	expectClosedC(t, sub)
	if m := <-gotComplete; m.raw != `{"type":"complete","id":"1"}` {
		t.Errorf("complete = %s", m.raw)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("second Unsubscribe: %v", err)
	}
	want := "sent:connection_init received:connection_ack sent:subscribe#1 received:next#1 sent:complete#1 received:next#1(unknown_id) received:ping sent:pong"
	waitEvents(t, ev, "sent:pong")
	if got := ev.summary(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if s.State() != StateReady {
		t.Errorf("state %s; an unknown id must not close the session", s.State())
	}
}

func TestSubscriptionError(t *testing.T) {
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		id := p.expect("subscribe", "").ID
		p.send(`{"type":"next","id":"` + id + `","payload":{"data":null,"errors":[{"message":"partial","path":["a"]}]}}`)
		p.send(`{"type":"error","id":"` + id + `","payload":[{"message":"Cannot query field \"x\"","locations":[{"line":1,"column":16}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`)
		// After error the subscription is over: a complete is not expected.
		p.send(`{"type":"ping","payload":{"k":1}}`)
		if m := p.expect("pong", ""); string(m.Payload) != `{"k":1}` {
			t.Errorf("pong payload %s, want echo", m.Payload)
		}
		p.drain()
	})
	ev := &events{}
	o := options(srv, ev)
	o.EchoPingPayload = true
	s := dial(t, o)
	sub := subscribe(t, s, "subscription { x }")
	r := next(t, sub)
	if r.Kind != ResultNext || !r.HasErrors || r.Errors[0].Message != "partial" || string(r.Errors[0].Path) != `["a"]` {
		t.Errorf("next with errors = %+v", r)
	}
	r = next(t, sub)
	if r.Kind != ResultError || !r.HasErrors || len(r.Errors) != 1 {
		t.Fatalf("error result = %+v", r)
	}
	e := r.Errors[0]
	if e.Message != `Cannot query field "x"` || len(e.Locations) != 1 || e.Locations[0] != (Location{1, 16}) ||
		string(e.Extensions) != `{"code":"GRAPHQL_VALIDATION_FAILED"}` || !strings.HasPrefix(string(e.Raw), `{"message"`) {
		t.Errorf("error = %+v", e)
	}
	expectClosedC(t, sub)
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("Unsubscribe after error: %v", err)
	}
	waitEvents(t, ev, "sent:pong")
	if err := s.Close(context.Background(), 1000, ""); err != nil {
		t.Fatal(err)
	}
}

func TestServerPingAndClientPings(t *testing.T) {
	pings := make(chan serverMsg, 10)
	srv := startServer(t, serverOptions{}, func(p *peer) {
		acked := false
		for {
			m, err := p.tryRead()
			if err != nil {
				return
			}
			switch m.Type {
			case "connection_init":
				// Pings are allowed before connection_ack.
				p.send(`{"type":"ping","payload":{"early":true}}`)
			case "pong":
				if m.raw != `{"type":"pong"}` {
					t.Errorf("pong without echo = %s", m.raw)
				}
				if !acked {
					acked = true
					p.send(`{"type":"connection_ack"}`)
				}
			case "ping":
				pings <- m
				p.send(`{"type":"pong"}`)
			}
		}
	})
	ev := &events{}
	o := options(srv, ev)
	o.PingInterval = 20 * time.Millisecond
	s := dial(t, o)
	for range 3 {
		select {
		case m := <-pings:
			if m.raw != `{"type":"ping"}` {
				t.Errorf("client ping = %s", m.raw)
			}
		case <-time.After(waitLimit):
			t.Fatal("no client ping")
		}
	}
	if err := s.Ping(json.RawMessage(`{"manual":1}`)); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if err := s.Ping(json.RawMessage(`[1]`)); errCode(err) != CodeInvalidArgument {
		t.Errorf("Ping with array: %v", err)
	}
	if err := s.Close(context.Background(), 1000, ""); err != nil {
		t.Fatal(err)
	}
	sum := ev.summary()
	for _, want := range []string{"received:ping sent:pong", "sent:ping", "received:pong"} {
		if !strings.Contains(sum, want) {
			t.Errorf("events %s: missing %q", sum, want)
		}
	}
}

func TestAckTimeout(t *testing.T) {
	serverSaw := make(chan *wsframe.CloseError, 1)
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.expect("connection_init", "")
		serverSaw <- p.drain()
	})
	o := options(srv, &events{})
	o.ConnectionInitTimeout = 50 * time.Millisecond
	s := dial(t, o)

	_, err := s.Subscribe(context.Background(), SubscribeRequest{Query: "subscription { a }"})
	if errCode(err) != CodeAckTimeout || !errors.Is(err, &errs.Error{Kind: errs.Timeout}) {
		t.Errorf("Subscribe: %v, want ack timeout", err)
	}
	waitDone(t, s)
	if errCode(s.Err()) != CodeAckTimeout {
		t.Errorf("Err = %v", s.Err())
	}
	cf := <-serverSaw
	if cf == nil || cf.Code != CloseConnectionInitTimeout || cf.Reason != "Connection initialisation timeout" {
		t.Errorf("server saw %v, want 4408", cf)
	}
	if info := s.CloseInfo(); info == nil || info.InitiatedBy != wsclient.InitiatorLocal || info.Sent.Code != 4408 {
		t.Errorf("close info %+v", info)
	}
}

func TestSubprotocolNotSelected(t *testing.T) {
	serverSaw := make(chan *wsframe.CloseError, 1)
	srv := startServer(t, serverOptions{noSubprotocol: true}, func(p *peer) {
		serverSaw <- p.drain()
	})
	ev := &events{}
	o := options(srv, ev)
	s, err := Dial(context.Background(), o)
	if s != nil || errCode(err) != CodeSubprotocolNotSelected || !errors.Is(err, &errs.Error{Kind: errs.Protocol}) {
		t.Fatalf("Dial = %v, %v; want subprotocol error", s, err)
	}
	if cf := <-serverSaw; cf == nil || cf.Code != CloseSubprotocolNotAcceptable {
		t.Errorf("server saw %v, want 4406", cf)
	}
	if got := ev.summary(); got != "" {
		t.Errorf("events %q, want none (no connection_init)", got)
	}
}

func TestHandshakeFailure(t *testing.T) {
	o := Options{WS: wsclient.Options{URL: "http://example.com"}}
	o.WS.Dialer = options(startServer(t, serverOptions{}, func(*peer) {}), &events{}).WS.Dialer
	if _, err := Dial(context.Background(), o); errCode(err) != wsclient.CodeSchemeUnsupported {
		t.Errorf("Dial http URL: %v", err)
	}
	o.ConnectionParams = json.RawMessage(`"token"`)
	if _, err := Dial(context.Background(), o); errCode(err) != CodeInvalidArgument {
		t.Errorf("Dial with string params: %v", err)
	}
}

func TestServerClose(t *testing.T) {
	cases := []struct {
		code     wsframe.StatusCode
		reason   string
		kind     errs.Kind
		afterAck bool
	}{
		{4401, "Unauthorized", errs.PermissionDenied, true},
		{4403, "Forbidden", errs.PermissionDenied, false},
		{4500, "Internal server error", errs.Network, true},
		{4409, "Subscriber for 1 already exists", errs.Protocol, true},
		{1000, "", errs.Network, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			srv := startServer(t, serverOptions{}, func(p *peer) {
				if tc.afterAck {
					p.init()
					p.expect("subscribe", "")
					p.expect("subscribe", "")
				} else {
					p.expect("connection_init", "")
				}
				p.closeWith(tc.code, tc.reason)
			})
			s := dial(t, options(srv, &events{}))
			var subs []*Subscription
			if tc.afterAck {
				subs = append(subs, subscribe(t, s, "subscription { a }"), subscribe(t, s, "subscription { b }"))
			} else {
				_, err := s.Subscribe(context.Background(), SubscribeRequest{Query: "subscription { a }"})
				if errCode(err) != CodeClosedByServer {
					t.Errorf("Subscribe before ack: %v", err)
				}
			}
			waitDone(t, s)
			err := s.Err()
			if errCode(err) != CodeClosedByServer || !errors.Is(err, &errs.Error{Kind: tc.kind}) ||
				detail(err, "code") != fmt.Sprint(int(tc.code)) || detail(err, "reason") != tc.reason {
				t.Errorf("Err = %#v", err)
			}
			if !strings.Contains(err.Error(), CloseCodeName(tc.code)) {
				t.Errorf("message %q does not name the code", err.Error())
			}
			info := s.CloseInfo()
			if info.Code != tc.code || info.Reason != tc.reason || !info.WasClean || info.InitiatedBy != wsclient.InitiatorRemote {
				t.Errorf("close info %+v", info)
			}
			for _, sub := range subs {
				r := next(t, sub)
				if r.Kind != ResultClosed || errCode(r.Err) != CodeClosedByServer || detail(r.Err, "code") != fmt.Sprint(int(tc.code)) {
					t.Errorf("sub %s: %+v", sub.ID(), r)
				}
				expectClosedC(t, sub)
			}
			if _, err := s.Subscribe(context.Background(), SubscribeRequest{}); errCode(err) != CodeClosedByServer {
				t.Errorf("Subscribe after close: %v", err)
			}
			if s.State() != StateClosed {
				t.Errorf("state %s", s.State())
			}
		})
	}
}

func TestInvalidMessages(t *testing.T) {
	cases := []struct {
		name    string
		preAck  bool // send before connection_ack
		message string
	}{
		{"not JSON", false, `hello`},
		{"array", false, `[1]`},
		{"no type", false, `{"id":"1"}`},
		{"type not string", false, `{"type":1}`},
		{"unknown type", false, `{"type":"data","id":"1","payload":{}}`},
		{"subscribe from server", false, `{"type":"subscribe","id":"1","payload":{"query":"{a}"}}`},
		{"connection_init from server", false, `{"type":"connection_init"}`},
		{"duplicate ack", false, `{"type":"connection_ack"}`},
		{"next without id", false, `{"type":"next","payload":{"data":{}}}`},
		{"next with numeric id", false, `{"type":"next","id":1,"payload":{"data":{}}}`},
		{"next without payload", false, `{"type":"next","id":"1"}`},
		{"next with null payload", false, `{"type":"next","id":"1","payload":null}`},
		{"error with empty array", false, `{"type":"error","id":"1","payload":[]}`},
		{"error without message", false, `{"type":"error","id":"1","payload":[{"msg":"x"}]}`},
		{"complete without id", false, `{"type":"complete"}`},
		{"ack payload array", true, `{"type":"connection_ack","payload":[]}`},
		{"next before ack", true, `{"type":"next","id":"1","payload":{"data":{}}}`},
		{"binary", false, "\x00binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverSaw := make(chan *wsframe.CloseError, 1)
			srv := startServer(t, serverOptions{}, func(p *peer) {
				p.expect("connection_init", "")
				if !tc.preAck {
					p.send(`{"type":"connection_ack"}`)
					p.expect("subscribe", "1")
				}
				if strings.HasPrefix(tc.message, "\x00") {
					_ = p.conn.WriteMessage(wsframe.OpBinary, []byte(tc.message))
				} else {
					p.send(tc.message)
				}
				// Anything after the invalid message is ignored.
				_ = p.conn.WriteMessage(wsframe.OpText, []byte(`{"type":"next","id":"1","payload":{"data":{}}}`))
				serverSaw <- p.drain()
			})
			ev := &events{}
			s := dial(t, options(srv, ev))
			var sub *Subscription
			if !tc.preAck {
				sub = subscribe(t, s, "subscription { a }")
			}
			waitDone(t, s)
			if errCode(s.Err()) != CodeInvalidMessage || !errors.Is(s.Err(), &errs.Error{Kind: errs.Protocol}) {
				t.Errorf("Err = %v", s.Err())
			}
			cf := <-serverSaw
			if cf == nil || cf.Code != CloseBadRequest || cf.Reason != "Invalid message received" {
				t.Errorf("server saw %v, want 4400", cf)
			}
			if sub != nil {
				if r := next(t, sub); r.Kind != ResultClosed || errCode(r.Err) != CodeInvalidMessage {
					t.Errorf("subscription result %+v", r)
				}
				expectClosedC(t, sub)
			}
			var bad, ignored int
			for _, e := range ev.all() {
				if e.Direction == Received && e.Err != nil {
					bad++
					if string(e.Data) != tc.message {
						t.Errorf("event data %q", e.Data)
					}
				}
				if e.Note == NoteIgnored {
					ignored++
				}
			}
			if bad != 1 || ignored > 1 {
				t.Errorf("events %s", ev.summary())
			}
		})
	}
}

func TestSubscribeWaitsForAck(t *testing.T) {
	release := make(chan struct{})
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.expect("connection_init", "")
		<-release
		p.send(`{"type":"connection_ack"}`)
		id := p.expect("subscribe", "").ID
		p.send(`{"type":"complete","id":"` + id + `"}`)
		p.drain()
	})
	o := options(srv, &events{})
	o.ConnectionInitTimeout = -1
	s := dial(t, o)
	if s.State() != StateInitialising {
		t.Errorf("state %s", s.State())
	}

	got := make(chan *Subscription, 1)
	go func() {
		sub, err := s.Subscribe(context.Background(), SubscribeRequest{Query: "subscription { a }"})
		if err != nil {
			t.Errorf("Subscribe: %v", err)
		}
		got <- sub
	}()
	select {
	case <-got:
		t.Fatal("Subscribe returned before connection_ack")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	var sub *Subscription
	select {
	case sub = <-got:
	case <-time.After(waitLimit):
		t.Fatal("Subscribe did not return after connection_ack")
	}
	if sub == nil {
		t.FailNow()
	}
	if r := next(t, sub); r.Kind != ResultComplete {
		t.Errorf("result %+v", r)
	}
	select {
	case <-s.Acked():
	default:
		t.Error("Acked not closed")
	}
}

func TestCancellation(t *testing.T) {
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.expect("connection_init", "")
		p.drain()
	})
	ev := &events{}
	o := options(srv, ev)
	o.ConnectionInitTimeout = -1
	s := dial(t, o)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := s.Subscribe(ctx, SubscribeRequest{Query: "subscription { a }"})
	if errCode(err) != CodeCancelled || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Subscribe with deadline: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := s.Subscribe(ctx2, SubscribeRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Subscribe with cancelled ctx: %v", err)
	}
	if _, err := s.Subscribe(context.Background(), SubscribeRequest{Variables: json.RawMessage(`[]`)}); errCode(err) != CodeInvalidArgument {
		t.Errorf("Subscribe with array variables: %v", err)
	}
	if got := ev.summary(); got != "sent:connection_init" {
		t.Errorf("events %s", got)
	}

	// Close while a Subscribe is waiting: it fails with session_closed.
	waiting := make(chan error, 1)
	go func() {
		_, err := s.Subscribe(context.Background(), SubscribeRequest{})
		waiting <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := s.Close(context.Background(), 1000, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; errCode(err) != CodeSessionClosed {
		t.Errorf("waiting Subscribe: %v", err)
	}
	if err := s.Close(context.Background(), 1000, ""); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// Dial with a cancelled context.
	if _, err := Dial(ctx2, options(srv, &events{})); !errors.Is(err, context.Canceled) {
		t.Errorf("Dial with cancelled ctx: %v", err)
	}
}

func TestLocalCloseEndsSubscriptions(t *testing.T) {
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		p.expect("subscribe", "")
		p.drain()
	})
	s := dial(t, options(srv, &events{}))
	sub := subscribe(t, s, "subscription { a }")
	if err := s.Close(context.Background(), 4000, "done"); err != nil {
		t.Fatal(err)
	}
	r := next(t, sub)
	if r.Kind != ResultClosed || errCode(r.Err) != CodeSessionClosed {
		t.Errorf("result %+v", r)
	}
	expectClosedC(t, sub)
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("Unsubscribe after close: %v", err)
	}
	if err := s.Close(context.Background(), 1, ""); err == nil {
		t.Error("Close with invalid code succeeded")
	}
}

func TestDroppedTransport(t *testing.T) {
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		p.expect("subscribe", "")
		// Return without a close frame: the connection drops.
	})
	s := dial(t, options(srv, &events{}))
	sub := subscribe(t, s, "subscription { a }")
	waitDone(t, s)
	if errCode(s.Err()) != wsframe.CodeAbnormalClosure {
		t.Errorf("Err = %v", s.Err())
	}
	if r := next(t, sub); r.Kind != ResultClosed || errCode(r.Err) != wsframe.CodeAbnormalClosure {
		t.Errorf("result %+v", r)
	}
}

func TestUnreadResultsDoNotStallTheConnection(t *testing.T) {
	const n = 200
	srv := startServer(t, serverOptions{}, func(p *peer) {
		p.init()
		a := p.expect("subscribe", "").ID
		b := p.expect("subscribe", "").ID
		for i := range n {
			p.send(fmt.Sprintf(`{"type":"next","id":%q,"payload":{"data":{"i":%d}}}`, a, i))
		}
		p.send(`{"type":"complete","id":"` + b + `"}`)
		p.drain()
	})
	s := dial(t, options(srv, &events{}))
	a := subscribe(t, s, "subscription { a }")
	b := subscribe(t, s, "subscription { b }")
	// b completes although nobody reads a.
	if r := next(t, b); r.Kind != ResultComplete {
		t.Fatalf("b: %+v", r)
	}
	if err := a.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	for range a.C() {
	}
}

func TestParse(t *testing.T) {
	good := []string{
		`{"type":"connection_ack"}`,
		`{"type":"connection_ack","payload":null}`,
		`{"type":"ping","payload":{}}`,
		` {"type":"pong"} `,
		`{"type":"next","id":"x","payload":{"data":null},"extra":1}`,
		`{"type":"error","id":"x","payload":[{"message":"m"}]}`,
		`{"type":"complete","id":"x"}`,
	}
	for _, g := range good {
		if _, problem := parse([]byte(g), ProtocolTransportWS); problem != "" {
			t.Errorf("parse(%s): %s", g, problem)
		}
	}
	for _, b := range []string{``, `null`, `"x"`, `{"type":"next","id":"","payload":{}}`, `{"type":"pong","payload":1}`, `{"type":"complete","id":null}`, `{bad`} {
		if _, problem := parse([]byte(b), ProtocolTransportWS); problem == "" {
			t.Errorf("parse(%s) accepted", b)
		}
	}
}

func TestCloseCodeName(t *testing.T) {
	for code, want := range map[wsframe.StatusCode]string{
		4400: "invalid message", 4401: "unauthorized", 4403: "forbidden", 4406: "subprotocol not acceptable",
		4408: "connection initialisation timeout", 4409: "subscriber already exists",
		4429: "too many initialisation requests", 4500: "internal server error",
		1000: "normal closure", 1001: "going away", 4123: "",
	} {
		if got := CloseCodeName(code); got != want {
			t.Errorf("CloseCodeName(%d) = %q, want %q", code, got, want)
		}
	}
}
