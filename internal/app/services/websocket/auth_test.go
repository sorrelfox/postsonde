package websocket

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// fixedAuth is a request.HandshakeAuthFunc that sends auths[requestID]
// as the app's does (request.HandshakeAuth), recording the input.
func fixedAuth(auths map[string]projectfile.Auth, seen *[]request.HandshakeInput) request.HandshakeAuthFunc {
	return func(_ context.Context, in request.HandshakeInput) (request.Handshake, error) {
		*seen = append(*seen, in)
		a, ok := auths[in.RequestID]
		if !ok {
			return request.Handshake{Type: projectfile.AuthNone}, nil
		}
		r, err := request.HandshakeAuth(a, in.Protocol, request.AuthOptions{Expand: in.Expand, URL: in.URL})
		if err != nil {
			return request.Handshake{}, err
		}
		if in.Ready != nil {
			if err := in.Ready(); err != nil {
				return request.Handshake{}, err
			}
		}
		return request.Handshake{Type: a.Type, Headers: r.Headers, Query: r.Query, Credentials: r.Credentials, Secrets: r.Secrets}, nil
	}
}

// TestConnectSendsAuth (AUTH-31): the request's auth goes with the
// upgrade request, its variables rendered with the request's scopes; a
// typed header of the same name wins; the values are masked in the
// handshake and URL the UI is shown; a type that cannot be sent on a
// handshake refuses the connect.
func TestConnectSendsAuth(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	vars := map[string]string{"token": "ws-s3cret-1", "key": "q-s3cret-2"}
	scopes := func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.Vars("Globals", vars)}, nil
	}
	var inputs []request.HandshakeInput
	auths := map[string]projectfile.Auth{
		"req_bearer": {Type: projectfile.AuthBearer, Token: "{{token}}"},
		"req_basic":  {Type: projectfile.AuthBasic, Username: "ann", Password: "{{token}}"},
		"req_query":  {Type: projectfile.AuthAPIKey, Key: "api_key", Value: "{{key}}", In: projectfile.APIKeyInQuery},
		"req_digest": {Type: projectfile.AuthDigest, Username: "ann", Password: "pw"},
		"req_undef":  {Type: projectfile.AuthBearer, Token: "{{nope}}"},
	}
	s, ev := newService(t, nil, WithScopes(scopes), WithAuth(fixedAuth(auths, &inputs)))
	ctx := t.Context()

	connect := func(id string, headers ...HeaderDTO) (seen, *StateEventDTO) {
		t.Helper()
		conn, err := s.Connect(ctx, ConnectInputDTO{RequestID: id, URL: wsURL(srv, "/feed?room=a"), Headers: headers})
		if err != nil {
			t.Fatalf("%s: Connect: %v", id, err)
		}
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,open" })
		open := ev.state(conn.ConnID, "open")
		if err := s.Close(ctx, CloseInputDTO{ConnID: conn.ConnID}); err != nil {
			t.Fatal(err)
		}
		return recvSeen(t, seenCh), open
	}

	got, open := connect("req_bearer")
	if v := got.header.Get("Authorization"); v != "Bearer ws-s3cret-1" {
		t.Errorf("server saw Authorization %q", v)
	}
	for _, h := range open.Handshake.RequestHeaders {
		if strings.Contains(h.Value, "ws-s3cret-1") {
			t.Errorf("handshake shows %s: %s", h.Name, h.Value)
		}
		if strings.EqualFold(h.Name, "Authorization") && h.Value != "Bearer "+httpclient.Mask {
			t.Errorf("handshake Authorization = %q", h.Value)
		}
	}
	if len(inputs) == 0 || inputs[0].Protocol != request.HandshakeWebSocket || !strings.HasSuffix(inputs[0].URL, "/feed?room=a") {
		t.Errorf("auth input = %+v", inputs)
	}

	got, _ = connect("req_basic")
	if v := got.header.Get("Authorization"); v != "Basic "+base64.StdEncoding.EncodeToString([]byte("ann:ws-s3cret-1")) {
		t.Errorf("server saw Authorization %q", v)
	}
	// A header typed in the Headers tab wins over the Auth tab.
	got, _ = connect("req_bearer", HeaderDTO{Name: "authorization", Value: "Token mine", Enabled: true})
	if v := got.header.Values("Authorization"); len(v) != 1 || v[0] != "Token mine" {
		t.Errorf("server saw Authorization %q", v)
	}

	got, open = connect("req_query")
	if got.query != "room=a&api_key=q-s3cret-2" {
		t.Errorf("server saw query %q", got.query)
	}
	if strings.Contains(open.URL, "q-s3cret-2") || strings.Contains(open.Handshake.URL, "q-s3cret-2") || !strings.Contains(open.URL, "api_key="+httpclient.Mask) {
		t.Errorf("shown URL = %q, handshake %q", open.URL, open.Handshake.URL)
	}

	// No request, no auth.
	got, _ = connect("")
	if got.header.Get("Authorization") != "" {
		t.Errorf("server saw %v", got.header)
	}

	_, err := s.Connect(ctx, ConnectInputDTO{RequestID: "req_digest", URL: wsURL(srv, "/")})
	checkCode(t, err, errs.Unsupported, request.CodeAuthNotApplicable)
	_, err = s.Connect(ctx, ConnectInputDTO{RequestID: "req_undef", URL: wsURL(srv, "/")})
	checkCode(t, err, errs.InvalidArgument, template.CodeUnresolved)
	if e, _ := errors.AsType[*errs.Error](err); e.Details["names"] != "nope" {
		t.Errorf("undefined auth variable: %v", err)
	}
}
