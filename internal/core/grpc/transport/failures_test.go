package transport_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// TestNetworkFailureMessages provokes DNS, refused and reset failures for
// real and checks the shared code, the message, the hint and the gRPC
// status (DBG-09). TLS certificate failures are in
// TestServerVerificationFailures. A reset of a plaintext (h2c) connection
// reaches the transport only as "client conn could not be established",
// so it stays connection_failed.
func TestNetworkFailureMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address func(t *testing.T) string
		dial    netx.Options
		tls     bool
		kind    errs.Kind
		code    string
		message string // ADDR is the address
		hint    string
	}{
		{"dns not found", func(*testing.T) string { return "nothing-here.invalid:443" }, netx.Options{Resolver: netxtest.NXDomainResolver()}, true,
			errs.Network, netx.CodeDNSNotFound, "The host name in ADDR was not found.", netx.HintDNSNotFound},
		{"connection refused", closedAddr, netx.Options{}, false,
			errs.Network, netx.CodeConnectionRefused, "The connection to ADDR was refused.", netx.HintConnectionRefused},
		{"reset, TLS", resettingServer, netx.Options{}, true,
			errs.Network, transport.CodeConnectionReset, "The server at ADDR reset the connection.", netx.HintConnectionReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr := tt.address(t)
			c := transport.New(netx.NewDirect(tt.dial), transport.Options{})
			t.Cleanup(c.CloseIdleConnections)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := c.Unary(ctx, transport.CallOptions{Address: addr, TLS: tt.tls, Method: "/test.Echo/Unary"}, []byte("x"))
			wantStatus(t, err, transport.Unavailable, tt.code)
			e, _ := errors.AsType[*errs.Error](err)
			if e.Kind != tt.kind {
				t.Errorf("kind = %s, want %s", e.Kind, tt.kind)
			}
			if want := strings.ReplaceAll(tt.message, "ADDR", addr); e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
			if e.Hint != tt.hint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
		})
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// resettingServer reads the client's first bytes (a TLS ClientHello or
// the HTTP/2 preface), then resets the connection (SO_LINGER 0).
func resettingServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				conn.Read(make([]byte, 16))
				conn.(*net.TCPConn).SetLinger(0)
				conn.Close()
			}()
		}
	}()
	return ln.Addr().String()
}
