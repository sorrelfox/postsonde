package http

import (
	"context"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Repeat and interval sending (HTTP-27): one request sent again and again
// as one operation, each send's outcome a data item. See
// docs/http-client.md#repeat-and-interval-sending.

// RepeatDomain names repeat runs. Their items are RepeatResultDTO, not
// ResponseDTO, so they have a domain segment of their own
// (docs/events.md).
const RepeatDomain = Domain + ".repeat"

// CodeRepeatInvalid: Repeat was given a count or interval out of range
// (kind invalid_argument). Details: field.
const CodeRepeatInvalid = "http.repeat_invalid"

// Limits of a run.
const (
	// MaxRepeatCount is the most sends a run with a count makes.
	MaxRepeatCount = 100_000
	// MaxRepeatIntervalMs is the longest interval: a day.
	MaxRepeatIntervalMs = 24 * 60 * 60 * 1000
)

// StageNextSend is the progress message of a run between two sends.
const StageNextSend = "Waiting for the next send"

// repeatResponseEvery is how often, at most, a run's result carries the
// whole response: a run of fast back-to-back sends would otherwise push
// every body through the bridge. Runs with an interval at least this long
// carry every response.
const repeatResponseEvery = 250 * time.Millisecond

// RepeatInputDTO is what Repeat takes.
type RepeatInputDTO struct {
	// RequestID is the HTTP or GraphQL request to send, from the open
	// project.
	RequestID string `json:"requestId"`
	// AllowUnresolved sends undefined {{variables}} as written, as
	// SendInputDTO.AllowUnresolved does, on every send of the run.
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
	// Prompts are the values typed for the request's {{$prompt}}
	// references, by label (DYN-07), as SendInputDTO.Prompts: asked once
	// before the run, used by every send of it, never stored.
	Prompts map[string]string `json:"prompts,omitempty"`
	// Count is how many times the request is sent, 1 to MaxRepeatCount;
	// 0 sends it until the run is stopped, which needs an interval.
	Count int `json:"count"`
	// IntervalMs is the time from the start of one send to the start of
	// the next, 0 to MaxRepeatIntervalMs; 0 sends back to back. A send
	// that takes longer is followed by the next one at once: sends never
	// overlap.
	IntervalMs int64 `json:"intervalMs"`
}

// RepeatResultDTO is the outcome of one send of a run, one row of the
// run's results.
type RepeatResultDTO struct {
	// Index is the send's number in the run: 1, 2, 3, …
	Index int `json:"index"`
	// StartedAt is when the send started, in Unix milliseconds.
	StartedAt int64 `json:"startedAt"`
	// DurationMs is how long the send took: the response's DurationMs, or
	// until it failed.
	DurationMs int64 `json:"durationMs"`
	// StatusCode and Status are the response's ("200", "200 OK"); 0 and
	// "" when the send failed.
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	// Size is the number of body bytes received.
	Size int64 `json:"size"`
	// TestsPassed and TestsFailed count the request's assertions that
	// passed, and that failed or could not be evaluated (TEST-01).
	TestsPassed int `json:"testsPassed"`
	TestsFailed int `json:"testsFailed"`
	// Error is why the send failed (a refused connection, a timeout), its
	// message with the request's secrets masked; null when a response
	// arrived, whatever its status.
	Error *apperr.ErrorDTO `json:"error"`
	// Response is the whole response, as Send sends it, for the response
	// view. Set on the last send of a run with a count and on at most a
	// few results a second otherwise (on all of them with an interval of
	// 250 ms or more); null on the rest, whose bodies are not kept.
	Response *ResponseDTO `json:"response"`
}

// Repeat starts a run that sends request in.RequestID in.Count times (or
// until stopped) with in.IntervalMs between the starts of the sends
// (HTTP-27), and returns its operation ID at once. Each send is built and
// sent as Send does it — from the request as saved then, with the active
// environment's variables, fresh dynamic values, the auth, the cookie jar
// and the assertions — so a run sees edits and environment changes made
// while it runs. Every send's outcome arrives as one http.repeat:op:data
// item (RepeatResultDTO), a failed send included: the run goes on. The
// progress is the sends done and in.Count (0 without a count) with the
// current stage. OpsService.Cancel stops the run at once, also during a
// send (which is dropped, not listed) or the wait before the next; the
// done event is then "cancelled". A run with a count that sent them all
// is "ok"; one whose request can no longer be built (deleted, an
// undefined variable) ends with "error".
//
// A run adds one entry to the request history: its last send that was
// not cut short by stopping the run (docs/history.md).
//
// It fails at once, without sending anything, as Send does, and with
// CodeRepeatInvalid for a count or interval out of range.
func (s *HTTPService) Repeat(ctx context.Context, in RepeatInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending the request was interrupted.")
	}
	if s.ops == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	if err := in.validate(); err != nil {
		return operation.StartedDTO{}, err
	}
	o := in.buildOptions()
	plan := &responseFlow{mode: responsesPlan}
	o.responses = plan
	first, err := s.prepare(ctx, in.RequestID, o, nil)
	if plan.needsSending() {
		// The first send sends the referenced requests first (DYN-06),
		// in the run: it is built there. What the UI asks about is
		// reported now, as for Send.
		if code := codeOf(err); code == template.CodePrompt || code == template.CodeUnresolved || code == CodeSSERequest {
			return operation.StartedDTO{}, err
		}
		first, err = nil, nil
	}
	if err != nil {
		return operation.StartedDTO{}, err
	}
	r := &repeatRun{s: s, in: in, first: first, record: s.historyRecord()}
	// A few dozen results per event: some of them carry a whole response.
	started, err := operation.Start(s.ops, operation.Spec{Domain: RepeatDomain, Kind: "repeat", MaxItems: 32}, r.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The run could not be started.")
	}
	s.logger.Debug("http repeat started", "opId", started.OpID, "requestId", in.RequestID,
		"count", in.Count, "intervalMs", in.IntervalMs)
	return started, nil
}

