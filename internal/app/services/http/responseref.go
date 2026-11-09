package http

import (
	"context"
	"encoding/base64"
	"errors"
	nethttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/extract"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Response references (DYN-06): {{$response REQUEST SOURCE EXPR
// [send=WHEN] [secret]}} is a value from another request's last response.
// The last response of every request sent this session is kept in memory
// as received (lastResponses); after a restart the request history is
// read instead, as it was recorded (masked, the body cut at 64 KiB).
// A reference that asks for it (send=missing, send=10m, send=always)
// sends the referenced request first, inside the send's operation, with
// a guard against requests that refer to each other in a loop. See
// docs/templates.md#response-references.

// Error codes of response references, besides the extraction codes of a
// value that is not in the response (extract.not_found,
// extract.unreadable_body, jsonpath.invalid_query...).
const (
	// CodeResponseRequestNotFound: the referenced request is not an HTTP
	// or GraphQL request of the open project. Details: requestId.
	CodeResponseRequestNotFound = "http.response_request_not_found"
	// CodeResponseNotSent: the referenced request has no response, and
	// the reference does not send it (or this send cannot). Details:
	// requestId.
	CodeResponseNotSent = "http.response_not_sent"
	// CodeResponseCycle: sending the referenced request first would send
	// the request being built again: they refer to each other's
	// responses in a loop. Details: requests (their names, joined with " → ").
	CodeResponseCycle = "http.response_cycle"
	// CodeResponseTooDeep: more than MaxResponseDepth requests would
	// have to be sent first, one for the other. Details: requests.
	CodeResponseTooDeep = "http.response_too_deep"
	// CodeResponseSendFailed: the referenced request was sent first and
	// failed (it could not be built, or got no response). Details:
	// requestId, cause (the send's code).
	CodeResponseSendFailed = "http.response_send_failed"
	// CodeResponseMasked: the value is masked in the history's copy of
	// the response (it holds a secret). Details: requestId.
	CodeResponseMasked = "http.response_masked"
	// CodeResponseBodyUnavailable: the body is not kept whole (over
	// MaxReferencedBody, or cut in the history). Details: requestId.
	CodeResponseBodyUnavailable = "http.response_body_unavailable"
)

// Limits of response references.
const (
	// MaxResponseDepth is how many requests may be sent first, one for
	// the other, for one send: A needs B's response, B needs C's...
	MaxResponseDepth = 5
	// MaxReferencedBody is the largest body kept in memory for
	// references; the status and headers of a larger one are kept.
	MaxReferencedBody = 4 << 20
	// maxKeptResponseBytes bounds the bodies kept in memory in all; the
	// oldest responses go first.
	maxKeptResponseBytes = 32 << 20
)

// StageSendingFirst is the progress stage while a referenced request is
// sent before the request itself.
const StageSendingFirst = "Sending a referenced request first"

// ResponseHistory opens the request history of the open project, which
// references read when no response was kept this session; nil without
// one.
type ResponseHistory func() (*history.Store, error)

// WithResponseHistory makes response references read the request history
// when a request was not sent this session. Without it they only see the
// responses of this session.
func WithResponseHistory(open ResponseHistory) Option {
	return func(s *HTTPService) { s.responseHistory = open }
}

// ForgetResponses drops the responses kept for references of request id,
// or of every request when id is "": the app calls it when the history
// of the request (or the whole history) is cleared, so a cleared response
// is not read any more. It is a function, not a method, so it is not
// bound.
func ForgetResponses(s *HTTPService, id string) {
	if s != nil {
		s.lastResponses.forget(id)
	}
}

// keptResponse is what a reference reads of a request's last response.
type keptResponse struct {
	// at is when it arrived, by the service's clock (the age send=10m
	// compares); recorded is the wall clock then, which the history's
	// times are compared with.
	at, recorded time.Time
	status       int
	// statusText is "200 OK".
	statusText string
	header     nethttp.Header
	body       []byte
	// bodyProblem says why body is not the whole body ("" when it is).
	bodyProblem string
	// secrets are the values the send masked: a value holding one is
	// secret.
	secrets []string
	// fromHistory is set for a response read from the history, which was
	// masked when it was recorded.
	fromHistory bool
}

type keptKey struct{ root, id string }

// lastResponses keeps the last response of each request sent this
// session.
type lastResponses struct {
	mu    sync.Mutex
	m     map[keptKey]*keptResponse
	bytes int
}

func (c *lastResponses) get(root, id string) *keptResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[keptKey{root, id}]
}

