package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// patternReader yields n bytes of a repeating ASCII line without
// allocating them.
type patternReader struct {
	n   int64
	off int
}

const patternLine = "0123456789abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ\n"

func (p *patternReader) Read(b []byte) (int, error) {
	if p.n <= 0 {
		return 0, io.EOF
	}
	b = b[:min(int64(len(b)), p.n)]
	for i := range b {
		b[i] = patternLine[p.off]
		p.off = (p.off + 1) % len(patternLine)
	}
	p.n -= int64(len(b))
	return len(b), nil
}

// spoolFiles lists the files of the spool directory.
func spoolFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestSendLargeBodyStreamsToDisk sends a request whose body is 200 MiB
// and checks that it went to a spool file while arriving, with progress
// events counting the bytes, and that the send allocated a small fraction
// of the body (HTTP-37). Not parallel: the allocation count is its own.
func TestSendLargeBodyStreamsToDisk(t *testing.T) {
	const size = 200 << 20
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = io.Copy(w, &patternReader{n: size})
	}))
	defer srv.Close()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixture(t, httpSource(srv.URL), nil, WithSaveBody(spool, nil), WithMaxBodySize(func() int64 { return 1 << 30 }))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	id := f.send(t)
	// 200 MiB takes over 10 s under -race when other packages run too.
	done := f.rec.waitWithin(t, id, time.Minute)
	runtime.ReadMemStats(&after)
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if resp.Size != size || resp.BodySize != size || resp.BodyTruncated || resp.BodyLimit != 1<<30 {
		t.Fatalf("size %d, bodySize %d, truncated %v, limit %d", resp.Size, resp.BodySize, resp.BodyTruncated, resp.BodyLimit)
	}
	if !resp.BodyClipped || resp.BodyShown > MaxBodyText || !strings.HasPrefix(resp.Body, patternLine) {
		t.Errorf("view: clipped %v, shown %d", resp.BodyClipped, resp.BodyShown)
	}
	// The server, the client and the service run in this process; all
	// together stay far below the body size.
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %.1f MiB, heap in use %.1f MiB", float64(alloc)/(1<<20), float64(after.HeapInuse)/(1<<20))
	if alloc > 40<<20 {
		t.Errorf("allocated %d MiB while receiving a %d MiB body", alloc>>20, size>>20)
	}
	files := spoolFiles(t, spool)
	if len(files) != 1 {
		t.Fatalf("spool = %v, want one file", files)
	}
	if info, err := os.Stat(filepath.Join(spool, files[0])); err != nil || info.Size() != size {
		t.Errorf("spool file: %v, %v", info, err)
	}

	f.rec.mu.Lock()
	events := f.rec.counts[id]
	f.rec.mu.Unlock()
	var receiving []operation.ProgressEventDTO
	for _, e := range events {
		if e.Message == StageReceiving && e.Total > 0 {
			receiving = append(receiving, e)
		}
	}
	if len(receiving) == 0 {
		t.Fatalf("no receiving progress with a total: %+v", events)
	}
	last := receiving[len(receiving)-1]
	if last.Total != size || last.Done != size {
		t.Errorf("last progress = %d of %d, want %d of %d", last.Done, last.Total, size, size)
	}

	// The end of the body, a page away.
	page, err := f.svc.ReadBody(t.Context(), ReadBodyInputDTO{BodyID: resp.BodyID, Offset: size - 100, Charset: resp.Charset})
	if err != nil || page.End != size || page.Size != size || len(page.Text) != 100 {
		t.Errorf("last page: %+v, %v", page, err)
	}
}

func TestSendBodyLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = io.Copy(w, &patternReader{n: 3 << 20})
	}))
	defer srv.Close()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixture(t, httpSource(srv.URL), nil, WithSaveBody(spool, nil), WithMaxBodySize(func() int64 { return 2 << 20 }))
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if !resp.BodyTruncated || resp.BodySize != 2<<20 || resp.BodyLimit != 2<<20 {
		t.Errorf("truncated %v, bodySize %d, limit %d", resp.BodyTruncated, resp.BodySize, resp.BodyLimit)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Code != CodeBodyLimit || !strings.Contains(resp.Warnings[0].Message, "2 MiB") {
		t.Errorf("warnings = %+v", resp.Warnings)
	}
	if info, err := os.Stat(filepath.Join(spool, resp.BodyID+".body")); err != nil || info.Size() != 2<<20 {
		t.Errorf("kept part: %v, %v", info, err)
	}
}

// TestSendCancelRemovesSpool cancels a send while its body arrives: the
// part already written to the spool is removed.
func TestSendCancelRemovesSpool(t *testing.T) {
	t.Parallel()
	written := make(chan struct{})
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(100<<20))
		_, _ = io.Copy(w, &patternReader{n: 4 << 20})
		w.(nethttp.Flusher).Flush()
		close(written)
		<-r.Context().Done()
	}))
	defer srv.Close()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixture(t, httpSource(srv.URL), nil, WithSaveBody(spool, nil))
	id := f.send(t)
	<-written
	// Wait until the client has spooled part of it.
	deadline := time.Now().Add(5 * time.Second)
	for len(spoolFiles(t, spool)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(spoolFiles(t, spool)) == 0 {
		t.Fatal("nothing was spooled")
	}
	f.ops.Cancel(id)
	if done := f.rec.wait(t, id); done.Status != operation.StatusCancelled {
		t.Fatalf("done = %+v", done)
	}
	if files := spoolFiles(t, spool); len(files) != 0 {
		t.Errorf("spool after cancel = %v, want empty", files)
	}
}

