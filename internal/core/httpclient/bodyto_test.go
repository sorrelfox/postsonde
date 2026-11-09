package httpclient

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// zeroReader yields n zero bytes, then io.EOF, without allocating them.
type zeroReader struct{ n int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), z.n)]
	clear(p)
	z.n -= int64(len(p))
	return len(p), nil
}

// countWriter counts what is written to it and keeps nothing.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// sendTo sends a GET to url with SendTo into a countWriter.
func sendTo(t *testing.T, c *Client, ctx context.Context, url string, limit int64, decode bool, progress func(int64)) (*Response, *countWriter, error) {
	t.Helper()
	w := &countWriter{}
	r := get(url)
	r.Settings.MaxBodyBytes = limit
	resp, err := c.SendTo(ctx, r, BodyTarget{
		Open:     func(*Response) (io.Writer, error) { return w, nil },
		Decode:   decode,
		Progress: progress,
	})
	return resp, w, err
}

// TestSendToLargeBodyConstantMemory streams a 200 MiB body through
// SendTo and checks that the client allocated a small fraction of it
// (HTTP-37): the body is never held in memory.
func TestSendToLargeBodyConstantMemory(t *testing.T) {
	const size = 200 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = io.Copy(w, &zeroReader{n: size})
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	var calls atomic.Int64
	var last atomic.Int64
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	resp, w, err := sendTo(t, c, t.Context(), srv.URL, -1, true, func(n int64) {
		calls.Add(1)
		last.Store(n)
	})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if w.n != size || resp.Size != size || resp.DecodedSize != size || resp.BodyTruncated {
		t.Fatalf("written %d, Size %d, DecodedSize %d, truncated %v; want %d each, not truncated", w.n, resp.Size, resp.DecodedSize, resp.BodyTruncated, size)
	}
	if len(resp.Body) != 0 {
		t.Errorf("Body has %d bytes, want none", len(resp.Body))
	}
	// The server's side runs in this process too; both together stay far
	// below the body size.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 24<<20 {
		t.Errorf("allocated %d MiB while streaming a %d MiB body", alloc>>20, size>>20)
	}
	if n := calls.Load(); n < size/ProgressStep/2 || n > size/ProgressStep+1 {
		t.Errorf("%d progress calls, want about %d", n, size/ProgressStep)
	}
	if last.Load() != size {
		t.Errorf("last progress = %d, want %d", last.Load(), size)
	}
}

func TestSendToLimit(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	resp, w, err := sendTo(t, c, t.Context(), srv.URL, 4096, false, nil)
	if err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if !resp.BodyTruncated || w.n != 4096 || resp.DecodedSize != 4096 || resp.Size <= 4096 {
		t.Errorf("truncated %v, written %d, DecodedSize %d, Size %d; want true, 4096, 4096, > 4096", resp.BodyTruncated, w.n, resp.DecodedSize, resp.Size)
	}

	// Exactly at the limit is not truncated.
	resp, w, err = sendTo(t, c, t.Context(), srv.URL, int64(len(body)), false, nil)
	if err != nil || resp.BodyTruncated || w.n != int64(len(body)) {
		t.Errorf("at the limit: err %v, truncated %v, written %d", err, resp != nil && resp.BodyTruncated, w.n)
	}
}

