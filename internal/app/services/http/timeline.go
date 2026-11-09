package http

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// MaxTimelineBody is how much of a request body TimelineDTO carries per
// hop: the first 64 KiB as text, or as a hex dump of as many bytes when it
// is not UTF-8 text. httpclient records up to MaxRecordedBody.
const MaxTimelineBody = MaxHexBytes

// TimelineDTO is the Timeline tab (DBG-01): each hop's phase timings and
// connection (DBG-03, DBG-04), its request as it went over the wire and the
// response head it got, then the trace of the send.
// Secret values are masked (docs/privacy.md).
type TimelineDTO struct {
	// Hops are the OAuth 2.0 token requests (AUTH-33), the redirects
	// followed and auth challenges answered, in order, then the final
	// exchange.
	// Never empty.
	Hops []TimelineHopDTO `json:"hops"`
	// Events are the trace of the send, all hops (httpclient.TimelineEvent),
	// at most 1000 of them.
	Events []TimelineEventDTO `json:"events"`
	// Masked is set when a secret was hidden in a request.
	Masked bool `json:"masked"`
}

// Kinds of TimelineHopDTO.
const (
	// HopRedirect: answered with a redirect that was followed.
	HopRedirect = httpclient.HopRedirect
	// HopAuth: answered with a 401 auth challenge, which the next hop
	// answers (Digest, AUTH-06).
	HopAuth = httpclient.HopAuth
	// HopFinal: the last exchange, whose response is shown.
	HopFinal = "final"
)

// TimelineHopDTO is one request and the head of its response.
type TimelineHopDTO struct {
	// Kind is "token" (an OAuth 2.0 token request, AUTH-33), "redirect",
	// "auth" or "final".
	Kind    string         `json:"kind"`
	Request SentRequestDTO `json:"request"`
	// StartMs is when the hop started, from the start of the send.
	StartMs float64 `json:"startMs"`
	// ResponseProto, StatusCode and Status are the response's status line.
	ResponseProto string `json:"responseProto"`
	StatusCode    int    `json:"statusCode"`
	Status        string `json:"status"`
	// ResponseHead is the status line and headers as HTTP/1.1 text ("HTTP/1.1
	// 200 OK", then "Name: value" per header, lines ended by CRLF, then a
	// blank line). The headers are as Go parsed them: names canonicalized
	// and sorted, the values of one name in the order received.
	ResponseHead string `json:"responseHead"`
	// Timing is the hop's phases (DBG-03) and Conn its connection
	// (DBG-04, DBG-05).
	Timing HopTimingDTO `json:"timing"`
	Conn   ConnInfoDTO  `json:"conn"`
	// ResponseBody is the body of an OAuth 2.0 token response (kind
	// "token", AUTH-33) as text, masked, at most MaxTimelineBody bytes;
	// "" for the other hops, whose bodies are not shown here.
	ResponseBody string `json:"responseBody,omitempty"`
}

// SentRequestDTO is a request as sent (httpclient.SentRequest), masked.
type SentRequestDTO struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Proto is "HTTP/1.1" or "HTTP/2.0"; "" when nothing was written.
	Proto  string `json:"proto"`
	Target string `json:"target"`
	// Headers are the header fields in the order written, HTTP/2
	// pseudo-header fields included.
	Headers []HeaderDTO `json:"headers"`
	// Head is the request line and headers as written (HTTP/1.1 exactly,
	// HTTP/2 as "name: value" lines), lines ended by CRLF, with a blank
	// line at the end.
	Head string `json:"head"`
	// Body is the body as text, or a hex dump when BodyHex. At most
	// MaxTimelineBody bytes of it are shown (BodyShown); BodyClipped is set
	// when the body is longer than that. BodySize counts every byte sent.
	Body        string `json:"body"`
	BodyHex     bool   `json:"bodyHex"`
	BodyShown   int64  `json:"bodyShown"`
	BodyClipped bool   `json:"bodyClipped"`
	BodySize    int64  `json:"bodySize"`
	// BodyChunked is set when the body went out with chunked transfer
	// coding; Body is the payload without the framing.
	BodyChunked bool `json:"bodyChunked"`
}

// TimelineEventDTO is an entry of the trace (httpclient.TimelineEvent).
type TimelineEventDTO struct {
	// AtMs is the time from the start of the send, in milliseconds with
	// microsecond precision.
	AtMs   float64 `json:"atMs"`
	Name   string  `json:"name"`
	Detail string  `json:"detail"`
}

// secretsOf returns the values the timeline masks for a send: the secret
// variables x substituted, and what the request's auth added or signed with
// as b was built (request.HTTPBuild.AuthSecrets: an API key's value, a
// JWT and its secret; Authorization is masked by name too). They are the
// values of this build: a JWT is signed anew on every build, with its own
// iat and jti.
func secretsOf(x *expansion, b request.HTTPBuild) []string {
	out := append(append([]string(nil), x.secrets...), b.AuthSecrets...)
	// The password of the request's own proxy is never in a request, but
	// is masked like the auth's should it be echoed anywhere.
	if pw := b.Proxy.Secret(); pw != "" {
		out = append(out, pw)
	}
	return out
}

