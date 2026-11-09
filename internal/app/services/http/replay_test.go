package http

import (
	"bytes"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// A prepared request goes out as it is, through the guarded dialer (the
// operation marks it as the user's), and its response arrives like a
// send's.
func TestSendPrepared(t *testing.T) {
	t.Parallel()
	type got struct {
		method, uri, ua string
		header          nethttp.Header
		body            []byte
	}
	seen := make(chan got, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- got{r.Method, r.RequestURI, r.UserAgent(), r.Header.Clone(), body}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	f := newFixture(t, httpSource(srv.URL), nil)
	f.setDefault(3 * time.Second)
	body := []byte{0, 1, 2, 0xff}
	started, err := SendPrepared(f.svc, httpclient.Request{
		Method: "PATCH",
		URL:    srv.URL + "/hook?x=1",
		Headers: []httpclient.Header{
			{Name: "X-Signature", Value: "sig", Enabled: true},
			{Name: "X-Multi", Value: "a", Enabled: true},
			{Name: "X-Multi", Value: "b", Enabled: true},
		},
		Body: httpclient.RawBody{Data: body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	g := <-seen
	if g.method != "PATCH" || g.uri != "/hook?x=1" || g.ua != "" || !bytes.Equal(g.body, body) ||
		g.header.Get("X-Signature") != "sig" || len(g.header.Values("X-Multi")) != 2 || g.header.Get("Content-Type") != "" {
		t.Errorf("server saw %+v", g)
	}
	resp := f.rec.response(t, started.OpID)
	if resp.StatusCode != 201 || resp.Body != `{"ok":true}` || resp.BodyLanguage != "json" || resp.TimeoutMs != 3000 || resp.TimeoutSource != "settings" {
		t.Errorf("response = %d %q %q timeout %d %q", resp.StatusCode, resp.Body, resp.BodyLanguage, resp.TimeoutMs, resp.TimeoutSource)
	}
}

func TestSendPreparedUnavailable(t *testing.T) {
	t.Parallel()
	_, err := SendPrepared(New(nil, nil, nil, nil, nil, nil), httpclient.Request{Method: "GET", URL: "http://127.0.0.1:1/"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeUnavailable {
		t.Fatalf("err = %v, want %s", err, CodeUnavailable)
	}
}

func TestBodyView(t *testing.T) {
	t.Parallel()
	v := BodyView("application/json", []byte(`{"a":1}`), 7, false)
	if v.Body != `{"a":1}` || v.BodyLanguage != "json" || v.PrettyState != "formatted" || v.Size != 7 {
		t.Errorf("json view = %+v", v)
	}
	v = BodyView("", []byte{0, 1, 2, 0xfe}, 10, true)
	if !v.BodyHex || !v.BodyTruncated || v.Size != 10 {
		t.Errorf("binary view = %+v", v)
	}
}
