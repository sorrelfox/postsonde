package wsclient

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// hijack takes over the connection of an HTTP handler.
func hijack(t *testing.T, w http.ResponseWriter) (net.Conn, *bufio.Reader) {
	t.Helper()
	nc, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		panic(http.ErrAbortHandler)
	}
	return nc, brw.Reader
}

// upgrade answers a WebSocket upgrade with 101 plus extra header lines
// ("Name: value\r\n"), followed by after (raw bytes sent in the same
// write), and returns the server side Conn.
func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request, extra string, after []byte) (*wsframe.Conn, net.Conn) {
	t.Helper()
	nc, br := hijack(t, w)
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n" + extra + "\r\n"
	if _, err := nc.Write(append([]byte(resp), after...)); err != nil {
		t.Errorf("write 101: %v", err)
	}
	return wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: br}), nc
}

// newServer starts an http or https test server.
func newServer(t *testing.T, secure bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	if secure {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

// wsURL is the ws:// or wss:// URL of srv plus path.
func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

// states records state changes and messages.
type states struct {
	mu       sync.Mutex
	changes  []StateChange
	messages chan Message
}

func (s *states) names() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n []string
	for _, c := range s.changes {
		n = append(n, string(c.State))
	}
	return strings.Join(n, ",")
}

// options returns Options for srv that record into a new states.
func options(srv *httptest.Server, path string) (Options, *states) {
	st := &states{messages: make(chan Message, 1000)}
	o := Options{
		URL:    wsURL(srv, path),
		Dialer: netx.NewDirect(netx.Options{}),
		OnState: func(c StateChange) {
			st.mu.Lock()
			st.changes = append(st.changes, c)
			st.mu.Unlock()
		},
		OnMessage: func(m Message) { st.messages <- m },
	}
	if srv.TLS != nil {
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		o.RootCAs = pool
	}
	return o, st
}

func connect(t *testing.T, o Options) *Client {
	t.Helper()
	c, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Abort)
	return c
}

func waitDone(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not reach closed")
	}
}

func recvMessage(t *testing.T, st *states) Message {
	t.Helper()
	select {
	case m := <-st.messages:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	return Message{}
}

func errCode(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// echo reads messages and answers "echo:<msg>" until the connection ends,
// then closes the transport and reports the read error.
func echo(sc *wsframe.Conn, nc net.Conn, result chan<- error) {
	defer nc.Close()
	for {
		op, msg, err := sc.ReadMessage()
		if err != nil {
			result <- err
			return
		}
		if err := sc.WriteMessage(op, append([]byte("echo:"), msg...)); err != nil {
			result <- err
			return
		}
	}
}
