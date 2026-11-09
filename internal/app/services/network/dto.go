package network

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netlog"
)

// ConnDTO is one outgoing connection of the connection log (PRV-06). It
// names the target by host:port only, never a URL (docs/privacy.md).
type ConnDTO struct {
	// ID numbers the connections over the app run, from 1.
	ID uint64 `json:"id"`
	// OpKind and OpID are the user action that caused it ("http.send" and
	// the operation ID, "tcp.connect" and the connection ID, ...); "" when
	// no user action did, which the privacy guard refuses.
	OpKind string `json:"opKind"`
	OpID   string `json:"opId"`
	// Network is "tcp" (or "udp", "unix"); Address the target host:port
	// as the protocol client asked for it.
	Network string `json:"network"`
	Address string `json:"address"`
	// LocalAddr and RemoteAddr are the connection's addresses once open;
	// through a proxy, RemoteAddr is the proxy's.
	LocalAddr  string `json:"localAddr"`
	RemoteAddr string `json:"remoteAddr"`
	// Via is the proxy the connection went through ("socks5h://proxy:1080",
	// "http://proxy:3128", never with credentials), "" for a direct one
	// (NET-06).
	Via string `json:"via"`
	// State is "connecting", "open", "closed", "failed" or "refused".
	State string `json:"state"`
	// StartedAt, ConnectedAt and EndedAt are RFC 3339 in UTC; "" when the
	// connection has not got there.
	StartedAt   string `json:"startedAt"`
	ConnectedAt string `json:"connectedAt"`
	EndedAt     string `json:"endedAt"`
	// ConnectMs is how long the dial took (DNS and connect) when it
	// succeeded; DurationMs how long from the dial's start to its end, 0
	// while active.
	ConnectMs  float64 `json:"connectMs"`
	DurationMs float64 `json:"durationMs"`
	// BytesIn and BytesOut are read and written on the wire (TLS included).
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
	// ErrorCode and Error say why a failed or refused dial did not connect.
	ErrorCode string `json:"errorCode"`
	Error     string `json:"error"`
}

// ListDTO is what List returns.
type ListDTO struct {
	// Conns are the kept connections, oldest first.
	Conns []ConnDTO `json:"conns"`
	// Active is how many connections are connecting or open now.
	Active int `json:"active"`
	// Capacity is how many connections the log keeps.
	Capacity int `json:"capacity"`
}

func toConnDTO(c netlog.Conn) ConnDTO {
	d := ConnDTO{
		ID:          c.ID,
		OpKind:      c.Op.Kind,
		OpID:        c.Op.ID,
		Network:     c.Network,
		Address:     c.Address,
		LocalAddr:   c.LocalAddr,
		RemoteAddr:  c.RemoteAddr,
		Via:         c.Via,
		State:       string(c.State),
		StartedAt:   timeString(c.Started),
		ConnectedAt: timeString(c.Connected),
		EndedAt:     timeString(c.Ended),
		BytesIn:     c.BytesIn,
		BytesOut:    c.BytesOut,
		ErrorCode:   c.ErrCode,
		Error:       c.Err,
	}
	if !c.Connected.IsZero() {
		d.ConnectMs = millis(c.Connected.Sub(c.Started))
	}
	if !c.Ended.IsZero() {
		d.DurationMs = millis(c.Ended.Sub(c.Started))
	}
	return d
}

func toConnDTOs(conns []netlog.Conn) []ConnDTO {
	out := make([]ConnDTO, len(conns))
	for i, c := range conns {
		out[i] = toConnDTO(c)
	}
	return out
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// millis is d in milliseconds, to the microsecond.
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
