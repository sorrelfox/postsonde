// Package udpserver is the UDP server type (SSRV-06): it receives
// datagrams, logs each one (peer, size, content), answers with an echo
// and/or a fixed reply, keeps the recent peers and sends a datagram to one
// of them from the app. It is a servers.PacketType: the Manager opens its
// sockets (one per loopback family for localhost). See docs/servers.md.
package udpserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
)

// TypeName is the Spec.Type of the UDP server.
const TypeName = "udp"

// Defaults of Options.
const (
	// DefaultWriteTimeout bounds one datagram write.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultMaxPeers is how many recent peers are kept; the one heard
	// from longest ago is dropped first.
	DefaultMaxPeers = 100
)

// Reply kinds of servers.UDPInfo.Reply.
const (
	ReplyEcho  = "echo"
	ReplyFixed = "fixed"
)

// Error codes of Send.
const (
	CodePeerNotFound = "udpserver.peer_not_found"
	CodeTooLarge     = "udpserver.too_large"
	CodeSendFailed   = "udpserver.send_failed"
)

// Options change limits, for tests.
type Options struct {
	// WriteTimeout: 0 means DefaultWriteTimeout.
	WriteTimeout time.Duration
	// MaxPeers: 0 means DefaultMaxPeers.
	MaxPeers int
}

// Type is the UDP server type.
type Type struct {
	opts Options
}

// New returns the UDP server type.
func New(opts Options) Type {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.MaxPeers <= 0 {
		opts.MaxPeers = DefaultMaxPeers
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "UDP" }

// NormalizeConfig takes a Config, a *Config or nil (DefaultConfig).
func (Type) NormalizeConfig(config any) (any, error) {
	switch c := config.(type) {
	case nil:
		return DefaultConfig(), nil
	case Config:
		return Normalize(c)
	case *Config:
		if c == nil {
			return DefaultConfig(), nil
		}
		return Normalize(*c)
	}
	return nil, errs.New(errs.Internal, fmt.Sprintf("A UDP server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start refuses a stream listener: the Manager calls StartPacket.
func (Type) Start(_ servers.Spec, ln net.Listener, _ servers.Sink) (servers.Instance, error) {
	_ = ln.Close()
	return nil, errs.New(errs.Internal, "A UDP server needs a datagram socket.")
}

// socketBuffer is the send and receive buffer of the server's sockets.
const socketBuffer = 256 << 10

// StartPacket receives datagrams on conns.
func (t Type) StartPacket(spec servers.Spec, conns []net.PacketConn, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		for _, c := range conns {
			_ = c.Close()
		}
		return nil, err
	}
	inst := &Instance{conns: conns, sink: sink, opts: t.opts, peers: map[string]*peer{}, done: make(chan struct{})}
	inst.Reconfigure(cfg)
	inst.readers.Add(len(conns))
	for _, c := range conns {
		if b, ok := c.(interface {
			SetReadBuffer(int) error
			SetWriteBuffer(int) error
		}); ok {
			// macOS limits a datagram to the send buffer, 9 KiB by
			// default; replies up to MaxDatagram need more.
			_ = b.SetWriteBuffer(socketBuffer)
			_ = b.SetReadBuffer(socketBuffer)
		}
		go inst.read(c)
	}
	go func() {
		inst.readers.Wait()
		close(inst.done)
	}()
	return inst, nil
}

// Peer is a snapshot of a peer the server heard from.
type Peer struct {
	// Addr is the peer's address ("127.0.0.1:53122"), what Send takes.
	Addr string
	// Local is the server's address the peer sent to.
	Local     string
	FirstSeen time.Time
	LastSeen  time.Time
	// Datagrams and bytes received from and sent to the peer.
	DatagramsIn  uint64
	DatagramsOut uint64
	BytesIn      uint64
	BytesOut     uint64
}

// Hub is the running UDP server's peer operations, implemented by
// *Instance.
type Hub interface {
	Peers() []Peer
	Send(peer string, data []byte) error
}

var _ Hub = (*Instance)(nil)
var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of a UDP server. It implements servers.Instance,
// servers.Reconfigurable and Hub.
type Instance struct {
	conns []net.PacketConn
	sink  servers.Sink
	opts  Options
	live  atomic.Pointer[live]

	readers sync.WaitGroup
	done    chan struct{} // closed once every reader ended
	errOnce sync.Once
	err     error // why reading ended on its own; set before done closes

	mu       sync.Mutex
	peers    map[string]*peer
	stopping bool
	sends    sync.WaitGroup // Send calls, added under mu while not stopping

	stopOnce sync.Once
}

// live is the configuration in use and its reply bytes.
type live struct {
	cfg   Config
	reply []byte
}

type peer struct {
	addr  net.Addr
	conn  net.PacketConn
	local string
	first time.Time

	// Guarded by Instance.mu.
	last                                         time.Time
	datagramsIn, datagramsOut, bytesIn, bytesOut uint64
}

func (i *Instance) Done() <-chan struct{} { return i.done }
func (i *Instance) Err() error            { return i.err }

// Reconfigure applies config (a normalized Config) to the next datagram.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	reply, err := c.replyBytes()
	if err != nil {
		reply = nil // Normalize checked it; never here
	}
	i.live.Store(&live{cfg: c, reply: reply})
}

// Stop closes the sockets and returns once every reader and send ended.
// Datagrams have no work in progress to wait for, so ctx is not needed.
func (i *Instance) Stop(context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		i.mu.Unlock()
		for _, c := range i.conns {
			_ = c.Close()
		}
		<-i.done
		i.sends.Wait()
	})
	return nil
}