func (c *lastResponses) put(root, id string, r *keptResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[keptKey]*keptResponse{}
	}
	k := keptKey{root, id}
	if old, ok := c.m[k]; ok {
		c.bytes -= len(old.body)
	}
	c.m[k] = r
	c.bytes += len(r.body)
	for c.bytes > maxKeptResponseBytes && len(c.m) > 1 {
		var oldest keptKey
		var at time.Time
		for key, v := range c.m {
			if key != k && (at.IsZero() || v.at.Before(at)) {
				oldest, at = key, v.at
			}
		}
		c.bytes -= len(c.m[oldest].body)
		delete(c.m, oldest)
	}
}

func (c *lastResponses) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if id == "" || k.id == id {
			c.bytes -= len(v.body)
			delete(c.m, k)
		}
	}
}

// rememberResponse keeps r, the response of the send, for references
// (exchange calls it once the extraction rules ran, so the secrets they
// set are known).
func (x *send) rememberResponse(r *httpclient.Response, sp *spool) {
	if x.svc == nil || x.meta.requestID == "" {
		return
	}
	// A large body is not read back from its spool file: only its size
	// is needed to say it is not kept.
	size := int64(len(r.Body))
	if sp != nil {
		size = max(size, sp.n)
	}
	in, problem := extractInput(r, sp, size <= MaxReferencedBody)
	kept := &keptResponse{at: x.svc.clock(), recorded: time.Now(), status: r.StatusCode, statusText: r.Status, header: in.Header,
		secrets: slices.Clone(x.secrets)}
	switch {
	case size > MaxReferencedBody:
		kept.bodyProblem = "The body is " + strconv.FormatInt(size, 10) + " bytes, more than the " + strconv.Itoa(MaxReferencedBody>>20) + " MiB kept for references."
	case problem != "":
		kept.bodyProblem = problem
	default:
		kept.body = slices.Clone(in.Body)
	}
	if r.BodyTruncated && kept.bodyProblem == "" {
		kept.bodyProblem = "The body was cut at the response size limit."
	}
	x.svc.lastResponses.put(x.root, x.meta.requestID, kept)
}

// last returns the last response of request id of the project at root:
// the one kept this session, or the history's newest with a response
// when that is newer; nil when there is none.
func (s *HTTPService) lastResponse(root, id string) *keptResponse {
	mem := s.lastResponses.get(root, id)
	if s.responseHistory == nil {
		return mem
	}
	st, err := s.responseHistory()
	if err != nil || st == nil {
		return mem
	}
	page := st.List(history.Query{RequestID: id, Limit: history.MaxPageSize})
	for _, sum := range page.Entries {
		if sum.Outcome != history.OutcomeOK || sum.StatusCode == 0 {
			continue
		}
		at := sum.Time.Add(time.Duration(sum.DurationMs) * time.Millisecond)
		if mem != nil && !mem.recorded.Before(sum.Time) {
			return mem
		}
		e, err := st.Get(sum.ID)
		if err != nil || e.Response == nil {
			return mem
		}
		return historyResponse(e, at)
	}
	return mem
}

// historyResponse is a history entry's response, as references read it.
func historyResponse(e history.Entry, at time.Time) *keptResponse {
	r := e.Response
	k := &keptResponse{at: at, status: r.StatusCode, statusText: r.Status, header: nethttp.Header{}, fromHistory: true}
	for _, h := range r.Headers {
		k.header.Add(h.Name, h.Value)
	}
	body := []byte(r.Body.Text)
	if r.Body.Base64 {
		var err error
		if body, err = base64.StdEncoding.DecodeString(r.Body.Text); err != nil {
			body = nil
		}
	}
	switch {
	case e.Trimmed:
		k.bodyProblem = "The history did not keep the body."
	case r.Body.Clipped || r.Truncated:
		k.bodyProblem = "The history keeps only the first " + strconv.Itoa(history.MaxBody>>10) + " KiB of the body."
	default:
		k.body = body
	}
	return k
}

// responseMode says what a build's response references may do.
type responseMode int

