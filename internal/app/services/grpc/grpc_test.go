package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyDir copies the linker's multi-root test project to dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeFile(t, dst, filepath.ToSlash(rel), string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

const greeter = `syntax = "proto3";

package demo.v1;

import "google/protobuf/empty.proto";

// Greeter says hello.
//   Twice, if asked.
service Greeter {
  // SayHello greets one person.
  rpc SayHello(HelloRequest) returns (HelloReply);
  rpc Chat(stream HelloRequest) returns (stream HelloReply); // Chat talks back.
  rpc Old(google.protobuf.Empty) returns (HelloReply) {
    option deprecated = true;
  }
}

message HelloRequest { string name = 1; }
message HelloReply { string message = 1; }
`

// newProject copies the linker's test project into proto/ of a new project,
// adds demo/greeter.proto and opens it.
func newProject(t *testing.T, pick PathPicker) (*GRPCService, string) {
	t.Helper()
	dir := filepath.Join(tempDir(t), "shop")
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop"}`)
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "grpc", "proto", "linker", "testdata", "project"))
	if err != nil {
		t.Fatal(err)
	}
	copyDir(t, src, filepath.Join(dir, "proto"))
	writeFile(t, dir, "demo/greeter.proto", greeter)
	manager := coreworkspace.New(tempDir(t))
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	return New(manager, nil, nil, pick, nil), dir
}

var shopRoots = []string{"proto/api", "proto/third_party", "proto/vendor"}

func describe(t *testing.T, s *GRPCService, in DescribeInputDTO) DescribeResultDTO {
	t.Helper()
	res, err := s.Describe(t.Context(), in)
	if err != nil {
		t.Fatalf("Describe(%+v) = %v", in, err)
	}
	return res
}

func methodKinds(svc ServiceDTO) map[string]string {
	out := map[string]string{}
	for _, m := range svc.Methods {
		out[m.Name] = m.Kind
	}
	return out
}

// TestDescribeProject links a multi-file project with three import roots
// and lists its services with streaming kinds.
func TestDescribeProject(t *testing.T) {
	t.Parallel()
	s, _ := newProject(t, nil)
	res := describe(t, s, DescribeInputDTO{
		Files:       []string{"proto/api/shop/v1/orders.proto", "proto/api/shop/v1/legacy_ext.proto", " "},
		ImportPaths: shopRoots,
	})
	for _, d := range res.Diagnostics {
		t.Errorf("diagnostic: %+v", d)
	}
	if res.Errors != 0 || res.Cached || res.Files < 8 {
		t.Errorf("result = errors %d cached %v files %d", res.Errors, res.Cached, res.Files)
	}
	i := slices.IndexFunc(res.Services, func(s ServiceDTO) bool { return s.FullName == "shop.v1.OrderService" })
	if i < 0 {
		t.Fatalf("services = %+v", res.Services)
	}
	svc := res.Services[i]
	if svc.Name != "OrderService" || svc.File != "shop/v1/orders.proto" {
		t.Errorf("service = %+v", svc)
	}
	want := map[string]string{
		"GetOrder": MethodUnary, "UpdateOrder": MethodUnary, "DeleteOrder": MethodUnary,
		"WatchOrders": MethodServerStream, "ImportOrders": MethodClientStream, "Sync": MethodBidi,
	}
	got := methodKinds(svc)
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: kind %q, want %q", name, got[name], kind)
		}
	}
	if m := svc.Methods[0]; m.Name != "GetOrder" || m.InputType != "shop.v1.GetOrderRequest" || m.OutputType != "shop.v1.Order" || m.FullName != "shop.v1.OrderService.GetOrder" {
		t.Errorf("GetOrder = %+v", m)
	}
	if !slices.IsSortedFunc(res.Services, func(a, b ServiceDTO) int { return strings.Compare(a.FullName, b.FullName) }) {
		t.Errorf("services not sorted: %+v", res.Services)
	}

	// The same setup again comes from the cache; Reload links again.
	if again := describe(t, s, DescribeInputDTO{Files: []string{"proto/api/shop/v1/orders.proto", "proto/api/shop/v1/legacy_ext.proto"}, ImportPaths: shopRoots}); !again.Cached || len(again.Services) != len(res.Services) {
		t.Errorf("second Describe cached = %v", again.Cached)
	}
	if again := describe(t, s, DescribeInputDTO{Files: []string{"proto/api/shop/v1/orders.proto", "proto/api/shop/v1/legacy_ext.proto"}, ImportPaths: shopRoots, Reload: true}); again.Cached {
		t.Error("Reload used the cache")
	}
}

