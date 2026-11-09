package settings

import (
	"context"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestTestProxy (SOCKS-11): TestProxy marks the call as the user's action
// with a deadline, and turns the result into the DTO: the proxy without
// its password, the step and error of a failure, the hop.
func TestTestProxy(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := proxyService(t, "", nil)
	if _, err := svc.TestProxy(t.Context(), ProxyTestInputDTO{Target: "example.com:443"}); errs.KindOf(err) != errs.Unsupported {
		t.Fatalf("without a tester: %v", err)
	}

	var gotTarget string
	var gotOp netx.UserOp
	var hasDeadline bool
	result := netx.ProxyTest{
		Proxy:    netx.ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "alice"},
		Duration: 1500 * time.Microsecond,
		Hop:      &netx.ProxyHop{Scheme: "socks5h", Version: "SOCKS5", Proxy: "proxy.lan:1080", Auth: netx.AuthUserPass, Replied: true, Reply: 2, ReplyText: "not allowed by ruleset", Connect: time.Millisecond},
		Err:      errs.New(errs.Network, "not allowed").WithCode(netx.CodeSOCKSNotAllowed),
		Step:     netx.ProxyStepTarget,
	}
	WithProxyTester(func(ctx context.Context, target string) netx.ProxyTest {
		gotTarget = target
		gotOp, _ = netx.UserOpFrom(ctx)
		_, hasDeadline = ctx.Deadline()
		return result
	})(svc)
	res, err := svc.TestProxy(t.Context(), ProxyTestInputDTO{Target: "  api.example.com:443 "})
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != "api.example.com:443" || gotOp.Kind != UserOpProxyTest || gotOp.ID == "" || !hasDeadline {
		t.Errorf("tester got target %q, op %+v, deadline %v", gotTarget, gotOp, hasDeadline)
	}
	if res.OK || res.Proxy != "socks5h://alice@proxy.lan:1080" || res.Step != netx.ProxyStepTarget || res.DurationMs != 1.5 ||
		res.Error == nil || res.Error.Code != netx.CodeSOCKSNotAllowed {
		t.Errorf("result = %+v", res)
	}
	if h := res.Hop; h == nil || h.Reply == nil || *h.Reply != 2 || h.Auth != netx.AuthUserPass || h.ConnectMs != 1 {
		t.Errorf("hop = %+v", res.Hop)
	}

	result = netx.ProxyTest{Proxy: result.Proxy, Hop: &netx.ProxyHop{Version: "SOCKS5"}}
	if res, err = svc.TestProxy(t.Context(), ProxyTestInputDTO{Target: "api.example.com:443"}); err != nil || !res.OK || res.Error != nil || res.Hop.Reply != nil {
		t.Errorf("success = %+v, %v", res, err)
	}
}
