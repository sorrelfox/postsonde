package grpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/rpcstatus"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

const callsProto = `syntax = "proto3";

package calls.v1;

message Req {
  string text = 1;
  int64 count = 2;
  bytes blob = 3;
}

message Reply {
  string text = 1;
  int64 count = 2;
  bytes blob = 3;
}

service Calls {
  rpc Echo(Req) returns (Reply);
  rpc Repeat(Req) returns (stream Reply);
  rpc Wait(Req) returns (Reply);
  rpc Fail(Req) returns (Reply);
  rpc Garbage(Req) returns (Reply);
  rpc Twice(Req) returns (Reply);
  rpc Nothing(Req) returns (Reply);
  rpc Upload(stream Req) returns (Reply);
  rpc Chat(stream Req) returns (stream Reply);
}
`

// recorder collects the events of one registry.
type recorder struct {
	mu    sync.Mutex
	items map[string][]CallEventDTO
	seqs  map[string][]int64
	done  map[string]chan operation.DoneEventDTO
}

func newRecorder() *recorder {
	return &recorder{items: map[string][]CallEventDTO{}, seqs: map[string][]int64{}, done: map[string]chan operation.DoneEventDTO{}}
}

func (r *recorder) doneChan(id string) chan operation.DoneEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.done[id]
	if !ok {
		ch = make(chan operation.DoneEventDTO, 1)
		r.done[id] = ch
	}
	return ch
}

func (r *recorder) emit(name string, data any) {
	switch d := data.(type) {
	case operation.DataEventDTO[CallEventDTO]:
		if name != EventData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
		r.seqs[d.OpID] = append(r.seqs[d.OpID], d.Seq)
		r.mu.Unlock()
	case operation.DoneEventDTO:
		if name != EventDone {
			panic("done event named " + name)
		}
		r.mu.Lock()
		r.seqs[d.OpID] = append(r.seqs[d.OpID], d.Seq)
		r.mu.Unlock()
		r.doneChan(d.OpID) <- d
	}
}

func (r *recorder) wait(t *testing.T, id string) ([]CallEventDTO, operation.DoneEventDTO) {
	t.Helper()
	select {
	case d := <-r.doneChan(id):
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, s := range r.seqs[id] {
			if s != int64(i+1) {
				t.Errorf("event %d has seq %d", i, s)
			}
		}
		return r.items[id], d
	case <-time.After(20 * time.Second):
		t.Fatal("no done event")
		return nil, operation.DoneEventDTO{}
	}
}

// callServer is the test server with the Calls service.
type callServer struct {
	*grpctest.Server
	mu       sync.Mutex
	metadata transport.Metadata
	timeout  time.Duration
	waiting  chan struct{} // Wait received its request
	waitDone chan error    // how Wait's context ended
}

func textField(s string) []byte {
	return append([]byte{0x0a, byte(len(s))}, s...)
}

func newCallServer(t *testing.T, withTLS bool) *callServer {
	t.Helper()
	o := grpctest.ServerOptions{}
	if withTLS {
		o.Security = grpctest.TLS
	}
	return newCallServerWith(t, o)
}

