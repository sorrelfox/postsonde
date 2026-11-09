// Package transportsse reads GraphQL over Server-Sent Events (GQL-32):
// the response to a request of graphql-sse's "distinct connections" mode,
// one HTTP request per operation, POST (or GET) with
// Accept: text/event-stream. The server streams "next" events whose data
// is an ExecutionResult and ends with a "complete" event; comment lines
// keep the connection alive. The caller sends the request (the app's
// HTTP client) and hands Read the response; closing the response body
// (cancelling the request) stops the operation. A response that is not
// an event stream (a request error such as a validation failure, or a
// server answering a query with plain JSON) is read as one result.
//
// Events are parsed with realtime/sse. Every event and keep-alive is
// reported to Options.OnEvent with a timestamp for the event log. See
// docs/graphql-subscriptions.md.
package transportsse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sse"
)

// Error codes.
const (
	// CodeInvalidEvent: a next event's data is not a JSON object (kind
	// protocol). Reading stops.
	CodeInvalidEvent = "transportsse.invalid_event"
	// CodeStreamEnded: the event stream ended without a complete event
	// (kind network).
	CodeStreamEnded = "transportsse.stream_ended"
	// CodeHTTPStatus: the server answered with a status other than 2xx
	// and no GraphQL errors (kind permission_denied for 401 and 403,
	// protocol otherwise). Details: status.
	CodeHTTPStatus = "transportsse.http_status"
	// CodeUnexpectedResponse: a 2xx response that is neither an event
	// stream nor a JSON object (kind protocol). Details: contentType.
	CodeUnexpectedResponse = "transportsse.unexpected_response"
)

// Event types reported besides the names of ignored events.
const (
	// TypeNext is a next event (or an event without a name, "message"):
	// Payload is the ExecutionResult.
	TypeNext = "next"
	// TypeComplete is the complete event: the operation is over.
	TypeComplete = "complete"
	// TypeKeepAlive is a comment line, which servers send to keep the
	// connection open; Data is its text.
	TypeKeepAlive = "keep-alive"
	// TypeResponse is a response body that is not an event stream: the
	// result of the request, Payload when it is a JSON object.
	TypeResponse = "response"
)

// Note marks an event that was not acted on.
type Note string

// Notes.
const (
	// NoteIgnored: an event of another type, or a message event without
	// data.
	NoteIgnored Note = "ignored"
	// NoteNotEventStream: the response was not an event stream (TypeResponse).
	NoteNotEventStream Note = "not_event_stream"
)

// DefaultMaxResponse is the most bytes of a response body that is not an
// event stream Read takes.
const DefaultMaxResponse = 16 << 20

// Head is what Read needs of the response's head.
type Head struct {
	Status      int
	ContentType string
}

// Options configure Read. The zero value is ready to use.
type Options struct {
	// OnEvent is called for every event, keep-alive and a response that is
	// not an event stream, in order.
	OnEvent func(Event)
	// Now returns the time events are stamped with; nil is time.Now.
	Now func() time.Time
	// MaxResponse caps a body that is not an event stream; 0 is
	// DefaultMaxResponse.
	MaxResponse int
}

// Event is one event of the stream as received.
type Event struct {
	// At is when the read that completed it returned.
	At time.Time
	// Type is TypeNext, TypeComplete, TypeKeepAlive, TypeResponse or the
	// name of an ignored event.
	Type string
	// Payload is the ExecutionResult of a next event, or a response body
	// that is a JSON object; nil otherwise.
	Payload json.RawMessage
	// Data is the event's data (a comment's text, a response body) as
	// received; Size its size in bytes.
	Data string
	Size int
	// Err is set for an event that broke the protocol; reading stops.
	Err  error
	Note Note
}

// OutcomeKind says how an operation ended.
type OutcomeKind string

// Outcome kinds.
const (
	// OutcomeComplete: the server sent complete, or answered with one
	// result that is not an event stream.
	OutcomeComplete OutcomeKind = "complete"
	// OutcomeRejected: the server answered with GraphQL errors and no
	// data instead of a stream (Errors).
	OutcomeRejected OutcomeKind = "rejected"
	// OutcomeFailed: the stream broke the protocol, ended early or could
	// not be read, or the response was an HTTP error (Err).
	OutcomeFailed OutcomeKind = "failed"
)

// Outcome is how Read ended.
type Outcome struct {
	Kind OutcomeKind
	// Errors is the GraphQL error list of OutcomeRejected, as received.
	Errors json.RawMessage
	// Err says why, for OutcomeFailed: CodeInvalidEvent,
	// CodeStreamEnded, CodeHTTPStatus, CodeUnexpectedResponse, a limit
	// error of realtime/sse, or the body's read error as returned (e.g.
	// when the request was cancelled).
	Err error
}

// Read reads the response of a GraphQL over SSE request until the
// operation ends and reports its events to o.OnEvent. It returns when the
// server sends complete, the stream breaks the protocol, ends or fails.
func Read(head Head, body io.Reader, o Options) Outcome {
	r := reader{o: o}
	if r.o.Now == nil {
		r.o.Now = time.Now
	}
	if r.o.MaxResponse <= 0 {
		r.o.MaxResponse = DefaultMaxResponse
	}
	mt, _, _ := mime.ParseMediaType(head.ContentType)
	if mt != "text/event-stream" || head.Status < 200 || head.Status > 299 {
		return r.response(head, body)
	}
	return r.stream(body)
}

type reader struct {
	o Options
}

func (r *reader) emit(ev Event) {
	if r.o.OnEvent != nil {
		r.o.OnEvent(ev)
	}
}

// item is an event or a comment, ordered by where it ends in the stream.
type item struct {
	offset  int64
	event   *sse.Event
	comment *sse.Comment
}

