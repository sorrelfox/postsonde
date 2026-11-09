package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestMapError covers the mapping of errors that are hard to provoke on a
// real connection (expired certificates, resets); the other tests provoke
// the rest.
func TestMapError(t *testing.T) {
	t.Parallel()

	urlErr := func(err error) error { return &url.Error{Op: "Get", URL: "https://h", Err: err} }
	verify := func(err error) error { return urlErr(&tls.CertificateVerificationError{Err: err}) }
	own := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	denied := netx.Deny(netx.DialCheck{Network: "tcp", Address: "h:443"}, "Offline mode is on.")

	tests := []struct {
		name  string
		ctx   context.Context
		err   error
		stage stage
		kind  errs.Kind
		code  string
	}{
		{"expired certificate", nil, verify(x509.CertificateInvalidError{Reason: x509.Expired}), stageConnecting, errs.TLS, CodeTLSCertificateExpired},
		{"invalid certificate", nil, verify(x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}), stageConnecting, errs.TLS, CodeTLSCertificateInvalid},
		{"unknown authority", nil, verify(x509.UnknownAuthorityError{}), stageConnecting, errs.TLS, CodeTLSUnknownAuthority},
		{"hostname mismatch", nil, verify(x509.HostnameError{Host: "h"}), stageConnecting, errs.TLS, CodeTLSHostnameMismatch},
		{"platform verifier error", nil, verify(errors.New("x509: certificate is not standards compliant")), stageConnecting, errs.TLS, CodeTLSCertificateInvalid},
		{"TLS alert", nil, urlErr(tls.AlertError(40)), stageConnecting, errs.TLS, CodeTLSHandshakeFailed},
		{"plain HTTP answer", nil, urlErr(http.ErrSchemeMismatch), stageConnecting, errs.TLS, CodeTLSServerSentHTTP},
		{"I/O timeout", nil, urlErr(&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}), stageConnecting, errs.Timeout, CodeConnectionTimeout},
		{"connection reset", nil, urlErr(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}), stageWaiting, errs.Network, CodeConnectionReset},
		{"other connection error", nil, urlErr(&net.OpError{Op: "read", Net: "tcp", Err: errors.New("something odd")}), stageWaiting, errs.Network, CodeConnectionFailed},
		{"closed", nil, urlErr(io.EOF), stageWaiting, errs.Network, CodeConnectionClosed},
		{"garbage", nil, urlErr(errors.New(`malformed HTTP response "x"`)), stageWaiting, errs.Protocol, CodeMalformedResponse},
		{"body cut short", nil, io.ErrUnexpectedEOF, stageBody, errs.Network, CodeBodyIncomplete},
		{"body reset", nil, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, stageBody, errs.Network, CodeConnectionReset},
		{"body read failed", nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("something odd")}, stageBody, errs.Network, CodeBodyReadFailed},
		{"bad chunk", nil, errors.New("invalid byte in chunk length"), stageBody, errs.Protocol, CodeMalformedBody},
		{"caller cancelled", cancelled, urlErr(context.Canceled), stageWaiting, errs.Cancelled, CodeCancelled},
		{"caller deadline", nil, urlErr(context.DeadlineExceeded), stageWaiting, errs.Timeout, CodeTimeout},
		{"request timeout", own(errRequestTimeout), urlErr(context.Canceled), stageConnecting, errs.Timeout, CodeTimeout},
		{"header timeout", own(errHeaderTimeout), urlErr(context.Canceled), stageWaiting, errs.Timeout, CodeResponseHeaderTimeout},
		{"errs error kept", nil, urlErr(denied), stageConnecting, errs.PermissionDenied, netx.CodeDialDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			e := mapError(ctx, tt.err, tt.stage, "h:443")
			wantErr(t, e, tt.kind, tt.code)
			if !errors.Is(e, tt.err) && e != denied {
				t.Errorf("the cause is lost: %v", e)
			}
			if tt.code != netx.CodeDialDenied && e.Details["stage"] != tt.stage.String() {
				t.Errorf("stage = %q, want %q", e.Details["stage"], tt.stage)
			}
		})
	}
}
