// Package runner is the engine side of the folder runner (RUN-01 to
// RUN-04): the plan of a run (the requests of a folder in order, with the
// ones a run cannot send marked as skipped), the selection and order of
// the requests a run sends, the steps of a run over its iterations, the
// delay between the requests it sends, stopping at the first failure and
// the run's own variables. It sends nothing itself: the app layer walks the steps and
// sends each request through the normal send pipeline
// (internal/app/services/runner). See docs/runner.md.
package runner

import (
	"context"
	"iter"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeInvalidOptions: the iterations, the delay or the stream window of a
// run are out of range (kind invalid_argument). Details: field.
const CodeInvalidOptions = "runner.invalid_options"

// CodeInvalidSelection: the requests chosen for a run name one that is
// not in its plan, or one twice (kind invalid_argument). Details:
// requestId, reason ("unknown" or "duplicate").
const CodeInvalidSelection = "runner.invalid_selection"

// Limits of a run.
const (
	// MaxIterations is the most iterations a run makes.
	MaxIterations = 10_000
	// MaxDelay is the longest delay between two requests.
	MaxDelay = time.Hour
	// MaxStreamWindow is the longest time an SSE request of a run is
	// read for.
	MaxStreamWindow = 10 * time.Minute
	// DefaultStreamWindow is how long an SSE request of a run is read for
	// when the run does not say.
	DefaultStreamWindow = 5 * time.Second
)

// Skip says why an item of a plan is not sent; "" means it is.
type Skip string

const (
	// Runnable: the item is sent.
	Runnable Skip = ""
	// SkipKind: a request of a kind a run does not send (WebSocket,
	// Socket.IO, gRPC, TCP): a connection, not a request and a response.
	SkipKind Skip = "kind"
	// SkipSubscription: a GraphQL request whose operation is a
	// subscription, or that runs over WebSocket.
	SkipSubscription Skip = "subscription"
	// SkipUnknown: a file of a kind this version does not know.
	SkipUnknown Skip = "unknown"
	// SkipInvalid: a file that could not be loaded.
	SkipInvalid Skip = "invalid"
)

// Item is one request of a plan.
type Item struct {
	// ID is the request's ID in the project.
	ID string
	// Name is the request's display name.
	Name string
	// Kind is the request file's kind ("http", "graphql", "sse",
	// "websocket", …); "" when it could not be read.
	Kind string
	// Method is an HTTP request's method, "" for other kinds.
	Method string
	// Path names the folders between the run's folder and the request,
	// outermost first; empty for a request directly in it.
	Path []string
	// Skip says why the item is not sent; Runnable when it is.
	Skip Skip
}

// Plan is what a run sends: its items in the order they are sent, the
// same in every iteration.
type Plan struct {
	Items []Item
	// Folder is the display name of the folder run, "" for the whole
	// project; Project is the project's. They name the run in its
	// record (RUN-09).
	Folder  string
	Project string
}

// Runnable is the number of items that are sent in each iteration.
func (p Plan) Runnable() int {
	n := 0
	for _, it := range p.Items {
		if it.Skip == Runnable {
			n++
		}
	}
	return n
}

// Select returns the plan of the items ids names, in the order of ids
// (RUN-02): the requests a run sends and the order it sends them in,
// whatever the folder's order. p itself is not changed. Items not named
// are left out; a skipped item that is named stays, and is listed as
// skipped. It fails with CodeInvalidSelection when an ID is not an item
// of p or is named twice. No IDs give an empty plan.
func (p Plan) Select(ids []string) (Plan, error) {
	byID := make(map[string]int, len(p.Items))
	for i, it := range p.Items {
		if _, seen := byID[it.ID]; !seen {
			byID[it.ID] = i
		}
	}
	invalid := func(id, reason, msg string) error {
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidSelection).
			WithHint("Load the folder's requests again and choose them again.").
			WithDetail("requestId", id).
			WithDetail("reason", reason)
	}
	out := Plan{Items: make([]Item, 0, len(ids)), Folder: p.Folder, Project: p.Project}
	chosen := make(map[string]bool, len(ids))
	for _, id := range ids {
		i, ok := byID[id]
		if !ok {
			return Plan{}, invalid(id, "unknown", "A request chosen for the run is not in the folder any more.")
		}
		if chosen[id] {
			return Plan{}, invalid(id, "duplicate", "A request was chosen twice for the run.")
		}
		chosen[id] = true
		out.Items = append(out.Items, p.Items[i])
	}
	return out, nil
}

