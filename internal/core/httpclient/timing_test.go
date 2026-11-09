package httpclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// --- recorder arithmetic, with a fake clock ---

type fakeClock struct{ t time.Time }

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (c *fakeClock) now() time.Time { return c.t }

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func fakeResponse(status, protoMajor int, rawURL string) *http.Response {
	u, _ := url.Parse(rawURL)
	return &http.Response{StatusCode: status, ProtoMajor: protoMajor, Request: &http.Request{URL: u}}
}

const allPhases = PhaseDNS | PhaseConnect | PhaseTLS | PhaseRequestWrite | PhaseWait | PhaseDownload

// TestRecorderPhases checks the phase arithmetic on scripted hook calls:
// each step sets the clock (in ms after the start of Send), then calls one
// hook of the current hop's request (after a redirect, the next one's).
func TestRecorderPhases(t *testing.T) {
	t.Parallel()

	type step = recStep
	getConn := func(tr *httptrace.ClientTrace, _ *recorder) { tr.GetConn("example.com:443") }
	dnsStart := func(tr *httptrace.ClientTrace, _ *recorder) { tr.DNSStart(httptrace.DNSStartInfo{Host: "example.com"}) }
	dnsDone := func(tr *httptrace.ClientTrace, _ *recorder) {
		tr.DNSDone(httptrace.DNSDoneInfo{Addrs: []net.IPAddr{{IP: net.IPv6loopback}, {IP: net.IPv4(127, 0, 0, 1)}}})
	}
	connStart := func(addr string) func(*httptrace.ClientTrace, *recorder) {
		return func(tr *httptrace.ClientTrace, _ *recorder) { tr.ConnectStart("tcp", addr) }
	}
	connDone := func(addr string, err error) func(*httptrace.ClientTrace, *recorder) {
		return func(tr *httptrace.ClientTrace, _ *recorder) { tr.ConnectDone("tcp", addr, err) }
	}
	tlsStart := func(tr *httptrace.ClientTrace, _ *recorder) { tr.TLSHandshakeStart() }
	tlsDone := func(tr *httptrace.ClientTrace, _ *recorder) {
		tr.TLSHandshakeDone(tls.ConnectionState{Version: tls.VersionTLS13}, nil)
	}
	gotConn := func(reused bool) func(*httptrace.ClientTrace, *recorder) {
		return func(tr *httptrace.ClientTrace, _ *recorder) { tr.GotConn(httptrace.GotConnInfo{Reused: reused}) }
	}
	wrote := func(tr *httptrace.ClientTrace, _ *recorder) { tr.WroteRequest(httptrace.WroteRequestInfo{}) }
	firstByte := func(tr *httptrace.ClientTrace, _ *recorder) { tr.GotFirstResponseByte() }
	redirect := func(_ *httptrace.ClientTrace, r *recorder) {
		next, _ := url.Parse("https://example.com/to")
		r.redirect(fakeResponse(302, 1, "https://example.com/from"), &http.Request{URL: next})
	}
	// A hook of the first hop's request that runs after its redirect: h2
	// writes a request on a goroutine of its own, and the answer can come
	// first.
	lateWrote := func(_ *httptrace.ClientTrace, r *recorder) {
		r.clientTrace(r.hops[0]).WroteRequest(httptrace.WroteRequestInfo{})
	}
	refused := errors.New("connection refused")

	newConn := []step{
		{1, getConn}, {1, dnsStart}, {3, dnsDone}, {3, connStart("[::1]:443")}, {7, connDone("[::1]:443", nil)},
		{7, tlsStart}, {15, tlsDone}, {16, gotConn(false)}, {18, wrote}, {30, firstByte},
	}
	tests := []struct {
		name      string
		steps     []step
		end       int
		want      Timing
		reused    bool
		redirects []Timing
	}{
		{
			name:  "new TLS connection",
			steps: newConn, end: 50,
			want: Timing{DNS: ms(2), Connect: ms(4), TLS: ms(8), RequestWrite: ms(2), Wait: ms(12), Download: ms(20), Total: ms(50), Observed: allPhases,
				Offsets: PhaseOffsets{DNS: ms(1), Connect: ms(3), TLS: ms(7), RequestWrite: ms(16), Wait: ms(18), Download: ms(30)}},
		},
		{
			name: "IP address over http: no DNS, no TLS",
			steps: []step{
				{1, getConn}, {1, connStart("127.0.0.1:80")}, {2, connDone("127.0.0.1:80", nil)},
				{2, gotConn(false)}, {3, wrote}, {6, firstByte},
			},
			end: 10,
			want: Timing{Connect: ms(1), RequestWrite: ms(1), Wait: ms(3), Download: ms(4), Total: ms(10), Observed: PhaseConnect | PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{Connect: ms(1), RequestWrite: ms(2), Wait: ms(3), Download: ms(6)}},
		},
		{
			name: "Happy Eyeballs: first start to the connect that worked",
			steps: []step{
				{1, getConn}, {1, dnsStart}, {2, dnsDone},
				{2, connStart("[::1]:443")}, {3, connDone("[::1]:443", refused)},
				{3, connStart("127.0.0.1:443")}, {4, connStart("127.0.0.2:443")}, {6, connDone("127.0.0.1:443", nil)},
				{7, connDone("127.0.0.2:443", errors.New("operation was canceled"))}, // the loser, before GotConn
				{7, gotConn(false)}, {8, wrote}, {9, firstByte},
				{9, connDone("127.0.0.3:443", refused)}, // after GotConn: ignored
			},
			end: 10,
			want: Timing{DNS: ms(1), Connect: ms(4), RequestWrite: ms(1), Wait: ms(1), Download: ms(1), Total: ms(10), Observed: PhaseDNS | PhaseConnect | PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{DNS: ms(1), Connect: ms(2), RequestWrite: ms(7), Wait: ms(8), Download: ms(9)}},
		},
		{
			name:  "reused connection",
			steps: []step{{1, getConn}, {2, gotConn(true)}, {3, wrote}, {8, firstByte}},
			end:   10, reused: true,
			want: Timing{RequestWrite: ms(1), Wait: ms(5), Download: ms(2), Total: ms(10), Observed: PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{RequestWrite: ms(2), Wait: ms(3), Download: ms(8)}},
		},
		{
			name: "reused while a dial it started runs on",
			steps: []step{
				{1, getConn}, {1, dnsStart}, {2, dnsDone}, {2, connStart("[::1]:443")},
				{3, gotConn(true)}, {4, connDone("[::1]:443", nil)}, {4, tlsStart}, {5, wrote}, {6, tlsDone}, {7, firstByte},
			},
			end: 8, reused: true,
			want: Timing{RequestWrite: ms(2), Wait: ms(2), Download: ms(1), Total: ms(8), Observed: PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{RequestWrite: ms(3), Wait: ms(5), Download: ms(7)}},
		},
		{
			name:  "answer before the request was written",
			steps: []step{{1, getConn}, {1, gotConn(true)}, {2, firstByte}, {5, wrote}},
			end:   9, reused: true,
			want: Timing{RequestWrite: ms(1), Wait: 0, Download: ms(7), Total: ms(9), Observed: PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{RequestWrite: ms(1), Wait: ms(2), Download: ms(2)}},
		},
		{
			name: "retried on a new connection",
			steps: append([]step{{1, getConn}, {2, gotConn(true)}, {3, func(tr *httptrace.ClientTrace, _ *recorder) {
				tr.WroteRequest(httptrace.WroteRequestInfo{Err: io.EOF})
			}}}, shift(newConn, 10)...),
			end: 60,
			want: Timing{DNS: ms(2), Connect: ms(4), TLS: ms(8), RequestWrite: ms(2), Wait: ms(12), Download: ms(20), Total: ms(60), Observed: allPhases,
				Offsets: PhaseOffsets{DNS: ms(11), Connect: ms(13), TLS: ms(17), RequestWrite: ms(26), Wait: ms(28), Download: ms(40)}},
		},
		{
			name:  "redirect, then a reused connection",
			steps: append(slices.Clone(newConn), step{35, redirect}, step{36, getConn}, step{36, gotConn(true)}, step{37, wrote}, step{40, firstByte}),
			end:   45, reused: true,
			want: Timing{RequestWrite: ms(1), Wait: ms(3), Download: ms(5), Total: ms(45), Observed: PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{RequestWrite: ms(1), Wait: ms(2), Download: ms(5)}},
			redirects: []Timing{{
				DNS: ms(2), Connect: ms(4), TLS: ms(8), RequestWrite: ms(2), Wait: ms(12), Total: ms(35),
				Observed: PhaseDNS | PhaseConnect | PhaseTLS | PhaseRequestWrite | PhaseWait,
				Offsets:  PhaseOffsets{DNS: ms(1), Connect: ms(3), TLS: ms(7), RequestWrite: ms(16), Wait: ms(18)},
			}},
		},
		{
			name: "the redirected request's WroteRequest after the redirect",
			steps: append(slices.Clone(newConn[:8]),
				step{30, firstByte}, step{35, redirect}, step{35, lateWrote},
				step{36, getConn}, step{36, gotConn(true)}, step{37, wrote}, step{40, firstByte}),
			end: 45, reused: true,
			want: Timing{RequestWrite: ms(1), Wait: ms(3), Download: ms(5), Total: ms(45), Observed: PhaseRequestWrite | PhaseWait | PhaseDownload,
				Offsets: PhaseOffsets{RequestWrite: ms(1), Wait: ms(2), Download: ms(5)}},
			redirects: []Timing{{
				DNS: ms(2), Connect: ms(4), TLS: ms(8), RequestWrite: ms(14), Wait: 0, Total: ms(35),
				Observed: PhaseDNS | PhaseConnect | PhaseTLS | PhaseRequestWrite | PhaseWait,
				Offsets:  PhaseOffsets{DNS: ms(1), Connect: ms(3), TLS: ms(7), RequestWrite: ms(16), Wait: ms(30)},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clk := &fakeClock{t: epoch}
			r := newRecorder(clk.now, epoch)
			hop := r.hops[0]
			tr := r.clientTrace(hop)
			for _, s := range tt.steps {
				clk.t = epoch.Add(ms(s.at))
				s.call(tr, r)
				// The next request carries the hooks of the hop it starts.
				if h := r.hops[len(r.hops)-1]; h != hop {
					hop, tr = h, r.clientTrace(h)
				}
			}
			clk.t = epoch.Add(ms(tt.end))
			got, conn, hops, events, _ := r.finish(fakeResponse(200, 1, "https://example.com/to"), 2, false)
			if got != tt.want {
				t.Errorf("Timing =\n %+v (%v), want\n %+v (%v)", got, got.Observed, tt.want, tt.want.Observed)
			}
			if conn.Reused != tt.reused || conn.Protocol != "HTTP/1.1" {
				t.Errorf("Conn = %+v, want Reused %v, HTTP/1.1", conn, tt.reused)
			}
			var hopTimings []Timing
			for i, h := range hops {
				hopTimings = append(hopTimings, h.Timing)
				if h.StatusCode != 302 || h.URL != "https://example.com/from" || h.Start != 0 || h.Conn.Reused {
					t.Errorf("hop %d = %+v", i, h)
				}
			}
			if !slices.Equal(hopTimings, tt.redirects) {
				t.Errorf("redirect timings =\n %+v, want\n %+v", hopTimings, tt.redirects)
			}
			if len(events) != len(tt.steps)+1 || events[len(events)-1].Name != EventBodyDone || events[len(events)-1].At != ms(tt.end) {
				t.Errorf("events = %+v, want one per step, then body_done at %v", events, ms(tt.end))
			}
		})
	}
}

// recStep sets the fake clock to at ms after the start, then calls a hook.
type recStep struct {
	at   int
	call func(tr *httptrace.ClientTrace, r *recorder)
}

// shift returns steps moved by ms later.
func shift(steps []recStep, by int) []recStep {
	out := slices.Clone(steps)
	for i := range out {
		out[i].at += by
	}
	return out
}

// TestRecorderEvents checks that nothing is recorded after the send ended,
// and the event cap.
func TestRecorderEvents(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: epoch}
	r := newRecorder(clk.now, epoch)
	tr, nt := r.clientTrace(r.hops[0]), r.netxTrace(r.hops[0])
	for range maxEvents + 5 {
		tr.WroteHeaders()
	}
	_, _, _, events, _ := r.finish(fakeResponse(200, 2, "https://example.com/"), 0, false)
	if len(events) != maxEvents+1 || events[maxEvents].Name != EventsDropped || events[maxEvents].Detail != "6" {
		t.Fatalf("%d events, last %+v, want %d then events_dropped 6", len(events), events[len(events)-1], maxEvents)
	}
	tr.TLSHandshakeDone(tls.ConnectionState{}, io.EOF)
	nt.DialStart(netx.DialStartInfo{Network: "tcp", Address: "x:1"})
	if n := len(r.events); n != maxEvents+1 {
		t.Errorf("%d events after finish, want no more", n)
	}

	r = newRecorder(clk.now, epoch)
	r.close()
	r.clientTrace(r.hops[0]).GetConn("x:1")
	if len(r.events) != 0 {
		t.Errorf("events after close = %+v", r.events)
	}
}

