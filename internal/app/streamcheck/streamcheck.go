// Package streamcheck checks the assertions of stream requests (TEST-08):
// gRPC calls, WebSocket and Socket.IO connections and SSE streams. The
// services record the messages they receive in a Log, and, when the
// stream ends or the user asks, evaluate the request's assertions against
// it with internal/core/assert (EvaluateStream). Results have the shape
// of the HTTP service's test results, so the UI shows them alike. See
// docs/assertions.md#streams.
package streamcheck

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TestResultDTO is the result of one of a stream request's assertions, as
// the stream view's Tests tab shows it. It has the members of the HTTP
// service's TestResultDTO, plus the assertion's message scope.
type TestResultDTO struct {
	// Index is the assertion's position in the request's assertions,
	// disabled ones counted, so the UI can jump to its row.
	Index int `json:"index"`
	// Target, Expr, Op, Messages and MessageIndex are the assertion's;
	// Expr and Expected have their {{variables}} rendered, secret values
	// masked.
	Target       string `json:"target"`
	Expr         string `json:"expr"`
	Op           string `json:"op"`
	Expected     string `json:"expected"`
	Messages     string `json:"messages"`
	MessageIndex int    `json:"messageIndex"`
	// Outcome is "passed", "failed" or "invalid".
	Outcome string `json:"outcome"`
	// Actual is the value as shown; "" when it does not exist or the
	// assertion is invalid.
	Actual string `json:"actual"`
	// Exists is false when the target is not there.
	Exists bool `json:"exists"`
	// Message is a sentence with the expected and the actual value:
	// "Expected the last message's JSONPath $.done to equal true, but it
	// was false."
	Message string `json:"message"`
	// Code is the error code of an invalid result (assert.*, template.*).
	Code string `json:"code"`
	// SchemaErrors are the schema errors of a failed matches_schema
	// assertion; empty otherwise.
	SchemaErrors []SchemaErrorDTO `json:"schemaErrors"`
}

// SchemaErrorDTO is one way a value does not match a JSON Schema.
type SchemaErrorDTO struct {
	Path         string `json:"path"`
	InstancePath string `json:"instancePath"`
	SchemaPath   string `json:"schemaPath"`
	Keyword      string `json:"keyword"`
	Message      string `json:"message"`
}

// Item is an enabled assertion of a request, rendered: the assertion to
// evaluate, or Preset, its result when rendering failed.
type Item struct {
	// Index is its position in the request's list, disabled ones counted.
	Index     int
	Assertion assert.Assertion
	Preset    *assert.Result
}

// Checks are a request's assertions, ready to be evaluated.
type Checks struct {
	Items []Item
	// Secrets are the secret values the assertions' {{variables}} used,
	// masked in the results.
	Secrets []string
	// LoadSchema reads the schema files of matches_schema assertions; nil:
	// none can be read.
	LoadSchema func(string) ([]byte, error)
}

// Empty reports whether there is nothing to check.
func (c Checks) Empty() bool { return len(c.Items) == 0 }

// Loader returns the checks of request requestID, its assertions as they
// are now (pending edits included) rendered with its variables. The app
// builds it from the collection and the HTTP service (http.StreamChecks).
type Loader func(ctx context.Context, requestID string) (Checks, error)

// Prepare renders the {{variables}} of the enabled assertions of list
// with r. An undefined variable, or another template problem, makes that
// assertion invalid (code template.unresolved…), not the others.
func Prepare(r template.Resolver, list []projectfile.Assertion, loadSchema func(string) ([]byte, error)) Checks {
	out := Checks{LoadSchema: loadSchema}
	for i, pa := range list {
		if pa.Disabled {
			continue
		}
		a := assert.Assertion{
			Target: assert.TargetKind(pa.Target), Op: assert.Operator(pa.Op),
			Messages: assert.MessageScope(pa.Messages), Index: pa.Index,
		}
		var fail error
		render := func(text string) string {
			if fail != nil || text == "" {
				return text
			}
			res := r.Render(text)
			collectSecrets(&out.Secrets, res.Refs)
			if err := res.Err(); err != nil {
				fail = err
				return text
			}
			return res.Text
		}
		a.Expr = render(pa.Expr)
		if a.Op != assert.OpExists && a.Op != assert.OpNotExists {
			a.Expected = render(pa.Expected)
		} else {
			a.Expected = pa.Expected
		}
		it := Item{Index: i, Assertion: a}
		if fail != nil {
			res := assert.Result{Assertion: a, Outcome: assert.Invalid, Message: "This assertion cannot be checked: " + fail.Error(), Code: template.CodeUnresolved}
			if e, ok := errors.AsType[*errs.Error](fail); ok {
				res.Message, res.Code = "This assertion cannot be checked. "+e.Message, e.Code
			}
			it.Preset = &res
		}
		out.Items = append(out.Items, it)
	}
	return out
}

func collectSecrets(out *[]string, refs []template.Ref) {
	for _, r := range refs {
		if r.Secret && r.Resolved && r.Value != "" {
			*out = append(*out, r.Value)
		}
		collectSecrets(out, r.Nested)
	}
}

// Failed is the result of every check when they could not be loaded:
// one invalid result with err's message, so the UI shows why.
func Failed(err error) []TestResultDTO {
	dto := TestResultDTO{Outcome: string(assert.Invalid), Message: "The assertions could not be loaded: " + err.Error(), SchemaErrors: []SchemaErrorDTO{}}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		dto.Message, dto.Code = "The assertions could not be loaded. "+e.Message, e.Code
	}
	return []TestResultDTO{dto}
}

