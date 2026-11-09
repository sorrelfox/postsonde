package engineio

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func kindOf(err error) errs.Kind { return errs.KindOf(err) }

// Spec "Protocol > Packet encoding" and "WebSocket > Packet encoding":
// one packet per WebSocket message.
func TestWebSocketSpecExamples(t *testing.T) {
	tests := []struct {
		name   string
		wire   []byte
		binary bool
		want   Packet
	}{
		{"open", []byte(`0{"sid":"lv_VI97HAXpY6yYWAAAC","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000,"maxPayload":1000000}`), false,
			Text(Open, `{"sid":"lv_VI97HAXpY6yYWAAAC","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000,"maxPayload":1000000}`)},
		{"close", []byte("1"), false, Packet{Type: Close, Data: []byte{}}},
		{"ping", []byte("2"), false, Packet{Type: Ping, Data: []byte{}}},
		{"pong", []byte("3"), false, Packet{Type: Pong, Data: []byte{}}},
		{"probe ping", []byte("2probe"), false, Text(Ping, "probe")},
		{"probe pong", []byte("3probe"), false, Text(Pong, "probe")},
		{"text message", []byte("4hello"), false, Text(Message, "hello")},
		{"empty message", []byte("4"), false, Packet{Type: Message, Data: []byte{}}},
		{"unicode message", []byte("4€ 😀"), false, Text(Message, "€ 😀")},
		{"upgrade", []byte("5"), false, Packet{Type: Upgrade, Data: []byte{}}},
		{"noop", []byte("6"), false, Packet{Type: Noop, Data: []byte{}}},
		{"binary message", []byte{1, 2, 3, 4}, true, BinaryMessage([]byte{1, 2, 3, 4})},
		{"empty binary message", []byte{}, true, BinaryMessage([]byte{})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePacket(tc.wire, tc.binary, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
			wire, binary, err := EncodePacket(got)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire, tc.wire) || binary != tc.binary {
				t.Fatalf("encoded %q (binary %v), want %q (binary %v)", wire, binary, tc.wire, tc.binary)
			}
		})
	}
}

// engine.io-parser decodes the "b" Base64 form on every transport.
func TestWebSocketTextBase64(t *testing.T) {
	got, err := DecodePacket([]byte("bAQIDBA=="), false, Options{})
	if err != nil || !reflect.DeepEqual(got, BinaryMessage([]byte{1, 2, 3, 4})) {
		t.Fatalf("got %#v, %v", got, err)
	}
}

