package httpclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Timing says where the time of one hop went (DBG-03). Response.Timing is
// the final hop; Response.Redirects hold the earlier ones. Each phase is
// bounded by two net/http/httptrace events, read from a monotonic clock:
//
//   - DNS: DNSStart to DNSDone.
//   - Connect: the first ConnectStart to the ConnectDone that succeeded (or
//     the last one, if all failed). With several addresses (Happy Eyeballs,
//     IPv6 then IPv4) it covers every attempt until the one that worked.
//   - Proxy: the hop through a proxy tunnel (SOCKS or HTTP CONNECT) after
//     the connection to the proxy was open: an https:// proxy's TLS
//     handshake and the proxy protocol, up to the tunnel being ready
//     (netx.ProxyHop.Setup, ending at the proxy dialer's DialDone). Only
//     observed through a proxy.
//   - TLS: TLSHandshakeStart to TLSHandshakeDone (the target's TLS, end to
//     end through a tunnel).
//   - RequestWrite: GotConn to WroteRequest (the request fully written and
//     flushed), or to the first response byte if that came first.
//   - Wait (time to first byte): the end of RequestWrite to
//     GotFirstResponseByte. Zero when the first byte came first.
//   - Download: GotFirstResponseByte to the last body byte read, or to the
//     byte past Settings.MaxBodyBytes. Final hop only.
//
// The phases do not overlap, so their sum is at most Total. Total is the
// time from the start of Send to the end of Download: all hops, redirects
// included. For a hop in Response.Redirects, Total is that hop's span
// instead (Hop.Start).
type Timing struct {
	DNS          time.Duration
	Connect      time.Duration
	Proxy        time.Duration
	TLS          time.Duration
	RequestWrite time.Duration
	Wait         time.Duration
	Download     time.Duration
	Total        time.Duration
	// Observed is the set of phases whose start and end events were both
	// seen. A phase not in it is zero because it did not happen: DNS for
	// an IP address, TLS for http://, DNS, Connect and TLS on a reused
	// connection (Conn.Reused), Download on a redirect hop.
	Observed Phases
	// Offsets are when each observed phase started, from the start of
	// the hop (Hop.Start; for the final hop, the end of the last earlier
	// hop, or the start of Send). With the durations they place the
	// phases on a waterfall: the gaps between them are request
	// preparation and the pool lookup. Zero for a phase not observed.
	Offsets PhaseOffsets
}

// PhaseOffsets holds the start of each Timing phase, from the start of
// its hop.
type PhaseOffsets struct {
	DNS          time.Duration
	Connect      time.Duration
	Proxy        time.Duration
	TLS          time.Duration
	RequestWrite time.Duration
	Wait         time.Duration
	Download     time.Duration
}

// Phases is a set of Timing phases.
type Phases uint8

// The phases of a Timing.
const (
	PhaseDNS Phases = 1 << iota
	PhaseConnect
	PhaseTLS
	PhaseRequestWrite
	PhaseWait
	PhaseDownload
	// PhaseProxy is last so the values of the others stay as they were;
	// in wire order it comes after PhaseConnect.
	PhaseProxy
)

var phaseNames = [...]string{"dns", "connect", "tls", "request_write", "wait", "download", "proxy"}

// Has reports whether p holds every phase of q.
func (p Phases) Has(q Phases) bool { return p&q == q }

