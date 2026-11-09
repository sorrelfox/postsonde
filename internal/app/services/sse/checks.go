package sse

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	coresse "github.com/sorrelfox/postsonde/internal/core/realtime/sse"
)

// WithChecks reloads the assertions of an sse request for CheckAssertions,
// so a check on demand sees them as they are now (TEST-08). Without it,
// CheckAssertions uses those rendered at Connect (Target.Checks).
func WithChecks(load streamcheck.Loader) Option {
	return func(s *SSEService) { s.checks = load }
}

// An Option configures New.
type Option func(*SSEService)

// checkTimeout bounds loading and evaluating a stream's checks.
const checkTimeout = 10 * time.Second

// CheckInputDTO names the stream CheckAssertions checks.
type CheckInputDTO struct {
	// OpID is the stream's operation ID, from Connect.
	OpID string `json:"opId"`
}

// CheckAssertions evaluates the request's assertions against the events a
// stream has received (TEST-08): a running stream, or one of the last
// that ended. It fails with streamcheck.not_found for another operation.
func (s *SSEService) CheckAssertions(ctx context.Context, in CheckInputDTO) ([]streamcheck.TestResultDTO, error) {
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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	checks := c.checks
	if s.checks != nil && c.requestID != "" {
		loaded, err := s.checks(ctx, c.requestID)
		if err != nil {
			return streamcheck.Failed(err), nil
		}
		checks = loaded
	}
	return checks.Run(ctx, c.log.Stream(), c.mask), nil
}

// track keeps a started stream for CheckAssertions.
func (s *SSEService) track(id string, c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.id = id
	if c.finished {
		s.ended.Put(id, c)
		return
	}
	s.conns[id] = c
}

// finish moves a stream that ended to the ended ones.
func (s *SSEService) finish(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.finished = true
	if c.id != "" {
		delete(s.conns, c.id)
		s.ended.Put(c.id, c)
	}
}

// responseOf is a stream's response as the assertions without a message
// scope read it: the status and headers.
func responseOf(head *httpclient.Response) assert.Response {
	h := http.Header{}
	for _, x := range head.Headers {
		h.Add(x.Name, x.Value)
	}
	return assert.Response{Status: head.StatusCode, Header: h}
}

// addEvent records an event for the checks: its data as the body, its
// type and ID as the Event and Id headers.
func addEvent(log *streamcheck.Log, ev coresse.Event, at time.Time) {
	h := http.Header{"Event": {ev.Type}}
	if ev.ID != "" {
		h.Set("Id", ev.ID)
	}
	log.Add([]byte(ev.Data), h, at)
}
