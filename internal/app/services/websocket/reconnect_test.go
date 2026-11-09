package websocket

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

// scriptServer upgrades every request and hands the n-th connection
// (from 1) to handle, with the request, the server side Conn and the
// transport. autoPong false makes the server ignore pings.
func scriptServer(t *testing.T, autoPong func(n int) bool, handle func(n int, r *http.Request, conn *wsframe.Conn, nc net.Conn)) *httptest.Server {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(count.Add(1))
		nc, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = nc.Close() }()
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
		if _, err := nc.Write([]byte(resp)); err != nil {
			return
		}
		conn := wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader, DisableAutoPong: autoPong != nil && !autoPong(n)})
		handle(n, r, conn, nc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readAll reads until the connection ends.
func readAll(conn *wsframe.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// echoAll echoes text messages; "drop" closes the transport, "close:N"
// closes with code N.
func echoAll(conn *wsframe.Conn, nc net.Conn) {
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg := string(data)
		switch {
		case msg == "drop":
			_ = nc.Close()
			return
		case strings.HasPrefix(msg, "close:"):
			code, _ := strconv.Atoi(strings.TrimPrefix(msg, "close:"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Close(ctx, wsframe.StatusCode(code), "bye")
			cancel()
			return
		default:
			_ = conn.WriteMessage(op, data)
		}
	}
}

// stateLine is "state@attempt" plus the reconnect decision of a closed
// event: "+delay" or "!stopped".
func stateLine(ev StateEventDTO) string {
	s := ev.State + "@" + strconv.Itoa(ev.Attempt)
	if r := ev.Reconnect; r != nil {
		if r.Stopped != "" {
			s += "!" + r.Stopped
		} else {
			s += "+" + strconv.FormatInt(r.DelayMs, 10)
		}
	}
	return s
}

func (e *events) lines(id string) string {
	var s []string
	for _, ev := range e.states {
		if ev.ConnID == id {
			s = append(s, stateLine(ev))
		}
	}
	return strings.Join(s, " ")
}

func (e *events) waitLines(t *testing.T, id, want string) {
	t.Helper()
	e.waitFor(t, want, func(e *events) bool { return e.lines(id) == want })
}

// waitTimers waits until the fake clock has n timers pending.
func waitTimers(t *testing.T, clock *fakeclock.Clock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for clock.Pending() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d timers pending, want %d", clock.Pending(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func newClock() *fakeclock.Clock {
	return fakeclock.New(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
}

func TestPing(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, nil, func(_ int, _ *http.Request, conn *wsframe.Conn, nc net.Conn) {
		_ = conn.Ping([]byte("from server"))
		echoAll(conn, nc)
	})
	s, ev := newService(t, nil)
	ctx := t.Context()
	conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/")})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	ev.waitFor(t, "the server's ping", func(e *events) bool { return len(e.log(id)) == 2 })

	if res, err := s.Ping(ctx, PingInputDTO{ConnID: id, Payload: "are you there"}); err != nil || res.Bytes != 13 {
		t.Fatalf("Ping = %+v, %v", res, err)
	}
	ev.waitFor(t, "the first pong", func(e *events) bool { return len(e.log(id)) == 4 })
	if res, err := s.Ping(ctx, PingInputDTO{ConnID: id, Payload: "01 02", Format: FormatHex}); err != nil || res.Bytes != 2 {
		t.Fatalf("Ping hex = %+v, %v", res, err)
	}
	ev.waitFor(t, "the pongs", func(e *events) bool { return len(e.log(id)) == 6 })
	ev.mu.Lock()
	got := ev.log(id)
	var pongs []MessageDTO
	for _, m := range ev.messages {
		for _, msg := range m.Messages {
			if msg.Opcode == "pong" && msg.Direction == "received" {
				pongs = append(pongs, msg)
			}
		}
	}
	ev.mu.Unlock()
	want := []string{
		"received ping from server",
		"sent pong from server",
		"sent ping are you there",
		"received pong are you there",
		"sent ping \x01\x02",
		"received pong \x01\x02",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("log = %q, want %q", got, want)
	}
	for _, p := range pongs {
		if p.RTTMs == nil || *p.RTTMs < 0 || p.Keepalive {
			t.Errorf("pong = %+v", p)
		}
	}

	_, err = s.Ping(ctx, PingInputDTO{ConnID: id, Payload: strings.Repeat("x", 126)})
	checkCode(t, err, errs.InvalidArgument, CodePingTooLong)
	_, err = s.Ping(ctx, PingInputDTO{ConnID: id, Payload: "zz", Format: FormatHex})
	if errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("bad hex = %v", err)
	}
	_, err = s.Ping(ctx, PingInputDTO{ConnID: id, Format: "xml"})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidFormat)
	_, err = s.Ping(ctx, PingInputDTO{ConnID: "ws_unknown"})
	checkCode(t, err, errs.NotFound, CodeNotFound)

	// Pings and pongs are not counted as messages.
	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	if c := ev.state(id, "closed").Close; c.MessagesSent != 0 || c.MessagesReceived != 0 {
		t.Errorf("close = %+v", c)
	}
}

// TestKeepaliveTimeoutThenReconnect: a server that stops answering pings
// is closed with wsclient.pong_timeout, and auto-reconnect connects again
// to a server that answers.
func TestKeepaliveTimeoutThenReconnect(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, func(n int) bool { return n > 1 }, func(n int, _ *http.Request, conn *wsframe.Conn, nc net.Conn) {
		if n == 1 {
			readAll(conn)
			return
		}
		echoAll(conn, nc)
	})
	clock := newClock()
	s, ev := newService(t, nil, withClock(clock))
	ctx := t.Context()
	on := true
	conn, err := s.Connect(ctx, ConnectInputDTO{
		URL:              wsURL(srv, "/"),
		PingIntervalMs:   new(int64(1000)),
		PongTimeoutMs:    new(int64(2500)),
		AutoReconnect:    &on,
		ReconnectDelayMs: new(int64(500)),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitLines(t, id, "connecting@1 open@1")
	waitTimers(t, clock, 1)
	clock.Advance(time.Second) // ping; the deadline is at 3.5 s
	clock.Advance(2 * time.Second)
	ev.waitFor(t, "3 pings", func(e *events) bool { return len(e.log(id)) == 3 })
	ev.mu.Lock()
	got := ev.lines(id)
	ev.mu.Unlock()
	if got != "connecting@1 open@1" {
		t.Fatalf("closed before the deadline: %s", got)
	}
	clock.Advance(500 * time.Millisecond)
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+500")
	closed := ev.state(id, "closed")
	if closed.Error == nil || closed.Error.Code != wsclient.CodePongTimeout || closed.Close.InitiatedBy != "local" || closed.Close.Code != 1006 {
		t.Fatalf("closed = %+v, error %+v", closed.Close, closed.Error)
	}
	if !strings.Contains(closed.Error.Message, "2500 ms") {
		t.Errorf("message = %q", closed.Error.Message)
	}

	waitTimers(t, clock, 1) // the back-off
	clock.Advance(500 * time.Millisecond)
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+500 connecting@2 open@2")
	waitTimers(t, clock, 1) // the keepalive of attempt 2
	clock.Advance(time.Second)
	ev.waitFor(t, "an answered keepalive", func(e *events) bool {
		for _, m := range e.messages {
			for _, msg := range m.Messages {
				if msg.Opcode == "pong" && msg.Keepalive && msg.RTTMs != nil {
					return true
				}
			}
		}
		return false
	})
	waitTimers(t, clock, 1) // the pong cleared the deadline
	clock.Advance(3 * time.Second)

	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	ev.mu.Lock()
	got = ev.lines(id)
	ev.mu.Unlock()
	if got != "connecting@1 open@1 closed@1+500 connecting@2 open@2 closing@2 closed@2!manual" {
		t.Errorf("states = %s", got)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers left", clock.Pending())
	}
}

// TestReconnectBackoffAndVariables: a refused connection is tried again
// with a doubling delay capped at the maximum, {{variables}} rendered
// anew for each attempt, until the maximum number of attempts.
func TestReconnectBackoffAndVariables(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // refused from now on
	var renders atomic.Int32
	clock := newClock()
	s, ev := newService(t, nil, withClock(clock), WithScopes(func(context.Context, string) (template.Chain, error) {
		n := renders.Add(1)
		return template.Chain{template.Vars("Globals", map[string]string{"n": strconv.Itoa(int(n))})}, nil
	}))
	on := true
	conn, err := s.Connect(t.Context(), ConnectInputDTO{
		RequestID:            "req_feed",
		URL:                  "ws://" + addr + "/?n={{n}}",
		AutoReconnect:        &on,
		ReconnectMaxAttempts: new(3),
		ReconnectDelayMs:     new(int64(1000)),
		ReconnectMaxDelayMs:  new(int64(3000)),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	want := "connecting@1 closed@1+1000"
	ev.waitLines(t, id, want)
	for i, d := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		waitTimers(t, clock, 1)
		clock.Advance(d - time.Millisecond)
		if clock.Pending() != 1 {
			t.Fatalf("reconnect %d came before %v", i+1, d)
		}
		clock.Advance(time.Millisecond)
		n := strconv.Itoa(i + 2)
		switch i {
		case 0:
			want += " connecting@2 closed@2+2000"
		case 1:
			want += " connecting@3 closed@3+3000"
		case 2:
			want += " connecting@4 closed@4!max_attempts"
		}
		ev.waitLines(t, id, want)
		ev.mu.Lock()
		last := ev.states[len(ev.states)-1]
		ev.mu.Unlock()
		if !strings.HasSuffix(last.URL, "/?n="+n) || last.Error == nil || errs.Kind(last.Error.Kind) != errs.Network {
			t.Errorf("attempt %s: url %s, error %+v", n, last.URL, last.Error)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.get(id) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the connection was not forgotten")
		}
		time.Sleep(time.Millisecond)
	}
	if ev.state(id, "closed").Reconnect.MaxAttempts != 3 {
		t.Errorf("reconnect = %+v", ev.state(id, "closed").Reconnect)
	}
}

// TestReconnectAfterDropResetsAndCancel: the server drops the TCP
// connection; the client reconnects, the count starts over once open, and
// Disconnect while waiting cancels the reconnect.
func TestReconnectAfterDropResetsAndCancel(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, nil, func(n int, _ *http.Request, conn *wsframe.Conn, nc net.Conn) {
		if n == 1 {
			_ = nc.Close()
			return
		}
		echoAll(conn, nc)
	})
	clock := newClock()
	s, ev := newService(t, nil, withClock(clock))
	ctx := t.Context()
	on := true
	conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/"), AutoReconnect: &on})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+1000")
	if c := ev.state(id, "closed"); c.Error == nil || c.Close.Code != 1006 || c.Reconnect.Attempt != 1 || c.Reconnect.MaxAttempts != 5 {
		t.Fatalf("closed = %+v %+v", c.Close, c.Reconnect)
	}
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "while waiting"})
	checkCode(t, err, errs.InvalidArgument, CodeNotOpen)

	waitTimers(t, clock, 1)
	clock.Advance(time.Second)
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+1000 connecting@2 open@2")
	if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: "drop"}); err != nil {
		t.Fatal(err)
	}
	// Attempt 2 opened, so the next reconnect is number 1 again.
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+1000 connecting@2 open@2 closed@2+1000")

	waitTimers(t, clock, 1)
	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	ev.mu.Lock()
	got := ev.lines(id)
	last := ev.states[len(ev.states)-1]
	ev.mu.Unlock()
	if got != "connecting@1 open@1 closed@1+1000 connecting@2 open@2 closed@2+1000 closed@2!manual" {
		t.Errorf("states = %s", got)
	}
	if last.Close != nil || last.Error != nil {
		t.Errorf("cancel event = %+v", last)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers left", clock.Pending())
	}
	if s.get(id) != nil {
		t.Error("the connection was not forgotten")
	}
}

