package har

import (
	"encoding/base64"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// timeLayout is how HAR dates are written: ISO 8601 with milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// unknownMime is the content mimeType of a response without a
// Content-Type, as browsers write it.
const unknownMime = "x-unknown"

// Date formats t as a HAR date.
func Date(t time.Time) string { return t.Format(timeLayout) }

// AddPage appends a page titled title, started at started, and returns
// its ID ("page_1", "page_2"…), for Entry.Pageref.
func (f *File) AddPage(title string, started time.Time) string {
	id := "page_" + strconv.Itoa(len(f.Log.Pages)+1)
	f.Log.Pages = append(f.Log.Pages, Page{
		StartedDateTime: Date(started), ID: id, Title: title,
		PageTimings: PageTimings{OnContentLoad: -1, OnLoad: -1},
	})
	return id
}

// FromHistory returns the HAR entry of a history entry: the request as it
// went over the wire (the first hop), the final response, and the timings
// of the final hop. History entries are masked when they are recorded
// (docs/history.md#secrets), so the entry is too; nothing is unmasked.
// A send without a response has status 0 and its error in _error.
func FromHistory(e history.Entry) Entry {
	note := "The body is clipped to the first " + strconv.Itoa(history.MaxBody>>10) + " KiB the history keeps."
	return fromEntry(e, note, note)
}

// FromExchange returns the HAR entry of a response's whole exchange
// (EXP-06): e is made like a history entry, masked, but with its bodies
// whole: the request body as the client recorded it (its first
// httpclient.MaxRecordedBody bytes) and the response body as kept. A
// request body marked Clipped is longer than what was recorded; the
// response body is never clipped, only cut at the client's size limit
// (Truncated). Both say so in their comments.
func FromExchange(e history.Entry) Entry {
	return fromEntry(e,
		"The body is cut to the first "+strconv.Itoa(httpclient.MaxRecordedBody>>10)+" KiB PostSonde records of a request.",
		"The body is cut to what PostSonde kept of it.")
}

// fromEntry is FromHistory with the comments of clipped request and
// response bodies.
func fromEntry(e history.Entry, requestClipped, responseClipped string) Entry {
	out := Entry{
		StartedDateTime: Date(e.Time),
		Request:         historyRequest(e, requestClipped),
		Comment:         e.RequestName,
	}
	if e.Response == nil {
		out.Response = noResponse()
		out.Error = e.Error
		if out.Error == "" && e.Outcome == history.OutcomeCancelled {
			out.Error = "Cancelled."
		}
		out.Timings = Timings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1, Wait: float64(e.DurationMs)}
		out.Time = float64(e.DurationMs)
		return out
	}
	r := e.Response
	out.Response = historyResponse(r, responseClipped)
	out.Timings = historyTimings(r.Timings, e.DurationMs)
	out.Time = total(out.Timings)
	// The server's IP and, as the connection's ID, the local port.
	if host, _, ok := splitAddr(r.RemoteAddr); ok {
		out.ServerIPAddress = host
	}
	if _, port, ok := splitAddr(r.LocalAddr); ok {
		out.Connection = port
	}
	if r.Redirects > 0 {
		note := "Followed " + strconv.Itoa(r.Redirects) + " redirect"
		if r.Redirects > 1 {
			note += "s"
		}
		note += " to " + r.FinalURL + "; the request is the first hop, the response and timings the last."
		out.Comment = joinNote(out.Comment, note)
	}
	if e.Trimmed {
		out.Comment = joinNote(out.Comment, "The history left out the bodies (or headers) of this entry: it was too large.")
	}
	return out
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + " — " + b
}

// splitAddr splits "ip:port" (or "[ip6]:port"); ok is false for "".
func splitAddr(addr string) (host, port string, ok bool) {
	if addr == "" {
		return "", "", false
	}
	i := strings.LastIndexByte(addr, ':')
	if i < 0 || strings.HasSuffix(addr, "]") {
		return strings.Trim(addr, "[]"), "", true
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], true
}

func historyRequest(e history.Entry, clipped string) Request {
	s := e.Request
	method, u := s.Method, s.URL
	if method == "" {
		method = e.Method
	}
	if u == "" {
		u = e.URL
	}
	r := Request{
		Method:      method,
		URL:         u,
		HTTPVersion: httpVersion(s.Proto),
		Headers:     nameValues(s.Headers),
		Cookies:     requestCookies(s.Headers),
		QueryString: queryString(u),
		HeadersSize: -1,
		BodySize:    s.Body.Size,
	}
	if s.Body.Size > 0 || s.Body.Text != "" {
		pd := &PostData{MimeType: headerValue(s.Headers, "Content-Type"), Text: s.Body.Text}
		switch {
		case s.Body.Base64:
			pd.Comment = "The body is binary; text holds it in Base64."
		case s.Body.Clipped:
			pd.Comment = clipped
		}
		if !s.Body.Base64 && !s.Body.Clipped {
			pd.Params = formParams(pd.MimeType, pd.Text)
		}
		r.PostData = pd
	}
	return r
}

