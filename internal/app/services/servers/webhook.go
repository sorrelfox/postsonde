package servers

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
)

// The webhook receiver's operations (MOCK-08): the requests it keeps whole,
// one of them in full, and replaying one to a URL.

// Error codes of the webhook operations.
const (
	// CodeNotWebhook: a webhook operation on a server of another type.
	CodeNotWebhook = "servers.not_webhook"
	// CodeInvalidReplayURL: the replay target is not an absolute http or
	// https URL.
	CodeInvalidReplayURL = "servers.invalid_replay_url"
	// CodeCaptureTruncated: the request's body was larger than the
	// receiver keeps, so it cannot be replayed unchanged.
	CodeCaptureTruncated = "servers.capture_truncated"
	// CodeReplayUnavailable: the service was built without a way to send.
	CodeReplayUnavailable = "servers.replay_unavailable"
)

// Replayer sends a request built from a captured one through the app's
// HTTP send pipeline, as an operation, and returns its ID: the app passes
// httpservice.SendPrepared (WithReplayer).
type Replayer func(req httpclient.Request) (operation.StartedDTO, error)

// Option configures a ServersService.
type Option func(*ServersService)

// WithReplayer lets Replay send captured requests; without it Replay fails
// with servers.replay_unavailable.
func WithReplayer(r Replayer) Option {
	return func(s *ServersService) { s.replay = r }
}

// hopHeaders are not replayed: they describe the connection the request
// came on, and the client sets its own (Content-Length from the body).
var hopHeaders = map[string]bool{
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Connection":        true,
	"Keep-Alive":        true,
	"Proxy-Connection":  true,
	"Te":                true,
	"Trailer":           true,
	"Upgrade":           true,
}

// Captures returns the requests the webhook receiver id keeps, oldest
// first (at most 200, 64 MiB). A server of another type is
// servers.not_webhook.
func (s *ServersService) Captures(ctx context.Context, id string) ([]CaptureSummaryDTO, error) {
	if err := interrupted(ctx, "Loading the requests"); err != nil {
		return nil, err
	}
	if _, err := s.webhookServer(id); err != nil {
		return nil, err
	}
	return s.capturesOf(id), nil
}

// Capture returns one kept request in full: method, target, path and
// query, headers, and the body as the response view shows one. A request
// no longer kept is servers.capture_not_found.
func (s *ServersService) Capture(ctx context.Context, in CaptureInputDTO) (CaptureDTO, error) {
	if err := interrupted(ctx, "Loading the request"); err != nil {
		return CaptureDTO{}, err
	}
	if _, err := s.webhookServer(in.ServerID); err != nil {
		return CaptureDTO{}, err
	}
	c, err := s.manager.Capture(in.ServerID, uint64(in.Seq))
	if err != nil {
		return CaptureDTO{}, err
	}
	return toCaptureDTO(in.ServerID, c), nil
}

// Replay sends a kept request to in.URL through the HTTP send pipeline
// and returns the send's operation ID at once: its progress, response
// (a http ResponseDTO) and end arrive as http:op:* events, like a send's,
// and OpsService.Cancel cancels it. The method, headers and body are sent
// unchanged; Host comes from the URL, Content-Length from the body, and
// the connection's own headers (Connection, Transfer-Encoding, …) are
// left out. Redirects are not followed. A URL that is not absolute http
// or https is servers.invalid_replay_url; a request whose body was cut is
// servers.capture_truncated.
func (s *ServersService) Replay(ctx context.Context, in ReplayInputDTO) (operation.StartedDTO, error) {
	if err := interrupted(ctx, "Replaying the request"); err != nil {
		return operation.StartedDTO{}, err
	}
	if _, err := s.webhookServer(in.ServerID); err != nil {
		return operation.StartedDTO{}, err
	}
	c, err := s.manager.Capture(in.ServerID, uint64(in.Seq))
	if err != nil {
		return operation.StartedDTO{}, err
	}
	req, err := replayRequest(c, in.URL)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	if s.replay == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Replaying requests is not available.").WithCode(CodeReplayUnavailable)
	}
	started, err := s.replay(req)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	s.logger.Debug("webhook request replayed", "id", in.ServerID, "seq", in.Seq, "opId", started.OpID)
	return started, nil
}

// replayRequest builds the request that replays c to target.
func replayRequest(c core.Capture, target string) (httpclient.Request, error) {
	target = strings.TrimSpace(target)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return httpclient.Request{}, errs.New(errs.InvalidArgument, "Enter the URL to send the request to, starting with http:// or https://.").
			WithCode(CodeInvalidReplayURL).WithDetail("url", target)
	}
	if c.Truncated {
		return httpclient.Request{}, errs.New(errs.InvalidArgument, "This request's body was larger than the receiver keeps, so it cannot be replayed unchanged.").
			WithCode(CodeCaptureTruncated).
			WithDetail("size", formatInt(c.Size))
	}
	req := httpclient.Request{Method: c.Method, URL: target, Headers: make([]httpclient.Header, 0, len(c.Headers))}
	hadLength := false
	for _, h := range c.Headers {
		name := http.CanonicalHeaderKey(h.Name)
		if name == "Content-Length" {
			hadLength = true
		}
		if hopHeaders[name] {
			continue
		}
		req.Headers = append(req.Headers, httpclient.Header{Name: h.Name, Value: h.Value, Enabled: true})
	}
	// A body, or an empty one the sender declared (Content-Length: 0).
	if len(c.Body) > 0 || hadLength {
		req.Body = httpclient.RawBody{Data: c.Body}
	}
	return req, nil
}

// webhookServer returns the server id when it is a webhook receiver.
func (s *ServersService) webhookServer(id string) (core.Server, error) {
	srv, err := s.manager.Get(id)
	if err != nil {
		return core.Server{}, err
	}
	if srv.Spec.Type != webhook.TypeName {
		return core.Server{}, errs.New(errs.InvalidArgument, "This server is not a webhook receiver.").
			WithCode(CodeNotWebhook).WithDetail("id", id)
	}
	return srv, nil
}

// capturesOf lists the kept requests of server id, [] when none.
func (s *ServersService) capturesOf(id string) []CaptureSummaryDTO {
	list, err := s.manager.Captures(id)
	if err != nil {
		return []CaptureSummaryDTO{}
	}
	return toCaptureSummaryDTOs(list)
}

func (s *ServersService) publishCaptures(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventCaptures, CapturesEventDTO{ServerID: id, Captures: s.capturesOf(id)})
}
