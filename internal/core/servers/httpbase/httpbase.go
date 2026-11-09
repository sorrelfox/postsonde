// Package httpbase holds what the HTTP server types share: an http.Server
// serving the Manager's listener, connection IDs and connection events
// through ConnState, a graceful stop that logs the connections still open,
// and helpers for request and response events. The echo server and the
// HTTP mock are built on it. See docs/servers.md.
package httpbase

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/servers"
)

// ReadHeaderTimeout drops clients that never finish sending headers.
const ReadHeaderTimeout = 30 * time.Second

// Instance is one run of an HTTP server type. It implements
// servers.Instance.
type Instance struct {
	srv  *http.Server
	sink servers.Sink
	done chan struct{}
	err  error // set before done is closed

	mu       sync.Mutex
	conns    map[net.Conn]uint64 // open connections and their IDs
	nextConn uint64
	stopped  bool

	stopOnce sync.Once
	stopErr  error
}

// New returns an instance that emits to sink. Call Serve to start it.
func New(sink servers.Sink) *Instance {
	return &Instance{
		sink:  sink,
		conns: map[net.Conn]uint64{},
		done:  make(chan struct{}),
	}
}

// Serve serves HTTP/1.1 with h on ln, which the instance now owns, and
// returns without blocking.
func (i *Instance) Serve(ln net.Listener, h http.Handler) {
	i.ServeProtocols(ln, h, nil)
}

// ServeProtocols is Serve with the protocols the server speaks: nil is
// HTTP/1.1 (and HTTP/2 over TLS when the listener negotiates h2, which
// the HTTP types do not offer). The gRPC mock adds HTTP/2 with prior
// knowledge (h2c) on plaintext listeners.
func (i *Instance) ServeProtocols(ln net.Listener, h http.Handler, p *http.Protocols) {
	i.srv = &http.Server{
		Protocols: p,
		Handler:   h,
		ConnState: i.connState,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
		ReadHeaderTimeout: ReadHeaderTimeout,
		// Protocol errors from clients are not app errors; keep them out of
		// the process log. A failed TLS handshake goes to the traffic.
		ErrorLog: log.New(errorLog{i}, "", 0),
	}
	go func() {
		err := i.srv.Serve(ln)
		if !errors.Is(err, http.ErrServerClosed) {
			i.err = err
		}
		close(i.done)
	}()
}

func (i *Instance) Done() <-chan struct{} { return i.done }
func (i *Instance) Err() error            { return i.err }

// Stop shuts the HTTP server down gracefully until ctx is done, then closes
// every connection. Connections still open are logged as closed before it
// returns.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		err := i.srv.Shutdown(ctx)
		if err != nil {
			_ = i.srv.Close()
		}
		<-i.done
		i.mu.Lock()
		i.stopped = true
		left := make([]net.Conn, 0, len(i.conns))
		for c := range i.conns {
			left = append(left, c)
		}
		slices.SortFunc(left, func(a, b net.Conn) int { return cmp.Compare(i.conns[a], i.conns[b]) })
		for _, c := range left {
			i.sink.Emit(closeEvent(c, i.conns[c], "Connection closed (server stopped)"))
			delete(i.conns, c)
		}
		i.mu.Unlock()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			err = nil // forced close is the expected end of a timed-out graceful stop
		}
		i.stopErr = err
	})
	return i.stopErr
}

func (i *Instance) connState(c net.Conn, state http.ConnState) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped {
		return
	}
	switch state {
	case http.StateNew:
		i.nextConn++
		i.conns[c] = i.nextConn
		i.sink.Emit(servers.Event{
			Peer:      c.RemoteAddr().String(),
			ConnID:    i.nextConn,
			Direction: servers.System,
			Kind:      servers.KindConnectionOpen,
			Summary:   "Connection opened",
		})
	case http.StateClosed:
		id, ok := i.conns[c]
		if !ok {
			return
		}
		delete(i.conns, c)
		i.sink.Emit(closeEvent(c, id, "Connection closed"))
	case http.StateHijacked:
		// The handler that took the connection over (the WebSocket server)
		// owns it from now on and logs how it ends; it must take the
		// connection ID (ConnID) before hijacking.
		delete(i.conns, c)
	}
}

func closeEvent(c net.Conn, id uint64, summary string) servers.Event {
	return servers.Event{
		Peer:      c.RemoteAddr().String(),
		ConnID:    id,
		Direction: servers.System,
		Kind:      servers.KindConnectionClose,
		Summary:   summary,
	}
}

