package grpc

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// WithChecks checks the assertions of grpc requests (TEST-08) with the
// checks load returns: when a call ends (CallEventDTO.Tests) and on
// demand (CheckAssertions). Without it no call is checked.
func WithChecks(load streamcheck.Loader) Option {
	return func(s *GRPCService) { s.checks = load }
}

// checkTimeout bounds loading and evaluating a call's checks.
const checkTimeout = 10 * time.Second

// endedCall is what CheckAssertions needs of a call that ended.
type endedCall struct {
	requestID string
	log       *streamcheck.Log
	mask      func(string) string
}

// runChecks loads the request's checks now and evaluates them against the
// call so far. It returns an empty list without a loader or a request.
func runChecks(ctx context.Context, load streamcheck.Loader, requestID string, log *streamcheck.Log, mask func(string) string) []streamcheck.TestResultDTO {
	if load == nil || requestID == "" {
		return []streamcheck.TestResultDTO{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	checks, err := load(ctx, requestID)
	if err != nil {
		return streamcheck.Failed(err)
	}
	return checks.Run(ctx, log.Stream(), mask)
}

// CheckAssertions evaluates the assertions of a call's request, as they
// are now, against the messages it has received (TEST-08): a running call
// (a stream checked before it ends) or one of the last calls that ended,
// whose assertions were edited since. It fails with streamcheck.not_found
// for another operation ID and streamcheck.unavailable without WithChecks.
func (s *GRPCService) CheckAssertions(ctx context.Context, in CallRefDTO) ([]streamcheck.TestResultDTO, error) {
	if s.checks == nil {
		return nil, streamcheck.Unavailable()
	}
	id := strings.TrimSpace(in.OpID)
	s.callsMu.Lock()
	c := s.calls[id]
	s.callsMu.Unlock()
	if c != nil {
		return runChecks(ctx, s.checks, c.requestID, c.log, c.masker.MaskText), nil
	}
	if e, ok := s.ended.Get(id); ok {
		return runChecks(ctx, s.checks, e.requestID, e.log, e.mask), nil
	}
	return nil, streamcheck.NotFound(id)
}

// metadataHeader is call metadata as the header target reads it; binary
// values in Base64, as the UI shows them.
func metadataHeader(h http.Header, md transport.Metadata) {
	for _, e := range md {
		v := e.Value
		if transport.IsBinaryKey(e.Key) {
			v = base64.StdEncoding.EncodeToString([]byte(e.Value))
		}
		h.Add(e.Key, v)
	}
}
