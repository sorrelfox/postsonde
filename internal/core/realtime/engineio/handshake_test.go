package engineio

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Spec "Handshake": the open packet's data.
func TestParseHandshakeSpecExample(t *testing.T) {
	data := `{"sid":"lv_VI97HAXpY6yYWAAAC","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000,"maxPayload":1000000}`
	h, err := ParseHandshake([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := Handshake{SID: "lv_VI97HAXpY6yYWAAAC", Upgrades: []string{"websocket"}, PingInterval: 25000, PingTimeout: 5000, MaxPayload: 1000000}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("got %+v", h)
	}
	if h.PingIntervalDuration() != 25*time.Second || h.PingTimeoutDuration() != 5*time.Second {
		t.Fatal("durations")
	}
	p, err := h.Packet()
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != Open || string(p.Data) != data {
		t.Fatalf("Packet() = %d %s", p.Type, p.Data)
	}
}

func TestParseHandshakeLenient(t *testing.T) {
	// Unknown fields are ignored, maxPayload is optional, upgrades may be
	// empty (a WebSocket-only connection).
	h, err := ParseHandshake([]byte(` {"sid":"a","upgrades":[],"pingInterval":1,"pingTimeout":2,"extra":{"x":1}} `))
	if err != nil {
		t.Fatal(err)
	}
	if h.MaxPayload != 0 || len(h.Upgrades) != 0 || h.Upgrades == nil {
		t.Fatalf("got %+v", h)
	}
	p, _ := h.Packet()
	if string(p.Data) != `{"sid":"a","upgrades":[],"pingInterval":1,"pingTimeout":2}` {
		t.Fatalf("Packet() = %s", p.Data)
	}
}

func TestParseHandshakeErrors(t *testing.T) {
	tests := []struct {
		name, data, field string
	}{
		{"not json", `nope`, ""},
		{"array", `[]`, ""},
		{"null", `null`, ""},
		{"trailing", `{"sid":"a","upgrades":[],"pingInterval":1,"pingTimeout":1}x`, ""},
		{"no sid", `{"upgrades":[],"pingInterval":1,"pingTimeout":1}`, "sid"},
		{"empty sid", `{"sid":"","upgrades":[],"pingInterval":1,"pingTimeout":1}`, "sid"},
		{"number sid", `{"sid":1,"upgrades":[],"pingInterval":1,"pingTimeout":1}`, "sid"},
		{"no upgrades", `{"sid":"a","pingInterval":1,"pingTimeout":1}`, "upgrades"},
		{"null upgrades", `{"sid":"a","upgrades":null,"pingInterval":1,"pingTimeout":1}`, "upgrades"},
		{"bad upgrades", `{"sid":"a","upgrades":[1],"pingInterval":1,"pingTimeout":1}`, "upgrades"},
		{"no interval", `{"sid":"a","upgrades":[],"pingTimeout":1}`, "pingInterval"},
		{"zero interval", `{"sid":"a","upgrades":[],"pingInterval":0,"pingTimeout":1}`, "pingInterval"},
		{"fractional interval", `{"sid":"a","upgrades":[],"pingInterval":1.5,"pingTimeout":1}`, "pingInterval"},
		{"quoted interval", `{"sid":"a","upgrades":[],"pingInterval":"1","pingTimeout":1}`, "pingInterval"},
		{"huge timeout", `{"sid":"a","upgrades":[],"pingInterval":1,"pingTimeout":2147483648}`, "pingTimeout"},
		{"negative max payload", `{"sid":"a","upgrades":[],"pingInterval":1,"pingTimeout":1,"maxPayload":-1}`, "maxPayload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseHandshake([]byte(tc.data))
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != CodeInvalidHandshake || e.Kind != errs.Protocol || e.Details[DetailField] != tc.field {
				t.Fatalf("error %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestHandshakePacketErrors(t *testing.T) {
	for _, h := range []Handshake{
		{PingInterval: 1, PingTimeout: 1},
		{SID: "a", PingTimeout: 1},
		{SID: "a", PingInterval: 1, PingTimeout: 1, MaxPayload: -1},
	} {
		if _, err := h.Packet(); codeOf(err) != CodeInvalidArgument {
			t.Fatalf("%+v: %v", h, err)
		}
	}
}
