package grpc

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// snapshot returns the items of operation id received so far.
func (r *recorder) snapshot(id string) []CallEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.items[id])
}

// waitFor waits until the items of id satisfy ok.
func (r *recorder) waitFor(t *testing.T, id string, what string, ok func([]CallEventDTO) bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok(r.snapshot(id)) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; items %s", what, types(r.snapshot(id)))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// received counts the response messages among items.
func received(items []CallEventDTO) int {
	n := 0
	for _, it := range items {
		if it.Type == CallMessage {
			n++
		}
	}
	return n
}

// withoutHeaders drops the headers item, whose place depends on when the
// server's headers arrive.
func withoutHeaders(items []CallEventDTO) string {
	return types(slices.DeleteFunc(slices.Clone(items), func(it CallEventDTO) bool { return it.Type == CallHeaders }))
}

func start(t *testing.T, s *GRPCService, in InvokeInputDTO) string {
	t.Helper()
	started, err := s.Invoke(t.Context(), in)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return started.OpID
}

func send(t *testing.T, s *GRPCService, id, text string) SentMessageDTO {
	t.Helper()
	out, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: id, Message: `{"text": "` + text + `"}`})
	if err != nil {
		t.Fatalf("SendMessage %q: %v", text, err)
	}
	return out
}

func errCode(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func TestClientStreaming(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Upload")
	in.Message = "not JSON, and not sent" // a stream sends with SendMessage
	id := start(t, s, in)
	for i, text := range []string{"a", "b", "c"} {
		if out := send(t, s, id, text); out.Index != i || out.Size != 3 {
			t.Errorf("send %d = %+v", i, out)
		}
	}
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
		t.Fatal(err)
	}
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if got := withoutHeaders(items); got != "request,sent,sent,sent,half-close,message,end" {
		t.Fatalf("items %s", got)
	}
	req := items[0]
	if !req.ClientStreaming || req.JSON != "" || req.Size != 0 || req.Method != "/calls.v1.Calls/Upload" {
		t.Errorf("request %+v", req)
	}
	for i := range 3 {
		it := items[1+i]
		if it.Index != i || compactJSON(t, it.JSON) != `{"text":"`+string(rune('a'+i))+`"}` || it.Size != 3 {
			t.Errorf("sent item %d %+v", i, it)
		}
	}
	if hc := items[4]; hc.Index != 3 {
		t.Errorf("half-close %+v", hc)
	}
	msg := items[len(items)-2]
	if got := compactJSON(t, msg.JSON); got != `{"text":"a+b+c","count":"3"}` {
		t.Errorf("response %s", got)
	}
	end := items[len(items)-1]
	if end.Status.Name != "OK" || end.Messages != 1 || end.Sent != 3 {
		t.Errorf("end %+v", end)
	}
	if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: id, Message: "{}"}); errCode(err) != CodeCallNotRunning {
		t.Errorf("SendMessage after the end = %v", err)
	}
}

func TestClientStreamingNoMessages(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	id := start(t, s, input(srv.Addr, "Upload"))
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
		t.Fatal(err)
	}
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if got := withoutHeaders(items); got != "request,half-close,message,end" {
		t.Errorf("items %s", got)
	}
}

