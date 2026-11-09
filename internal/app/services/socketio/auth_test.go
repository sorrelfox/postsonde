package socketio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// fixedAuth is a request.HandshakeAuthFunc that sends auths[requestID]
// as the app's does (request.HandshakeAuth).
func fixedAuth(auths map[string]projectfile.Auth) request.HandshakeAuthFunc {
	return func(_ context.Context, in request.HandshakeInput) (request.Handshake, error) {
		a, ok := auths[in.RequestID]
		if !ok {
			return request.Handshake{Type: projectfile.AuthNone}, nil
		}
		r, err := request.HandshakeAuth(a, in.Protocol, request.AuthOptions{Expand: in.Expand, URL: in.URL})
		if err != nil {
			return request.Handshake{}, err
		}
		return request.Handshake{Type: a.Type, Headers: r.Headers, Query: r.Query, Credentials: r.Credentials, Secrets: r.Secrets}, nil
	}
}

// requestsSince returns the requests srv received after its first before,
// leaving out those of earlier connections: a poll that was in flight when
// the client closed can reach the server after before was taken, and it
// carries that earlier connection's session ID.
func requestsSince(srv *siotest.Server, before int) []*http.Request {
	all := srv.Requests()
	old := map[string]bool{}
	for _, r := range all[:before] {
		if sid := r.URL.Query().Get("sid"); sid != "" {
			old[sid] = true
		}
	}
	var out []*http.Request
	for _, r := range all[before:] {
		if !old[r.URL.Query().Get("sid")] {
			out = append(out, r)
		}
	}
	return out
}

// TestConnectSendsAuthV2 (AUTH-31, SIO-02): socket.io v2 has no auth
// payload, so authIn "payload" sends the credentials in the URL's query,
// which a v2 server reads as socket.handshake.query: with the handshake
// for the main namespace and in the namespace's CONNECT for another. A
// query parameter typed in the URL wins; "handshake" still sends headers.
func TestConnectSendsAuthV2(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	srv := siotest.Start(siotest.Config{V2: true, Namespaces: []string{"/admin"}, Authorize: func(nsp string, auth json.RawMessage) error {
		mu.Lock()
		seen = append(seen, nsp+" "+string(auth))
		mu.Unlock()
		return nil
	}}, t.Cleanup)
	f := newFixture(t, map[string]string{"token": "v2-s3cret-1"})
	f.svc.auth = fixedAuth(map[string]projectfile.Auth{"req_1": {Type: projectfile.AuthBearer, Token: "{{token}}"}})

	run := func(in ConnectInputDTO) []ItemDTO {
		t.Helper()
		in.RequestID, in.Version, in.Transport = "req_1", "v2", "polling"
		id := f.connect(t, in)
		f.rec.waitFor(t, id, "connected", isType(ItemConnected))
		f.ops.Cancel(id)
		f.rec.wait(t, id)
		return f.rec.list(id)
	}
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen[len(seen)-1]
	}

	before := len(srv.Requests())
	run(ConnectInputDTO{URL: srv.URL + "?room=a", AuthIn: "payload"})
	if got := last(); got != `/ {"room":"a","token":"v2-s3cret-1"}` {
		t.Errorf("main namespace saw %s", got)
	}
	for _, r := range requestsSince(srv, before) {
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("token") != "v2-s3cret-1" {
			t.Errorf("request %s, Authorization %q", r.URL.RawQuery, r.Header.Get("Authorization"))
		}
	}

	items := run(ConnectInputDTO{URL: srv.URL + "/admin", AuthIn: "payload"})
	if got := last(); got != `/admin {"token":"v2-s3cret-1"}` {
		t.Errorf("namespace CONNECT saw %s", got)
	}
	for _, it := range items {
		if b, _ := json.Marshal(it); strings.Contains(string(b), "v2-s3cret-1") {
			t.Errorf("item shows the token: %s", b)
		}
	}

	// A token typed in the URL wins.
	run(ConnectInputDTO{URL: srv.URL + "?token=mine", AuthIn: "payload"})
	if got := last(); got != `/ {"token":"mine"}` {
		t.Errorf("typed query: %s", got)
	}

	// In the handshake: the header, nothing in the query.
	before = len(srv.Requests())
	run(ConnectInputDTO{URL: srv.URL})
	for _, r := range requestsSince(srv, before) {
		if r.Header.Get("Authorization") != "Bearer v2-s3cret-1" || r.URL.Query().Has("token") {
			t.Errorf("handshake mode: %s, Authorization %q", r.URL.RawQuery, r.Header.Get("Authorization"))
		}
	}
}

