package http

import (
	"context"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeNotSSE: NewStream was given a request that is not an SSE request
// (kind invalid_argument). Details: requestId.
const CodeNotSSE = "http.not_sse"

// EventStreamType is the media type of a Server-Sent Events stream, sent
// as Accept by an SSE request without an Accept row of its own.
const EventStreamType = "text/event-stream"

// LastEventIDHeader is the header a reconnecting stream sends its last
// event ID in (SSE-03).
const LastEventIDHeader = "Last-Event-ID"

// Stream is an SSE request (SSE-01) built from its file the way Send
// builds an HTTP request: the pending edits, the active environment's
// {{variables}}, the resolved auth, the folders' headers and the request's
// settings. Open connects and hands the response body to the caller as it
// arrives. It is not bound; the SSE service gets it through NewStream.
type Stream struct {
	// Method and URL are the request line as sent, with the URL's secrets
	// masked, for the event log.
	Method, URL string
	// Reconnect is the request's reconnection policy (SSE-03), from its
	// settings; nil when it has none.
	Reconnect *projectfile.SSEReconnect
	// Checks are the request's assertions (TEST-08), rendered with the
	// variables it was built with, for its events.
	Checks streamcheck.Checks
	// Mask hides the request's secrets in the results of Checks.
	Mask func(string) string

	s      *HTTPService
	build  request.HTTPBuild
	masker *httpclient.SecretMasker
}

// NewStream loads the SSE request id and builds it. It fails as Send does
// for an unknown ID, an undefined {{variable}} (template.unresolved, all
// fields together, unless allowUnresolved sends them as written), a
// {{$prompt}} (template.prompt: a stream never asks, DYN-07) and the
// other errors of request.BuildHTTP, and with CodeNotSSE for another kind
// of request. Without an enabled Accept row, "Accept: text/event-stream"
// is added.
func NewStream(ctx context.Context, s *HTTPService, id string, allowUnresolved bool) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the request was interrupted.")
	}
	if s == nil {
		return nil, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	return newStream(ctx, s, id, buildOptions{allowUnresolved: allowUnresolved})
}

// newStream is NewStream with the build options o.
func newStream(ctx context.Context, s *HTTPService, id string, o buildOptions) (*Stream, error) {
	src, x, b, err := s.build(ctx, id, o)
	if err != nil {
		return nil, err
	}
	if !src.SSE {
		return nil, errs.New(errs.InvalidArgument, "Only SSE requests are read as event streams.").
			WithCode(CodeNotSSE).
			WithDetail("requestId", id)
	}
	if !hasHeader(b.Request.Headers, "Accept") {
		b.Request.Headers = append(b.Request.Headers, httpclient.Header{Name: "Accept", Value: EventStreamType, Enabled: true})
	}
	masker := httpclient.NewSecretMasker(secretsOf(x, b))
	return &Stream{
		Method:    b.Request.Method,
		URL:       masker.MaskText(b.Request.URL),
		Reconnect: src.Request.Settings.Reconnect,
		Checks:    streamcheck.Prepare(x.resolver, src.Request.Assertions, schemaLoader(s.filePolicy(src))),
		Mask:      masker.MaskText,
		s:         s,
		build:     b,
		masker:    masker,
	}, nil
}

// hasHeader reports whether headers has an enabled row called name.
func hasHeader(headers []httpclient.Header, name string) bool {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(strings.TrimSpace(h.Name), name) {
			return true
		}
	}
	return false
}

// Open sends the request with the project's cookie jar and calls fn with
// the response as soon as its headers are in (httpclient.SendStream). ctx
// must be an operation's context: the app's dialer refuses connections
// without one (PRV-05). A timeout is reported as Send reports it, with
// where the timeout was set.
//
// A lastEventID other than "" is sent as the Last-Event-ID header
// (SSE-03), replacing the request's own rows of that name; with "" the
// request is sent as built. Open may be called again to reconnect: the
// request is sent as it was built, with the cookies the jar has then. An
// oauth2 auth gets its token first, the session's or a new one from the
// token endpoint (authorizeOAuth2).
func (x *Stream) Open(ctx context.Context, lastEventID string, fn httpclient.StreamFunc) (*httpclient.Response, error) {
	client, err := x.s.client(x.build.InsecureSkipVerify, x.build.Request.URL, x.build.Proxy)
	if err != nil {
		return nil, err
	}
	if x.s.jars != nil {
		if jar, err := x.s.jars.Jar(); err != nil {
			x.s.logger.Warn("sse request sent without cookies", "err", err)
		} else if jar != nil {
			client = client.WithCookieJar(jar)
		}
	}
	// An oauth2 auth's token (AUTH-08): stored, or got now; on every
	// connection, so an expired one is replaced.
	b := x.build
	if _, err := x.s.authorizeOAuth2(ctx, &b); err != nil {
		return nil, err
	}
	req := b.Request
	if lastEventID != "" {
		headers := make([]httpclient.Header, 0, len(req.Headers)+1)
		for _, h := range req.Headers {
			if !strings.EqualFold(strings.TrimSpace(h.Name), LastEventIDHeader) {
				headers = append(headers, h)
			}
		}
		req.Headers = append(headers, httpclient.Header{Name: LastEventIDHeader, Value: lastEventID, Enabled: true})
	}
	resp, err := client.SendStream(ctx, req, fn)
	if err != nil {
		// Masked like a send's error: it can repeat the URL.
		return resp, errs.Redact(timeoutError(err, x.build), x.masker.MaskText)
	}
	return resp, nil
}
