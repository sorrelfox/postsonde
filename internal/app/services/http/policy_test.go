package http

import (
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// The admin policy's requireTlsVerification (SET-02): a request that turns
// verification off is refused before anything is sent; verified requests
// still go out.
func TestSendRequireTLSVerification(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)

	no := false
	src := httpSource(srv.URL)
	src.Request.Settings.VerifyTLS = &no
	f := newFixture(t, src, nil, WithRequireTLSVerification(true))

	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("Send = %v, want an error", err)
	}
	checkErr(t, e, errs.PermissionDenied, coresettings.CodeTLSVerificationRequired)
	if hits.Load() != 0 {
		t.Errorf("the refused request reached the server %d times", hits.Load())
	}

	f.src.Request.Settings.VerifyTLS = nil
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK || hits.Load() != 1 {
		t.Errorf("verified send: done %+v, hits %d", done, hits.Load())
	}
}

func TestGraphQLSubscriptionRequireTLSVerification(t *testing.T) {
	t.Parallel()
	f := newFixture(t, subscriptionSource("{{base}}/graphql"), nil,
		WithVerifyTLS(func() bool { return false }), WithRequireTLSVerification(true))
	_, err := NewGraphQLSubscription(t.Context(), f.svc, "req_sub", false)
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("NewGraphQLSubscription = %v, want an error", err)
	}
	checkErr(t, e, errs.PermissionDenied, coresettings.CodeTLSVerificationRequired)
}
