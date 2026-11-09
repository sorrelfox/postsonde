package transport

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()
	msgs := [][]byte{{}, []byte("a"), bytes.Repeat([]byte("xyz"), 1000), make([]byte, directReadLimit+10)}
	var wire []byte
	for i, m := range msgs {
		wire = AppendFrame(wire, i%2 == 1, m)
	}
	readers := map[string]func(io.Reader) io.Reader{
		"whole":    func(r io.Reader) io.Reader { return r },
		"one byte": iotest.OneByteReader,
		"half":     iotest.HalfReader,
		"data err": iotest.DataErrReader,
	}
	for name, wrap := range readers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fr := NewFrameReader(wrap(bytes.NewReader(wire)), -1)
			for i, want := range msgs {
				f, err := fr.Next()
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if f.Compressed != (i%2 == 1) || !bytes.Equal(f.Payload, want) {
					t.Fatalf("frame %d = compressed %v, %d bytes; want %v, %d bytes", i, f.Compressed, len(f.Payload), i%2 == 1, len(want))
				}
			}
			if _, err := fr.Next(); err != io.EOF {
				t.Fatalf("after last frame: %v, want io.EOF", err)
			}
		})
	}
}

func TestFrameHeaderBytes(t *testing.T) {
	t.Parallel()
	got := AppendFrame([]byte{0xAA}, true, []byte("hi"))
	want := []byte{0xAA, 1, 0, 0, 0, 2, 'h', 'i'}
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendFrame = % x, want % x", got, want)
	}
}

func TestFrameReaderErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []byte
		max   int
		check func(error) bool
	}{
		{"empty", nil, 0, func(err error) bool { return err == io.EOF }},
		{"partial header", []byte{0, 0, 0}, 0, func(err error) bool {
			return errors.Is(err, ErrFrameTruncated) && errors.Is(err, io.ErrUnexpectedEOF)
		}},
		{"partial payload", []byte{0, 0, 0, 0, 5, 'a', 'b'}, 0, func(err error) bool { return errors.Is(err, ErrFrameTruncated) }},
		{"lying length, unlimited", []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 'a'}, -1, func(err error) bool { return errors.Is(err, ErrFrameTruncated) }},
		{"too large", []byte{0, 0, 0, 0, 11}, 10, func(err error) bool {
			var e *FrameTooLargeError
			return errors.As(err, &e) && e.Size == 11 && e.Max == 10
		}},
		{"default limit", []byte{0, 0, 0x40, 0, 1}, 0, func(err error) bool {
			var e *FrameTooLargeError
			return errors.As(err, &e) && e.Max == DefaultMaxRecvMessageSize
		}},
		{"bad flag", []byte{2, 0, 0, 0, 0}, 0, func(err error) bool {
			var e *FrameFlagError
			return errors.As(err, &e) && e.Flag == 2
		}},
		{"reader error", nil, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := io.Reader(bytes.NewReader(tt.input))
			boom := errors.New("boom")
			if tt.check == nil {
				r = iotest.ErrReader(boom)
				tt.check = func(err error) bool { return err == boom }
			}
			_, err := NewFrameReader(r, tt.max).Next()
			if !tt.check(err) {
				t.Fatalf("Next() error = %v", err)
			}
		})
	}
}

func TestFrameExactLimit(t *testing.T) {
	t.Parallel()
	wire := AppendFrame(nil, false, []byte(strings.Repeat("a", 10)))
	if _, err := NewFrameReader(bytes.NewReader(wire), 10).Next(); err != nil {
		t.Fatalf("payload at the limit: %v", err)
	}
}

func FuzzFrameReader(f *testing.F) {
	f.Add([]byte{}, 16, uint8(1))
	f.Add(AppendFrame(AppendFrame(nil, false, []byte("hello")), true, []byte("x")), 16, uint8(3))
	f.Add([]byte{0, 0, 0, 0, 20, 1, 2}, 16, uint8(2))
	f.Add([]byte{1, 0xFF, 0xFF, 0xFF, 0xFF}, -1, uint8(1))
	f.Add([]byte{7, 0, 0, 0, 0}, 0, uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, max int, chunk uint8) {
		if max > 1<<16 {
			max = 1 << 16
		}
		var r io.Reader = bytes.NewReader(data)
		if chunk%2 == 0 {
			r = iotest.OneByteReader(r)
		}
		fr := NewFrameReader(r, max)
		consumed := 0
		for {
			fm, err := fr.Next()
			if err != nil {
				if err == io.EOF && consumed != len(data) {
					t.Fatalf("io.EOF after %d of %d bytes", consumed, len(data))
				}
				return
			}
			if fr.Max() >= 0 && len(fm.Payload) > fr.Max() {
				t.Fatalf("payload of %d bytes over limit %d", len(fm.Payload), fr.Max())
			}
			// Re-encoding gives back the input bytes.
			enc := AppendFrame(nil, fm.Compressed, fm.Payload)
			if !bytes.Equal(enc, data[consumed:consumed+len(enc)]) {
				t.Fatalf("re-encoded frame differs from input at %d", consumed)
			}
			consumed += len(enc)
		}
	})
}