// Step is one item of one iteration.
type Step struct {
	// Iteration counts from 1.
	Iteration int
	// Position is the item's place in the plan, from 1: in the order of
	// the run after Select.
	Position int
	// Index counts the steps of the run from 1: (Iteration-1) × items +
	// Position.
	Index int
	Item  Item
}

// Steps returns the steps of a run of iterations iterations over p: every
// item of the plan in order, then again for the next iteration.
func (p Plan) Steps(iterations int) iter.Seq[Step] {
	return func(yield func(Step) bool) {
		index := 0
		for i := 1; i <= iterations; i++ {
			for pos, it := range p.Items {
				index++
				if !yield(Step{Iteration: i, Position: pos + 1, Index: index, Item: it}) {
					return
				}
			}
		}
	}
}

// Options are how a run goes.
type Options struct {
	// Iterations is how many times the plan is run, 1 to MaxIterations.
	Iterations int
	// Delay is the wait between two requests the run sends, from the end
	// of one to the start of the next, 0 to MaxDelay. Skipped items are
	// not waited for.
	Delay time.Duration
	// StreamWindow is how long an SSE request is read for, at most; 0
	// means DefaultStreamWindow.
	StreamWindow time.Duration
	// StopOnFailure ends the run after the first result that failed (an
	// assertion failed or could not be evaluated) or is an error (the
	// request could not be built or sent): bail (RUN-04). Skipped items
	// never stop it. Without it the run goes on.
	StopOnFailure bool
}

// Stops reports whether a run with o ends after a result; failed is set
// when the result failed or is an error.
func (o Options) Stops(failed bool) bool { return o.StopOnFailure && failed }

// Validate checks the options against the limits, with CodeInvalidOptions
// and the field in the details.
func (o Options) Validate() error {
	invalid := func(field, msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidOptions).WithDetail("field", field)
	}
	switch {
	case o.Iterations < 1 || o.Iterations > MaxIterations:
		return invalid("iterations", "The number of iterations must be between 1 and "+strconv.Itoa(MaxIterations)+".")
	case o.Delay < 0 || o.Delay > MaxDelay:
		return invalid("delayMs", "The delay must be between 0 and 1 hour.")
	case o.StreamWindow < 0 || o.StreamWindow > MaxStreamWindow:
		return invalid("streamMs", "The time an event stream is read for must be between 0 and 10 minutes.")
	}
	return nil
}

// Window is the StreamWindow to use: DefaultStreamWindow for 0.
func (o Options) Window() time.Duration {
	if o.StreamWindow <= 0 {
		return DefaultStreamWindow
	}
	return o.StreamWindow
}

// SleepFunc waits d, or until ctx is done, whose error it then returns.
type SleepFunc func(ctx context.Context, d time.Duration) error

// Sleep is the SleepFunc of a real timer.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Pacer keeps the delay between the requests of a run: Wait before each
// request returns at once the first time and waits the delay every other
// time.
type Pacer struct {
	delay time.Duration
	sleep SleepFunc
	sent  bool
}

// NewPacer returns a Pacer that waits delay with sleep (nil: Sleep).
func NewPacer(delay time.Duration, sleep SleepFunc) *Pacer {
	if sleep == nil {
		sleep = Sleep
	}
	return &Pacer{delay: delay, sleep: sleep}
}

// Waits reports whether the next Wait waits.
func (p *Pacer) Waits() bool { return p.sent && p.delay > 0 }

// Wait is called just before a request goes out. It returns ctx's error
// when the run was stopped while it waited (or before).
func (p *Pacer) Wait(ctx context.Context) error {
	if !p.Waits() {
		p.sent = true
		return ctx.Err()
	}
	return p.sleep(ctx, p.delay)
}