// newCallServerWith is newCallServer with the server secured as o says.
func newCallServerWith(t *testing.T, o grpctest.ServerOptions) *callServer {
	t.Helper()
	cs := &callServer{Server: grpctest.NewServerWith(t, o), waiting: make(chan struct{}, 1), waitDone: make(chan error, 1)}
	recv := func(ss *grpctest.ServerStream) ([]byte, transport.Status, bool) {
		cs.mu.Lock()
		cs.metadata = ss.Metadata
		if ss.HasTimeout {
			cs.timeout = ss.Timeout
		}
		cs.mu.Unlock()
		for _, e := range ss.Metadata {
			if strings.HasPrefix(e.Key, "x-") {
				ss.SetHeader(e)
				ss.SetTrailer(transport.MDEntry{Key: "t-" + e.Key, Value: e.Value})
			}
		}
		msg, err := ss.Recv()
		if err != nil {
			return nil, transport.Status{Code: transport.Internal, Message: err.Error()}, false
		}
		return msg, transport.Status{}, true
	}
	cs.Handle("/calls.v1.Calls/Echo", func(ss *grpctest.ServerStream) transport.Status {
		msg, st, ok := recv(ss)
		if ok {
			_ = ss.Send(msg)
		}
		return st
	})
	cs.Handle("/calls.v1.Calls/Repeat", func(ss *grpctest.ServerStream) transport.Status {
		msg, st, ok := recv(ss)
		if !ok {
			return st
		}
		for i := range 3 {
			if err := ss.Send(append(append([]byte(nil), msg...), textField("#"+strconv.Itoa(i))...)); err != nil {
				return transport.Status{Code: transport.Internal, Message: err.Error()}
			}
		}
		return transport.Status{}
	})
	cs.Handle("/calls.v1.Calls/Wait", func(ss *grpctest.ServerStream) transport.Status {
		if _, st, ok := recv(ss); !ok {
			return st
		}
		_ = ss.SendHeader()
		cs.waiting <- struct{}{}
		<-ss.Context().Done()
		cs.waitDone <- ss.Context().Err()
		return transport.Status{Code: transport.DeadlineExceeded, Message: "too slow"}
	})
	cs.Handle("/calls.v1.Calls/Fail", func(ss *grpctest.ServerStream) transport.Status {
		if _, st, ok := recv(ss); !ok {
			return st
		}
		return transport.Status{Code: transport.NotFound, Message: "no such order", Details: []byte{1, 2, 3}}
	})
	cs.Handle("/calls.v1.Calls/Garbage", func(ss *grpctest.ServerStream) transport.Status {
		if _, st, ok := recv(ss); !ok {
			return st
		}
		_ = ss.Send([]byte{0x0a, 0x05, 'a'}) // a string field cut short
		return transport.Status{}
	})
	cs.Handle("/calls.v1.Calls/Twice", func(ss *grpctest.ServerStream) transport.Status {
		msg, st, ok := recv(ss)
		if ok {
			_ = ss.Send(msg)
			_ = ss.Send(msg)
		}
		return st
	})
	cs.Handle("/calls.v1.Calls/Nothing", func(ss *grpctest.ServerStream) transport.Status {
		_, st, _ := recv(ss)
		return st
	})
	// Upload answers every message with their texts joined by "+" and
	// their count.
	cs.Handle("/calls.v1.Calls/Upload", func(ss *grpctest.ServerStream) transport.Status {
		var texts []string
		for {
			msg, err := ss.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return transport.Status{Code: transport.Internal, Message: err.Error()}
			}
			texts = append(texts, reqText(msg))
		}
		return sendStatus(ss, append(textField(strings.Join(texts, "+")), 0x10, byte(len(texts))))
	})
	// Chat answers each message with "echo:<text>" as it arrives, and a
	// message with the text "fail" with ABORTED.
	cs.Handle("/calls.v1.Calls/Chat", func(ss *grpctest.ServerStream) transport.Status {
		if err := ss.SendHeader(); err != nil {
			return transport.Status{Code: transport.Internal, Message: err.Error()}
		}
		for {
			msg, err := ss.Recv()
			if errors.Is(err, io.EOF) {
				return transport.Status{}
			}
			if err != nil {
				return transport.Status{Code: transport.Canceled, Message: err.Error()}
			}
			text := reqText(msg)
			if text == "fail" {
				return transport.Status{Code: transport.Aborted, Message: "boom"}
			}
			if err := ss.Send(textField("echo:" + text)); err != nil {
				return transport.Status{Code: transport.Internal, Message: err.Error()}
			}
		}
	})
	return cs
}

func sendStatus(ss *grpctest.ServerStream, msg []byte) transport.Status {
	if err := ss.Send(msg); err != nil {
		return transport.Status{Code: transport.Internal, Message: err.Error()}
	}
	return transport.Status{}
}

// reqText is the text field of an encoded calls.v1.Req.
func reqText(b []byte) string {
	for len(b) > 0 {
		num, typ, n, err := protowire.ConsumeTag(b)
		if err != nil {
			return ""
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, _, _ := protowire.ConsumeBytes(b)
			return string(v)
		}
		if n, err = protowire.ConsumeFieldValue(num, typ, b); err != nil {
			return ""
		}
		b = b[n:]
	}
	return ""
}

func (cs *callServer) lastMetadata() transport.Metadata {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.metadata
}

