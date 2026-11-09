package snippet

// websocket.go: a WebSocket connection as a websocat or wscat command
// (GEN-11), for a POSIX shell. docs/snippets.md#websocat-and-wscat.

import (
	"encoding/base64"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
)

// WebSocketRequest is a WebSocket handshake as PostSonde makes it, for the
// WebSocket generators (WebSocketLanguages).
type WebSocketRequest struct {
	// URL is the ws:// or wss:// URL without userinfo (WebSocketURL).
	URL string
	// Headers are the enabled rows in order, after the Authorization the
	// URL's userinfo makes (WebSocketURL) and with the jar's cookies in
	// the first Cookie row or one of their own, as the handshake sends
	// them.
	Headers []Header
	// Subprotocols are offered in Sec-WebSocket-Protocol, in order.
	Subprotocols []string
	// Insecure turns TLS verification off.
	Insecure bool
	// PingInterval sends a keepalive ping this often; zero sends none.
	// PongTimeout is how long its pong may take; zero is no limit.
	PingInterval time.Duration
	PongTimeout  time.Duration
}

// WebSocketURL returns raw as the WebSocket client sends it: the userinfo
// split off, with the "Basic …" Authorization value it makes ("" for
// none), and the control characters, spaces and non-ASCII bytes of the
// path and query percent-encoded. A userinfo with an invalid escape is
// left in the URL.
func WebSocketURL(raw string) (string, string) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw, ""
	}
	authority, target := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, target = rest[:i], encodeTarget(rest[i:])
	}
	i := strings.LastIndexByte(authority, '@')
	if i < 0 {
		return scheme + "://" + authority + target, ""
	}
	user, pass, _ := strings.Cut(authority[:i], ":")
	u, err1 := url.PathUnescape(user)
	p, err2 := url.PathUnescape(pass)
	if err1 != nil || err2 != nil {
		return scheme + "://" + authority + target, ""
	}
	return scheme + "://" + authority[i+1:] + target, "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
}

// encodeTarget percent-encodes the bytes a request line cannot hold, as
// the WebSocket client does: control characters, space, DEL and non-ASCII
// bytes.
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

// WithCookies returns headers with cookies (a Cookie value, "a=1; b=2")
// added as the handshake adds the jar's: after the first Cookie row's
// value, or as a Cookie row at the end. The caller leaves out the jar
// cookies a Cookie row already names.
func WithCookies(headers []Header, cookies string) []Header {
	if cookies == "" {
		return headers
	}
	out := append([]Header(nil), headers...)
	for i, h := range out {
		if strings.EqualFold(h.Name, "Cookie") {
			if strings.TrimSpace(h.Value) == "" {
				out[i].Value = cookies
			} else {
				out[i].Value = h.Value + "; " + cookies
			}
			return out
		}
	}
	return append(out, Header{Name: "Cookie", Value: cookies})
}

// Masked returns r with its secrets replaced by Mask, and whether anything
// was (PRV-09, GEN-13): each secret in the URL, a header value or a
// subprotocol, and the credentials of every Authorization and
// Proxy-Authorization row after the scheme. With templated, a credential
// made only of {{variable}} references is kept (it names the variable).
func (r WebSocketRequest) Masked(secrets []string, templated bool) (WebSocketRequest, bool) {
	m := newMasker(secrets)
	out := r
	out.URL = m.text(r.URL)
	out.Headers = m.headers(r.Headers, templated)
	if r.Subprotocols != nil {
		out.Subprotocols = make([]string, len(r.Subprotocols))
		for i, p := range r.Subprotocols {
			out.Subprotocols[i] = m.text(p)
		}
	}
	return out, m.changed
}

// Websocat returns r as a websocat command for a POSIX shell (GEN-11):
// the URL on the first line, then one option per continued line, every
// word quoted with Quote. Header rows are -H 'Name: value', the
// subprotocols one --protocol 'a, b' (the Sec-WebSocket-Protocol value),
// -k (--insecure) turns verification off, and --ping-interval and
// --ping-timeout (whole seconds, rounded up) follow the keepalive. The
// command sends each line typed as a text message and prints what
// arrives. It fails with CodeNulByte for a NUL byte.
func Websocat(r WebSocketRequest) (string, error) {
	w := shellWords{}
	first := []string{"websocat", w.quote(r.URL, "URL")}
	for _, h := range r.Headers {
		w.line("-H", w.quote(headerArg(h), "header "+h.Name))
	}
	if len(r.Subprotocols) > 0 {
		w.line("--protocol", w.quote(strings.Join(r.Subprotocols, ", "), "subprotocols"))
	}
	if r.Insecure {
		w.line("-k")
	}
	if r.PingInterval > 0 {
		w.line("--ping-interval", wholeSeconds(r.PingInterval))
		if r.PongTimeout > 0 {
			w.line("--ping-timeout", wholeSeconds(r.PongTimeout))
		}
	}
	return w.command(first)
}

