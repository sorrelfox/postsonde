package socketio

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
)

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// specCases are the examples of the Socket.IO v5 protocol document
// (socket.io-protocol README, sections "Packet encoding" and "Exchange
// protocol": connection to a namespace, sending and receiving data,
// acknowledgement, disconnection), with the binary attachments that follow
// BINARY_EVENT and BINARY_ACK packets.
var specCases = []struct {
	name string
	text string
	atts [][]byte
	want Packet
}{
	{"CONNECT main namespace", "0", nil, Packet{Type: Connect, Namespace: "/"}},
	{"CONNECT namespace", "0/admin,", nil, Packet{Type: Connect, Namespace: "/admin"}},
	{"CONNECT with auth", `0{"token":"123"}`, nil, Packet{Type: Connect, Namespace: "/", Data: raw(`{"token":"123"}`)}},
	{"CONNECT namespace with auth", `0/admin,{"token":"123"}`, nil, Packet{Type: Connect, Namespace: "/admin", Data: raw(`{"token":"123"}`)}},
	{"CONNECT answer", `0/admin,{"sid":"oSO0OpakMV_3jnilAAAA"}`, nil, Packet{Type: Connect, Namespace: "/admin", Data: raw(`{"sid":"oSO0OpakMV_3jnilAAAA"}`)}},
	{"CONNECT_ERROR", `4{"message":"Not authorized"}`, nil, Packet{Type: ConnectError, Namespace: "/", Data: raw(`{"message":"Not authorized"}`)}},
	{"CONNECT_ERROR namespace", `4/admin,{"message":"Not authorized"}`, nil, Packet{Type: ConnectError, Namespace: "/admin", Data: raw(`{"message":"Not authorized"}`)}},
	{"CONNECT_ERROR string (v3 servers)", `4"Not authorized"`, nil, Packet{Type: ConnectError, Namespace: "/", Data: raw(`"Not authorized"`)}},
	{"DISCONNECT", "1", nil, Packet{Type: Disconnect, Namespace: "/"}},
	{"DISCONNECT namespace", "1/admin,", nil, Packet{Type: Disconnect, Namespace: "/admin"}},
	{"EVENT", `2["hello",1]`, nil, Packet{Type: Event, Namespace: "/", Data: raw(`["hello",1]`)}},
	{"EVENT namespace", `2/admin,["project:delete",123]`, nil, Packet{Type: Event, Namespace: "/admin", Data: raw(`["project:delete",123]`)}},
	{"EVENT with ack ID", `2/admin,456["project:delete",123]`, nil, Packet{Type: Event, Namespace: "/admin", ID: 456, HasID: true, Data: raw(`["project:delete",123]`)}},
	{"EVENT ack ID 0", `20["x"]`, nil, Packet{Type: Event, Namespace: "/", ID: 0, HasID: true, Data: raw(`["x"]`)}},
	{"EVENT numeric name", `2[1,"a"]`, nil, Packet{Type: Event, Namespace: "/", Data: raw(`[1,"a"]`)}},
	{"ACK", `3/admin,456[]`, nil, Packet{Type: Ack, Namespace: "/admin", ID: 456, HasID: true, Data: raw(`[]`)}},
	{"ACK main namespace", `312["ok"]`, nil, Packet{Type: Ack, Namespace: "/", ID: 12, HasID: true, Data: raw(`["ok"]`)}},
	{"BINARY_EVENT", `51-["hello",{"_placeholder":true,"num":0}]`, [][]byte{{1, 2, 3}},
		Packet{Type: BinaryEvent, Namespace: "/", Data: raw(`["hello",{"_placeholder":true,"num":0}]`), Attachments: [][]byte{{1, 2, 3}}}},
	{"BINARY_ACK", `61-/admin,456[{"_placeholder":true,"num":0}]`, [][]byte{{3}},
		Packet{Type: BinaryAck, Namespace: "/admin", ID: 456, HasID: true, Data: raw(`[{"_placeholder":true,"num":0}]`), Attachments: [][]byte{{3}}}},
	{"BINARY_EVENT two attachments", `52-/admin,["up",{"a":{"_placeholder":true,"num":1}},[{"_placeholder":true,"num":0}]]`, [][]byte{{0}, {}},
		Packet{Type: BinaryEvent, Namespace: "/admin", Data: raw(`["up",{"a":{"_placeholder":true,"num":1}},[{"_placeholder":true,"num":0}]]`), Attachments: [][]byte{{0}, {}}}},
	{"BINARY_EVENT no attachments", `50-["x"]`, nil, Packet{Type: BinaryEvent, Namespace: "/", Data: raw(`["x"]`), Attachments: [][]byte{}}},
	{"namespace with digits", `2/v2,7["x"]`, nil, Packet{Type: Event, Namespace: "/v2", ID: 7, HasID: true, Data: raw(`["x"]`)}},
	{"max ack ID", `29007199254740991["x"]`, nil, Packet{Type: Event, Namespace: "/", ID: MaxAckID, HasID: true, Data: raw(`["x"]`)}},
}

