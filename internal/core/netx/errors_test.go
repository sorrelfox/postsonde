package netx

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// TestDialErrorMapping makes each error happen for real and checks the
// mapping and that the net package error is still reachable.
func TestDialErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		opts     Options
		address  func(*testing.T) string
		wantCode string
		wantHint string
		check    func(*testing.T, error)
	}{
		{
			name:     "connection refused",
			address:  closedPort,
			wantCode: CodeConnectionRefused,
			wantHint: HintConnectionRefused,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*net.OpError](err); !ok {
					t.Errorf("no *net.OpError in %v", err)
				}
			},
		},
		{
			name:     "dns not found",
			opts:     Options{Resolver: nxdomainResolver()},
			address:  func(*testing.T) string { return "nothing-here.invalid:80" },
			wantCode: CodeDNSNotFound,
			wantHint: HintDNSNotFound,
			check: func(t *testing.T, err error) {
				if de, ok := errors.AsType[*net.DNSError](err); !ok || !de.IsNotFound {
					t.Errorf("no not-found *net.DNSError in %v", err)
				}
				if _, ok := errors.AsType[*net.OpError](err); !ok {
					t.Errorf("no *net.OpError in %v", err)
				}
			},
		},
		{
			name:     "dns server failure",
			opts:     Options{Resolver: failingResolver()},
			address:  func(*testing.T) string { return "api.example.test:443" },
			wantCode: CodeDNSFailed,
			wantHint: HintDNSFailed,
			check: func(t *testing.T, err error) {
				if de, ok := errors.AsType[*net.DNSError](err); !ok || de.IsNotFound {
					t.Errorf("no failed (not not-found) *net.DNSError in %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr := tt.address(t)
			conn, err := NewDirect(tt.opts).DialContext(t.Context(), "tcp", addr)
			if conn != nil {
				conn.Close()
				t.Fatal("DialContext returned a connection")
			}
			e := wantErr(t, err, errs.Network, tt.wantCode)
			if e.Hint != tt.wantHint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.wantHint)
			}
			if e.Details["address"] != addr {
				t.Errorf("details = %v, want address %s", e.Details, addr)
			}
			tt.check(t, err)
		})
	}
}

// TestWrapDialError covers the mapping for errors that cannot be made to
// happen deterministically, such as unreachable hosts.
func TestWrapDialError(t *testing.T) {
	t.Parallel()
	opErr := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	denied := Deny(DialCheck{Network: "tcp", Address: "blocked.test:80"}, "Offline mode is on.")
	plain := errors.New("something odd")
	tests := []struct {
		name     string
		err      error
		wantKind errs.Kind
		wantCode string
		wantHint string
	}{
		{"cancelled", context.Canceled, errs.Cancelled, CodeCancelled, ""},
		{"deadline", context.DeadlineExceeded, errs.Timeout, CodeTimeout, HintTimeout},
		{"os deadline", opErr(os.ErrDeadlineExceeded), errs.Timeout, CodeTimeout, HintTimeout},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", Name: "x.test", IsTimeout: true}, errs.Timeout, CodeTimeout, HintTimeout},
		{"dns not found", &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}, errs.Network, CodeDNSNotFound, HintDNSNotFound},
		{"dns other", &net.DNSError{Err: "server misbehaving", Name: "x.test"}, errs.Network, CodeDNSFailed, HintDNSFailed},
		{"refused", opErr(refusedErrnos[0]), errs.Network, CodeConnectionRefused, HintConnectionRefused},
		{"host unreachable", opErr(unreachableErrnos[0]), errs.Network, CodeUnreachable, HintUnreachable},
		{"network unreachable", opErr(unreachableErrnos[len(unreachableErrnos)-1]), errs.Network, CodeUnreachable, HintUnreachable},
		{"other", plain, errs.Network, CodeDialFailed, ""},
		{"errs error inside", opErr(denied), errs.PermissionDenied, CodeDialDenied, "Offline mode is on."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := WrapDialError(tt.err, "tcp", "x.test:80")
			e := wantErr(t, err, tt.wantKind, tt.wantCode)
			if e.Hint != tt.wantHint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.wantHint)
			}
			if tt.name != "errs error inside" && !errors.Is(err, tt.err) {
				t.Errorf("errors.Is(err, cause) = false")
			}
		})
	}

	if err := WrapDialError(nil, "tcp", "x.test:80"); err != nil {
		t.Errorf("WrapDialError(nil) = %v", err)
	}
}
