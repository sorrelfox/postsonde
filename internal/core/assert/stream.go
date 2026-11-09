package assert

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MessageScope says which messages of a stream an assertion checks
// (TEST-08).
type MessageScope string

// Message scopes.
const (
	// MessagesAny passes when at least one message satisfies the check.
	MessagesAny MessageScope = "any"
	// MessagesEvery passes when every message does; it fails when there
	// are no messages.
	MessagesEvery MessageScope = "every"
	// MessagesNone passes when no message does (also when there are none).
	MessagesNone MessageScope = "none"
	// MessagesFirst, MessagesLast and MessagesNth check one message: the
	// first, the last, or the one at Assertion.Index (from 1). They fail
	// when there is no such message.
	MessagesFirst MessageScope = "first"
	MessagesLast  MessageScope = "last"
	MessagesNth   MessageScope = "nth"
)

// Error codes of Invalid stream results.
const (
	// CodeNotStream: a message assertion (a message scope, or the
	// message_count target) evaluated against a response that is not a
	// stream.
	CodeNotStream = "assert.not_a_stream"
	// CodeUnknownScope: Messages is not one of the scopes above.
	CodeUnknownScope = "assert.unknown_scope"
	// CodeInvalidIndex: MessagesNth with an index under 1.
	CodeInvalidIndex = "assert.invalid_index"
	// CodeMessageNotKept: MessagesNth names a message that was received
	// but not kept (Stream.Messages is bounded).
	CodeMessageNotKept = "assert.message_not_kept"
)

// Message is one message of a stream, as a message assertion reads it: the
// body targets (jsonpath, xpath, regex, body, size) read Body, header
// reads Header, time is Time.
type Message struct {
	// Body is the payload: a gRPC message in the protobuf JSON mapping, a
	// WebSocket message's bytes, an SSE event's data, a Socket.IO event's
	// arguments as a JSON array.
	Body []byte
	// Header holds the message's own fields, if the protocol has any: an
	// SSE event's "Event" and "Id", a WebSocket message's "Type" (text or
	// binary), a Socket.IO event's "Event". May be nil.
	Header http.Header
	// Time is when it arrived, counted from the start of the stream.
	Time time.Duration
}

// Stream is what message assertions are evaluated against: the stream as
// a whole (its Response: the status, headers and time; assertions without
// a message scope read it) and its received messages.
type Stream struct {
	Response
	// Messages are the messages received, in order from the first; a
	// recorder may keep only the first ones (Total says how many came).
	Messages []Message
	// Total is the number of messages received; 0 means len(Messages).
	Total int
	// Last is the last message received when Messages does not hold it
	// (Total > len(Messages)); nil otherwise.
	Last *Message
}

func (s *Stream) total() int {
	if s.Total > len(s.Messages) {
		return s.Total
	}
	return len(s.Messages)
}

// last is the last message received, or nil without any.
func (s *Stream) last() *Message {
	if s.Last != nil && s.Total > len(s.Messages) {
		return s.Last
	}
	if len(s.Messages) == 0 {
		return nil
	}
	return &s.Messages[len(s.Messages)-1]
}

// numbered is a message with its number, from 1.
type numbered struct {
	n int
	m *Message
}

// all are the messages a scope over every message reads: the kept ones
// and the last.
func (s *Stream) all() []numbered {
	out := make([]numbered, 0, len(s.Messages)+1)
	for i := range s.Messages {
		out = append(out, numbered{i + 1, &s.Messages[i]})
	}
	if s.Last != nil && s.Total > len(s.Messages) {
		out = append(out, numbered{s.Total, s.Last})
	}
	return out
}

// EvaluateStream checks every assertion against a stream, in order
// (TEST-08). An assertion with a message scope (Assertion.Messages) reads
// its target from the messages; message_count is the number of messages;
// the others read s.Response, as Evaluate does.
func EvaluateStream(ctx context.Context, s Stream, list []Assertion, opts Options) []Result {
	ev := &evaluator{ctx: ctx, resp: s.Response, opts: opts, stream: &s, schemas: map[string]compiledSchema{}}
	out := make([]Result, len(list))
	for i, a := range list {
		out[i] = ev.eval(a)
	}
	return out
}

func notStream() error {
	return invalid(CodeNotStream, "Only the messages of a stream can be checked this way: this response is not a stream.")
}

// forMessage is an evaluator of one message: its body, its header, its
// time; the status of the stream. Compiled schemas are shared.
func (ev *evaluator) forMessage(m *Message) *evaluator {
	if ev.schemas == nil {
		ev.schemas = map[string]compiledSchema{}
	}
	return &evaluator{
		ctx: ev.ctx, opts: ev.opts, schemas: ev.schemas,
		resp: Response{Status: ev.resp.Status, Header: m.Header, Body: m.Body, Time: m.Time},
	}
}