// String lists the phases as "dns|connect|tls", or "none".
func (p Phases) String() string {
	var names []string
	for i, n := range phaseNames {
		if p&(1<<i) != 0 {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "|")
}

// Conn describes the connection a hop used (DBG-04).
type Conn struct {
	// Reused is true when the request went over a kept-alive connection
	// (HTTP/1.1) or as a new stream on an open one (h2): no DNS, Connect or
	// TLS happened for it.
	Reused bool
	// WasIdle is true when the reused connection was idle, for IdleTime.
	// On h2 it means the connection had no other active stream.
	WasIdle  bool
	IdleTime time.Duration
	// LocalAddr and RemoteAddr are the socket's addresses, "ip:port". With
	// a proxy, RemoteAddr is the proxy's.
	LocalAddr  string
	RemoteAddr string
	// Protocol is "h2" or "HTTP/1.1" (any HTTP/1.x connection).
	Protocol string
	// Via names the proxy the connection goes through
	// ("socks5h://proxy:1080", "http://proxy:3128", no credentials;
	// netx.ViaOf), new or reused; "" for a direct one (NET-06).
	Via string
	// Proxy is the hop through a proxy of a new connection (SOCKS-12): the
	// proxy, its protocol version, authentication, DNS mode, reply and
	// timings. Nil for a direct connection and for a reused one.
	Proxy *netx.ProxyHop
	// TLS is the TLS session of an https connection, nil for http.
	TLS *TLSInfo
}

// TLSInfo describes a TLS session (DBG-05).
type TLSInfo struct {
	// Version is "TLS 1.2" or "TLS 1.3".
	Version string
	// CipherSuite is the IANA name, e.g. "TLS_AES_128_GCM_SHA256".
	CipherSuite string
	// ServerName is the SNI sent, empty for an IP address.
	ServerName string
	// NegotiatedProtocol is the ALPN result: "h2", "http/1.1" or empty.
	NegotiatedProtocol string
	// DidResume is true for a resumed session.
	DidResume bool
	// PeerCertificates is the chain the server sent, leaf first.
	PeerCertificates []CertInfo
}

// Validity of a certificate at the time of the request (CertInfo.Validity).
const (
	CertValid       = "valid"
	CertExpired     = "expired"
	CertNotYetValid = "not_yet_valid"
)

// CertInfo summarizes one certificate for display.
type CertInfo struct {
	Subject     string
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	DNSNames    []string
	IPAddresses []string
	// Validity is CertValid, CertExpired or CertNotYetValid when the TLS
	// session was described (at the request), whether or not the
	// certificate was verified.
	Validity string
	// SelfSigned is set when the certificate's issuer is its subject and
	// its own key verifies its signature.
	SelfSigned bool
}

// Hop is a request before the final one: answered with a redirect that
// Send followed, or with a 401 auth challenge that Send answered.
type Hop struct {
	// Kind is HopRedirect or HopAuth.
	Kind string
	// URL is the URL requested, without userinfo and fragment.
	URL string
	// StatusCode is the redirect or challenge status.
	StatusCode int
	// Start is when the hop started, as an offset from the start of Send.
	// Timing.Total is its span, up to the start of the next hop.
	Start  time.Duration
	Timing Timing
	Conn   Conn
	// RequestCookie is the Cookie header the hop sent, as in
	// Response.RequestCookie.
	RequestCookie string
	// Request is the request the hop sent, as written (DBG-01).
	Request *SentRequest
	// Proto, Status and Headers are the redirect or challenge response's,
	// as in Response.
	Proto   string
	Status  string
	Headers []Header
}

// TimelineEvent is one entry of the raw trace of a send, for the timeline
// (DBG-01).
type TimelineEvent struct {
	// At is the offset from the start of Send.
	At time.Duration
	// Name is one of the Event* constants.
	Name string
	// Detail is a short description: an address, an error, a count.
	Detail string
}

// Names of TimelineEvents. dial_start and dial_done come from the
// netx.Trace; redirect, body_done and events_dropped are added by Send; the
// others come from httptrace. The cookie jar adds EventCookiesSent and
// EventCookiesStored (cookies.go), Digest auth EventAuthChallenge and
// EventAuthUnanswered (authtransport.go). docs/http-client.md lists each Detail.
const (
	EventGetConn      = "get_conn"
	EventDialStart    = "dial_start"
	EventDNSStart     = "dns_start"
	EventDNSDone      = "dns_done"
	EventConnectStart = "connect_start"
	EventConnectDone  = "connect_done"
	EventDialDone     = "dial_done"
	EventTLSStart     = "tls_start"
	EventTLSDone      = "tls_done"
	EventGotConn      = "got_conn"
	EventWroteHeaders = "wrote_headers"
	EventWroteRequest = "wrote_request"
	EventGot1xx       = "got_1xx"
	EventFirstByte    = "first_byte"
	EventRedirect     = "redirect"
	EventBodyDone     = "body_done"
	EventsDropped     = "events_dropped"
	// EventProxyHop follows the dial_done of a dial through a proxy: its
	// detail is the hop (netx.ProxyHop.String): the proxy, version,
	// authentication, DNS mode, reply and timings (SOCKS-12).
	EventProxyHop = "proxy_hop"
)

// maxEvents bounds Response.Events; an events_dropped entry counts the rest.
const maxEvents = 1000

// recorder collects the timing of one Send from httptrace and netx.Trace
// hooks. Hooks may run on other goroutines (dials, the HTTP/2 read loop)
// and after Send returned (a dial Go finishes in the background), so
// everything is under mu and nothing is recorded once done.
type recorder struct {
	now   func() time.Time
	start time.Time

	mu      sync.Mutex
	done    bool
	hops    []*hopRecord // the last one is the current hop
	events  []TimelineEvent
	dropped int
	// proxyAuth is the Proxy-Authorization value net/http adds for an
	// HTTP proxy in absolute form; that field is not recorded.
	proxyAuth string
}

// hopRecord holds the event times of one hop; zero means not seen.
type hopRecord struct {
	start, end                time.Time
	getConn                   time.Time
	dnsStart, dnsDone         time.Time
	connStart, connOK, connKO time.Time
	tlsStart, tlsDone         time.Time
	proxyStart, proxyDone     time.Time
	proxy                     *netx.ProxyHop
	gotConn                   time.Time
	wroteRequest, firstByte   time.Time
	conn                      Conn
	url                       string
	statusCode                int
	// kind is the Hop.Kind of a hop before the final one.
	kind string
	// req is the request as written; kept when the hop starts over.
	req sentRecord
	// proto, status and headers are the hop's response head.
	proto, status string
	headers       []Header
}

func newRecorder(now func() time.Time, start time.Time) *recorder {
	return &recorder{now: now, start: start, hops: []*hopRecord{{start: start}}}
}

// record adds an event and applies update to the current hop.
func (r *recorder) record(name, detail string, update func(h *hopRecord, at time.Time)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	at := r.now()
	r.addEvent(at, name, detail)
	if update != nil {
		update(r.hops[len(r.hops)-1], at)
	}
}

func (r *recorder) addEvent(at time.Time, name, detail string) {
	if len(r.events) >= maxEvents {
		r.dropped++
		return
	}
	r.events = append(r.events, TimelineEvent{At: at.Sub(r.start), Name: name, Detail: detail})
}

// setup reports whether connection setup events still count for h: once
// the hop has its connection, DNS, connect and TLS events come from a dial
// it does not use.
func setup(h *hopRecord) bool { return h.gotConn.IsZero() }

// clientTrace returns the httptrace hooks of r.
func (r *recorder) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			r.record(EventGetConn, hostPort, func(h *hopRecord, at time.Time) {
				switch {
				case h.getConn.IsZero():
					h.getConn = at
				case !h.gotConn.IsZero():
					// net/http retries on another connection (a reused one
					// the server had closed): the hop starts over.
					*h = hopRecord{start: h.start, getConn: at, req: h.req}
					h.req.fields = nil
				}
			})
		},
		DNSStart: func(i httptrace.DNSStartInfo) {
			r.record(EventDNSStart, i.Host, func(h *hopRecord, at time.Time) {
				if setup(h) && h.dnsStart.IsZero() {
					h.dnsStart = at
				}
			})
		},
		DNSDone: func(i httptrace.DNSDoneInfo) {
			detail := errDetail(i.Err)
			if i.Err == nil {
				addrs := make([]string, len(i.Addrs))
				for n, a := range i.Addrs {
					addrs[n] = a.String()
				}
				detail = strings.Join(addrs, ", ")
			}
			r.record(EventDNSDone, detail, func(h *hopRecord, at time.Time) {
				if setup(h) && !h.dnsStart.IsZero() {
					h.dnsDone = at
				}
			})
		},
		ConnectStart: func(network, addr string) {
			r.record(EventConnectStart, network+" "+addr, func(h *hopRecord, at time.Time) {
				if setup(h) && h.connStart.IsZero() {
					h.connStart = at
				}
			})
		},
		ConnectDone: func(network, addr string, err error) {
			detail := network + " " + addr
			if err != nil {
				detail += ": " + err.Error()
			}
			r.record(EventConnectDone, detail, func(h *hopRecord, at time.Time) {
				switch {
				case !setup(h) || h.connStart.IsZero():
				case err != nil:
					h.connKO = at
				case h.connOK.IsZero():
					h.connOK = at
				}
			})
		},
		TLSHandshakeStart: func() {
			r.record(EventTLSStart, "", func(h *hopRecord, at time.Time) {
				if setup(h) && h.tlsStart.IsZero() {
					h.tlsStart = at
				}
			})
		},
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			detail := errDetail(err)
			if err == nil {
				detail = tls.VersionName(cs.Version)
				if cs.NegotiatedProtocol != "" {
					detail += ", " + cs.NegotiatedProtocol
				}
			}
			r.record(EventTLSDone, detail, func(h *hopRecord, at time.Time) {
				if setup(h) && !h.tlsStart.IsZero() {
					h.tlsDone = at
				}
			})
		},
		GotConn: func(i httptrace.GotConnInfo) {
			c := Conn{Reused: i.Reused, WasIdle: i.WasIdle, IdleTime: i.IdleTime}
			if i.Conn != nil {
				c.LocalAddr = i.Conn.LocalAddr().String()
				c.RemoteAddr = i.Conn.RemoteAddr().String()
				c.Via = netx.ViaOf(i.Conn)
			}
			detail := "new"
			if c.Reused {
				detail = "reused"
				if c.WasIdle {
					detail += ", idle " + c.IdleTime.String()
				}
			}
			detail += " " + c.LocalAddr + " → " + c.RemoteAddr
			r.record(EventGotConn, detail, func(h *hopRecord, at time.Time) {
				if h.gotConn.IsZero() {
					h.gotConn = at
					h.conn = c
				}
			})
		},
		WroteHeaderField: func(key string, values []string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.done {
				return
			}
			h := r.hops[len(r.hops)-1]
			for _, v := range values {
				if r.proxyAuth != "" && v == r.proxyAuth && strings.EqualFold(key, "Proxy-Authorization") {
					continue // the proxy's credentials, not the request's
				}
				h.req.fields = append(h.req.fields, Header{Name: key, Value: v, Enabled: true})
			}
		},
		WroteHeaders: func() { r.record(EventWroteHeaders, "", nil) },
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			r.record(EventWroteRequest, errDetail(i.Err), func(h *hopRecord, at time.Time) {
				if h.wroteRequest.IsZero() {
					h.wroteRequest = at
				}
			})
		},
		Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
			r.record(EventGot1xx, strconv.Itoa(code), nil)
			return nil
		},
		GotFirstResponseByte: func() {
			r.record(EventFirstByte, "", func(h *hopRecord, at time.Time) {
				if h.firstByte.IsZero() {
					h.firstByte = at
				}
			})
		},
	}
}