// Wscat returns r as a wscat command for a POSIX shell (GEN-11): -c with
// the URL on the first line, then one option per continued line, every
// word quoted with Quote. Header rows are -H 'Name: value' (wscat keeps
// one value per name, so rows of the same name are joined: Cookie values
// with "; ", others with ", "), each subprotocol one -s, and -n
// (--no-check) turns verification off. wscat has no keepalive pings. The
// command sends each line typed as a text message and prints what
// arrives. It fails with CodeNulByte for a NUL byte.
func Wscat(r WebSocketRequest) (string, error) {
	w := shellWords{}
	first := []string{"wscat", "-c", w.quote(r.URL, "URL")}
	for _, h := range joinedHeaders(r.Headers) {
		w.line("-H", w.quote(headerArg(h), "header "+h.Name))
	}
	for _, p := range r.Subprotocols {
		w.line("-s", w.quote(p, "subprotocol"))
	}
	if r.Insecure {
		w.line("-n")
	}
	return w.command(first)
}

// headerArg is h as a "Name: value" option value ("Name:" when empty).
func headerArg(h Header) string {
	if h.Value == "" {
		return h.Name + ":"
	}
	return h.Name + ": " + h.Value
}

// joinedHeaders returns headers with the rows of the same name
// (case-insensitively) joined into the first: Cookie values with "; ",
// others with ", ".
func joinedHeaders(headers []Header) []Header {
	var out []Header
	at := map[string]int{}
	for _, h := range headers {
		key := strings.ToLower(h.Name)
		i, ok := at[key]
		if !ok {
			at[key] = len(out)
			out = append(out, h)
			continue
		}
		sep := ", "
		if key == "cookie" {
			sep = "; "
		}
		switch {
		case h.Value == "":
		case out[i].Value == "":
			out[i].Value = h.Value
		default:
			out[i].Value += sep + h.Value
		}
	}
	return out
}

// wholeSeconds is d in whole seconds, rounded up, at least 1.
func wholeSeconds(d time.Duration) string {
	return strconv.FormatInt(max(1, int64(math.Ceil(d.Seconds()))), 10)
}

// shellWords collects the continued lines of a command; the first quoting
// error is kept.
type shellWords struct {
	lines [][]string
	err   error
}

// quote is quoteArg, keeping the first error.
func (w *shellWords) quote(s, field string) string {
	if w.err != nil {
		return ""
	}
	q, err := quoteArg(s, field)
	w.err = err
	return q
}

func (w *shellWords) line(words ...string) { w.lines = append(w.lines, words) }

// command writes first, then each line continued with " \".
func (w *shellWords) command(first []string) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	var b strings.Builder
	b.WriteString(strings.Join(first, " "))
	for _, words := range w.lines {
		b.WriteString(" \\\n  " + strings.Join(words, " "))
	}
	return b.String(), nil
}

// masker masks the text of a gRPC or WebSocket request as Masked masks an
// HTTP one, and records whether it changed anything.
type masker struct {
	m       *httpclient.SecretMasker
	changed bool
}

func newMasker(secrets []string) *masker { return &masker{m: httpclient.NewSecretMasker(secrets)} }

func (m *masker) text(s string) string {
	t := m.m.MaskText(s)
	m.changed = m.changed || t != s
	return t
}

// headers masks each secret in the values and the credentials of the
// Authorization and Proxy-Authorization rows (case-insensitive names, as
// gRPC's lower-case keys); with templated, a credential made only of
// {{variable}} references is kept.
func (m *masker) headers(hs []Header, templated bool) []Header {
	if hs == nil {
		return nil
	}
	out := make([]Header, len(hs))
	for i, h := range hs {
		v := m.m.MaskHeader(h.Name, h.Value)
		if templated && isCredentialHeader(h.Name) && requestsecrets.IsReference(credentialOf(h.Value)) {
			v = m.m.MaskText(h.Value)
		}
		m.changed = m.changed || v != h.Value
		out[i] = Header{Name: h.Name, Value: v}
	}
	return out
}
