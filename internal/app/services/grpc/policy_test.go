package grpc

import (
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// The admin policy's requireTlsVerification (SET-02): a TLS call that
// turns verification off is refused before it connects; a plaintext call
// is not affected by its verifyTls.
func TestInvokeRequireTLSVerification(t *testing.T) {
	t.Parallel()
	s, _ := newCallService(t, WithRequireTLSVerification(true))
	off := false
	in := input("127.0.0.1:1", "Echo")
	in.TLS, in.VerifyTLS = true, &off
	_, err := s.Invoke(t.Context(), in)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != coresettings.CodeTLSVerificationRequired || e.Kind != errs.PermissionDenied {
		t.Fatalf("Invoke = %v, want %s", err, coresettings.CodeTLSVerificationRequired)
	}

	in.TLS = false
	if _, err := s.Invoke(t.Context(), in); err != nil {
		t.Errorf("plaintext call refused: %v", err)
	}
}
