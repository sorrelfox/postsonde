// Package netlog keeps the connection log (PRV-06): a record of every
// outgoing connection the app's dialer opens or refuses, with the user
// action that caused it, so users can check that PostSonde connects only
// where they asked.
//
// A Log plugs into the shared dialer as a netx.Middleware (Log.Middleware),
// outside the privacy guard so it also records the dials the guard refuses.
// Each dial becomes a Conn: the operation that caused it (netx.UserOp from
// the dial's context), the network and target host:port as the protocol
// client asked for it, the local and remote addresses once connected, when
// it started, connected and ended, the bytes read and written, and how it
// ended. A dial through a proxy (netx.SOCKS5) records the target, the
// proxy it went through (Conn.Via) and the proxy as the remote address.
// A connection that dialed successfully is wrapped so its bytes and its
// Close are counted.
//
// The log is bounded (Options.Size, oldest first out) and in memory only.
// It holds no URLs, paths, headers or payloads: only host:port and IP
// addresses, which the user typed or which the host resolved to.
//
// The package imports only the standard library and this module, like all
// of internal/core.
package netlog

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// DefaultSize is how many connections a Log keeps by default.
const DefaultSize = 500

// State is where a connection is in its life.
type State string

// The states of a Conn. Connecting and Open count as active.
const (
	// Connecting: the dial has started (DNS lookup, connect) and not ended.
	Connecting State = "connecting"
	// Open: the dial succeeded and the connection is not closed yet.
	Open State = "open"
	// Closed: the connection was open and has been closed.
	Closed State = "closed"
	// Failed: the dial failed (DNS, refused, timeout, cancelled).
	Failed State = "failed"
	// Refused: a guard refused the dial; nothing reached the network.
	Refused State = "refused"
)

// Active reports whether s is Connecting or Open.
func (s State) Active() bool { return s == Connecting || s == Open }

// Conn is one outgoing connection as the log shows it: a copy, safe to keep.
type Conn struct {
	// ID numbers the connections of a Log from 1, over its lifetime (Clear
	// does not reset it).
	ID uint64
	// Op is the user action that caused the dial (netx.WithUserOp); zero
	// when the dial's context carried none (the guard refuses those).
	Op netx.UserOp
	// Network and Address are as passed to DialContext: "tcp" and the
	// target host:port (a host name or an IP address).
	Network string
	Address string
	// LocalAddr and RemoteAddr are the connection's addresses once it is
	// open, "" before and when the dial failed. Through a proxy, RemoteAddr
	// is the proxy's.
	LocalAddr  string
	RemoteAddr string
	// Via names the proxy the connection goes through
	// ("socks5h://proxy:1080", no credentials; netx.ViaOf), or the proxy a
	// failed dial went through (the error's "via" detail); "" when direct.
	Via   string
	State State
	// Started is when the dial started; Connected when it succeeded (zero
	// otherwise); Ended when it failed or was refused, or when an open
	// connection was closed (zero while active).
	Started   time.Time
	Connected time.Time
	Ended     time.Time
	// BytesIn and BytesOut are the bytes read from and written to the
	// connection, on the wire (TLS records included).
	BytesIn  int64
	BytesOut int64
	// ErrCode and Err are the dial error's code (netx.dial_timeout,
	// netx.not_user_initiated, ...) and message, for Failed and Refused.
	ErrCode string
	Err     string
}

// Options configure New. The zero value gives the defaults.
type Options struct {
	// Size is how many connections are kept; the oldest go first. Zero or
	// less means DefaultSize.
	Size int
	// Now is the clock; nil means time.Now. Tests inject a fake.
	Now func() time.Time
}

// Log records the connections dialed through its Middleware. It is safe
// for concurrent use.
type Log struct {
	now  func() time.Time
	size int

	mu      sync.Mutex
	conns   []*record // oldest first, at most size
	nextID  uint64
	active  int
	nextSub int
	subs    map[int]func(Conn)
}

// record is a Conn being tracked. Its fields are guarded by Log.mu, except
// the byte counters.
type record struct {
	c        Conn
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
}

// New returns an empty Log.
func New(o Options) *Log {
	if o.Size <= 0 {
		o.Size = DefaultSize
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Log{now: o.Now, size: o.Size, subs: map[int]func(Conn){}}
}

// Capacity is how many connections the log keeps.
func (l *Log) Capacity() int { return l.size }

// Middleware returns the netx.Middleware that records every dial through
// it. Put it outermost, before the guard, so refused dials are recorded
// too:
//
//	netx.Chain(netx.NewDirect(o), log.Middleware(), netx.Guarded(guard))
//
// A nil Log returns a nil Middleware, which netx.Chain skips.
func (l *Log) Middleware() netx.Middleware {
	if l == nil {
		return nil
	}
	return func(next netx.Dialer) netx.Dialer {
		return netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			op, _ := netx.UserOpFrom(ctx)
			r := l.start(op, network, address)
			conn, err := next.DialContext(ctx, network, address)
			if err != nil {
				l.fail(r, err)
				return nil, err
			}
			l.open(r, conn)
			return wrap(conn, l, r), nil
		})
	}
}

