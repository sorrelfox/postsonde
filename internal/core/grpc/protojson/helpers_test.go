package protojson

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

var (
	poolOnce sync.Once
	testPool *desc.Pool
	poolErr  error
)

// pool links the test .proto files in testdata once.
func pool(tb testing.TB) *desc.Pool {
	tb.Helper()
	poolOnce.Do(func() {
		res, err := linker.Load(context.Background(), linker.Options{
			Files:       []string{"jsontest.proto", "jsontest2.proto", "jsontest_editions.proto"},
			ImportPaths: []string{"testdata"},
		})
		if err != nil {
			poolErr = err
			return
		}
		if len(res.Diagnostics) > 0 {
			poolErr = fmt.Errorf("diagnostics: %v", res.Diagnostics)
			return
		}
		testPool = res.Pool
	})
	if poolErr != nil {
		tb.Fatal(poolErr)
	}
	return testPool
}

func msgType(tb testing.TB, name string) *desc.Message {
	tb.Helper()
	md := pool(tb).FindMessage(name)
	if md == nil {
		tb.Fatalf("no message %s", name)
	}
	return md
}

func TestSchemasLink(t *testing.T) {
	pool(t)
}