func TestBidiOrdering(t *testing.T) {
	t.Parallel()
	for _, withTLS := range []bool{false, true} {
		t.Run("tls="+strconv.FormatBool(withTLS), func(t *testing.T) {
			t.Parallel()
			srv := newCallServer(t, withTLS)
			s, rec := newCallService(t)
			in := input(srv.Addr, "Chat")
			in.TLS = withTLS
			if withTLS {
				verify := false
				in.VerifyTLS = &verify
			}
			in.Metadata = []MetadataRowDTO{{Name: "x-trace", Value: "1", Enabled: true}}
			id := start(t, s, in)
			// Each reply is awaited before the next send, so the order is
			// fixed: sent, reply, sent, reply.
			for i, text := range []string{"one", "two"} {
				send(t, s, id, text)
				rec.waitFor(t, id, "reply "+text, func(items []CallEventDTO) bool { return received(items) == i+1 })
			}
			if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
				t.Fatal(err)
			}
			// Closing again is harmless.
			if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
				t.Errorf("second CloseSend: %v", err)
			}
			items, done := rec.wait(t, id)
			if done.Status != operation.StatusOK {
				t.Fatalf("done %+v %+v", done, done.Error)
			}
			if got := withoutHeaders(items); got != "request,sent,message,sent,message,half-close,end" {
				t.Fatalf("items %s", got)
			}
			for i, it := range items {
				if it.Type == CallHeaders && slices.ContainsFunc(items[:i], func(x CallEventDTO) bool { return x.Type == CallMessage }) {
					t.Errorf("headers after a message: %s", types(items))
				}
			}
			var replies []string
			var last time.Time
			for _, it := range items {
				at, err := time.Parse(time.RFC3339Nano, it.At)
				if err != nil {
					t.Fatalf("at %q: %v", it.At, err)
				}
				if at.Before(last) {
					t.Errorf("%s at %s is before the previous item at %s", it.Type, at, last)
				}
				last = at
				if it.Type == CallMessage {
					replies = append(replies, compactJSON(t, it.JSON))
				}
			}
			if strings.Join(replies, " ") != `{"text":"echo:one"} {"text":"echo:two"}` {
				t.Errorf("replies %v", replies)
			}
			end := items[len(items)-1]
			if end.Messages != 2 || end.Sent != 2 {
				t.Errorf("end %+v", end)
			}
			if !slices.ContainsFunc(items, func(it CallEventDTO) bool {
				_, ok := md(it.Metadata, "content-type")
				return it.Type == CallHeaders && ok
			}) {
				t.Errorf("no headers item: %s", types(items))
			}
		})
	}
}

// TestBidiConcurrentSends sends from several goroutines while replies
// arrive: every reply comes after the message it answers.
func TestBidiConcurrentSends(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	id := start(t, s, input(srv.Addr, "Chat"))
	const senders, each = 4, 10
	var wg sync.WaitGroup
	for g := range senders {
		wg.Go(func() {
			for i := range each {
				send(t, s, id, "m"+strconv.Itoa(g)+"-"+strconv.Itoa(i))
			}
		})
	}
	wg.Wait()
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
		t.Fatal(err)
	}
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	sentAt := map[string]int{}
	next := 0
	for i, it := range items {
		switch it.Type {
		case CallSent:
			if it.Index != next {
				t.Errorf("sent item %d has index %d, want %d", i, it.Index, next)
			}
			next++
			sentAt[strings.TrimPrefix(compactJSON(t, it.JSON), `{"text":"`)] = i
		case CallMessage:
			key := strings.TrimPrefix(compactJSON(t, it.JSON), `{"text":"echo:`)
			pos, ok := sentAt[key]
			if !ok || pos > i {
				t.Errorf("reply %s at %d comes before its message", key, i)
			}
		}
	}
	if next != senders*each || received(items) != senders*each {
		t.Errorf("%d sent, %d received", next, received(items))
	}
	// The half-close follows every sent message; replies may follow it.
	hc := slices.IndexFunc(items, func(it CallEventDTO) bool { return it.Type == CallHalfClose })
	if hc < 0 || slices.ContainsFunc(items[hc:], func(it CallEventDTO) bool { return it.Type == CallSent }) || items[len(items)-1].Type != CallEnd {
		t.Errorf("items %s", types(items))
	}
}

