package collection

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestSSERequest creates an sse request, edits it through the auto-save
// pipeline and checks the file, the DTO shape (an http request's, kind
// "sse"), the round trip and LoadHTTPSource.
func TestSSERequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Prices", Kind: "sse", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "sse" || req.Method != "GET" || req.Auth.Type != projectfile.AuthInherit || req.Params == nil || req.Body.Fields == nil {
		t.Fatalf("new request = %+v", req)
	}
	file := readFile(t, dir, "prices.json")
	if bytes.Contains([]byte(file), []byte(`"method"`)) || bytes.Contains([]byte(file), []byte(`"body"`)) {
		t.Errorf("new file = %s", file)
	}

	timeout := int64(5000)
	req.URL = "http://localhost:8080/prices?symbol={{symbol}}"
	req.Params = []KeyValueDTO{{Name: "symbol", Value: "{{symbol}}", Enabled: true}}
	req.Headers = []KeyValueDTO{{Name: "Accept", Value: "text/event-stream", Enabled: true}}
	req.Settings.TimeoutMs = &timeout
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file = readFile(t, dir, "prices.json")
	want := `{
  "schemaVersion": 1,
  "kind": "sse",
  "id": "` + req.ID + `",
  "name": "Prices",
  "url": "http://localhost:8080/prices?symbol={{symbol}}",
  "params": [
    {
      "name": "symbol",
      "value": "{{symbol}}"
    }
  ],
  "headers": [
    {
      "name": "Accept",
      "value": "text/event-stream"
    }
  ],
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "timeoutMs": 5000
  },
  "x-note": "kept"
}
`
	if file != want {
		t.Errorf("file =\n%s\nwant\n%s", file, want)
	}

	// The DTO shape, with a fixed ID.
	got := getRequest(t, svc, req.ID)
	got.ID = "req_sse"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-sse", append(data, '\n'))

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

	// A POST with a body is kept.
	got.ID = req.ID
	got.Method = "POST"
	got.Body = BodyDTO{Mode: "raw", Type: "json", Text: `{"stream":true}`}
	update(t, svc, got, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if file := readFile(t, dir, "prices.json"); !bytes.Contains([]byte(file), []byte(`"method": "POST"`)) || !bytes.Contains([]byte(file), []byte(`"text": "{\"stream\":true}"`)) {
		t.Errorf("file = %s", file)
	}

	// Sent as the HTTP request it is, marked as a stream.
	src, err := LoadHTTPSource(svc, ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !src.SSE || src.GraphQL != nil || src.Request.Method != "POST" || src.Request.URL != req.URL || src.Request.ID != req.ID {
		t.Errorf("source = %+v", src)
	}
}