const (
	// responsesStored reads the stored responses and never sends: the
	// previews, streams and subscriptions, a token request.
	responsesStored responseMode = iota
	// responsesPlan is a send's first build, before its operation
	// starts: a reference that needs its request sent first notes it
	// (responseFlow.pending) and gets the stored value, or "".
	responsesPlan
	// responsesSend sends what needs to be sent first, inside the send's
	// operation.
	responsesSend
)

// responseFlow is what the response references of one send share,
// through every request sent first for it.
type responseFlow struct {
	mode responseMode
	// stage reports progress while a request is sent first; nil for
	// none.
	stage func(msg string)

	mu sync.Mutex
	// sent are the requests sent first during this send, with the
	// error of their send: each is sent once, however many references
	// read it.
	sent map[string]error
	// pending are the requests a plan build found to send first.
	pending []string
}

func (f *responseFlow) needsSending() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending) > 0
}

// responseReader is the template.ResponseReader of one build.
type responseReader struct {
	s    *HTTPService
	ctx  context.Context
	root string
	// chain are the requests being built, outermost first, the one this
	// reader builds last; names are their names.
	chain, names []string
	flow         *responseFlow
	// preview leaves a reference to a request without a response as
	// written (errResponseDeferred) instead of failing.
	preview bool
	// now is the time staleness is measured at.
	now time.Time
}

// errResponseDeferred is the cause of a reference a preview leaves as
// written: there is no response to read yet.
var errResponseDeferred = errors.New("no response yet")

// responsesDeferred reports whether p is a response reference a preview
// leaves as written.
func responsesDeferred(p template.Problem) bool {
	return p.Kind == template.ProblemResponse && errors.Is(p.Cause, errResponseDeferred)
}

// responseReaderFor returns the reader of request src's build.
func (s *HTTPService) responseReaderFor(ctx context.Context, src collection.HTTPSource, id string, o buildOptions) *responseReader {
	flow := o.responses
	if flow == nil {
		flow = &responseFlow{mode: responsesStored}
	}
	return &responseReader{s: s, ctx: ctx, root: workspaceRoot(src), chain: append(slices.Clone(o.responseChain), id),
		names: append(slices.Clone(o.responseNames), requestNameOf(src)), flow: flow, preview: o.preview, now: s.clock()}
}

// requestNameOf is the name of the request src sends.
func requestNameOf(src collection.HTTPSource) string {
	if src.GraphQL != nil {
		return src.GraphQL.Name
	}
	return src.Request.Name
}

// ReadResponse reads ref from the referenced request's last response,
// sending the request first when ref asks for it and the build may.
func (r *responseReader) ReadResponse(ref template.ResponseRef) (template.ResponseValue, error) {
	id := ref.RequestID
	name, err := r.referenced(id)
	if err != nil {
		return template.ResponseValue{}, err
	}
	label := strconv.Quote(name)
	last := r.s.lastResponse(r.root, id)
	// A value masked in the history's copy is as good as none: sending
	// the request again is what gets it.
	var masked error
	if last != nil && last.fromHistory {
		if _, err := readKept(r.ctx, last, ref, label); err != nil && codeOf(err) == CodeResponseMasked {
			masked, last = err, nil
		}
	}
	if r.needsSending(ref, last) {
		switch r.flow.mode {
		case responsesSend:
			if err := r.sendFirst(id, name); err != nil {
				return template.ResponseValue{}, err
			}
			// The response just kept is the one to read.
			if last = r.s.lastResponses.get(r.root, id); last == nil {
				last = r.s.lastResponse(r.root, id)
			}
		case responsesPlan:
			if err := r.checkLoop(id, name); err != nil {
				return template.ResponseValue{}, err
			}
			r.flow.mu.Lock()
			if !slices.Contains(r.flow.pending, id) {
				r.flow.pending = append(r.flow.pending, id)
			}
			r.flow.mu.Unlock()
			// A stand-in: this build is not sent; the operation builds
			// again once the request was sent.
			if last != nil {
				if v, err := readKept(r.ctx, last, ref, label); err == nil {
					return v, nil
				}
			}
			return template.ResponseValue{Request: name}, nil
		}
	}
	if last == nil {
		if masked != nil {
			return template.ResponseValue{}, masked
		}
		return template.ResponseValue{}, r.notSent(ref, id, label)
	}
	v, err := readKept(r.ctx, last, ref, label)
	if err != nil {
		return template.ResponseValue{}, err
	}
	v.Request = name
	return v, nil
}

