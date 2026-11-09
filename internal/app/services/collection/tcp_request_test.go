package collection

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestTCPRequest creates a tcp request, edits it through the auto-save
// pipeline and checks the file, the DTO shape and the round trip.
func TestTCPRequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Echo", Kind: "tcp", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "tcp" || req.Address != "" || req.SendMode != "" {
		t.Fatalf("new request = %+v", req)
	}

	timeout := int64(2500)
	req.Address, req.SendMode, req.LineEnding, req.Message = "localhost:7000", "hex", "crlf", "DE AD"
	req.Settings.ConnectTimeoutMs = &timeout
	on, off := true, false
	req.Settings.TLS, req.Settings.VerifyTLS = &on, &off
	if req.Framing == nil || req.Framing.Mode != "" {
		t.Fatalf("new request framing = %+v", req.Framing)
	}
	req.Framing = &TCPFramingDTO{Mode: "newline", Delimiter: "crlf"}
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "echo.json")
	want := `{
  "schemaVersion": 1,
  "kind": "tcp",
  "id": "` + req.ID + `",
  "name": "Echo",
  "address": "localhost:7000",
  "sendMode": "hex",
  "lineEnding": "crlf",
  "message": "DE AD",
  "framing": {
    "mode": "newline",
    "delimiter": "crlf"
  },
  "settings": {
    "connectTimeoutMs": 2500,
    "tls": true,
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
	got.ID = "req_tcp"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-tcp", append(data, '\n'))

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
}
