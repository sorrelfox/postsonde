package http

import (
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/incremental"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// PartialInterval is the shortest time between two partial responses of
// an incremental GraphQL response (GQL-08): parts that arrive faster are
// shown together.
const PartialInterval = 100 * time.Millisecond

// IncrementalDTO is a GraphQL response delivered incrementally (GQL-08,
// @defer and @stream): its payloads as they arrived and their merged
// result. See docs/graphql.md.
type IncrementalDTO struct {
	// Format is "multipart" (multipart/mixed) or "event-stream"
	// (text/event-stream).
	Format string `json:"format"`
	// Receiving is set on the partial responses sent while the body
	// arrives; the final response has it false.
	Receiving bool `json:"receiving"`
	// Parts are the payloads in the order they arrived, at most
	// incremental.MaxParts of them; PartCount counts all.
	Parts     []IncrementalPartDTO `json:"parts"`
	PartCount int                  `json:"partCount"`
	// Merged is the merged result, two-space indented: the initial data
	// with every deferred fragment merged at its path and every streamed
	// item appended, and all errors and extensions. "" when it is over
	// incremental.MaxMergedText (MergedTooLarge; MergedSize is its size)
	// or before a payload was merged.
	Merged         string `json:"merged"`
	MergedTooLarge bool   `json:"mergedTooLarge"`
	MergedSize     int    `json:"mergedSize"`
	// HasNext is the latest payload's hasNext; Completed is set once a
	// payload said hasNext: false (or an event stream sent complete). A
	// final response without Completed ended early.
	HasNext   bool `json:"hasNext"`
	Completed bool `json:"completed"`
	// Pending counts the announced deferred fragments and streams not
	// completed yet (newer payload format).
	Pending int `json:"pending"`
	// Errors counts the errors of the merged result.
	Errors int `json:"errors"`
}

// IncrementalPartDTO is one payload.
type IncrementalPartDTO struct {
	// Index counts from 0.
	Index int `json:"index"`
	// AtMs is when the payload was complete, in milliseconds since the
	// request was sent (fractions kept).
	AtMs float64 `json:"atMs"`
	// Text is the payload, two-space indented when it is JSON; cut at 64
	// KiB, or left out once the listed parts carry 2 MiB (Clipped).
	Text    string `json:"text"`
	Clipped bool   `json:"clipped"`
	// Size is the payload's size in bytes.
	Size int `json:"size"`
	// Entries is what the payload did.
	Entries []IncrementalEntryDTO `json:"entries"`
	// HasNext is the payload's hasNext, null when it has none.
	HasNext *bool `json:"hasNext"`
	// Problem says why it was not merged (not a JSON object), else "".
	Problem string `json:"problem"`
}

// IncrementalEntryDTO is one thing a payload did.
type IncrementalEntryDTO struct {
	// Kind is "initial", "defer", "stream", "pending" or "completed".
	Kind string `json:"kind"`
	// Path is where it applies ("user.friends[2]"), "" for the root.
	Path string `json:"path"`
	// Label is the @defer or @stream label; ID the newer format's id.
	Label string `json:"label"`
	ID    string `json:"id"`
	// Items counts the streamed items; Errors the errors that came with
	// it.
	Items  int `json:"items"`
	Errors int `json:"errors"`
	// Problem says why it could not be merged, else "".
	Problem string `json:"problem"`
}

func toIncrementalDTO(s incremental.Snapshot, receiving bool) *IncrementalDTO {
	out := &IncrementalDTO{
		Format:         s.Format,
		Receiving:      receiving,
		Parts:          make([]IncrementalPartDTO, len(s.Parts)),
		PartCount:      s.Count,
		Merged:         s.Merged,
		MergedTooLarge: s.MergedTooLarge,
		MergedSize:     s.MergedSize,
		HasNext:        s.HasNext,
		Completed:      s.Completed,
		Pending:        s.Pending,
		Errors:         s.Errors,
	}
	for i, p := range s.Parts {
		d := IncrementalPartDTO{
			Index:   p.Index,
			AtMs:    float64(p.At.Microseconds()) / 1000,
			Text:    p.Text,
			Clipped: p.Clipped,
			Size:    p.Size,
			Entries: make([]IncrementalEntryDTO, len(p.Entries)),
			HasNext: p.HasNext,
			Problem: p.Problem,
		}
		for j, e := range p.Entries {
			d.Entries[j] = IncrementalEntryDTO{Kind: e.Kind, Path: e.Path, Label: e.Label, ID: e.ID, Items: e.Items, Errors: e.Errors, Problem: e.Problem}
		}
		out.Parts[i] = d
	}
	return out
}

// contentType returns the Content-Type header of headers, or "".
func contentType(headers []httpclient.Header) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			return h.Value
		}
	}
	return ""
}

// partialResponseDTO is the response shown while an incremental body
// arrives: the head, the time and bytes so far, and the payloads, with m
// masking the URLs as the final response does. The body, cookies,
// timeline and tests come with the final response.
func partialResponseDTO(b request.HTTPBuild, head *httpclient.Response, elapsed time.Duration, received int64, inc *IncrementalDTO, m httpclient.Masker) ResponseDTO {
	out := ResponseDTO{
		Method:            b.Request.Method,
		URL:               m.MaskText(b.Request.URL),
		FinalURL:          m.MaskText(head.URL),
		Proto:             head.Proto,
		Protocol:          protocolLabel(head.Proto),
		StatusCode:        head.StatusCode,
		Status:            head.Status,
		Headers:           make([]HeaderDTO, 0, len(head.Headers)),
		Cookies:           []CookieDTO{},
		ContentType:       contentType(head.Headers),
		Size:              received,
		DurationMs:        elapsed.Milliseconds(),
		TimeoutMs:         b.Request.Settings.Timeout.Milliseconds(),
		TimeoutSource:     b.TimeoutSource,
		InsecureTLS:       b.InsecureSkipVerify,
		InsecureTLSSource: b.TLSSource,
		RequestProxy:      b.Proxy.String(),
		Warnings:          []WarningDTO{},
		Timeline:          TimelineDTO{Hops: []TimelineHopDTO{}, Events: []TimelineEventDTO{}},
		Tests:             []TestResultDTO{},
		Extractions:       []ExtractionResultDTO{},
		Incremental:       inc,
	}
	out.HeaderSize = int64(len("HTTP/1.1 ") + len(head.Status) + len("\r\n\r\n"))
	for _, h := range head.Headers {
		out.Headers = append(out.Headers, HeaderDTO{Name: h.Name, Value: h.Value})
		out.HeaderSize += int64(len(h.Name) + len(": ") + len(h.Value) + len("\r\n"))
	}
	return out
}

// throttle calls fn when poked, at most once per interval: a poke inside
// the interval is delivered at its end. After stop returns, fn is never
// called again.
type throttle struct {
	interval time.Duration
	fn       func()

	mu      sync.Mutex
	last    time.Time
	timer   *time.Timer
	pending bool
	stopped bool
}

func (t *throttle) poke() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.pending {
		return
	}
	wait := t.interval - time.Since(t.last)
	if wait <= 0 {
		t.last = time.Now()
		t.fn()
		return
	}
	t.pending = true
	t.timer = time.AfterFunc(wait, t.fire)
}

func (t *throttle) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = false
	if t.stopped {
		return
	}
	t.last = time.Now()
	t.fn()
}

// stop cancels a delivery that waits; fn runs under the lock, so none is
// running once stop returns.
func (t *throttle) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
	}
}
