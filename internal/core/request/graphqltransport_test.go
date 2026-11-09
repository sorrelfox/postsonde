package request

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestGraphQLTransportFor(t *testing.T) {
	t.Parallel()
	all := &projectfile.GraphQLTransport{Query: "ws", Mutation: "sse", Subscription: "ws-legacy"}
	cases := []struct {
		t      *projectfile.GraphQLTransport
		opType string
		want   string
	}{
		{nil, "query", "http"},
		{nil, "mutation", "http"},
		{nil, "subscription", "ws"},
		{nil, "", "http"},
		{&projectfile.GraphQLTransport{}, "subscription", "ws"},
		{all, "query", "ws"},
		{all, "", "ws"},
		{all, "mutation", "sse"},
		{all, "subscription", "ws-legacy"},
		{&projectfile.GraphQLTransport{Query: "http", Subscription: "sse"}, "query", "http"},
		{&projectfile.GraphQLTransport{Query: "http", Subscription: "sse"}, "subscription", "sse"},
	}
	for _, tc := range cases {
		got, err := GraphQLTransportFor(tc.t, tc.opType)
		if err != nil || got != tc.want {
			t.Errorf("GraphQLTransportFor(%+v, %q) = %q, %v; want %q", tc.t, tc.opType, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		t      *projectfile.GraphQLTransport
		opType string
		field  string
	}{
		{&projectfile.GraphQLTransport{Subscription: "http"}, "subscription", "settings.graphqlTransport.subscription"},
		{&projectfile.GraphQLTransport{Mutation: "quic"}, "mutation", "settings.graphqlTransport.mutation"},
		{&projectfile.GraphQLTransport{Query: "WS"}, "query", "settings.graphqlTransport.query"},
	} {
		_, err := GraphQLTransportFor(tc.t, tc.opType)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeGraphQLInvalidTransport || e.Details[DetailField] != tc.field || e.Kind != errs.InvalidArgument {
			t.Errorf("GraphQLTransportFor(%+v, %q) = %v", tc.t, tc.opType, err)
		}
	}
}

func TestGraphQLTransportOf(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		Query:         "query A { a(x: \"{{v}}\") } subscription B { b }",
		OperationName: "B",
		Settings:      projectfile.RequestSettings{GraphQLTransport: &projectfile.GraphQLTransport{Subscription: "sse"}},
	}
	if tr, op, err := GraphQLTransportOf(f); tr != "sse" || op != "subscription" || err != nil {
		t.Errorf("B = %q %q %v", tr, op, err)
	}
	f.OperationName = "A"
	if tr, op, err := GraphQLTransportOf(f); tr != "http" || op != "query" || err != nil {
		t.Errorf("A = %q %q %v", tr, op, err)
	}
	f.OperationName = ""
	if tr, op, err := GraphQLTransportOf(f); tr != "http" || op != "" || err != nil {
		t.Errorf("none picked = %q %q %v", tr, op, err)
	}
}

func TestBuildGraphQLSSE(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		URL:        "{{base}}/graphql",
		Query:      "subscription S { s }",
		Variables:  `{"a": 1}`,
		Extensions: `{"trace": "{{user}}"}`,
		Files:      []projectfile.Part{{Name: "variables.file", File: "/nowhere/ignored.png"}},
	}
	expand := buildExpand(map[string]string{"base": "http://api.test", "user": "42"})
	b, err := BuildGraphQLSSE(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := b.Request.Body.(httpclient.RawBody)
	if b.Request.Method != "POST" || !ok || string(raw.Data) != `{"query":"subscription S { s }","variables":{"a":1},"operationName":"S","extensions":{"trace":"42"}}` {
		t.Errorf("request %s %+v", b.Request.Method, b.Request.Body)
	}
	if got := headerValue(b.Request.Headers, "Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q", got)
	}

	// A row of the request's own wins; GET puts the operation in the URL.
	f.Headers = []projectfile.KeyValue{{Name: "accept", Value: "text/event-stream;q=1, application/json;q=0.5"}}
	f.Method = "GET"
	b, err = BuildGraphQLSSE(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	if got := headerValue(b.Request.Headers, "Accept"); got != "text/event-stream;q=1, application/json;q=0.5" {
		t.Errorf("Accept = %q", got)
	}
	if b.Request.Method != "GET" || b.Request.URL != "http://api.test/graphql?query=subscription%20S%20%7B%20s%20%7D&variables=%7B%22a%22%3A1%7D&operationName=S&extensions=%7B%22trace%22%3A%2242%22%7D" {
		t.Errorf("GET %s", b.Request.URL)
	}
}

// headerValue is the value of the enabled header name (any case), "" if none.
func headerValue(headers []httpclient.Header, name string) string {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
