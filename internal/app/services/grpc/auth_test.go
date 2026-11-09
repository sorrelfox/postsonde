package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// fixedAuth is a request.HandshakeAuthFunc that sends auths[requestID]
// as the app's does (request.HandshakeAuth).
func fixedAuth(auths map[string]projectfile.Auth, urls *[]string) request.HandshakeAuthFunc {
	return func(_ context.Context, in request.HandshakeInput) (request.Handshake, error) {
		*urls = append(*urls, in.URL)
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

// TestInvokeSendsAuth (AUTH-31): the request's auth goes in the call's
// metadata, keys in lower case, its variables rendered with the request's
// scopes; a metadata row of the same key wins; the value is masked in the
// request item; a query parameter, a type that signs HTTP requests and
// an undefined variable refuse the call.
func TestInvokeSendsAuth(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	scopes := func(_ context.Context, id string) (template.Chain, error) {
		return template.Chain{template.Vars("Globals", map[string]string{"token": "grpc-s3cret-1"})}, nil
	}
	auths := map[string]projectfile.Auth{
		"req_bearer": {Type: projectfile.AuthBearer, Token: "{{token}}"},
		"req_key":    {Type: projectfile.AuthAPIKey, Key: "X-Api-Key", Value: "{{token}}"},
		"req_query":  {Type: projectfile.AuthAPIKey, Key: "k", Value: "v", In: projectfile.APIKeyInQuery},
		"req_aws":    {Type: projectfile.AuthAWSV4, AccessKey: "AK"},
		"req_undef":  {Type: projectfile.AuthBearer, Token: "{{nope}}"},
	}
	var urls []string
	s, rec := newCallService(t, WithAuth(fixedAuth(auths, &urls), scopes))

	call := func(id string, rows ...MetadataRowDTO) []CallEventDTO {
		t.Helper()
		in := input(srv.Addr, "Echo")
		in.RequestID, in.Metadata = id, rows
		items, done := invoke(t, s, rec, in)
		if done.Status != operation.StatusOK {
			t.Fatalf("%s: done %+v %+v", id, done, done.Error)
		}
		return items
	}

	items := call("req_bearer")
	if v := srv.lastMetadata().Get("authorization"); len(v) != 1 || v[0] != "Bearer grpc-s3cret-1" {
		t.Errorf("server got authorization %q", v)
	}
	if e, ok := md(items[0].Metadata, "authorization"); !ok || e.Value != "Bearer "+httpclient.Mask {
		t.Errorf("request item metadata %+v", items[0].Metadata)
	}
	if len(urls) == 0 || urls[0] != "http://"+srv.Addr {
		t.Errorf("auth URL = %q", urls)
	}

	items = call("req_key")
	if v := srv.lastMetadata().Get("x-api-key"); len(v) != 1 || v[0] != "grpc-s3cret-1" {
		t.Errorf("server got x-api-key %q", v)
	}
	if e, ok := md(items[0].Metadata, "x-api-key"); !ok || strings.Contains(e.Value, "grpc-s3cret-1") {
		t.Errorf("request item metadata %+v", items[0].Metadata)
	}

	// A metadata row wins over the Auth tab.
	call("req_bearer", MetadataRowDTO{Name: "Authorization", Value: "Bearer mine", Enabled: true})
	if v := srv.lastMetadata().Get("authorization"); len(v) != 1 || v[0] != "Bearer mine" {
		t.Errorf("server got authorization %q", v)
	}

	for id, code := range map[string]string{"req_query": request.CodeAuthNotApplicable, "req_aws": request.CodeAuthNotApplicable, "req_undef": template.CodeUnresolved} {
		in := input(srv.Addr, "Echo")
		in.RequestID = id
		_, err := s.Invoke(t.Context(), in)
		if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != code {
			t.Errorf("%s: %v, want %s", id, err, code)
		}
	}
}