func historyResponse(s *history.ResponseSnapshot, clipped string) Response {
	r := Response{
		Status:      s.StatusCode,
		StatusText:  statusText(s.StatusCode, s.Status),
		HTTPVersion: httpVersion(s.Proto),
		Headers:     nameValues(s.Headers),
		Cookies:     responseCookies(s.Headers),
		HeadersSize: -1,
		BodySize:    s.Body.Size,
		Content: Content{
			Size:     s.Body.Size,
			MimeType: s.ContentType,
			Text:     s.Body.Text,
		},
	}
	if r.Content.MimeType == "" {
		r.Content.MimeType = unknownMime
	}
	if s.Body.Base64 {
		r.Content.Encoding = "base64"
	}
	switch {
	case s.Body.Clipped:
		r.Content.Comment = clipped
	case s.Truncated:
		r.Content.Comment = "The body was cut at the client's size limit."
	}
	if s.StatusCode >= 300 && s.StatusCode < 400 {
		r.RedirectURL = headerValue(s.Headers, "Location")
	}
	return r
}

// noResponse is the response of an entry that got none.
func noResponse() Response {
	return Response{
		Cookies: []Cookie{}, Headers: []NameValue{},
		Content:     Content{MimeType: unknownMime},
		HeadersSize: -1, BodySize: -1,
	}
}

// historyTimings maps the recorded phases: connect includes ssl, as HAR
// 1.2 has it; what no phase covers (preparation, the pool, earlier hops)
// is blocked. Without recorded phases the whole duration is wait.
func historyTimings(t *history.Timings, durationMs int64) Timings {
	if t == nil {
		return Timings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1, Wait: float64(durationMs)}
	}
	nonNeg := func(v float64) float64 { return max(v, 0) }
	out := Timings{
		DNS:     t.DNS,
		Connect: t.Connect,
		SSL:     t.TLS,
		Send:    nonNeg(t.Send),
		Wait:    nonNeg(t.Wait),
		Receive: nonNeg(t.Receive),
	}
	if out.Connect >= 0 && out.SSL > 0 {
		out.Connect = round(out.Connect + out.SSL)
	} else if out.Connect < 0 && out.SSL >= 0 {
		out.Connect = out.SSL
	}
	phases := nonNeg(out.DNS) + nonNeg(out.Connect) + out.Send + out.Wait + out.Receive
	out.Blocked = -1
	if rest := round(t.Total - phases); rest > 0 {
		out.Blocked = rest
	}
	return out
}

// total is an entry's time: the sum of its non-negative phases, ssl
// excluded (it is in connect).
func total(t Timings) float64 {
	sum := 0.0
	for _, v := range []float64{t.Blocked, t.DNS, t.Connect, t.Send, t.Wait, t.Receive} {
		if v > 0 {
			sum += v
		}
	}
	return round(sum)
}

// round keeps three decimals (microseconds).
func round(v float64) float64 { return math.Round(v*1000) / 1000 }

