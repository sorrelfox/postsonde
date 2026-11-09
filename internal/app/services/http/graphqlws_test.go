package http

import (
	"crypto/x509"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func subscriptionSource(url string) collection.HTTPSource {
	return collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{
			ID:               "req_sub",
			URL:              url,
			Query:            "subscription { a }",
			ConnectionParams: `{"t": "{{token}}"}`,
		},
		Auth:            projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"},
		FolderVariables: [][]projectfile.Variable{{{Name: "base", Value: "https://api.test"}, {Name: "token", Value: "tok"}}},
	}
}

// A subscription's handshake takes the request's settings like a send:
// the scheme mapped, auth, network.verifyTls and the custom CA bundles
// (NET-04, only for wss).
func TestNewGraphQLSubscription(t *testing.T) {
	t.Parallel()
	pool := x509.NewCertPool()
	verify := true
	var rootsErr error
	f := newFixture(t, subscriptionSource("{{base}}/graphql"), nil,
		WithVerifyTLS(func() bool { return verify }),
		WithTrust(func() (*x509.CertPool, error) { return pool, rootsErr }))

	sub, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.WS.URL != "wss://api.test/graphql" || sub.WS.RootCAs != pool || sub.WS.InsecureSkipVerify || sub.WS.Dialer == nil {
		t.Errorf("options = %+v", sub.WS)
	}
	if len(sub.WS.Headers) != 1 || sub.WS.Headers[0].Value != "Bearer tok" {
		t.Errorf("headers = %+v", sub.WS.Headers)
	}
	if string(sub.Build.ConnectionParams) != `{"t":"tok"}` {
		t.Errorf("params = %s", sub.Build.ConnectionParams)
	}

	verify = false // network.verifyTls off
	if sub, err = NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false); err != nil || !sub.WS.InsecureSkipVerify || sub.WS.RootCAs != nil {
		t.Errorf("verifyTls off: %+v, %v", sub, err)
	}
	verify = true

	rootsErr = errs.New(errs.InvalidArgument, "bad bundle").WithCode("tlsconf.test")
	if _, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false); err == nil {
		t.Error("a broken CA bundle did not fail a wss subscription")
	}
	f.src = subscriptionSource("http://{{token}}.test/graphql")
	if sub, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false); err != nil || sub.WS.URL != "ws://tok.test/graphql" || sub.WS.RootCAs != nil {
		t.Errorf("ws with a broken bundle: %+v, %v", sub, err)
	}
}

func TestNewGraphQLSubscriptionErrors(t *testing.T) {
	t.Parallel()
	src := subscriptionSource("{{base}}/graphql")
	src.GraphQL.ConnectionParams = `{"t": "{{nope}}"}`
	f := newFixture(t, src, nil)
	_, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved {
		t.Fatalf("undefined variable: %v", err)
	}
	if _, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", true); err != nil {
		t.Errorf("allowUnresolved: %v", err)
	}
	f.src.GraphQL.URL = "ftp://h"
	if _, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", true); !errors.As(err, &e) || e.Code != request.CodeGraphQLWebSocketScheme {
		t.Errorf("bad scheme: %v", err)
	}
	f.src = httpSource("http://h/x")
	if _, err := NewGraphQLSubscription(t.Context(), f.svc, f.src.Request.ID, false); !errors.As(err, &e) || e.Code != CodeNotGraphQL {
		t.Errorf("http request: %v", err)
	}
}
