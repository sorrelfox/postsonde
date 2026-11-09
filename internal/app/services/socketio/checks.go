package socketio

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
)

// WithChecks checks the assertions of socketio requests (TEST-08) with the
// checks load returns: when a connection closes (the closed item's tests)
// and on demand (CheckAssertions). Without it no connection is checked.
func WithChecks(load streamcheck.Loader) Option {
	return func(s *SocketIOService) { s.checks = load }
}

// checkTimeout bounds loading and evaluating a connection's checks.
const checkTimeout = 10 * time.Second

// CheckInputDTO names the connection CheckAssertions checks.
type CheckInputDTO struct {
	// OpID is the connection's operation ID, from Connect.
	OpID string `json:"opId"`
}

// CheckAssertions evaluates the assertions of a connection's request, as
// they are now, against the events it has received (TEST-08): each event
// is a message whose body is its arguments as a JSON array and whose
// Event header is its name. It takes a running connection or one of the
// last that closed; another fails with streamcheck.not_found, and
// streamcheck.unavailable without WithChecks.
func (s *SocketIOService) CheckAssertions(ctx context.Context, in CheckInputDTO) ([]streamcheck.TestResultDTO, error) {
	if s.checks == nil {
		return nil, streamcheck.Unavailable()
	}
	id := strings.TrimSpace(in.OpID)
	s.mu.Lock()
	c := s.conns[id]
	s.mu.Unlock()
	if c == nil {
		var ok bool
		if c, ok = s.ended.Get(id); !ok {
			return nil, streamcheck.NotFound(id)
		}
	}
	return c.runChecks(ctx), nil
}

// runChecks loads the request's checks now and evaluates them against the
// events received so far; empty without a loader or a request.
func (c *conn) runChecks(ctx context.Context) []streamcheck.TestResultDTO {
	if c.checks == nil || c.requestID == "" {
		return []streamcheck.TestResultDTO{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	checks, err := c.checks(ctx, c.requestID)
	if err != nil {
		return streamcheck.Failed(err)
	}
	return checks.Run(ctx, c.log.Stream(), c.masker.MaskText)
}

// eventHeader is the header of a received event: its name.
func eventHeader(name string) http.Header {
	return http.Header{"Event": {name}}
}
