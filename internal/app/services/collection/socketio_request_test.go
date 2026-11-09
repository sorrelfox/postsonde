package collection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestSocketIORequest creates a socketio request, edits it through the
// auto-save pipeline and checks the file, the DTO shape and the round trip
// (SIO-01, SIO-06, SIO-07).
func TestSocketIORequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Chat", Kind: "socketio", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "socketio" || req.URL != "" || req.Params == nil || req.Transport != "" || req.Listen != nil {
		t.Fatalf("new request = %+v", req)
	}

	ackTimeout, verify := int64(2500), false
	req.URL = "https://localhost:3000/admin?room=lobby"
	req.Params = []KeyValueDTO{{Name: "room", Value: "lobby", Enabled: true}}
	req.Headers = []KeyValueDTO{{Name: "X-Client", Value: "postsonde", Enabled: true}}
	req.Path = "/rt/"
	req.Namespace = "/admin"
	req.AuthPayload = `{"token":"{{token}}"}`
	req.Transport = "polling"
	req.SocketIOVersion = "v2"
	req.Event, req.Message, req.Ack = "chat", `["hi", {"$base64": "AAE="}]`, true
	req.SavedMessages = []SavedMessageDTO{
		{Name: "Join", Text: `["lobby"]`, Event: "join", Ack: true},
		{Name: "Ping", Event: "ping", Extra: `{"x-color":"red"}`},
	}
	req.Listen = []string{"chat", "typing"}
	req.Settings.AckTimeoutMs = &ackTimeout
	req.Settings.VerifyTLS = &verify
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "chat.json")
	want := `{
  "schemaVersion": 1,
  "kind": "socketio",
  "id": "` + req.ID + `",
  "name": "Chat",
  "url": "https://localhost:3000/admin?room=lobby",
  "params": [
    {
      "name": "room",
      "value": "lobby"
    }
  ],
  "headers": [
    {
      "name": "X-Client",
      "value": "postsonde"
    }
  ],
  "path": "/rt/",
  "namespace": "/admin",
  "authPayload": "{\"token\":\"{{token}}\"}",
  "transport": "polling",
  "version": "v2",
  "event": "chat",
  "args": "[\"hi\", {\"$base64\": \"AAE=\"}]",
  "ack": true,
  "savedEmits": [
    {
      "name": "Join",
      "event": "join",
      "args": "[\"lobby\"]",
      "ack": true
    },
    {
      "name": "Ping",
      "event": "ping",
      "x-color": "red"
    }
  ],
  "listen": [
    "chat",
    "typing"
  ],
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "ackTimeoutMs": 2500,
    "verifyTls": false
  },
  "x-note": "kept"
}
`
	if file != want {
		t.Errorf("file =\n%s\nwant\n%s", file, want)
	}

	// The DTO shape, with a fixed ID.
	got := getRequest(t, svc, req.ID)
	got.ID = "req_sio"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-socketio", append(data, '\n'))

	// File -> DTO -> JSON -> DTO -> file gives the same bytes.
	f, err := projectfile.Decode([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(toRequestDTO(f))
	if err != nil {
		t.Fatal(err)
	}
	var dto RequestDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	back, err := dto.toFile()
	if err != nil {
		t.Fatal(err)
	}
	again, err := projectfile.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, []byte(file)) {
		t.Errorf("round trip =\n%s\nwant\n%s", again, file)
	}

	// The defaults are left out: auto, the default path, no listen filter.
	got.ID = req.ID
	got.Transport, got.Path, got.Listen, got.SavedMessages = "auto", "/socket.io/", []string{}, nil
	update(t, svc, got, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file = readFile(t, dir, "chat.json")
	for _, member := range []string{`"transport"`, `"path"`, `"listen"`, `"savedEmits"`} {
		if strings.Contains(file, member) {
			t.Errorf("%s written:\n%s", member, file)
		}
	}
}
