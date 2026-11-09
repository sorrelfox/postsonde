package websocket

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// events records what the service emits.
type events struct {
	mu       sync.Mutex
	names    []string
	states   []StateEventDTO
	messages []MessageEventDTO
	seqs     map[string][]int64
	wake     chan struct{}
}

func newEvents() *events { return &events{seqs: map[string][]int64{}, wake: make(chan struct{}, 1)} }

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	e.names = append(e.names, name)
	switch d := data.(type) {
	case StateEventDTO:
		if name != EventState {
			panic(name)
		}
		e.states = append(e.states, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	case MessageEventDTO:
		if name != EventMessage || len(d.Messages) == 0 {
			panic(name)
		}
		e.messages = append(e.messages, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	default:
		panic(name)
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// waitFor waits until cond holds for the recorded events.
func (e *events) waitFor(t *testing.T, what string, cond func(e *events) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e.mu.Lock()
		ok := cond(e)
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-e.wake:
		case <-deadline:
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Fatalf("timed out waiting for %s; states %+v, messages %+v", what, e.states, e.messages)
		}
	}
}

// statesOf lists the states of connection id; e.mu must be held.
func (e *events) statesOf(id string) string {
	var s []string
	for _, ev := range e.states {
		if ev.ConnID == id {
			s = append(s, ev.State)
		}
	}
	return strings.Join(s, ",")
}

// log lists the messages of connection id as "dir opcode text"; e.mu must
// be held.
func (e *events) log(id string) []string {
	var out []string
	for _, ev := range e.messages {
		if ev.ConnID != id {
			continue
		}
		for _, m := range ev.Messages {
			content := m.Text
			if m.Opcode == "binary" {
				b, _ := base64.StdEncoding.DecodeString(m.Data)
				content = string(b)
			}
			out = append(out, m.Direction+" "+m.Opcode+" "+content)
		}
	}
	return out
}

func (e *events) state(id, state string) *StateEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.states {
		if e.states[i].ConnID == id && e.states[i].State == state {
			ev := e.states[i]
			return &ev
		}
	}
	return nil
}

// seen is what the test server saw of one connection.
type seen struct {
	header    http.Header
	query     string
	closeCode wsframe.StatusCode
	closeText string
}

// echoServer upgrades every request with wsframe in the server role,
// selects the first offered subprotocol, sets a cookie, and echoes
// messages: binary messages as they are, text with "echo " before it;
// "binary" is answered with a binary message, "close-me" with
// a close frame 4001 "server bye". What it saw is sent on the channel when
// the connection ends.
func echoServer(t *testing.T) (*httptest.Server, chan seen) {
	t.Helper()
	ch := make(chan seen, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nc, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = nc.Close() }()
		extra := "Set-Cookie: seen=1; Path=/\r\n"
		if offered := r.Header.Get("Sec-WebSocket-Protocol"); offered != "" {
			first, _, _ := strings.Cut(offered, ",")
			extra += "Sec-WebSocket-Protocol: " + strings.TrimSpace(first) + "\r\n"
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n" + extra + "\r\n"
		if _, err := nc.Write([]byte(resp)); err != nil {
			return
		}
		conn := wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader})
		s := seen{header: r.Header.Clone(), query: r.URL.RawQuery}
		defer func() { ch <- s }()
		for {
			op, data, err := conn.ReadMessage()
			if err != nil {
				if ce, ok := errors.AsType[*wsframe.CloseError](err); ok {
					s.closeCode, s.closeText = ce.Code, ce.Reason
				}
				return
			}
			if op == wsframe.OpBinary {
				// Binary messages come back byte for byte.
				_ = conn.WriteMessage(op, data)
				continue
			}
			switch string(data) {
			case "binary":
				_ = conn.WriteMessage(wsframe.OpBinary, []byte{0x00, 0xff, 'b'})
			case "close-me":
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = conn.Close(ctx, 4001, "server bye")
				cancel()
				return
			default:
				_ = conn.WriteMessage(op, append([]byte("echo "), data...))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func recvSeen(t *testing.T, ch chan seen) seen {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not see the connection end")
	}
	return seen{}
}

// registryJars is a JarSource with the shared jar of one local directory.
type registryJars struct {
	registry *cookies.Registry
	dir      string
}

func (r registryJars) Jar() (*cookies.Jar, error) {
	s, err := r.registry.Open(r.dir)
	if err != nil {
		return nil, err
	}
	return s.Jar(), nil
}

func (r registryJars) CloseAll() error { return r.registry.CloseAll() }

// fakeJars hands out one in-memory jar.
type fakeJars struct {
	jar    *cookies.Jar
	mu     sync.Mutex
	closed int
}

func (f *fakeJars) Jar() (httpclient.CookieJar, error) { return f.jar, nil }
func (f *fakeJars) CloseAll() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func newService(t *testing.T, jars CookieJars, opts ...Option) (*WebSocketService, *events) {
	t.Helper()
	ev := newEvents()
	opts = append([]Option{withInterval(5 * time.Millisecond),
		withClientOptions(func(o *wsclient.Options) { o.CloseLinger = 50 * time.Millisecond })}, opts...)
	s := New(netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), jars, ev.emit, nil, opts...)
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, ev
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code {
		t.Fatalf("error = %v, want %s %s", err, kind, code)
	}
}

