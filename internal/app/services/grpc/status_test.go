package grpc

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
	"github.com/sorrelfox/postsonde/internal/core/grpc/rpcstatus"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// echoProto describes grpctest's Status method, which answers with the
// status given in the x-status-* metadata.
const echoProto = `syntax = "proto3";

package grpctest;

message Msg {
  string text = 1;
}

service Echo {
  rpc Status(Msg) returns (Msg);
}
`

// newStatusService opens a project with calls.proto and echo.proto.
func newStatusService(t *testing.T) (*GRPCService, *recorder) {
	t.Helper()
	dir := filepath.Join(tempDir(t), "status")
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Status"}`)
	writeFile(t, dir, "proto/calls.proto", callsProto)
	writeFile(t, dir, "proto/echo.proto", echoProto)
	manager := coreworkspace.New(tempDir(t))
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	reg := operation.NewRegistry(rec.emit, nil)
	t.Cleanup(func() { _ = reg.Shutdown(t.Context()) })
	return New(manager, reg, netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), nil, nil), rec
}

// statusInput calls grpctest.Echo/Status, which ends with code, message
// and details (none when nil).
func statusInput(addr string, code int, message string, details []byte, trailersOnly bool) InvokeInputDTO {
	in := InvokeInputDTO{
		RequestID: "req_1",
		Files:     []string{"proto/calls.proto", "proto/echo.proto"},
		Address:   addr,
		Service:   "grpctest.Echo",
		Method:    "Status",
		Message:   `{"text": "hi"}`,
		Metadata: []MetadataRowDTO{
			{Name: "x-status-code", Value: strconv.Itoa(code), Enabled: true},
			{Name: "x-status-message", Value: message, Enabled: message != ""},
		},
	}
	if details != nil {
		in.Metadata = append(in.Metadata, MetadataRowDTO{Name: "x-status-details-bin", Value: base64.StdEncoding.EncodeToString(details), Enabled: true})
	}
	if trailersOnly {
		in.Metadata = append(in.Metadata, MetadataRowDTO{Name: "x-trailers-only", Value: "1", Enabled: true})
	}
	return in
}

func keys(md []MetadataDTO) map[string]string {
	out := map[string]string{}
	for _, e := range md {
		out[e.Key] = e.Value
	}
	return out
}

// A successful call keeps the response headers and the trailers apart:
// the status is only in the trailers.
func TestInvokeHeadersAndTrailers(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	s, rec := newStatusService(t)
	in := input(srv.Addr, "Echo")
	in.Files = []string{"proto/calls.proto"}
	in.Metadata = []MetadataRowDTO{{Name: "x-trace", Value: "t1", Enabled: true}}
	items, done := invoke(t, s, rec, in)
	if done.Status != operation.StatusOK {
		t.Fatalf("done %+v", done)
	}
	if got := types(items); got != "request,headers,message,end" {
		t.Fatalf("items %s", got)
	}
	headers, trailers := keys(items[1].Metadata), keys(items[3].Metadata)
	if headers["content-type"] != "application/grpc" || headers["x-trace"] != "t1" {
		t.Errorf("headers %v", headers)
	}
	for _, k := range []string{"grpc-status", "grpc-message", "t-x-trace"} {
		if _, ok := headers[k]; ok {
			t.Errorf("headers hold %s: %v", k, headers)
		}
	}
	if trailers["grpc-status"] != "0" || trailers["t-x-trace"] != "t1" {
		t.Errorf("trailers %v", trailers)
	}
	if _, ok := trailers["content-type"]; ok {
		t.Errorf("trailers hold content-type: %v", trailers)
	}
	st := items[3].Status
	if st == nil || st.Code != 0 || st.Name != "OK" || st.Message != "" || st.Details != "" || st.Rich != nil {
		t.Errorf("status %+v", st)
	}
}

