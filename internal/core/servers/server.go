package servers

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"net"
	"net/netip"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// State is where a server is in its life cycle:
//
//	stopped ──Start──▶ starting ──▶ running ──Stop──▶ stopping ──▶ stopped
//	                      │            │
//	                      └────────────┴──(listen or serve error)──▶ failed
//
// A failed server can be started again, like a stopped one.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

// Active reports whether the server holds (or is about to hold) a socket.
func (s State) Active() bool {
	return s == StateStarting || s == StateRunning || s == StateStopping
}

// Spec defines a server: what it is and where it listens.
type Spec struct {
	// Type names a registered Type ("echo_http").
	Type string
	// Name is a label for the user; empty gets a default from the type.
	Name string
	// Host, Port and AllowLAN are passed to listen.Listen: Host "" or
	// "localhost" is loopback on IPv4 and IPv6, Port 0 lets the OS pick a
	// port at every start, and a non-loopback Host needs AllowLAN.
	Host     string
	Port     int
	AllowLAN bool
	// Config is the type's own configuration (the HTTP mock's routes); nil
	// for a type without one. A ConfigurableType checks and normalizes it
	// in Add and UpdateConfig. Treat it as immutable: replace it with
	// UpdateConfig instead of changing it.
	Config any
	// TLS makes the server serve TLS (SRV-05); nil serves plain text.
	// Like the address, it can change only while the server does not hold
	// a socket.
	TLS *TLS
	// Owner says where the server is defined: the zero Owner for a server
	// made in the Servers view, a project folder for a saved server config
	// (SRV-04). The Manager only keeps it.
	Owner Owner
}

// Owner is the project folder a saved server config belongs to (SRV-04).
type Owner struct {
	// Workspace and Folder are the IDs of the project and of the folder
	// whose _folder.json holds the definition.
	Workspace string
	Folder    string
	// Label is the folder's name when the server was last defined, for a
	// server whose project is no longer open.
	Label string
}

// IsZero reports whether o is no owner: an ad-hoc server.
func (o Owner) IsZero() bool { return o.Workspace == "" && o.Folder == "" }

// Type is a kind of server. Implementations must be safe for concurrent
// use: one Type starts every server of its kind.
type Type interface {
	// Name is the stable identifier used in Spec.Type.
	Name() string
	// Label is a short name for the user ("Echo HTTP").
	Label() string
	// Start serves on ln, which it now owns, and returns without blocking.
	// Everything the server sees goes to sink. An error means nothing is
	// running; Start must close ln then.
	Start(spec Spec, ln net.Listener, sink Sink) (Instance, error)
}

// PacketType is a Type that serves datagrams (UDP) instead of a stream
// listener. The Manager opens its sockets with listen.ListenPackets (one
// per loopback family for localhost) and calls StartPacket instead of
// Start. It cannot serve TLS: Check refuses Spec.TLS
// (servers.tls_not_supported).
type PacketType interface {
	Type
	// StartPacket serves on conns, which it now owns, and returns without
	// blocking. An error means nothing is running; it must close conns
	// then.
	StartPacket(spec Spec, conns []net.PacketConn, sink Sink) (Instance, error)
}

// ConfigurableType is a Type with a configuration (Spec.Config).
type ConfigurableType interface {
	Type
	// NormalizeConfig checks config and returns the value to keep: nil
	// gets the default configuration. An error rejects the configuration
	// as a whole; problems the server can live with (a route it skips) are
	// for the type to report, not errors.
	NormalizeConfig(config any) (any, error)
}

// Reconfigurable is an Instance that takes a new configuration while it
// runs. Requests in progress finish with the configuration they started
// with.
type Reconfigurable interface {
	Reconfigure(config any)
}

// Instance is one run of a server, returned by Type.Start.
type Instance interface {
	// Done is closed when the instance stopped serving on its own (the
	// listener broke) or after Stop.
	Done() <-chan struct{}
	// Err is why serving ended on its own, or nil. Valid once Done is
	// closed.
	Err() error
	// Stop stops accepting, lets work in progress finish until ctx is done,
	// then closes every connection. It returns once nothing is left
	// running and emits nothing after that. It is safe to call more than
	// once and after the instance ended on its own.
	Stop(ctx context.Context) error
}

// Server is a snapshot of one server.
type Server struct {
	ID   string
	Spec Spec
	// TypeLabel is the Type's Label.
	TypeLabel string
	State     State
	// Addrs are the bound addresses while starting succeeded (running,
	// stopping), with the actual port; nil otherwise.
	Addrs []netip.AddrPort
	// Scope and Warning come from listen.Info: Warning is set when the
	// server can be reached from other devices.
	Scope   listen.Scope
	Warning *listen.Warning
	// StartedAt is when the server last started running (for uptime),
	// StoppedAt when it last stopped or failed. Zero when it never did.
	StartedAt time.Time
	StoppedAt time.Time
	// Err is why the last start or run failed; nil unless State is failed.
	Err error
	// Events is the number of traffic events recorded since the server was
	// added (including cleared ones).
	Events uint64
	// Cert is the certificate a TLS server presents while it runs (the
	// leaf); nil otherwise.
	Cert *x509.Certificate
}

// Port returns the bound port, or 0 when not bound.
func (s Server) Port() int {
	if len(s.Addrs) == 0 {
		return 0
	}
	return int(s.Addrs[0].Port())
}

// IDPrefix starts every server ID.
const IDPrefix = "srv_"

// newID returns "srv_" and 16 hex digits from crypto/rand.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return IDPrefix + hex.EncodeToString(b[:])
}
