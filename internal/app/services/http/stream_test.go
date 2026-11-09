package http

import (
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestNewStream builds an SSE request like an HTTP send ({{variables}},
// auth), adds Accept: text/event-stream, masks the auth's query secret in
// the URL shown, and opens the stream on an operation-marked context.
func TestNewStream(t *testing.T) {
	t.Parallel()
	var accept, auth string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		accept, auth = r.Header.Get("Accept"), r.URL.Query().Get("key")
		w.Header().Set("Content-Type", EventStreamType)
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	defer srv.Close()

	src := httpSource(srv.URL + "/{{path}}")
	src.SSE = true
	src.FolderVariables = [][]projectfile.Variable{{{Name: "path", Value: "events"}}}
	src.Auth = projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "key", Value: "s3cret", In: projectfile.APIKeyInQuery}
	f := newFixture(t, src, nil)

	st, err := NewStream(t.Context(), f.svc, "req_1", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Method != "GET" || st.URL != srv.URL+"/events?key="+httpclient.Mask {
		t.Errorf("stream = %s %s", st.Method, st.URL)
	}
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "1"})
	var body string
	resp, err := st.Open(ctx, "", func(head *httpclient.Response, r io.Reader) error {
		b, err := io.ReadAll(r)
		body = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || body != "data: hi\n\n" || accept != EventStreamType || auth != "s3cret" {
		t.Errorf("status %d, body %q, accept %q, key %q", resp.StatusCode, body, accept, auth)
	}

	// An Accept row of the request's own wins.
	src.Request.Headers = []projectfile.KeyValue{{Name: "accept", Value: "*/*"}}
	f.src = src
	st, err = NewStream(t.Context(), f.svc, "req_1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(ctx, "", func(*httpclient.Response, io.Reader) error { return nil }); err != nil || accept != "*/*" {
		t.Errorf("accept = %q, err %v", accept, err)
	}
	if st.Reconnect != nil {
		t.Errorf("Reconnect = %+v", st.Reconnect)
	}
}

// TestStreamLastEventID: a reconnect sends Last-Event-ID (SSE-03) in place
// of the request's own row of that name; without an ID the row is sent as
// typed. The reconnection policy comes from the request's settings.
func TestStreamLastEventID(t *testing.T) {
	t.Parallel()
	var got []string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got = r.Header.Values(LastEventIDHeader)
		w.Header().Set("Content-Type", EventStreamType)
	}))
	defer srv.Close()

	src := httpSource(srv.URL)
	src.SSE = true
	src.Request.Headers = []projectfile.KeyValue{{Name: "last-event-id", Value: "typed"}}
	on := true
	src.Request.Settings.Reconnect = &projectfile.SSEReconnect{Enabled: &on}
	f := newFixture(t, src, nil)
	st, err := NewStream(t.Context(), f.svc, "req_1", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Reconnect == nil || st.Reconnect.Enabled == nil || !*st.Reconnect.Enabled {
		t.Errorf("Reconnect = %+v", st.Reconnect)
	}
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "1"})
	noop := func(*httpclient.Response, io.Reader) error { return nil }
	if _, err := st.Open(ctx, "", noop); err != nil || len(got) != 1 || got[0] != "typed" {
		t.Errorf("no id: header %q, err %v", got, err)
	}
	if _, err := st.Open(ctx, "42", noop); err != nil || len(got) != 1 || got[0] != "42" {
		t.Errorf("id 42: header %q, err %v", got, err)
	}
}

// TestStreamErrors: Send refuses an SSE request, NewStream an HTTP one,
// and undefined variables are reported before connecting.
func TestStreamErrors(t *testing.T) {
	t.Parallel()
	src := httpSource("http://127.0.0.1:1/{{missing}}")
	src.SSE = true
	f := newFixture(t, src, nil)
	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", AllowUnresolved: true})
	checkErr(t, asErr(err), errs.InvalidArgument, CodeSSERequest)
	_, err = NewStream(t.Context(), f.svc, "req_1", false)
	checkErr(t, asErr(err), errs.InvalidArgument, "template.unresolved")
	if _, err := NewStream(t.Context(), f.svc, "req_1", true); err != nil {
		t.Errorf("allowUnresolved: %v", err)
	}

	f.src.SSE = false
	f.src.Request.URL = "http://127.0.0.1:1/"
	_, err = NewStream(t.Context(), f.svc, "req_1", false)
	checkErr(t, asErr(err), errs.InvalidArgument, CodeNotSSE)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewStream(cancelled, f.svc, "req_1", false); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled: %v", err)
	}
}

func asErr(err error) *errs.Error {
	e, _ := err.(*errs.Error)
	return e
}