// newCallService opens a project with calls.proto and returns a service
// with its own registry.
func newCallService(t *testing.T, opts ...Option) (*GRPCService, *recorder) {
	t.Helper()
	dir := filepath.Join(tempDir(t), "calls")
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Calls"}`)
	writeFile(t, dir, "proto/calls.proto", callsProto)
	manager := coreworkspace.New(tempDir(t))
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	reg := operation.NewRegistry(rec.emit, nil)
	t.Cleanup(func() { _ = reg.Shutdown(t.Context()) })
	return New(manager, reg, netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), nil, nil, opts...), rec
}

func input(addr, method string) InvokeInputDTO {
	return InvokeInputDTO{
		RequestID: "req_1",
		Files:     []string{"proto/calls.proto"},
		Address:   addr,
		Service:   "calls.v1.Calls",
		Method:    method,
		Message:   `{"text": "hi", "count": 7}`,
	}
}

func invoke(t *testing.T, s *GRPCService, rec *recorder, in InvokeInputDTO) ([]CallEventDTO, operation.DoneEventDTO) {
	t.Helper()
	started, err := s.Invoke(t.Context(), in)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return rec.wait(t, started.OpID)
}

func types(items []CallEventDTO) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Type)
	}
	return strings.Join(out, ",")
}

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("not JSON %q: %v", s, err)
	}
	return b.String()
}

func md(items []MetadataDTO, key string) (MetadataDTO, bool) {
	for _, e := range items {
		if e.Key == key {
			return e, true
		}
	}
	return MetadataDTO{}, false
}

func TestInvokeUnary(t *testing.T) {
	t.Parallel()
	for _, withTLS := range []bool{false, true} {
		t.Run("tls="+strconv.FormatBool(withTLS), func(t *testing.T) {
			t.Parallel()
			srv := newCallServer(t, withTLS)
			s, rec := newCallService(t)
			in := input(srv.Addr, "Echo")
			in.TLS = withTLS
			if withTLS {
				verify := false // the test CA is not trusted by the system
				in.VerifyTLS = &verify
			}
			in.Message = "{\n  \"text\": \"hi\",\n  \"count\": \"7\",\n  \"blob\": \"AAEC\"\n}"
			items, done := invoke(t, s, rec, in)
			if done.Status != operation.StatusOK || done.Error != nil {
				t.Fatalf("done = %+v, error %+v", done, done.Error)
			}
			if got := types(items); got != "request,headers,message,end" {
				t.Fatalf("items %s", got)
			}
			req := items[0]
			if req.Method != "/calls.v1.Calls/Echo" || req.Size != 11 || req.DeadlineMs != 0 {
				t.Errorf("request item %+v", req)
			}
			if got := compactJSON(t, req.JSON); got != `{"text":"hi","count":"7","blob":"AAEC"}` {
				t.Errorf("request JSON %s", got)
			}
			msg := items[2]
			if got := compactJSON(t, msg.JSON); got != `{"text":"hi","count":"7","blob":"AAEC"}` || msg.Index != 0 || msg.Size != 11 || msg.DecodeError != "" {
				t.Errorf("message item %+v", msg)
			}
			if ct, ok := md(items[1].Metadata, "content-type"); !ok || ct.Value != "application/grpc" {
				t.Errorf("headers %+v", items[1].Metadata)
			}
			end := items[3]
			if end.Status == nil || end.Status.Code != 0 || end.Status.Name != "OK" || end.Messages != 1 {
				t.Errorf("end item %+v status %+v", end, end.Status)
			}
			if _, err := time.Parse(time.RFC3339Nano, end.At); err != nil {
				t.Errorf("end.at %q: %v", end.At, err)
			}
		})
	}
}

func TestInvokeEmptyMessage(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Echo")
	in.Message = "  \n"
	items, done := invoke(t, s, rec, in)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v", done)
	}
	if items[0].JSON != "{}" || items[0].Size != 0 || items[2].JSON != "{}" {
		t.Errorf("items %+v", items)
	}
}

