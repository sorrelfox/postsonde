// Package sse is the bound service that connects to Server-Sent Events
// streams (SSE-01, SSE-02): Connect builds an sse request the way an HTTP
// send is built (pending edits, {{variables}}, auth, folder headers,
// settings, cookies; internal/app/services/http), sends it through
// internal/core/httpclient as an operation and parses the response body
// with internal/core/realtime/sse as it arrives. Each event, comment and
// retry field is sent to the UI as an item of the operation (sse:op:data),
// so the log shows them live; OpsService.Cancel stops the stream. With
// the request's reconnection policy on, a stream that ends or drops is
// opened again with Last-Event-ID (SSE-03). See docs/sse.md.
package sse

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	nethttp "net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	coresse "github.com/sorrelfox/postsonde/internal/core/realtime/sse"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Domain names the SSE service and its operations.
const Domain = "sse"

// Error codes of the service.
const (
	// CodeUnavailable: the service was built without what connecting
	// needs (kind unsupported).
	CodeUnavailable = "sse.unavailable"
	// CodeBadStatus: the server answered with a status other than 2xx
	// (kind protocol). Details: status. The body, as far as read, is in a
	// notice item before the end.
	CodeBadStatus = "sse.bad_status"
	// CodeUnsupportedEncoding: the response has a Content-Encoding other
	// than gzip, deflate or identity (kind unsupported). Details:
	// encoding.
	CodeUnsupportedEncoding = "sse.unsupported_encoding"
	// CodeReconnectFailed: every reconnection attempt the policy allows
	// failed in a row (SSE-03); the kind is the last attempt's, protocol
	// when it connected but got nothing. Details: attempts.
	CodeReconnectFailed = "sse.reconnect_failed"
)

const (
	// maxItems is the largest number of items in one sse:op:data event.
	maxItems = 200
	// maxData is how much of an event's data an item carries.
	maxData = 1 << 20
	// maxErrorBody is how much of a non-2xx response body is shown.
	maxErrorBody = 64 << 10
	// readSize is the size of one read from the response body.
	readSize = 32 << 10
)

// Stream is an sse request, built and ready to connect
// (http.Stream).
type Stream interface {
	// Open sends the request and calls fn with the response once its
	// headers are in (httpclient.SendStream). A lastEventID other than ""
	// is sent as the Last-Event-ID header (SSE-03). It may be called again
	// to reconnect.
	Open(ctx context.Context, lastEventID string, fn httpclient.StreamFunc) (*httpclient.Response, error)
}

// Target is a built sse request: the stream, the method and URL it is sent
// with (the URL's secrets masked) and its reconnection policy.
type Target struct {
	Stream      Stream
	Method, URL string
	Reconnect   Reconnect
	// Checks are the request's assertions (TEST-08), checked against the
	// events when the stream ends; Mask hides the request's secrets in
	// their results (may be nil).
	Checks streamcheck.Checks
	Mask   func(string) string
}

// Opener builds the sse request id (http.NewStream). It fails for a
// request that cannot be built, and with template.unresolved for undefined
// {{variables}} unless allowUnresolved.
type Opener func(ctx context.Context, id string, allowUnresolved bool) (Target, error)

// Defaults of the reconnection policy (SSE-03).
const (
	// DefaultReconnectAttempts is how many reconnection attempts in a row
	// may fail before the stream gives up.
	DefaultReconnectAttempts = 5
	// DefaultReconnectDelay is the reconnection time until the server
	// sets one with a retry field.
	DefaultReconnectDelay = 3 * time.Second
	// MaxReconnectDelay caps the backoff of failed attempts; a larger
	// retry from the server is kept as it is.
	MaxReconnectDelay = time.Minute
)

// Reconnect is a stream's reconnection policy (SSE-03,
// docs/sse.md#reconnecting).
type Reconnect struct {
	// Enabled reconnects when the stream ends or its connection drops.
	Enabled bool
	// MaxAttempts is how many attempts in a row may fail (connect
	// without delivering a byte of the stream) before giving up; 0 means
	// no limit.
	MaxAttempts int
	// Delay is the reconnection time until the server sets one with a
	// retry field.
	Delay time.Duration
}

