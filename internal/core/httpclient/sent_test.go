package httpclient

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestSentRequestRedirects checks that every hop's request and response
// head are recorded: a 307 sends the body again.
func TestSentRequestRedirects(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/307", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hop", "first")
		http.Redirect(w, r, "/echo?x=1", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})

	req := Request{
		Method:   http.MethodPost,
		URL:      srv.URL + "/307",
		Headers:  []Header{on("X-Token", "t1")},
		Body:     RawBody{Data: []byte("abc"), Type: "text/plain"},
		Settings: DefaultSettings(),
	}
	resp, err := c.Send(t.Context(), req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(resp.Redirects) != 1 {
		t.Fatalf("%d redirects, want 1", len(resp.Redirects))
	}
	hop := resp.Redirects[0]
	first := hop.Request
	if first == nil || first.Method != "POST" || first.Target != "/307" || first.URL != srv.URL+"/307" || string(first.Body) != "abc" {
		t.Errorf("first request = %+v", first)
	}
	if hop.Proto != "HTTP/1.1" || hop.Status != "307 Temporary Redirect" || !slices.Equal(headerValues(hop.Headers, "X-Hop"), []string{"first"}) {
		t.Errorf("first response = %s %q %v", hop.Proto, hop.Status, hop.Headers)
	}
	final := resp.Request
	if final.Target != "/echo?x=1" || final.URL != srv.URL+"/echo?x=1" || string(final.Body) != "abc" || final.BodySize != 3 {
		t.Errorf("final request = %+v", final)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	want := []Header{on("Host", host), on("Content-Length", "3"), on("Content-Type", "text/plain"), on("X-Token", "t1")}
	if !slices.Equal(final.Headers, want) {
		t.Errorf("final headers = %v, want %v", final.Headers, want)
	}
	if got, want := string(final.Head()), "POST /echo?x=1 HTTP/1.1\r\nHost: "+host+"\r\nContent-Length: 3\r\nContent-Type: text/plain\r\nX-Token: t1\r\n\r\n"; got != want {
		t.Errorf("Head =\n%q, want\n%q", got, want)
	}
}

// TestSentRequestHTTP2 checks the header fields recorded for an HTTP/2
// request against those the server received.
func TestSentRequestHTTP2(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var got *http.Request
	var gotBody []byte
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, gotBody = r, b
		mu.Unlock()
	}), true)
	c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})

	req := Request{
		Method:  "PUT",
		URL:     srv.URL + "/p?q=1",
		Headers: []Header{on("X-MiXeD", "v"), on("Cookie", "a=1; b=2"), on("user-agent", "ua/1")},
		Body:    RawBody{Data: []byte("hello")},
	}
	resp, err := c.Send(t.Context(), req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := resp.Request
	if sent.Proto != "HTTP/2.0" || sent.Target != "/p?q=1" || string(sent.Body) != "hello" {
		t.Errorf("sent = %+v", sent)
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	pseudo := []Header{on(":authority", host), on(":method", "PUT"), on(":path", "/p?q=1"), on(":scheme", "https")}
	if len(sent.Headers) < 4 || !slices.Equal(sent.Headers[:4], pseudo) {
		t.Fatalf("pseudo-header fields = %v, want %v", sent.Headers, pseudo)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(gotBody, sent.Body) {
		t.Errorf("server received body %q, recorded %q", gotBody, sent.Body)
	}
	// The server's view: canonical names, cookie crumbs joined by net/http.
	received := http.Header{}
	for _, h := range sent.Headers[4:] {
		if h.Name != strings.ToLower(h.Name) {
			t.Errorf("field %q not lower-cased", h.Name)
		}
		received.Add(h.Name, h.Value)
	}
	want := got.Header.Clone()
	if c := want.Values("Cookie"); len(c) == 1 {
		// net/http's h2 server joins the crumbs with "; ".
		want["Cookie"] = strings.Split(c[0], "; ")
	}
	for name, vv := range want {
		if !slices.Equal(received[name], vv) {
			t.Errorf("%s: recorded %q, server received %q", name, received[name], vv)
		}
	}
	for name := range received {
		if _, ok := want[name]; !ok {
			t.Errorf("recorded %s the server did not receive", name)
		}
	}
	if head := string(sent.Head()); !strings.HasPrefix(head, ":authority: "+host+"\r\n:method: PUT\r\n") || !strings.HasSuffix(head, "\r\n\r\n") {
		t.Errorf("Head = %q", head)
	}
}

// TestSentRequestBodyCap checks that a long body is kept up to
// MaxRecordedBody and counted whole.
func TestSentRequestBodyCap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) }))
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})
	data := bytes.Repeat([]byte("0123456789"), MaxRecordedBody/10+100)
	resp, err := c.Send(t.Context(), Request{Method: "POST", URL: srv.URL, Body: RawBody{Data: data}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := resp.Request
	if len(sent.Body) != MaxRecordedBody || sent.BodySize != int64(len(data)) || !sent.BodyTruncated || !bytes.Equal(sent.Body, data[:MaxRecordedBody]) {
		t.Errorf("recorded %d bytes of %d, truncated %v", len(sent.Body), sent.BodySize, sent.BodyTruncated)
	}
}

func TestSentRequestNoBody(t *testing.T) {
	t.Parallel()

	srv := startRawServer(t, okResponse)
	c := newTestClient(t, TransportOptions{})
	resp, err := c.Send(t.Context(), get("http://"+srv.addr+"/"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s := resp.Request; s.Body == nil || len(s.Body) != 0 || s.BodySize != 0 || s.BodyTruncated {
		t.Errorf("body = %v %d %v, want empty", s.Body, s.BodySize, s.BodyTruncated)
	}
}

func TestSecretMasker(t *testing.T) {
	t.Parallel()

	m := NewSecretMasker([]string{"s3cr et", "", "s3cr"})
	sent := &SentRequest{
		Method: "POST",
		URL:    "https://x.test/p?k=s3cr+et&k2=s3cr%20et",
		Target: "/p?k=s3cr+et&k2=s3cr%20et",
		Proto:  "HTTP/1.1",
		Headers: []Header{
			on("Authorization", "Basic dXNlcjpwYXNz"),
			on("proxy-authorization", "token-without-scheme"),
			on("X-Key", "s3cr"),
			on("X-Other", "plain"),
		},
		Body: []byte(`{"a":"s3cr et","b":"s3cret"}`),
	}
	got := sent.Masked(m)
	if want := "https://x.test/p?k=" + Mask + "&k2=" + Mask; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
	if want := "/p?k=" + Mask + "&k2=" + Mask; got.Target != want {
		t.Errorf("Target = %q, want %q", got.Target, want)
	}
	wantHeaders := []Header{on("Authorization", "Basic "+Mask), on("proxy-authorization", Mask), on("X-Key", Mask), on("X-Other", "plain")}
	if !slices.Equal(got.Headers, wantHeaders) {
		t.Errorf("Headers = %v, want %v", got.Headers, wantHeaders)
	}
	if want := `{"a":"` + Mask + `","b":"` + Mask + `et"}`; string(got.Body) != want {
		t.Errorf("Body = %s, want %s", got.Body, want)
	}
	if sent.Headers[0].Value != "Basic dXNlcjpwYXNz" || !strings.Contains(string(sent.Body), "s3cr et") {
		t.Error("Masked changed the original")
	}

	var zero SecretMasker
	if v := zero.MaskHeader("X-Key", "s3cr"); v != "s3cr" {
		t.Errorf("zero masker masked %q", v)
	}
	if v := zero.MaskHeader("authorization", "Bearer abc"); v != "Bearer "+Mask {
		t.Errorf("zero masker left %q", v)
	}
}
