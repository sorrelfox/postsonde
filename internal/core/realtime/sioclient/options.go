package sioclient

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Transport says how the Engine.IO connection is made (SIO-01).
type Transport string

const (
	// TransportAuto (also "") connects over WebSocket and, when that
	// connection cannot be made (refused, rejected upgrade, TLS), falls
	// back to HTTP long-polling.
	TransportAuto Transport = "auto"
	// TransportWebSocket connects over WebSocket only.
	TransportWebSocket Transport = "websocket"
	// TransportPolling uses HTTP long-polling only, never upgrading.
	TransportPolling Transport = "polling"
	// TransportUpgrade starts with long-polling and upgrades to WebSocket
	// when the server offers it, as socket.io-client does by default. A
	// failed upgrade keeps the connection on long-polling.
	TransportUpgrade Transport = "upgrade"
)

// Version is the Socket.IO version of the server (SIO-02).
type Version string

const (
	// VersionV4 (also "") speaks Engine.IO 4 and Socket.IO 5: the
	// protocol of socket.io v3 and v4 servers.
	VersionV4 Version = "v4"
	// VersionV2 speaks Engine.IO 3 and Socket.IO 4: the protocol of
	// socket.io v2 servers (and of v3/v4 servers with allowEIO3). The
	// client pings, binary data and long-polling payloads are written the
	// Engine.IO 3 way, the main namespace is joined without a CONNECT and
	// there is no auth payload (the URL's query goes with the CONNECT of
	// another namespace instead).
	VersionV2 Version = "v2"
)

// Versions lists the versions in the order the editor offers them.
func Versions() []Version { return []Version{VersionV4, VersionV2} }

func checkVersion(v Version) (Version, error) {
	switch v {
	case "", "v3", VersionV4:
		return VersionV4, nil
	case VersionV2:
		return VersionV2, nil
	}
	return "", invalid(CodeInvalidVersion, "The Socket.IO version "+strconv.Quote(string(v))+" does not exist.").
		WithHint(`Use "v4" for Socket.IO v3 and v4 servers, or "v2".`)
}

// Transports lists the transports in the order the editor offers them.
func Transports() []Transport {
	return []Transport{TransportAuto, TransportWebSocket, TransportPolling, TransportUpgrade}
}

// Defaults for zero Options fields.
const (
	// DefaultPath is the Engine.IO handshake path of Socket.IO servers.
	DefaultPath = "/socket.io/"
	// DefaultHandshakeTimeout bounds the Engine.IO handshake, an upgrade
	// and the namespace CONNECT together (socket.io-client's timeout).
	DefaultHandshakeTimeout = 20 * time.Second
	// DefaultAckTimeout is how long an emit waits for its acknowledgement.
	DefaultAckTimeout = 10 * time.Second
)