// TestReconnectCloseCodes: the server's close code decides whether to
// come back; a close by the user never reconnects.
func TestReconnectCloseCodes(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, nil, func(_ int, _ *http.Request, conn *wsframe.Conn, nc net.Conn) {
		echoAll(conn, nc)
	})
	clock := newClock()
	s, ev := newService(t, nil, withClock(clock))
	ctx := t.Context()
	on := true
	for _, tc := range []struct {
		code int
		want string
	}{
		{1000, "!close_code"},
		{1008, "!close_code"},
		{4001, "!close_code"},
		{1001, "+1000"},
		{1011, "+1000"},
		{1012, "+1000"},
		{1013, "+1000"},
		{0, "!manual"}, // closed by the user
	} {
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/"), AutoReconnect: &on})
		if err != nil {
			t.Fatal(err)
		}
		id := conn.ConnID
		ev.waitLines(t, id, "connecting@1 open@1")
		if tc.code == 0 {
			if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
				t.Fatal(err)
			}
			ev.waitLines(t, id, "connecting@1 open@1 closing@1 closed@1"+tc.want)
			continue
		}
		if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: "close:" + strconv.Itoa(tc.code)}); err != nil {
			t.Fatal(err)
		}
		ev.waitLines(t, id, "connecting@1 open@1 closing@1 closed@1"+tc.want)
		if c := ev.state(id, "closed"); c.Close.Code != tc.code || c.Close.InitiatedBy != "remote" || c.Error != nil {
			t.Errorf("%d: closed = %+v", tc.code, c.Close)
		}
		if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
			t.Fatal(err)
		}
	}

	// Without auto-reconnect no decision is reported.
	conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/")})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitLines(t, conn.ConnID, "connecting@1 open@1")
	if _, err := s.Send(ctx, SendInputDTO{ConnID: conn.ConnID, Text: "drop"}); err != nil {
		t.Fatal(err)
	}
	ev.waitLines(t, conn.ConnID, "connecting@1 open@1 closed@1")
}