func TestSendGzipStreams(t *testing.T) {
	t.Parallel()
	var plain bytes.Buffer
	_, _ = io.Copy(&plain, &patternReader{n: 3 << 20})
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write(plain.Bytes())
	_ = zw.Close()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(zipped.Bytes())
	}))
	defer srv.Close()
	spool := filepath.Join(t.TempDir(), "spool")
	out := filepath.Join(t.TempDir(), "saved.txt")
	f := newFixture(t, httpSource(srv.URL), nil, WithSaveBody(spool, func(context.Context, SaveDialog) (string, error) { return out, nil }))
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if resp.ContentEncoding != "gzip" || resp.Size != int64(zipped.Len()) || resp.BodySize != int64(plain.Len()) {
		t.Errorf("encoding %q, size %d, bodySize %d", resp.ContentEncoding, resp.Size, resp.BodySize)
	}
	if resp.BodyHex || !strings.HasPrefix(resp.Body, patternLine) || resp.BodyNote != "" {
		t.Errorf("body shown as hex %v, note %q", resp.BodyHex, resp.BodyNote)
	}
	res, err := f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: resp.BodyID})
	if err != nil || res.Size != int64(plain.Len()) {
		t.Fatalf("save = %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, plain.Bytes()) {
		t.Error("saved body differs from the decompressed body")
	}
}

// TestLargeBodyAssertions checks a body over SpoolThreshold from its
// file, and a body over MaxEvalBodyBytes not at all.
func TestLargeBodyAssertions(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = io.Copy(w, &patternReader{n: 3 << 20})
		_, _ = io.WriteString(w, "THE END")
	}))
	defer srv.Close()
	src := httpSource(srv.URL)
	src.Request.Assertions = []projectfile.Assertion{
		{Target: "body", Op: "contains", Expected: "THE END"},
		{Target: "size", Op: "equals", Expected: strconv.Itoa(3<<20 + 7)},
	}
	f := newFixture(t, src, nil, WithSaveBody(filepath.Join(t.TempDir(), "spool"), nil))
	id := f.send(t)
	f.rec.wait(t, id)
	tests := f.rec.response(t, id).Tests
	if len(tests) != 2 || tests[0].Outcome != "passed" || tests[1].Outcome != "passed" {
		t.Errorf("tests = %+v", tests)
	}

	sp := &spool{n: MaxEvalBodyBytes + 1, path: "/nonexistent"}
	items := []checkItem{{a: assert.Assertion{Target: assert.TargetBody, Op: assert.OpContains, Expected: "x"}}}
	res := runChecks(t.Context(), items, &httpclient.Response{Body: []byte{}}, sp, &httpclient.SecretMasker{}, nil)
	if len(res) != 1 || res[0].Outcome != "invalid" || res[0].Code != CodeBodyTooLarge {
		t.Errorf("over the limit: %+v", res)
	}
}

func TestReadBodyPages(t *testing.T) {
	t.Parallel()
	s := New(nil, nil, nil, nil, nil, nil, WithSaveBody(t.TempDir(), nil))
	// "é" is two bytes in UTF-8: a page boundary at an odd offset falls
	// inside one.
	body := []byte(strings.Repeat("é", MaxBodyText))
	id := s.bodies.put(body, "x.txt")

	var pages int
	var text strings.Builder
	for off := int64(0); off < int64(len(body)); pages++ {
		p, err := s.ReadBody(t.Context(), ReadBodyInputDTO{BodyID: id, Offset: off, Charset: charset.UTF8})
		if err != nil || p.Start != off || p.End <= off || p.Binary {
			t.Fatalf("page at %d: %+v, %v", off, p, err)
		}
		text.WriteString(p.Text)
		off = p.End
	}
	if pages != 2 || text.String() != string(body) {
		t.Errorf("%d pages, text equal %v", pages, text.String() == string(body))
	}

	// Starting inside a character moves to the next one.
	p, err := s.ReadBody(t.Context(), ReadBodyInputDTO{BodyID: id, Offset: 1})
	if err != nil || p.Start != 2 || !strings.HasPrefix(p.Text, "é") || p.Binary {
		t.Errorf("mid-character page: start %d, %v", p.Start, err)
	}

	// Hex pages start on a line.
	p, err = s.ReadBody(t.Context(), ReadBodyInputDTO{BodyID: id, Offset: 70005, Hex: true})
	if err != nil || p.Start != 70000 || p.End-p.Start != MaxHexBytes || !strings.HasPrefix(p.Text, "00011170  ") {
		t.Errorf("hex page: %d-%d %q, %v", p.Start, p.End, p.Text[:min(len(p.Text), 20)], err)
	}

	if _, err := s.ReadBody(t.Context(), ReadBodyInputDTO{BodyID: "gone"}); err == nil {
		t.Error("unknown body read")
	}
}
