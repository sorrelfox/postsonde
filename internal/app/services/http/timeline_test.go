package http

import (
	"crypto/tls"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// DBG-01: the timeline shows the request as sent, after variables and
// auth, every hop, with the secret variables and the API key masked.
func TestSendTimeline(t *testing.T) {
	t.Parallel()
	var received string
	mux := nethttp.NewServeMux()
	mux.HandleFunc("/start", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, "/end", nethttp.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/end", func(w nethttp.ResponseWriter, r *nethttp.Request) {
		b, _ := io.ReadAll(r.Body)
		received = r.Header.Get("X-Token") + " " + r.Header.Get("X-Api-Key") + " " + string(b)
		w.Header().Set("X-Server", "end")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws, store, dev, _ := envWorkspace(t, srv.URL)
	if err := store.SetActive(dev); err != nil {
		t.Fatal(err)
	}

	src := httpSource("{{base}}/start?tag={{tag}}")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Token", Value: "{{token}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{"t":"{{token}}","id":"{{$uuid}}"}`}
	src.Auth = projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Api-Key", Value: "k3y-{{$randomInt 1000 9999}}"}
	src.Workspace = ws
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if !strings.HasPrefix(received, "dev-secret k3y-") {
		t.Fatalf("server received %q", received)
	}
	tl := f.rec.response(t, id).Timeline
	if len(tl.Hops) != 2 || !tl.Masked || len(tl.Events) == 0 {
		t.Fatalf("timeline = %d hops, masked %v, %d events", len(tl.Hops), tl.Masked, len(tl.Events))
	}
	first, final := tl.Hops[0], tl.Hops[1]
	if first.StatusCode != 307 || !strings.HasPrefix(first.ResponseHead, "HTTP/1.1 307 Temporary Redirect\r\n") || !strings.Contains(first.ResponseHead, "\r\nLocation: /end\r\n") {
		t.Errorf("first response = %d %q", first.StatusCode, first.ResponseHead)
	}
	if first.Request.Target != "/start?tag=dev-tag" || final.Request.Target != "/end" || final.Request.Method != "POST" {
		t.Errorf("targets = %q, %q", first.Request.Target, final.Request.Target)
	}
	if final.StatusCode != 200 || !strings.Contains(final.ResponseHead, "\r\nX-Server: end\r\n") || final.StartMs < first.StartMs {
		t.Errorf("final = %+v", final)
	}

	for i, hop := range tl.Hops {
		r := hop.Request
		if strings.Contains(r.Head, "dev-secret") || strings.Contains(r.Body, "dev-secret") || strings.Contains(r.Head, "k3y-") {
			t.Errorf("hop %d shows a secret:\n%s%s", i, r.Head, r.Body)
		}
		for _, want := range []string{"\r\nX-Token: " + httpclient.Mask + "\r\n", "\r\nX-Api-Key: " + httpclient.Mask + "\r\n", "\r\nContent-Length: "} {
			if !strings.Contains(r.Head, want) {
				t.Errorf("hop %d head has no %q:\n%s", i, want, r.Head)
			}
		}
		// The dynamic value is shown as it was sent, not rendered again.
		wantBody := strings.Replace(received[strings.Index(received, "{"):], "dev-secret", httpclient.Mask, 1)
		if r.Body != wantBody || r.BodySize != int64(len(received)-strings.Index(received, "{")) || r.BodyClipped || r.BodyHex {
			t.Errorf("hop %d body = %q (%d bytes), want %q", i, r.Body, r.BodySize, wantBody)
		}
	}
}

func TestTimelineBody(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("é", MaxTimelineBody) // two bytes each
	tests := []struct {
		name    string
		body    []byte
		hex     bool
		shown   int64
		clipped bool
	}{
		{"text", []byte("hello"), false, 5, false},
		{"empty", []byte{}, false, 0, false},
		{"binary", []byte{0xff, 0x00, 0x01}, true, 3, false},
		{"long text", []byte(big), false, MaxTimelineBody, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &httpclient.SentRequest{Method: "PUT", Target: "/", Proto: "HTTP/1.1", Body: tt.body, BodySize: int64(len(tt.body))}
			got, masked := sentRequestDTO(s, &httpclient.SecretMasker{})
			if masked || got.BodyHex != tt.hex || got.BodyShown != tt.shown || got.BodyClipped != tt.clipped || got.BodySize != int64(len(tt.body)) {
				t.Errorf("got hex %v shown %d clipped %v size %d masked %v", got.BodyHex, got.BodyShown, got.BodyClipped, got.BodySize, masked)
			}
			if !tt.hex && got.Body != string(tt.body[:got.BodyShown]) {
				t.Errorf("body = %q", got.Body)
			}
			if got.Head != "PUT / HTTP/1.1\r\n\r\n" {
				t.Errorf("head = %q", got.Head)
			}
		})
	}
}

func TestTimelineChunkedAndTiming(t *testing.T) {
	t.Parallel()
	r := &httpclient.Response{
		Proto: "HTTP/2.0", StatusCode: 204, Status: "204 No Content",
		Request: &httpclient.SentRequest{
			Method: "POST", Proto: "HTTP/1.1", Target: "/",
			Headers: []httpclient.Header{{Name: "Transfer-Encoding", Value: "chunked", Enabled: true}},
			Body:    []byte{},
		},
		Redirects: []httpclient.Hop{{StatusCode: 302, Status: "302 Found", Proto: "HTTP/2.0", Start: 0, Timing: httpclient.Timing{Total: 1500 * time.Microsecond}}},
		Events:    []httpclient.TimelineEvent{{At: 1234567 * time.Nanosecond, Name: httpclient.EventFirstByte}},
	}
	tl := toTimelineDTO(r, &httpclient.SecretMasker{})
	if len(tl.Hops) != 2 || tl.Hops[0].Request.Headers == nil || tl.Hops[0].ResponseHead != "HTTP/2.0 302 Found\r\n\r\n" {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	if tl.Hops[1].StartMs != 1.5 || !tl.Hops[1].Request.BodyChunked {
		t.Errorf("final hop = %+v", tl.Hops[1])
	}
	if tl.Events[0].AtMs != 1.234 {
		t.Errorf("event at %v ms, want 1.234", tl.Events[0].AtMs)
	}
}

// DBG-03, DBG-04, DBG-05: each hop carries its phases, in order, and its
// connection; a second send reuses the connection.
func TestTimelinePhasesAndConn(t *testing.T) {
	t.Parallel()
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var peer atomic.Value
			srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				peer.Store(r.RemoteAddr)
				io.WriteString(w, "hello")
			}))
			if secure {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			t.Cleanup(srv.Close)
			f := newFixture(t, httpSource(srv.URL+"/x"), nil, WithVerifyTLS(func() bool { return false }))

			sendHop := func() TimelineHopDTO {
				t.Helper()
				id := f.send(t)
				if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
					t.Fatalf("done = %+v", done)
				}
				tl := f.rec.response(t, id).Timeline
				if len(tl.Hops) != 1 {
					t.Fatalf("%d hops, want 1", len(tl.Hops))
				}
				return tl.Hops[0]
			}

			first := sendHop()
			wantSkipped := map[string]string{PhaseDNS: SkipNoLookup, PhaseTLS: SkipNoTLS}
			if secure {
				delete(wantSkipped, PhaseTLS)
			}
			checkHopPhases(t, first.Timing, wantSkipped)
			c := first.Conn
			if c.Reused || c.Protocol != "HTTP/1.1" || c.RemoteAddr != srv.Listener.Addr().String() || c.LocalAddr != peer.Load() {
				t.Errorf("first conn = %+v, want new, HTTP/1.1, %v → %s", c, peer.Load(), srv.Listener.Addr())
			}
			if !secure && c.TLS != nil {
				t.Errorf("TLS = %+v on http", c.TLS)
			}
			if secure {
				ti := c.TLS
				if ti == nil || ti.Version != "TLS 1.3" || ti.CipherSuite == "" || ti.ServerName != "" || len(ti.Certificates) != 1 {
					t.Fatalf("TLS = %+v, want TLS 1.3, a cipher, no SNI for an IP, one certificate", ti)
				}
				cert := ti.Certificates[0]
				notBefore, err1 := time.Parse(time.RFC3339, cert.NotBefore)
				notAfter, err2 := time.Parse(time.RFC3339, cert.NotAfter)
				if err1 != nil || err2 != nil || !notBefore.Before(notAfter) || !strings.Contains(cert.Subject, "Acme Co") ||
					!slices.Contains(cert.IPAddresses, "127.0.0.1") || !slices.Contains(cert.DNSNames, "example.com") ||
					cert.Validity != httpclient.CertValid {
					t.Errorf("certificate = %+v", cert)
				}
			}

			second := sendHop()
			checkHopPhases(t, second.Timing, map[string]string{PhaseDNS: SkipReused, PhaseConnect: SkipReused, PhaseTLS: SkipReused})
			c2 := second.Conn
			if !c2.Reused || c2.LocalAddr != c.LocalAddr || c2.RemoteAddr != c.RemoteAddr || (c2.TLS != nil) != secure {
				t.Errorf("second conn = %+v, want the first one reused", c2)
			}
		})
	}
}

// DBG-05: with TLS verification off, a server whose certificate is
// expired and self-signed, or one not valid yet issued by a CA, still
// shows its TLS details and chain, each certificate marked by its
// validity at the time of the request.
func TestTimelineCertificateValidity(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca := tlstest.NewCA(t, "Validity CA")
	expired := tlstest.SelfSigned(t, tlstest.CertOptions{
		CommonName: "expired.test", NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-24 * time.Hour),
	})
	future := ca.Issue(t, tlstest.CertOptions{
		CommonName: "future.test", NotBefore: now.Add(24 * time.Hour), NotAfter: now.Add(48 * time.Hour),
	})
	future.TLS.Certificate = append(future.TLS.Certificate, ca.Cert.Raw)

	type wantCert struct {
		cn, validity string
		selfSigned   bool
	}
	for _, tc := range []struct {
		name string
		cert tls.Certificate
		want []wantCert
	}{
		{"expired self-signed", expired.TLS, []wantCert{{"expired.test", httpclient.CertExpired, true}}},
		{"not yet valid", future.TLS, []wantCert{
			{"future.test", httpclient.CertNotYetValid, false},
			{"Validity CA", httpclient.CertValid, true},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				io.WriteString(w, "hello")
			}))
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{tc.cert}}
			srv.StartTLS()
			t.Cleanup(srv.Close)
			f := newFixture(t, httpSource(srv.URL+"/x"), nil, WithVerifyTLS(func() bool { return false }))

			id := f.send(t)
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			hops := f.rec.response(t, id).Timeline.Hops
			if len(hops) != 1 || hops[0].Conn.TLS == nil {
				t.Fatalf("hops = %+v, want one over TLS", hops)
			}
			ti := hops[0].Conn.TLS
			if ti.Version == "" || ti.CipherSuite == "" || ti.ALPN != "http/1.1" {
				t.Errorf("TLS = %+v, want a version, a cipher and ALPN http/1.1", ti)
			}
			if len(ti.Certificates) != len(tc.want) {
				t.Fatalf("certificates = %+v, want %d", ti.Certificates, len(tc.want))
			}
			for i, w := range tc.want {
				c := ti.Certificates[i]
				if !strings.Contains(c.Subject, "CN="+w.cn) || c.Validity != w.validity || c.SelfSigned != w.selfSigned {
					t.Errorf("certificate %d = %+v, want %s, %s, self-signed %v", i, c, w.cn, w.validity, w.selfSigned)
				}
			}
			leaf := ti.Certificates[0]
			if want := tc.cert.Leaf.NotAfter.UTC().Format(time.RFC3339); leaf.NotAfter != want {
				t.Errorf("notAfter = %s, want %s", leaf.NotAfter, want)
			}
		})
	}
}

// checkHopPhases checks that tm has the six phases in order, those in
// skipped not observed for the reason given, the others observed one after
// the other within the hop.
func checkHopPhases(t *testing.T, tm HopTimingDTO, skipped map[string]string) {
	t.Helper()
	want := []string{PhaseDNS, PhaseConnect, PhaseTLS, PhaseRequestWrite, PhaseWait, PhaseDownload}
	if len(tm.Phases) != len(want) {
		t.Fatalf("phases = %+v", tm.Phases)
	}
	end := 0.0
	for i, p := range tm.Phases {
		if p.Phase != want[i] {
			t.Errorf("phase %d = %q, want %q", i, p.Phase, want[i])
		}
		if reason, ok := skipped[p.Phase]; ok {
			if p.Observed || p.Skipped != reason || p.StartMs != 0 || p.DurationMs != 0 {
				t.Errorf("phase %+v, want skipped: %s", p, reason)
			}
			continue
		}
		if !p.Observed || p.Skipped != "" || p.DurationMs < 0 || p.StartMs < end-0.0015 { // a microsecond of rounding
			t.Errorf("phase %+v, want observed, starting at or after %v", p, end)
		}
		end = p.StartMs + p.DurationMs
	}
	if tm.TotalMs <= 0 || end > tm.TotalMs+0.0015 {
		t.Errorf("phases end at %v ms, total %v ms", end, tm.TotalMs)
	}
}

func TestHopTimingSkipReasons(t *testing.T) {
	t.Parallel()
	redirect := hopTimingDTO(httpclient.Timing{Observed: httpclient.PhaseConnect | httpclient.PhaseTLS | httpclient.PhaseRequestWrite | httpclient.PhaseWait},
		time.Millisecond, httpclient.Conn{RemoteAddr: "10.0.0.1:443", Protocol: "h2", TLS: &httpclient.TLSInfo{}}, false)
	got := map[string]string{}
	for _, p := range redirect.Phases {
		got[p.Phase] = p.Skipped
	}
	if got[PhaseDNS] != SkipNoLookup || got[PhaseTLS] != "" || got[PhaseDownload] != SkipNotFinal || redirect.TotalMs != 1 {
		t.Errorf("redirect hop = %+v", redirect)
	}
	// No connection recorded at all: no reason is made up.
	unknown := hopTimingDTO(httpclient.Timing{}, 0, httpclient.Conn{}, true)
	for _, p := range unknown.Phases {
		if p.Observed || p.Skipped != "" {
			t.Errorf("phase %+v, want not observed, no reason", p)
		}
	}
}
