package sse

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	coresse "github.com/sorrelfox/postsonde/internal/core/realtime/sse"
)

// CodeNoResponse: Probe's window passed before the server answered (kind
// timeout). Details: windowMs.
const CodeNoResponse = "sse.no_response"

// ProbeResult is what Probe found out about an event stream.
type ProbeResult struct {
	// StatusCode, Status and ContentType are the response's; 0 and ""
	// when none arrived.
	StatusCode  int
	Status      string
	ContentType string
	// Events is the number of events received.
	Events int
	// Bytes is the number of body bytes received, as sent.
	Bytes int64
	// Ended is set when the server ended the stream within the window.
	Ended bool
	// Duration is from the start to the end of the stream or the window.
	Duration time.Duration
}

// Probe opens st once and reads its events until the server ends the
// stream or window has passed, which is how a folder run sends an SSE
// request (RUN-01, docs/runner.md): no reconnection, nothing is sent to
// the UI but the counts. A response with a status other than 2xx is not
// read; its status is in the result. ctx must be an operation's context
// (Stream.Open).
//
// It fails as Connect does before the stream (a refused connection, the
// request's timeout), with CodeNoResponse when the window passes first,
// and with the parser's limits or an unsupported Content-Encoding while
// reading; the result then has what was found before. Stopping ctx
// returns its error.
func Probe(ctx context.Context, st Stream, window time.Duration) (ProbeResult, error) {
	return ProbeInto(ctx, st, window, nil)
}

// ProbeInto is Probe that also records the response and every event in
// log (may be nil), for the request's assertions (TEST-08): a folder run
// checks them against it (docs/runner.md).
func ProbeInto(ctx context.Context, st Stream, window time.Duration, log *streamcheck.Log) (ProbeResult, error) {
	start := time.Now()
	defer func() { log.SetTime(time.Since(start)) }()
	wctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var out ProbeResult
	answered := false
	resp, err := st.Open(wctx, "", func(head *httpclient.Response, body io.Reader) error {
		answered = true
		out.StatusCode, out.Status, out.ContentType = head.StatusCode, head.Status, header(head, "Content-Type")
		log.SetResponse(responseOf(head))
		if head.StatusCode < 200 || head.StatusCode > 299 {
			return nil
		}
		r, err := decoded(header(head, "Content-Encoding"), body)
		if err != nil {
			return err
		}
		for ev, err := range coresse.Events(r, coresse.Options{}) {
			if err != nil {
				return err
			}
			addEvent(log, ev, time.Now())
			out.Events++
		}
		out.Ended = true
		return nil
	})
	out.Duration = time.Since(start)
	if resp != nil {
		out.Bytes = resp.Size
	}
	switch {
	case err == nil || ctx.Err() != nil:
		return out, ctx.Err()
	case wctx.Err() != nil && answered:
		// The window passed while the stream was open: read as far as it
		// went.
		return out, nil
	case wctx.Err() != nil:
		return out, errs.New(errs.Timeout, "The event stream did not answer within "+strconv.FormatInt(window.Milliseconds(), 10)+" ms.").
			WithCode(CodeNoResponse).
			WithHint("Check that the server is running, or read the stream for longer.").
			WithDetail("windowMs", strconv.FormatInt(window.Milliseconds(), 10))
	}
	return out, err
}
