package request

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func codeOfErr(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// TestHandshakeAuthApplies: the types that sign or answer an HTTP request
// are refused on every handshake, with the protocol named; a query
// parameter is refused on gRPC only; disabled and none send nothing
// (AUTH-31).
func TestHandshakeAuthApplies(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{projectfile.AuthDigest, projectfile.AuthOAuth1, projectfile.AuthAWSV4, projectfile.AuthHawk} {
		for _, p := range []HandshakeProtocol{HandshakeWebSocket, HandshakeSocketIO, HandshakeGRPC} {
			_, err := HandshakeAuth(projectfile.Auth{Type: typ, Username: "u", Password: "p"}, p, AuthOptions{})
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != CodeAuthNotApplicable || e.Kind != errs.Unsupported || e.Details[DetailAuthType] != typ || e.Details[DetailAuthProtocol] != string(p) {
				t.Errorf("%s on %s: %v", typ, p, err)
			}
		}
		if _, err := HandshakeAuth(projectfile.Auth{Type: typ, Disabled: true}, HandshakeWebSocket, AuthOptions{}); err != nil {
			t.Errorf("disabled %s: %v", typ, err)
		}
	}
	query := projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: "v", In: projectfile.APIKeyInQuery}
	if _, err := HandshakeAuth(query, HandshakeGRPC, AuthOptions{}); codeOfErr(err) != CodeAuthNotApplicable {
		t.Errorf("query on grpc: %v", err)
	}
	r, err := HandshakeAuth(query, HandshakeWebSocket, AuthOptions{})
	if err != nil || !reflect.DeepEqual(r.Query, []QueryParam{{Name: "k", Value: "v"}}) {
		t.Errorf("query on websocket = %+v, %v", r, err)
	}
	if r, err := HandshakeAuth(projectfile.Auth{Type: projectfile.AuthNone}, HandshakeGRPC, AuthOptions{}); err != nil || r.Headers != nil {
		t.Errorf("none = %+v, %v", r, err)
	}
}

// TestHandshakeCredentials: the values each type sends, for the
// Socket.IO auth payload.
func TestHandshakeCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		auth projectfile.Auth
		want *Credentials
	}{
		{projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{t}}"}, &Credentials{Token: "tok"}},
		{projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "{{t}}"}, &Credentials{Username: "u", Password: "tok"}},
		{projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Key", Value: "{{t}}"}, &Credentials{Key: "X-Key", Value: "tok"}},
		{projectfile.Auth{Type: projectfile.AuthBearer}, nil},
	}
	expand := func(s string) (string, error) {
		if s == "{{t}}" {
			return "tok", nil
		}
		return s, nil
	}
	for _, tt := range tests {
		r, err := HandshakeAuth(tt.auth, HandshakeSocketIO, AuthOptions{Expand: expand})
		if err != nil || !reflect.DeepEqual(r.Credentials, tt.want) {
			t.Errorf("%s: credentials = %+v, %v", tt.auth.Type, r.Credentials, err)
		}
	}
}

func TestWithPayloadCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		payload string
		c       *Credentials
		want    string
	}{
		{"", &Credentials{Token: "t"}, `{"token":"t"}`},
		{` {"room": "a"} `, &Credentials{Token: "t"}, `{"room": "a","token":"t"}`},
		{`{"token": "mine"}`, &Credentials{Token: "t"}, `{"token": "mine"}`},
		{`{}`, &Credentials{Username: "u", Password: "p"}, `{"username":"u","password":"p"}`},
		{`{"a":1}`, &Credentials{Key: "api_key", Value: "v\"x"}, `{"a":1,"api_key":"v\"x"}`},
		{`{"a":1}`, nil, `{"a":1}`},
		{"", nil, ""},
	}
	for _, tt := range tests {
		got, err := WithPayloadCredentials(tt.payload, tt.c)
		if err != nil || got != tt.want {
			t.Errorf("WithPayloadCredentials(%q) = %q, %v; want %q", tt.payload, got, err, tt.want)
		}
	}
	// Socket.IO v2: the same members as query parameters.
	if got := PayloadQuery(&Credentials{Username: "u", Password: "p"}); !reflect.DeepEqual(got, []QueryParam{{"username", "u"}, {"password", "p"}}) {
		t.Errorf("PayloadQuery = %+v", got)
	}
	if got := PayloadQuery(nil); got != nil {
		t.Errorf("PayloadQuery(nil) = %+v", got)
	}
	for _, bad := range []string{"[1]", "null", "{", `"x"`} {
		if _, err := WithPayloadCredentials(bad, &Credentials{Token: "t"}); codeOfErr(err) != CodeAuthPayloadInvalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// TestHandshakeHeaders: an enabled row of the same name wins, a
// disabled one does not; gRPC keys are in lower case.
func TestHandshakeHeaders(t *testing.T) {
	t.Parallel()
	auth := []httpclient.Header{{Name: "Authorization", Value: "Bearer t", Enabled: true}, {Name: "X-Key", Value: "k", Enabled: true}}
	rows := []httpclient.Header{{Name: "authorization", Value: "mine", Enabled: true}, {Name: "X-Key", Value: "off"}}
	got := HandshakeHeaders(rows, auth, false)
	if !reflect.DeepEqual(got, []httpclient.Header{{Name: "X-Key", Value: "k", Enabled: true}}) {
		t.Errorf("headers = %+v", got)
	}
	got = HandshakeHeaders(nil, auth, true)
	if len(got) != 2 || got[0].Name != "authorization" || got[1].Name != "x-key" {
		t.Errorf("metadata = %+v", got)
	}
}