func TestConnectSendAndClose(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	jar := cookies.New(cookies.Options{})
	u, _ := url.Parse(srv.URL)
	jar.SetCookieLines(u, []string{"session=abc; Path=/"})
	jars := &fakeJars{jar: jar}
	s, ev := newService(t, jars)
	ctx := t.Context()

	conn, err := s.Connect(ctx, ConnectInputDTO{
		RequestID: "req_feed",
		URL:       wsURL(srv, "/feed?room=lobby"),
		Headers: []HeaderDTO{
			{Name: "X-Custom", Value: "yes", Enabled: true},
			{Name: "X-Disabled", Value: "no", Enabled: false},
		},
		Subprotocols: []string{"chat.v2", "chat.v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conn.ConnID, "ws_") || conn.RequestID != "req_feed" || !strings.HasSuffix(conn.URL, "/feed?room=lobby") {
		t.Fatalf("connection = %+v", conn)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	open := ev.state(id, "open")
	hs := open.Handshake
	if hs == nil || hs.Subprotocol != "chat.v2" || hs.Response == nil || hs.Response.StatusCode != 101 || hs.RemoteAddr == "" {
		t.Fatalf("handshake = %+v", hs)
	}

	want := []string{
		"sent text hello",
		"received text echo hello",
		`sent text {"type":"subscribe","id":1}`,
		`received text echo {"type":"subscribe","id":1}`,
		"sent text binary",
		"received binary \x00\xffb",
	}
	for i, text := range []string{"hello", `{"type":"subscribe","id":1}`, "binary"} {
		res, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: text})
		if err != nil || res.Bytes != len(text) {
			t.Fatalf("Send(%q) = %+v, %v", text, res, err)
		}
		ev.waitFor(t, "the reply to "+text, func(e *events) bool { return len(e.log(id)) >= 2*(i+1) })
	}
	ev.mu.Lock()
	got := ev.log(id)
	ev.mu.Unlock()
	// Each reply comes after the message it answers.
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("log =\n%q\nwant\n%q", got, want)
	}

	if err := s.Close(ctx, CloseInputDTO{ConnID: id, Code: 4000, Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	closed := ev.state(id, "closed")
	if closed == nil || closed.Error != nil || closed.Handshake != nil || closed.Close == nil {
		t.Fatalf("closed = %+v", closed)
	}
	if c := closed.Close; c.InitiatedBy != "local" || !c.WasClean || c.Code != 4000 ||
		c.Sent == nil || c.Sent.Code != 4000 || c.Sent.Reason != "done" || c.MessagesSent != 3 || c.MessagesReceived != 3 || c.BytesSent != int64(len("hello")+len(`{"type":"subscribe","id":1}`)+len("binary")) {
		t.Errorf("close = %+v", c)
	}

	// What the server saw: the header row, not the disabled one, the jar's
	// cookie, the offered subprotocols and the close code.
	srvSeen := recvSeen(t, seenCh)
	if srvSeen.header.Get("X-Custom") != "yes" || srvSeen.header.Get("X-Disabled") != "" ||
		srvSeen.header.Get("Cookie") != "session=abc" || srvSeen.header.Get("Sec-WebSocket-Protocol") != "chat.v2, chat.v1" {
		t.Errorf("server saw headers %v", srvSeen.header)
	}
	if srvSeen.closeCode != 4000 || srvSeen.closeText != "done" {
		t.Errorf("server saw close %d %q", srvSeen.closeCode, srvSeen.closeText)
	}
	// The cookie the 101 set is in the jar.
	names := map[string]bool{}
	for _, c := range jar.All() {
		names[c.Name] = true
	}
	if !names["seen"] {
		t.Errorf("jar = %+v", jar.All())
	}

	ev.mu.Lock()
	if got := ev.statesOf(id); got != "connecting,open,closing,closed" {
		t.Errorf("states = %s", got)
	}
	for i, seq := range ev.seqs[id] {
		if seq != int64(i+1) {
			t.Errorf("seqs = %v", ev.seqs[id])
			break
		}
	}
	ev.mu.Unlock()

	// Gone: sending fails, closing again does nothing.
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "x"})
	checkCode(t, err, errs.NotFound, CodeNotFound)
	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Error(err)
	}
}

