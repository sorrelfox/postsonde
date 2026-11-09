package collection

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestWebSocketRequest creates a websocket request, edits it through the
// auto-save pipeline and checks the file, the DTO shape and the round trip.
func TestWebSocketRequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Feed", Kind: "websocket", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "websocket" || req.URL != "" || req.Subprotocols == nil || len(req.Subprotocols) != 0 || req.Params == nil {
		t.Fatalf("new request = %+v", req)
	}

	timeout, cookies := int64(4000), false
	req.URL = "ws://localhost:8080/feed?room=lobby"
	req.Params = []KeyValueDTO{{Name: "room", Value: "lobby", Enabled: true}, {Name: "debug", Value: "1"}}
	req.Headers = []KeyValueDTO{{Name: "X-Client", Value: "postsonde", Enabled: true}}
	req.Subprotocols = []string{"chat.v1", "chat.v2"}
	req.Message, req.MessageFormat = `{"hello":1}`, "json"
	req.SavedMessages = []SavedMessageDTO{
		{Name: "Join", Text: `{"join":"{{room}}"}`, Format: "json"},
		{Name: "Bye", Text: "bye", Format: "text", Extra: `{"x-color":"red"}`},
	}
	req.Settings.ConnectTimeoutMs = &timeout
	req.Settings.SendCookies = &cookies
	ping, pong, reconnect, attempts, delay, maxDelay := int64(20000), int64(0), true, 0, int64(250), int64(8000)
	req.Settings.PingIntervalMs, req.Settings.PongTimeoutMs = &ping, &pong
	req.Settings.AutoReconnect, req.Settings.ReconnectMaxAttempts = &reconnect, &attempts
	req.Settings.ReconnectDelayMs, req.Settings.ReconnectMaxDelayMs = &delay, &maxDelay
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "feed.json")
	want := `{
  "schemaVersion": 1,
  "kind": "websocket",
  "id": "` + req.ID + `",
  "name": "Feed",
  "url": "ws://localhost:8080/feed?room=lobby",
  "params": [
    {
      "name": "room",
      "value": "lobby"
    },
    {
      "name": "debug",
      "value": "1",
      "disabled": true
    }
  ],
  "headers": [
    {
      "name": "X-Client",
      "value": "postsonde"
    }
  ],
  "subprotocols": [
    "chat.v1",
    "chat.v2"
  ],
  "message": "{\"hello\":1}",
  "messageFormat": "json",
  "savedMessages": [
    {
      "name": "Join",
      "text": "{\"join\":\"{{room}}\"}",
      "format": "json"
    },
    {
      "name": "Bye",
      "text": "bye",
      "x-color": "red"
    }
  ],
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "connectTimeoutMs": 4000,
    "sendCookies": false,
    "pingIntervalMs": 20000,
    "pongTimeoutMs": 0,
    "autoReconnect": true,
    "reconnectMaxAttempts": 0,
    "reconnectDelayMs": 250,
    "reconnectMaxDelayMs": 8000
  },
  "x-note": "kept"
}
`
	if file != want {
		t.Errorf("file =\n%s\nwant\n%s", file, want)
	}

	// The DTO shape, with a fixed ID.
	got := getRequest(t, svc, req.ID)
	got.ID = "req_ws"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-websocket", append(data, '\n'))

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

	// An empty subprotocol list, the text format and no saved messages are
	// left out.
	got.ID = req.ID
	got.Subprotocols, got.MessageFormat, got.SavedMessages = []string{}, "text", []SavedMessageDTO{}
	update(t, svc, got, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if file := readFile(t, dir, "feed.json"); bytes.Contains([]byte(file), []byte("subprotocols")) || bytes.Contains([]byte(file), []byte("messageFormat")) || bytes.Contains([]byte(file), []byte("savedMessages")) {
		t.Errorf("file = %s", file)
	}
}