func TestSpecExamples(t *testing.T) {
	for _, tc := range specCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode(tc.text, tc.atts, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v\nwant    %#v", got, tc.want)
			}
			text, atts, err := Encode(got)
			if err != nil {
				t.Fatal(err)
			}
			if text != tc.text || len(atts) != len(tc.atts) {
				t.Fatalf("encoded %q with %d attachments, want %q with %d", text, len(atts), tc.text, len(tc.atts))
			}
		})
	}
}

// Non-canonical forms socket.io-parser accepts decode, and encode in the
// canonical form.
func TestDecodeNonCanonical(t *testing.T) {
	tests := []struct{ in, out string }{
		{"0/,", "0"},
		{"1/admin", "1/admin,"},
		{`2 ["x"]`, `2 ["x"]`},
	}
	for _, tc := range tests {
		p, err := Decode(tc.in, nil, Options{})
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		text, _, _ := Encode(p)
		if text != tc.out {
			t.Fatalf("%q encoded as %q, want %q", tc.in, text, tc.out)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		code   string
		offset string
	}{
		{"empty", "", CodeEmptyPacket, "0"},
		{"unknown type", `7["x"]`, CodeUnknownType, "0"},
		{"letter type", `a`, CodeUnknownType, "0"},
		{"engine type left in", `42["x"]`, CodeInvalidAckID, "1"},
		{"binary without count", `5["x"]`, CodeInvalidAttachments, "1"},
		{"binary empty count", `5-["x"]`, CodeInvalidAttachments, "1"},
		{"binary count without dash", `51`, CodeInvalidAttachments, "1"},
		{"binary count leading zero", `501-["x",{"_placeholder":true,"num":0}]`, CodeInvalidAttachments, "1"},
		{"binary too many", `5257-["x"]`, CodeTooManyAttachments, "1"},
		{"binary huge count", `599999999999999999999-["x"]`, CodeTooManyAttachments, "1"},
		{"ack ID leading zero", `201["x"]`, CodeInvalidAckID, "1"},
		{"ack ID too big", `29007199254740992["x"]`, CodeInvalidAckID, "1"},
		{"ack ID too long", `212345678901234567["x"]`, CodeInvalidAckID, "1"},
		{"ack ID on CONNECT", `01{}`, CodeInvalidAckID, "1"},
		{"ack ID on DISCONNECT", `1/a,5`, CodeInvalidAckID, "4"},
		{"ack ID on CONNECT_ERROR", `45{"message":"x"}`, CodeInvalidAckID, "1"},
		{"ACK without ID", `3["x"]`, CodeInvalidAckID, "1"},
		{"BINARY_ACK without ID", `60-[]`, CodeInvalidAckID, "3"},
		{"CONNECT array", `0["x"]`, CodeInvalidPayload, "1"},
		{"CONNECT null", `0null`, CodeInvalidPayload, "1"},
		{"CONNECT string", `0"x"`, CodeInvalidPayload, "1"},
		{"DISCONNECT payload", `1{}`, CodeInvalidPayload, "1"},
		{"CONNECT_ERROR array", `4[]`, CodeInvalidPayload, "1"},
		{"CONNECT_ERROR missing", `4`, CodeInvalidPayload, "1"},
		{"EVENT missing", `2`, CodeInvalidPayload, "1"},
		{"EVENT object", `2{"a":1}`, CodeInvalidPayload, "1"},
		{"EVENT empty array", `2[]`, CodeInvalidPayload, "1"},
		{"EVENT null name", `2[null]`, CodeInvalidPayload, "1"},
		{"EVENT object name", `2[{}]`, CodeInvalidPayload, "1"},
		{"EVENT reserved name", `2["connect"]`, CodeReservedEvent, "1"},
		{"EVENT reserved removeListener", `2/a,["removeListener"]`, CodeReservedEvent, "4"},
		{"ACK object", `31{}`, CodeInvalidPayload, "2"},
		{"ACK missing", `31`, CodeInvalidPayload, "2"},
		{"bad JSON", `2["x"`, CodeInvalidPayload, "5"},
		{"trailing data", `2["x"]]`, CodeInvalidPayload, "6"},
		{"two values", `2["x"] ["y"]`, CodeInvalidPayload, "6"},
		{"payload after ID garbage", `2/a,1x`, CodeInvalidPayload, "5"},
		{"placeholder out of range", `51-["x",{"_placeholder":true,"num":1}]`, CodeInvalidPlaceholder, "3"},
		{"placeholder negative", `51-["x",{"_placeholder":true,"num":-1}]`, CodeInvalidPlaceholder, "3"},
		{"placeholder fraction", `51-["x",{"_placeholder":true,"num":0.5}]`, CodeInvalidPlaceholder, "3"},
		{"placeholder no num", `51-["x",{"_placeholder":true}]`, CodeInvalidPlaceholder, "3"},
		{"placeholder string num", `51-["x",{"_placeholder":true,"num":"0"}]`, CodeInvalidPlaceholder, "3"},
		{"placeholder without attachments", `50-["x",{"_placeholder":true,"num":0}]`, CodeInvalidPlaceholder, "3"},
		{"nested placeholder out of range", `61-1[{"a":[{"_placeholder":true,"num":3}]}]`, CodeInvalidPlaceholder, "4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDecoder(Options{}).DecodeText(tc.text)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != tc.code || e.Kind != errs.Protocol || e.Details[DetailOffset] != tc.offset {
				t.Fatalf("error %v (%+v), want code %q at offset %s", err, e, tc.code, tc.offset)
			}
		})
	}
}