func TestServerCloseAndCookiesOff(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	jar := cookies.New(cookies.Options{})
	u, _ := url.Parse(srv.URL)
	jar.SetCookieLines(u, []string{"session=abc; Path=/"})
	s, ev := newService(t, &fakeJars{jar: jar})
	ctx := t.Context()

	off := false
	conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/"), SendCookies: &off})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	if hs := ev.state(id, "open").Handshake; hs.Subprotocol != "" {
		t.Errorf("subprotocol = %q", hs.Subprotocol)
	}
	if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: "close-me"}); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(id), "closed") })
	closed := ev.state(id, "closed")
	if c := closed.Close; closed.Error != nil || c.InitiatedBy != "remote" || c.Code != 4001 || c.Reason != "server bye" || !c.WasClean ||
		c.Received == nil || c.Received.Code != 4001 {
		t.Errorf("closed = %+v, close %+v", closed, closed.Close)
	}
	srvSeen := recvSeen(t, seenCh)
	if srvSeen.header.Get("Cookie") != "" {
		t.Errorf("cookies sent with sendCookies false: %q", srvSeen.header.Get("Cookie"))
	}
	if len(jar.All()) != 1 {
		t.Errorf("a cookie was stored with sendCookies false: %+v", jar.All())
	}
}

func TestFailures(t *testing.T) {
	t.Parallel()
	s, ev := newService(t, nil)
	ctx := t.Context()

	t.Run("rejected upgrade keeps the response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Why", "no")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("forbidden"))
		}))
		t.Cleanup(srv.Close)
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/")})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
		closed := ev.state(conn.ConnID, "closed")
		if closed.Error == nil || closed.Error.Code != wsclient.CodeUpgradeRejected || closed.Close.InitiatedBy != "none" || closed.Close.Code != 1006 {
			t.Fatalf("closed = %+v", closed)
		}
		if r := closed.Handshake.Response; r == nil || r.StatusCode != 403 || r.Body != "forbidden" {
			t.Errorf("response = %+v", r)
		}
	})

	t.Run("refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: "ws://" + addr + "/"})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "closed", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
		if closed := ev.state(conn.ConnID, "closed"); closed.Error == nil || closed.Error.Code != netx.CodeConnectionRefused {
			t.Errorf("closed = %+v", closed)
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		_, err := s.Connect(ctx, ConnectInputDTO{URL: "http://localhost/"})
		checkCode(t, err, errs.InvalidArgument, wsclient.CodeSchemeUnsupported)
		_, err = s.Connect(ctx, ConnectInputDTO{URL: "ws://localhost/", Headers: []HeaderDTO{{Name: "Upgrade", Value: "x", Enabled: true}}})
		checkCode(t, err, errs.InvalidArgument, wsclient.CodeReservedHeader)
		_, err = s.Connect(ctx, ConnectInputDTO{URL: "ws://localhost/", Subprotocols: []string{"a b"}})
		checkCode(t, err, errs.InvalidArgument, wsclient.CodeInvalidSubprotocol)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Connect(cancelled, ConnectInputDTO{URL: "ws://localhost/"}); !errors.Is(err, context.Canceled) {
			t.Errorf("Connect with a cancelled context = %v", err)
		}
		// Invalid close codes and reasons.
		checkCode(t, s.Close(ctx, CloseInputDTO{ConnID: "ws_x", Code: 1006}), errs.InvalidArgument, wsframe.CodeInvalidArgument)
		checkCode(t, s.Close(ctx, CloseInputDTO{ConnID: "ws_x", Code: 70000}), errs.InvalidArgument, wsframe.CodeInvalidArgument)
		checkCode(t, s.Close(ctx, CloseInputDTO{ConnID: "ws_x", Reason: strings.Repeat("x", 124)}), errs.InvalidArgument, wsframe.CodeInvalidArgument)
	})
}

