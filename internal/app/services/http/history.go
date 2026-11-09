package http

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// HistoryTarget is called when a send starts and returns the function that
// records it in the open workspace's request history (NAV-02), or nil when
// it is not recorded (no project open). Binding the workspace at the start
// keeps a send that ends after a project switch in the history of the
// project it was sent from.
type HistoryTarget func() func(history.Entry)

// WithHistory records every send, successful or not, with target
// (docs/history.md). Without it nothing is recorded.
func WithHistory(target HistoryTarget) Option {
	return func(s *HTTPService) { s.history = target }
}

// historyRecord returns the recorder of a send starting now; nil for none.
func (s *HTTPService) historyRecord() func(history.Entry) {
	if s.history == nil {
		return nil
	}
	return s.history()
}

// sendMeta is what the history says about the request of a send.
type sendMeta struct {
	kind, requestID, requestName string
	// environment names the active environment ("" for none).
	environment string
	// template is the request as the editor had it (NAV-03), not masked
	// yet; nil for a replay. Each entry masks a copy.
	template *history.Template
}

// environmentName returns the name of the active environment in chain
// (its scope's label, as Variables reads it), or "".
func environmentName(chain template.Chain) string {
	out := ""
	for _, sc := range chain {
		if name, ok := strings.CutPrefix(sc.Name(), labelEnvironment); ok {
			out = name
		}
	}
	return out
}

// entryTemplate returns a masked copy of meta's template for one entry;
// masked reports whether m hid anything.
func entryTemplate(meta sendMeta, m httpclient.Masker) (*history.Template, bool) {
	if meta.template == nil {
		return nil, false
	}
	t := meta.template.Clone()
	masked := history.MaskTemplate(&t, m)
	return &t, masked
}

// recordResponse records a send that got a response.
func (x *send) recordResponse(started time.Time, r *httpclient.Response, dto ResponseDTO) {
	if x.record == nil {
		return
	}
	x.record(responseEntry(x.meta, x.build, started, r, dto, x.responseMasker(r)))
}

// responseMasker is the history's masker of a send that got response r:
// its secrets and the credentials of every Authorization field sent, on
// every hop. Saved exchanges use it too (exchange.go).
func (x *send) responseMasker(r *httpclient.Response) *httpclient.SecretMasker {
	sent := [][]httpclient.Header{x.build.Request.Headers}
	for _, h := range r.Redirects {
		if h.Request != nil {
			sent = append(sent, h.Request.Headers)
		}
	}
	if r.Request != nil {
		sent = append(sent, r.Request.Headers)
	}
	return historyMasker(x.historySecrets(), sent...)
}

// recordFailure records a send that failed or was cancelled.
func (x *send) recordFailure(ctx context.Context, started time.Time, err error) {
	if x.record == nil {
		return
	}
	x.record(failureEntry(ctx, x.meta, x.build, started, err, historyMasker(x.historySecrets(), x.build.Request.Headers)))
}

// historySecrets are the send's secrets and the password of its URL's
// userinfo, which the client sends as Basic credentials.
func (x *send) historySecrets() []string {
	out := slices.Clone(x.secrets)
	if u, err := url.Parse(x.build.Request.URL); err == nil && u.User != nil {
		if pass, ok := u.User.Password(); ok && pass != "" {
			out = append(out, pass)
		}
	}
	return out
}

// historyMasker returns the masker of a send's history entry: the
// timeline's (the secret variables, what the auth added, the credentials
// of the Authorization fields by name) plus the credentials of every
// Authorization and Proxy-Authorization field sent, as values, so they are
// hidden where the server echoed them back too. A Basic password is added
// decoded. Digest credentials are not secret; their response hash is
// masked by name.
func historyMasker(secrets []string, headers ...[]httpclient.Header) *httpclient.SecretMasker {
	all := secrets
	for _, hs := range headers {
		for _, h := range hs {
			if !strings.EqualFold(h.Name, "Authorization") && !strings.EqualFold(h.Name, "Proxy-Authorization") {
				continue
			}
			all = append(all, credentials(h.Value)...)
		}
	}
	return httpclient.NewSecretMasker(all)
}

// credentials returns the secret parts of an Authorization value.
func credentials(value string) []string {
	value = strings.TrimSpace(value)
	scheme, rest, ok := strings.Cut(value, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case !ok || rest == "":
		return []string{value}
	case strings.EqualFold(scheme, "Digest"):
		return nil
	case strings.EqualFold(scheme, "Basic"):
		out := []string{rest}
		if raw, err := base64.StdEncoding.DecodeString(rest); err == nil {
			if _, pass, ok := strings.Cut(string(raw), ":"); ok && pass != "" {
				out = append(out, pass)
			}
		}
		return out
	default:
		return []string{rest}
	}
}

// newSummary starts an entry's summary; m masks the URL.
func newSummary(meta sendMeta, b request.HTTPBuild, started time.Time, m httpclient.Masker) history.Summary {
	return history.Summary{
		Time:        started,
		Kind:        meta.kind,
		RequestID:   meta.requestID,
		RequestName: meta.requestName,
		Environment: meta.environment,
		Method:      b.Request.Method,
		URL:         m.MaskText(withoutUserinfo(b.Request.URL)),
	}
}