func TestInvokeServerStream(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	items, done := invoke(t, s, rec, input(srv.Addr, "Repeat"))
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if got := types(items); got != "request,headers,message,message,message,end" {
		t.Fatalf("items %s", got)
	}
	for i := range 3 {
		it := items[2+i]
		want := `{"text":"#` + strconv.Itoa(i) + `","count":"7"}`
		if got := compactJSON(t, it.JSON); got != want || it.Index != i {
			t.Errorf("message %d = %s (index %d), want %s", i, got, it.Index, want)
		}
	}
	if end := items[5]; end.Messages != 3 || end.Status.Name != "OK" {
		t.Errorf("end %+v", end)
	}
}

func TestInvokeMetadata(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Echo")
	in.Metadata = []MetadataRowDTO{
		{Name: " X-Trace ", Value: "abc", Enabled: true},
		{Name: "x-off", Value: "no", Enabled: false},
		{Name: "", Value: "empty name", Enabled: true},
		{Name: "x-token-bin", Value: "AAEC/w==", Enabled: true},
		{Name: "x-raw-bin", Value: "AAE", Enabled: true}, // unpadded
		{Name: "x-url-bin", Value: "_-8", Enabled: true}, // URL-safe
		{Name: "x-trace", Value: "second", Enabled: true},
	}
	items, done := invoke(t, s, rec, in)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}

	got := srv.lastMetadata()
	if v := got.Get("x-trace"); len(v) != 2 || v[0] != "abc" || v[1] != "second" {
		t.Errorf("server got x-trace %q", v)
	}
	if v := got.Get("x-off"); len(v) != 0 {
		t.Errorf("disabled row sent: %q", v)
	}
	for key, want := range map[string]string{"x-token-bin": "\x00\x01\x02\xff", "x-raw-bin": "\x00\x01", "x-url-bin": "\xff\xef"} {
		if v := got.Get(key); len(v) != 1 || v[0] != want {
			t.Errorf("server got %s %q, want %q", key, v, want)
		}
	}

	sent := items[0].Metadata
	if e, ok := md(sent, "x-token-bin"); !ok || !e.Binary || e.Value != "AAEC/w==" {
		t.Errorf("request item metadata %+v", sent)
	}
	if e, ok := md(sent, "x-trace"); !ok || e.Binary || e.Value != "abc" {
		t.Errorf("request item metadata %+v", sent)
	}
	headers := items[1].Metadata
	if e, ok := md(headers, "x-token-bin"); !ok || !e.Binary || e.Value != "AAEC/w==" {
		t.Errorf("response headers %+v", headers)
	}
	trailers := items[3].Metadata
	if e, ok := md(trailers, "t-x-trace"); !ok || e.Value != "abc" {
		t.Errorf("trailers %+v", trailers)
	}
	if e, ok := md(trailers, "t-x-token-bin"); !ok || !e.Binary || e.Value != "AAEC/w==" {
		t.Errorf("trailers %+v", trailers)
	}
}

func TestInvokeStatus(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	items, done := invoke(t, s, rec, input(srv.Addr, "Fail"))
	if done.Status != operation.StatusError || done.Error == nil {
		t.Fatalf("done %+v", done)
	}
	if done.Error.Code != transport.CodeStatus || done.Error.Details["grpc_status"] != "NOT_FOUND" {
		t.Errorf("done error %+v", done.Error)
	}
	// Trailers-only: no headers item.
	if got := types(items); got != "request,end" {
		t.Fatalf("items %s", got)
	}
	st := items[1].Status
	if st == nil || st.Code != 5 || st.Name != "NOT_FOUND" || st.Message != "no such order" || st.Details != "AQID" {
		t.Errorf("status %+v", st)
	}
	// The bytes are not a google.rpc.Status: kept, with the reason.
	if st == nil || st.Rich == nil || st.Rich.Error == "" || len(st.Rich.Details) != 0 {
		t.Errorf("rich %+v", st.Rich)
	}
}

func TestInvokeCardinality(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	for method, messages := range map[string]int{"Twice": 2, "Nothing": 0} {
		items, done := invoke(t, s, rec, input(srv.Addr, method))
		if done.Status != operation.StatusError || done.Error.Code != transport.CodeCardinality || done.Error.Details["grpc_status"] != "INTERNAL" {
			t.Errorf("%s: done %+v %+v", method, done, done.Error)
		}
		end := items[len(items)-1]
		if end.Type != CallEnd || end.Status.Name != "INTERNAL" || end.Messages != messages {
			t.Errorf("%s: end %+v", method, end)
		}
	}
}