// netxTrace returns the dial hooks of r. They only add events: the phases
// come from httptrace (docs/http-client.md, "netx trace and httptrace").
func (r *recorder) netxTrace() *netx.Trace {
	return &netx.Trace{
		DialStart: func(i netx.DialStartInfo) { r.record(EventDialStart, i.Network+" "+i.Address+via(i.Via), nil) },
		DialDone: func(i netx.DialDoneInfo) {
			detail := errDetail(i.Err)
			if i.Err == nil {
				detail = i.LocalAddr.String() + " → " + i.RemoteAddr.String()
			}
			r.record(EventDialDone, detail+via(i.Via), nil)
			if i.Proxy == nil {
				return
			}
			hop := *i.Proxy
			r.record(EventProxyHop, hop.String(), func(h *hopRecord, at time.Time) {
				if i.Err != nil || !setup(h) || h.proxy != nil {
					return
				}
				h.proxy = &hop
				if !hop.Forward || hop.TLS > 0 {
					h.proxyStart, h.proxyDone = at.Add(-hop.Setup()), at
				}
			})
		},
	}
}

// via is the " via proxy" suffix of a dial event through a proxy.
func via(proxy string) string {
	if proxy == "" {
		return ""
	}
	return " via " + proxy
}

// forwardProxy notes that the current hop goes to the HTTP proxy via in
// absolute form: the recorded request-target becomes target, the
// proxy's Proxy-Authorization value auth is not recorded, and an
// EventProxy names the proxy.
func (r *recorder) forwardProxy(via, target, auth string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	if auth != "" {
		r.proxyAuth = auth
	}
	r.hops[len(r.hops)-1].req.target = target
	r.addEvent(r.now(), EventProxy, via+", absolute-form request")
}

