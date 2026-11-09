package sse

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes.
const (
	// CodeLineTooLong: a line is longer than Options.MaxLineSize bytes.
	CodeLineTooLong = "sse.line_too_long"
	// CodeEventTooBig: the data buffer of an event would grow past
	// Options.MaxEventSize bytes.
	CodeEventTooBig = "sse.event_too_big"
	// CodeClosed: Feed was called after Close.
	CodeClosed = "sse.closed"
	// CodeReadFailed: reading the stream failed (Events). The kind comes
	// from the cause, for example timeout or cancelled.
	CodeReadFailed = "sse.read_failed"
)

func limitError(code, msg, hint string, limit int, pos Position) *errs.Error {
	return errs.New(errs.Protocol, msg).
		WithCode(code).
		WithHint(hint).
		WithDetail("limit", strconv.Itoa(limit)).
		WithDetail("line", strconv.Itoa(pos.Line)).
		WithDetail("offset", strconv.FormatInt(pos.Offset, 10))
}

func lineTooLong(limit int, pos Position) *errs.Error {
	return limitError(CodeLineTooLong, "A line in the event stream is longer than the limit.",
		"Raise the line size limit if the server really sends lines this long.", limit, pos)
}

func eventTooBig(limit int, pos Position) *errs.Error {
	return limitError(CodeEventTooBig, "An event in the event stream is larger than the limit.",
		"Raise the event size limit if the server really sends events this large.", limit, pos)
}

var errClosed = errs.New(errs.InvalidArgument, "The event stream parser is closed.").WithCode(CodeClosed)

func readFailed(err error) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), "Could not read the event stream.").WithCode(CodeReadFailed)
}