func TestPhasesString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		p    Phases
		want string
	}{
		{0, "none"},
		{PhaseDNS | PhaseTLS, "dns|tls"},
		{allPhases, "dns|connect|tls|request_write|wait|download"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

// --- against real servers ---

// localhostCert is a self-signed certificate for localhost, 127.0.0.1 and
// ::1 (httptest's does not cover "localhost").
var localhostCert = sync.OnceValues(func() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost", Organization: []string{"PostSonde test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
})

// newLocalTLSServer starts a TLS server on 127.0.0.1 with localhostCert,
// with HTTP/2 when h2 is set.
func newLocalTLSServer(t *testing.T, h http.Handler, h2 bool) *httptest.Server {
	t.Helper()
	cert, err := localhostCert()
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = h2
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// checkPhases checks that no phase is negative and that they fit in Total.
func checkPhases(t *testing.T, tm Timing) {
	t.Helper()
	phases := []time.Duration{tm.DNS, tm.Connect, tm.TLS, tm.RequestWrite, tm.Wait, tm.Download}
	var sum time.Duration
	for _, d := range phases {
		if d < 0 {
			t.Errorf("negative phase in %+v", tm)
		}
		sum += d
	}
	if !testenv.Measured(tm.Total) || sum > tm.Total {
		t.Errorf("phases sum to %v, Total %v: %+v", sum, tm.Total, tm)
	}
	// The observed phases follow one another: each starts no earlier than
	// the previous one ended, and the last ends within Total.
	o := tm.Offsets
	offsets := []time.Duration{o.DNS, o.Connect, o.TLS, o.RequestWrite, o.Wait, o.Download}
	var end time.Duration
	for i, d := range phases {
		if !tm.Observed.Has(1 << i) {
			if offsets[i] != 0 {
				t.Errorf("phase %v not observed but has offset %v", Phases(1<<i), offsets[i])
			}
			continue
		}
		if offsets[i] < end {
			t.Errorf("phase %v starts at %v, before the previous phase ended at %v: %+v", Phases(1<<i), offsets[i], end, tm)
		}
		end = offsets[i] + d
	}
	if end > tm.Total {
		t.Errorf("phases end at %v, after Total %v", end, tm.Total)
	}
}

// checkEventOrder checks that names occur in events in this order.
func checkEventOrder(t *testing.T, events []TimelineEvent, names ...string) {
	t.Helper()
	i := 0
	for _, e := range events {
		if i < len(names) && e.Name == names[i] {
			i++
		}
	}
	if i < len(names) {
		t.Errorf("event %q missing or out of order in %+v", names[i], events)
	}
}

func protocols(h2 bool) (proto, alpn string) {
	if h2 {
		return "h2", "h2"
	}
	return "HTTP/1.1", "http/1.1"
}

// TestTiming sends two requests over one client: the first opens a
// connection and reports every phase, the second reuses it.
func TestTiming(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		h2   bool
	}{
		{"HTTP/1.1, host name", "localhost", false},
		{"h2, host name", "localhost", true},
		{"HTTP/1.1, IP address", "127.0.0.1", false},
		{"h2, IP address", "127.0.0.1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var peer atomic.Value
			srv := newLocalTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peer.Store(r.RemoteAddr)
				io.WriteString(w, "hello")
			}), tt.h2)
			port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
			c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})
			req := get("https://" + net.JoinHostPort(tt.host, port) + "/")
			proto, alpn := protocols(tt.h2)
			dns := tt.host == "localhost"

			first, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("first Send: %v", err)
			}
			checkPhases(t, first.Timing)
			tm := first.Timing
			t.Logf("first: DNS %v, connect %v, TLS %v, write %v, wait %v, download %v, total %v",
				tm.DNS, tm.Connect, tm.TLS, tm.RequestWrite, tm.Wait, tm.Download, tm.Total)
			want := allPhases
			if !dns {
				want &^= PhaseDNS
			}
			if got := first.Timing.Observed; got != want {
				t.Errorf("first request observed %v, want %v", got, want)
			}
			if !dns && first.Timing.DNS != 0 {
				t.Errorf("DNS = %v for an IP address", first.Timing.DNS)
			}
			if first.Duration != first.Timing.Total {
				t.Errorf("Duration %v != Timing.Total %v", first.Duration, first.Timing.Total)
			}
			conn := first.Conn
			if conn.Reused || conn.WasIdle || conn.Protocol != proto {
				t.Errorf("first Conn = %+v, want new, %s", conn, proto)
			}
			if conn.RemoteAddr != srv.Listener.Addr().String() || conn.LocalAddr != peer.Load() {
				t.Errorf("addresses %s → %s, want %s → %s", conn.LocalAddr, conn.RemoteAddr, peer.Load(), srv.Listener.Addr())
			}
			wantSNI := ""
			if dns {
				wantSNI = "localhost"
			}
			if ti := conn.TLS; ti == nil || ti.Version != "TLS 1.3" || ti.NegotiatedProtocol != alpn || ti.ServerName != wantSNI ||
				ti.CipherSuite == "" || len(ti.PeerCertificates) != 1 {
				t.Fatalf("TLS = %+v, want TLS 1.3, ALPN %s, SNI %q, one certificate", ti, alpn, wantSNI)
			}
			if cert := conn.TLS.PeerCertificates[0]; !slices.Equal(cert.DNSNames, []string{"localhost"}) ||
				!slices.Equal(cert.IPAddresses, []string{"127.0.0.1", "::1"}) || !strings.Contains(cert.Subject, "CN=localhost") ||
				cert.Subject != cert.Issuer || !cert.NotBefore.Before(cert.NotAfter) {
				t.Errorf("certificate = %+v", cert)
			}
			order := []string{EventGetConn, EventDialStart}
			if dns {
				order = append(order, EventDNSStart, EventDNSDone)
			}
			order = append(order, EventConnectStart, EventConnectDone, EventDialDone, EventTLSStart, EventTLSDone,
				EventGotConn, EventFirstByte, EventBodyDone)
			checkEventOrder(t, first.Events, order...)

			second, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("second Send: %v", err)
			}
			checkPhases(t, second.Timing)
			if got, want := second.Timing.Observed, PhaseRequestWrite|PhaseWait|PhaseDownload; got != want {
				t.Errorf("second request observed %v, want %v", got, want)
			}
			if tm := second.Timing; tm.DNS != 0 || tm.Connect != 0 || tm.TLS != 0 {
				t.Errorf("second request has setup phases: %+v", tm)
			}
			c2 := second.Conn
			if !c2.Reused || c2.Protocol != proto || c2.LocalAddr != conn.LocalAddr || c2.RemoteAddr != conn.RemoteAddr ||
				c2.TLS == nil || c2.TLS.NegotiatedProtocol != alpn {
				t.Errorf("second Conn = %+v, want the first one reused", c2)
			}
			if !c2.WasIdle {
				t.Errorf("second Conn not idle before: %+v", c2)
			}
			checkEventOrder(t, second.Events, EventGetConn, EventGotConn, EventFirstByte, EventBodyDone)
			for _, e := range second.Events {
				if e.Name == EventDialStart || e.Name == EventTLSStart {
					t.Errorf("second request dialed: %+v", second.Events)
				}
			}
		})
	}
}

