package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Folder runs (RUN-01, RUN-03): the runner service
// (internal/app/services/runner) sends the requests of a folder one after
// the other through a RunSession, each built and sent as Send does it. See
// docs/runner.md.

// RunOptions says how the requests of a run are built.
type RunOptions struct {
	// AllowUnresolved sends undefined {{variables}} as written, as
	// SendInputDTO.AllowUnresolved does.
	AllowUnresolved bool
	// Prompts answer the {{$prompt}} references, by label (DYN-07): asked
	// once before the run, used by every request of it.
	Prompts map[string]string
	// Vars are the run's own variables (runner.Vars), above every other
	// scope; nil for none.
	Vars template.Scope
}

func (o RunOptions) build() buildOptions {
	return buildOptions{allowUnresolved: o.AllowUnresolved, prompts: o.Prompts, runVars: o.Vars}
}

// RunSession sends the HTTP and GraphQL requests of one run, and builds
// its SSE requests. It is not bound. Its methods may be called from one
// goroutine at a time; Close ends it.
//
// History: a run adds one entry per request to the request history, not
// one per send, written by Close: the request's last send that was not cut
// short by stopping the run (the cancelled one only if none of that
// request finished), in the order the requests were first sent
// (docs/history.md).
type RunSession struct {
	s *HTTPService
	// record writes the run's history entries; nil for none. It is bound
	// when the session starts, as a single send's is.
	record func(history.Entry)

	mu    sync.Mutex
	last  map[string]history.Entry
	order []string
}

// NewRunSession starts the session of a run on s.
func NewRunSession(s *HTTPService) *RunSession {
	return &RunSession{s: s, record: s.historyRecord(), last: map[string]history.Entry{}}
}

// RunSendResult is the outcome of one send of a run.
type RunSendResult struct {
	// Response is the response, with its assertions checked (Tests), as
	// Send sends it but without a kept body (BodyID is ""); nil when the
	// request could not be built or sent.
	Response *ResponseDTO
	// Error says why there is no response: the build's error (an
	// undefined variable, a missing file) or the send's (a refused
	// connection, a timeout), its message with the request's secrets
	// masked.
	Error *apperr.ErrorDTO
	// Started is when the send started; Duration how long it took, until
	// the response's last byte or the error.
	Started  time.Time
	Duration time.Duration
}

// Send builds HTTP or GraphQL request id as Send does, with o, and sends
// it on ctx, an operation's context; progress (nil for none) gets the
// stages of the send. A request that cannot be built or sent is a result
// with its Error, not an error: the run goes on. When ctx is done the
// result is that of a send cut short, which the caller drops.
func (r *RunSession) Send(ctx context.Context, id string, o RunOptions, progress func(msg string)) RunSendResult {
	start := time.Now()
	res := RunSendResult{Started: start}
	b := o.build()
	// Inside the run's operation: the requests {{$response}} references
	// need are sent first (DYN-06).
	b.responses = &responseFlow{mode: responsesSend, stage: progress}
	snd, err := r.s.prepare(ctx, id, b, nil)
	if err != nil {
		dto := apperr.ToDTO(err)
		res.Error, res.Duration = &dto, time.Since(start)
		return res
	}
	snd.record = r.capture(ctx, id)
	res.Started = time.Now()
	ex, err := snd.exchange(ctx, func(_, _ int64, msg string) {
		if progress != nil {
			progress(msg)
		}
	}, nil)
	res.Duration = time.Since(res.Started)
	if err != nil {
		dto := apperr.ToDTO(err)
		dto.Message = snd.maskerOrDefault().MaskText(dto.Message)
		res.Error = &dto
		return res
	}
	// The run lists the results, not the bodies: none is kept.
	ex.body.abort()
	dto := ex.dto
	res.Response = &dto
	snd.recordResponse(ex.started, ex.resp, dto)
	return res
}

// Stream builds SSE request id with o, as NewStream does, for the run to
// read (sse.Probe). It fails as NewStream does.
func (r *RunSession) Stream(ctx context.Context, id string, o RunOptions) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the request was interrupted.")
	}
	return newStream(ctx, r.s, id, o.build())
}

// MissingPrompts builds HTTP or GraphQL requests ids with o, sending
// nothing, and returns one template.prompt error listing the {{$prompt}}
// labels of all of them that o.Prompts does not answer (details.prompts,
// in the order met), so the UI asks for them all once before the run, as
// it does before a send (DYN-07). Other build errors are left to the run,
// which lists them with their request. nil when nothing is missing.
func (r *RunSession) MissingPrompts(ctx context.Context, ids []string, o RunOptions) error {
	o.AllowUnresolved = true
	var prompts []template.Prompt
	index := map[string]int{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errs.Wrap(err, errs.KindOf(err), "Preparing the run was interrupted.")
		}
		b := o.build()
		// A request still to be sent first is no reason to stop here.
		b.responses = &responseFlow{mode: responsesPlan}
		_, _, _, err := r.s.build(ctx, id, b)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != template.CodePrompt {
			continue
		}
		var list []template.Prompt
		if json.Unmarshal([]byte(e.Details["prompts"]), &list) != nil {
			continue
		}
		for _, p := range list {
			i, seen := index[p.Label]
			if !seen {
				index[p.Label] = len(prompts)
				prompts = append(prompts, p)
				continue
			}
			prompts[i].Secret = prompts[i].Secret || p.Secret
			if prompts[i].Default == "" {
				prompts[i].Default = p.Default
			}
		}
	}
	if len(prompts) == 0 {
		return nil
	}
	labels := make([]string, len(prompts))
	for i, p := range prompts {
		labels[i] = fmt.Sprintf("%q", p.Label)
	}
	msg := fmt.Sprintf("The run asks for %d values before it starts: %s.", len(prompts), strings.Join(labels, ", "))
	if len(prompts) == 1 {
		msg = fmt.Sprintf("The run asks for the value %q before it starts.", prompts[0].Label)
	}
	b, _ := json.Marshal(prompts)
	return errs.New(errs.InvalidArgument, msg).
		WithCode(template.CodePrompt).
		WithHint("The values are asked for once and used by every request of the run.").
		WithDetail("prompts", string(b))
}

// capture keeps the entry of the latest send of request id that was not
// cut short by stopping the run (ctx).
func (r *RunSession) capture(ctx context.Context, id string) func(history.Entry) {
	return func(e history.Entry) {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, seen := r.last[id]
		if ctx.Err() != nil && seen {
			return
		}
		if !seen {
			r.order = append(r.order, id)
		}
		r.last[id] = e
	}
}

// Close writes the run's history entries: one per request sent.
func (r *RunSession) Close() {
	r.mu.Lock()
	entries := make([]history.Entry, 0, len(r.order))
	for _, id := range r.order {
		entries = append(entries, r.last[id])
	}
	r.last, r.order = map[string]history.Entry{}, nil
	r.mu.Unlock()
	if r.record == nil {
		return
	}
	for _, e := range entries {
		r.record(e)
	}
}