// ReconnectFrom is the policy of an sse request's settings: missing
// members take their defaults, negative numbers too.
func ReconnectFrom(r *projectfile.SSEReconnect) Reconnect {
	out := Reconnect{MaxAttempts: DefaultReconnectAttempts, Delay: DefaultReconnectDelay}
	if r == nil {
		return out
	}
	out.Enabled = r.Enabled != nil && *r.Enabled
	if r.MaxAttempts != nil && *r.MaxAttempts >= 0 {
		out.MaxAttempts = *r.MaxAttempts
	}
	if r.DelayMs != nil && *r.DelayMs >= 0 && *r.DelayMs <= int64(math.MaxInt64/time.Millisecond) {
		out.Delay = time.Duration(*r.DelayMs) * time.Millisecond
	}
	return out
}

// SSEService connects to event streams for the UI.
type SSEService struct {
	open   Opener
	ops    *operation.Registry
	logger *slog.Logger
	// interval is the batching interval; 0 is the default.
	interval time.Duration
	// after waits before a reconnect (time.After); tests replace it.
	after func(time.Duration) <-chan time.Time

	// checks reloads a request's assertions (WithChecks); conns are the
	// running streams and ended the last that ended, for CheckAssertions.
	checks streamcheck.Loader
	mu     sync.Mutex
	conns  map[string]*conn
	ended  streamcheck.Recent[*conn]
}

// New returns an SSEService that builds requests with open and runs the
// streams as operations in ops. A nil open or ops makes Connect fail; a
// nil logger means slog.Default().
func New(open Opener, ops *operation.Registry, logger *slog.Logger, opts ...Option) *SSEService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SSEService{open: open, ops: ops, logger: logger.With("service", Domain), after: time.After, conns: map[string]*conn{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *SSEService) ServiceName() string { return Domain }

// Connect builds the sse request in.RequestID and starts reading its event
// stream, returning the operation's ID at once. The stream arrives as
// sse:op:data items (StreamItemDTO): open with the status and headers,
// then event, comment, retry and notice items as the server sends them,
// then end. sse:op:done follows: "ok" when the server ended the stream,
// "cancelled" after OpsService.Cancel (Stop, or the tab closed), "error"
// with the error otherwise (a connection error, a timeout before the
// headers, sse.bad_status, sse.* parser limits, sse.reconnect_failed).
//
// With the request's reconnection policy on (SSE-03), a stream that ends
// or drops is not over: a reconnect item says when the next attempt is,
// and after the wait the request is sent again with Last-Event-ID, its
// items following in the same operation. A non-2xx status, 204 No
// Content, a response that is not text/event-stream and a parser limit
// end it for good; Stop ends it at any time, also while waiting.
// in.LastEventID resumes a stream by hand: the first connection sends it.
//
// It fails at once, connecting to nothing, when the request cannot be
// built: as HTTPService.Send does, with undefined {{variables}} reported
// together as template.unresolved unless in.AllowUnresolved (ENV-11).
func (s *SSEService) Connect(ctx context.Context, in ConnectInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Connecting was interrupted.")
	}
	if s.open == nil || s.ops == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Connecting to event streams is not available.").WithCode(CodeUnavailable)
	}
	t, err := s.open(ctx, in.RequestID, in.AllowUnresolved)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	after := s.after
	if after == nil {
		after = time.After
	}
	start := time.Now()
	c := &conn{
		stream: t.Stream, method: t.Method, url: t.URL, policy: t.Reconnect,
		start: start, logger: s.logger, after: after, lastID: in.LastEventID,
		requestID: in.RequestID, checks: t.Checks, mask: t.Mask, log: streamcheck.NewLog(start),
		onEnd: s.finish,
	}
	spec := operation.Spec{Domain: Domain, Kind: "connect", Interval: s.interval, MaxItems: maxItems}
	started, err := operation.Start(s.ops, spec, c.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The stream could not be started.")
	}
	s.track(started.OpID, c)
	s.logger.Debug("sse stream started", "opId", started.OpID, "requestId", in.RequestID, "method", t.Method,
		"reconnect", t.Reconnect.Enabled, "resume", in.LastEventID != "")
	return started, nil
}

