package engineio

import (
	"bytes"
	"reflect"
	"testing"
)

// The examples of the Engine.IO protocol 3 document (engine.io-protocol
// v3 "Encoding", engine.io-parser 2.x): WebSocket messages and both forms
// of long-polling payloads.
func TestV3WebSocket(t *testing.T) {
	tests := []struct {
		name   string
		wire   []byte
		binary bool
		want   Packet
	}{
		{"text", []byte("4hello"), false, Text(Message, "hello")},
		{"ping", []byte("2"), false, Packet{Type: Ping, Data: []byte{}}},
		{"probe", []byte("2probe"), false, Text(Ping, "probe")},
		{"binary", []byte{4, 1, 2, 3}, true, BinaryMessage([]byte{1, 2, 3})},
		{"empty binary", []byte{4}, true, BinaryMessage([]byte{})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePacketV3(tc.wire, tc.binary, Options{})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, %v", got, err)
			}
			wire, binary, err := EncodePacketV3(got)
			if err != nil || !bytes.Equal(wire, tc.wire) || binary != tc.binary {
				t.Fatalf("encoded %q %v %v", wire, binary, err)
			}
		})
	}
	if _, err := DecodePacketV3([]byte{2, 1}, true, Options{}); codeOf(err) != CodeUnknownType {
		t.Errorf("binary non-message: %v", err)
	}
	if _, err := DecodePacketV3(nil, true, Options{}); codeOf(err) != CodeEmptyPacket {
		t.Errorf("empty binary: %v", err)
	}
	// A Base64 message in a text frame is decoded, as engine.io-parser 2 does.
	if p, err := DecodePacketV3([]byte("b4AQI="), false, Options{}); err != nil || !reflect.DeepEqual(p, BinaryMessage([]byte{1, 2})) {
		t.Errorf("b4 = %#v, %v", p, err)
	}
}

func TestV3Payloads(t *testing.T) {
	packets := []Packet{
		Text(Open, `{"sid":"abc","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000}`),
		Text(Message, "0"),
		Text(Message, `2["€ 😀"]`),
		BinaryMessage([]byte{1, 2, 3, 4}),
	}
	text := `79:0{"sid":"abc","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000}2:4010:42["€ 😀"]10:b4AQIDBA==`
	got, err := EncodePayloadV3(packets)
	if err != nil || string(got) != text {
		t.Fatalf("text payload = %q, %v", got, err)
	}
	back, err := DecodePayloadV3(got, Options{})
	if err != nil || !reflect.DeepEqual(back, packets) {
		t.Fatalf("decoded %#v, %v", back, err)
	}
	if !LooksLikeV3Payload(got) || LooksLikeV3Payload([]byte(`0{"sid":"x"}`)) || LooksLikeV3Payload([]byte("4")) {
		t.Error("LooksLikeV3Payload")
	}

	bin, err := EncodeBinaryPayloadV3([]Packet{Text(Message, "2[\"a\"]"), BinaryMessage([]byte{9, 8})})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0, 7, 255}, "42[\"a\"]"...)
	want = append(want, 1, 3, 255, 4, 9, 8)
	if !bytes.Equal(bin, want) {
		t.Fatalf("binary payload = %v, want %v", bin, want)
	}
	back, err = DecodePayloadV3(bin, Options{})
	if err != nil || !reflect.DeepEqual(back, []Packet{Text(Message, "2[\"a\"]"), BinaryMessage([]byte{9, 8})}) {
		t.Fatalf("binary decoded %#v, %v", back, err)
	}

	for _, bad := range []string{"", "4hello", "5:4", "x:4", "3:4a", "2:b1AA"} {
		if _, err := DecodePayloadV3([]byte(bad), Options{}); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
	for _, bad := range [][]byte{{0, 5, 255, '4'}, {2, 1, 255, '4'}, {1, 1, 255, 2}, {0, 12, 255, '4'}} {
		if _, err := DecodePayloadV3(bad, Options{}); err == nil {
			t.Errorf("%v decoded", bad)
		}
	}
}
