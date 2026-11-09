package sse

import (
	"bytes"
	"math"
	"strings"
	"time"
)

// Default limits.
const (
	DefaultMaxLineSize  = 16 << 20
	DefaultMaxEventSize = 16 << 20
)

// DefaultEventType is the type of an event without an "event" field.
const DefaultEventType = "message"

// Options configure a Parser. The zero value is ready to use.
type Options struct {
	// MaxLineSize is the most bytes one line may have, without its line
	// break (and with a leading byte order mark). 0 means
	// DefaultMaxLineSize; a negative value means no limit.
	MaxLineSize int
	// MaxEventSize is the most bytes the data buffer of one event may
	// hold: the decoded "data" values with an LF after each. 0 means
	// DefaultMaxEventSize; a negative value means no limit.
	MaxEventSize int
	// OnComment, if set, is called for every comment line, during Feed.
	OnComment func(Comment)
	// OnField, if set, is called for every field line after it has been
	// applied, during Feed. Ignored fields are reported too.
	OnField func(Field)
	// LastEventID is the last event ID the stream starts with: the one a
	// reconnection sent as Last-Event-ID (SSE-03), which events carry
	// until an "id" field changes it, as the standard keeps it across
	// reconnections. A value containing U+0000 is ignored.
	LastEventID string
}

// Position is where a line starts in the stream.
type Position struct {
	// Offset is the byte offset in the raw stream, counting a byte order
	// mark and line breaks.
	Offset int64 `json:"offset"`
	// Line is the line number, from 1.
	Line int `json:"line"`
}