// TestTimingPlausible makes "plausible" concrete: a server that sleeps
// before the headers shows in Wait, a slowly streamed body in Download.
//
// The server can only sleep before the headers, since the request reached
// it before it could answer. For the body, it paces the rest only once the
// client saw the first byte: the caller's GotFirstResponseByte hook runs
// after the recorder's, so Download is at least the pacing. (Sleeping
// right after the first flush is not enough: the client may see that
// first byte after the server already started sleeping.)
func TestTimingPlausible(t *testing.T) {
	t.Parallel()

	const d = 100 * time.Millisecond
	slowHeaders := func(<-chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(d)
			io.WriteString(w, "ok")
		}
	}
	slowBody := func(clientGotFirst <-chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			select {
			case <-clientGotFirst:
			case <-time.After(5 * time.Second):
			}
			for _, part := range []string{"b", "c"} {
				time.Sleep(d)
				io.WriteString(w, part)
				w.(http.Flusher).Flush()
			}
		}
	}
	tests := []struct {
		name    string
		handler func(clientGotFirst <-chan struct{}) http.HandlerFunc
		h2      bool
		wait    time.Duration
		dl      time.Duration
	}{
		{"slow headers, HTTP/1.1", slowHeaders, false, d, 0},
		{"slow headers, h2", slowHeaders, true, d, 0},
		{"slow body, HTTP/1.1", slowBody, false, 0, 2 * d},
		{"slow body, h2", slowBody, true, 0, 2 * d},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotFirst := make(chan struct{})
			var once sync.Once
			ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
				GotFirstResponseByte: func() { once.Do(func() { close(gotFirst) }) },
			})
			srv := newLocalTLSServer(t, tt.handler(gotFirst), tt.h2)
			c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})
			resp, err := c.Send(ctx, get(srv.URL))
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			tm := resp.Timing
			checkPhases(t, tm)
			// The server can start its sleep while the client is still
			// in RequestWrite (with h2, it reads the headers before the
			// client reports the request written), so the sleep is only
			// bound to fall within RequestWrite and Wait together.
			// RequestWrite starts at GotConn, before any request byte is
			// sent. Wait itself must still carry most of the sleep.
			if tm.RequestWrite+tm.Wait < tt.wait || tm.Wait < tt.wait*9/10 || tm.Download < tt.dl {
				t.Errorf("write %v, Wait %v, Download %v, want write+wait at least %v, wait at least %v, download at least %v (%+v)",
					tm.RequestWrite, tm.Wait, tm.Download, tt.wait, tt.wait*9/10, tt.dl, tm)
			}
			if tm.Total < tt.wait+tt.dl {
				t.Errorf("Total %v < %v", tm.Total, tt.wait+tt.dl)
			}
			t.Logf("%s: DNS %v, connect %v, TLS %v, write %v, wait %v, download %v, total %v",
				tt.name, tm.DNS, tm.Connect, tm.TLS, tm.RequestWrite, tm.Wait, tm.Download, tm.Total)
		})
	}
}