// TestReconnectStopsWhenVariablesNoLongerRender: a variable that is gone
// by the time of the reconnect stops it with the template error.
func TestReconnectStopsWhenVariablesNoLongerRender(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, nil, func(n int, _ *http.Request, _ *wsframe.Conn, nc net.Conn) {
		_ = nc.Close()
	})
	var renders atomic.Int32
	clock := newClock()
	s, ev := newService(t, nil, withClock(clock), WithScopes(func(context.Context, string) (template.Chain, error) {
		if renders.Add(1) == 1 {
			return template.Chain{template.Vars("Globals", map[string]string{"path": "feed"})}, nil
		}
		return nil, nil
	}))
	on := true
	conn, err := s.Connect(t.Context(), ConnectInputDTO{RequestID: "req_feed", URL: wsURL(srv, "/{{path}}"), AutoReconnect: &on})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+1000")
	waitTimers(t, clock, 1)
	clock.Advance(time.Second)
	ev.waitLines(t, id, "connecting@1 open@1 closed@1+1000 closed@2!error")
	ev.mu.Lock()
	last := ev.states[len(ev.states)-1]
	ev.mu.Unlock()
	if last.Error == nil || last.Error.Code != template.CodeUnresolved || last.Close == nil || last.Close.Code != 1006 {
		t.Errorf("last = %+v, error %+v", last, last.Error)
	}
}

