package servers

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
)

// PRV-10: the .proto files and import paths of a gRPC mock server
// are read from the project folder or an allowed directory only; one
// outside fails the start with fileaccess.not_allowed, and nothing is
// served from it.
func TestGRPCMockProtoAllowlist(t *testing.T) {
	t.Parallel()
	project, allowed, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{project, allowed, outside} {
		if err := os.WriteFile(filepath.Join(dir, "orders.proto"), []byte(mockProto), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "orders.proto"), filepath.Join(project, "escape.proto")); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	svc := newService(nil, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second},
		WithProjectRoot(func() string { return project }), WithAllowedDirs(func() []string { return []string{allowed} }))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	for _, tc := range []struct {
		name         string
		files, roots []string
		allowed      bool
	}{
		{"project-relative", []string{"orders.proto"}, nil, true},
		{"allowed directory", []string{filepath.Join(allowed, "orders.proto")}, []string{allowed}, true},
		{"outside", []string{filepath.Join(outside, "orders.proto")}, nil, false},
		{"import path outside", []string{"orders.proto"}, []string{outside}, false},
		{"link escaping", []string{"escape.proto"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created, err := svc.Create(t.Context(), CreateInputDTO{Type: grpcmock.TypeName,
				Config: ConfigDTO{GRPCMock: &GRPCMockConfigDTO{Files: tc.files, ImportPaths: tc.roots}}})
			if err != nil {
				t.Fatal(err)
			}
			running, err := svc.Start(t.Context(), created.ID)
			if tc.allowed {
				if err != nil {
					t.Fatalf("start: %v", err)
				}
				_, _ = svc.Stop(t.Context(), running.ID)
				return
			}
			e, ok := err.(*errs.Error)
			if !ok {
				t.Fatalf("start = %v", err)
			}
			if !strings.Contains(e.Message, "outside the directories allowed for file access") ||
				e.Code != fileaccess.CodeNotAllowed || e.Hint != fileaccess.HintAllow {
				t.Errorf("start = %s: %s", e.Code, e.Message)
			}
		})
	}
}