// Every status code arrives with its name and message, after messages
// (in trailers) and without (trailers-only), and maps to the call's
// error kind by class.
func TestInvokeStatusCodes(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	s, rec := newStatusService(t)
	kinds := map[int]string{1: "cancelled", 4: "timeout", 14: "network"}
	for code := 0; code <= 17; code++ {
		c := code
		if c == 17 {
			c = 42 // outside the known range
		}
		for _, trailersOnly := range []bool{false, true} {
			if c == 0 && trailersOnly {
				continue // OK without a message breaks a unary call's cardinality (TestInvokeCardinality)
			}
			t.Run(strconv.Itoa(c)+"/trailersOnly="+strconv.FormatBool(trailersOnly), func(t *testing.T) {
				msg := ""
				if c != 0 {
					msg = "failed: 100% done" // percent-encoded on the wire
				}
				items, done := invoke(t, s, rec, statusInput(srv.Addr, c, msg, nil, trailersOnly))
				end := items[len(items)-1]
				name := transport.Code(uint32(c)).String()
				if end.Type != CallEnd || end.Status == nil || end.Status.Code != c || end.Status.Name != name || end.Status.Message != msg || end.Status.Rich != nil {
					t.Fatalf("end %+v status %+v", end, end.Status)
				}
				wantTypes := "request,headers,message,end"
				if trailersOnly {
					wantTypes = "request,end"
				}
				if got := types(items); got != wantTypes {
					t.Errorf("items %s, want %s", got, wantTypes)
				}
				if tr := keys(end.Metadata); tr["grpc-status"] != strconv.Itoa(c) {
					t.Errorf("trailers %v", tr)
				}
				if c == 0 {
					if done.Status != operation.StatusOK || done.Error != nil {
						t.Errorf("done %+v", done)
					}
					return
				}
				if done.Status != operation.StatusError || done.Error == nil {
					t.Fatalf("done %+v", done)
				}
				kind := kinds[c]
				if kind == "" {
					kind = "protocol"
				}
				if done.Error.Code != transport.CodeStatus || done.Error.Kind != kind ||
					done.Error.Details["grpc_status"] != name || done.Error.Details["grpc_code"] != strconv.Itoa(c) {
					t.Errorf("done error %+v", done.Error)
				}
			})
		}
	}
}

