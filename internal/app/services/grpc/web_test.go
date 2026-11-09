package grpc

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
)

// TestInvokeWeb calls unary and server-streaming methods over gRPC-Web
// (GRPC-25): binary and text framing, over HTTP/1.1 (plaintext and TLS)
// and HTTP/2 (TLS with ALPN h2).
func TestInvokeWeb(t *testing.T) {
	t.Parallel()
	servers := map[string]grpctest.ServerOptions{
		"plaintext": {},
		"tls h2":    {Security: grpctest.TLS},
		"tls http1": {Security: grpctest.TLS, HTTP1Only: true},
	}
	for name, o := range servers {
		for _, tr := range []string{"grpc-web", "grpc-web-text"} {
			t.Run(name+"/"+tr, func(t *testing.T) {
				t.Parallel()
				srv := newCallServerWith(t, o)
				s, rec := newCallService(t)
				base := func(method string) InvokeInputDTO {
					in := input(srv.Addr, method)
					in.Transport = tr
					in.TLS = o.Security != grpctest.H2C
					if in.TLS {
						verify := false // the test CA is not trusted by the system
						in.VerifyTLS = &verify
					}
					in.Metadata = []MetadataRowDTO{{Name: "x-id", Value: "42", Enabled: true}}
					return in
				}

				items, done := invoke(t, s, rec, base("Echo"))
				if done.Status != operation.StatusOK {
					t.Fatalf("Echo: done %+v, error %+v", done, done.Error)
				}
				if got := types(items); got != "request,headers,message,end" {
					t.Fatalf("Echo items %s", got)
				}
				if items[0].Transport != tr || items[0].Compression != "identity" {
					t.Errorf("request item transport %q compression %q", items[0].Transport, items[0].Compression)
				}
				if got := compactJSON(t, items[2].JSON); got != `{"text":"hi","count":"7"}` {
					t.Errorf("Echo message %s", got)
				}
				if ct, ok := md(items[1].Metadata, "content-type"); !ok || !strings.HasPrefix(ct.Value, "application/"+tr) {
					t.Errorf("headers %+v", items[1].Metadata)
				}
				if v, ok := md(items[3].Metadata, "t-x-id"); !ok || v.Value != "42" {
					t.Errorf("trailers %+v", items[3].Metadata)
				}
				if r := srv.LastRequest(); !strings.HasPrefix(r.Header.Get("Content-Type"), "application/"+tr+"+proto") {
					t.Errorf("request content-type %q", r.Header.Get("Content-Type"))
				}

				items, done = invoke(t, s, rec, base("Repeat"))
				if done.Status != operation.StatusOK || types(items) != "request,headers,message,message,message,end" {
					t.Fatalf("Repeat: %s, done %+v", types(items), done)
				}
				if got := compactJSON(t, items[4].JSON); got != `{"text":"#2","count":"7"}` {
					t.Errorf("Repeat third message %s", got)
				}

				items, done = invoke(t, s, rec, base("Fail"))
				if done.Status != operation.StatusError || done.Error.Code != transport.CodeStatus || done.Error.Details["grpc_status"] != "NOT_FOUND" {
					t.Fatalf("Fail: done %+v, error %+v", done, done.Error)
				}
				end := items[len(items)-1]
				if end.Status == nil || end.Status.Message != "no such order" || end.Status.Details != "AQID" {
					t.Errorf("Fail end %+v", end.Status)
				}

				for _, method := range []string{"Upload", "Chat"} {
					_, err := s.Invoke(t.Context(), base(method))
					e, ok := errors.AsType[*errs.Error](err)
					if !ok || e.Code != CodeWebClientStreaming || e.Kind != errs.Unsupported || e.Hint == "" {
						t.Errorf("%s over gRPC-Web: %v", method, err)
					}
				}
			})
		}
	}
}

// TestInvokeGzip sends compressed requests and reads compressed replies
// (GRPC-24) over gRPC and gRPC-Web.
func TestInvokeGzip(t *testing.T) {
	t.Parallel()
	for _, tr := range []string{"", "grpc", "grpc-web", "grpc-web-text"} {
		t.Run("transport="+tr, func(t *testing.T) {
			t.Parallel()
			srv := newCallServer(t, false)
			s, rec := newCallService(t)
			in := input(srv.Addr, "Repeat")
			in.Transport = tr
			in.Compression = "gzip"
			in.Message = `{"text": "` + strings.Repeat("squash", 200) + `"}`
			items, done := invoke(t, s, rec, in)
			if done.Status != operation.StatusOK || types(items) != "request,headers,message,message,message,end" {
				t.Fatalf("items %s, done %+v %+v", types(items), done, done.Error)
			}
			if items[0].Compression != "gzip" {
				t.Errorf("request compression %q", items[0].Compression)
			}
			if v, ok := md(items[1].Metadata, "grpc-encoding"); !ok || v.Value != "gzip" {
				t.Errorf("response headers %+v", items[1].Metadata)
			}
			if items[2].Size != 1203+4 || items[2].DecodeError != "" {
				t.Errorf("message size %d (%s)", items[2].Size, items[2].DecodeError)
			}
			r := srv.LastRequest()
			if r.Header.Get("Grpc-Encoding") != "gzip" || r.Header.Get("Grpc-Accept-Encoding") != "gzip" {
				t.Errorf("request headers %v", r.Header)
			}

			// A compressed reply to an uncompressed request is read too.
			in.Compression = ""
			in.Metadata = []MetadataRowDTO{{Name: "x-response-encoding", Value: "gzip", Enabled: true}}
			items, done = invoke(t, s, rec, in)
			if done.Status != operation.StatusOK || items[0].Compression != "identity" {
				t.Fatalf("uncompressed request: done %+v, compression %q", done, items[0].Compression)
			}
			if v, ok := md(items[1].Metadata, "grpc-encoding"); !ok || v.Value != "gzip" {
				t.Errorf("response headers %+v", items[1].Metadata)
			}
			if r := srv.LastRequest(); r.Header.Get("Grpc-Encoding") != "" {
				t.Errorf("uncompressed request sent grpc-encoding %q", r.Header.Get("Grpc-Encoding"))
			}
		})
	}
}

func TestInvokeTransportSettings(t *testing.T) {
	t.Parallel()
	s, _ := newCallService(t)
	for _, tt := range []struct{ transport, compression, field string }{
		{"grpc-websocket", "", "transport"},
		{"grpc", "zstd", "compression"},
	} {
		in := input("localhost:1", "Echo")
		in.Transport, in.Compression = tt.transport, tt.compression
		_, err := s.Invoke(t.Context(), in)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != CodeInvalidSettings || e.Details["field"] != tt.field {
			t.Errorf("%+v: %v", tt, err)
		}
	}
}
