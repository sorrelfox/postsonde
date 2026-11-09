package servers

import (
	"crypto/x509"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	grpcservice "github.com/sorrelfox/postsonde/internal/app/services/grpc"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// The gRPC mock server (SSRV-07) end to end: defined through the service
// with project-relative .proto paths, and called by the app's own gRPC
// client (GRPCService.Invoke, the operation the gRPC request editor runs)
// over h2c and TLS with the local CA: unary, server streaming and an
// error status. grpcurl against the same server is left to a manual check
// (it cannot be installed here).

const mockProto = `syntax = "proto3";

package shop.v1;

service Orders {
  rpc GetOrder(GetOrderRequest) returns (Order);
  rpc ListOrders(GetOrderRequest) returns (stream Order);
  rpc CancelOrder(GetOrderRequest) returns (Order);
}

message GetOrderRequest { string id = 1; }
message Order { string id = 1; int64 total = 2; }
`

// callRecorder collects the gRPC service's operation events.
type callRecorder struct {
	mu    sync.Mutex
	items map[string][]grpcservice.CallEventDTO
	done  map[string]operation.DoneEventDTO
}

func (r *callRecorder) emit(_ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch d := data.(type) {
	case operation.DataEventDTO[grpcservice.CallEventDTO]:
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
	case operation.DoneEventDTO:
		r.done[d.OpID] = d
	}
}

func (r *callRecorder) wait(t *testing.T, id string) ([]grpcservice.CallEventDTO, operation.DoneEventDTO) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		d, ok := r.done[id]
		items := r.items[id]
		r.mu.Unlock()
		if ok {
			return items, d
		}
		if time.Now().After(deadline) {
			t.Fatal("no done event")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newGRPCClient is a gRPC request service without a project, which may
// read the .proto files of the allowed directories.
func newGRPCClient(t *testing.T, roots *x509.CertPool, allowed ...string) (*grpcservice.GRPCService, *callRecorder) {
	t.Helper()
	rec := &callRecorder{items: map[string][]grpcservice.CallEventDTO{}, done: map[string]operation.DoneEventDTO{}}
	reg := operation.NewRegistry(rec.emit, nil)
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	opts := []grpcservice.Option{grpcservice.WithAllowedDirs(func() []string { return allowed })}
	if roots != nil {
		opts = append(opts, grpcservice.WithTrust(func() (*x509.CertPool, error) { return roots, nil }))
	}
	svc := grpcservice.New(nil, reg, dialer, nil, slog.New(slog.DiscardHandler), opts...)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return svc, rec
}

func TestGRPCMockEndToEnd(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "protos"), 0o755); err != nil {
		t.Fatal(err)
	}
	protoPath := filepath.Join(project, "protos", "orders.proto")
	if err := os.WriteFile(protoPath, []byte(mockProto), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	caPath := filepath.Join(dataDir, "servers", "tls", "ca.pem")
	rec := &recorder{}
	svc := newService(rec.emit, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second},
		WithProjectRoot(func() string { return project }))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	tlsSvc := newTLSService(t, dataDir, []string{caPath}, WithProjectRoot(func() string { return project }))

	config := ConfigDTO{GRPCMock: &GRPCMockConfigDTO{
		Files: []string{"protos/orders.proto"}, ImportPaths: []string{}, Reflection: true,
		Methods: []GRPCMockMethodDTO{
			{Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "o-1", "total": "42"}`},
				Headers: []MockHeaderDTO{{Enabled: true, Name: "x-mock", Value: "yes"}}},
			{Service: "shop.v1.Orders", Method: "ListOrders", Messages: []string{`{"id": "1"}`, `{"id": "2"}`, `{"id": "3"}`}, IntervalMs: 10},
			{Service: "shop.v1.Orders", Method: "CancelOrder", Status: 9, StatusMessage: "already shipped",
				Trailers: []MockHeaderDTO{{Enabled: true, Name: "x-reason", Value: "shipped"}}},
		},
	}}

	for _, withTLS := range []bool{false, true} {
		t.Run("tls="+strconv.FormatBool(withTLS), func(t *testing.T) {
			s := svc
			in := CreateInputDTO{Type: grpcmock.TypeName, Config: config}
			var roots *x509.CertPool
			if withTLS {
				s = tlsSvc
				in.TLS = &ServerTLSInputDTO{Certificate: core.CertGenerated}
			}
			created, err := s.Create(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if c := created.Config.GRPCMock; c == nil || len(c.Methods) != 3 || c.Methods[0].Bidi != grpcmock.BidiEach {
				t.Fatalf("config = %+v", created.Config)
			}
			running, err := s.Start(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if withTLS {
				if roots, err = tlsconf.NewTrust(func() []string { return []string{caPath} }).Roots(); err != nil {
					t.Fatal(err)
				}
			}
			client, calls := newGRPCClient(t, roots, project)
			invoke := func(method, message string) ([]grpcservice.CallEventDTO, operation.DoneEventDTO) {
				t.Helper()
				started, err := client.Invoke(t.Context(), grpcservice.InvokeInputDTO{
					Files: []string{protoPath}, Address: "localhost:" + strconv.Itoa(running.BoundPort), TLS: withTLS,
					Service: "shop.v1.Orders", Method: method, Message: message,
				})
				if err != nil {
					t.Fatal(err)
				}
				return calls.wait(t, started.OpID)
			}

			items, done := invoke("GetOrder", `{"id": "o-1"}`)
			var messages []string
			var header string
			for _, it := range items {
				switch it.Type {
				case "message":
					messages = append(messages, it.JSON)
				case "headers":
					for _, md := range it.Metadata {
						if md.Key == "x-mock" {
							header = md.Value
						}
					}
				}
			}
			if done.Status != operation.StatusOK || len(messages) != 1 || !strings.Contains(messages[0], `"total": "42"`) || header != "yes" {
				t.Errorf("unary: %s %q header %q", done.Status, messages, header)
			}

			items, done = invoke("ListOrders", `{}`)
			messages = nil
			for _, it := range items {
				if it.Type == "message" {
					messages = append(messages, it.JSON)
				}
			}
			if done.Status != operation.StatusOK || len(messages) != 3 || !strings.Contains(messages[2], `"id": "3"`) {
				t.Errorf("server streaming: %s %q", done.Status, messages)
			}

			items, done = invoke("CancelOrder", `{"id": "o-2"}`)
			end := items[len(items)-1]
			if done.Status != operation.StatusError || end.Type != "end" || end.Status == nil ||
				end.Status.Name != "FAILED_PRECONDITION" || end.Status.Message != "already shipped" {
				t.Errorf("error status: %s %+v", done.Status, end)
			}
			var reason bool
			for _, md := range end.Metadata {
				reason = reason || (md.Key == "x-reason" && md.Value == "shipped")
			}
			if !reason {
				t.Errorf("trailers = %+v", end.Metadata)
			}

			// The traffic has every call with its request decoded as JSON.
			traffic, err := s.Traffic(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			var ends []string
			var sawRequest bool
			for _, e := range traffic {
				if e.GRPC == nil {
					continue
				}
				if e.Kind == "response" {
					ends = append(ends, e.GRPC.CodeName)
				}
				if e.Kind == "message" && e.Direction == "in" && strings.Contains(e.Text, `"id": "o-2"`) && e.GRPC.Type == "shop.v1.GetOrderRequest" {
					sawRequest = true
				}
			}
			if strings.Join(ends, ",") != "OK,OK,FAILED_PRECONDITION" || !sawRequest {
				t.Errorf("traffic ends = %v, request logged = %v", ends, sawRequest)
			}
		})
	}
}

// TestGRPCMockRelativePathsNeedProject: without a project, a relative
// .proto path fails the start with a clear error.
func TestGRPCMockRelativePathsNeedProject(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	created, err := svc.Create(t.Context(), CreateInputDTO{Type: grpcmock.TypeName, Config: ConfigDTO{GRPCMock: &GRPCMockConfigDTO{Files: []string{"protos/x.proto"}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Start(t.Context(), created.ID)
	if codeOf(err) != grpcmock.CodeRelativePath {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{}); codeOf(err) != grpcmock.CodeInvalidConfig {
		t.Errorf("missing config: %v", err)
	}
}