func TestInvokeUndecodableResponse(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	items, done := invoke(t, s, rec, input(srv.Addr, "Garbage"))
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	msg := items[2]
	if msg.Type != CallMessage || msg.JSON != "" || !strings.Contains(msg.DecodeError, "calls.v1.Reply") || msg.Raw != "CgVh" || msg.Size != 3 {
		t.Errorf("message %+v", msg)
	}
}

func TestInvokeDeadline(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Wait")
	deadline := int64(200)
	in.DeadlineMs = &deadline
	items, done := invoke(t, s, rec, in)
	if done.Status != operation.StatusError || done.Error.Details["grpc_status"] != "DEADLINE_EXCEEDED" || done.Error.Kind != string(errs.Timeout) {
		t.Fatalf("done %+v %+v", done, done.Error)
	}
	if items[0].DeadlineMs != 200 {
		t.Errorf("request item deadline %d", items[0].DeadlineMs)
	}
	if end := items[len(items)-1]; end.Status.Name != "DEADLINE_EXCEEDED" {
		t.Errorf("end %+v", end.Status)
	}
	srv.mu.Lock()
	timeout := srv.timeout
	srv.mu.Unlock()
	if timeout <= 0 || timeout > 200*time.Millisecond {
		t.Errorf("server saw grpc-timeout %v, want (0, 200ms]", timeout)
	}
	select {
	case err := <-srv.waitDone:
		if err == nil {
			t.Error("server context not done")
		}
	case <-time.After(10 * time.Second):
		t.Error("the server never saw the deadline")
	}
}

func TestInvokeCancel(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	started, err := s.Invoke(t.Context(), input(srv.Addr, "Wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("the call never reached the server")
	}
	if !s.ops.Cancel(started.OpID) {
		t.Fatal("Cancel found no operation")
	}
	items, done := rec.wait(t, started.OpID)
	if done.Status != operation.StatusCancelled {
		t.Fatalf("done %+v", done)
	}
	// The headers may or may not have arrived before the cancel.
	if got := types(items); got != "request,headers,end" && got != "request,end" {
		t.Errorf("items %s", got)
	}
	if end := items[len(items)-1]; end.Status.Name != "CANCELLED" {
		t.Errorf("end %+v", end.Status)
	}
	select {
	case err := <-srv.waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("server context ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the server never saw the cancellation")
	}
}

func TestInvokeConnectionFailure(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, true)
	s, rec := newCallService(t)
	in := input(srv.Addr, "Echo")
	in.TLS = true // verification on: the test CA is not trusted
	_, done := invoke(t, s, rec, in)
	if done.Status != operation.StatusError || done.Error.Kind != string(errs.TLS) {
		t.Errorf("done %+v %+v", done, done.Error)
	}
}

