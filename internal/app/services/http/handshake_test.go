package http

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestHandshakeAuthOAuth2 (AUTH-31): an oauth2 auth of a websocket,
// socketio or grpc request gets its token as a send does, from the token
// endpoint first and from the store after, only once the connection's
// variables are all defined (Ready); the token goes in the header and is
// a secret to mask.
func TestHandshakeAuthOAuth2(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	src := oauth2Source("ws://feed.test", idp.URL)
	src.Handshake = projectfile.KindWebSocket
	f := newFixture(t, src, nil)
	ctx := t.Context()

	notReady := errs.New(errs.InvalidArgument, "undefined").WithCode("template.unresolved")
	_, err := HandshakeAuth(ctx, f.svc, request.HandshakeInput{RequestID: "req_1", Protocol: request.HandshakeWebSocket, Ready: func() error { return notReady }})
	if !errors.Is(err, notReady) || idp.count() != 0 {
		t.Fatalf("not ready: %v, %d token requests", err, idp.count())
	}

	for i := range 2 {
		h, err := HandshakeAuth(ctx, f.svc, request.HandshakeInput{RequestID: "req_1", Protocol: request.HandshakeWebSocket, URL: "ws://feed.test/"})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Headers) != 1 || h.Headers[0].Name != "Authorization" || h.Headers[0].Value != "Bearer issued-token-1" {
			t.Errorf("%d: headers = %+v", i, h.Headers)
		}
		if h.Type != projectfile.AuthOAuth2 || h.Credentials == nil || h.Credentials.Token != "issued-token-1" || !slices.Contains(h.Secrets, "issued-token-1") || !slices.Contains(h.Secrets, idpSecret) {
			t.Errorf("%d: handshake = %+v", i, h)
		}
	}
	if idp.count() != 1 {
		t.Errorf("%d token requests, want 1 (then the stored token)", idp.count())
	}

	// gRPC has no query string.
	f.src.Auth.In = projectfile.APIKeyInQuery
	_, err = HandshakeAuth(ctx, f.svc, request.HandshakeInput{RequestID: "req_1", Protocol: request.HandshakeGRPC, URL: "http://h:1"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != request.CodeAuthNotApplicable {
		t.Errorf("grpc in query: %v", err)
	}
	h, err := HandshakeAuth(ctx, f.svc, request.HandshakeInput{RequestID: "req_1", Protocol: request.HandshakeSocketIO, URL: "http://h/"})
	if err != nil || len(h.Query) != 1 || h.Query[0] != (request.QueryParam{Name: "access_token", Value: "issued-token-1"}) {
		t.Errorf("socketio in query = %+v, %v", h, err)
	}

	// No request: nothing, and no loading.
	if h, err := HandshakeAuth(ctx, f.svc, request.HandshakeInput{Protocol: request.HandshakeWebSocket}); err != nil || h.Type != projectfile.AuthNone || h.Headers != nil {
		t.Errorf("no request = %+v, %v", h, err)
	}
}

// TestAuthToolsOnHandshakeRequests: the Auth tab's JWT preview works on a
// websocket request through the auth loader.
func TestAuthToolsOnHandshakeRequests(t *testing.T) {
	t.Parallel()
	src := collection.HTTPSource{
		Request:   projectfile.HTTPRequest{ID: "req_ws", URL: "ws://h/"},
		Handshake: projectfile.KindWebSocket,
		Auth:      projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "k3y", Payload: `{"sub":"ws"}`},
	}
	f := newFixture(t, httpSource("http://unused.test/"), nil, WithAuthLoader(func(_ context.Context, id string) (collection.HTTPSource, error) {
		if id != "req_ws" {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request")
		}
		return src, nil
	}))
	p, err := f.svc.JWTPreview(t.Context(), JWTPreviewInputDTO{RequestID: "req_ws"})
	if err != nil || !strings.Contains(p.Payload, `"sub": "ws"`) || p.Token == "" {
		t.Errorf("JWTPreview = %+v, %v", p, err)
	}
}
