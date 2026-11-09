package socketio

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
)

// frame is one line of testdata/reference-v4.txt.
type frame struct {
	line      int
	dir       string // ">" client to server, "<" server to client
	transport string // polling, ws-text, ws-binary
	data      []byte
}

func loadReference(t *testing.T) (sessions [][]frame) {
	t.Helper()
	f, err := os.Open("testdata/reference-v4.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if strings.HasPrefix(line, "# session:") {
			sessions = append(sessions, nil)
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dir, rest, _ := strings.Cut(line, " ")
		transport, data, _ := strings.Cut(rest, " ")
		fr := frame{line: n, dir: dir, transport: transport}
		if transport == "ws-binary" {
			fr.data, err = hex.DecodeString(data)
		} else {
			var s string
			err = json.Unmarshal([]byte(data), &s)
			fr.data = []byte(s)
		}
		if err != nil || (dir != ">" && dir != "<") || len(sessions) == 0 {
			t.Fatalf("line %d: bad fixture line %q (%v)", n, line, err)
		}
		sessions[len(sessions)-1] = append(sessions[len(sessions)-1], fr)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return sessions
}

// direction holds the Socket.IO decoding state of one direction.
type direction struct {
	dec  *Decoder
	text string
	atts [][]byte
}

// TestReferenceRoundTrip decodes the traffic captured from a socket.io
// 4.8.1 server and client session and checks that every Engine.IO frame and
// Socket.IO packet encodes back to the captured bytes.
func TestReferenceRoundTrip(t *testing.T) {
	sessions := loadReference(t)
	if len(sessions) != 3 {
		t.Fatalf("%d sessions", len(sessions))
	}
	eioTypes := map[engineio.PacketType]int{}
	sioTypes := map[PacketType]int{}
	var decoded []Packet
	for _, frames := range sessions {
		dirs := map[string]*direction{">": {dec: NewDecoder(Options{})}, "<": {dec: NewDecoder(Options{})}}
		for _, fr := range frames {
			var packets []engineio.Packet
			switch fr.transport {
			case "polling":
				ps, err := engineio.DecodePayload(fr.data, engineio.Options{})
				if err != nil {
					t.Fatalf("line %d: %v", fr.line, err)
				}
				wire, err := engineio.EncodePayload(ps)
				if err != nil || !bytes.Equal(wire, fr.data) {
					t.Fatalf("line %d: payload encodes as %q (%v), captured %q", fr.line, wire, err, fr.data)
				}
				packets = ps
			case "ws-text", "ws-binary":
				binary := fr.transport == "ws-binary"
				p, err := engineio.DecodePacket(fr.data, binary, engineio.Options{})
				if err != nil {
					t.Fatalf("line %d: %v", fr.line, err)
				}
				wire, bin, err := engineio.EncodePacket(p)
				if err != nil || !bytes.Equal(wire, fr.data) || bin != binary {
					t.Fatalf("line %d: packet encodes as %q (%v), captured %q", fr.line, wire, err, fr.data)
				}
				packets = []engineio.Packet{p}
			default:
				t.Fatalf("line %d: unknown transport %q", fr.line, fr.transport)
			}
			d := dirs[fr.dir]
			for _, ep := range packets {
				eioTypes[ep.Type]++
				if ep.Type == engineio.Open {
					h, err := engineio.ParseHandshake(ep.Data)
					if err != nil {
						t.Fatalf("line %d: %v", fr.line, err)
					}
					again, err := h.Packet()
					if err != nil || !bytes.Equal(again.Data, ep.Data) {
						t.Fatalf("line %d: handshake encodes as %s (%v)", fr.line, again.Data, err)
					}
				}
				if ep.Type != engineio.Message {
					continue
				}
				if ep.Binary {
					d.atts = append(d.atts, ep.Data)
				} else {
					d.text, d.atts = string(ep.Data), nil
				}
				p, err := d.dec.Feed(ep)
				if err != nil {
					t.Fatalf("line %d: %v", fr.line, err)
				}
				if p == nil {
					continue
				}
				text, atts, err := Encode(*p)
				if err != nil || text != d.text || !reflect.DeepEqual(atts, d.atts) && len(atts)+len(d.atts) > 0 {
					t.Fatalf("line %d: packet encodes as %q %x (%v), captured %q %x", fr.line, text, atts, err, d.text, d.atts)
				}
				if _, err := p.Value(); err != nil {
					t.Fatalf("line %d: Value: %v", fr.line, err)
				}
				sioTypes[p.Type]++
				decoded = append(decoded, *p)
			}
		}
		for name, d := range dirs {
			if d.dec.Waiting() != 0 {
				t.Fatalf("direction %s ends waiting for attachments", name)
			}
		}
	}
	for ty := engineio.Open; ty <= engineio.Noop; ty++ {
		if eioTypes[ty] == 0 {
			t.Errorf("the fixture has no Engine.IO %s packet", ty)
		}
	}
	for ty := Connect; ty <= BinaryAck; ty++ {
		if sioTypes[ty] == 0 {
			t.Errorf("the fixture has no Socket.IO %s packet", ty)
		}
	}

	// Spot checks of reconstructed values.
	find := func(ty PacketType, name string) Packet {
		for _, p := range decoded {
			if p.Type == ty && (name == "" || p.EventName() == name) {
				return p
			}
		}
		t.Fatalf("no %s %s packet", ty, name)
		return Packet{}
	}
	args, _ := find(BinaryEvent, "bytes").Args()
	if want := []any{"bytes", []byte{1, 2, 3}, map[string]any{"nested": []any{[]byte("hi")}}}; !reflect.DeepEqual(args, want) {
		t.Errorf("bytes event: %#v", args)
	}
	args, _ = find(Event, "unicode").Args()
	if want := []any{"unicode", "héllo ✓ 😀", nil, true, json.Number("1.5"), json.Number("-2")}; !reflect.DeepEqual(args, want) {
		t.Errorf("unicode event: %#v", args)
	}
	ack := find(BinaryAck, "")
	args, _ = ack.Args()
	if !ack.HasID || len(args) != 2 {
		t.Errorf("binary ack: %+v %#v", ack, args)
	}
	ce := find(ConnectError, "")
	if v, _ := ce.Value(); ce.Namespace != "/admin" || v.(map[string]any)["message"] != "Not authorized" {
		t.Errorf("connect error: %+v", ce)
	}
}