func TestSendToDecodes(t *testing.T) {
	plain := bytes.Repeat([]byte(`{"k":"value"},`), 50000)
	gz := func(b []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(b)
		_ = zw.Close()
		return buf.Bytes()
	}
	zl := func(b []byte) []byte {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		_, _ = zw.Write(b)
		_ = zw.Close()
		return buf.Bytes()
	}
	raw := func(b []byte) []byte {
		var buf bytes.Buffer
		zw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		_, _ = zw.Write(b)
		_ = zw.Close()
		return buf.Bytes()
	}
	gzipped := gz(plain)
	tests := []struct {
		name, coding string
		body         []byte
		decode       bool
		wantEnc      string
		wantN        int
		wantErr      bool
	}{
		{"gzip", "gzip", gzipped, true, EncodingGzip, len(plain), false},
		{"x-gzip", "X-GZIP", gzipped, true, EncodingGzip, len(plain), false},
		{"zlib deflate", "deflate", zl(plain), true, EncodingDeflate, len(plain), false},
		{"raw deflate", "deflate", raw(plain), true, EncodingDeflate, len(plain), false},
		{"not decoded", "gzip", gzipped, false, "", len(gzipped), false},
		{"brotli kept", "br", []byte("\x0b\x02\x80abc\x03"), true, "", 7, false},
		{"stacked kept", "gzip, gzip", gzipped, true, "", len(gzipped), false},
		{"not gzip", "gzip", []byte("plain text"), true, "", 10, true},
		{"cut gzip", "gzip", gzipped[:len(gzipped)/2], true, EncodingGzip, -1, true},
		{"empty", "gzip", nil, true, EncodingGzip, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Encoding", tt.coding)
				_, _ = w.Write(tt.body)
			}))
			defer srv.Close()
			c := newTestClient(t, TransportOptions{})
			var out bytes.Buffer
			r := get(srv.URL)
			resp, err := c.SendTo(t.Context(), r, BodyTarget{Open: func(*Response) (io.Writer, error) { return &out, nil }, Decode: tt.decode})
			if err != nil {
				t.Fatalf("SendTo: %v", err)
			}
			if resp.Encoding != tt.wantEnc {
				t.Errorf("Encoding = %q, want %q", resp.Encoding, tt.wantEnc)
			}
			if (resp.DecodeError != "") != tt.wantErr {
				t.Errorf("DecodeError = %q, want an error: %v", resp.DecodeError, tt.wantErr)
			}
			if resp.Size != int64(len(tt.body)) {
				t.Errorf("Size = %d, want %d (as received)", resp.Size, len(tt.body))
			}
			if tt.wantN >= 0 && out.Len() != tt.wantN {
				t.Errorf("wrote %d bytes, want %d", out.Len(), tt.wantN)
			}
			if resp.DecodedSize != int64(out.Len()) {
				t.Errorf("DecodedSize = %d, wrote %d", resp.DecodedSize, out.Len())
			}
			if tt.wantEnc != "" && !tt.wantErr && !bytes.Equal(out.Bytes(), plain[:out.Len()]) {
				t.Error("decoded body differs from the original")
			}
			if tt.name == "not gzip" && out.String() != "plain text" {
				t.Errorf("body = %q, want it as received", out.String())
			}
		})
	}
}

// TestSendToGzipBomb checks that the limit applies to the decoded bytes.
func TestSendToGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = io.Copy(zw, &zeroReader{n: 64 << 20})
	_ = zw.Close()
	bomb := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(bomb)
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})
	resp, w, err := sendTo(t, c, t.Context(), srv.URL, 1<<20, true, nil)
	if err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if !resp.BodyTruncated || w.n != 1<<20 {
		t.Errorf("truncated %v, written %d; want true, 1 MiB", resp.BodyTruncated, w.n)
	}
}

func TestSendToCancelMidBody(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(100<<20))
		_, _ = w.Write(make([]byte, 1<<20))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	resp, w, err := sendTo(t, c, ctx, srv.URL, -1, true, nil)
	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	wantErr(t, err, errs.Cancelled, CodeCancelled)
	if w.n == 0 {
		t.Log("cancelled before any byte was written")
	}
}

func TestSendToOpenAndWriteErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	openErr := errors.New("no room")
	_, err := c.SendTo(t.Context(), get(srv.URL), BodyTarget{Open: func(*Response) (io.Writer, error) { return nil, openErr }})
	if !errors.Is(err, openErr) {
		t.Errorf("Open error: got %v, want %v", err, openErr)
	}

	_, err = c.SendTo(t.Context(), get(srv.URL), BodyTarget{Open: func(*Response) (io.Writer, error) { return failWriter{}, nil }})
	wantErr(t, err, errs.IO, CodeBodyWriteFailed)

	_, err = c.SendTo(t.Context(), get(srv.URL), BodyTarget{})
	if errs.KindOf(err) != errs.InvalidArgument {
		t.Errorf("no Open: got %v, want invalid_argument", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestSendToHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})
	var head *Response
	resp, err := c.SendTo(t.Context(), get(srv.URL), BodyTarget{Open: func(h *Response) (io.Writer, error) {
		head = h
		return io.Discard, nil
	}})
	if err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if head == nil || head.StatusCode != 200 || head.ContentLength != 4 || len(head.Headers) == 0 {
		t.Errorf("head = %+v", head)
	}
	if resp.DecodedSize != 4 || resp.Size != 4 {
		t.Errorf("sizes %d/%d, want 4", resp.Size, resp.DecodedSize)
	}
}