// Subscribe calls fn with a copy of a connection whenever it is recorded
// or changes state (not on every byte). fn runs on the dialing or closing
// goroutine, outside the log's lock; it must be cheap and must not block.
// It returns the function that unsubscribes.
func (l *Log) Subscribe(fn func(Conn)) (unsubscribe func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := l.nextSub
	l.nextSub++
	l.subs[id] = fn
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.subs, id)
	}
}

// Conns returns the kept connections, oldest first.
func (l *Log) Conns() []Conn {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Conn, len(l.conns))
	for i, r := range l.conns {
		out[i] = r.snapshot()
	}
	return out
}

// Get returns the kept connection with the given ID.
func (l *Log) Get(id uint64) (Conn, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.conns {
		if r.c.ID == id {
			return r.snapshot(), true
		}
	}
	return Conn{}, false
}

// ActiveConns returns the connections that are connecting or open, oldest
// first. The size limit drops ended connections first, so active ones are
// missing only when more than Capacity are active at once; Active still
// counts them.
func (l *Log) ActiveConns() []Conn {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Conn
	for _, r := range l.conns {
		if r.c.State.Active() {
			out = append(out, r.snapshot())
		}
	}
	return out
}

// Active is the number of connections connecting or open right now.
func (l *Log) Active() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// Clear drops the connections that have ended. Active ones stay: they are
// still open, and the log must show every open connection.
func (l *Log) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.conns[:0]
	for _, r := range l.conns {
		if r.c.State.Active() {
			kept = append(kept, r)
		}
	}
	clear(l.conns[len(kept):])
	l.conns = kept
}

func (r *record) snapshot() Conn {
	c := r.c
	c.BytesIn = r.bytesIn.Load()
	c.BytesOut = r.bytesOut.Load()
	return c
}

// start records a dial that is starting.
func (l *Log) start(op netx.UserOp, network, address string) *record {
	l.mu.Lock()
	l.nextID++
	r := &record{c: Conn{
		ID:      l.nextID,
		Op:      op,
		Network: network,
		Address: address,
		State:   Connecting,
		Started: l.now(),
	}}
	for len(l.conns) >= l.size {
		l.evictLocked()
	}
	l.conns = append(l.conns, r)
	l.active++
	l.notifyLocked(r)
	return r
}

// evictLocked drops the oldest connection that has ended, so every
// active one stays listed; only when all are active does the oldest go.
func (l *Log) evictLocked() {
	i := slices.IndexFunc(l.conns, func(r *record) bool { return !r.c.State.Active() })
	if i < 0 {
		i = 0
	}
	l.conns = slices.Delete(l.conns, i, i+1)
}

// fail records a dial that failed or was refused.
func (l *Log) fail(r *record, err error) {
	l.mu.Lock()
	r.c.Ended = l.now()
	r.c.State = Failed
	if e, ok := errors.AsType[*errs.Error](err); ok {
		r.c.ErrCode = e.Code
		r.c.Err = e.Message
		r.c.Via = e.Details["via"]
		if e.Kind == errs.PermissionDenied {
			r.c.State = Refused
		}
	} else {
		r.c.Err = err.Error()
	}
	l.active--
	l.notifyLocked(r)
}

// open records a dial that succeeded.
func (l *Log) open(r *record, conn net.Conn) {
	l.mu.Lock()
	r.c.Connected = l.now()
	r.c.State = Open
	r.c.LocalAddr = addrString(conn.LocalAddr())
	r.c.RemoteAddr = addrString(conn.RemoteAddr())
	r.c.Via = netx.ViaOf(conn)
	l.notifyLocked(r)
}

// closed records that an open connection was closed.
func (l *Log) closed(r *record) {
	l.mu.Lock()
	r.c.Ended = l.now()
	r.c.State = Closed
	l.active--
	l.notifyLocked(r)
}

// notifyLocked unlocks l.mu, then calls the subscribers with r's state.
func (l *Log) notifyLocked(r *record) {
	c := r.snapshot()
	subs := make([]func(Conn), 0, len(l.subs))
	for _, fn := range l.subs {
		subs = append(subs, fn)
	}
	l.mu.Unlock()
	for _, fn := range subs {
		fn(c)
	}
}

func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}
