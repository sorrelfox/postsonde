package wsframe

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func errCode(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// The examples of RFC 6455 §5.7.
func TestRFC6455Examples(t *testing.T) {
	t.Parallel()
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	bin256 := make([]byte, 256)
	bin64k := make([]byte, 65536)
	for i := range bin256 {
		bin256[i] = byte(i)
	}
	for i := range bin64k {
		bin64k[i] = byte(i * 7)
	}
	tests := []struct {
		name   string
		frame  Frame
		wire   []byte
		prefix bool // wire is only the header; the payload follows
	}{
		{
			name:  "single-frame unmasked text",
			frame: Frame{Header: Header{Fin: true, Opcode: OpText}, Payload: []byte("Hello")},
			wire:  mustHex(t, "81 05 48 65 6c 6c 6f"),
		},
		{
			name:  "single-frame masked text",
			frame: Frame{Header: Header{Fin: true, Opcode: OpText, Masked: true, MaskKey: key}, Payload: []byte("Hello")},
			wire:  mustHex(t, "81 85 37 fa 21 3d 7f 9f 4d 51 58"),
		},
		{
			name:  "fragment 1: Hel",
			frame: Frame{Header: Header{Opcode: OpText}, Payload: []byte("Hel")},
			wire:  mustHex(t, "01 03 48 65 6c"),
		},
		{
			name:  "fragment 2: lo",
			frame: Frame{Header: Header{Fin: true, Opcode: OpContinuation}, Payload: []byte("lo")},
			wire:  mustHex(t, "80 02 6c 6f"),
		},
		{
			name:  "unmasked ping",
			frame: Frame{Header: Header{Fin: true, Opcode: OpPing}, Payload: []byte("Hello")},
			wire:  mustHex(t, "89 05 48 65 6c 6c 6f"),
		},
		{
			name:  "masked pong",
			frame: Frame{Header: Header{Fin: true, Opcode: OpPong, Masked: true, MaskKey: key}, Payload: []byte("Hello")},
			wire:  mustHex(t, "8a 85 37 fa 21 3d 7f 9f 4d 51 58"),
		},
		{
			name:   "256 bytes binary, 16-bit length",
			frame:  Frame{Header: Header{Fin: true, Opcode: OpBinary}, Payload: bin256},
			wire:   mustHex(t, "82 7E 0100"),
			prefix: true,
		},
		{
			name:   "64KiB binary, 64-bit length",
			frame:  Frame{Header: Header{Fin: true, Opcode: OpBinary}, Payload: bin64k},
			wire:   mustHex(t, "82 7F 0000000000010000"),
			prefix: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := tt.wire
			if tt.prefix {
				want = append(append([]byte{}, tt.wire...), tt.frame.Payload...)
			}
			orig := append([]byte(nil), tt.frame.Payload...)
			got, err := AppendFrame(nil, tt.frame)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("AppendFrame = % x, want % x", head(got), head(want))
			}
			if !bytes.Equal(tt.frame.Payload, orig) {
				t.Fatal("AppendFrame modified the payload")
			}
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tt.frame); err != nil || !bytes.Equal(buf.Bytes(), want) {
				t.Fatalf("WriteFrame = % x, %v", head(buf.Bytes()), err)
			}

			r := bytes.NewReader(want)
			f, err := ReadFrame(r, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if r.Len() != 0 {
				t.Fatalf("%d bytes left", r.Len())
			}
			if !bytes.Equal(f.Payload, tt.frame.Payload) {
				t.Fatalf("payload = % x", head(f.Payload))
			}
			wantH := tt.frame.Header
			wantH.Length = int64(len(tt.frame.Payload))
			wantH.LengthSize = minimalLengthSize(wantH.Length)
			if f.Header != wantH {
				t.Fatalf("header = %+v, want %+v", f.Header, wantH)
			}
			if f.Size() != len(want)-len(tt.frame.Payload) {
				t.Fatalf("Size = %d", f.Size())
			}
		})
	}
}

