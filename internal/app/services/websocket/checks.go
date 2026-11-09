package websocket

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/assert"
)

// WithChecks checks the assertions of websocket requests (TEST-08) with
// the checks load returns: when a connection ends for good (the last
// closed event's tests) and on demand (CheckAssertions). Without it no
// connection is checked.
func WithChecks(load streamcheck.Loader) Option {
	return func(s *WebSocketService) { s.checks = load }
}

// checkTimeout bounds loading and evaluating a connection's checks.
const checkTimeout = 10 * time.Second

// CheckInputDTO names the connection CheckAssertions checks.
type CheckInputDTO struct {
	ConnID string `json:"connId"`
}

// CheckAssertions evaluates the assertions of a connection's request, as
// they are now, against the messages it has received (TEST-08): an open
// connection, or one of the last that closed. It fails with
// streamcheck.not_found for another connection and
// streamcheck.unavailable without WithChecks.
func (s *WebSocketService) CheckAssertions(ctx context.Context, in CheckInputDTO) ([]streamcheck.TestResultDTO, error) {
	if s.checks == nil {
		return nil, streamcheck.Unavailable()
	}
	id := strings.TrimSpace(in.ConnID)
	c := s.get(id)
	if c == nil {
		var ok bool
		if c, ok = s.ended.Get(id); !ok {
			return nil, streamcheck.NotFound(id)
		}
	}
	return c.runChecks(ctx), nil
}

// runChecks loads the request's checks now and evaluates them against the
// messages received so far, every attempt's (WS-10). Empty without a
// loader or a request.
func (c *connection) runChecks(ctx context.Context) []streamcheck.TestResultDTO {
	if c.checks == nil || c.requestID == "" {
		return []streamcheck.TestResultDTO{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	checks, err := c.checks(ctx, c.requestID)
	if err != nil {
		return streamcheck.Failed(err)
	}
	c.mu.Lock()
	mask := c.masker.MaskText
	c.mu.Unlock()
	return checks.Run(ctx, c.log.Stream(), mask)
}

// handshakeResponse is the upgrade's answer as the assertions without a
// message scope read it: its status (101 when it opened) and headers.
func handshakeResponse(hs *HandshakeDTO, d time.Duration) assert.Response {
	r := assert.Response{Header: http.Header{}, Time: d}
	if hs == nil || hs.Response == nil {
		return r
	}
	r.Status = hs.Response.StatusCode
	for _, h := range hs.Response.Headers {
		r.Header.Add(h.Name, h.Value)
	}
	r.Body = []byte(hs.Response.Body)
	return r
}

// messageHeader is the header of a received message: its type, "text" or
// "binary".
func messageHeader(text bool) http.Header {
	if text {
		return http.Header{"Type": {"text"}}
	}
	return http.Header{"Type": {"binary"}}
}
