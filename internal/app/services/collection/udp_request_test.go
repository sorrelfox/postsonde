package collection

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestUDPRequest creates a udp request (UDP-01), edits it through the
// auto-save pipeline and checks the file, the DTO shape and the round
// trip, unknown members included.
func TestUDPRequest(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "DNS probe", Kind: "udp", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Kind != "udp" || req.Address != "" || req.SendMode != "" || req.UDP == nil || req.UDP.Connected || req.Framing != nil {
		t.Fatalf("new request = %+v", req)
	}

	maxBytes, timeout := int64(1500), int64(0)
	req.Address, req.SendMode, req.Message = "{{dns}}:53", "base64", "EjQBAA=="
	req.UDP = &UDPOptionsDTO{Connected: true, IPVersion: "ipv6", MaxDatagramBytes: &maxBytes, OpenTimeoutMs: &timeout}
	req.Settings.Extra = `{"ttl":4}`
	req.Extra = `{"x-note":"kept"}`
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "dns-probe.json")
	want := `{
  "schemaVersion": 1,
  "kind": "udp",
  "id": "` + req.ID + `",
  "name": "DNS probe",
  "address": "{{dns}}:53",
  "sendMode": "base64",
  "message": "EjQBAA==",
  "settings": {
    "connected": true,
    "ipVersion": "ipv6",
    "maxDatagramBytes": 1500,
    "openTimeoutMs": 0,
    "ttl": 4
  },
  "x-note": "kept"
}
`
	if file != want {
		t.Errorf("file =\n%s\nwant\n%s", file, want)
	}

	got := getRequest(t, svc, req.ID)
	got.ID = "req_udp"
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/request-udp", append(data, '\n'))

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
