package http

import (
	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// SendPrepared sends req, a request that does not come from the
// collection (a webhook request replayed, MOCK-08), through the pipeline
// of Send: an http "send" operation of the shared registry, so its dial is
// a user operation (PRV-05), on the service's client and dialer, with the
// stages as progress and the response as one http:op:data item
// (ResponseDTO), then http:op:done. req is sent as it is: no variables,
// no auth, no cookie jar. It is recorded in the request history (NAV-02). It gets the default timeout (network
// defaultTimeoutMs) when it has none, the trusted CA bundles and the TLS
// verification setting; it does not follow redirects unless req.Settings
// says so.
//
// It is a function, not a method, so it is not bound to the UI: the
// servers service calls it (docs/servers.md).
func SendPrepared(s *HTTPService, req httpclient.Request) (operation.StartedDTO, error) {
	if s == nil || s.ops == nil || s.dialer == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	b := request.HTTPBuild{Request: req}
	if b.Request.Settings.Timeout <= 0 && s.defaultTimeout != nil {
		if d := s.defaultTimeout(); d > 0 {
			b.Request.Settings.Timeout = d
			b.TimeoutSource = request.TimeoutFromSettings
		}
	}
	if s.defaultInsecure() {
		b.InsecureSkipVerify, b.TLSSource = true, request.TLSFromSettings
	}
	// The same client as a send: the trusted CA bundles apply too.
	client, err := s.client(b.InsecureSkipVerify, b.Request.URL)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	snd := &send{client: client, build: b, bodies: s.bodies, maxBody: s.maxBodyBytes(), record: s.historyRecord(), meta: sendMeta{kind: history.KindReplay}}
	started, err := operation.Start(s.ops, operation.Spec{Domain: Domain, Kind: "send", MaxItems: 1}, snd.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The request could not be started.")
	}
	s.logger.Debug("http request started", "opId", started.OpID, "method", req.Method, "source", "prepared")
	return started, nil
}

// BodyView returns a ResponseDTO with only its body fields set, as the
// response view shows a body (HTTP-31): language, charset, decoded text
// or hex dump, pretty-printed JSON or XML. contentType is the body's
// Content-Type, truncated says raw is only the first size bytes. The
// servers service shows received request bodies with it.
func BodyView(contentType string, raw []byte, size int64, truncated bool) ResponseDTO {
	out := ResponseDTO{ContentType: contentType, BodyTruncated: truncated, Size: size, Tests: []TestResultDTO{}, Extractions: []ExtractionResultDTO{}}
	setBody(&out, raw)
	return out
}
