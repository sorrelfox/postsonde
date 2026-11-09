package engineio

import (
	"bytes"
	"testing"
)

// FuzzDecode checks that whatever decodes encodes back to the same bytes
// (the codec is exact on valid input), over WebSocket and long-polling.
func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		"0{\"sid\":\"x\",\"upgrades\":[],\"pingInterval\":1,\"pingTimeout\":1}", "1", "2probe", "3", "4hello",
		"5", "6", "bAQIDBA==", "4a\x1e4b", "4hello\x1ebAQIDBA==", "2\x1e", "", "\x1e", "4\xff",
	} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, data []byte, binary bool) {
		p, err := DecodePacket(data, binary, Options{MaxSize: 1 << 16})
		if err == nil {
			wire, bin, err := EncodePacket(p)
			if err != nil {
				t.Fatalf("decoded %q but cannot encode it: %v", data, err)
			}
			// The "b" form in a text message is the one input that
			// comes back in another form.
			if !(len(data) > 0 && data[0] == 'b' && !binary) && (!bytes.Equal(wire, data) || bin != binary) {
				t.Fatalf("%q (binary %v) encoded back as %q (binary %v)", data, binary, wire, bin)
			}
		}
		packets, err := DecodePayload(data, Options{MaxSize: 1 << 16})
		if err != nil {
			return
		}
		wire, err := EncodePayload(packets)
		if err != nil {
			t.Fatalf("decoded %q but cannot encode it: %v", data, err)
		}
		again, err := DecodePayload(wire, Options{MaxSize: -1})
		if err != nil || len(again) != len(packets) {
			t.Fatalf("re-encoded %q does not decode: %v", wire, err)
		}
		for i := range packets {
			if again[i].Type != packets[i].Type || again[i].Binary != packets[i].Binary || !bytes.Equal(again[i].Data, packets[i].Data) {
				t.Fatalf("packet %d changed: %#v -> %#v", i, packets[i], again[i])
			}
		}
	})
}