// TestVariables: {{variables}} in the URL, enabled header rows,
// subprotocols and messages are rendered with the request's scopes, read
// again on every connect and send; undefined names of every field come
// back in one template.unresolved error unless allowUnresolved (WS-11,
// ENV-11).
func TestVariables(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	var mu sync.Mutex
	vars := map[string]string{
		"base":  strings.TrimPrefix(wsURL(srv, ""), "ws://"),
		"room":  "lobby",
		"token": "t1",
		"proto": "chat.v1",
		"loop":  "{{loop}}",
	}
	var asked []string
	scopes := func(_ context.Context, id string) (template.Chain, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, id)
		if id == "req_gone" {
			return nil, errs.New(errs.NotFound, "The request is no longer in the project.")
		}
		return template.Chain{template.Vars("Environment: dev", vars)}, nil
	}
	s, ev := newService(t, nil, WithScopes(scopes))
	ctx := t.Context()

	in := ConnectInputDTO{
		RequestID: "req_feed",
		URL:       "ws://{{base}}/feed?room={{room}}",
		Headers: []HeaderDTO{
			{Name: "X-Token", Value: "{{token}}", Enabled: true},
			{Name: "X-Off", Value: "{{nope}}"},
		},
		Subprotocols: []string{"{{proto}}"},
	}
	conn, err := s.Connect(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(conn.URL, "/feed?room=lobby") {
		t.Errorf("url = %q", conn.URL)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })

	// The message is rendered with the variables as they are at send time.
	mu.Lock()
	vars["room"] = "kitchen"
	mu.Unlock()
	if res, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: `{"join":"{{room}}"}`}); err != nil || res.Bytes != len(`{"join":"kitchen"}`) {
		t.Fatalf("Send = %+v, %v", res, err)
	}
	ev.waitFor(t, "the echo", func(e *events) bool { return len(e.log(id)) >= 2 })

	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "{{a}} {{room}} {{b}}"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || e.Details["names"] != "a,b" {
		t.Fatalf("Send with undefined names = %v", err)
	}
	if _, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: "{{a}}!", AllowUnresolved: true}); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "the echo", func(e *events) bool { return len(e.log(id)) >= 4 })
	// Other problems refuse, also with AllowUnresolved.
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "{{loop}}", AllowUnresolved: true})
	checkCode(t, err, errs.InvalidArgument, template.CodeCycle)

	ev.mu.Lock()
	got := ev.log(id)
	ev.mu.Unlock()
	want := []string{`sent text {"join":"kitchen"}`, `received text echo {"join":"kitchen"}`, "sent text {{a}}!", "received text echo {{a}}!"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("log =\n%q\nwant\n%q", got, want)
	}
	// Dynamic values are generated, not undefined names.
	if res, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: "{{$uuid}}"}); err != nil || res.Bytes != 36 {
		t.Errorf("Send $uuid = %+v, %v", res, err)
	}
	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	srvSeen := recvSeen(t, seenCh)
	if srvSeen.header.Get("X-Token") != "t1" || srvSeen.header.Get("X-Off") != "" || srvSeen.header.Get("Sec-WebSocket-Protocol") != "chat.v1" {
		t.Errorf("server saw headers %v", srvSeen.header)
	}

	// Undefined names of the URL, an enabled row and a subprotocol, in
	// one error; a disabled row does not count.
	_, err = s.Connect(ctx, ConnectInputDTO{
		RequestID:    "req_feed",
		URL:          "ws://{{host}}/",
		Headers:      []HeaderDTO{{Name: "X-A", Value: "{{token2}}", Enabled: true}, {Name: "X-B", Value: "{{off}}"}},
		Subprotocols: []string{"{{sub}}"},
	})
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || e.Details["names"] != "host,token2,sub" {
		t.Fatalf("Connect with undefined names = %v", err)
	}
	// Connect anyway: the URL keeps the braces, which wsclient refuses.
	_, err = s.Connect(ctx, ConnectInputDTO{RequestID: "req_feed", URL: "ws://{{host}}/", AllowUnresolved: true})
	if errs.KindOf(err) != errs.InvalidArgument || errors.As(err, &e) && e.Code == template.CodeUnresolved {
		t.Errorf("Connect anyway = %v", err)
	}
	// The scopes cannot be loaded.
	_, err = s.Connect(ctx, ConnectInputDTO{RequestID: "req_gone", URL: "ws://localhost/"})
	if errs.KindOf(err) != errs.NotFound {
		t.Errorf("Connect for a gone request = %v", err)
	}
	// No request ID: nothing is defined.
	_, err = s.Connect(ctx, ConnectInputDTO{URL: "ws://{{base}}/"})
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved {
		t.Errorf("Connect without a request = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(asked, "") {
		t.Errorf("scopes asked for %q", asked)
	}
}

