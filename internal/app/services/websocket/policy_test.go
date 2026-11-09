package websocket

import (
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// The admin policy's requireTlsVerification (SET-02): a connect that turns
// verification off is refused at once, before it dials.
func TestConnectRequireTLSVerification(t *testing.T) {
	t.Parallel()
	s := New(netx.NewDirect(netx.Options{}), nil, nil, nil, WithRequireTLSVerification(true))
	off := false
	_, err := s.Connect(t.Context(), ConnectInputDTO{URL: "wss://127.0.0.1:1/", VerifyTLS: &off})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != coresettings.CodeTLSVerificationRequired || e.Kind != errs.PermissionDenied {
		t.Fatalf("Connect = %v, want %s", err, coresettings.CodeTLSVerificationRequired)
	}
}