// redirect ends the current hop with the redirect response resp and starts
// the hop of next.
func (r *recorder) redirect(resp *http.Response, next *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	at := r.now()
	r.addEvent(at, EventRedirect, strconv.Itoa(resp.StatusCode)+" → "+next.URL.Host)
	r.endHop(resp, at)
	r.hops[len(r.hops)-1].kind = HopRedirect
	r.hops = append(r.hops, &hopRecord{start: at})
}

// authChallenge ends the current hop with the challenge response resp
// and starts the hop that answers it.
func (r *recorder) authChallenge(resp *http.Response, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	at := r.now()
	r.addEvent(at, EventAuthChallenge, detail)
	r.endHop(resp, at)
	r.hops[len(r.hops)-1].kind = HopAuth
	r.hops = append(r.hops, &hopRecord{start: at})
}

// request starts recording the request of the current hop: req as the
// transport gets it, its body read through body (nil for none).
func (r *recorder) request(req *http.Request, body *bodyCapture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	h := r.hops[len(r.hops)-1]
	h.req = sentRecord{method: req.Method, url: displayURL(req.URL), target: req.URL.RequestURI(), body: body}
}

func (r *recorder) endHop(resp *http.Response, at time.Time) {
	h := r.hops[len(r.hops)-1]
	h.end = at
	h.statusCode = resp.StatusCode
	h.proto, h.status = resp.Proto, resp.Status
	h.headers = headerList(resp.Header)
	if resp.Request != nil {
		h.url = displayURL(resp.Request.URL)
	}
	h.conn.Protocol = "HTTP/1.1"
	if resp.ProtoMajor == 2 {
		h.conn.Protocol = "h2"
	}
	if !h.conn.Reused {
		h.conn.Proxy = h.proxy
	}
	h.conn.TLS = tlsInfo(resp.TLS)
}

