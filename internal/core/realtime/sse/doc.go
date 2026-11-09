// Package sse parses Server-Sent Events streams (text/event-stream) as the
// WHATWG HTML standard's "Interpreting an event stream" section describes.
//
// A Parser is fed the response body chunk by chunk as it arrives and returns
// the events each chunk completes. Chunks may split lines, multi-byte UTF-8
// sequences and CRLF pairs anywhere: the output does not depend on how the
// stream is chunked. Events wraps a Parser around an io.Reader as an
// iterator.
//
// The parser implements the standard exactly:
//
//   - the stream is decoded as UTF-8, invalid sequences become U+FFFD (the
//     WHATWG decoder's replacement, one U+FFFD per maximal invalid
//     subpart), and one U+FEFF byte order mark is dropped at the start of
//     the stream only;
//   - lines end with CRLF, LF or CR; a CR that ends a chunk ends its line at
//     once, and an LF that starts the next chunk belongs to the same line
//     break;
//   - a line starting with ':' is a comment; otherwise the field name runs
//     to the first ':' and one space after it is dropped from the value;
//   - "event" sets the event type, "data" appends the value and an LF to
//     the data buffer, "id" sets the last event ID buffer unless the value
//     contains U+0000, "retry" sets the reconnection time when the value is
//     only ASCII digits, and other fields are ignored;
//   - a blank line dispatches: the last event ID is committed, and when the
//     data buffer is not empty an Event is produced with its final LF
//     removed and the type "message" unless an "event" field set one;
//   - at the end of the stream (Close) an incomplete event is discarded.
//
// Comments and every field line are reported through Options.OnComment and
// Options.OnField, with byte offsets and line numbers, for the debugger
// timeline. Lines and the data buffer are limited in size (16 MiB each by
// default); exceeding a limit fails the parser with a CodeLineTooLong or
// CodeEventTooBig error. See docs/sse.md.
package sse