// Peers returns the recent peers, the most recently heard from first.
func (i *Instance) Peers() []Peer {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]Peer, 0, len(i.peers))
	for _, p := range i.peers {
		out = append(out, p.snapshot())
	}
	slices.SortFunc(out, func(a, b Peer) int {
		if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
			return c
		}
		return cmp.Compare(a.Addr, b.Addr)
	})
	return out
}

func (p *peer) snapshot() Peer {
	return Peer{
		Addr: p.addr.String(), Local: p.local, FirstSeen: p.first, LastSeen: p.last,
		DatagramsIn: p.datagramsIn, DatagramsOut: p.datagramsOut, BytesIn: p.bytesIn, BytesOut: p.bytesOut,
	}
}

// Send sends one datagram to a recent peer (Peers), from the socket the
// peer last sent to. The datagram is logged before it is written.
func (i *Instance) Send(peerAddr string, data []byte) error {
	if len(data) > MaxDatagram {
		return errs.New(errs.InvalidArgument, fmt.Sprintf("The datagram is %d bytes; UDP carries at most %d.", len(data), MaxDatagram)).
			WithCode(CodeTooLarge)
	}
	i.mu.Lock()
	p, ok := i.peers[peerAddr]
	if !ok || i.stopping {
		i.mu.Unlock()
		return errs.New(errs.NotFound, "PostSonde has not heard from this peer recently.").
			WithCode(CodePeerNotFound).
			WithHint("A UDP server can send only to a peer that sent it a datagram.").
			WithDetail("peer", peerAddr)
	}
	i.sends.Add(1)
	i.mu.Unlock()
	defer i.sends.Done()
	return i.write(p, data, "")
}

// write sends data to p; how is "" (from the app), ReplyEcho or ReplyFixed.
func (i *Instance) write(p *peer, data []byte, how string) error {
	i.mu.Lock()
	conn, local := p.conn, p.local
	i.mu.Unlock()
	summary := fmt.Sprintf("%s to %s", byteCount(len(data)), p.addr)
	if how != "" {
		summary += " (" + how + ")"
	}
	i.sink.Emit(servers.Event{
		Peer: p.addr.String(), Direction: servers.Out, Kind: servers.KindMessage, Summary: summary,
		Content: data, UDP: &servers.UDPInfo{Local: local, Reply: how},
	})
	_, err := writeTo(conn, data, p.addr, i.opts.WriteTimeout)
	if err != nil {
		i.sink.Emit(servers.Event{
			Peer: p.addr.String(), Direction: servers.System, Kind: servers.KindError,
			Summary: fmt.Sprintf("The datagram could not be sent to %s: %v", p.addr, err),
			UDP:     &servers.UDPInfo{Local: local, Reply: how},
		})
		return errs.Wrap(err, errs.KindOf(err), fmt.Sprintf("The datagram could not be sent to %s.", p.addr)).WithCode(CodeSendFailed)
	}
	i.mu.Lock()
	p.datagramsOut++
	p.bytesOut += uint64(len(data))
	i.mu.Unlock()
	return nil
}

// read receives datagrams on c until it is closed.
func (i *Instance) read(c net.PacketConn) {
	defer i.readers.Done()
	local := c.LocalAddr().String()
	buf := make([]byte, 64<<10)
	for {
		n, addr, err := c.ReadFrom(buf)
		if err != nil {
			i.mu.Lock()
			stopping := i.stopping
			i.mu.Unlock()
			switch {
			case stopping || errors.Is(err, net.ErrClosed):
				return
			case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED):
				// An earlier datagram to a closed port came back as ICMP
				// "port unreachable" (Windows reports it on the next read).
				i.sink.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError,
					Summary: "A reply could not be delivered: " + err.Error(), UDP: &servers.UDPInfo{Local: local}})
				continue
			}
			i.errOnce.Do(func() { i.err = err })
			for _, other := range i.conns {
				_ = other.Close() // one broken socket ends the run
			}
			return
		}
		data := buf[:n]
		p := i.heard(addr, c, local, n)
		i.sink.Emit(servers.Event{
			Peer: addr.String(), Direction: servers.In, Kind: servers.KindMessage,
			Summary: fmt.Sprintf("%s from %s", byteCount(n), addr),
			Content: slices.Clone(data), UDP: &servers.UDPInfo{Local: local},
		})
		l := i.live.Load()
		if l.cfg.Echo {
			_ = i.write(p, slices.Clone(data), ReplyEcho)
		}
		if l.cfg.FixedReply {
			_ = i.write(p, l.reply, ReplyFixed)
		}
	}
}

// heard records a datagram of n bytes from addr on c and returns its peer.
func (i *Instance) heard(addr net.Addr, c net.PacketConn, local string, n int) *peer {
	now := time.Now()
	key := addr.String()
	i.mu.Lock()
	defer i.mu.Unlock()
	p, ok := i.peers[key]
	if !ok {
		if len(i.peers) >= i.opts.MaxPeers {
			var oldest *peer
			for _, q := range i.peers {
				if oldest == nil || q.last.Before(oldest.last) {
					oldest = q
				}
			}
			delete(i.peers, oldest.addr.String())
		}
		p = &peer{addr: addr, first: now}
		i.peers[key] = p
	}
	p.conn, p.local, p.last = c, local, now
	p.datagramsIn++
	p.bytesIn += uint64(n)
	return p
}

// writeTo writes one datagram. The deadline is not reset afterwards: every
// write sets its own, and reads never have one.
func writeTo(c net.PacketConn, data []byte, addr net.Addr, timeout time.Duration) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(timeout))
	return c.WriteTo(data, addr)
}

func byteCount(n int) string {
	if n == 1 {
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", n)
}
