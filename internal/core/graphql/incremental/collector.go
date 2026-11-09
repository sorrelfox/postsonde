package incremental

import (
	"mime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/realtime/sse"
)

// Formats of an incremental response body.
const (
	// FormatMultipart is multipart/mixed, each part one JSON payload.
	FormatMultipart = "multipart"
	// FormatEventStream is text/event-stream, each "next" (or unnamed)
	// event's data one JSON payload, "complete" the end.
	FormatEventStream = "event-stream"
)

// DefaultBoundary is the boundary of a multipart/mixed response whose
// Content-Type names none: the one every GraphQL server uses.
const DefaultBoundary = "-"

// Limits of what a Snapshot carries: the parts' texts are for reading,
// the body itself is kept elsewhere.
const (
	// MaxParts is how many parts are listed; later ones are counted.
	MaxParts = 1000
	// MaxPartText is the most of one part's text listed.
	MaxPartText = 64 << 10
	// MaxPartsText is the most text all listed parts carry together;
	// after it, parts are listed without their text.
	MaxPartsText = 2 << 20
	// MaxMergedText is the largest merged result a Snapshot carries.
	MaxMergedText = 4 << 20
)

// Detect returns the format of a response whose Content-Type is
// contentType, and the multipart boundary: "" for a body that is not
// incremental delivery.
func Detect(contentType string) (format, boundary string) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", ""
	}
	switch mt {
	case "multipart/mixed":
		b := params["boundary"]
		if b == "" {
			b = DefaultBoundary
		}
		return FormatMultipart, b
	case "text/event-stream":
		return FormatEventStream, ""
	}
	return "", ""
}

// Part is one payload as it arrived.
type Part struct {
	// Index counts the parts from 0.
	Index int
	// At is when the part was complete, from the collector's start.
	At time.Duration
	// Text is the payload, two-space indented when it is JSON; empty
	// once the parts listed carry MaxPartsText. Clipped says it was cut
	// at MaxPartText bytes (or left out).
	Text    string
	Clipped bool
	// Size is the payload's size in bytes, as received.
	Size int
	// Entries is what the payload did: the initial result, deferred data,
	// streamed items, pending and completed entries.
	Entries []Entry
	// HasNext is the payload's hasNext, nil when it has none.
	HasNext *bool
	// Problem says why the payload was not merged: it is not a JSON
	// object. "" when it was.
	Problem string
}

// Snapshot is what a collector has so far.
type Snapshot struct {
	Format string
	// Parts are the first MaxParts parts; Count counts all of them.
	Parts []Part
	Count int
	// Merged is the merged result (Result.JSON), "" before a payload was
	// merged or when it is over MaxMergedText (MergedTooLarge; its size
	// is MergedSize).
	Merged         string
	MergedTooLarge bool
	MergedSize     int
	// HasNext is the latest hasNext; Completed is set once a payload said
	// hasNext: false (or, for an event stream, a complete event came).
	HasNext   bool
	Completed bool
	// Pending counts the deferred fragments and streams announced but not
	// completed (newer format).
	Pending int
	// Errors counts the errors of the merged result.
	Errors int
}

// Collector reads an incremental response body as it arrives: it splits
// it into payloads, notes when each came and merges them. Write it the
// body's bytes; it never fails. It is safe for concurrent use: Snapshot
// may be called while another goroutine writes.
type Collector struct {
	mu        sync.Mutex
	format    string
	mp        *multipartParser
	events    *sse.Parser
	result    Result
	parts     []Part
	count     int
	textBytes int
	start     time.Time
	now       func() time.Time
	onPart    func()
	completed bool
	written   int64
}

// NewCollector returns a collector for a body of Content-Type
// contentType, or nil when it is not incremental delivery (Detect).
// Parts are timed from start. onPart, when set, is called after a Write
// that completed parts, outside the collector's lock.
func NewCollector(contentType string, start time.Time, onPart func()) *Collector {
	format, boundary := Detect(contentType)
	if format == "" {
		return nil
	}
	c := &Collector{format: format, start: start, now: time.Now, onPart: onPart}
	if format == FormatMultipart {
		c.mp = newMultipartParser(boundary)
	} else {
		c.events = sse.NewParser(sse.Options{})
	}
	return c
}

// Write reads the next bytes of the body. It always takes them all.
func (c *Collector) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.written += int64(len(p))
	n := c.count
	if c.mp != nil {
		for _, body := range c.mp.feed(p) {
			c.add(body)
		}
	} else {
		evs, _ := c.events.Feed(p)
		c.addEvents(evs)
	}
	added := c.count > n
	c.mu.Unlock()
	if added && c.onPart != nil {
		c.onPart()
	}
	return len(p), nil
}

// Close ends the body: a last multipart part without a delimiter after
// it is taken.
func (c *Collector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mp != nil {
		for _, body := range c.mp.close() {
			c.add(body)
		}
	} else {
		c.events.Close()
	}
}

func (c *Collector) addEvents(evs []sse.Event) {
	for _, ev := range evs {
		if c.completed {
			return
		}
		switch ev.Type {
		case "next", sse.DefaultEventType:
			if strings.TrimSpace(ev.Data) != "" {
				c.add([]byte(ev.Data))
			}
		case "complete":
			c.completed = true
		}
	}
}

// add merges one payload and lists it.
func (c *Collector) add(body []byte) {
	part := Part{Index: c.count, At: c.now().Sub(c.start), Size: len(body)}
	c.count++
	entries, err := c.result.Apply(body)
	if err != nil {
		part.Problem = "The part is " + err.Error() + ", so it was not merged."
	} else {
		part.Entries = entries
		part.HasNext = c.result.lastNext()
	}
	if len(c.parts) >= MaxParts {
		return
	}
	text := prettyText(body)
	if err != nil {
		text = string(body)
	}
	if len(text) > MaxPartText {
		text, part.Clipped = clipUTF8(text, MaxPartText), true
	}
	if c.textBytes+len(text) > MaxPartsText {
		text, part.Clipped = "", true
	}
	c.textBytes += len(text)
	part.Text = text
	c.parts = append(c.parts, part)
}

// clipUTF8 cuts s to at most n bytes, at a character boundary.
func clipUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Written counts the body bytes written so far.
func (c *Collector) Written() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

// Snapshot returns what has arrived so far.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Snapshot{
		Format:    c.format,
		Parts:     append([]Part(nil), c.parts...),
		Count:     c.count,
		HasNext:   c.result.HasNext(),
		Completed: c.result.Completed() || c.completed,
		Pending:   c.result.Pending(),
		Errors:    c.result.Errors(),
	}
	merged := c.result.JSON()
	s.MergedSize = len(merged)
	if len(merged) > MaxMergedText {
		s.MergedTooLarge = true
	} else {
		s.Merged = merged
	}
	return s
}