// hopKind is the DTO kind of a hop before the final one: a hop without a
// kind is a redirect.
func hopKind(kind string) string {
	if kind == "" {
		return HopRedirect
	}
	return kind
}

// toTimelineDTO builds the timeline of r, masking with m.
func toTimelineDTO(r *httpclient.Response, m httpclient.Masker) TimelineDTO {
	out := TimelineDTO{Hops: make([]TimelineHopDTO, 0, len(r.Redirects)+1), Events: make([]TimelineEventDTO, 0, len(r.Events))}
	for _, h := range r.Redirects {
		req, masked := sentRequestDTO(h.Request, m)
		out.Masked = out.Masked || masked
		out.Hops = append(out.Hops, TimelineHopDTO{
			Kind:          hopKind(h.Kind),
			Request:       req,
			StartMs:       ms(h.Start),
			ResponseProto: h.Proto,
			StatusCode:    h.StatusCode,
			Status:        h.Status,
			ResponseHead:  responseHead(h.Proto, h.Status, h.Headers, m),
			Timing:        hopTimingDTO(h.Timing, h.Timing.Total, h.Conn, false),
			Conn:          connInfoDTO(h.Conn),
		})
	}
	req, masked := sentRequestDTO(r.Request, m)
	out.Masked = out.Masked || masked
	var start time.Duration
	if n := len(r.Redirects); n > 0 {
		last := r.Redirects[n-1]
		start = last.Start + last.Timing.Total
	}
	out.Hops = append(out.Hops, TimelineHopDTO{
		Kind:          HopFinal,
		Request:       req,
		StartMs:       ms(start),
		ResponseProto: r.Proto,
		StatusCode:    r.StatusCode,
		Status:        r.Status,
		ResponseHead:  responseHead(r.Proto, r.Status, r.Headers, m),
		Timing:        hopTimingDTO(r.Timing, r.Timing.Total-start, r.Conn, true),
		Conn:          connInfoDTO(r.Conn),
	})
	for _, e := range r.Events {
		out.Events = append(out.Events, TimelineEventDTO{AtMs: ms(e.At), Name: e.Name, Detail: e.Detail})
	}
	return out
}

// sentRequestDTO masks s with m and converts it; masked reports whether
// anything was hidden.
func sentRequestDTO(s *httpclient.SentRequest, m httpclient.Masker) (SentRequestDTO, bool) {
	if s == nil {
		return SentRequestDTO{Headers: []HeaderDTO{}}, false
	}
	v := s.Masked(m)
	masked := v.URL != s.URL || v.Target != s.Target || string(v.Body) != string(s.Body)
	out := SentRequestDTO{
		Method:   v.Method,
		URL:      v.URL,
		Proto:    v.Proto,
		Target:   v.Target,
		Headers:  make([]HeaderDTO, 0, len(v.Headers)),
		Head:     string(v.Head()),
		BodySize: s.BodySize,
	}
	for i, h := range v.Headers {
		masked = masked || h.Value != s.Headers[i].Value
		out.Headers = append(out.Headers, HeaderDTO{Name: h.Name, Value: h.Value})
		if strings.EqualFold(h.Name, "Transfer-Encoding") && strings.EqualFold(h.Value, "chunked") {
			out.BodyChunked = true
		}
	}
	body := v.Body
	if utf8.Valid(clipUTF8(body, MaxTimelineBody)) {
		shown := clipUTF8(body, MaxTimelineBody)
		out.Body, out.BodyShown = string(shown), int64(len(shown))
	} else {
		shown := body[:min(len(body), MaxTimelineBody)]
		out.Body, out.BodyHex, out.BodyShown = charset.HexDump(shown), true, int64(len(shown))
	}
	out.BodyClipped = out.BodyShown < s.BodySize
	return out, masked
}

// clipUTF8 cuts b to at most n bytes without splitting a UTF-8 sequence
// at the end.
func clipUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	b = b[:n]
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				b = b[:i]
			}
			break
		}
	}
	return b
}

// responseHead writes a response's status line and headers as HTTP/1.1
// text, the values masked with m.
func responseHead(proto, status string, headers []httpclient.Header, m httpclient.Masker) string {
	var b strings.Builder
	if proto == "" {
		proto = "HTTP/1.1"
	}
	b.WriteString(proto + " " + status + "\r\n")
	for _, h := range headers {
		b.WriteString(h.Name + ": " + m.MaskHeader(h.Name, h.Value) + "\r\n")
	}
	b.WriteString("\r\n")
	return b.String()
}

// ms converts d to milliseconds, keeping microseconds.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
