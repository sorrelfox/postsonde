package transport

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestWebTextReader(t *testing.T) {
	t.Parallel()
	a, b, c := []byte("first"), []byte("second message"), []byte{0, 1, 2, 0xff}
	std := base64.StdEncoding.EncodeToString
	tests := []struct {
		name  string
		input string
		want  []byte
	}{
		{"one segment", std(slices.Concat(a, b, c)), slices.Concat(a, b, c)},
		{"padded segments", std(a) + std(b) + std(c), slices.Concat(a, b, c)},
		{"line breaks", std(a) + "\r\n" + std(b) + "\n " + std(c), slices.Concat(a, b, c)},
		{"unpadded end", std(a) + base64.RawStdEncoding.EncodeToString(b), slices.Concat(a, b)},
		{"empty", "", nil},
	}
	readers := map[string]func(io.Reader) io.Reader{
		"whole":    func(r io.Reader) io.Reader { return r },
		"one byte": iotest.OneByteReader,
		"half":     iotest.HalfReader,
	}
	for _, tt := range tests {
		for name, wrap := range readers {
			got, err := io.ReadAll(NewWebTextReader(wrap(strings.NewReader(tt.input))))
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Errorf("%s/%s: %q, %v; want %q", tt.name, name, got, err, tt.want)
			}
		}
	}
	for _, bad := range []string{"abc!", "a", std(a)[:len(std(a))-1] + "*"} {
		_, err := io.ReadAll(NewWebTextReader(strings.NewReader(bad)))
		if !errors.Is(err, errWebText) {
			t.Errorf("%q: error %v, want errWebText", bad, err)
		}
	}
}

func TestTrailerFrame(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Grpc-Status", "5")
	h.Set("Grpc-Message", "not found")
	h.Add("X-Id", "1")
	h.Add("X-Id", "2")
	wire := AppendTrailerFrame(nil, h)
	if wire[0] != 0x80 {
		t.Fatalf("flag = %#x", wire[0])
	}
	if want := "grpc-message: not found\r\ngrpc-status: 5\r\nx-id: 1\r\nx-id: 2\r\n"; string(wire[FrameHeaderLen:]) != want {
		t.Fatalf("block = %q", wire[FrameHeaderLen:])
	}
	// A message frame, then the trailer frame.
	wire = append(AppendFrame(nil, false, []byte("m")), wire...)
	fr := NewWebFrameReader(bytes.NewReader(wire), 0)
	if f, err := fr.Next(); err != nil || f.Trailer || string(f.Payload) != "m" {
		t.Fatalf("first frame = %+v, %v", f, err)
	}
	f, err := fr.Next()
	if err != nil || !f.Trailer || f.Compressed {
		t.Fatalf("trailer frame = %+v, %v", f, err)
	}
	got, err := parseTrailerBlock(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	st, found := parseStatus(got)
	if !found || st.Code != NotFound || st.Message != "not found" || !slices.Equal(got["X-Id"], []string{"1", "2"}) {
		t.Fatalf("parsed %v: %+v", got, st)
	}

	// A plain gRPC reader refuses the trailer flag.
	var flagErr *FrameFlagError
	if _, err := NewFrameReader(bytes.NewReader(AppendTrailerFrame(nil, h)), 0).Next(); !errors.As(err, &flagErr) || flagErr.Flag != 0x80 {
		t.Fatalf("gRPC reader: %v", err)
	}
	// Web readers accept 0x80 and 0x81 only.
	if _, err := NewWebFrameReader(bytes.NewReader([]byte{0x82, 0, 0, 0, 0}), 0).Next(); !errors.As(err, &flagErr) || flagErr.Flag != 0x82 {
		t.Fatalf("flag 0x82: %v", err)
	}
	if f, err := NewWebFrameReader(bytes.NewReader([]byte{0x81, 0, 0, 0, 0}), 0).Next(); err != nil || !f.Trailer || !f.Compressed {
		t.Fatalf("flag 0x81: %+v, %v", f, err)
	}
}

func TestParseTrailerBlock(t *testing.T) {
	t.Parallel()
	h, err := parseTrailerBlock([]byte("grpc-status:0\n\r\nGrpc-Message:  ok \r\n"))
	if err != nil || h.Get("Grpc-Status") != "0" || h.Get("Grpc-Message") != "ok" {
		t.Fatalf("lenient block: %v, %v", h, err)
	}
	for _, bad := range []string{"no colon\r\n", ": empty key\r\n", "bad key: v\r\n"} {
		if _, err := parseTrailerBlock([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestWebContentType(t *testing.T) {
	t.Parallel()
	for ct, want := range map[string][2]bool{
		"application/grpc-web":                 {true, false},
		"application/grpc-web+proto":           {true, false},
		"Application/GRPC-Web;charset=utf-8":   {true, false},
		"application/grpc-web-text":            {true, true},
		"application/grpc-web-text+proto":      {true, true},
		"application/grpc":                     {false, false},
		"application/grpc+proto":               {false, false},
		"application/grpc-webby":               {false, false},
		"application/grpc-web-textual":         {false, false},
		"text/plain":                           {false, false},
		"application/grpc-web-text; charset=x": {true, true},
	} {
		ok, text := webContentType(ct)
		if ok != want[0] || text != want[1] {
			t.Errorf("%q = %v, %v; want %v", ct, ok, text, want)
		}
	}
}