// Run evaluates the checks against s and returns their results, never
// nil. The secret values of the checks are masked, and so is whatever
// mask (may be nil) masks: the stream's own secrets.
func (c Checks) Run(ctx context.Context, s assert.Stream, mask func(string) string) []TestResultDTO {
	out := make([]TestResultDTO, 0, len(c.Items))
	if len(c.Items) == 0 {
		return out
	}
	sm := httpclient.NewSecretMasker(c.Secrets)
	m := func(text string) string {
		text = sm.MaskText(text)
		if mask != nil {
			text = mask(text)
		}
		return text
	}
	var list []assert.Assertion
	for _, it := range c.Items {
		if it.Preset == nil {
			list = append(list, it.Assertion)
		}
	}
	evaluated := assert.EvaluateStream(ctx, s, list, assert.Options{LoadSchema: c.LoadSchema})
	for _, it := range c.Items {
		var res assert.Result
		if it.Preset != nil {
			res = *it.Preset
		} else {
			res, evaluated = evaluated[0], evaluated[1:]
		}
		schemaErrors := make([]SchemaErrorDTO, len(res.SchemaErrors))
		for i, e := range res.SchemaErrors {
			schemaErrors[i] = SchemaErrorDTO{
				Path: m(e.Path), InstancePath: m(e.InstancePath), SchemaPath: e.SchemaPath, Keyword: e.Keyword, Message: m(e.Message),
			}
		}
		out = append(out, TestResultDTO{
			Index:        it.Index,
			Target:       string(it.Assertion.Target),
			Expr:         m(it.Assertion.Expr),
			Op:           string(it.Assertion.Op),
			Expected:     m(it.Assertion.Expected),
			Messages:     string(it.Assertion.Messages),
			MessageIndex: it.Assertion.Index,
			Outcome:      string(res.Outcome),
			Actual:       m(res.Actual),
			Exists:       res.Exists,
			Message:      m(res.Message),
			Code:         res.Code,
			SchemaErrors: schemaErrors,
		})
	}
	return out
}

// Limits of a Log.
const (
	// MaxMessages is how many messages a Log keeps from the start; the
	// count and the last message go on.
	MaxMessages = 10000
	// MaxBytes bounds the bodies kept.
	MaxBytes = 32 << 20
)

// Log records the messages a stream receives, for its checks: the first
// MaxMessages (and at most MaxBytes of bodies), the count of all and the
// last one. It is safe for concurrent use.
type Log struct {
	mu    sync.Mutex
	start time.Time
	kept  []assert.Message
	bytes int
	full  bool
	total int
	last  *assert.Message
	// resp is the stream as a whole, set by the service.
	resp assert.Response
}

// NewLog returns an empty log of a stream that started at start.
func NewLog(start time.Time) *Log { return &Log{start: start} }

// Add records a message received at at. body is copied.
func (l *Log) Add(body []byte, header http.Header, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := assert.Message{Body: append([]byte(nil), body...), Header: header, Time: at.Sub(l.start)}
	l.total++
	if !l.full && len(l.kept) < MaxMessages && l.bytes+len(body) <= MaxBytes {
		l.kept = append(l.kept, m)
		l.bytes += len(body)
		l.last = nil
		return
	}
	l.full = true
	l.last = &m
}

// SetResponse sets what assertions without a message scope read: the
// status, headers and time of the stream as a whole. A zero Time is the
// time since the start, when Stream is called.
func (l *Log) SetResponse(r assert.Response) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resp = r
}

// SetTime sets the time of the stream as a whole (its duration, once it
// has ended), keeping the rest of its response.
func (l *Log) SetTime(d time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resp.Time = d
}

// Count is the number of messages received.
func (l *Log) Count() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// Stream is the stream so far, for assert.EvaluateStream.
func (l *Log) Stream() assert.Stream {
	if l == nil {
		return assert.Stream{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	resp := l.resp
	if resp.Time == 0 {
		resp.Time = time.Since(l.start)
	}
	s := assert.Stream{Response: resp, Messages: l.kept[:len(l.kept):len(l.kept)], Total: l.total}
	if l.last != nil {
		last := *l.last
		s.Last = &last
	}
	return s
}

// Recent keeps the logs of the last streams that ended, by their ID
// (an operation or connection ID), so their checks can run again after
// the end. It is safe for concurrent use.
type Recent[V any] struct {
	mu    sync.Mutex
	order []string
	byID  map[string]V
}

// MaxRecent is how many ended streams Recent keeps.
const MaxRecent = 16

// Put keeps v as id's, dropping the oldest beyond MaxRecent.
func (r *Recent[V]) Put(id string, v V) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[string]V{}
	}
	if _, ok := r.byID[id]; !ok {
		r.order = append(r.order, id)
	}
	r.byID[id] = v
	for len(r.order) > MaxRecent {
		delete(r.byID, r.order[0])
		r.order = r.order[1:]
	}
}

// Get returns id's value.
func (r *Recent[V]) Get(id string) (V, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.byID[id]
	return v, ok
}

// CodeNotFound: CheckAssertions names a stream that is neither running nor
// among the last ones that ended (kind not_found). Details: id.
const CodeNotFound = "streamcheck.not_found"

// NotFound is the CodeNotFound error for id.
func NotFound(id string) error {
	return errs.New(errs.NotFound, "That stream is no longer kept: run it again to check its messages.").
		WithCode(CodeNotFound).WithDetail("id", id)
}

// CodeUnavailable: the service was built without a Loader (kind
// unsupported).
const CodeUnavailable = "streamcheck.unavailable"

// Unavailable is the CodeUnavailable error.
func Unavailable() error {
	return errs.New(errs.Unsupported, "Checking the assertions of streams is not available.").WithCode(CodeUnavailable)
}
