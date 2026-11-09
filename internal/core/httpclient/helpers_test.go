package httpclient

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// okResponse is a minimal HTTP/1.1 answer that closes the connection.
const okResponse = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"

// newTestClient returns a Client over a Direct dialer.
func newTestClient(t *testing.T, o TransportOptions) *Client {
	t.Helper()
	c := New(netx.NewDirect(netx.Options{}), o)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func get(url string, headers ...Header) Request {
	return Request{Method: http.MethodGet, URL: url, Headers: headers, Settings: DefaultSettings()}
}

func on(name, value string) Header { return Header{Name: name, Value: value, Enabled: true} }

// wantErr checks that err is an *errs.Error itself with kind and code.
func wantErr(t *testing.T, err error, kind errs.Kind, code string) *errs.Error {
	t.Helper()
	e, ok := err.(*errs.Error)
	if !ok {
		t.Fatalf("error = %T %v, want *errs.Error", err, err)
	}
	if e.Kind != kind || e.Code != code {
		t.Fatalf("error kind, code = %q, %q, want %q, %q (%v)", e.Kind, e.Code, kind, code, err)
	}
	if got := errs.KindOf(err); got != kind {
		t.Errorf("errs.KindOf = %q, want %q", got, kind)
	}
	return e
}

// rawServer is a TCP server that records each request byte for byte and
// answers it with a fixed response, then closes the connection.
type rawServer struct {
	addr string
	reqs chan []byte
}

// startRawServer starts a rawServer; an empty response closes the
// connection without answering.
func startRawServer(t *testing.T, response string) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &rawServer{addr: ln.Addr().String(), reqs: make(chan []byte, 16)}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				raw, err := readRawRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				select {
				case s.reqs <- raw:
				default:
				}
				io.WriteString(conn, response)
			})
		}
	})
	return s
}

// next returns the next request the server received.
func (s *rawServer) next(t *testing.T) []byte {
	t.Helper()
	select {
	case r := <-s.reqs:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the raw server received no request")
		return nil
	}
}

// readRawRequest reads one HTTP/1.1 request: the head, then a body framed
// by Content-Length or chunked encoding (without trailers).
func readRawRequest(br *bufio.Reader) ([]byte, error) {
	var raw bytes.Buffer
	var length int64
	chunked := false
	for first := true; ; first = false {
		line, err := br.ReadString('\n')
		raw.WriteString(line)
		if err != nil {
			return raw.Bytes(), err
		}
		if line == "\r\n" {
			break
		}
		if first {
			continue
		}
		name, value, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "content-length":
			length, _ = strconv.ParseInt(value, 10, 64)
		case "transfer-encoding":
			chunked = strings.EqualFold(value, "chunked")
		}
	}
	if !chunked {
		_, err := io.CopyN(&raw, br, length)
		return raw.Bytes(), err
	}
	for {
		line, err := br.ReadString('\n')
		raw.WriteString(line)
		if err != nil {
			return raw.Bytes(), err
		}
		size, err := strconv.ParseInt(strings.TrimRight(line, "\r\n"), 16, 64)
		if err != nil {
			return raw.Bytes(), err
		}
		if size == 0 {
			end, err := br.ReadString('\n')
			raw.WriteString(end)
			return raw.Bytes(), err
		}
		if _, err := io.CopyN(&raw, br, size+2); err != nil {
			return raw.Bytes(), err
		}
	}
}

// renderWire makes raw request bytes readable and stable for a golden
// file: \r and \n are shown as escapes (each \n also ends the line), other
// bytes outside printable ASCII as \xNN, and addr as ADDR.
func renderWire(raw []byte, addr string) []byte {
	raw = bytes.ReplaceAll(raw, []byte(addr), []byte("ADDR"))
	var b bytes.Buffer
	for _, c := range raw {
		switch {
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\n':
			b.WriteString("\\n\n")
		case c == '\\':
			b.WriteString(`\\`)
		case c < ' ' || c > '~':
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 && !bytes.HasSuffix(b.Bytes(), []byte("\n")) {
		b.WriteString("\n")
	}
	return b.Bytes()
}

// blockingServer starts an httptest server whose handler calls write (if
// set), signals entered, then blocks until the client goes away or the
// test ends.
func blockingServer(t *testing.T, write func(http.ResponseWriter)) (srv *httptest.Server, entered <-chan struct{}) {
	t.Helper()
	in := make(chan struct{}, 1)
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if write != nil {
			write(w)
			w.(http.Flusher).Flush()
		}
		select {
		case in <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-in first-out: release the handlers, then close.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, in
}

// waitGoroutines waits until at most n goroutines run, and fails with a
// dump of them if that does not happen soon.
func waitGoroutines(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > n {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("%d goroutines still running, want at most %d:\n%s", runtime.NumGoroutine(), n, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
