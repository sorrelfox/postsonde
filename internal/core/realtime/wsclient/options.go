package wsclient

import (
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Defaults for zero Options fields.
const (
	// DefaultHandshakeTimeout bounds dialing, the TLS handshake and the
	// upgrade exchange together.
	DefaultHandshakeTimeout = 30 * time.Second
	// DefaultCloseLinger is how long the client waits for the server to
	// close the TCP connection after the close handshake (RFC 6455 §7.1.1:
	// the server closes first) before closing it itself.
	DefaultCloseLinger = 1 * time.Second
	// DefaultMaxResponseBody caps the body kept from a rejected upgrade.
	DefaultMaxResponseBody = 1 << 20
	// maxResponseHeader caps the status line and headers of the upgrade
	// response.
	maxResponseHeader = 64 << 10
)

// Options configure a Client.
type Options struct {
	// URL is the ws:// or wss:// URL (scheme in any case). The path and
	// query are sent as typed (only bytes that cannot appear in a request
	// line are percent-encoded); a fragment is an error. Userinfo becomes
	// "Authorization: Basic" unless an Authorization row is enabled.
	URL string
	// Headers are extra handshake headers, sent after the generated ones
	// in row order, as spelled, duplicates kept; rows that are not Enabled
	// are skipped. A Host row replaces the generated Host. The headers the
	// handshake needs (Upgrade, Connection, Sec-WebSocket-Key,
	// Sec-WebSocket-Version, Sec-WebSocket-Protocol,
	// Sec-WebSocket-Extensions) and the body headers (Content-Length,
	// Transfer-Encoding) are rejected unless AllowHandshakeHeaders is set.
	// There is no Origin header unless a row adds one.
	Headers []httpclient.Header
	// AllowHandshakeHeaders lets rows replace the generated handshake
	// headers, to test how a server handles a broken handshake. A replaced
	// Sec-WebSocket-Key is still checked against Sec-WebSocket-Accept.
	AllowHandshakeHeaders bool
	// Subprotocols are offered in Sec-WebSocket-Protocol, in order.
	Subprotocols []string

	// Dialer opens the TCP connection. Required.
	Dialer netx.Dialer
	// RootCAs are the certificate authorities trusted for wss URLs; nil
	// means the system's.
	RootCAs *x509.CertPool
	// InsecureSkipVerify turns off certificate verification. Off by
	// default, as in httpclient.
	InsecureSkipVerify bool
	// ClientCertificate is the client certificate (mTLS) a wss
	// connection sends when the server asks (AUTH-30); nil sends none.
	// Sending it adds a client_cert event to the handshake timeline.
	ClientCertificate *httpclient.ClientCert
	// Jar, if set, supplies cookies for the handshake and stores the
	// Set-Cookie headers of the upgrade response (101 or not). A cookie
	// whose name a Cookie row already sends is left out.
	Jar httpclient.CookieJar

	// HandshakeTimeout bounds dialing, TLS and the upgrade exchange. Zero
	// means DefaultHandshakeTimeout; negative means no limit besides the
	// context of Connect.
	HandshakeTimeout time.Duration
	// CloseLinger is how long to wait for the server to close the TCP
	// connection after the close handshake. Zero means DefaultCloseLinger;
	// negative closes at once.
	CloseLinger time.Duration
	// MaxResponseBody caps the body kept from a response that is not 101.
	// Zero means DefaultMaxResponseBody; negative keeps none.
	MaxResponseBody int64

	// Limits, MaxMessageSize and DisableAutoPong are passed to wsframe.
	Limits          wsframe.Limits
	MaxMessageSize  int64
	DisableAutoPong bool

	// OnState is called for every state change, in order (see Client).
	// It runs synchronously on the goroutine making the change and must
	// not call Connect, Close or Abort.
	OnState func(StateChange)
	// OnMessage is called on the read goroutine for every text or binary
	// message. The data is owned by the callee. Reading waits while it
	// runs, so a slow handler slows the connection down, not memory.
	OnMessage func(Message)
	// OnFrame is called for every frame read and written (wsframe.Options).
	OnFrame func(dir wsframe.Direction, f wsframe.Frame)
	// OnPingPong is called for every ping and pong sent and received
	// (WS-09): pings this client sends just before they are written, on
	// the writing goroutine; the rest on the read goroutine (or, for an
	// automatic pong, while it is written). Received pongs are matched to
	// the pings sent, with the round-trip time. It must not call Ping.
	OnPingPong func(PingPong)

	// PingInterval sends a keepalive ping (empty payload) this long after
	// the connection opened and then every PingInterval while it is open
	// (WS-09). Zero or negative sends none.
	PingInterval time.Duration
	// PongTimeout, with PingInterval, is how long a pong may take after a
	// keepalive ping; when none arrives in time, the transport is closed
	// and the connection fails with CodePongTimeout. Any pong counts.
	// Zero or negative never gives up.
	PongTimeout time.Duration
	// Clock runs the keepalive timers; nil means SystemClock. It does not
	// replace Now.
	Clock Clock

	// Rand is the source of the handshake key and mask keys; nil means
	// crypto/rand.
	Rand io.Reader
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// target is the parsed URL.
type target struct {
	secure     bool
	hostHeader string   // authority without userinfo, as typed
	dialAddr   string   // host:port
	serverName string   // for SNI and certificate checks
	requestURI string   // path and query for the request line
	auth       string   // "Basic ..." from userinfo, or ""
	jarURL     *url.URL // for the cookie jar
	display    string   // URL without userinfo
}

func parseURL(raw string) (*target, error) {
	bad := func(msg string) error {
		return invalid(CodeInvalidURL, msg).WithHint("Enter a URL like ws://localhost:8080/socket or wss://example.com/socket.")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return nil, bad("The URL must start with ws:// or wss://.")
	}
	t := &target{}
	switch strings.ToLower(scheme) {
	case "ws":
	case "wss":
		t.secure = true
	default:
		return nil, invalid(CodeSchemeUnsupported, "The scheme "+strconv.Quote(scheme)+" is not a WebSocket scheme.").
			WithHint("Use ws:// or wss://.")
	}
	scheme = strings.ToLower(scheme)
	if strings.IndexByte(rest, '#') >= 0 {
		return nil, bad("A WebSocket URL cannot have a fragment (#).")
	}
	authority, reqTarget := rest, ""
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority, reqTarget = rest[:i], rest[i:]
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		user, pass, _ := strings.Cut(authority[:i], ":")
		u, err1 := url.PathUnescape(user)
		p, err2 := url.PathUnescape(pass)
		if err1 != nil || err2 != nil {
			return nil, bad("The user name or password in the URL has an invalid escape.")
		}
		t.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
		authority = authority[i+1:]
	}
	if authority == "" {
		return nil, bad("The URL has no host.")
	}
	for i := 0; i < len(authority); i++ {
		if c := authority[i]; c <= ' ' || c >= 0x7f {
			return nil, bad("The host in the URL contains invalid characters.")
		}
	}
	hu, err := url.Parse(scheme + "://" + authority)
	if err != nil || hu.Hostname() == "" || hu.Path != "" || hu.RawQuery != "" || hu.User != nil {
		return nil, bad("The host in the URL is not valid.")
	}
	port := hu.Port()
	if port == "" {
		if strings.HasSuffix(authority, ":") {
			return nil, bad("The port in the URL is empty.")
		}
		port = "80"
		if t.secure {
			port = "443"
		}
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, bad("The port in the URL must be from 1 to 65535.")
	}
	t.hostHeader = authority
	t.dialAddr = net.JoinHostPort(hu.Hostname(), port)
	t.serverName = hu.Hostname()

	switch {
	case reqTarget == "":
		reqTarget = "/"
	case reqTarget[0] == '?':
		reqTarget = "/" + reqTarget
	}
	t.requestURI = encodeTarget(reqTarget)
	path, _, _ := strings.Cut(reqTarget, "?")
	if p, err := url.PathUnescape(path); err == nil {
		path = p
	}
	t.jarURL = &url.URL{Scheme: scheme, Host: authority, Path: path}
	t.display = scheme + "://" + authority + t.requestURI
	return t, nil
}

// encodeTarget percent-encodes the bytes that cannot appear in a request
// line: control characters, space, DEL and non-ASCII bytes.
func encodeTarget(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// reservedHeaders are generated by the handshake (or make no sense on it).
var reservedHeaders = []string{
	"Upgrade", "Connection", "Sec-WebSocket-Key", "Sec-WebSocket-Version",
	"Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions", "Sec-WebSocket-Accept",
	"Content-Length", "Transfer-Encoding",
}

func isReserved(name string) bool {
	for _, r := range reservedHeaders {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// checkHeaders validates the enabled rows and returns them with values
// trimmed.
func checkHeaders(rows []httpclient.Header, allowHandshake bool) ([]httpclient.Header, error) {
	var out []httpclient.Header
	hosts := 0
	for i, h := range rows {
		if !h.Enabled {
			continue
		}
		if !isToken(h.Name) {
			return nil, invalid(CodeInvalidHeader, "The header name "+strconv.Quote(h.Name)+" is not valid.").
				WithDetail("index", strconv.Itoa(i))
		}
		v := strings.Trim(h.Value, " \t")
		for j := 0; j < len(v); j++ {
			if c := v[j]; (c < ' ' && c != '\t') || c == 0x7f {
				return nil, invalid(CodeInvalidHeader, "The value of the header "+h.Name+" contains control characters.").
					WithDetail("index", strconv.Itoa(i))
			}
		}
		if strings.EqualFold(h.Name, "Host") {
			hosts++
			if hosts > 1 {
				return nil, invalid(CodeInvalidHeader, "Only one Host header can be sent.").WithDetail("index", strconv.Itoa(i))
			}
		}
		if !allowHandshake && isReserved(h.Name) {
			hint := HintReservedHeader
			if strings.EqualFold(h.Name, "Sec-WebSocket-Protocol") {
				hint = "Add subprotocols in the subprotocol list instead of a header row."
			}
			return nil, invalid(CodeReservedHeader, "The header "+h.Name+" is set by the WebSocket handshake.").
				WithHint(hint).WithDetail("index", strconv.Itoa(i))
		}
		out = append(out, httpclient.Header{Name: h.Name, Value: v, Enabled: true})
	}
	return out, nil
}

func checkSubprotocols(list []string) error {
	seen := map[string]bool{}
	for _, p := range list {
		if !isToken(p) {
			return invalid(CodeInvalidSubprotocol, "The subprotocol "+strconv.Quote(p)+" is not valid.").
				WithHint("A subprotocol is a token: letters, digits and !#$%&'*+-.^_`|~, no spaces or commas.")
		}
		if seen[p] {
			return invalid(CodeInvalidSubprotocol, "The subprotocol "+strconv.Quote(p)+" is listed twice.")
		}
		seen[p] = true
	}
	return nil
}

// isToken reports whether s is a non-empty RFC 9110 token.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