// Placeholders are only special in binary packets, like in
// socket.io-parser.
func TestPlaceholderInTextEvent(t *testing.T) {
	p, err := Decode(`2["x",{"_placeholder":true,"num":9}]`, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := p.Value()
	want := []any{"x", map[string]any{"_placeholder": true, "num": json.Number("9")}}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("Value = %#v", v)
	}
}

func TestDecoderAttachments(t *testing.T) {
	d := NewDecoder(Options{})
	p, err := d.DecodeText(`452-["bytes",{"_placeholder":true,"num":0},{"nested":[{"_placeholder":true,"num":1}]}]`[1:])
	if p != nil || err != nil || d.Waiting() != 2 {
		t.Fatalf("header: %v %v waiting %d", p, err, d.Waiting())
	}
	src := []byte{1, 2, 3}
	if p, err = d.DecodeBinary(src); p != nil || err != nil || d.Waiting() != 1 {
		t.Fatalf("first attachment: %v %v", p, err)
	}
	src[0] = 9 // the decoder keeps a copy
	if p, err = d.Feed(engineio.BinaryMessage([]byte("hi"))); p == nil || err != nil || d.Waiting() != 0 {
		t.Fatalf("last attachment: %v %v", p, err)
	}
	args, err := p.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"bytes", []byte{1, 2, 3}, map[string]any{"nested": []any{[]byte("hi")}}}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args = %#v", args)
	}
	if p.EventName() != "bytes" {
		t.Fatalf("EventName = %q", p.EventName())
	}
	// The raw payload keeps the placeholders.
	if !strings.Contains(string(p.Data), `"_placeholder":true`) {
		t.Fatalf("Data = %s", p.Data)
	}
}

