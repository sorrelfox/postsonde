package httpclient

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// SentRequest is one hop's request as the client wrote it (DBG-01): the
// final URL, every header field (those the client adds itself included)
// and the body, after variables and auth were applied. It is recorded from
// the net/http/httptrace WroteHeaderField hook and from the body as the
// transport read it, so it is what went over the wire, not what was
// configured.
type SentRequest struct {
	// Method is the method as sent.
	Method string
	// URL is the absolute URL of the hop, without userinfo and fragment.
	URL string
	// Proto is "HTTP/1.1" or "HTTP/2.0", the protocol the request was sent
	// with. "" when no header field was written (the send failed first).
	Proto string
	// Target is the request-target of the HTTP/1.1 request line ("/a?b=1"),
	// or the :path pseudo-header field on HTTP/2.
	Target string
	// Headers are the header fields in the order they were written, as
	// written. On HTTP/1.1 that is Host, User-Agent (when not empty),
	// Content-Length or Transfer-Encoding, then the other fields sorted by
	// name, each name spelled as typed. On HTTP/2 the pseudo-header fields
	// come first (":authority", ":method", ":path", ":scheme"), the other
	// names are lower-cased, a Cookie field is split into one field per
	// cookie, and the fields go over the wire HPACK-compressed. Enabled is
	// always true.
	Headers []Header
	// Body is the start of the request body as the transport read it for
	// sending, at most MaxRecordedBody bytes. Chunked framing is not
	// included. Never nil.
	Body []byte
	// BodySize is the number of body bytes read; BodyTruncated is set when
	// it is more than Body holds.
	BodySize      int64
	BodyTruncated bool
}

// MaxRecordedBody is how much of each hop's request body a SentRequest
// keeps (1 MiB). The size is always counted.
const MaxRecordedBody = 1 << 20

// Head returns the request line and header fields as text, each line ended
// by CRLF, then the blank line. On HTTP/1.1 these are the bytes written,
// exactly: Head followed by Body is the request as the server received it
// when Body holds the whole body and it was not sent chunked. On HTTP/2
// the lines are "name: value" per field, pseudo-header fields included,
// without a request line: the fields went over the wire compressed.
func (r *SentRequest) Head() []byte {
	var b strings.Builder
	if r.Proto != "HTTP/2.0" {
		method := r.Method
		if method == "" {
			method = http.MethodGet
		}
		b.WriteString(method + " " + r.Target + " HTTP/1.1\r\n")
	}
	for _, h := range r.Headers {
		b.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String())
}

// Masker hides secrets in a recorded request for display (DBG-01): the
// service that shows a SentRequest knows which values are secret, the
// client does not. SecretMasker is the usual implementation.
type Masker interface {
	// MaskHeader returns the value of header field name to show.
	MaskHeader(name, value string) string
	// MaskText returns s, a URL, request-target or body, to show.
	MaskText(s string) string
}

// Masked returns a copy of r with m applied to the URL, the target, every
// header value and the body. The body is masked as text: a secret is found
// in it by its bytes. r is not changed.
func (r *SentRequest) Masked(m Masker) *SentRequest {
	out := *r
	out.URL = m.MaskText(r.URL)
	out.Target = m.MaskText(r.Target)
	out.Headers = make([]Header, len(r.Headers))
	for i, h := range r.Headers {
		out.Headers[i] = Header{Name: h.Name, Value: m.MaskHeader(h.Name, h.Value), Enabled: h.Enabled}
	}
	out.Body = r.Body
	if len(r.Body) > 0 {
		if s := m.MaskText(string(r.Body)); s != string(r.Body) {
			out.Body = []byte(s)
		}
	}
	return &out
}

// Mask replaces a secret in what SecretMasker returns.
const Mask = "••••••"

// SecretMasker hides known secret values and the credentials of the
// Authorization and Proxy-Authorization fields.
//
//   - Authorization and Proxy-Authorization (any spelling): the value after
//     the scheme is replaced ("Bearer ••••••", "Basic ••••••"); a value
//     without a scheme is replaced whole. So a Basic password is hidden
//     although only its Base64 form is sent. Digest credentials keep
//     their parameters, except the response hash ("Digest username=…,
//     response=\"••••••\""); the known secrets are masked in the rest.
//   - Everywhere else: each occurrence of a secret, as it is and in its
//     query and path encodings, is replaced by Mask. Longer secrets are
//     replaced first, so one that contains another is hidden whole.
//
// The zero value masks the credential fields only.
type SecretMasker struct {
	// needles are the strings to replace, longest first.
	needles []string
}