// TestDescribeDTO fixes the DTO shape: comments, deprecated methods,
// streaming flags, well-known types.
func TestDescribeDTO(t *testing.T) {
	t.Parallel()
	s, dir := newProject(t, nil)
	// An absolute path works too.
	res := describe(t, s, DescribeInputDTO{Files: []string{filepath.Join(dir, "demo", "greeter.proto")}, ImportPaths: []string{"demo"}})
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/describe-greeter", append(data, '\n'))
}

// TestDescribeDiagnostics: a missing import root gives the linker's
// diagnostic with its position, a missing import path and a missing file
// are reported, and nothing to load is an empty result.
func TestDescribeDiagnostics(t *testing.T) {
	t.Parallel()
	s, _ := newProject(t, nil)

	res := describe(t, s, DescribeInputDTO{Files: []string{"proto/api/shop/v1/orders.proto"}, ImportPaths: []string{"proto/api", "proto/vendor", "proto/nowhere"}})
	if res.Errors == 0 {
		t.Fatalf("no errors: %+v", res.Diagnostics)
	}
	var missingImport, missingRoot bool
	for _, d := range res.Diagnostics {
		switch {
		case d.Code == "proto.import_not_found" && d.File == "shop/v1/orders.proto" && strings.Contains(d.Message, "google/api/annotations.proto"):
			missingImport = d.Severity == "error" && d.Line == 5 && d.Column > 0 && d.Path == "proto/api/shop/v1/orders.proto"
		case d.Code == CodeImportPathMissing:
			missingRoot = d.Severity == "warning" && d.File == "proto/nowhere" && d.Line == 0
		}
	}
	if !missingImport || !missingRoot {
		t.Errorf("diagnostics = %+v", res.Diagnostics)
	}
	// Services still come from what linked.
	if !slices.ContainsFunc(res.Services, func(s ServiceDTO) bool { return s.FullName == "shop.v1.OrderService" }) {
		t.Errorf("services = %+v", res.Services)
	}
	// A result with a missing import is not cached.
	if again := describe(t, s, DescribeInputDTO{Files: []string{"proto/api/shop/v1/orders.proto"}, ImportPaths: []string{"proto/api", "proto/vendor", "proto/nowhere"}}); again.Cached {
		t.Error("a result with a missing import was cached")
	}

	res = describe(t, s, DescribeInputDTO{Files: []string{"demo/missing.proto"}})
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "proto.file_not_found" || res.Diagnostics[0].File != "demo/missing.proto" || res.Errors != 1 {
		t.Errorf("missing file = %+v", res.Diagnostics)
	}

	res = describe(t, s, DescribeInputDTO{ImportPaths: []string{"proto/api"}})
	if res.Services == nil || len(res.Services) != 0 || res.Diagnostics == nil || len(res.Diagnostics) != 0 || res.Files != 0 {
		t.Errorf("no files = %+v", res)
	}

	// Relative paths need an open project.
	noProject := New(coreworkspace.New(tempDir(t)), nil, nil, nil, nil)
	res, err := noProject.Describe(t.Context(), DescribeInputDTO{Files: []string{"demo/greeter.proto"}})
	if err != nil || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != CodeNoProject || res.Errors != 1 {
		t.Errorf("no project = %+v, %v", res, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Describe(ctx, DescribeInputDTO{Files: []string{"demo/greeter.proto"}}); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled Describe = %v", err)
	}
}

// TestDescribeCacheInvalidation: changing a file or one of its imports
// links again; creating a missing import fixes the result.
func TestDescribeCacheInvalidation(t *testing.T) {
	t.Parallel()
	s, dir := newProject(t, nil)
	in := DescribeInputDTO{Files: []string{"demo/greeter.proto"}}
	first := describe(t, s, in)
	if first.Cached || len(first.Services) != 1 || len(first.Services[0].Methods) != 3 {
		t.Fatalf("first = %+v", first)
	}
	if second := describe(t, s, in); !second.Cached {
		t.Fatal("not cached")
	}

	// Edit the file: a fourth method, a different size and time.
	changed := strings.Replace(greeter, "service Greeter {", "service Greeter {\n  rpc Ping(HelloRequest) returns (HelloReply);", 1)
	writeFile(t, dir, "demo/greeter.proto", changed)
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "demo", "greeter.proto"), later, later); err != nil {
		t.Fatal(err)
	}
	third := describe(t, s, in)
	if third.Cached || len(third.Services[0].Methods) != 4 {
		t.Errorf("after the change = cached %v, %+v", third.Cached, third.Services)
	}

	// A file that imports a missing file, which is then created.
	writeFile(t, dir, "demo/uses.proto", "syntax = \"proto3\";\npackage demo.v1;\nimport \"demo/types.proto\";\nservice Uses { rpc Do(T) returns (T); }\n")
	in = DescribeInputDTO{Files: []string{"demo/uses.proto"}, ImportPaths: []string{"."}}
	if res := describe(t, s, in); res.Errors == 0 {
		t.Fatalf("missing import not reported: %+v", res)
	}
	writeFile(t, dir, "demo/types.proto", "syntax = \"proto3\";\npackage demo.v1;\nmessage T {}\n")
	if res := describe(t, s, in); res.Errors != 0 || res.Cached || res.Services[0].Methods[0].InputType != "demo.v1.T" {
		t.Errorf("after creating the import = %+v", res)
	}

	// Editing an import invalidates too.
	if res := describe(t, s, in); !res.Cached {
		t.Fatal("not cached")
	}
	writeFile(t, dir, "demo/types.proto", "syntax = \"proto3\";\npackage demo.v1;\nmessage T { string a = 1; }\nmessage U {}\n")
	if err := os.Chtimes(filepath.Join(dir, "demo", "types.proto"), later, later); err != nil {
		t.Fatal(err)
	}
	if res := describe(t, s, in); res.Cached {
		t.Error("an edited import used the cache")
	}
}