func message(t *testing.T, p *desc.Pool, name, js string) *dynamic.Message {
	t.Helper()
	md := p.FindMessage(name)
	if md == nil {
		t.Fatalf("no type %s", name)
	}
	m := dynamic.New(md)
	if err := (protojson.UnmarshalOptions{Resolver: p}).Unmarshal([]byte(js), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func compactOrEmpty(t *testing.T, s string) string {
	t.Helper()
	if s == "" {
		return ""
	}
	return compactJSON(t, s)
}

// A failing call's google.rpc.Status details are decoded: the standard
// error details, a type from the request's .proto files, an unknown type
// and a detail that does not decode.
func TestInvokeRichStatus(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	s, rec := newStatusService(t)
	rpc, err := rpcstatus.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	linked, _, _, err := s.link(t.Context(), []string{"proto/calls.proto"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	details, err := rpcstatus.Marshal(&rpcstatus.Status{
		Code:    3,
		Message: "the order is invalid",
		Details: []rpcstatus.Detail{
			{Message: message(t, rpc, "google.rpc.ErrorInfo", `{"reason": "OUT_OF_STOCK", "domain": "shop.example", "metadata": {"sku": "A1"}}`)},
			{Message: message(t, rpc, "google.rpc.BadRequest", `{"fieldViolations": [{"field": "items[0].qty", "description": "must be positive"}]}`)},
			{Message: message(t, rpc, "google.rpc.RetryInfo", `{"retryDelay": "2s"}`)},
			{Message: message(t, rpc, "google.rpc.QuotaFailure", `{"violations": [{"subject": "project:1", "description": "daily limit", "futureQuotaValue": "10"}]}`)},
			{Message: message(t, rpc, "google.rpc.PreconditionFailure", `{"violations": [{"type": "TOS", "subject": "user", "description": "not accepted"}]}`)},
			{Message: message(t, rpc, "google.rpc.ResourceInfo", `{"resourceType": "order", "resourceName": "orders/1"}`)},
			{Message: message(t, rpc, "google.rpc.RequestInfo", `{"requestId": "r-1"}`)},
			{Message: message(t, rpc, "google.rpc.Help", `{"links": [{"description": "docs", "url": "https://example.com"}]}`)},
			{Message: message(t, rpc, "google.rpc.LocalizedMessage", `{"locale": "en-US", "message": "Out of stock"}`)},
			{Message: message(t, rpc, "google.rpc.DebugInfo", `{"stackEntries": ["a.go:1"], "detail": "boom"}`)},
			{Message: message(t, linked.Pool, "calls.v1.Reply", `{"text": "custom", "count": "3"}`)},
			{TypeURL: "type.googleapis.com/other.v1.Secret", Value: []byte{0x08, 0x01}},
			{TypeURL: "type.googleapis.com/google.rpc.ErrorInfo", Value: []byte{0x0a, 0x09, 'x'}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, trailersOnly := range []bool{false, true} {
		items, done := invoke(t, s, rec, statusInput(srv.Addr, 3, "the order is invalid", details, trailersOnly))
		if done.Status != operation.StatusError || done.Error.Details["grpc_status"] != "INVALID_ARGUMENT" {
			t.Fatalf("done %+v", done)
		}
		st := items[len(items)-1].Status
		if st == nil || st.Code != 3 || st.Details != base64.StdEncoding.EncodeToString(details) || st.Rich == nil {
			t.Fatalf("status %+v", st)
		}
		rich := st.Rich
		if rich.Code != 3 || rich.Name != "INVALID_ARGUMENT" || rich.Message != "the order is invalid" || rich.Error != "" || len(rich.Details) != 13 {
			t.Fatalf("rich %+v", rich)
		}
		want := []StatusDetailDTO{
			{Type: "google.rpc.ErrorInfo", Known: true, JSON: `{"reason":"OUT_OF_STOCK","domain":"shop.example","metadata":{"sku":"A1"}}`},
			{Type: "google.rpc.BadRequest", Known: true, JSON: `{"fieldViolations":[{"field":"items[0].qty","description":"must be positive"}]}`},
			{Type: "google.rpc.RetryInfo", Known: true, JSON: `{"retryDelay":"2s"}`},
			{Type: "google.rpc.QuotaFailure", Known: true, JSON: `{"violations":[{"subject":"project:1","description":"daily limit","futureQuotaValue":"10"}]}`},
			{Type: "google.rpc.PreconditionFailure", Known: true, JSON: `{"violations":[{"type":"TOS","subject":"user","description":"not accepted"}]}`},
			{Type: "google.rpc.ResourceInfo", Known: true, JSON: `{"resourceType":"order","resourceName":"orders/1"}`},
			{Type: "google.rpc.RequestInfo", Known: true, JSON: `{"requestId":"r-1"}`},
			{Type: "google.rpc.Help", Known: true, JSON: `{"links":[{"description":"docs","url":"https://example.com"}]}`},
			{Type: "google.rpc.LocalizedMessage", Known: true, JSON: `{"locale":"en-US","message":"Out of stock"}`},
			{Type: "google.rpc.DebugInfo", Known: true, JSON: `{"stackEntries":["a.go:1"],"detail":"boom"}`},
			{Type: "calls.v1.Reply", Known: true, JSON: `{"text":"custom","count":"3"}`},
			{Type: "other.v1.Secret", Raw: "CAE="},
			{Type: "google.rpc.ErrorInfo", Known: true, Raw: "Cgl4", Error: "x"},
		}
		for i, w := range want {
			got := rich.Details[i]
			got.JSON = compactOrEmpty(t, got.JSON)
			if got.TypeURL != "type.googleapis.com/"+w.Type {
				t.Errorf("detail %d typeUrl %q", i, got.TypeURL)
			}
			if (got.Error != "") != (w.Error != "") {
				t.Errorf("detail %d error %q", i, got.Error)
			}
			got.TypeURL, got.Error, w.Error = "", "", ""
			if got != w {
				t.Errorf("detail %d\n got %+v\nwant %+v", i, got, w)
			}
		}
	}
}

func TestRichStatusNotAStatus(t *testing.T) {
	t.Parallel()
	st := toStatusDTO(transport.Status{Code: transport.Internal, Details: []byte{0x0a, 0x05, 'a'}}, nil)
	if st.Details != "CgVh" || st.Rich == nil || st.Rich.Error == "" || st.Rich.Details == nil || len(st.Rich.Details) != 0 {
		t.Errorf("status %+v rich %+v", st, st.Rich)
	}
	b, err := json.Marshal(st.Rich)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(b, &shape); err != nil || len(shape) != 5 {
		t.Errorf("rich JSON %s", b)
	}
}
