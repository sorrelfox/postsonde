package projectfile

import (
	"reflect"
	"strings"
	"testing"
)

// TestRequestSettingsRedirectMembers: the redirect (HTTP-22), TLS
// (HTTP-23), URL encoding (HTTP-24), HTTP version (HTTP-25) and cookie
// (HTTP-26) members of an http request's settings survive a write and a
// read, and a request without them writes no settings.
func TestRequestSettingsRedirectMembers(t *testing.T) {
	t.Parallel()
	want := RequestSettings{
		FollowRedirects:      ptr(true),
		MaxRedirects:         ptr(0),
		FollowOriginalMethod: ptr(true),
		ForwardAuthorization: ptr(false),
		VerifyTLS:            ptr(false),
		URLEncoding:          URLEncodingRFC3986,
		HTTPVersion:          HTTPVersion2,
		SendCookies:          ptr(false),
		StoreCookies:         ptr(false),
	}
	f := &HTTPRequest{Head: Head{SchemaVersion: CurrentVersion, Kind: KindHTTP}, ID: "req_0123456789abcdef", Name: "r", Method: "QUERY", URL: "https://x/", Settings: want}
	data, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`"maxRedirects": 0`, `"followOriginalMethod": true`, `"forwardAuthorization": false`, `"verifyTls": false`,
		`"urlEncoding": "rfc3986"`, `"httpVersion": "http2"`, `"sendCookies": false`, `"storeCookies": false`} {
		if !strings.Contains(string(data), member) {
			t.Errorf("file lacks %s:\n%s", member, data)
		}
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.(*HTTPRequest).Settings; !reflect.DeepEqual(s, want) {
		t.Errorf("read back %+v, want %+v", s, want)
	}

	f.Settings = RequestSettings{}
	if data, err = Marshal(f); err != nil || strings.Contains(string(data), "settings") {
		t.Errorf("empty settings written: %s, %v", data, err)
	}
}
