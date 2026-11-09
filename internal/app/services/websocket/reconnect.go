package websocket

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Defaults of the keepalive and reconnect settings (WS-09, WS-10), used
// when a ConnectInputDTO member is null.
const (
	// DefaultPongTimeout is how long a pong may take after a keepalive
	// ping.
	DefaultPongTimeout = 10 * time.Second
	// DefaultReconnectMaxAttempts is how many reconnects are tried in a
	// row before giving up.
	DefaultReconnectMaxAttempts = 5
	// DefaultReconnectDelay is the wait before the first reconnect; it
	// doubles for each further one.
	DefaultReconnectDelay = time.Second
	// DefaultReconnectMaxDelay caps the doubling.
	DefaultReconnectMaxDelay = 30 * time.Second
	// minInterval is the shortest ping interval and reconnect delay: a
	// smaller value is raised to it, so a typo cannot flood a server.
	minInterval = 100 * time.Millisecond
)

// Why auto-reconnect stopped (ReconnectDTO.Stopped).
const (
	// StoppedManual: the user closed the connection, or cancelled the
	// wait for the next attempt.
	StoppedManual = "manual"
	// StoppedCloseCode: the server closed with a code that says not to
	// come back (1000, 1002, 1003, 1007–1010, 3000–4999, an empty close).
	StoppedCloseCode = "close_code"
	// StoppedError: the attempt failed in a way another attempt would not
	// fix (TLS, a rejected upgrade, a protocol error, {{variables}} that
	// no longer render).
	StoppedError = "error"
	// StoppedMaxAttempts: the reconnects in a row reached the maximum.
	StoppedMaxAttempts = "max_attempts"
)

// reconnectPolicy is the auto-reconnect setting of one connection.
type reconnectPolicy struct {
	enabled     bool
	maxAttempts int // 0: unlimited
	delay       time.Duration
	maxDelay    time.Duration
}

func newReconnectPolicy(in ConnectInputDTO) reconnectPolicy {
	p := reconnectPolicy{
		enabled:     in.AutoReconnect != nil && *in.AutoReconnect,
		maxAttempts: DefaultReconnectMaxAttempts,
		delay:       DefaultReconnectDelay,
		maxDelay:    DefaultReconnectMaxDelay,
	}
	if in.ReconnectMaxAttempts != nil {
		p.maxAttempts = max(*in.ReconnectMaxAttempts, 0)
	}
	if in.ReconnectDelayMs != nil {
		p.delay = max(time.Duration(*in.ReconnectDelayMs)*time.Millisecond, minInterval)
	}
	if in.ReconnectMaxDelayMs != nil {
		p.maxDelay = max(time.Duration(*in.ReconnectMaxDelayMs)*time.Millisecond, minInterval)
	}
	p.maxDelay = max(p.maxDelay, p.delay)
	return p
}

// backoff is the wait before reconnect n (1 for the first): the delay,
// doubled for each reconnect before it, capped at maxDelay.
func (p reconnectPolicy) backoff(n int) time.Duration {
	d := p.delay
	for i := 1; i < n && d < p.maxDelay; i++ {
		d *= 2
	}
	return min(d, p.maxDelay)
}

// keepalive sets the keepalive options of o from in (WS-09).
func keepalive(o *wsclient.Options, in ConnectInputDTO) {
	if in.PingIntervalMs == nil || *in.PingIntervalMs <= 0 {
		return
	}
	o.PingInterval = max(time.Duration(*in.PingIntervalMs)*time.Millisecond, minInterval)
	o.PongTimeout = DefaultPongTimeout
	if in.PongTimeoutMs != nil {
		o.PongTimeout = time.Duration(*in.PongTimeoutMs) * time.Millisecond // 0 or less: never give up
	}
}

// retryable reports whether a connection that ended this way is worth
// another attempt: it dropped or timed out (network and timeout errors,
// including a missing pong and a refused dial), or the server closed with
// 1001 (going away), 1011 (internal error), 1012 (service restart), 1013
// (try again later) or 1014 (bad gateway). The second result says why not.
func retryable(info *wsclient.CloseInfo, err error) (bool, string) {
	if err != nil {
		switch errs.KindOf(err) {
		case errs.Network, errs.Timeout:
			return true, ""
		}
		return false, StoppedError
	}
	if info == nil {
		return false, StoppedError
	}
	switch info.Code {
	case wsframe.CloseGoingAway, wsframe.CloseInternalError, wsframe.CloseServiceRestart, wsframe.CloseTryAgainLater, wsframe.CloseBadGateway:
		return info.InitiatedBy == wsclient.InitiatorRemote, StoppedCloseCode
	}
	return false, StoppedCloseCode
}
