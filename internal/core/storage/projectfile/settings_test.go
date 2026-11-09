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

// TestRequestProxyRoundTrip: settings.proxy of every request kind
// survives a write and a read, its unknown members included; "inherit" is
// written as a missing member, and a file without it (as every file
// written before it existed) reads as inherit and writes no settings.
func TestRequestProxyRoundTrip(t *testing.T) {
	t.Parallel()
	proxy := RequestProxy{
		Mode:         ProxyCustom,
		URL:          "socks5h://{{proxyUser}}@proxy.lan:1080",
		Password:     "{{proxyPassword}}",
		LocalSecrets: []string{ProxyPasswordMember},
		Unknown:      Unknown{"note": []byte(`"kept"`)},
	}
	h := func(k Kind) Head { return Head{SchemaVersion: CurrentVersion, Kind: k} }
	files := []File{
		&HTTPRequest{Head: h(KindHTTP), ID: "req_h", Name: "h", Method: "GET", URL: "http://x/", Auth: Auth{Type: AuthInherit}, Settings: RequestSettings{Proxy: proxy}},
		&GraphQLRequest{Head: h(KindGraphQL), ID: "req_g", Name: "g", URL: "http://x/", Auth: Auth{Type: AuthInherit}, Settings: RequestSettings{Proxy: proxy}},
		&SSERequest{Head: h(KindSSE), ID: "req_s", Name: "s", URL: "http://x/", Auth: Auth{Type: AuthInherit}, Settings: RequestSettings{Proxy: proxy}},
		&TCPRequest{Head: h(KindTCP), ID: "req_t", Name: "t", Address: "x:1", Settings: TCPSettings{Proxy: proxy}},
		&UDPRequest{Head: h(KindUDP), ID: "req_u", Name: "u", Address: "x:1", Settings: UDPSettings{Proxy: proxy}},
		&WebSocketRequest{Head: h(KindWebSocket), ID: "req_w", Name: "w", URL: "ws://x/", Settings: WebSocketSettings{Proxy: proxy}},
		&SocketIORequest{Head: h(KindSocketIO), ID: "req_i", Name: "i", URL: "http://x/", Settings: SocketIOSettings{Proxy: proxy}},
		&GRPCRequest{Head: h(KindGRPC), ID: "req_r", Name: "r", Address: "x:1", Settings: GRPCSettings{Proxy: proxy}},
	}
	for _, f := range files {
		data, err := Marshal(f)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		if !strings.Contains(string(data), `"proxy": {`) || !strings.Contains(string(data), `"note": "kept"`) {
			t.Errorf("%T: file = %s", f, data)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		if p := ProxyOf(got); p == nil || !reflect.DeepEqual(*p, proxy) {
			t.Errorf("%T: read back %+v, want %+v", f, p, proxy)
		}
		again, _ := Marshal(got)
		if string(again) != string(data) {
			t.Errorf("%T: not stable:\n%s\n%s", f, data, again)
		}

		// Inherit is the default: left out, with the settings.
		*ProxyOf(got) = RequestProxy{}
		if data, err = Marshal(got); err != nil || strings.Contains(string(data), "proxy") || strings.Contains(string(data), "settings") {
			t.Errorf("%T: inherit written: %s, %v", f, data, err)
		}
		*ProxyOf(got) = RequestProxy{Mode: ProxyNone}
		if data, _ = Marshal(got); !strings.Contains(string(data), `"proxy": {`+"\n"+`      "mode": "none"`) {
			t.Errorf("%T: none = %s", f, data)
		}
	}

	old := `{"schemaVersion": 1, "kind": "tcp", "id": "req_t", "name": "t", "address": "x:1", "settings": {"tls": true}}`
	got, err := Decode([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if p := ProxyOf(got); p == nil || p.EffectiveMode() != ProxyInherit {
		t.Errorf("an old file's proxy = %+v, want inherit", p)
	}
	written := `{"schemaVersion": 1, "kind": "udp", "id": "req_u", "name": "u", "address": "x:1", "settings": {"proxy": {"mode": "inherit"}}}`
	if got, err = Decode([]byte(written)); err != nil {
		t.Fatal(err)
	}
	if data, _ := Marshal(got); strings.Contains(string(data), "settings") {
		t.Errorf("an explicit inherit is written back: %s", data)
	}
	if ProxyOf(&Folder{}) != nil {
		t.Error("ProxyOf(folder) is not nil")
	}
}