// Spec "HTTP long-polling > Packet encoding": packets joined by \x1e,
// binary as "b" + Base64.
func TestPayloadSpecExamples(t *testing.T) {
	tests := []struct {
		name string
		wire string
		want []Packet
	}{
		{"single", "4hello", []Packet{Text(Message, "hello")}},
		{"text and binary", "4hello\x1ebAQIDBA==", []Packet{Text(Message, "hello"), BinaryMessage([]byte{1, 2, 3, 4})}},
		{"heartbeat and messages", "2\x1e4a\x1e4b", []Packet{{Type: Ping, Data: []byte{}}, Text(Message, "a"), Text(Message, "b")}},
		{"close after noop", "6\x1e1", []Packet{{Type: Noop, Data: []byte{}}, {Type: Close, Data: []byte{}}}},
		{"empty binary", "b", []Packet{BinaryMessage([]byte{})}},
		{"empty text message", "4\x1e4", []Packet{{Type: Message, Data: []byte{}}, {Type: Message, Data: []byte{}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePayload([]byte(tc.wire), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
			wire, err := EncodePayload(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(wire) != tc.wire {
				t.Fatalf("encoded %q, want %q", wire, tc.wire)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		wire   string
		ws     bool
		code   string
		index  string
		offset string
	}{
		{"empty message", "", true, CodeEmptyPacket, "", ""},
		{"unknown type", "7", true, CodeUnknownType, "", ""},
		{"letter type", "x", true, CodeUnknownType, "", ""},
		{"v3 binary prefix", "\x04hello", true, CodeUnknownType, "", ""},
		{"invalid utf-8", "4\xff", true, CodeInvalidUTF8, "", ""},
		{"close with data", "1x", true, CodeUnexpectedData, "", ""},
		{"upgrade with data", "5x", true, CodeUnexpectedData, "", ""},
		{"noop with data", "6x", true, CodeUnexpectedData, "", ""},
		{"bad base64", "b!!", true, CodeInvalidBase64, "", ""},
		{"base64 without padding", "bAQIDBA", true, CodeInvalidBase64, "", ""},
		{"base64 with line break", "bAQID\nBA==", true, CodeInvalidBase64, "", ""},
		{"empty payload", "", false, CodeEmptyPacket, "", ""},
		{"empty record", "4a\x1e\x1e4b", false, CodeEmptyPacket, "1", "3"},
		{"trailing separator", "4a\x1e", false, CodeEmptyPacket, "1", "3"},
		{"bad second record", "4a\x1e9", false, CodeUnknownType, "1", "3"},
		{"bad base64 record", "2\x1e4x\x1eb%", false, CodeInvalidBase64, "2", "5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.ws {
				_, err = DecodePacket([]byte(tc.wire), false, Options{})
			} else {
				_, err = DecodePayload([]byte(tc.wire), Options{})
			}
			if codeOf(err) != tc.code || kindOf(err) != errs.Protocol {
				t.Fatalf("error %v (code %q, kind %q), want code %q", err, codeOf(err), kindOf(err), tc.code)
			}
			e, _ := errors.AsType[*errs.Error](err)
			if e.Details[DetailIndex] != tc.index || e.Details[DetailOffset] != tc.offset {
				t.Fatalf("details %v, want index %q offset %q", e.Details, tc.index, tc.offset)
			}
		})
	}
}

func TestSizeLimits(t *testing.T) {
	if _, err := DecodePacket([]byte("4hello"), false, Options{MaxSize: 5}); codeOf(err) != CodeTooBig {
		t.Fatalf("text: %v", err)
	}
	if _, err := DecodePacket([]byte("4hell"), false, Options{MaxSize: 5}); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if _, err := DecodePacket(make([]byte, 6), true, Options{MaxSize: 5}); codeOf(err) != CodeTooBig {
		t.Fatalf("binary: %v", err)
	}
	if _, err := DecodePayload([]byte("4a\x1e4b"), Options{MaxSize: 4}); codeOf(err) != CodeTooBig {
		t.Fatalf("payload: %v", err)
	}
	big := bytes.Repeat([]byte("x"), DefaultMaxSize+1)
	if _, err := DecodePacket(big, true, Options{}); codeOf(err) != CodeTooBig {
		t.Fatalf("default limit: %v", err)
	}
	if _, err := DecodePacket(big, true, Options{MaxSize: -1}); err != nil {
		t.Fatalf("no limit: %v", err)
	}
}

func TestEncodeErrors(t *testing.T) {
	tests := []struct {
		name string
		p    Packet
	}{
		{"unknown type", Packet{Type: 7}},
		{"binary ping", Packet{Type: Ping, Data: []byte{1}, Binary: true}},
		{"invalid utf-8", Text(Message, "\xff")},
		{"close with data", Text(Close, "x")},
		{"noop with data", Text(Noop, "x")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := EncodePacket(tc.p); codeOf(err) != CodeInvalidArgument || kindOf(err) != errs.InvalidArgument {
				t.Fatalf("EncodePacket: %v", err)
			}
			if _, err := EncodePayload([]Packet{Text(Message, "ok"), tc.p}); codeOf(err) != CodeInvalidArgument {
				t.Fatalf("EncodePayload: %v", err)
			} else if e, _ := errors.AsType[*errs.Error](err); e.Details[DetailIndex] != "1" {
				t.Fatalf("index: %v", e.Details)
			}
		})
	}
	if _, err := EncodePayload(nil); codeOf(err) != CodeInvalidArgument {
		t.Fatalf("empty payload: %v", err)
	}
	if _, err := EncodePayload([]Packet{Text(Message, "a\x1eb")}); codeOf(err) != CodeInvalidArgument {
		t.Fatalf("separator in text: %v", err)
	}
	// A text WebSocket message may contain \x1e.
	if _, _, err := EncodePacket(Text(Message, "a\x1eb")); err != nil {
		t.Fatalf("separator over WebSocket: %v", err)
	}
}

func TestDecodeDoesNotAlias(t *testing.T) {
	in := []byte("4hello")
	p, _ := DecodePacket(in, false, Options{})
	in[1] = 'j'
	if string(p.Data) != "hello" {
		t.Fatalf("text data aliases input: %q", p.Data)
	}
	bin := []byte{1, 2}
	p, _ = DecodePacket(bin, true, Options{})
	bin[0] = 9
	if p.Data[0] != 1 {
		t.Fatal("binary data aliases input")
	}
}

func TestPacketTypeString(t *testing.T) {
	if Open.String() != "open" || Noop.String() != "noop" || PacketType(9).String() != "PacketType(9)" {
		t.Fatal("String")
	}
}