// TestTimingBodyCap checks that Download ends at the body limit: the server
// never finishes the body.
func TestTimingBodyCap(t *testing.T) {
	t.Parallel()

	for _, h2 := range []bool{false, true} {
		proto, _ := protocols(h2)
		t.Run(proto, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			srv := newLocalTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, strings.Repeat("x", 100))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}), h2)
			t.Cleanup(func() { close(release) }) // before srv.Close
			c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})
			req := get(srv.URL)
			req.Settings.MaxBodyBytes = 10
			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if !resp.BodyTruncated {
				t.Fatal("body not truncated")
			}
			checkPhases(t, resp.Timing)
			if !resp.Timing.Observed.Has(PhaseDownload | PhaseWait) {
				t.Errorf("observed %v, want wait and download", resp.Timing.Observed)
			}
			last := resp.Events[len(resp.Events)-1]
			if last.Name != EventBodyDone || !strings.Contains(last.Detail, "cut at the body limit") || last.At != resp.Timing.Total {
				t.Errorf("last event = %+v, want body_done at %v, cut", last, resp.Timing.Total)
			}
		})
	}
}

// TestTimingRedirect checks the per-hop timing: the redirect hop opens the
// connection, the final hop reuses it.
func TestTimingRedirect(t *testing.T) {
	t.Parallel()

	for _, h2 := range []bool{false, true} {
		proto, _ := protocols(h2)
		t.Run(proto, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("/from", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/to", http.StatusFound) })
			mux.HandleFunc("/to", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "done") })
			srv := newLocalTLSServer(t, mux, h2)
			c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})

			resp, err := c.Send(t.Context(), get(srv.URL+"/from"))
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if len(resp.Redirects) != 1 {
				t.Fatalf("Redirects = %+v, want one hop", resp.Redirects)
			}
			hop := resp.Redirects[0]
			if hop.URL != srv.URL+"/from" || hop.StatusCode != http.StatusFound || hop.Start != 0 {
				t.Errorf("hop = %s %d at %v, want %s/from 302 at 0", hop.URL, hop.StatusCode, hop.Start, srv.URL)
			}
			if want := PhaseConnect | PhaseTLS | PhaseRequestWrite | PhaseWait; hop.Timing.Observed != want {
				t.Errorf("hop observed %v, want %v", hop.Timing.Observed, want)
			}
			if hop.Conn.Reused || hop.Conn.Protocol != proto || hop.Conn.TLS == nil {
				t.Errorf("hop Conn = %+v, want a new %s connection", hop.Conn, proto)
			}
			if want := PhaseRequestWrite | PhaseWait | PhaseDownload; resp.Timing.Observed != want || !resp.Conn.Reused {
				t.Errorf("final hop observed %v, reused %v, want %v on the reused connection", resp.Timing.Observed, resp.Conn.Reused, want)
			}
			checkPhases(t, hop.Timing)
			checkPhases(t, resp.Timing)
			if hop.Timing.Total > resp.Timing.Total {
				t.Errorf("hop Total %v > send Total %v", hop.Timing.Total, resp.Timing.Total)
			}
			checkEventOrder(t, resp.Events, EventTLSDone, EventFirstByte, EventRedirect, EventGetConn, EventGotConn, EventFirstByte, EventBodyDone)
		})
	}
}

// TestTimingLateHooks cancels a send during the TLS handshake. Go finishes
// the handshake in the background and its hooks fire after Send returned:
// they reach the caller's trace and are ignored by the finished recorder
// (the race detector checks the locking).
func TestTimingLateHooks(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			accepted <- conn // never answers the handshake
		}
	}()

	handshakeDone := make(chan error, 1)
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) { handshakeDone <- err },
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverConn := make(chan net.Conn, 1)
	go func() {
		conn := <-accepted
		cancel() // the client is in the handshake now
		serverConn <- conn
	}()

	c := newTestClient(t, TransportOptions{})
	_, err = c.Send(ctx, get("https://"+ln.Addr().String()+"/"))
	e := wantErr(t, err, errs.Cancelled, CodeCancelled)
	if e.Details["stage"] != "connecting" {
		t.Errorf("stage = %q, want connecting", e.Details["stage"])
	}
	select {
	case conn := <-serverConn:
		conn.Close() // ends the background handshake
	case <-time.After(10 * time.Second):
		t.Fatal("the server accepted no connection")
	}
	select {
	case err := <-handshakeDone:
		if err == nil {
			t.Error("the handshake succeeded")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the caller's TLSHandshakeDone hook never fired")
	}
}