// finish ends the send once the body of resp is read: size bytes, cut at
// the cap if truncated. Hooks called later are ignored.
func (r *recorder) finish(resp *http.Response, size int64, truncated bool) (Timing, Conn, []Hop, []TimelineEvent, *SentRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	detail := strconv.FormatInt(size, 10) + " bytes"
	if truncated {
		detail += ", cut at the body limit"
	}
	r.addEvent(at, EventBodyDone, detail)
	if r.dropped > 0 {
		r.events = append(r.events, TimelineEvent{At: at.Sub(r.start), Name: EventsDropped, Detail: strconv.Itoa(r.dropped)})
	}
	r.done = true
	r.endHop(resp, at)

	last := len(r.hops) - 1
	var hops []Hop
	for _, h := range r.hops[:last] {
		t := h.timing(time.Time{})
		t.Total = h.end.Sub(h.start)
		hops = append(hops, Hop{
			Kind: h.kind, URL: h.url, StatusCode: h.statusCode, Start: h.start.Sub(r.start), Timing: t, Conn: h.conn,
			Request: h.req.request(), Proto: h.proto, Status: h.status, Headers: h.headers,
		})
	}
	final := r.hops[last]
	t := final.timing(at)
	t.Total = at.Sub(r.start)
	return t, final.conn, hops, r.events, final.req.request()
}