// ALPN is the application protocol the HTTP types offer over TLS
// (SRV-05): HTTP/1.1 only, which the WebSocket upgrade needs too.
func ALPN() []string { return []string{"http/1.1"} }

// errorLog takes the http.Server's error log: a failed TLS handshake
// becomes an error event of its connection, everything else is dropped.
type errorLog struct{ i *Instance }

const handshakePrefix = "http: TLS handshake error from "

func (w errorLog) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	rest, ok := strings.CutPrefix(line, handshakePrefix)
	if !ok {
		return len(p), nil
	}
	peer, msg, _ := strings.Cut(rest, ": ")
	w.i.handshakeFailed(peer, msg)
	return len(p), nil
}

// handshakeFailed emits the error event of a failed TLS handshake from
// peer. The connection is still open when the server logs it.
func (i *Instance) handshakeFailed(peer, msg string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped {
		return
	}
	var id uint64
	for c, cid := range i.conns {
		if c.RemoteAddr().String() == peer {
			id = cid
			break
		}
	}
	i.sink.Emit(servers.Event{
		Peer:      peer,
		ConnID:    id,
		Direction: servers.System,
		Kind:      servers.KindError,
		Summary:   servers.HandshakeSummary(msg),
	})
}

// ConnID returns the ID of the connection r arrived on, or 0.
func (i *Instance) ConnID(r *http.Request) uint64 {
	c, ok := r.Context().Value(connKey{}).(net.Conn)
	if !ok {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.conns[c]
}

type connKey struct{}

// Emit sends ev unless the instance has stopped.
func (i *Instance) Emit(ev servers.Event) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.stopped {
		i.sink.Emit(ev)
	}
}

// ContentLimit is the sink's content limit.
func (i *Instance) ContentLimit() int { return i.sink.ContentLimit() }

// ReadRequest reads r's body up to the content limit, emits the request
// event (with route, when not nil) and, when reading failed, an error
// event. It returns the event's peer and connection ID for the response.
func (i *Instance) ReadRequest(r *http.Request, route *servers.RouteMatch) (peer string, connID uint64) {
	peer, connID, _ = i.ReadRequestLimit(r, route, 0)
	return peer, connID
}

// ReadRequestLimit is ReadRequest that reads up to limit bytes of the body
// when that is more than the content limit, and returns what it read. The
// event keeps the content limit's worth.
func (i *Instance) ReadRequestLimit(r *http.Request, route *servers.RouteMatch, limit int) (peer string, connID uint64, body []byte) {
	shownLimit := i.sink.ContentLimit()
	body, size, readErr := ReadCapped(r.Body, max(limit, shownLimit))
	shown := body
	if len(body) > shownLimit {
		// The event must not hold on to the whole body.
		shown = slices.Clone(body[:shownLimit])
	}
	peer = r.RemoteAddr
	connID = i.ConnID(r)
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.In,
		Kind:      servers.KindRequest,
		Summary:   r.Method + " " + r.RequestURI,
		Content:   shown,
		Size:      int(size),
		Truncated: size > int64(len(shown)),
		HTTP: &servers.HTTPInfo{
			Method:  r.Method,
			Target:  r.RequestURI,
			Proto:   r.Proto,
			Headers: RequestHeaders(r),
			Route:   route,
		},
	})
	if readErr != nil {
		i.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "Reading the request body failed: " + readErr.Error(),
		})
	}
	return peer, connID, body
}

// ReadCapped reads up to limit bytes of r and discards the rest, returning
// what it kept and the full size.
func ReadCapped(r io.Reader, limit int) ([]byte, int64, error) {
	if r == nil {
		return nil, 0, nil
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)))
	size := int64(len(body))
	if err != nil {
		return body, size, err
	}
	rest, err := io.Copy(io.Discard, r)
	return body, size + rest, err
}

// RequestHeaders lists the request's headers with Host first (net/http
// moves it out of Header), then the others by name.
func RequestHeaders(r *http.Request) []servers.Header {
	out := make([]servers.Header, 0, len(r.Header)+1)
	if r.Host != "" {
		out = append(out, servers.Header{Name: "Host", Value: r.Host})
	}
	return append(out, HeaderList(r.Header)...)
}

// HeaderList lists h sorted by name (case-insensitively), repeated names in
// their order.
func HeaderList(h http.Header) []servers.Header {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	out := make([]servers.Header, 0, len(names))
	for _, name := range names {
		for _, v := range h[name] {
			out = append(out, servers.Header{Name: name, Value: v})
		}
	}
	return out
}