func TestDecoderStateErrors(t *testing.T) {
	d := NewDecoder(Options{})
	if _, err := d.DecodeBinary([]byte{1}); codeOf(err) != CodeUnexpectedBinary {
		t.Fatalf("binary first: %v", err)
	}
	if _, err := d.DecodeText(`51-["x",{"_placeholder":true,"num":0}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeText(`2["y"]`); codeOf(err) != CodeUnexpectedText {
		t.Fatalf("text while waiting: %v", err)
	}
	// The error reset the decoder.
	if d.Waiting() != 0 {
		t.Fatal("not reset")
	}
	if p, err := d.DecodeText(`2["y"]`); err != nil || p == nil {
		t.Fatalf("after reset: %v", err)
	}
	if _, err := d.Feed(engineio.Packet{Type: engineio.Ping}); codeOf(err) != CodeNotMessage {
		t.Fatalf("ping: %v", err)
	}
	if _, err := Decode(`51-["x",{"_placeholder":true,"num":0}]`, nil, Options{}); codeOf(err) != CodeInvalidAttachments {
		t.Fatalf("missing attachment: %v", err)
	}
	if _, err := Decode(`51-["x",{"_placeholder":true,"num":0}]`, [][]byte{{1}, {2}}, Options{}); codeOf(err) != CodeUnexpectedBinary {
		t.Fatalf("extra attachment: %v", err)
	}
	if _, err := Decode(`2["x"]`, [][]byte{{1}}, Options{}); codeOf(err) != CodeUnexpectedBinary {
		t.Fatalf("attachment on text packet: %v", err)
	}
}

func TestLimits(t *testing.T) {
	if _, err := Decode(`2["abcdef"]`, nil, Options{MaxPacketSize: 10}); codeOf(err) != CodeTooBig {
		t.Fatalf("size: %v", err)
	}
	if _, err := Decode(`2["abcde"]`, nil, Options{MaxPacketSize: 10}); err != nil {
		t.Fatalf("at size limit: %v", err)
	}
	if _, err := Decode(`52-["x"]`, nil, Options{MaxAttachments: 1}); codeOf(err) != CodeTooManyAttachments {
		t.Fatalf("attachments: %v", err)
	}
	if _, err := Decode(`5300-["x"]`, make([][]byte, 300), Options{MaxAttachments: -1}); err != nil {
		t.Fatalf("no attachment limit: %v", err)
	}
	if _, err := Decode(`2["`+strings.Repeat("x", DefaultMaxPacketSize)+`"]`, nil, Options{}); codeOf(err) != CodeTooBig {
		t.Fatalf("default size: %v", err)
	}
}

func TestEncodeErrors(t *testing.T) {
	tests := []struct {
		name string
		p    Packet
		code string
	}{
		{"unknown type", Packet{Type: 9}, CodeInvalidArgument},
		{"namespace without slash", Packet{Type: Connect, Namespace: "admin"}, CodeInvalidNamespace},
		{"namespace with comma", Packet{Type: Connect, Namespace: "/a,b"}, CodeInvalidNamespace},
		{"attachments on EVENT", Packet{Type: Event, Data: raw(`["x"]`), Attachments: [][]byte{{1}}}, CodeInvalidArgument},
		{"ID on CONNECT", Packet{Type: Connect, HasID: true}, CodeInvalidAckID},
		{"ACK without ID", Packet{Type: Ack, Data: raw(`[]`)}, CodeInvalidAckID},
		{"ID too big", Packet{Type: Event, HasID: true, ID: MaxAckID + 1, Data: raw(`["x"]`)}, CodeInvalidAckID},
		{"EVENT without payload", Packet{Type: Event}, CodeInvalidPayload},
		{"EVENT bad JSON", Packet{Type: Event, Data: raw(`["x"`)}, CodeInvalidPayload},
		{"reserved event", Packet{Type: Event, Data: raw(`["disconnect"]`)}, CodeReservedEvent},
		{"DISCONNECT payload", Packet{Type: Disconnect, Data: raw(`{}`)}, CodeInvalidPayload},
		{"placeholder without attachment", Packet{Type: BinaryEvent, Data: raw(`["x",{"_placeholder":true,"num":0}]`)}, CodeInvalidPlaceholder},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Encode(tc.p)
			if codeOf(err) != tc.code || errs.KindOf(err) != errs.InvalidArgument {
				t.Fatalf("error %v, want code %q", err, tc.code)
			}
		})
	}
}

func TestEncodeMessages(t *testing.T) {
	p, err := NewEvent("/", "upload", map[string]any{"name": "f.bin"}, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.HasID = 2, true
	msgs, err := EncodeMessages(p)
	if err != nil {
		t.Fatal(err)
	}
	body, err := engineio.EncodePayload(msgs)
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes a socket.io v4 client sends (reference-v4.txt).
	want := "451-2[\"upload\",{\"name\":\"f.bin\"},{\"_placeholder\":true,\"num\":0}]\x1ebAQIDBA=="
	if string(body) != want {
		t.Fatalf("got  %q\nwant %q", body, want)
	}
}

func TestBuilders(t *testing.T) {
	tests := []struct {
		name string
		make func() (Packet, error)
		text string
		atts int
	}{
		{"event", func() (Packet, error) { return NewEvent("", "hello", "world", 1) }, `2["hello","world",1]`, 0},
		{"event no args", func() (Packet, error) { return NewEvent("/admin", "ping") }, `2/admin,["ping"]`, 0},
		{"event html not escaped", func() (Packet, error) { return NewEvent("/", "<b>", "a&b") }, `2["<b>","a&b"]`, 0},
		{"event raw JSON arg", func() (Packet, error) { return NewEvent("/", "x", json.RawMessage(`{"z":1,"a":2}`)) }, `2["x",{"z":1,"a":2}]`, 0},
		{"binary event", func() (Packet, error) {
			return NewEvent("/", "bytes", []byte{1}, map[string]any{"nested": []any{[]byte("hi")}})
		}, `52-["bytes",{"_placeholder":true,"num":0},{"nested":[{"_placeholder":true,"num":1}]}]`, 2},
		{"ack", func() (Packet, error) { return NewAck("/admin", 7, map[string]any{"deleted": 42}) }, `3/admin,7[{"deleted":42}]`, 0},
		{"ack no args", func() (Packet, error) { return NewAck("/", 0) }, `30[]`, 0},
		{"binary ack", func() (Packet, error) { return NewAck("/", 1, []byte{9}) }, `61-1[{"_placeholder":true,"num":0}]`, 1},
		{"connect", func() (Packet, error) { return NewConnect("/", nil) }, `0`, 0},
		{"connect auth", func() (Packet, error) { return NewConnect("/admin", map[string]any{"token": "123"}) }, `0/admin,{"token":"123"}`, 0},
		{"connect struct auth", func() (Packet, error) {
			return NewConnect("/", struct {
				Token string `json:"token"`
			}{"t"})
		}, `0{"token":"t"}`, 0},
		{"disconnect", func() (Packet, error) { return NewDisconnect("/admin") }, `1/admin,`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.make()
			if err != nil {
				t.Fatal(err)
			}
			text, atts, err := Encode(p)
			if err != nil || text != tc.text || len(atts) != tc.atts {
				t.Fatalf("Encode = %q, %d attachments, %v; want %q, %d", text, len(atts), err, tc.text, tc.atts)
			}
		})
	}
}

func TestBuilderErrors(t *testing.T) {
	if _, err := NewEvent("/", "connect"); codeOf(err) != CodeReservedEvent {
		t.Fatalf("reserved: %v", err)
	}
	if _, err := NewConnect("/", []any{1}); codeOf(err) != CodeInvalidPayload {
		t.Fatalf("array auth: %v", err)
	}
	if _, err := NewConnect("/", map[string]any{"b": []byte{1}}); codeOf(err) != CodeInvalidArgument {
		t.Fatalf("binary auth: %v", err)
	}
	if _, err := NewDisconnect("x"); codeOf(err) != CodeInvalidNamespace {
		t.Fatalf("namespace: %v", err)
	}
	if _, err := NewEvent("/", "x", func() {}); codeOf(err) != CodeInvalidArgument {
		t.Fatalf("func arg: %v", err)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := NewEvent("/", "x", cyclic); codeOf(err) != CodeInvalidArgument {
		t.Fatalf("cycle: %v", err)
	}
}

func TestValueErrors(t *testing.T) {
	p := Packet{Type: BinaryEvent, Data: raw(`["x",{"_placeholder":true,"num":0}]`)}
	if _, err := p.Value(); codeOf(err) != CodeInvalidPlaceholder {
		t.Fatalf("missing attachment: %v", err)
	}
	if v, err := (Packet{Type: Connect}).Value(); v != nil || err != nil {
		t.Fatalf("no payload: %v %v", v, err)
	}
	if _, err := (Packet{Type: Connect, Data: raw(`{"sid":"a"}`)}).Args(); codeOf(err) != CodeInvalidPayload {
		t.Fatalf("Args on object: %v", err)
	}
	if name := (Packet{Type: Event, Data: raw(`[12,"a"]`)}).EventName(); name != "12" {
		t.Fatalf("numeric name: %q", name)
	}
	if name := (Packet{Type: Ack, Data: raw(`["a"]`), HasID: true}).EventName(); name != "" {
		t.Fatalf("ack name: %q", name)
	}
}

func TestPacketTypeString(t *testing.T) {
	if BinaryAck.String() != "BINARY_ACK" || PacketType(7).String() != "PacketType(7)" {
		t.Fatal("String")
	}
}
