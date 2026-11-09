package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestCancel cancels a request at every stage and checks that Send returns
// a cancelled error at once and leaves no goroutine behind. It is not
// parallel, so the goroutine count is its own.
func TestCancel(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		send  func(t *testing.T) error
	}{
		{"before sending", "before_send", func(t *testing.T) error {
			var dials atomic.Int32
			c := New(netx.DialerFunc(func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected dial")
			}), TransportOptions{})
			defer c.CloseIdleConnections()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := c.Send(ctx, get("http://127.0.0.1:1/"))
			if n := dials.Load(); n != 0 {
				t.Errorf("%d dials, want 0", n)
			}
			return err
		}},
		{"while connecting", "connecting", func(t *testing.T) error {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			c := New(netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
				entered <- struct{}{}
				select {
				case <-ctx.Done():
				case <-release:
				}
				return nil, netx.WrapDialError(context.Canceled, network, address)
			}), TransportOptions{})
			defer c.CloseIdleConnections()
			// net/http lets a dial outlive the cancelled request; end it.
			defer close(release)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				<-entered
				cancel()
			}()
			_, err := c.Send(ctx, get("http://192.0.2.1/")) // TEST-NET-1, never dialed for real
			return err
		}},
		{"while waiting for the response", "waiting", func(t *testing.T) error {
			srv, entered := blockingServer(t, nil)
			c := newTestClient(t, TransportOptions{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				<-entered
				cancel()
			}()
			_, err := c.Send(ctx, get(srv.URL))
			return err
		}},
		{"while reading the body", "reading_body", func(t *testing.T) error {
			srv, _ := blockingServer(t, func(w http.ResponseWriter) {
				w.Header().Set("Content-Length", "100")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "part of a slow body")
			})
			c := newTestClient(t, TransportOptions{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.afterHeaders = cancel
			_, err := c.Send(ctx, get(srv.URL))
			return err
		}},
	}
	for _, tt := range tests {
		base := runtime.NumGoroutine()
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := tt.send(t)
			e := wantErr(t, err, errs.Cancelled, CodeCancelled)
			if e.Details["stage"] != tt.stage {
				t.Errorf("stage = %q, want %q", e.Details["stage"], tt.stage)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(err, context.Canceled) = false for %v", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Send returned after %v", d)
			}
		})
		// The subtest's cleanups (server, idle connections) have run.
		waitGoroutines(t, base)
	}
}
