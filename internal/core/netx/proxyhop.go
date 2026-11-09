package netx

import (
	"strconv"
	"strings"
	"time"
)

// ProxyHop describes how one dial went through a proxy (SOCKS-12): which
// proxy, which protocol version, the authentication and DNS mode, the
// proxy's answer and where the time went. Proxy dialers report it in
// DialDoneInfo.Proxy, on success and on failure (as far as the dial got),
// and the proxy connection test returns it (ProxyRouter.Test). It never
// holds a credential.
type ProxyHop struct {
	// Scheme is the proxy's scheme as in its Via: socks5, socks5h,
	// socks4, socks4a, http or https.
	Scheme string
	// Version is the protocol spoken with the proxy: "SOCKS5", "SOCKS4",
	// "SOCKS4a" or "HTTP/1.1".
	Version string
	// Proxy is the proxy's host:port as configured; ProxyAddr the
	// address the connection to it went to (ip:port), "" when that
	// connection was not opened.
	Proxy     string
	ProxyAddr string
	// Target is the address the dial asked for.
	Target string
	// Auth is the authentication used with the proxy: AuthNone,
	// AuthUserPass (RFC 1929), AuthUserID (a SOCKS4 user ID) or AuthBasic
	// (HTTP);
	// "" when the dial failed before it was chosen.
	Auth string
	// RemoteDNS is set when a target host name goes to the proxy
	// unresolved (socks5h, socks4a, HTTP proxies); otherwise it is
	// resolved here first (Lookup).
	RemoteDNS bool
	// Replied is set when the proxy answered the connect request; Reply is
	// then its code (the SOCKS reply code, or the HTTP status of a CONNECT)
	// and ReplyText its meaning ("succeeded", "200 Connection
	// established"). A connection for an absolute-form HTTP request
	// (Forward) sends no CONNECT and has no reply.
	Replied   bool
	Reply     int
	ReplyText string
	// Forward is set for a connection to an HTTP proxy that carries
	// absolute-form requests instead of a tunnel (HTTPProxy.DialForward).
	Forward bool
	// Command is CommandUDPAssociate for a UDP dial through a SOCKS5
	// proxy (SOCKS-07), "" for a CONNECT. RelayAddr is then the relay's
	// host:port the datagrams go to, "" until the proxy named it.
	Command   string
	RelayAddr string
	// Lookup is the local resolution of the target's host name (socks5,
	// socks4 with a name); Connect the connection to the proxy (the
	// forward dialer); TLS the TLS handshake with an https:// proxy;
	// Handshake the proxy protocol: greeting, authentication and CONNECT
	// for SOCKS, the CONNECT exchange for HTTP. Zero when the step did not
	// happen.
	Lookup    time.Duration
	Connect   time.Duration
	TLS       time.Duration
	Handshake time.Duration
}

// Authentication methods of a ProxyHop.
const (
	AuthNone     = "none"
	AuthUserPass = "username/password"
	AuthUserID   = "user ID"
	AuthBasic    = "basic"
)

// Setup is the time spent on the proxy after the connection to it was
// open: its TLS handshake and the proxy protocol. It is the "proxy" phase
// of a timeline, between the connect and the target's TLS handshake.
func (h ProxyHop) Setup() time.Duration { return h.TLS + h.Handshake }

// DNSMode is "remote" when the proxy resolves target host names, "local"
// when they are resolved here.
func (h ProxyHop) DNSMode() string {
	if h.RemoteDNS {
		return "remote"
	}
	return "local"
}

// String describes the hop in one line, in English, for the timeline:
//
//	SOCKS5 proxy.lan:1080 (10.0.0.1:1080), auth username/password,
//	remote DNS, reply 0 succeeded, connect 1.2ms, handshake 800µs
func (h ProxyHop) String() string {
	var b strings.Builder
	b.WriteString(h.Version + " " + h.Proxy)
	if h.ProxyAddr != "" && h.ProxyAddr != h.Proxy {
		b.WriteString(" (" + h.ProxyAddr + ")")
	}
	if h.Forward {
		b.WriteString(", absolute-form requests")
	}
	if h.Command != "" {
		b.WriteString(", " + h.Command)
	}
	if h.RelayAddr != "" {
		b.WriteString(", relay " + h.RelayAddr)
	}
	if h.Auth != "" {
		b.WriteString(", auth " + h.Auth)
	}
	b.WriteString(", " + h.DNSMode() + " DNS")
	if h.Replied {
		b.WriteString(", reply " + strconv.Itoa(h.Reply))
		if h.ReplyText != "" && h.ReplyText != strconv.Itoa(h.Reply) {
			b.WriteString(" " + strings.TrimPrefix(h.ReplyText, strconv.Itoa(h.Reply)+" "))
		}
	}
	for _, d := range []struct {
		name string
		d    time.Duration
	}{{"lookup", h.Lookup}, {"connect", h.Connect}, {"tls", h.TLS}, {"handshake", h.Handshake}} {
		if d.d > 0 {
			b.WriteString(", " + d.name + " " + roundDuration(d.d).String())
		}
	}
	return b.String()
}

// roundDuration rounds d for display: to the microsecond below 10 ms, to
// 10 µs above.
func roundDuration(d time.Duration) time.Duration {
	if d < 10*time.Millisecond {
		return d.Round(time.Microsecond)
	}
	return d.Round(10 * time.Microsecond)
}

// socks5ReplyText is the meaning of a SOCKS5 reply code (RFC 1928
// section 6); "" for an unknown one.
func socks5ReplyText(rep byte) string {
	switch rep {
	case SOCKSReplySucceeded:
		return "succeeded"
	case SOCKSReplyGeneralFailure:
		return "general failure"
	case SOCKSReplyNotAllowed:
		return "not allowed by ruleset"
	case SOCKSReplyNetworkUnreachable:
		return "network unreachable"
	case SOCKSReplyHostUnreachable:
		return "host unreachable"
	case SOCKSReplyConnectionRefused:
		return "connection refused"
	case SOCKSReplyTTLExpired:
		return "TTL expired"
	case SOCKSReplyCommandNotSupported:
		return "command not supported"
	case SOCKSReplyAddressTypeNotSupported:
		return "address type not supported"
	}
	return ""
}
