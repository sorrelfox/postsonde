package httpclient

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// streamServer answers with a text/event-stream that sends the lines of
// next as they arrive and ends when next is closed.
func streamServer(t *testing.T, next <-chan string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case line, ok := <-next:
				if !ok {
					return
				}
				_, _ = io.WriteString(w, line)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hasHeader(hs []Header, name, value string) bool {
	for _, h := range hs {
		if h.Name == name && h.Value == value {
			return true
		}
	}
	return false
}

// TestSendStream reads a body chunk by chunk while the server is still
// sending it, past the request's timeout, and returns the response with
// the bytes read counted.
func TestSendStream(t *testing.T) {
	t.Parallel()
	next := make(chan string)
	srv := streamServer(t, next)
	c := newTestClient(t, TransportOptions{})
	r := get(srv.URL + "/events")
	r.Settings.Timeout = 200 * time.Millisecond

	var gotHead *Response
	var lines []string
	resp, err := c.SendStream(t.Context(), r, func(head *Response, body io.Reader) error {
		gotHead = head
		sc := bufio.NewScanner(body)
		next <- "a\n"
		for sc.Scan() {
			lines = append(lines, sc.Text())
			switch len(lines) {
			case 1:
				// Longer than the timeout: it only covers the headers.
				time.Sleep(300 * time.Millisecond)
				next <- "b\n"
			case 2:
				close(next)
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHead.StatusCode != 200 || !hasHeader(gotHead.Headers, "Content-Type", "text/event-stream") || len(gotHead.Body) != 0 {
		t.Errorf("head = %+v", gotHead)
	}
	if strings.Join(lines, ",") != "a,b" {
		t.Errorf("lines = %q", lines)
	}
	if resp == nil || resp.Size != 4 || resp.StatusCode != 200 || resp.Request == nil || resp.Timing.Total < 300*time.Millisecond {
		t.Errorf("resp = %+v", resp)
	}
}

// TestSendStreamCancel: cancelling the context ends the body with a
// cancelled error, which fn returns.
func TestSendStreamCancel(t *testing.T) {
	t.Parallel()
	next := make(chan string)
	srv := streamServer(t, next)
	c := newTestClient(t, TransportOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := c.SendStream(ctx, get(srv.URL), func(head *Response, body io.Reader) error {
		next <- "x\n"
		buf := make([]byte, 16)
		if _, err := body.Read(buf); err != nil {
			return err
		}
		cancel()
		_, err := io.ReadAll(body)
		return err
	})
	wantErr(t, err, errs.Cancelled, CodeCancelled)
}

// TestSendStreamHeaderTimeout: a server that never sends the headers hits
// the timeout, and fn is not called.
func TestSendStreamHeaderTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	c := newTestClient(t, TransportOptions{})
	r := get(srv.URL)
	r.Settings.Timeout = 50 * time.Millisecond
	called := false
	resp, err := c.SendStream(t.Context(), r, func(*Response, io.Reader) error {
		called = true
		return nil
	})
	wantErr(t, err, errs.Timeout, CodeTimeout)
	if called || resp != nil {
		t.Errorf("called = %v, resp = %v", called, resp)
	}
}
