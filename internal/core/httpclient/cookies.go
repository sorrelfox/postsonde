package httpclient

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// CookieJar is the cookie store a Client uses (CK-01): internal/core/cookies
// implements it. A Client has none unless WithCookieJar sets one.
type CookieJar interface {
	// Cookies returns the cookies to send to u, in Cookie header order.
	// Only Name and Value are used; a cookie with an empty name is sent as
	// its value alone.
	Cookies(u *url.URL) []*http.Cookie
	// SetCookieLines stores the raw Set-Cookie field values of a response
	// to u. It returns the names stored and, for cookies it ignored,
	// "name: reason". Neither may contain values.
	SetCookieLines(u *url.URL, lines []string) (stored, rejected []string)
}

// Timeline events of the cookie jar, only recorded when the Client has one.
const (
	// EventCookiesSent: names of the jar's cookies added to a hop's
	// request.
	EventCookiesSent = "cookies_sent"
	// EventCookiesStored: names of the cookies a hop's response stored and
	// of those the jar ignored, with the reason.
	EventCookiesStored = "cookies_stored"
)

// WithCookieJar returns a Client that sends and stores cookies with jar,
// sharing c's connections. A nil jar gives a Client without cookie jar.
func (c *Client) WithCookieJar(jar CookieJar) *Client {
	cp := *c
	cp.jar = jar
	return &cp
}

// cookieTransport is the RoundTripper of the http.Client. It sees every hop,
// the first request and each redirect, after net/http copied the user's
// headers onto it:
//
//   - it starts recording the hop's request for Response.Request and
//     Hop.Request, reading the body through a capture (sent.go), and adds
//     the hop's timing hooks to its context (recorder.request);
//   - it records the Cookie header each hop sends;
//   - with a jar, it adds the jar's cookies to the request (a copy) and
//     stores the response's Set-Cookie values before net/http follows a
//     redirect, so the next hop already sends them; SkipSendingCookies and
//     SkipStoringCookies turn either off for the send (HTTP-26).
type cookieTransport struct {
	base http.RoundTripper
}

func (t *cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	st, _ := req.Context().Value(stateKey{}).(*sendState)
	if st == nil {
		return t.base.RoundTrip(req)
	}
	if st.jar != nil && !st.settings.SkipSendingCookies {
		req = addJarCookies(req, st)
	}
	st.addCookieHeader(cookieHeader(req.Header))
	if st.rec != nil {
		var body *bodyCapture
		req, body = captureBody(req)
		req = st.rec.request(req, body)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil || st.jar == nil {
		return resp, err
	}
	if lines := resp.Header.Values("Set-Cookie"); len(lines) > 0 {
		if st.settings.SkipStoringCookies {
			if st.rec != nil {
				st.rec.record(EventCookiesStored, "none stored, off for this request: "+setCookieNames(lines), nil)
			}
			return resp, nil
		}
		stored, rejected := st.jar.SetCookieLines(req.URL, lines)
		if st.rec != nil {
			st.rec.record(EventCookiesStored, cookiesStoredDetail(stored, rejected), nil)
		}
	}
	return resp, nil
}

// addJarCookies returns req with the jar's cookies for its URL added to the
// Cookie header, or req itself when there are none. A cookie whose name a
// Cookie header row already sends is left out: the user's row wins. The
// cookies are appended to the first Cookie field (in the spelling of the
// row), or sent as a new "Cookie" field.
func addJarCookies(req *http.Request, st *sendState) *http.Request {
	jarCookies := st.jar.Cookies(req.URL)
	if len(jarCookies) == 0 {
		return req
	}
	key := ""
	userNames := map[string]bool{}
	for name, values := range req.Header {
		if !strings.EqualFold(name, "Cookie") {
			continue
		}
		if key == "" || name < key {
			key = name
		}
		for _, v := range values {
			for pair := range strings.SplitSeq(v, ";") {
				if n, _, _ := strings.Cut(pair, "="); strings.TrimSpace(n) != "" {
					userNames[strings.TrimSpace(n)] = true
				}
			}
		}
	}
	var added []*http.Cookie
	var names []string
	for _, c := range jarCookies {
		if userNames[c.Name] {
			continue
		}
		added = append(added, c)
		names = append(names, cookieName(c.Name))
	}
	if st.rec != nil && len(names) > 0 {
		st.rec.record(EventCookiesSent, strings.Join(names, ", "), nil)
	}
	if len(added) == 0 {
		return req
	}
	value := serializeCookies(added)
	r := new(http.Request)
	*r = *req
	r.Header = req.Header.Clone()
	if key == "" {
		r.Header["Cookie"] = []string{value}
	} else if values := r.Header[key]; strings.Trim(values[0], "; \t") == "" {
		values[0] = value
	} else {
		values[0] = strings.TrimRight(values[0], "; \t") + "; " + value
	}
	return r
}

// serializeCookies writes cookies as a Cookie header value, byte for byte.
func serializeCookies(cookies []*http.Cookie) string {
	var b strings.Builder
	for i, c := range cookies {
		if i > 0 {
			b.WriteString("; ")
		}
		if c.Name != "" {
			b.WriteString(c.Name)
			b.WriteByte('=')
		}
		b.WriteString(c.Value)
	}
	return b.String()
}

// cookieHeader returns the Cookie fields of h joined by "; ", in any
// spelling.
func cookieHeader(h http.Header) string {
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(h)) {
		if strings.EqualFold(name, "Cookie") {
			parts = append(parts, h[name]...)
		}
	}
	return strings.Join(parts, "; ")
}

func cookieName(name string) string {
	if name == "" {
		return `""`
	}
	return name
}

// setCookieNames returns the cookie names of Set-Cookie values, never
// their values, joined by ", ".
func setCookieNames(lines []string) string {
	names := make([]string, len(lines))
	for i, l := range lines {
		pair, _, _ := strings.Cut(l, ";")
		name, _, found := strings.Cut(pair, "=")
		if !found {
			// A nameless cookie (RFC 6265 section 5.2): the pair is its value.
			name = ""
		}
		names[i] = cookieName(strings.TrimSpace(name))
	}
	return strings.Join(names, ", ")
}

func cookiesStoredDetail(stored, rejected []string) string {
	var parts []string
	if len(stored) > 0 {
		names := make([]string, len(stored))
		for i, n := range stored {
			names[i] = cookieName(n)
		}
		parts = append(parts, "stored "+strings.Join(names, ", "))
	}
	if len(rejected) > 0 {
		parts = append(parts, "ignored "+strings.Join(rejected, ", "))
	}
	return strings.Join(parts, "; ")
}