// Options configure a Client.
type Options struct {
	// URL is the server: http://, https://, ws:// or wss:// (the scheme
	// only says whether TLS is used; both transports are derived from it).
	// Its query is sent with every request, as typed. Its path is the
	// namespace when Namespace is "", as with io("https://host/admin").
	// Userinfo becomes "Authorization: Basic" unless a header row sets
	// Authorization. A fragment is an error.
	URL string
	// Path is the Engine.IO handshake path; "" means DefaultPath. A
	// trailing "/" is added when missing, as socket.io-client does.
	Path string
	// Namespace is the namespace to connect to ("/admin"); "" means the
	// URL's path, or "/" when it has none. A missing leading "/" is added.
	Namespace string
	// Auth is the auth payload of the CONNECT packet (SIO-07): a JSON
	// object, or empty for none.
	Auth json.RawMessage
	// Headers are sent with every long-polling request and the WebSocket
	// handshake, in order; rows that are not Enabled are skipped.
	Headers []httpclient.Header
	// Transport is "" (TransportAuto) or one of the Transport constants.
	Transport Transport
	// Version is "" (VersionV4) or VersionV2 (SIO-02). Auth must be empty
	// with VersionV2.
	Version Version

	// Dialer opens the TCP connections. Required. The context of Connect
	// is passed to every dial, also those of later long-polling requests,
	// so a netx.WithUserOp mark on it covers the whole connection.
	Dialer netx.Dialer
	// RootCAs are trusted for https and wss; nil means the system's.
	RootCAs *x509.CertPool
	// InsecureSkipVerify turns off certificate verification.
	InsecureSkipVerify bool
	// ClientCertificate is sent when the server asks (AUTH-30); nil sends
	// none.
	ClientCertificate *httpclient.ClientCert
	// Jar, if set, supplies cookies for every request and stores the
	// Set-Cookie headers of the responses (sticky sessions of load
	// balancers rely on them).
	Jar httpclient.CookieJar

	// HandshakeTimeout bounds connecting: zero means
	// DefaultHandshakeTimeout, negative no limit besides Connect's context.
	HandshakeTimeout time.Duration
	// AckTimeout is how long Emit waits for an acknowledgement before a
	// TraceAckTimeout; zero means DefaultAckTimeout.
	AckTimeout time.Duration
	// MaxMessageSize caps one WebSocket message and one long-polling
	// response body; zero means engineio.DefaultMaxSize.
	MaxMessageSize int

	// OnTrace is called for everything that happens on the connection, in
	// order (see Trace). It runs on the goroutine that caused it, must
	// return quickly and must not call Connect or Disconnect.
	OnTrace func(Trace)

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// target is the parsed URL and path.
type target struct {
	secure    bool
	authority string // host[:port] as typed, without userinfo
	auth      string // "Basic ..." from userinfo, or ""
	query     string // the URL's raw query, without "?"
	path      string // the Engine.IO path, with a trailing "/"
	urlPath   string // the URL's path, decoded
}

func parseTarget(raw, path string) (*target, error) {
	bad := func(msg string) error {
		return invalid(CodeInvalidURL, msg).WithHint("Enter the server's URL, like http://localhost:3000 or https://example.com/admin.")
	}
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return nil, bad("The URL must start with http://, https://, ws:// or wss://.")
	}
	t := &target{}
	switch strings.ToLower(scheme) {
	case "http", "ws":
	case "https", "wss":
		t.secure = true
	default:
		return nil, invalid(CodeSchemeUnsupported, "The scheme "+strconv.Quote(scheme)+" cannot be used for Socket.IO.").
			WithHint("Use http://, https://, ws:// or wss://.")
	}
	if strings.IndexByte(rest, '#') >= 0 {
		return nil, bad("A Socket.IO URL cannot have a fragment (#).")
	}
	authority, reqPath := rest, ""
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority, reqPath = rest[:i], rest[i:]
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
	hu, err := url.Parse("http://" + authority)
	if err != nil || hu.Hostname() == "" || hu.Path != "" || hu.RawQuery != "" || hu.User != nil {
		return nil, bad("The host in the URL is not valid.")
	}
	if port := hu.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, bad("The port in the URL must be from 1 to 65535.")
		}
	} else if strings.HasSuffix(authority, ":") {
		return nil, bad("The port in the URL is empty.")
	}
	t.authority = authority
	p, q, _ := strings.Cut(reqPath, "?")
	t.query = encodeQuery(q)
	if p != "" && p != "/" {
		if d, err := url.PathUnescape(p); err == nil {
			p = d
		}
		t.urlPath = p
	}

	if path = strings.TrimSpace(path); path == "" {
		path = DefaultPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.ContainsAny(path, "?#") || strings.ContainsFunc(path, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return nil, invalid(CodeInvalidPath, "The Socket.IO path cannot contain spaces, \"?\" or \"#\".").
			WithHint("The path is where the server listens, " + DefaultPath + " unless the server sets another.")
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	t.path = encodeQuery(path)
	return t, nil
}

// encodeQuery percent-encodes the bytes that cannot appear in a request
// line (controls, space, DEL, non-ASCII); the rest is kept as typed.
func encodeQuery(s string) string {
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

// url returns the URL of one Engine.IO request of protocol eio: over
// WebSocket (ws) or long-polling, with the session ID once there is one.
// Engine.IO 3 long-polling asks for text payloads (b64=1), as a client
// without XHR2 does.
func (t *target) url(ws bool, sid string, eio int) string {
	scheme := "http"
	transport := "polling"
	if ws {
		scheme = "ws"
		transport = "websocket"
	}
	if t.secure {
		scheme += "s"
	}
	q := t.query
	if q != "" {
		q += "&"
	}
	q += "EIO=" + strconv.Itoa(eio) + "&transport=" + transport
	if eio == 3 && !ws {
		q += "&b64=1"
	}
	if sid != "" {
		q += "&sid=" + url.QueryEscape(sid)
	}
	return scheme + "://" + t.authority + t.path + "?" + q
}

// Display is the server as shown in logs and the UI: scheme, host and
// path, without userinfo and query.
func (t *target) display() string {
	scheme := "http"
	if t.secure {
		scheme = "https"
	}
	return scheme + "://" + t.authority + t.path
}

// namespaceOf returns the namespace to connect to.
func namespaceOf(ns, urlPath string) (string, error) {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		ns = urlPath
	}
	if ns == "" {
		return "/", nil
	}
	if !strings.HasPrefix(ns, "/") {
		ns = "/" + ns
	}
	if strings.ContainsRune(ns, ',') {
		return "", invalid(CodeInvalidNamespace, "A Socket.IO namespace cannot contain \",\".").
			WithHint("Namespaces look like /admin or /chat.")
	}
	return ns, nil
}

// checkAuth checks that auth is empty or one JSON object and returns it
// compacted.
func checkAuth(auth json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(auth)) == 0 {
		return nil, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(auth))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, invalid(CodeInvalidAuth, "The auth payload is not valid JSON.").
			WithHint(`Enter a JSON object, like {"token": "abc"}.`)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, invalid(CodeInvalidAuth, "The auth payload must be a JSON object.").
			WithHint(`Enter a JSON object, like {"token": "abc"}.`)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, auth); err != nil {
		return nil, invalid(CodeInvalidAuth, "The auth payload is not valid JSON.")
	}
	return buf.Bytes(), nil
}

// checkHeaders validates the enabled rows and returns them with values
// trimmed.
func checkHeaders(rows []httpclient.Header) ([]httpclient.Header, error) {
	var out []httpclient.Header
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
		out = append(out, httpclient.Header{Name: h.Name, Value: v, Enabled: true})
	}
	return out, nil
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

func checkTransport(t Transport) (Transport, error) {
	if t == "" {
		return TransportAuto, nil
	}
	for _, k := range Transports() {
		if t == k {
			return t, nil
		}
	}
	return "", invalid(CodeInvalidTransport, "The transport "+strconv.Quote(string(t))+" does not exist.").
		WithHint(`Use "auto", "websocket", "polling" or "upgrade".`)
}