// responseEntry is the history entry of a send that got response r (its
// DTO dto), with every secret m knows masked: the request as sent (its
// first hop) and a snapshot of the response.
func responseEntry(meta sendMeta, b request.HTTPBuild, started time.Time, r *httpclient.Response, dto ResponseDTO, m httpclient.Masker) history.Entry {
	e := history.Entry{Summary: newSummary(meta, b, started, m)}
	e.ID = dto.HistoryID
	e.Outcome = history.OutcomeOK
	e.StatusCode, e.Status = r.StatusCode, r.Status
	e.DurationMs = r.Duration.Milliseconds()
	e.ResponseSize = r.Size

	sent := r.Request
	if len(r.Redirects) > 0 && r.Redirects[0].Request != nil {
		sent = r.Redirects[0].Request
	}
	var masked bool
	if sent != nil {
		e.URL = m.MaskText(sent.URL)
		e.Masked = e.Masked || e.URL != sent.URL
		e.Request = history.RequestSnapshot{Method: sent.Method, URL: e.URL, Proto: sent.Proto}
		e.Request.Headers, masked = history.Headers(sent.Headers, m, false)
		e.Masked = e.Masked || masked
		e.Request.Body, masked = history.Snapshot(sent.Body, sent.BodySize, m)
		e.Masked = e.Masked || masked
		e.RequestSize = sent.BodySize
	} else {
		e.Request = builtRequest(&e, b, m)
	}

	final := m.MaskText(r.URL)
	e.Masked = e.Masked || final != r.URL
	resp := &history.ResponseSnapshot{
		Proto:       r.Proto,
		StatusCode:  r.StatusCode,
		Status:      r.Status,
		FinalURL:    final,
		Redirects:   dto.Redirects,
		ContentType: dto.ContentType,
		Truncated:   r.BodyTruncated,
		Timings:     historyTimings(r.Timing),
		RemoteAddr:  r.Conn.RemoteAddr,
		LocalAddr:   r.Conn.LocalAddr,
	}
	resp.Headers, masked = history.Headers(r.Headers, m, false)
	e.Masked = e.Masked || masked
	resp.Body, masked = history.Snapshot(r.Body, r.Size, m)
	e.Masked = e.Masked || masked
	e.Response = resp
	e.Template, masked = entryTemplate(meta, m)
	e.Masked = e.Masked || masked
	return e
}

// historyTimings converts the final hop's timing to the history's
// milliseconds (to the microsecond), -1 for a phase not observed.
func historyTimings(t httpclient.Timing) *history.Timings {
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	phase := func(d time.Duration, p httpclient.Phases) float64 {
		if !t.Observed.Has(p) {
			return -1
		}
		return ms(d)
	}
	return &history.Timings{
		DNS:     phase(t.DNS, httpclient.PhaseDNS),
		Connect: phase(t.Connect, httpclient.PhaseConnect),
		TLS:     phase(t.TLS, httpclient.PhaseTLS),
		Send:    phase(t.RequestWrite, httpclient.PhaseRequestWrite),
		Wait:    phase(t.Wait, httpclient.PhaseWait),
		Receive: phase(t.Download, httpclient.PhaseDownload),
		Total:   ms(t.Total),
	}
}

// failureEntry is the history entry of a send that failed with err: the
// request as it was built (nothing of it may have been written), masked,
// and the error.
func failureEntry(ctx context.Context, meta sendMeta, b request.HTTPBuild, started time.Time, err error, m httpclient.Masker) history.Entry {
	e := history.Entry{Summary: newSummary(meta, b, started, m)}
	e.Masked = e.URL != withoutUserinfo(b.Request.URL)
	e.Outcome = history.OutcomeError
	if errs.KindOf(err) == errs.Cancelled || errors.Is(ctx.Err(), context.Canceled) {
		e.Outcome = history.OutcomeCancelled
	}
	msg := err.Error()
	var ee *errs.Error
	if errors.As(err, &ee) {
		e.ErrorCode = ee.Code
		if ee.Message != "" {
			msg = ee.Message
		}
	}
	e.Error = m.MaskText(msg)
	e.Masked = e.Masked || e.Error != msg
	e.Request = builtRequest(&e, b, m)
	var masked bool
	e.Template, masked = entryTemplate(meta, m)
	e.Masked = e.Masked || masked
	return e
}

// builtRequest is the snapshot of the request as built, for a send whose
// request was not recorded as sent: the enabled header rows and a raw
// body (a multipart or file body is not read again; only its absence of
// text shows).
func builtRequest(e *history.Entry, b request.HTTPBuild, m httpclient.Masker) history.RequestSnapshot {
	out := history.RequestSnapshot{Method: b.Request.Method, URL: e.URL}
	var masked bool
	out.Headers, masked = history.Headers(b.Request.Headers, m, true)
	e.Masked = e.Masked || masked
	if raw, ok := b.Request.Body.(httpclient.RawBody); ok {
		out.Body, masked = history.Snapshot(raw.Data, int64(len(raw.Data)), m)
		e.Masked = e.Masked || masked
		e.RequestSize = int64(len(raw.Data))
	}
	return out
}

// withoutUserinfo drops the user and password of a URL (they go in an
// Authorization header, and the sent URL never has them).
func withoutUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