// referenced returns the name of request id, or why it cannot be read.
func (r *responseReader) referenced(id string) (string, error) {
	notFound := func(why string) error {
		return errs.New(errs.NotFound, "The referenced request "+strconv.Quote(id)+" "+why+".").
			WithCode(CodeResponseRequestNotFound).
			WithHint("Pick the request again: type {{$response and choose it from the list.").
			WithDetail("requestId", id)
	}
	if r.s.load == nil {
		return "", notFound("cannot be loaded")
	}
	src, err := r.s.load(r.ctx, id)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) && e.Kind != errs.NotFound && e.Code != collection.CodeNotHTTP {
			return "", err
		}
		return "", notFound("is not an HTTP or GraphQL request of this project")
	}
	if src.SSE {
		return "", notFound("is an SSE request, which has no response to read")
	}
	return requestNameOf(src), nil
}

// needsSending reports whether ref asks for its request to be sent
// before its response is read, given last (nil for none).
func (r *responseReader) needsSending(ref template.ResponseRef, last *keptResponse) bool {
	switch ref.Send {
	case template.SendAlways:
		return true
	case template.SendMissing:
		return last == nil
	case template.SendStale:
		return last == nil || r.now.Sub(last.at) > ref.MaxAge
	}
	return false
}

// sendDescription says when ref's request is sent first, for the
// resolution view.
func sendDescription(ref template.ResponseRef) string {
	switch ref.Send {
	case template.SendMissing:
		return "From the last response; the request is sent first when it has none"
	case template.SendStale:
		return "From the last response; the request is sent first when it has none or it is older than " + template.FormatAge(ref.MaxAge)
	case template.SendAlways:
		return "The request is sent first at every send, and the value read from its response"
	}
	return "From the last response; the request is never sent for it"
}

// notSent is the error of a reference to a request without a response.
func (r *responseReader) notSent(ref template.ResponseRef, id, label string) error {
	if r.preview {
		msg := "The request " + label + " has no response yet"
		if ref.Send != template.SendNever {
			msg += "; it is sent first when this request is sent"
		}
		return errs.Wrap(errResponseDeferred, errs.InvalidArgument, msg+".").WithCode(CodeResponseNotSent).WithDetail("requestId", id)
	}
	e := errs.New(errs.InvalidArgument, "The request "+label+" has no response yet.").
		WithCode(CodeResponseNotSent).
		WithDetail("requestId", id)
	if ref.Send == template.SendNever {
		return e.WithHint("Send " + label + " first, or add send=missing to the reference to send it first automatically.")
	}
	return e.WithHint("Requests are sent first only by HTTP and GraphQL sends, runs and repeats: send " + label + " first.")
}

// checkLoop returns the error for sending request id first when it is
// already being built (a loop), or when that would go too deep.
func (r *responseReader) checkLoop(id, name string) error {
	path := func() string {
		names := slices.Clone(r.names)
		return strings.Join(append(names, name), " → ")
	}
	if i := slices.Index(r.chain, id); i >= 0 {
		return errs.New(errs.InvalidArgument, "The requests refer to each other's responses in a loop: "+path()+".").
			WithCode(CodeResponseCycle).
			WithHint("Change send= on one of the references so that it reads the stored response instead of sending its request.").
			WithDetail("requests", path())
	}
	if len(r.chain) > MaxResponseDepth {
		return errs.New(errs.InvalidArgument, "Too many requests would be sent first, one for the other: "+path()+".").
			WithCode(CodeResponseTooDeep).
			WithHint("At most "+strconv.Itoa(MaxResponseDepth)+" requests are sent first for one send.").
			WithDetail("requests", path())
	}
	return nil
}

