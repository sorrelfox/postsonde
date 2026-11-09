package transportws

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

const waitLimit = 5 * time.Second

// peer is the server side of one test connection.
type peer struct {
	t        *testing.T
	conn     *wsframe.Conn
	nc       net.Conn
	protocol string // the Sec-WebSocket-Protocol header the client sent
}

// serverMsg is a message the test server received.
type serverMsg struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
	raw     string
}

// read returns the next text message, failing the test on errors.
func (p *peer) read() serverMsg {
	p.t.Helper()
	m, err := p.tryRead()
	if err != nil {
		p.t.Errorf("server read: %v", err)
		panic(http.ErrAbortHandler)
	}
	return m
}

func (p *peer) tryRead() (serverMsg, error) {
	_ = p.nc.SetReadDeadline(time.Now().Add(waitLimit))
	op, data, err := p.conn.ReadMessage()
	if err != nil {
		return serverMsg{}, err
	}
	if op != wsframe.OpText {
		return serverMsg{}, errors.New("not a text message")
	}
	var m serverMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return serverMsg{}, err
	}
	m.raw = string(data)
	return m, nil
}

// expect reads the next message and checks its type (and id if not "").
func (p *peer) expect(typ, id string) serverMsg {
	p.t.Helper()
	m := p.read()
	if m.Type != typ || (id != "" && m.ID != id) {
		p.t.Errorf("server got %s, want type %q id %q", m.raw, typ, id)
		panic(http.ErrAbortHandler)
	}
	return m
}

func (p *peer) send(msg string) {
	p.t.Helper()
	if err := p.conn.WriteMessage(wsframe.OpText, []byte(msg)); err != nil {
		p.t.Errorf("server send: %v", err)
		panic(http.ErrAbortHandler)
	}
}

// init reads connection_init and acknowledges it.
func (p *peer) init() serverMsg {
	p.t.Helper()
	m := p.expect("connection_init", "")
	p.send(`{"type":"connection_ack"}`)
	return m
}

// closeWith starts the close handshake from the server and waits for the
// client's echo.
func (p *peer) closeWith(code wsframe.StatusCode, reason string) {
	p.t.Helper()
	if _, err := p.conn.WriteClose(code, reason); err != nil {
		p.t.Errorf("server close: %v", err)
	}
	p.drain()
}

// drain reads until the connection ends and returns the close frame the
// client sent, if any.
func (p *peer) drain() *wsframe.CloseError {
	for {
		if _, err := p.tryRead(); err != nil {
			return p.conn.PeerClose()
		}
	}
}

type serverOptions struct {
	noSubprotocol bool
	// subprotocol is the one the server selects when the client offers
	// it; "" is graphql-transport-ws.
	subprotocol string
}

// startServer runs handler for each upgraded connection. The connection is
// closed when handler returns.
func startServer(t *testing.T, so serverOptions, handler func(p *peer)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nc, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer nc.Close()
		extra := ""
		want := Subprotocol
		if so.subprotocol != "" {
			want = so.subprotocol
		}
		if !so.noSubprotocol && slices.Contains(strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ", "), want) {
			extra = "Sec-WebSocket-Protocol: " + want + "\r\n"
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n" + extra + "\r\n"
		if _, err := nc.Write([]byte(resp)); err != nil {
			t.Errorf("write 101: %v", err)
			return
		}
		p := &peer{
			t:        t,
			conn:     wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader}),
			nc:       nc,
			protocol: r.Header.Get("Sec-WebSocket-Protocol"),
		}
		handler(p)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// events records Session events.
type events struct {
	mu   sync.Mutex
	list []Event
}

func (e *events) add(ev Event) {
	e.mu.Lock()
	e.list = append(e.list, ev)
	e.mu.Unlock()
}

// summary is "sent:connection_init received:connection_ack ...".
func (e *events) summary() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var parts []string
	for _, ev := range e.list {
		s := string(ev.Direction) + ":" + string(ev.Type)
		if ev.ID != "" {
			s += "#" + ev.ID
		}
		if ev.Note != "" {
			s += "(" + string(ev.Note) + ")"
		}
		if ev.Err != nil {
			s += "(err)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func (e *events) all() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.list...)
}

func options(srv *httptest.Server, ev *events) Options {
	return Options{
		WS: wsclient.Options{
			URL:    "ws" + strings.TrimPrefix(srv.URL, "http") + "/graphql",
			Dialer: netx.NewDirect(netx.Options{}),
		},
		OnEvent: ev.add,
	}
}

func dial(t *testing.T, o Options) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	s, err := Dial(ctx, o)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() {
		s.Client().Abort()
		<-s.Done()
	})
	return s
}

func subscribe(t *testing.T, s *Session, query string) *Subscription {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	sub, err := s.Subscribe(ctx, SubscribeRequest{Query: query})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return sub
}

// next returns the next Result, failing if C is closed.
func next(t *testing.T, sub *Subscription) Result {
	t.Helper()
	select {
	case r, ok := <-sub.C():
		if !ok {
			t.Fatalf("subscription %s: channel closed", sub.ID())
		}
		return r
	case <-time.After(waitLimit):
		t.Fatalf("subscription %s: no result", sub.ID())
	}
	return Result{}
}

// expectClosedC checks that C is closed without further results.
func expectClosedC(t *testing.T, sub *Subscription) {
	t.Helper()
	select {
	case r, ok := <-sub.C():
		if ok {
			t.Fatalf("subscription %s: unexpected result %+v", sub.ID(), r)
		}
	case <-time.After(waitLimit):
		t.Fatalf("subscription %s: channel not closed", sub.ID())
	}
}

func waitDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(waitLimit):
		t.Fatal("the session did not end")
	}
}

func errCode(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func detail(err error, key string) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Details[key]
	}
	return ""
}

// waitEvents waits until the event summary ends with suffix.
func waitEvents(t *testing.T, ev *events, suffix string) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !strings.HasSuffix(ev.summary(), suffix) {
		if time.Now().After(deadline) {
			t.Fatalf("events %s: no %s", ev.summary(), suffix)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