func TestBidiCancelMidStream(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	id := start(t, s, input(srv.Addr, "Chat"))
	send(t, s, id, "one")
	rec.waitFor(t, id, "the reply", func(items []CallEventDTO) bool { return received(items) == 1 })
	if !s.ops.Cancel(id) {
		t.Fatal("Cancel found no operation")
	}
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusCancelled {
		t.Fatalf("done %+v", done)
	}
	if got := withoutHeaders(items); got != "request,sent,message,end" {
		t.Errorf("items %s", got)
	}
	if end := items[len(items)-1]; end.Status.Name != "CANCELLED" || end.Sent != 1 || end.Messages != 1 {
		t.Errorf("end %+v", end)
	}
	if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: id, Message: "{}"}); errCode(err) != CodeCallNotRunning {
		t.Errorf("SendMessage after cancel = %v", err)
	}
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); errCode(err) != CodeCallNotRunning {
		t.Errorf("CloseSend after cancel = %v", err)
	}
}

func TestBidiServerErrorMidStream(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	id := start(t, s, input(srv.Addr, "Chat"))
	send(t, s, id, "one")
	rec.waitFor(t, id, "the reply", func(items []CallEventDTO) bool { return received(items) == 1 })
	send(t, s, id, "fail")
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error.Details["grpc_status"] != "ABORTED" {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if got := withoutHeaders(items); got != "request,sent,message,sent,end" {
		t.Errorf("items %s", got)
	}
	if end := items[len(items)-1]; end.Status.Name != "ABORTED" || end.Status.Message != "boom" {
		t.Errorf("end %+v", end.Status)
	}
}

func TestClientStreamingDeadline(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Upload")
	deadline := int64(200)
	in.DeadlineMs = &deadline
	id := start(t, s, in)
	send(t, s, id, "a")
	// Never half-closed: the deadline ends the call.
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error.Details["grpc_status"] != "DEADLINE_EXCEEDED" {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if got := withoutHeaders(items); got != "request,sent,end" {
		t.Errorf("items %s", got)
	}
	if items[0].DeadlineMs != 200 {
		t.Errorf("request %+v", items[0])
	}
}

func TestSendMessageRejected(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)

	if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: "op_nope", Message: "{}"}); errCode(err) != CodeCallNotRunning {
		t.Errorf("unknown call: %v", err)
	}
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: "op_nope"}); errCode(err) != CodeCallNotRunning {
		t.Errorf("CloseSend unknown call: %v", err)
	}

	// A server stream sends its one message with the request.
	wait := start(t, s, input(srv.Addr, "Wait"))
	<-srv.waiting
	if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: wait, Message: "{}"}); errCode(err) != CodeNotClientStreaming {
		t.Errorf("unary call: %v", err)
	}
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: wait}); errCode(err) != CodeNotClientStreaming {
		t.Errorf("CloseSend unary call: %v", err)
	}
	s.ops.Cancel(wait)
	rec.wait(t, wait)

	id := start(t, s, input(srv.Addr, "Chat"))
	for _, tt := range []struct{ message, code string }{
		{`{"text": }`, CodeInvalidMessage},
		{`{"nope": 1}`, CodeInvalidMessage},
		{`{"text": "{{name}}"}`, CodeVariablesUnsupported},
	} {
		if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: id, Message: tt.message}); errCode(err) != tt.code {
			t.Errorf("SendMessage(%s) = %v, want %s", tt.message, err, tt.code)
		}
	}
	if err := s.CloseSend(t.Context(), CallRefDTO{OpID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(t.Context(), SendMessageInputDTO{OpID: id, Message: "{}"}); errCode(err) != CodeSendClosed && errCode(err) != CodeCallNotRunning {
		t.Errorf("SendMessage after CloseSend = %v", err)
	}
	items, done := rec.wait(t, id)
	if done.Status != operation.StatusOK || withoutHeaders(items) != "request,half-close,end" {
		t.Errorf("done %+v, items %s", done, types(items))
	}
	s.callsMu.Lock()
	n := len(s.calls)
	s.callsMu.Unlock()
	if n != 0 {
		t.Errorf("%d calls still registered", n)
	}
}