// sendFirst sends request id (named name) before the request being
// built, once per send: a request already sent first for this send is
// not sent again.
func (r *responseReader) sendFirst(id, name string) error {
	if err := r.checkLoop(id, name); err != nil {
		return err
	}
	f := r.flow
	f.mu.Lock()
	if err, done := f.sent[id]; done {
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	if f.stage != nil {
		f.stage(StageSendingFirst)
	}
	err := r.s.sendFirst(r.ctx, id, buildOptions{responses: f, responseChain: r.chain, responseNames: r.names})
	switch codeOf(err) {
	case CodeResponseSendFailed, CodeResponseCycle, CodeResponseTooDeep:
		// Already says which request, deeper down the chain.
	default:
		if err == nil {
			break
		}
		label := strconv.Quote(name)
		code := codeOf(err)
		e := errs.Wrap(err, errs.KindOf(err), "The request "+label+" was sent first to read its response, but "+lowerFirst(errMessage(err))).
			WithCode(CodeResponseSendFailed).
			WithHint("Send "+label+" by itself to see what is wrong.").
			WithDetail("requestId", id)
		if code != "" {
			e = e.WithDetail("cause", code)
		}
		err = e
	}
	f.mu.Lock()
	if f.sent == nil {
		f.sent = map[string]error{}
	}
	f.sent[id] = err
	f.mu.Unlock()
	return err
}

// sendFirst builds and sends request id with o, records it in the
// history and keeps its response for references; its response is not
// shown.
func (s *HTTPService) sendFirst(ctx context.Context, id string, o buildOptions) error {
	snd, err := s.prepare(ctx, id, o, s.historyRecord())
	if err != nil {
		return err
	}
	ex, err := snd.exchange(ctx, func(int64, int64, string) {}, nil)
	if err != nil {
		return err
	}
	ex.body.abort()
	snd.recordResponse(ex.started, ex.resp, ex.dto)
	return nil
}

// readKept reads ref from k; label names the request in messages.
func readKept(ctx context.Context, k *keptResponse, ref template.ResponseRef, label string) (template.ResponseValue, error) {
	fail := func(code, msg string) *errs.Error {
		return errs.New(errs.NotFound, msg).WithCode(code).WithDetail("requestId", ref.RequestID)
	}
	needsBody := ref.Source != template.ResponseHeader && ref.Source != template.ResponseCookie && ref.Source != template.ResponseStatus
	if needsBody && k.bodyProblem != "" {
		return template.ResponseValue{}, fail(CodeResponseBodyUnavailable, "The body of the last response of "+label+" cannot be read: "+lowerFirst(k.bodyProblem)).
			WithHint("Read a header instead, or send " + label + " again.")
	}
	var value string
	if ref.Source == template.ResponseBody {
		value = string(k.body)
	} else {
		rule := extract.Rule{Source: extract.Source(ref.Source), Expr: ref.Expr, Variable: "value", Scope: extract.ScopeRuntime}
		res := extract.Evaluate(ctx, assert.Response{Status: k.status, Header: k.header, Body: k.body}, []extract.Rule{rule})[0]
		switch res.Outcome {
		case extract.Found:
			value = res.Value
		case extract.Failed:
			return template.ResponseValue{}, fail(res.Code, inLastResponse(res.Message, label, k))
		default:
			return template.ResponseValue{}, errs.New(errs.InvalidArgument, res.Message).WithCode(res.Code).WithDetail("requestId", ref.RequestID)
		}
	}
	if k.fromHistory && strings.Contains(value, httpclient.Mask) {
		return template.ResponseValue{}, errs.New(errs.InvalidArgument, "The value is masked in the history's copy of the last response of "+label+": it holds a secret.").
			WithCode(CodeResponseMasked).
			WithHint("Send "+label+" again, or add send=missing to the reference to send it first when it was not sent since PostSonde started.").
			WithDetail("requestId", ref.RequestID)
	}
	secret := false
	for _, sec := range k.secrets {
		if sec != "" && strings.Contains(value, sec) {
			secret = true
			break
		}
	}
	return template.ResponseValue{Value: value, Secret: secret}, nil
}

// inLastResponse says where an extraction message looked: "JSONPath
// $.token selects nothing in the body." becomes "… in the body of the last
// response of "Login" (status 401)."
func inLastResponse(msg, label string, k *keptResponse) string {
	msg = strings.TrimSuffix(msg, ".")
	last := "the last response of " + label + " (status " + strconv.Itoa(k.status) + ")"
	switch {
	case strings.HasSuffix(msg, " the body"):
		return msg + " of " + last + "."
	case strings.HasSuffix(msg, " the response"):
		return strings.TrimSuffix(msg, "the response") + last + "."
	}
	return msg + ", in " + last + "."
}

// codeOf is err's errs code, or "".
func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// lowerFirst lowers the first letter of a message joined after "but".
func lowerFirst(s string) string {
	if len(s) > 1 && s[0] >= 'A' && s[0] <= 'Z' && (s[1] < 'A' || s[1] > 'Z') {
		return string(s[0]+'a'-'A') + s[1:]
	}
	return s
}