// NewSecretMasker returns a SecretMasker for secrets. Empty secrets are
// ignored.
func NewSecretMasker(secrets []string) *SecretMasker {
	seen := map[string]bool{}
	var needles []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			needles = append(needles, s)
		}
	}
	for _, s := range secrets {
		add(s)
		add(url.QueryEscape(s))
		add(url.PathEscape(s))
	}
	slices.SortStableFunc(needles, func(a, b string) int { return len(b) - len(a) })
	return &SecretMasker{needles: needles}
}

var _ Masker = (*SecretMasker)(nil)

// MaskHeader implements Masker.
func (m *SecretMasker) MaskHeader(name, value string) string {
	if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
		if value == "" {
			return value
		}
		scheme, rest, ok := strings.Cut(value, " ")
		if !ok || strings.TrimSpace(rest) == "" {
			return Mask
		}
		if strings.EqualFold(scheme, "Digest") {
			return scheme + " " + m.MaskText(maskDigestResponse(rest))
		}
		return scheme + " " + Mask
	}
	return m.MaskText(value)
}

// maskDigestResponse replaces the value of the response parameter of
// Digest credentials with a quoted Mask. The other parameters (username,
// realm, nonce, uri, cnonce…) are kept: they say what was answered, and
// only the response, a hash of the password, could be used to guess it.
func maskDigestResponse(params string) string {
	p := &authParser{s: params}
	var b strings.Builder
	last := 0
	for {
		p.skip(" \t,")
		if p.done() {
			break
		}
		name := p.token()
		if name == "" {
			p.i++
			continue
		}
		p.skip(" \t")
		if p.peek() != '=' {
			continue
		}
		p.i++
		p.skip(" \t")
		start := p.i
		if p.peek() == '"' {
			p.quoted()
		} else {
			p.token()
		}
		if strings.EqualFold(name, "response") {
			b.WriteString(params[last:start])
			b.WriteString(`"` + Mask + `"`)
			last = p.i
		}
	}
	b.WriteString(params[last:])
	return b.String()
}

// MaskText implements Masker.
func (m *SecretMasker) MaskText(s string) string {
	if m == nil {
		return s
	}
	for _, n := range m.needles {
		if strings.Contains(s, n) {
			s = strings.ReplaceAll(s, n, Mask)
		}
	}
	return s
}

// bodyCapture keeps the start of a hop's request body as the transport
// reads it, and counts the rest.
type bodyCapture struct {
	mu   sync.Mutex
	data []byte
	size int64
}

func (c *bodyCapture) add(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size += int64(len(p))
	if room := MaxRecordedBody - len(c.data); room > 0 {
		c.data = append(c.data, p[:min(room, len(p))]...)
	}
}

// reset starts over, for a body sent again (a retry on a new connection).
func (c *bodyCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data, c.size = c.data[:0], 0
}

func (c *bodyCapture) snapshot() (data []byte, size int64) {
	if c == nil {
		return []byte{}, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte{}, c.data...), c.size
}

// captureReader reads a body and copies what it reads to a bodyCapture.
type captureReader struct {
	rc io.ReadCloser
	c  *bodyCapture
}

func (r *captureReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		r.c.add(p[:n])
	}
	return n, err
}

func (r *captureReader) Close() error { return r.rc.Close() }

// captureBody returns req with its body read through a new bodyCapture,
// and the capture; req itself and nil when it has no body. GetBody, which
// net/http calls to send the body again on another connection, starts the
// capture over.
func captureBody(req *http.Request) (*http.Request, *bodyCapture) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}
	c := &bodyCapture{}
	r := new(http.Request)
	*r = *req
	r.Body = &captureReader{rc: req.Body, c: c}
	if get := req.GetBody; get != nil {
		r.GetBody = func() (io.ReadCloser, error) {
			rc, err := get()
			if err != nil {
				return nil, err
			}
			c.reset()
			return &captureReader{rc: rc, c: c}, nil
		}
	}
	return r, c
}

// sentRecord is what the recorder collects about one hop's request.
type sentRecord struct {
	method, url, target string
	body                *bodyCapture
	// fields are the header fields written, from WroteHeaderField.
	fields []Header
}

// request returns the hop's SentRequest.
func (s *sentRecord) request() *SentRequest {
	out := &SentRequest{Method: s.method, URL: s.url, Target: s.target, Headers: slices.Clone(s.fields)}
	if out.Headers == nil {
		out.Headers = []Header{}
	}
	if len(s.fields) > 0 {
		out.Proto = "HTTP/1.1"
		if strings.HasPrefix(s.fields[0].Name, ":") {
			out.Proto = "HTTP/2.0"
			for _, f := range s.fields {
				if f.Name == ":path" {
					out.Target = f.Value
					break
				}
			}
		}
	}
	data, size := s.body.snapshot()
	out.Body, out.BodySize = data, size
	out.BodyTruncated = size > int64(len(data))
	return out
}