func TestInvokeRejected(t *testing.T) {
	t.Parallel()
	s, _ := newCallService(t)
	neg := int64(-1)
	tests := []struct {
		name  string
		edit  func(*InvokeInputDTO)
		code  string
		check func(t *testing.T, e *errs.Error)
	}{
		{name: "variable in address", edit: func(in *InvokeInputDTO) { in.Address = "{{host}}:1" }, code: CodeVariablesUnsupported},
		{name: "variable in message", edit: func(in *InvokeInputDTO) { in.Message = `{"text": "{{x}}"}` }, code: CodeVariablesUnsupported},
		{name: "variable in metadata", edit: func(in *InvokeInputDTO) {
			in.Metadata = []MetadataRowDTO{{Name: "x-a", Value: "{{v}}", Enabled: true}}
		}, code: CodeVariablesUnsupported},
		{name: "no method", edit: func(in *InvokeInputDTO) { in.Method = "" }, code: CodeNoMethod},
		{name: "no files", edit: func(in *InvokeInputDTO) { in.Files = nil }, code: CodeNoProto},
		{name: "unknown service", edit: func(in *InvokeInputDTO) { in.Service = "calls.v1.Nope" }, code: CodeMethodNotFound},
		{name: "unknown method", edit: func(in *InvokeInputDTO) { in.Method = "Nope" }, code: CodeMethodNotFound},
		{name: "invalid JSON", edit: func(in *InvokeInputDTO) { in.Message = "{\n  \"text\": }" }, code: CodeInvalidMessage,
			check: func(t *testing.T, e *errs.Error) {
				if e.Details["line"] != "2" || e.Details["column"] == "" || e.Details["type"] != "calls.v1.Req" {
					t.Errorf("details %v", e.Details)
				}
			}},
		{name: "unknown field", edit: func(in *InvokeInputDTO) { in.Message = `{"nope": 1}` }, code: CodeInvalidMessage,
			check: func(t *testing.T, e *errs.Error) {
				if !strings.Contains(e.Message, `"nope"`) {
					t.Errorf("message %q", e.Message)
				}
			}},
		{name: "bad binary metadata", edit: func(in *InvokeInputDTO) {
			in.Metadata = []MetadataRowDTO{{Name: "x-a", Value: "1", Enabled: true}, {Name: "x-b-bin", Value: "not base64!", Enabled: true}}
		}, code: CodeInvalidBinaryMetadata, check: func(t *testing.T, e *errs.Error) {
			if e.Details["row"] != "1" || e.Details["key"] != "x-b-bin" {
				t.Errorf("details %v", e.Details)
			}
		}},
		{name: "reserved metadata", edit: func(in *InvokeInputDTO) {
			in.Metadata = []MetadataRowDTO{{Name: "grpc-timeout", Value: "1S", Enabled: true}}
		}, code: transport.CodeReservedMetadata},
		{name: "negative deadline", edit: func(in *InvokeInputDTO) { in.DeadlineMs = &neg }, code: CodeInvalidSettings},
		{name: "negative connect timeout", edit: func(in *InvokeInputDTO) { in.ConnectTimeoutMs = &neg }, code: CodeInvalidSettings},
		{name: "empty address", edit: func(in *InvokeInputDTO) { in.Address = " " }, code: transport.CodeInvalidAddress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input("localhost:1", "Echo")
			tt.edit(&in)
			_, err := s.Invoke(t.Context(), in)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != tt.code {
				t.Fatalf("Invoke = %v, want code %s", err, tt.code)
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
	if n := s.ops.Running(); n != 0 {
		t.Errorf("%d operations started", n)
	}

	unavailable := New(nil, nil, nil, nil, nil)
	if _, err := unavailable.Invoke(t.Context(), input("localhost:1", "Echo")); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("Invoke without registry = %v", err)
	}
}

func TestInvokeConnectTimeoutClients(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newCallService(t)
	for _, ms := range []int64{0, 5000} {
		in := input(srv.Addr, "Echo")
		in.ConnectTimeoutMs = &ms
		if _, done := invoke(t, s, rec, in); done.Status != operation.StatusOK {
			t.Errorf("connect timeout %d: done %+v %+v", ms, done, done.Error)
		}
	}
	if len(s.clients) != 2 {
		t.Errorf("%d clients, want one per connect timeout", len(s.clients))
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Error(err)
	}
}

func TestCallEventJSON(t *testing.T) {
	t.Parallel()
	details, err := rpcstatus.Marshal(&rpcstatus.Status{Code: 5, Message: "x", Details: []rpcstatus.Detail{{TypeURL: "t/a.B", Value: []byte{1}}}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(CallEventDTO{Type: CallEnd, Status: toStatusDTO(transport.Status{Code: transport.NotFound, Message: "x", Details: details}, nil),
		Metadata: toMetadataDTOs(transport.Metadata{{Key: "a", Value: "b"}, {Key: "c-bin", Value: "\x00"}})})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"end","at":"","method":"","metadata":[{"key":"a","value":"b","binary":false},{"key":"c-bin","value":"AA==","binary":true}],` +
		`"json":"","size":0,"index":0,"decodeError":"","raw":"","deadlineMs":0,"transport":"","compression":"","clientStreaming":false,"status":{"code":5,"name":"NOT_FOUND","message":"x",` +
		`"details":"` + base64.StdEncoding.EncodeToString(details) + `","rich":{"code":5,"name":"NOT_FOUND","message":"x",` +
		`"details":[{"typeUrl":"t/a.B","type":"a.B","known":false,"json":"","error":"","raw":"AQ=="}],"error":""}},"messages":0,"sent":0,"elapsedMs":0,"tests":null}`
	if string(b) != want {
		t.Errorf("JSON\n got %s\nwant %s", b, want)
	}
}