// blockingDialer blocks until the dial context ends.
type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeoutAndCloseWhileConnecting(t *testing.T) {
	t.Parallel()
	ev := newEvents()
	s := New(blockingDialer{}, nil, ev.emit, nil, withInterval(5*time.Millisecond))
	ctx := t.Context()

	short := int64(30)
	conn, err := s.Connect(ctx, ConnectInputDTO{URL: "ws://example.invalid/", ConnectTimeoutMs: &short})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "timeout", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,closed" })
	if closed := ev.state(conn.ConnID, "closed"); closed.Error == nil || errs.Kind(closed.Error.Kind) != errs.Timeout {
		t.Errorf("closed = %+v, error %+v", closed, closed.Error)
	}

	none := int64(0)
	conn, err = s.Connect(ctx, ConnectInputDTO{URL: "ws://example.invalid/", ConnectTimeoutMs: &none})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "connecting", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting" })
	_, err = s.Send(ctx, SendInputDTO{ConnID: conn.ConnID, Text: "early"})
	checkCode(t, err, errs.InvalidArgument, CodeNotOpen)
	if err := s.Close(ctx, CloseInputDTO{ConnID: conn.ConnID}); err != nil {
		t.Fatal(err)
	}
	closed := ev.state(conn.ConnID, "closed")
	if closed == nil || closed.Error == nil || errs.Kind(closed.Error.Kind) != errs.Cancelled || closed.Close.InitiatedBy != "local" {
		t.Errorf("closed = %+v", closed)
	}
}