// TestConnectSendsAuth (AUTH-31): the request's auth goes in the headers
// and query of every transport request, or with authIn "payload" into the
// CONNECT packet's auth payload next to the members typed there (which
// win); the values are masked in the items.
func TestConnectSendsAuth(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var payloads []string
	srv := siotest.Start(siotest.Config{Authorize: func(_ string, auth json.RawMessage) error {
		mu.Lock()
		payloads = append(payloads, string(auth))
		mu.Unlock()
		return nil
	}}, t.Cleanup)
	f := newFixture(t, map[string]string{"token": "sio-s3cret-1"})
	auths := map[string]projectfile.Auth{
		// newFixture's scopes define variables for req_1 only.
		"req_1": {Type: projectfile.AuthBearer, Token: "{{token}}"},
		"req_q": {Type: projectfile.AuthAPIKey, Key: "key", Value: "q-s3cret-2", In: projectfile.APIKeyInQuery},
		"req_h": {Type: projectfile.AuthHawk, HawkID: "id"},
	}
	f.svc.auth = fixedAuth(auths)

	run := func(in ConnectInputDTO) []ItemDTO {
		t.Helper()
		id := f.connect(t, in)
		f.rec.waitFor(t, id, "connected", isType(ItemConnected))
		f.ops.Cancel(id)
		f.rec.wait(t, id)
		return f.rec.list(id)
	}

	for _, transport := range []string{"polling", "websocket"} {
		before := len(srv.Requests())
		items := run(ConnectInputDTO{RequestID: "req_1", URL: srv.URL, Transport: transport})
		reqs := requestsSince(srv, before)
		if len(reqs) == 0 {
			t.Fatalf("%s: no requests", transport)
		}
		for _, r := range reqs {
			if v := r.Header.Get("Authorization"); v != "Bearer sio-s3cret-1" {
				t.Errorf("%s: server saw Authorization %q", transport, v)
			}
		}
		for _, it := range items {
			if b, _ := json.Marshal(it); strings.Contains(string(b), "sio-s3cret-1") {
				t.Errorf("%s: item shows the token: %s", transport, b)
			}
		}
	}

	before := len(srv.Requests())
	run(ConnectInputDTO{RequestID: "req_q", URL: srv.URL, Transport: "polling"})
	for _, r := range requestsSince(srv, before) {
		if r.URL.Query().Get("key") != "q-s3cret-2" || r.URL.Query().Get("EIO") != "4" {
			t.Errorf("server saw query %q", r.URL.RawQuery)
		}
	}

	// In the auth payload: the token joins the typed members, a typed
	// "token" wins, and nothing goes in the headers.
	before = len(srv.Requests())
	items := run(ConnectInputDTO{RequestID: "req_1", URL: srv.URL, Transport: "polling", AuthIn: "payload", AuthPayload: `{"room": "a"}`})
	run(ConnectInputDTO{RequestID: "req_1", URL: srv.URL, Transport: "polling", AuthIn: "payload", AuthPayload: `{"token": "mine"}`})
	run(ConnectInputDTO{RequestID: "req_1", URL: srv.URL, Transport: "polling", AuthIn: "payload"})
	for _, r := range requestsSince(srv, before) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("payload mode sent Authorization %q", r.Header.Get("Authorization"))
		}
	}
	mu.Lock()
	got := append([]string(nil), payloads[len(payloads)-3:]...)
	mu.Unlock()
	// The client sends the payload compacted.
	want := []string{`{"room":"a","token":"sio-s3cret-1"}`, `{"token":"mine"}`, `{"token":"sio-s3cret-1"}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("payloads = %q, want %q", got, want)
	}
	for _, it := range items {
		if it.Type == ItemPacket && strings.Contains(it.Data, "sio-s3cret-1") {
			t.Errorf("CONNECT item shows the token: %s", it.Data)
		}
	}

	// A type that cannot be sent, a payload that is not an object and an
	// unknown authIn refuse the connect.
	for _, tc := range []struct {
		in   ConnectInputDTO
		code string
	}{
		{ConnectInputDTO{RequestID: "req_h", URL: srv.URL}, request.CodeAuthNotApplicable},
		{ConnectInputDTO{RequestID: "req_1", URL: srv.URL, AuthIn: "payload", AuthPayload: `["x"]`}, request.CodeAuthPayloadInvalid},
		{ConnectInputDTO{RequestID: "req_1", URL: srv.URL, AuthIn: "cookie"}, CodeInvalidAuthIn},
	} {
		if _, err := f.svc.Connect(t.Context(), tc.in); codeOf(err) != tc.code {
			t.Errorf("%+v: %v, want %s", tc.in, err, tc.code)
		}
	}
}