func TestPick(t *testing.T) {
	t.Parallel()
	var dialogs []Dialog
	var answer []string
	s, dir := newProject(t, func(_ context.Context, d Dialog) ([]string, error) {
		dialogs = append(dialogs, d)
		return answer, nil
	})
	ctx := t.Context()

	outside := filepath.Join(tempDir(t), "shared", "common.proto")
	answer = []string{filepath.Join(dir, "demo", "greeter.proto"), outside}
	files, err := s.PickProtoFiles(ctx)
	if err != nil || !slices.Equal(files, []string{"demo/greeter.proto", outside}) {
		t.Errorf("PickProtoFiles = %q, %v", files, err)
	}
	if d := dialogs[0]; !d.Multiple || d.Folder || d.FilterPattern != "*.proto" || d.Directory != dir {
		t.Errorf("dialog = %+v", d)
	}

	answer = []string{filepath.Join(dir, "proto", "api")}
	root, err := s.PickImportPath(ctx)
	if err != nil || root != "proto/api" {
		t.Errorf("PickImportPath = %q, %v", root, err)
	}
	if d := dialogs[1]; !d.Folder || d.Multiple {
		t.Errorf("dialog = %+v", d)
	}

	// Cancelled.
	answer = nil
	if files, err := s.PickProtoFiles(ctx); err != nil || files == nil || len(files) != 0 {
		t.Errorf("cancelled PickProtoFiles = %q, %v", files, err)
	}
	if root, err := s.PickImportPath(ctx); err != nil || root != "" {
		t.Errorf("cancelled PickImportPath = %q, %v", root, err)
	}

	// Without dialogs, or when the dialog fails.
	if _, err := New(nil, nil, nil, nil, nil).PickProtoFiles(ctx); errs.KindOf(err) != errs.Unsupported {
		t.Errorf("no picker = %v", err)
	}
	failing := New(nil, nil, nil, func(context.Context, Dialog) ([]string, error) { return nil, errors.New("boom") }, nil)
	if _, err := failing.PickImportPath(ctx); err == nil || !strings.Contains(err.Error(), "could not be shown") {
		t.Errorf("failing picker = %v", err)
	}
}