// evalMessages checks an assertion with a message scope.
func (ev *evaluator) evalMessages(a Assertion) Result {
	r := Result{Assertion: a}
	if ev.stream == nil {
		return invalidResult(r, notStream())
	}
	if a.Target == TargetMessageCount {
		return invalidResult(r, invalid(CodeUnknownTarget, "The message count is a value of the whole stream: check it without a message scope."))
	}
	s := ev.stream
	total := s.total()
	switch a.Messages {
	case MessagesFirst, MessagesLast, MessagesNth:
		var m *Message
		switch a.Messages {
		case MessagesFirst:
			if len(s.Messages) > 0 {
				m = &s.Messages[0]
			} else {
				m = s.last()
			}
		case MessagesLast:
			m = s.last()
		default:
			if a.Index < 1 {
				return invalidResult(r, invalid(CodeInvalidIndex, "The message number %d is not valid: messages are counted from 1.", a.Index))
			}
			switch {
			case a.Index > total:
				m = nil
			case a.Index <= len(s.Messages):
				m = &s.Messages[a.Index-1]
			case a.Index == total && s.Last != nil:
				m = s.Last
			default:
				return invalidResult(r, invalid(CodeMessageNotKept, "Message %d was received but not kept: only the first %d messages are kept for checks.", a.Index, len(s.Messages)))
			}
		}
		if m == nil {
			r.Outcome = Failed
			r.Message = fmt.Sprintf("Expected %s to %s, but %s.", label(a), phrase(a), fewMessages(total))
			return r
		}
		return ev.forMessage(m).evalPlain(a)
	case MessagesAny, MessagesEvery, MessagesNone:
	default:
		return invalidResult(r, invalid(CodeUnknownScope, "The message scope %q is not known.", string(a.Messages)))
	}

	plain := a
	plain.Messages, plain.Index = "", 0
	what := label(plain) + " to " + phrase(plain)
	note := ""
	if total > len(s.Messages)+boolInt(s.Last != nil) {
		note = fmt.Sprintf(" Only the first %d of the %d messages were kept for checks.", len(s.Messages), total)
	}
	list := s.all()
	r.Exists = true
	switch a.Messages {
	case MessagesAny:
		for _, nm := range list {
			sub := ev.forMessage(nm.m).evalPlain(plain)
			switch sub.Outcome {
			case Invalid:
				return invalidResult(r, invalidFrom(sub))
			case Passed:
				r.Outcome, r.Actual = Passed, sub.Actual
				r.Message = relabel(sub.Message, plain, fmt.Sprintf("Message %d's ", nm.n))
				return r
			}
		}
		r.Outcome = Failed
		switch {
		case len(list) == 0:
			r.Message = "Expected some message's " + what + ", but " + fewMessages(0) + "."
		case total == 1:
			r.Message = "Expected some message's " + what + ", but the only message did not."
		default:
			r.Message = "Expected some message's " + what + ", but none of the " + countMessages(total) + " did." + note
		}
		return r
	case MessagesEvery:
		if len(list) == 0 {
			r.Outcome = Failed
			r.Message = "Expected every message's " + what + ", but " + fewMessages(0) + "."
			return r
		}
		for _, nm := range list {
			sub := ev.forMessage(nm.m).evalPlain(plain)
			switch sub.Outcome {
			case Invalid:
				return invalidResult(r, invalidFrom(sub))
			case Failed:
				r.Outcome, r.Actual, r.Exists, r.SchemaErrors = Failed, sub.Actual, sub.Exists, sub.SchemaErrors
				r.Message = fmt.Sprintf("Expected every message's %s, but in message %d %s.%s", what, nm.n, why(sub.Message, plain), note)
				return r
			}
		}
		r.Outcome = Passed
		r.Message = fmt.Sprintf("Every message's %s %s (%s).%s", label(plain), phrase3(plain), countMessages(total), note)
		return r
	}
	// MessagesNone
	for _, nm := range list {
		sub := ev.forMessage(nm.m).evalPlain(plain)
		switch sub.Outcome {
		case Invalid:
			return invalidResult(r, invalidFrom(sub))
		case Passed:
			r.Outcome, r.Actual, r.Exists = Failed, sub.Actual, sub.Exists
			it := "it does not exist"
			if sub.Exists {
				it = "it was " + sub.Actual
			}
			r.Message = fmt.Sprintf("Expected no message's %s, but in message %d %s.", what, nm.n, it)
			return r
		}
	}
	r.Outcome = Passed
	if len(list) == 0 {
		r.Message = fmt.Sprintf("No message's %s %s: %s.", label(plain), phrase3(plain), fewMessages(0))
	} else {
		r.Message = fmt.Sprintf("No message's %s %s (%s).%s", label(plain), phrase3(plain), countMessages(total), note)
	}
	return r
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// invalidFrom is the error of an invalid result, to report it as the
// scope's.
func invalidFrom(r Result) error {
	return &invalidErr{code: r.Code, msg: r.Message}
}

// relabel puts prefix before the target's label in a message that starts
// with it: "JSONPath $.id equals 1: it was 1." → "Message 3's JSONPath
// $.id equals 1: it was 1.".
func relabel(msg string, a Assertion, prefix string) string {
	l := upperFirst(label(a))
	if rest, ok := strings.CutPrefix(msg, l); ok {
		return prefix + label(a) + rest
	}
	return prefix + msg
}

// why is the reason in a failure message, after "but": "it was 404",
// "it does not exist", "the body is not valid JSON".
func why(msg string, a Assertion) string {
	prefix := "Expected " + label(a) + " to " + phrase(a) + ", but "
	if rest, ok := strings.CutPrefix(msg, prefix); ok {
		return strings.TrimSuffix(rest, ".")
	}
	return strings.TrimSuffix(msg, ".")
}

// fewMessages: "there were no messages", "there was only 1 message",
// "there were only 3 messages".
func fewMessages(n int) string {
	switch n {
	case 0:
		return "there were no messages"
	case 1:
		return "there was only 1 message"
	}
	return "there were only " + strconv.Itoa(n) + " messages"
}

// countMessages: "1 message", "5 messages".
func countMessages(n int) string {
	if n == 1 {
		return "1 message"
	}
	return strconv.Itoa(n) + " messages"
}