func TestBackoff(t *testing.T) {
	t.Parallel()
	p := newReconnectPolicy(ConnectInputDTO{AutoReconnect: new(true)})
	var got []string
	for n := 1; n <= 8; n++ {
		got = append(got, p.backoff(n).String())
	}
	if s := strings.Join(got, " "); s != "1s 2s 4s 8s 16s 30s 30s 30s" {
		t.Errorf("backoff = %s", s)
	}
	p = newReconnectPolicy(ConnectInputDTO{ReconnectDelayMs: new(int64(5)), ReconnectMaxDelayMs: new(int64(1)), ReconnectMaxAttempts: new(-1)})
	if p.enabled || p.delay != minInterval || p.maxDelay != minInterval || p.maxAttempts != 0 {
		t.Errorf("clamped policy = %+v", p)
	}
	var o wsclient.Options
	keepalive(&o, ConnectInputDTO{PingIntervalMs: new(int64(10))})
	if o.PingInterval != minInterval || o.PongTimeout != DefaultPongTimeout {
		t.Errorf("keepalive = %v %v", o.PingInterval, o.PongTimeout)
	}
	o = wsclient.Options{}
	keepalive(&o, ConnectInputDTO{PingIntervalMs: new(int64(0)), PongTimeoutMs: new(int64(5))})
	if o.PingInterval != 0 || o.PongTimeout != 0 {
		t.Errorf("keepalive off = %v %v", o.PingInterval, o.PongTimeout)
	}
}
