package socketio

import (
	"reflect"
	"testing"
)

// FuzzDecode checks that decoding never panics, that a decoded packet is
// accepted by Encode and Value, and that its canonical encoding decodes to
// the same packet.
func FuzzDecode(f *testing.F) {
	for _, tc := range specCases {
		f.Add(tc.text, uint8(len(tc.atts)))
	}
	for _, s := range []string{"", "0/,", "1/admin", "5-[]", "51-", "2/a,01[\"x\"]", `2["x"]]`, `51-["x",{"_placeholder":true,"num":0.0}]`} {
		f.Add(s, uint8(1))
	}
	f.Fuzz(func(t *testing.T, text string, n uint8) {
		atts := make([][]byte, n%4)
		for i := range atts {
			atts[i] = []byte{byte(i)}
		}
		p, err := Decode(text, atts, Options{MaxAttachments: 8})
		if err != nil {
			return
		}
		wire, gotAtts, err := Encode(p)
		if err != nil {
			t.Fatalf("%q decoded to %+v, which does not encode: %v", text, p, err)
		}
		if _, err := p.Value(); err != nil {
			t.Fatalf("%q: Value: %v", text, err)
		}
		again, err := Decode(wire, gotAtts, Options{MaxAttachments: 8})
		if err != nil {
			t.Fatalf("%q encoded as %q, which does not decode: %v", text, wire, err)
		}
		if !reflect.DeepEqual(again, p) {
			t.Fatalf("%q -> %+v -> %q -> %+v", text, p, wire, again)
		}
	})
}
