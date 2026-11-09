package reflection

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestExternalServer loads services by reflection from a real server at
// $POSTSONDE_GRPC_REFLECTION_ADDR (plaintext h2c; append ",v1alpha" to
// force that version) and requires them to link without errors. It is
// skipped when the variable is unset.
func TestExternalServer(t *testing.T) {
	spec := os.Getenv("POSTSONDE_GRPC_REFLECTION_ADDR")
	if spec == "" {
		t.Skip("POSTSONDE_GRPC_REFLECTION_ADDR is not set")
	}
	addr, version, _ := strings.Cut(spec, ",")
	c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{})
	defer c.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := NewResolver(c, localDir(t.TempDir()))
	sc, err := r.Refresh(ctx, FetchOptions{Call: transport.CallOptions{Address: addr}, Version: Version(version)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s over %s: %d files, problems %+v", addr, sc.Snapshot.Version, len(sc.Snapshot.Files), sc.Problems)
	for _, d := range sc.Diagnostics {
		if d.Severity == parser.SeverityError {
			t.Errorf("%v", d)
		} else {
			t.Logf("%v", d)
		}
	}
	for _, svc := range sc.Services {
		for _, m := range svc.Methods {
			if m.Input == nil || m.Output == nil {
				t.Errorf("%s: unresolved types", m.FullName)
			}
			t.Logf("%s(%s) returns (%s)", m.FullName, m.InputName, m.OutputName)
		}
	}
	cached, err := r.LoadCached(ctx, addr)
	if err != nil || len(cached.Services) != len(sc.Services) {
		t.Errorf("cache: %v", err)
	}
}