// Event is a dispatched event.
type Event struct {
	// Type is the "event" field, or DefaultEventType.
	Type string `json:"type"`
	// Data is the "data" values joined with LF.
	Data string `json:"data"`
	// ID is the last event ID when the event was dispatched. It persists
	// from earlier events until an "id" field changes it.
	ID string `json:"id"`
	// Retry is the reconnection time if a valid "retry" field was in this
	// event's block of lines, nil otherwise.
	Retry *time.Duration `json:"retry,omitempty"`
	// Start is the first line of the event's block (after the previous
	// blank line); End is the blank line that dispatched it.
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Comment is a line starting with ':'.
type Comment struct {
	// Text is everything after the colon, spaces kept.
	Text string   `json:"text"`
	Pos  Position `json:"pos"`
}

// IgnoreReason says why a field line had no effect.
type IgnoreReason string

// Ignore reasons. The empty reason means the field was applied.
const (
	IgnoredUnknownField   IgnoreReason = "unknown_field"
	IgnoredIDContainsNull IgnoreReason = "id_contains_null"
	IgnoredInvalidRetry   IgnoreReason = "invalid_retry"
)

// Field is a field line: "name: value", "name:value" or "name".
type Field struct {
	Name  string   `json:"name"`
	Value string   `json:"value"`
	Pos   Position `json:"pos"`
	// Ignored is empty when the field was applied.
	Ignored IgnoreReason `json:"ignored,omitempty"`
}

var bom = []byte{0xEF, 0xBB, 0xBF}

// Parser is an incremental event stream parser. It is not safe for
// concurrent use.
type Parser struct {
	maxLine, maxEvent int
	onComment         func(Comment)
	onField           func(Field)

	partial []byte // bytes of the unfinished line
	offset  int64  // stream offset of the next byte
	pos     Position
	skipLF  bool // the last line ended with CR: an LF next is part of it

	data       []byte
	eventType  string
	idBuffer   string
	lastID     string
	retry      time.Duration
	hasRetry   bool
	blockRetry *time.Duration
	blockStart Position
	blockOpen  bool

	events []Event
	err    error
	closed bool
}

// NewParser returns a parser at the start of a stream.
func NewParser(opts Options) *Parser {
	p := &Parser{
		maxLine:   opts.MaxLineSize,
		maxEvent:  opts.MaxEventSize,
		onComment: opts.OnComment,
		onField:   opts.OnField,
		pos:       Position{Line: 1},
	}
	if !strings.ContainsRune(opts.LastEventID, 0) {
		p.idBuffer, p.lastID = opts.LastEventID, opts.LastEventID
	}
	if p.maxLine == 0 {
		p.maxLine = DefaultMaxLineSize
	}
	if p.maxEvent == 0 {
		p.maxEvent = DefaultMaxEventSize
	}
	return p
}

// Feed parses the next chunk of the stream and returns the events it
// dispatched, in order. After a limit error the parser is failed: Feed
// returns the events dispatched before the error with the error, and the
// same error on every later call.
func (p *Parser) Feed(chunk []byte) ([]Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.closed {
		return nil, errClosed
	}
	p.events = nil
	for len(chunk) > 0 {
		if p.skipLF {
			p.skipLF = false
			if chunk[0] == '\n' {
				chunk = chunk[1:]
				p.offset++
				p.pos.Offset = p.offset
				continue
			}
		}
		i := bytes.IndexAny(chunk, "\r\n")
		if i < 0 {
			if p.maxLine >= 0 && len(p.partial)+len(chunk) > p.maxLine {
				return p.fail(lineTooLong(p.maxLine, p.pos))
			}
			p.partial = append(p.partial, chunk...)
			p.offset += int64(len(chunk))
			break
		}
		if p.maxLine >= 0 && len(p.partial)+i > p.maxLine {
			return p.fail(lineTooLong(p.maxLine, p.pos))
		}
		line := chunk[:i]
		if len(p.partial) > 0 {
			p.partial = append(p.partial, line...)
			line = p.partial
		}
		p.skipLF = chunk[i] == '\r'
		p.offset += int64(i + 1)
		chunk = chunk[i+1:]
		err := p.processLine(line)
		p.resetPartial()
		if err != nil {
			return p.fail(err)
		}
		p.pos = Position{Offset: p.offset, Line: p.pos.Line + 1}
	}
	evs := p.events
	p.events = nil
	return evs, nil
}

func (p *Parser) fail(err error) ([]Event, error) {
	p.err = err
	evs := p.events
	p.events = nil
	p.partial, p.data = nil, nil
	return evs, err
}

func (p *Parser) resetPartial() {
	if cap(p.partial) > 64<<10 {
		p.partial = nil
	} else {
		p.partial = p.partial[:0]
	}
}

func (p *Parser) processLine(line []byte) error {
	pos := p.pos
	if pos.Line == 1 && bytes.HasPrefix(line, bom) {
		line = line[len(bom):]
	}
	if len(line) == 0 {
		p.dispatch(pos)
		return nil
	}
	if !p.blockOpen {
		p.blockStart, p.blockOpen = pos, true
	}
	if line[0] == ':' {
		if p.onComment != nil {
			p.onComment(Comment{Text: decodeUTF8(line[1:]), Pos: pos})
		}
		return nil
	}
	name, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		name, value = line[:i], line[i+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	var (
		ignored IgnoreReason
		decoded string
	)
	switch string(name) {
	case "event":
		decoded = decodeUTF8(value)
		p.eventType = decoded
	case "data":
		decoded = decodeUTF8(value)
		if p.maxEvent >= 0 && len(p.data)+len(decoded)+1 > p.maxEvent {
			return eventTooBig(p.maxEvent, pos)
		}
		p.data = append(p.data, decoded...)
		p.data = append(p.data, '\n')
	case "id":
		decoded = decodeUTF8(value)
		if bytes.IndexByte(value, 0) >= 0 {
			ignored = IgnoredIDContainsNull
		} else {
			p.idBuffer = decoded
		}
	case "retry":
		decoded = decodeUTF8(value)
		if d, ok := parseRetry(value); ok {
			p.retry, p.hasRetry = d, true
			p.blockRetry = &d
		} else {
			ignored = IgnoredInvalidRetry
		}
	default:
		ignored = IgnoredUnknownField
	}
	if p.onField != nil {
		if ignored == IgnoredUnknownField {
			decoded = decodeUTF8(value)
		}
		p.onField(Field{Name: decodeUTF8(name), Value: decoded, Pos: pos, Ignored: ignored})
	}
	return nil
}

// parseRetry reads a reconnection time in milliseconds: one or more ASCII
// digits. Values too large for a time.Duration are rejected.
func parseRetry(v []byte) (time.Duration, bool) {
	if len(v) == 0 {
		return 0, false
	}
	const maxMs = math.MaxInt64 / int64(time.Millisecond)
	var ms int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		ms = ms*10 + int64(c-'0')
		if ms > maxMs {
			return 0, false
		}
	}
	return time.Duration(ms) * time.Millisecond, true
}

func (p *Parser) dispatch(end Position) {
	start := end
	if p.blockOpen {
		start = p.blockStart
	}
	p.blockOpen = false
	retry := p.blockRetry
	p.blockRetry = nil
	p.lastID = p.idBuffer
	if len(p.data) == 0 {
		p.eventType = ""
		return
	}
	ev := Event{
		Type:  DefaultEventType,
		Data:  string(p.data[:len(p.data)-1]), // every append ends with LF
		ID:    p.lastID,
		Retry: retry,
		Start: start,
		End:   end,
	}
	if p.eventType != "" {
		ev.Type = p.eventType
	}
	p.eventType = ""
	if cap(p.data) > 1<<20 {
		p.data = nil
	} else {
		p.data = p.data[:0]
	}
	p.events = append(p.events, ev)
}

// Close marks the end of the stream. An incomplete event (lines after the
// last blank line, including an unfinished line) is discarded, as the
// standard requires; Close reports whether there was one. Fields of the
// discarded block that already took effect stay: a "retry" value and the
// last event ID buffer (which is committed only by a blank line, so
// LastEventID does not change).
func (p *Parser) Close() (discarded bool) {
	if p.closed {
		return false
	}
	p.closed = true
	discarded = p.err == nil && (len(p.partial) > 0 || p.blockOpen)
	p.partial, p.data, p.eventType = nil, nil, ""
	p.blockOpen, p.blockRetry = false, nil
	return discarded
}

// LastEventID is the last event ID committed by a blank line: the value to
// send as Last-Event-ID when reconnecting.
func (p *Parser) LastEventID() string { return p.lastID }

// Retry is the reconnection time from the latest valid "retry" field, and
// whether there was one.
func (p *Parser) Retry() (time.Duration, bool) { return p.retry, p.hasRetry }

// Offset is the number of bytes fed so far.
func (p *Parser) Offset() int64 { return p.offset }

// Err is the error that failed the parser, or nil.
func (p *Parser) Err() error { return p.err }