// conn is one running stream, over as many connections as reconnecting
// takes.
type conn struct {
	stream      Stream
	method, url string
	policy      Reconnect
	start       time.Time
	logger      *slog.Logger
	after       func(time.Duration) <-chan time.Time

	// opened is set once a connection got a response.
	opened bool
	// events, bytes and reconnects count over all connections.
	events     int
	bytes      int64
	reconnects int
	discarded  bool
	// lastID is the last event ID, sent as Last-Event-ID on the next
	// connection; retry the reconnection time the server set, if any.
	lastID string
	retry  *time.Duration
	parser *coresse.Parser

	// Of the current connection: whether it got a response, the body
	// bytes it delivered, and why the stream must not reconnect after it
	// ("" when it may).
	connOpened bool
	connBytes  int64
	final      string

	// The request's assertions (TEST-08): checked against log, the
	// events, when the stream ends. id, finished and onEnd are the
	// service's (track, finish).
	requestID string
	checks    streamcheck.Checks
	mask      func(string) string
	log       *streamcheck.Log
	id        string
	finished  bool
	onEnd     func(*conn)
}

// Reasons a stream does not reconnect after a response, shown in a notice.
const (
	finalNoContent = "The server answered 204 No Content, which tells clients not to reconnect."
	finalWrongType = "Not reconnecting: the response is not a text/event-stream."
	finalError     = "error"
)

// run is the operation: connect, read the stream and send its items;
// reconnect as the policy says when it ends (SSE-03); then the end item.
// Its error is the stream's.
func (c *conn) run(ctx context.Context, emit *operation.Emitter[StreamItemDTO]) error {
	if c.onEnd != nil {
		defer c.onEnd(c)
	}
	attempt := 0 // reconnection attempts in a row that failed
	var err error
	for {
		c.connOpened, c.connBytes, c.final = false, 0, ""
		_, err = c.stream.Open(ctx, c.lastID, func(head *httpclient.Response, body io.Reader) error {
			c.opened, c.connOpened = true, true
			return c.read(head, body, emit)
		})
		if !c.opened {
			// The first connection got no response: the error is all.
			return err
		}
		if c.parser != nil {
			c.lastID = c.parser.LastEventID()
			if d, ok := c.parser.Retry(); ok {
				c.retry = &d
			}
		}
		if ctx.Err() != nil || !c.policy.Enabled || c.final != "" {
			if c.final != "" && c.final != finalError && c.policy.Enabled {
				_ = emit.Send(StreamItemDTO{Type: ItemNotice, At: formatTime(time.Now()), Text: c.final})
			}
			break
		}
		if c.connBytes > 0 {
			attempt = 0
		}
		attempt++
		if c.policy.MaxAttempts > 0 && attempt > c.policy.MaxAttempts {
			err = c.gaveUp(err, attempt-1)
			break
		}
		delay := c.delay(attempt)
		it := StreamItemDTO{
			Type:        ItemReconnect,
			At:          formatTime(time.Now()),
			ID:          c.lastID,
			Attempt:     attempt,
			MaxAttempts: c.policy.MaxAttempts,
			DelayMs:     delay.Milliseconds(),
			Reason:      EndClosed,
			Text:        "The server ended the stream.",
		}
		if !c.connOpened {
			it.Text = "The connection failed."
		}
		if err != nil {
			it.Reason, it.Text = EndError, errText(err)
		}
		_ = emit.Send(it)
		c.logger.Debug("sse reconnecting", "attempt", attempt, "delay", delay, "err", err)
		select {
		case <-ctx.Done():
		case <-c.after(delay):
		}
		if ctx.Err() != nil {
			// Stop wins, also while waiting to reconnect.
			err = errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "The stream was stopped.")
			break
		}
		c.reconnects++
	}
	end := StreamItemDTO{
		Type:       ItemEnd,
		At:         formatTime(time.Now()),
		Reason:     EndClosed,
		ID:         c.lastID,
		Events:     c.events,
		Bytes:      c.bytes,
		Discarded:  c.discarded,
		Reconnects: c.reconnects,
		ElapsedMs:  time.Since(c.start).Milliseconds(),
	}
	if c.retry != nil {
		end.RetryMs = retryMs(*c.retry)
	}
	if c.log != nil {
		c.log.SetTime(time.Since(c.start))
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
		end.Tests = c.checks.Run(cctx, c.log.Stream(), c.mask)
		cancel()
	}
	switch {
	case ctx.Err() != nil:
		end.Reason = EndStopped
	case err != nil:
		end.Reason = EndError
	}
	_ = emit.Send(end)
	return err
}

// delay is how long to wait before reconnection attempt n (from 1): the
// server's retry, else the policy's delay, doubled for every attempt in a
// row that failed before, up to MaxReconnectDelay (never below the base).
func (c *conn) delay(n int) time.Duration {
	base := c.policy.Delay
	if c.retry != nil {
		base = *c.retry
	}
	d := base
	for i := 1; i < n && d < MaxReconnectDelay; i++ {
		d *= 2
	}
	if d > MaxReconnectDelay && n > 1 {
		d = max(MaxReconnectDelay, base)
	}
	return d
}

