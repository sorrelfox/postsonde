package wsframe

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

var fuzzSeeds = []string{
	"81 05 48 65 6c 6c 6f",
	"81 85 37 fa 21 3d 7f 9f 4d 51 58",
	"01 03 48 65 6c 80 02 6c 6f",
	"89 05 48 65 6c 6c 6f",
	"8a 85 37 fa 21 3d 7f 9f 4d 51 58",
	"82 7e 00 03 01 02 03",
	"82 7f 00 00 00 00 00 00 00 02 01 02",
	"88 02 03 e8",
	"88 01 03",
	"c1 00",
	"09 00",
	"82 7f 80 00 00 00 00 00 00 00",
	"81 82 00 00 00 00 ce 81 83 00 00 00 00 ba",
}

func FuzzReadFrame(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(mustHex(f, s), false)
	}
	f.Fuzz(func(t *testing.T, data []byte, lenient bool) {
		const maxPayload = 1 << 12
		lim := Limits{MaxPayload: maxPayload, AllowNonMinimalLength: lenient}
		if lenient {
			lim.AllowedRSV = RSV1 | RSV2 | RSV3
		}
		r := bytes.NewReader(data)
		for {
			before := r.Len()
			fr, err := ReadFrame(r, lim)
			if err != nil {
				if err == io.EOF && before != 0 {
					t.Fatal("io.EOF in the middle of the input")
				}
				if err != io.EOF && err != io.ErrUnexpectedEOF && errs.KindOf(err) != errs.Protocol {
					t.Fatalf("unexpected error %v", err)
				}
				if errCode(err) == CodeTooBig && fr.Length <= maxPayload {
					t.Fatalf("too big at %d bytes", fr.Length)
				}
				return
			}
			if int64(len(fr.Payload)) != fr.Length || fr.Length > maxPayload {
				t.Fatalf("payload %d, length %d", len(fr.Payload), fr.Length)
			}
			if fr.Opcode.IsControl() && (!fr.Fin || fr.Length > MaxControlPayload) {
				t.Fatalf("invalid control frame accepted: %+v", fr.Header)
			}
			// encode(decode(x)) == x.
			consumed := data[len(data)-before : len(data)-r.Len()]
			enc, err := AppendFrame(nil, fr)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(enc, consumed) {
				t.Fatalf("round trip\n got % x\nwant % x", enc, consumed)
			}
		}
	})
}

func FuzzConnRead(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(mustHex(f, s), false)
		f.Add(mustHex(f, s), true)
	}
	f.Fuzz(func(t *testing.T, data []byte, server bool) {
		const maxMsg = 1 << 12
		o := Options{Role: RoleClient, MaxMessageSize: maxMsg, Limits: Limits{MaxPayload: 1 << 11}}
		if server {
			o.Role = RoleServer
		}
		rw := &fakeRW{in: bytes.NewReader(data)}
		c := NewConn(rw, o)
		var last error
		for range 1000 {
			op, msg, err := c.ReadMessage()
			if err != nil {
				last = err
				break
			}
			if op != OpText && op != OpBinary {
				t.Fatalf("message opcode %v", op)
			}
			if len(msg) > maxMsg {
				t.Fatalf("message of %d bytes", len(msg))
			}
			if op == OpText && !utf8.Valid(msg) {
				t.Fatalf("invalid UTF-8 text %q", msg)
			}
		}
		if last == nil {
			return
		}
		if _, ok := errors.AsType[*errs.Error](last); !ok {
			t.Fatalf("error is not *errs.Error: %v", last)
		}
		// Whatever the Conn wrote is well-formed, in its own role, and ends
		// with at most one close frame.
		out := bytes.NewReader(rw.out.Bytes())
		closes := 0
		for {
			fr, err := ReadFrame(out, Limits{})
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("output: %v", err)
			}
			if fr.Masked != (o.Role == RoleClient) {
				t.Fatal("output masking does not match the role")
			}
			if closes > 0 {
				t.Fatal("frame after close")
			}
			if fr.Opcode == OpClose {
				closes++
				if _, _, err := ParseClosePayload(fr.Payload); err != nil {
					t.Fatalf("sent invalid close payload: %v", err)
				}
			}
		}
	})
}