// close stops recording, for a Send that failed.
func (r *recorder) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
}

// timing computes the phases of h; bodyEnd is the end of Download, zero
// for a redirect hop.
func (h *hopRecord) timing(bodyEnd time.Time) Timing {
	var t Timing
	phase := func(p Phases, d, off *time.Duration, from, to time.Time) {
		if !from.IsZero() && !to.IsZero() {
			*d = max(to.Sub(from), 0)
			*off = max(from.Sub(h.start), 0)
			t.Observed |= p
		}
	}
	o := &t.Offsets
	if !h.conn.Reused {
		// On a reused connection, any setup events came from a dial started
		// for this hop but not used by it.
		phase(PhaseDNS, &t.DNS, &o.DNS, h.dnsStart, h.dnsDone)
		connEnd := h.connOK
		if connEnd.IsZero() {
			connEnd = h.connKO
		}
		phase(PhaseConnect, &t.Connect, &o.Connect, h.connStart, connEnd)
		phase(PhaseProxy, &t.Proxy, &o.Proxy, h.proxyStart, h.proxyDone)
		phase(PhaseTLS, &t.TLS, &o.TLS, h.tlsStart, h.tlsDone)
	}
	writeEnd := h.wroteRequest
	if writeEnd.IsZero() || (!h.firstByte.IsZero() && h.firstByte.Before(writeEnd)) {
		writeEnd = h.firstByte
	}
	phase(PhaseRequestWrite, &t.RequestWrite, &o.RequestWrite, h.gotConn, writeEnd)
	phase(PhaseWait, &t.Wait, &o.Wait, writeEnd, h.firstByte)
	phase(PhaseDownload, &t.Download, &o.Download, h.firstByte, bodyEnd)
	return t
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func tlsInfo(cs *tls.ConnectionState) *TLSInfo {
	if cs == nil {
		return nil
	}
	info := &TLSInfo{
		Version:            tls.VersionName(cs.Version),
		CipherSuite:        tls.CipherSuiteName(cs.CipherSuite),
		ServerName:         cs.ServerName,
		NegotiatedProtocol: cs.NegotiatedProtocol,
		DidResume:          cs.DidResume,
	}
	now := time.Now()
	for _, c := range cs.PeerCertificates {
		info.PeerCertificates = append(info.PeerCertificates, certInfo(c, now))
	}
	return info
}

// certInfo summarizes c, with its validity at now.
func certInfo(c *x509.Certificate, now time.Time) CertInfo {
	ips := make([]string, len(c.IPAddresses))
	for i, ip := range c.IPAddresses {
		ips[i] = ip.String()
	}
	return CertInfo{
		Subject:     c.Subject.String(),
		Issuer:      c.Issuer.String(),
		NotBefore:   c.NotBefore,
		NotAfter:    c.NotAfter,
		DNSNames:    slices.Clone(c.DNSNames),
		IPAddresses: ips,
		Validity:    certValidity(c, now),
		SelfSigned:  selfSigned(c),
	}
}

// certValidity is CertValid, CertExpired or CertNotYetValid for c at now.
func certValidity(c *x509.Certificate, now time.Time) string {
	switch {
	case now.Before(c.NotBefore):
		return CertNotYetValid
	case now.After(c.NotAfter):
		return CertExpired
	}
	return CertValid
}

// selfSigned reports whether c is issued by itself: the same subject and
// issuer, and a signature its own key verifies (CheckSignatureFrom would
// also demand a CA, which a self-signed leaf often is not).
func selfSigned(c *x509.Certificate) bool {
	if !bytes.Equal(c.RawSubject, c.RawIssuer) {
		return false
	}
	return c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

// NewTLSInfo describes the TLS session cs, for clients that do their own
// TLS handshake (the WebSocket client). It returns nil for nil.
func NewTLSInfo(cs *tls.ConnectionState) *TLSInfo { return tlsInfo(cs) }