// gaveUp is the error of a stream whose reconnection attempts all failed:
// last is the last attempt's error, nil when it ended without a byte.
func (c *conn) gaveUp(last error, attempts int) error {
	msg := fmt.Sprintf("Reconnecting failed %s in a row.", plural(attempts, "time"))
	var e *errs.Error
	if last != nil {
		e = errs.Wrap(last, errs.KindOf(last), msg+" "+errText(last))
	} else {
		e = errs.New(errs.Protocol, msg+" The server ended the stream without sending anything.")
	}
	return e.WithCode(CodeReconnectFailed).
		WithHint("Check that the server is up; Connect starts again.").
		WithDetail("attempts", fmt.Sprint(attempts))
}

func errText(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Message
	}
	return err.Error()
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// read sends the open item and then the stream's items until the body
// ends or fails.
func (c *conn) read(head *httpclient.Response, body io.Reader, emit *operation.Emitter[StreamItemDTO]) error {
	now := time.Now()
	_ = emit.Send(StreamItemDTO{
		Type:       ItemOpen,
		At:         formatTime(now),
		Method:     c.method,
		URL:        c.url,
		Status:     head.StatusCode,
		StatusText: head.Status,
		Proto:      head.Proto,
		Headers:    toHeaderDTOs(head.Headers),
	})
	c.log.SetResponse(responseOf(head))
	if head.StatusCode < 200 || head.StatusCode > 299 {
		// Not a stream: never reconnected (SSE-03).
		c.final = finalError
		return c.badStatus(head, body, emit)
	}
	if head.StatusCode == nethttp.StatusNoContent {
		// The standard's way for a server to stop reconnecting.
		c.final = finalNoContent
		return nil
	}
	if note := contentTypeNote(header(head, "Content-Type")); note != "" {
		c.final = finalWrongType
		_ = emit.Send(StreamItemDTO{Type: ItemNotice, At: formatTime(now), Text: note})
	}
	r, err := decoded(header(head, "Content-Encoding"), body)
	if err != nil {
		c.final = finalError
		return err
	}

	var pending []positioned
	c.parser = coresse.NewParser(coresse.Options{
		LastEventID: c.lastID,
		OnComment: func(cm coresse.Comment) {
			pending = append(pending, positioned{cm.Pos.Offset, StreamItemDTO{Type: ItemComment, Text: cm.Text, Line: cm.Pos.Line}})
		},
		OnField: func(f coresse.Field) {
			if it, ok := fieldItem(f); ok {
				pending = append(pending, positioned{f.Pos.Offset, it})
			}
		},
	})
	buf := make([]byte, readSize)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			c.bytes += int64(n)
			c.connBytes += int64(n)
			pending = pending[:0]
			events, ferr := c.parser.Feed(buf[:n])
			at := formatTime(time.Now())
			for _, ev := range events {
				addEvent(c.log, ev, time.Now())
				c.events++
				pending = append(pending, positioned{ev.End.Offset, eventItem(ev, c.events)})
			}
			// Comments and fields are reported while a chunk is parsed,
			// events after it: put them back in stream order.
			slices.SortStableFunc(pending, func(a, b positioned) int { return compare(a.offset, b.offset) })
			items := make([]StreamItemDTO, len(pending))
			for i, p := range pending {
				p.item.At = at
				items[i] = p.item
			}
			if len(items) > 0 {
				_ = emit.Send(items...)
			}
			if ferr != nil {
				// A parser limit: reconnecting would hit it again.
				c.final = finalError
				return ferr
			}
		}
		if errors.Is(rerr, io.EOF) {
			c.discarded = c.parser.Close()
			return nil
		}
		if rerr != nil {
			if _, ok := errors.AsType[*errs.Error](rerr); !ok {
				// A compressed stream that does not decode.
				c.final = finalError
				rerr = errs.Wrap(rerr, errs.Protocol, "The compressed stream could not be read.").WithCode(CodeUnsupportedEncoding)
			}
			// Else the connection failed or dropped: reconnecting may help.
			c.discarded = c.parser.Close()
			return rerr
		}
	}
}

// positioned is an item and the stream offset it belongs at.
type positioned struct {
	offset int64
	item   StreamItemDTO
}