func head(b []byte) []byte {
	if len(b) > 24 {
		return b[:24]
	}
	return b
}

func TestLengthEncodings(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 125, 126, 127, 65535, 65536, 70000} {
		f := Frame{Header: Header{Fin: true, Opcode: OpBinary}, Payload: make([]byte, n)}
		b, err := AppendFrame(nil, f)
		if err != nil {
			t.Fatal(err)
		}
		wantSize := minimalLengthSize(int64(n))
		if len(b) != 2+int(wantSize)+n {
			t.Errorf("n=%d: encoded %d bytes", n, len(b))
		}
		switch {
		case n <= 125 && b[1] != byte(n):
			t.Errorf("n=%d: second byte %#x", n, b[1])
		case n > 125 && n <= 65535 && b[1] != 126:
			t.Errorf("n=%d: second byte %#x", n, b[1])
		case n > 65535 && b[1] != 127:
			t.Errorf("n=%d: second byte %#x", n, b[1])
		}
		got, err := ReadFrame(bytes.NewReader(b), Limits{})
		if err != nil || got.Length != int64(n) || got.LengthSize != wantSize {
			t.Errorf("n=%d: read %+v, %v", n, got.Header, err)
		}
	}
}

func TestNonMinimalLength(t *testing.T) {
	t.Parallel()
	for _, size := range []uint8{2, 8} {
		f := Frame{Header: Header{Fin: true, Opcode: OpText, LengthSize: size}, Payload: []byte("hi")}
		b, err := AppendFrame(nil, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 2+int(size)+2 {
			t.Fatalf("size %d: % x", size, b)
		}
		got, err := ReadFrame(bytes.NewReader(b), Limits{})
		if errCode(err) != CodeProtocolError || CloseCodeOf(err) != CloseProtocolError {
			t.Errorf("size %d: err = %v", size, err)
		}
		if got.LengthSize != size || got.Length != 2 {
			t.Errorf("size %d: header not returned with the error: %+v", size, got.Header)
		}
		got, err = ReadFrame(bytes.NewReader(b), Limits{AllowNonMinimalLength: true})
		if err != nil || string(got.Payload) != "hi" {
			t.Errorf("size %d allowed: %v %q", size, err, got.Payload)
		}
		if again, _ := AppendFrame(nil, got); !bytes.Equal(again, b) {
			t.Errorf("size %d: re-encoding differs", size)
		}
	}
	if _, err := AppendFrame(nil, Frame{Header: Header{LengthSize: 2}, Payload: make([]byte, 70000)}); errCode(err) != CodeInvalidArgument {
		t.Errorf("too small LengthSize: %v", err)
	}
	if _, err := AppendFrame(nil, Frame{Header: Header{LengthSize: 4}}); errCode(err) != CodeInvalidArgument {
		t.Errorf("LengthSize 4: %v", err)
	}
	if _, err := AppendFrame(nil, Frame{Header: Header{Opcode: 16}}); errCode(err) != CodeInvalidArgument {
		t.Errorf("opcode 16: %v", err)
	}
}

func TestReadFrameRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		wire   string
		lim    Limits
		code   string
		status StatusCode
	}{
		{"reserved data opcode", "83 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"reserved control opcode", "8b 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"rsv1", "c1 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"rsv2", "a1 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"rsv3", "91 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"rsv2 while rsv1 allowed", "a1 00", Limits{AllowedRSV: RSV1}, CodeProtocolError, CloseProtocolError},
		{"fragmented ping", "09 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"fragmented close", "08 00", Limits{}, CodeProtocolError, CloseProtocolError},
		{"ping of 126 bytes", "89 7e 007e", Limits{}, CodeProtocolError, CloseProtocolError},
		{"64-bit length msb", "82 7f 8000000000000000", Limits{}, CodeProtocolError, CloseProtocolError},
		{"over limit", "82 7e 0100", Limits{MaxPayload: 255}, CodeTooBig, CloseMessageTooBig},
		{"over default limit", "82 7f 0000000001000001", Limits{}, CodeTooBig, CloseMessageTooBig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ReadFrame(bytes.NewReader(mustHex(t, tt.wire)), tt.lim)
			if errCode(err) != tt.code || CloseCodeOf(err) != tt.status {
				t.Fatalf("err = %v (code %q, close %d)", err, errCode(err), CloseCodeOf(err))
			}
			if errs.KindOf(err) != errs.Protocol {
				t.Fatalf("kind = %s", errs.KindOf(err))
			}
		})
	}

	// Allowed RSV bits and an unlimited payload pass.
	if f, err := ReadFrame(bytes.NewReader(mustHex(t, "c1 00")), Limits{AllowedRSV: RSV1}); err != nil || !f.Rsv1 {
		t.Errorf("allowed rsv1: %+v %v", f, err)
	}
	if _, err := ReadFrame(bytes.NewReader(mustHex(t, "82 7f 0000000001000001")), Limits{MaxPayload: -1}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("unlimited: %v", err)
	}
}

