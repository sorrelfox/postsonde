package projectfile

import (
	"strings"
	"testing"
)

// TestHandshakeAuthMember (AUTH-31): a websocket, socketio or grpc file
// written before auth applied to handshakes has no auth member and reads
// as no auth; "none" is written as a missing member again, so such a file
// is not changed by a save; any other auth, its unknown members and an
// unknown authIn are kept.
func TestHandshakeAuthMember(t *testing.T) {
	t.Parallel()
	old := map[Kind]string{
		KindWebSocket: `{"schemaVersion": 1, "kind": "websocket", "id": "req_w", "name": "W", "url": "ws://h/"}`,
		KindSocketIO:  `{"schemaVersion": 1, "kind": "socketio", "id": "req_s", "name": "S", "url": "http://h/"}`,
		KindGRPC:      `{"schemaVersion": 1, "kind": "grpc", "id": "req_g", "name": "G", "address": "h:1"}`,
	}
	for kind, text := range old {
		f, err := Decode([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if a := HandshakeAuthOf(authOfFile(f)); a.Type != AuthNone {
			t.Errorf("%s: auth = %+v", kind, a)
		}
		data, err := Marshal(f)
		if err != nil || strings.Contains(string(data), `"auth"`) {
			t.Errorf("%s: written %s, %v", kind, data, err)
		}
	}

	ws := &WebSocketRequest{ID: "req_w", Name: "W", Auth: Auth{Type: AuthNone}}
	if data, _ := Marshal(ws); strings.Contains(string(data), `"auth"`) {
		t.Errorf("none written: %s", data)
	}
	ws.Auth = Auth{Type: AuthInherit}
	if data, _ := Marshal(ws); !strings.Contains(string(data), `"auth": {`+"\n"+`    "type": "inherit"`) {
		t.Errorf("inherit written: %s", data)
	}

	text := `{"schemaVersion": 1, "kind": "socketio", "id": "req_s", "name": "S", "url": "http://h/",
  "authIn": "payload", "auth": {"type": "bearer", "token": "{{t}}", "x-new": 1}}`
	f, err := Decode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	s := f.(*SocketIORequest)
	if s.AuthIn != SocketIOAuthPayload || s.Auth.Type != AuthBearer || s.Auth.Token != "{{t}}" || string(s.Auth.Unknown["x-new"]) != "1" {
		t.Errorf("decoded %+v", s)
	}
	data, err := Marshal(s)
	if err != nil || !strings.Contains(string(data), `"authIn": "payload"`) || !strings.Contains(string(data), `"x-new": 1`) {
		t.Errorf("written %s, %v", data, err)
	}
	s.AuthIn = SocketIOAuthHandshake
	if data, _ := Marshal(s); strings.Contains(string(data), "authIn") {
		t.Errorf("the default authIn written: %s", data)
	}
	g := &GRPCRequest{ID: "req_g", Name: "G", Auth: Auth{Type: "kerberos", Unknown: Unknown{"realm": []byte(`"X"`)}}}
	if data, _ := Marshal(g); !strings.Contains(string(data), `"type": "kerberos"`) || !strings.Contains(string(data), `"realm": "X"`) {
		t.Errorf("an unknown type written: %s", data)
	}
}

func authOfFile(f File) Auth {
	switch r := f.(type) {
	case *WebSocketRequest:
		return r.Auth
	case *SocketIORequest:
		return r.Auth
	case *GRPCRequest:
		return r.Auth
	}
	return Auth{Type: "?"}
}