func TestCloseAllAndWorkspaceChange(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	localDir := t.TempDir()
	registry := cookies.NewRegistry(cookies.Options{})
	s, ev := newService(t, WorkspaceJars(registryJars{registry, localDir}))
	ctx := t.Context()
	var ids []string
	for range 3 {
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/")})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, conn.ConnID)
	}
	for _, id := range ids {
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
	}
	OnWorkspaceChanged(s)
	for _, id := range ids {
		if closed := ev.state(id, "closed"); closed == nil || closed.Close.InitiatedBy != "local" || closed.Close.Code != 1001 {
			t.Errorf("%s closed = %+v", id, closed)
		}
	}
	for range ids {
		if got := recvSeen(t, seenCh); got.closeCode != wsframe.CloseGoingAway {
			t.Errorf("server saw close %d", got.closeCode)
		}
	}
	if n, err := s.CloseAll(ctx); n != 0 || err != nil {
		t.Errorf("CloseAll = %d, %v", n, err)
	}
	// The cookie the server set was saved in the project's local directory.
	data, err := os.ReadFile(filepath.Join(localDir, "cookies.json"))
	if err != nil || !strings.Contains(string(data), `"seen"`) {
		t.Errorf("cookies.json = %s, %v", data, err)
	}
}

func TestDTOs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 16, 10, 0, 0, 123_000_000, time.UTC)
	c := &connection{id: "ws_0123456789abcdef", requestID: "req_feed", url: "ws://localhost:8080/feed"}
	var got []any
	c.emit = func(_ string, data any) { got = append(got, data) }
	hs := &wsclient.Handshake{
		URL:            "ws://localhost:8080/feed",
		RequestHeaders: []httpclient.Header{{Name: "Host", Value: "localhost:8080", Enabled: true}, {Name: "Sec-WebSocket-Protocol", Value: "chat", Enabled: true}},
		Response: &wsclient.Response{
			Proto: "HTTP/1.1", StatusCode: 101, Status: "101 Switching Protocols",
			Headers: []httpclient.Header{{Name: "Upgrade", Value: "websocket", Enabled: true}},
			Body:    []byte{},
		},
		Subprotocol: "chat",
		Timing:      httpclient.Timing{Total: 12 * time.Millisecond},
		Conn:        httpclient.Conn{LocalAddr: "127.0.0.1:50000", RemoteAddr: "127.0.0.1:8080"},
	}
	c.flush([]item{
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, URL: c.url, Attempt: 1, State: "open", At: formatTime(at), Handshake: toHandshakeDTO(hs)}},
		{message: new(toMessageDTO("sent", wsframe.OpText, []byte(`{"a":1}`), at))},
		{message: new(toMessageDTO("received", wsframe.OpBinary, []byte{0, 0xff}, at))},
		{message: new(toPingPongDTO(wsclient.PingPong{Direction: wsframe.Sent, Opcode: wsframe.OpPing, Payload: []byte("hi"), At: at}))},
		{message: new(toPingPongDTO(wsclient.PingPong{Direction: wsframe.Received, Opcode: wsframe.OpPong, Payload: []byte("hi"), At: at, Answered: true, RTT: 1500 * time.Microsecond}))},
		{message: new(toPingPongDTO(wsclient.PingPong{Direction: wsframe.Sent, Opcode: wsframe.OpPing, Payload: []byte{}, At: at, Keepalive: true}))},
		{message: new(toPingPongDTO(wsclient.PingPong{Direction: wsframe.Received, Opcode: wsframe.OpPing, Payload: []byte{0xff}, At: at}))},
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, URL: c.url, Attempt: 1, State: "closed", At: formatTime(at.Add(time.Second)), Close: &CloseInfoDTO{
			Code: 1000, Reason: "bye", WasClean: true, InitiatedBy: "local",
			Sent: &CloseFrameDTO{Code: 1000, Reason: "bye"}, Received: &CloseFrameDTO{Code: 1000, Reason: "bye"},
			MessagesSent: 1, MessagesReceived: 1, BytesSent: 7, BytesReceived: 2, DurationMs: 1000,
		}}},
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, URL: c.url, Attempt: 2, State: "closed", At: formatTime(at.Add(2 * time.Second)), Close: &CloseInfoDTO{
			Code: 1006, InitiatedBy: "none", MessagesSent: 1, MessagesReceived: 1, BytesSent: 7, BytesReceived: 2, DurationMs: 500,
		}, Reconnect: &ReconnectDTO{Attempt: 1, MaxAttempts: 5, DelayMs: 1000}}},
	})
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/events", append(data, '\n'))
	if s := toValidUTF8([]byte("a\xffb")); s != "a�b" {
		t.Errorf("toValidUTF8 = %q", s)
	}
}