// FromRequest returns the HAR entry of a request as PostSonde would send
// it (snippet.FromBuild, masked or not by the caller), not sent: status 0
// and zero timings, started at started. comment is written as the entry's
// comment (the request's name).
func FromRequest(r snippet.Request, started time.Time, comment string) Entry {
	req := Request{
		Method:      r.Method,
		URL:         r.URL,
		HTTPVersion: "HTTP/1.1",
		HeadersSize: -1,
		BodySize:    0,
		QueryString: queryString(r.URL),
	}
	if r.HTTPVersion == httpclient.HTTPVersion2 {
		req.HTTPVersion = "HTTP/2.0"
	}
	headers := make([]history.Header, 0, len(r.Headers)+2)
	for _, h := range r.Headers {
		headers = append(headers, history.Header{Name: h.Name, Value: h.Value})
	}
	if r.Basic != nil {
		v := "Basic " + base64.StdEncoding.EncodeToString([]byte(r.Basic.Username+":"+r.Basic.Password))
		if r.Basic.Password == snippet.Mask {
			v = "Basic " + snippet.Mask
		}
		headers = append(headers, history.Header{Name: "Authorization", Value: v})
	}
	if r.Cookies != "" {
		headers = append(headers, history.Header{Name: "Cookie", Value: r.Cookies})
	}
	switch r.Body.Kind {
	case snippet.BodyData:
		mt := headerValue(headers, "Content-Type")
		if mt == "" && r.Body.ContentType != "" {
			mt = r.Body.ContentType
			headers = append(headers, history.Header{Name: "Content-Type", Value: mt})
		}
		req.PostData = &PostData{MimeType: mt, Text: string(r.Body.Data), Params: formParams(mt, string(r.Body.Data))}
		req.BodySize = int64(len(r.Body.Data))
	case snippet.BodyFile:
		mt := headerValue(headers, "Content-Type")
		if mt == "" {
			mt = r.Body.ContentType
		}
		req.PostData = &PostData{MimeType: mt, Comment: "The body is sent from the file " + filepath.Base(r.Body.File) + ", which is not included."}
		req.BodySize = -1
	case snippet.BodyMultipart:
		pd := &PostData{MimeType: "multipart/form-data"}
		for _, p := range r.Body.Parts {
			if p.File != "" {
				pd.Params = append(pd.Params, Param{Name: p.Name, FileName: p.Filename, ContentType: p.ContentType})
				continue
			}
			pd.Params = append(pd.Params, Param{Name: p.Name, Value: p.Value, ContentType: p.ContentType})
		}
		req.PostData = pd
		req.BodySize = -1
	}
	req.Headers = nameValues(headers)
	req.Cookies = requestCookies(headers)
	out := Entry{
		StartedDateTime: Date(started),
		Request:         req,
		Response:        noResponse(),
		Timings:         Timings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1},
		Comment:         comment,
	}
	out.Response.Comment = "Not sent: exported from the collection."
	if r.Digest != nil {
		out.Comment = joinNote(out.Comment, "Digest auth answers the server's challenge when the request is sent; its Authorization header is not included.")
	}
	return out
}

func httpVersion(proto string) string {
	if proto == "" {
		return "HTTP/1.1"
	}
	return proto
}

// statusText is the reason phrase: status without its code ("200 OK" is
// "OK"), else the standard text of code.
func statusText(code int, status string) string {
	if rest, ok := strings.CutPrefix(status, strconv.Itoa(code)); ok {
		if t := strings.TrimSpace(rest); t != "" {
			return t
		}
	} else if status != "" {
		return status
	}
	return http.StatusText(code)
}

func nameValues(hs []history.Header) []NameValue {
	out := make([]NameValue, 0, len(hs))
	for _, h := range hs {
		out = append(out, NameValue{Name: h.Name, Value: h.Value})
	}
	return out
}

func headerValue(hs []history.Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// requestCookies returns the cookies of the Cookie fields.
func requestCookies(hs []history.Header) []Cookie {
	out := []Cookie{}
	for _, h := range hs {
		if !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		cs, err := http.ParseCookie(h.Value)
		if err != nil {
			continue
		}
		for _, c := range cs {
			out = append(out, Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return out
}

// responseCookies returns the cookies of the Set-Cookie fields.
func responseCookies(hs []history.Header) []Cookie {
	out := []Cookie{}
	for _, h := range hs {
		if !strings.EqualFold(h.Name, "Set-Cookie") {
			continue
		}
		c, err := http.ParseSetCookie(h.Value)
		if err != nil {
			continue
		}
		hc := Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, HTTPOnly: c.HttpOnly, Secure: c.Secure}
		if !c.Expires.IsZero() {
			hc.Expires = Date(c.Expires.UTC())
		}
		out = append(out, hc)
	}
	return out
}

// queryString returns the query of u as entries, in order, decoded.
func queryString(u string) []NameValue {
	out := []NameValue{}
	parsed, err := url.Parse(u)
	if err != nil || parsed.RawQuery == "" {
		return out
	}
	for _, kv := range splitForm(parsed.RawQuery) {
		out = append(out, NameValue{Name: kv[0], Value: kv[1]})
	}
	return out
}

// formParams returns the fields of a URL-encoded body, nil for another
// type.
func formParams(mimeType, text string) []Param {
	mt, _, err := mime.ParseMediaType(mimeType)
	if err != nil || mt != "application/x-www-form-urlencoded" || text == "" {
		return nil
	}
	var out []Param
	for _, kv := range splitForm(text) {
		out = append(out, Param{Name: kv[0], Value: kv[1]})
	}
	return out
}

// splitForm splits "a=1&b=2" into decoded pairs, in order; an entry that
// does not decode is kept as written.
func splitForm(s string) [][2]string {
	var out [][2]string
	for entry := range strings.SplitSeq(s, "&") {
		if entry == "" {
			continue
		}
		k, v, _ := strings.Cut(entry, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		out = append(out, [2]string{k, v})
	}
	return out
}