// buildOptions is how every send of the run is built.
func (in RepeatInputDTO) buildOptions() buildOptions {
	return buildOptions{allowUnresolved: in.AllowUnresolved, prompts: in.Prompts}
}

// validate checks the count and interval of a run.
func (in RepeatInputDTO) validate() error {
	invalid := func(field, msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeRepeatInvalid).WithDetail("field", field)
	}
	switch {
	case in.Count < 0 || in.Count > MaxRepeatCount:
		return invalid("count", "The number of sends must be between 1 and "+strconv.Itoa(MaxRepeatCount)+".")
	case in.IntervalMs < 0 || in.IntervalMs > MaxRepeatIntervalMs:
		return invalid("intervalMs", "The interval must be between 0 and 24 hours.")
	case in.Count == 0 && in.IntervalMs == 0:
		return invalid("intervalMs", "Sending until stopped needs an interval.")
	}
	return nil
}

// repeatRun is one running run.
type repeatRun struct {
	s  *HTTPService
	in RepeatInputDTO
	// first is the first send, built when the run was started; nil when
	// it sends referenced requests first, built in the run then.
	first *send
	// record writes the run's history entry, nil for none. It was bound
	// when the run started, as a single send's is.
	record func(history.Entry)
}

// run is the operation: send, list the outcome, wait, again.
func (r *repeatRun) run(ctx context.Context, emit *operation.Emitter[RepeatResultDTO]) error {
	// Only the last send that was not cut short by the stop goes into the
	// history, written when the run ends.
	var last *history.Entry
	capture := func(e history.Entry) {
		if ctx.Err() != nil && last != nil {
			return
		}
		last = &e
	}
	defer func() {
		if r.record != nil && last != nil {
			r.record(*last)
		}
	}()

	count := int64(r.in.Count)
	interval := time.Duration(r.in.IntervalMs) * time.Millisecond
	var next time.Time
	var attached time.Time
	for i := 1; r.in.Count == 0 || i <= r.in.Count; i++ {
		done := int64(i - 1)
		snd := r.first
		if i > 1 {
			_ = emit.Progress(done, count, StageNextSend)
			if err := sleep(ctx, time.Until(next)); err != nil {
				return nil // stopped: the done event says "cancelled"
			}
		}
		if i > 1 || snd == nil {
			// Each send sends the requests its {{$response}} references
			// need first (DYN-06), as a single send does.
			o := r.in.buildOptions()
			o.responses = &responseFlow{mode: responsesSend, stage: func(msg string) { _ = emit.Progress(done, count, msg) }}
			var err error
			if snd, err = r.s.prepare(ctx, r.in.RequestID, o, nil); err != nil {
				return err
			}
		}
		snd.record = capture
		start := time.Now()
		next = start.Add(interval)
		ex, err := snd.exchange(ctx, func(_, _ int64, msg string) { _ = emit.Progress(done, count, msg) }, nil)
		res := RepeatResultDTO{Index: i, StartedAt: start.UnixMilli()}
		if err != nil {
			if ctx.Err() != nil {
				return nil // cut short by the stop: not listed
			}
			res.DurationMs = time.Since(start).Milliseconds()
			dto := apperr.ToDTO(err)
			dto.Message = snd.maskerOrDefault().MaskText(dto.Message)
			res.Error = &dto
		} else {
			dto := ex.dto
			res.DurationMs, res.StatusCode, res.Status, res.Size = dto.DurationMs, dto.StatusCode, dto.Status, dto.Size
			for _, t := range dto.Tests {
				if t.Outcome == "passed" {
					res.TestsPassed++
				} else {
					res.TestsFailed++
				}
			}
			now := time.Now()
			if i == r.in.Count || interval >= repeatResponseEvery || attached.IsZero() || now.Sub(attached) >= repeatResponseEvery {
				attached = now
				if snd.bodies != nil {
					dto.BodyID = snd.bodies.keep(ex.body, dto.FileName)
					snd.keepExchange(dto.BodyID, ex)
				}
				res.Response = &dto
			} else {
				// Not shown, so not kept: a run must not push the other
				// tabs' bodies out of the store.
				ex.body.abort()
			}
			snd.recordResponse(ex.started, ex.resp, dto)
		}
		if err := emit.Send(res); err != nil {
			return err
		}
	}
	return nil
}

// maskerOrDefault is the send's masker, or one that masks the
// Authorization fields only.
func (x *send) maskerOrDefault() httpclient.Masker {
	if x.masker != nil {
		return x.masker
	}
	return &httpclient.SecretMasker{}
}

// sleep waits d, or until ctx is done, which it returns the error of.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