// TestSendBinary sends binary messages typed as hex and Base64 (WS-04)
// through the echo server: every byte comes back as sent, {{variables}}
// are not rendered, and text that spells no bytes refuses before sending.
func TestSendBinary(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	s, ev := newService(t, nil, WithScopes(func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.Vars("Globals", map[string]string{"x": "y"})}, nil
	}))
	ctx := t.Context()
	conn, err := s.Connect(ctx, ConnectInputDTO{RequestID: "req_bin", URL: wsURL(srv, "/")})
	if err != nil {
		t.Fatal(err)
	}
	id := conn.ConnID
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })

	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	cases := []struct {
		format, text string
		want         []byte
	}{
		{FormatHex, `DE AD be:ef, 0x00ff \x7f`, []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0xff, 0x7f}},
		{FormatBase64, base64.StdEncoding.EncodeToString(all), all},
		{FormatBase64, "3q2-7w", []byte{0xde, 0xad, 0xbe, 0xef}},
		// No variables in binary messages: the braces are bytes.
		{FormatHex, "7b 7b 78 7d 7d", []byte("{{x}}")},
	}
	for i, tc := range cases {
		res, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: tc.text, Format: tc.format})
		if err != nil || res.Bytes != len(tc.want) {
			t.Fatalf("Send(%s %q) = %+v, %v", tc.format, tc.text, res, err)
		}
		ev.waitFor(t, "the echo", func(e *events) bool { return len(e.log(id)) >= 2*(i+1) })
	}
	ev.mu.Lock()
	got := ev.log(id)
	ev.mu.Unlock()
	for i, tc := range cases {
		want := "binary " + string(tc.want)
		if got[2*i] != "sent "+want || got[2*i+1] != "received "+want {
			t.Errorf("case %d: log %q, %q; want %q", i, got[2*i], got[2*i+1], want)
		}
	}

	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "de ad b", Format: FormatHex})
	checkCode(t, err, errs.InvalidArgument, "hexbytes.odd_digits")
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "3q2+{{x}}", Format: FormatBase64})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidBase64)
	_, err = s.Send(ctx, SendInputDTO{ConnID: id, Text: "00", Format: "bytes"})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidFormat)
	// JSON is sent as text, its variables rendered.
	if res, err := s.Send(ctx, SendInputDTO{ConnID: id, Text: `"{{x}}"`, Format: FormatJSON}); err != nil || res.Bytes != 3 {
		t.Fatalf("Send json = %+v, %v", res, err)
	}
	ev.waitFor(t, "the echo", func(e *events) bool { return len(e.log(id)) >= 2*len(cases)+2 })

	if err := s.Close(ctx, CloseInputDTO{ConnID: id}); err != nil {
		t.Fatal(err)
	}
	closed := ev.state(id, "closed")
	if c := closed.Close; c == nil || c.MessagesSent != int64(len(cases)+1) || c.BytesReceived != c.BytesSent+int64(len("echo ")) {
		t.Errorf("close = %+v", c)
	}
	recvSeen(t, seenCh)
}