func compare(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// eventItem is the item of the n-th event.
func eventItem(ev coresse.Event, n int) StreamItemDTO {
	it := StreamItemDTO{
		Type:  ItemEvent,
		Index: n,
		Event: ev.Type,
		ID:    ev.ID,
		Data:  ev.Data,
		Size:  len(ev.Data),
		Line:  ev.Start.Line,
	}
	if len(it.Data) > maxData {
		it.Data, it.Truncated = strings.ToValidUTF8(it.Data[:maxData], ""), true
	}
	if ev.Retry != nil {
		it.RetryMs = retryMs(*ev.Retry)
	}
	return it
}

// fieldItem is the item of a field line worth showing on its own: a valid
// retry, or an id or retry that was ignored.
func fieldItem(f coresse.Field) (StreamItemDTO, bool) {
	switch {
	case f.Name == "retry" && f.Ignored == "":
		var ms int64
		if _, err := fmt.Sscan(f.Value, &ms); err != nil {
			return StreamItemDTO{}, false
		}
		return StreamItemDTO{Type: ItemRetry, RetryMs: &ms, Line: f.Pos.Line}, true
	case f.Ignored == coresse.IgnoredInvalidRetry:
		return StreamItemDTO{Type: ItemNotice, Line: f.Pos.Line,
			Text: fmt.Sprintf("Line %d: the retry field %q is not a whole number of milliseconds, so it was ignored.", f.Pos.Line, clip(f.Value))}, true
	case f.Ignored == coresse.IgnoredIDContainsNull:
		return StreamItemDTO{Type: ItemNotice, Line: f.Pos.Line,
			Text: fmt.Sprintf("Line %d: the id field contains a NULL character, so it was ignored.", f.Pos.Line)}, true
	}
	return StreamItemDTO{}, false
}

// clip shortens a value quoted in a notice.
func clip(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

// badStatus shows the start of a non-2xx response's body and fails with
// CodeBadStatus.
func (c *conn) badStatus(head *httpclient.Response, body io.Reader, emit *operation.Emitter[StreamItemDTO]) error {
	data, _ := io.ReadAll(io.LimitReader(body, maxErrorBody))
	c.bytes = int64(len(data))
	if len(data) > 0 {
		text := strings.ToValidUTF8(string(data), "�")
		_ = emit.Send(StreamItemDTO{Type: ItemNotice, At: formatTime(time.Now()), Text: text})
	}
	return errs.New(errs.Protocol, fmt.Sprintf("The server answered %s instead of an event stream.", head.Status)).
		WithCode(CodeBadStatus).
		WithHint("Check the URL, the method and the auth; the response body is in the log.").
		WithDetail("status", fmt.Sprint(head.StatusCode))
}

// header returns the first value of the response header name.
func header(head *httpclient.Response, name string) string {
	for _, h := range head.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// contentTypeNote warns about a response that is not declared as an
// event stream; it is parsed as one anyway, as a tester should.
func contentTypeNote(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err == nil && mt == "text/event-stream" {
		return ""
	}
	if strings.TrimSpace(ct) == "" {
		return "The response has no Content-Type; it is read as an event stream anyway."
	}
	return fmt.Sprintf("The response's Content-Type is %s, not text/event-stream; it is read as an event stream anyway.", ct)
}

// decoded undoes the response's Content-Encoding (sent only when the
// request asked for one in Accept-Encoding).
func decoded(encoding string, body io.Reader) (io.Reader, error) {
	switch e := strings.ToLower(strings.TrimSpace(encoding)); e {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(body)
		if err != nil {
			return nil, decodeFailed(e, err)
		}
		return r, nil
	case "deflate":
		r, err := zlib.NewReader(body)
		if err != nil {
			return nil, decodeFailed(e, err)
		}
		return r, nil
	default:
		return nil, errs.New(errs.Unsupported, fmt.Sprintf("The stream is compressed with %s, which PostSonde cannot read as it arrives.", encoding)).
			WithCode(CodeUnsupportedEncoding).
			WithHint("Remove the Accept-Encoding header, or allow only gzip.").
			WithDetail("encoding", encoding)
	}
}

func decodeFailed(encoding string, err error) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e
	}
	return errs.Wrap(err, errs.Protocol, "The compressed stream could not be read.").
		WithCode(CodeUnsupportedEncoding).
		WithDetail("encoding", encoding)
}
