package sse

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// result is everything a parse run produces.
type result struct {
	Events    []Event
	Comments  []Comment
	Fields    []Field
	LastID    string
	Retry     time.Duration
	HasRetry  bool
	Discarded bool
	ErrCode   string
	Offset    int64
}

// run feeds chunks to a new parser, then closes it.
func run(opts Options, chunks [][]byte) result {
	var r result
	opts.OnComment = func(c Comment) { r.Comments = append(r.Comments, c) }
	opts.OnField = func(f Field) { r.Fields = append(r.Fields, f) }
	p := NewParser(opts)
	for _, c := range chunks {
		evs, err := p.Feed(c)
		r.Events = append(r.Events, evs...)
		if err != nil {
			if e, ok := errors.AsType[*errs.Error](err); ok {
				r.ErrCode = e.Code
			} else {
				r.ErrCode = "?" + err.Error()
			}
			break
		}
	}
	r.LastID = p.LastEventID()
	r.Retry, r.HasRetry = p.Retry()
	r.Offset = p.Offset()
	r.Discarded = p.Close()
	return r
}

func runWhole(opts Options, s string) result {
	return run(opts, [][]byte{[]byte(s)})
}

func bytewise(s string) [][]byte {
	out := make([][]byte, len(s))
	for i := range len(s) {
		out[i] = []byte{s[i]}
	}
	return out
}

// randomChunks splits s at random points, including empty chunks.
func randomChunks(rng *rand.Rand, s string) [][]byte {
	var out [][]byte
	for len(s) > 0 {
		n := rng.IntN(min(len(s), 8) + 1)
		if rng.IntN(10) == 0 {
			n = rng.IntN(len(s) + 1)
		}
		out = append(out, []byte(s[:n]))
		s = s[n:]
	}
	return out
}

// sameResult reports the first difference between two results, ignoring
// the Offset when the run stopped at an error (chunked runs stop earlier or
// later in the stream).
func sameResult(a, b result) string {
	if a.ErrCode != "" {
		a.Offset, b.Offset = 0, 0
		a.Discarded, b.Discarded = false, false
	}
	if reflect.DeepEqual(a, b) {
		return ""
	}
	return fmt.Sprintf("whole:\n%s\nchunked:\n%s", dump(a), dump(b))
}

func dump(r result) string {
	var sb strings.Builder
	for _, e := range r.Events {
		retry := "-"
		if e.Retry != nil {
			retry = e.Retry.String()
		}
		fmt.Fprintf(&sb, "  event type=%q data=%q id=%q retry=%s start=%v end=%v\n", e.Type, e.Data, e.ID, retry, e.Start, e.End)
	}
	for _, c := range r.Comments {
		fmt.Fprintf(&sb, "  comment %q %v\n", c.Text, c.Pos)
	}
	for _, f := range r.Fields {
		fmt.Fprintf(&sb, "  field %q=%q %v ignored=%q\n", f.Name, f.Value, f.Pos, f.Ignored)
	}
	fmt.Fprintf(&sb, "  lastID=%q retry=%v/%v discarded=%v err=%q offset=%d", r.LastID, r.Retry, r.HasRetry, r.Discarded, r.ErrCode, r.Offset)
	return sb.String()
}

// checkChunking parses s whole, byte by byte and in n random splits and
// fails when any result differs.
func checkChunking(t *testing.T, opts Options, s string, n int, seed uint64) result {
	t.Helper()
	whole := runWhole(opts, s)
	if d := sameResult(whole, run(opts, bytewise(s))); d != "" {
		t.Fatalf("byte by byte differs for %q\n%s", s, d)
	}
	rng := rand.New(rand.NewPCG(seed, 0x5e5e))
	for range n {
		chunks := randomChunks(rng, s)
		if d := sameResult(whole, run(opts, chunks)); d != "" {
			t.Fatalf("chunks %q differ\n%s", chunks, d)
		}
	}
	return whole
}

func dur(d time.Duration) *time.Duration { return &d }

// ev is a compact expected event without positions.
type ev struct {
	Type, Data, ID string
	Retry          *time.Duration
}

func stripEvents(evs []Event) []ev {
	out := make([]ev, 0, len(evs))
	for _, e := range evs {
		out = append(out, ev{e.Type, e.Data, e.ID, e.Retry})
	}
	return out
}
