package settings

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// TestSetProxyBypass (SOCKS-09): the list is checked entry by entry,
// saved to settings.json, returned by Proxy with NO_PROXY, and sends
// settings:changed only when it changed.
func TestSetProxyBypass(t *testing.T) {
	t.Parallel()
	svc, rec, path, _ := proxyService(t, "", map[string]string{"no_proxy": ".lan"})
	ctx := t.Context()

	got, err := svc.Proxy(ctx)
	if err != nil || len(got.Bypass) != 0 || got.Bypass == nil || got.NoProxyVar != "no_proxy" || got.NoProxy != ".lan" {
		t.Fatalf("defaults: %+v, %v", got, err)
	}
	_, err = svc.SetProxyBypass(ctx, []string{"ok.test", "10.0.0.0/33"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != netx.CodeProxyInvalidBypass || e.Details["field"] != "bypass" || e.Details["entry"] != "10.0.0.0/33" {
		t.Fatalf("invalid entry: %v", err)
	}
	if len(rec.all()) != 0 {
		t.Error("a refused list sent an event")
	}

	got, err = svc.SetProxyBypass(ctx, []string{" *.corp ", "", "[::1]:8080"})
	if err != nil || strings.Join(got.Bypass, ",") != "*.corp,[::1]:8080" {
		t.Fatalf("SetProxyBypass = %+v, %v", got, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"*.corp"`) {
		t.Errorf("settings.json: %s", data)
	}
	if n := len(rec.all()); n != 1 {
		t.Errorf("%d events, want 1", n)
	}
	if _, err := svc.SetProxyBypass(ctx, []string{"*.corp", "[::1]:8080"}); err != nil || len(rec.all()) != 1 {
		t.Errorf("unchanged list: %v, %d events", err, len(rec.all()))
	}
	if got, err := svc.SetProxyBypass(ctx, nil); err != nil || len(got.Bypass) != 0 || got.Bypass == nil {
		t.Errorf("cleared: %+v, %v", got, err)
	}
}

func TestSetProxyBypassLocked(t *testing.T) {
	t.Parallel()
	svc, rec, _ := policyService(t, `{"locked": {"network.proxyBypass": ["*.corp"]}}`, "")
	if got, err := svc.Proxy(t.Context()); err != nil || strings.Join(got.Bypass, ",") != "*.corp" {
		t.Fatalf("locked: %+v, %v", got, err)
	}
	_, err := svc.SetProxyBypass(t.Context(), []string{"other.test"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != core.CodeLocked || len(rec.all()) != 0 {
		t.Errorf("err = %v, want %s", err, core.CodeLocked)
	}
	if _, err := svc.SetProxyBypass(t.Context(), []string{"*.corp"}); err != nil {
		t.Errorf("the locked value itself: %v", err)
	}
}
