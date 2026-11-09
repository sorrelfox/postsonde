package reflection

import (
	"context"
	"slices"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/descproto"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// The test schema: two services in two files importing a shared file,
// which imports a well-known type.
var schemaSources = map[string]string{
	"greet/v1/types.proto": `syntax = "proto3";
package greet.v1;
import "google/protobuf/timestamp.proto";

enum Tone {
  TONE_UNSPECIFIED = 0;
  TONE_WARM = 1;
}

message HelloRequest {
  string name = 1;
  optional Tone tone = 2;
  repeated string cc = 3 [json_name = "copies"];
}

message Greeting {
  string text = 1;
  google.protobuf.Timestamp at = 2;
  map<string, string> tags = 3;
  oneof extra {
    int64 count = 4;
    HelloRequest echo = 5;
  }
}
`,
	"greet/v1/greeter.proto": `syntax = "proto3";
package greet.v1;
import "greet/v1/types.proto";
import "google/protobuf/empty.proto";

service Greeter {
  rpc Hello(HelloRequest) returns (Greeting);
  rpc Stream(HelloRequest) returns (stream Greeting);
  rpc Collect(stream HelloRequest) returns (google.protobuf.Empty);
  rpc Chat(stream HelloRequest) returns (stream Greeting);
}
`,
	"admin/v1/admin.proto": `syntax = "proto3";
package admin.v1;
import "greet/v1/types.proto";
import "google/protobuf/empty.proto";

service Admin {
  rpc Ping(google.protobuf.Empty) returns (greet.v1.Greeting);
}
`,
}

var schemaInputs = []string{"greet/v1/greeter.proto", "admin/v1/admin.proto"}

var (
	sourceOnce sync.Once
	sourceRes  *linker.Result
	sourceErr  error
)

// sourceSchema links the test schema from source once.
func sourceSchema(t testing.TB) *linker.Result {
	t.Helper()
	sourceOnce.Do(func() {
		fsys := fstest.MapFS{}
		for name, src := range schemaSources {
			fsys[name] = &fstest.MapFile{Data: []byte(src)}
		}
		sourceRes, sourceErr = linker.Load(context.Background(), linker.Options{
			Files: schemaInputs, ImportPaths: []string{"."}, FS: fsys,
		})
		if sourceErr == nil {
			sourceErr = sourceRes.Err()
		}
	})
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	return sourceRes
}

// descriptors returns the schema's files as FileDescriptorProtos; with
// wellKnown, the well-known files it imports are included, as grpc-go
// serves them.
func descriptors(t testing.TB, wellKnown bool) [][]byte {
	t.Helper()
	res := sourceSchema(t)
	var out [][]byte
	for _, f := range res.Pool.Files() {
		if f.Builtin && (!wellKnown || f.Path == "google/protobuf/descriptor.proto") {
			continue
		}
		b, err := descproto.Marshal(f, res.Pool)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// newServer starts a reflection test server.
func newServer(t *testing.T, withTLS bool, o grpctest.ReflectionOptions) *grpctest.Server {
	t.Helper()
	srv := grpctest.NewServer(t, withTLS)
	if o.Files == nil {
		o.Files = descriptors(t, true)
	}
	srv.EnableReflection(o)
	return srv
}

// recorder is an Invoker that records the methods called.
type recorder struct {
	c       *transport.Client
	mu      sync.Mutex
	methods []string
}

func (r *recorder) Invoke(ctx context.Context, o transport.CallOptions) (*transport.Stream, error) {
	r.mu.Lock()
	r.methods = append(r.methods, o.Method)
	r.mu.Unlock()
	return r.c.Invoke(ctx, o)
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.methods)
}

func newInvoker(t *testing.T, srv *grpctest.Server) *recorder {
	t.Helper()
	c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{Transport: httpclient.TransportOptions{RootCAs: srv.RootCAs}})
	t.Cleanup(c.CloseIdleConnections)
	return &recorder{c: c}
}

func fetchOptions(srv *grpctest.Server) FetchOptions {
	return FetchOptions{Call: transport.CallOptions{Address: srv.Addr, TLS: srv.TLS}}
}

func fileNames(s *Snapshot) []string {
	var out []string
	for _, f := range s.Files {
		out = append(out, f.Name)
	}
	slices.Sort(out)
	return out
}
