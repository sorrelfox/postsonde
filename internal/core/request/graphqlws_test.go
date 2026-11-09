package request

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestBuildGraphQLWS(t *testing.T) {
	t.Parallel()
	timeout := int64(2500)
	f := projectfile.GraphQLRequest{
		Method:           "GET", // ignored: the handshake is always a GET
		URL:              "{{base}}/graphql",
		Query:            "subscription Events($id: ID!) { events(id: $id) { at } }",
		Variables:        "{\n  \"id\": \"{{user}}\"\n}",
		ConnectionParams: "{\n  \"authToken\": \"{{token}}\",\n  \"html\": \"<b>\"\n}",
		Extensions:       `{"trace": "{{user}}"}`,
		Files:            []projectfile.Part{{Name: "variables.file", File: "/nowhere/ignored.png"}},
		Headers:          []projectfile.KeyValue{{Name: "X-Trace", Value: "t"}},
		Settings:         projectfile.RequestSettings{TimeoutMs: &timeout},
	}
	auth := projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"}
	expand := buildExpand(map[string]string{"base": "HTTPS://api.test", "user": "42", "token": "tok"})

	b, err := BuildGraphQLWS(f, auth, HTTPOptions{Expand: expand, DefaultInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.URL != "wss://api.test/graphql" {
		t.Errorf("URL = %q", b.URL)
	}
	wantHeaders := []httpclient.Header{on("X-Trace", "t"), on("Authorization", "Bearer tok")}
	if !reflect.DeepEqual(b.Headers, wantHeaders) {
		t.Errorf("headers = %+v\nwant %+v", b.Headers, wantHeaders)
	}
	if string(b.Variables) != `{"id":"42"}` {
		t.Errorf("variables = %s", b.Variables)
	}
	if string(b.Extensions) != `{"trace":"42"}` {
		t.Errorf("extensions = %s", b.Extensions)
	}
	if string(b.ConnectionParams) != `{"authToken":"tok","html":"<b>"}` {
		t.Errorf("connection params = %s", b.ConnectionParams)
	}
	if b.OperationName != "Events" || b.OperationType != "subscription" {
		t.Errorf("operation = %q %q", b.OperationName, b.OperationType)
	}
	if b.HandshakeTimeout != 2500*time.Millisecond {
		t.Errorf("timeout = %v", b.HandshakeTimeout)
	}
	if !b.InsecureSkipVerify || b.TLSSource != TLSFromSettings {
		t.Errorf("tls = %v %q", b.InsecureSkipVerify, b.TLSSource)
	}
}

func TestBuildGraphQLWSOperationType(t *testing.T) {
	t.Parallel()
	doc := "query A { a } subscription B { b }"
	for _, tt := range []struct{ saved, name, typ string }{
		{"B", "B", "subscription"},
		{"A", "A", "query"},
		{"", "", ""},
		{"C", "C", ""},
	} {
		b, err := BuildGraphQLWS(projectfile.GraphQLRequest{URL: "ws://h", Query: doc, OperationName: tt.saved}, noAuth, HTTPOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if b.OperationName != tt.name || b.OperationType != tt.typ {
			t.Errorf("saved %q: operation = %q %q, want %q %q", tt.saved, b.OperationName, b.OperationType, tt.name, tt.typ)
		}
		if b.Variables != nil || b.ConnectionParams != nil {
			t.Errorf("saved %q: variables %s, params %s", tt.saved, b.Variables, b.ConnectionParams)
		}
	}
}

func TestGraphQLWebSocketURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"http://h:8080/g?x=1": "ws://h:8080/g?x=1",
		"https://h/g":         "wss://h/g",
		"ws://h/g":            "ws://h/g",
		"WSS://h/g":           "wss://h/g",
	} {
		got, err := GraphQLWebSocketURL(in)
		if err != nil || got != want {
			t.Errorf("GraphQLWebSocketURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"ftp://h/g", "h/g", "://h"} {
		_, err := GraphQLWebSocketURL(in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeGraphQLWebSocketScheme {
			t.Errorf("GraphQLWebSocketURL(%q) err = %v", in, err)
		}
	}
}

func TestBuildGraphQLWSErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		f     projectfile.GraphQLRequest
		code  string
		field string
	}{
		{"no URL", projectfile.GraphQLRequest{Query: "{a}"}, CodeURLMissing, ""},
		{"bad scheme", projectfile.GraphQLRequest{URL: "ftp://h", Query: "{a}"}, CodeGraphQLWebSocketScheme, ""},
		{"no query", projectfile.GraphQLRequest{URL: "http://h", Query: " "}, CodeGraphQLQueryMissing, ""},
		{"bad variables", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Variables: "[1]"}, CodeGraphQLInvalidVariables, "variables"},
		{"bad params JSON", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", ConnectionParams: `{"a":`}, CodeGraphQLInvalidConnectionParams, "connectionParams"},
		{"params not an object", projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", ConnectionParams: `"tok"`}, CodeGraphQLInvalidConnectionParams, "connectionParams"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildGraphQLWS(tt.f, noAuth, HTTPOptions{})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code {
				t.Fatalf("err = %v, want code %s", err, tt.code)
			}
			if tt.field != "" && e.Details[DetailField] != tt.field {
				t.Errorf("field = %q, want %q", e.Details[DetailField], tt.field)
			}
		})
	}
}

func TestBuildGraphQLWSUndefinedVariableInParams(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{URL: "http://h", Query: "subscription { a }", ConnectionParams: `{"t": "{{missing}}"}`}
	_, err := BuildGraphQLWS(f, noAuth, HTTPOptions{Expand: buildExpand(nil)})
	var e *errs.Error
	if !errors.As(err, &e) || e.Details[DetailField] != "connectionParams" {
		t.Fatalf("err = %v, want an error on the connection params", err)
	}
}
