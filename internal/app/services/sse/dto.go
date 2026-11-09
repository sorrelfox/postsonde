package sse

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Types of StreamItemDTO, in the order a stream sends them.
const (
	// ItemOpen is the response's status line and headers, once they are in.
	ItemOpen = "open"
	// ItemEvent is one dispatched event (SSE-02).
	ItemEvent = "event"
	// ItemComment is a comment line (": ..."), typically a heartbeat.
	ItemComment = "comment"
	// ItemRetry is a valid retry field: the reconnection time the server
	// asks for, in effect at once.
	ItemRetry = "retry"
	// ItemNotice is something about the stream the user should know: a
	// Content-Type that is not text/event-stream, the body of a response
	// that is not a stream.
	ItemNotice = "notice"
	// ItemReconnect is a connection that ended while the stream
	// reconnects (SSE-03): why, and when the next attempt is.
	ItemReconnect = "reconnect"
	// ItemEnd ends every stream that opened: why, and what it carried.
	ItemEnd = "end"
)

// Reasons of an ItemEnd.
const (
	// EndClosed: the server ended the response.
	EndClosed = "closed"
	// EndStopped: the user stopped the stream (OpsService.Cancel), or the
	// app quit.
	EndStopped = "stopped"
	// EndError: the stream failed; the done event has the error.
	EndError = "error"
)

// ConnectInputDTO is what Connect takes.
type ConnectInputDTO struct {
	// RequestID is the sse request to connect.
	RequestID string `json:"requestId"`
	// AllowUnresolved sends undefined {{variables}} as written instead of
	// refusing (ENV-11).
	AllowUnresolved bool `json:"allowUnresolved"`
	// LastEventID, when not "", is sent as Last-Event-ID by the first
	// connection: resuming a stream that ended (SSE-03).
	LastEventID string `json:"lastEventId"`
}

// HeaderDTO is a response header.
type HeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StreamItemDTO is one item of a stream's operation (sse:op:data). Type
// says which fields are set; the others are empty.
type StreamItemDTO struct {
	// Type is ItemOpen, ItemEvent, ItemComment, ItemRetry, ItemNotice or
	// ItemEnd.
	Type string `json:"type"`
	// At is when the bytes that made the item arrived, RFC 3339 in UTC
	// with nanoseconds.
	At string `json:"at"`

	// Method and URL are the request as sent, secrets in the URL masked
	// (open).
	Method string `json:"method"`
	URL    string `json:"url"`
	// Status is the status code, StatusText the status line after the
	// protocol ("200 OK"), Proto "HTTP/1.1" or "HTTP/2.0" (open).
	Status     int    `json:"status"`
	StatusText string `json:"statusText"`
	Proto      string `json:"proto"`
	// Headers are the response headers, sorted by name (open).
	Headers []HeaderDTO `json:"headers"`

	// Index counts events from 1 (event).
	Index int `json:"index"`
	// Event is the event type: "message" unless an event field set it
	// (event).
	Event string `json:"event"`
	// ID is the last event ID when the event was dispatched: its own id
	// field, or the latest before it (event, end); the one the next
	// connection sends as Last-Event-ID (reconnect).
	ID string `json:"id"`
	// Data is the event's data, its data lines joined by LF; cut at
	// maxData bytes, with Truncated set (event).
	Data      string `json:"data"`
	Truncated bool   `json:"truncated"`
	// Size is the data's full length in bytes (event).
	Size int `json:"size"`
	// RetryMs is the reconnection time in milliseconds: set by this
	// event's block (event), the field's value (retry), the latest the
	// server set (end); null when there is none.
	RetryMs *int64 `json:"retryMs"`
	// Line is the line number in the stream, from 1, of the event's first
	// line, the comment or the field (event, comment, retry).
	Line int `json:"line"`

	// Text is the comment after its colon (comment), the message
	// (notice) or why the connection ended (reconnect).
	Text string `json:"text"`

	// Attempt counts the reconnection attempts in a row that did not
	// deliver a byte, from 1; MaxAttempts is the policy's limit, 0 for
	// none; DelayMs is the wait before the attempt (reconnect).
	Attempt     int   `json:"attempt"`
	MaxAttempts int   `json:"maxAttempts"`
	DelayMs     int64 `json:"delayMs"`

	// Reason is EndClosed, EndStopped or EndError (end); EndClosed or
	// EndError for the connection that ended (reconnect).
	Reason string `json:"reason"`
	// Events is the number of events dispatched, Bytes the body bytes
	// read (decompressed), over all connections (end).
	Events int   `json:"events"`
	Bytes  int64 `json:"bytes"`
	// Reconnects is the number of reconnections made (end).
	Reconnects int `json:"reconnects"`
	// Discarded is set when the stream ended in the middle of an event,
	// which is dropped as the standard says (end).
	Discarded bool `json:"discarded"`
	// ElapsedMs is the time from Connect (end).
	ElapsedMs int64 `json:"elapsedMs"`
	// Tests are the results of the request's assertions against the
	// events received, over every connection (TEST-08, end); empty
	// without assertions, null on the other items.
	Tests []streamcheck.TestResultDTO `json:"tests"`
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// toHeaderDTOs maps received headers; the list is never null.
func toHeaderDTOs(hs []httpclient.Header) []HeaderDTO {
	out := make([]HeaderDTO, 0, len(hs))
	for _, h := range hs {
		out = append(out, HeaderDTO{Name: h.Name, Value: h.Value})
	}
	return out
}

func retryMs(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}