func (r *reader) stream(body io.Reader) Outcome {
	var comments []sse.Comment
	// completeAt is where an "event: complete" line is that no dispatched
	// event covers yet, -1 for none: a complete without a data line is
	// never dispatched (the standard drops events without data), so it
	// counts once the server ends the stream after it.
	completeAt := int64(-1)
	p := sse.NewParser(sse.Options{
		OnComment: func(c sse.Comment) { comments = append(comments, c) },
		OnField: func(f sse.Field) {
			if f.Name == "event" && f.Value == TypeComplete {
				completeAt = f.Pos.Offset
			}
		},
	})
	defer p.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			evs, perr := p.Feed(buf[:n])
			at := r.o.Now()
			items := make([]item, 0, len(evs)+len(comments))
			for i := range evs {
				items = append(items, item{offset: evs[i].End.Offset, event: &evs[i]})
			}
			for i := range comments {
				items = append(items, item{offset: comments[i].Pos.Offset, comment: &comments[i]})
			}
			comments = comments[:0]
			slices.SortStableFunc(items, func(a, b item) int { return int(a.offset - b.offset) })
			for _, it := range items {
				if it.comment != nil {
					r.emit(Event{At: at, Type: TypeKeepAlive, Data: it.comment.Text, Size: len(it.comment.Text) + 1})
					continue
				}
				if it.event.End.Offset > completeAt {
					completeAt = -1
				}
				if out, done := r.event(at, *it.event); done {
					return out
				}
			}
			if perr != nil {
				return Outcome{Kind: OutcomeFailed, Err: perr}
			}
		}
		if errors.Is(err, io.EOF) && completeAt >= 0 {
			r.emit(Event{At: r.o.Now(), Type: TypeComplete})
			return Outcome{Kind: OutcomeComplete}
		}
		if errors.Is(err, io.EOF) {
			return Outcome{Kind: OutcomeFailed, Err: errs.New(errs.Network, "The server ended the event stream without a complete event.").
				WithCode(CodeStreamEnded).
				WithHint("The operation may not have finished. Check the server's GraphQL over SSE implementation.")}
		}
		if err != nil {
			return Outcome{Kind: OutcomeFailed, Err: err}
		}
	}
}

// event handles one event; done ends reading with out.
func (r *reader) event(at time.Time, e sse.Event) (out Outcome, done bool) {
	ev := Event{At: at, Type: e.Type, Data: e.Data, Size: len(e.Data)}
	switch e.Type {
	case TypeComplete:
		r.emit(ev)
		return Outcome{Kind: OutcomeComplete}, true
	case TypeNext, sse.DefaultEventType:
		ev.Type = TypeNext
		data := bytes.TrimSpace([]byte(e.Data))
		if len(data) == 0 && e.Type == sse.DefaultEventType {
			ev.Type, ev.Note = e.Type, NoteIgnored
			r.emit(ev)
			return Outcome{}, false
		}
		if !isObject(data) {
			err := errs.New(errs.Protocol, "The server sent a next event whose data is not a JSON object.").
				WithCode(CodeInvalidEvent).
				WithHint("PostSonde stopped reading the stream. Check the server's GraphQL over SSE implementation.")
			ev.Err = err
			r.emit(ev)
			return Outcome{Kind: OutcomeFailed, Err: err}, true
		}
		ev.Payload = json.RawMessage(data)
		r.emit(ev)
		return Outcome{}, false
	default:
		ev.Note = NoteIgnored
		r.emit(ev)
		return Outcome{}, false
	}
}

// response reads a body that is not an event stream as one result.
func (r *reader) response(head Head, body io.Reader) Outcome {
	data, err := io.ReadAll(io.LimitReader(body, int64(r.o.MaxResponse)))
	if err != nil {
		return Outcome{Kind: OutcomeFailed, Err: err}
	}
	ev := Event{At: r.o.Now(), Type: TypeResponse, Data: strings.ToValidUTF8(string(data), "�"), Size: len(data), Note: NoteNotEventStream}
	trimmed := bytes.TrimSpace(data)
	var result struct {
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	isJSON := isObject(trimmed) && json.Unmarshal(trimmed, &result) == nil
	if isJSON {
		ev.Payload = json.RawMessage(trimmed)
	}
	r.emit(ev)
	ok := head.Status >= 200 && head.Status <= 299
	noData := len(result.Data) == 0 || string(result.Data) == "null"
	hasErrors := bytes.HasPrefix(bytes.TrimSpace(result.Errors), []byte("[")) && string(bytes.TrimSpace(result.Errors)) != "[]"
	switch {
	case isJSON && noData && hasErrors:
		return Outcome{Kind: OutcomeRejected, Errors: result.Errors}
	case !ok:
		kind := errs.Protocol
		if head.Status == http.StatusUnauthorized || head.Status == http.StatusForbidden {
			kind = errs.PermissionDenied
		}
		text := strconv.Itoa(head.Status)
		if t := http.StatusText(head.Status); t != "" {
			text += " " + t
		}
		return Outcome{Kind: OutcomeFailed, Err: errs.New(kind, "The server answered "+text+" instead of an event stream.").
			WithCode(CodeHTTPStatus).
			WithDetail("status", strconv.Itoa(head.Status))}
	case isJSON:
		return Outcome{Kind: OutcomeComplete}
	}
	return Outcome{Kind: OutcomeFailed, Err: errs.New(errs.Protocol, "The server answered with neither an event stream nor a JSON result.").
		WithCode(CodeUnexpectedResponse).
		WithHint("Check that the endpoint supports GraphQL over Server-Sent Events.").
		WithDetail("contentType", head.ContentType)}
}

func isObject(raw []byte) bool {
	return len(raw) > 0 && raw[0] == '{' && json.Valid(raw)
}