func TestReadFrameEOF(t *testing.T) {
	t.Parallel()
	if _, err := ReadFrame(bytes.NewReader(nil), Limits{}); err != io.EOF {
		t.Errorf("empty: %v", err)
	}
	for _, wire := range []string{"81", "81 fe 00", "81 85 37 fa", "81 05 48 65"} {
		if _, err := ReadFrame(bytes.NewReader(mustHex(t, wire)), Limits{}); err != io.ErrUnexpectedEOF {
			t.Errorf("%s: %v", wire, err)
		}
	}
}

func TestReadPayloadGrowsProgressively(t *testing.T) {
	t.Parallel()
	data := make([]byte, 3*payloadChunk+17)
	for i := range data {
		data[i] = byte(i)
	}
	got, err := readPayload(bytes.NewReader(data), int64(len(data)), []byte("pre"))
	if err != nil || !bytes.Equal(got[3:], data) || string(got[:3]) != "pre" {
		t.Fatalf("readPayload: %v", err)
	}
	got, err = readPayload(bytes.NewReader(data[:10]), 1<<40, nil)
	if err != io.ErrUnexpectedEOF || len(got) != 10 || cap(got) > payloadChunk {
		t.Fatalf("lying length: len %d cap %d err %v", len(got), cap(got), err)
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	for n := range 70 {
		for pos := range 4 {
			key := [4]byte{byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32())}
			b := make([]byte, n)
			for i := range b {
				b[i] = byte(rng.Uint32())
			}
			want := append([]byte(nil), b...)
			for i := range want {
				want[i] ^= key[(pos+i)%4]
			}
			next := Mask(key, pos, b)
			if !bytes.Equal(b, want) {
				t.Fatalf("n=%d pos=%d: mismatch", n, pos)
			}
			if next != (pos+n)%4 {
				t.Fatalf("n=%d pos=%d: next %d", n, pos, next)
			}
		}
	}
	// Masking in two pieces equals masking at once.
	key := [4]byte{1, 2, 3, 4}
	a := bytes.Repeat([]byte{0xAA}, 37)
	b := append([]byte(nil), a...)
	Mask(key, 0, a)
	Mask(key, Mask(key, 0, b[:13]), b[13:])
	if !bytes.Equal(a, b) {
		t.Fatal("split masking differs")
	}
}

func TestComputeAccept(t *testing.T) {
	t.Parallel()
	// RFC 6455 §1.3.
	if got := ComputeAccept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("ComputeAccept = %q", got)
	}
}

func TestOpcodeString(t *testing.T) {
	t.Parallel()
	if OpText.String() != "text" || Opcode(3).String() != "reserved(0x3)" || !Opcode(0xB).IsReserved() || !Opcode(0xB).IsControl() || OpBinary.IsControl() {
		t.Fatal("opcode helpers")
	}
}
