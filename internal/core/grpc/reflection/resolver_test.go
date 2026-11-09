package reflection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
)

func localDir(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

// methodDump describes a method and its message types deeply enough to
// tell whether two models agree for calling it.
func methodDump(m *desc.Method) string {
	var b strings.Builder
	b.WriteString(m.FullName)
	if m.ClientStreaming {
		b.WriteString(" client-streaming")
	}
	if m.ServerStreaming {
		b.WriteString(" server-streaming")
	}
	seen := map[*desc.Message]bool{}
	var msg func(md *desc.Message)
	msg = func(md *desc.Message) {
		if md == nil || seen[md] {
			return
		}
		seen[md] = true
		b.WriteString("\n  message " + md.FullName)
		for _, f := range md.Fields {
			oneof := ""
			if f.Oneof != nil {
				oneof = " oneof=" + f.Oneof.Name
			}
			b.WriteString("\n    " + f.Name + " " + f.Kind.String() + " " + f.Cardinality.String() + " json=" + f.JSONName +
				" type=" + f.TypeName + oneof)
			if f.HasPresence {
				b.WriteString(" presence")
			}
			if f.Packed {
				b.WriteString(" packed")
			}
			if f.Enum != nil {
				for _, v := range f.Enum.Values {
					b.WriteString(" " + v.Name)
				}
			}
			msg(f.Message)
		}
	}
	b.WriteString("\n in:")
	msg(m.Input)
	b.WriteString("\n out:")
	msg(m.Output)
	return b.String()
}

// checkSchema requires every method of the source schema, and nothing
// else, with the same descriptors.
func checkSchema(t *testing.T, sc *Schema) {
	t.Helper()
	if len(sc.Diagnostics) > 0 || len(sc.Problems) > 0 {
		t.Fatalf("diagnostics %v problems %+v", sc.Diagnostics, sc.Problems)
	}
	src := sourceSchema(t)
	var names []string
	for _, svc := range sc.Services {
		names = append(names, svc.FullName)
		want := src.Pool.FindService(svc.FullName)
		if want == nil || len(want.Methods) != len(svc.Methods) {
			t.Fatalf("service %s does not match the source", svc.FullName)
		}
		for i, m := range svc.Methods {
			if got, w := methodDump(m), methodDump(want.Methods[i]); got != w {
				t.Errorf("method differs from source:\n got %s\nwant %s", got, w)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"admin.v1.Admin", "greet.v1.Greeter"}) {
		t.Errorf("services %q", names)
	}
}

// TestReflectionMatchesSource loads the schema by reflection over v1 and
// v1alpha and compares it with the linked sources.
func TestReflectionMatchesSource(t *testing.T) {
	t.Parallel()
	for _, o := range []grpctest.ReflectionOptions{{}, {NoV1: true}, {WithDependencies: true, NoV1Alpha: true}} {
		srv := newServer(t, false, o)
		r := NewResolver(newInvoker(t, srv), localDir(t.TempDir()))
		sc, err := r.Refresh(context.Background(), fetchOptions(srv))
		if err != nil {
			t.Fatal(err)
		}
		checkSchema(t, sc)
		if sc.FromCache {
			t.Error("FromCache after Refresh")
		}
	}
}

// TestCacheOffline: load by reflection,
// stop the server, start again with a new resolver and no network, and use
// the services from the cache.
func TestCacheOffline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	for _, noV1 := range []bool{false, true} {
		srv := newServer(t, true, grpctest.ReflectionOptions{NoV1: noV1})
		addr := srv.Addr
		o := fetchOptions(srv)
		r := NewResolver(newInvoker(t, srv), localDir(dir))
		r.Client.Now = func() time.Time { return time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC) }
		sc, err := r.Refresh(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		checkSchema(t, sc)

		path, _ := r.Cache.Path(addr)
		if rel, _ := filepath.Rel(dir, path); !strings.HasPrefix(filepath.ToSlash(rel), "grpc/reflection/") || !strings.HasSuffix(rel, ".json") {
			t.Errorf("cache path %s", rel)
		}
		if fi, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("cache file: %v %v", fi, err)
		}

		srv.Close()

		// A new process: new transport client, resolver and cache.
		restarted := NewResolver(newInvoker(t, srv), localDir(dir))
		sc, err = restarted.LoadCached(ctx, strings.ToUpper(addr))
		if err != nil {
			t.Fatal(err)
		}
		checkSchema(t, sc)
		wantVersion := V1
		if noV1 {
			wantVersion = V1Alpha
		}
		if !sc.FromCache || sc.Snapshot.Version != wantVersion || !sc.Snapshot.FetchedAt.Equal(time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC)) {
			t.Errorf("cached snapshot %+v", sc.Snapshot)
		}
		if restarted.Client.RememberedVersion(addr) != wantVersion {
			t.Error("the cached version was not remembered")
		}

		// The cached descriptors work with the codec.
		m := sc.FindMethod("/greet.v1.Greeter/Hello")
		if m == nil || sc.FindMethod("greet.v1.Greeter.Chat") == nil || sc.FindMethod("greet.v1.Greeter/Nope") != nil || sc.FindMethod("x") != nil {
			t.Fatal("FindMethod")
		}
		out := dynamic.New(m.Output)
		out.Set(m.Output.FieldByName("text"), "hi")
		out.Mutable(m.Output.FieldByName("tags")).(*dynamic.Map).Set("k", "v")
		echo := out.Mutable(m.Output.FieldByName("echo")).(*dynamic.Message)
		echo.Set(m.Input.FieldByName("tone"), dynamic.EnumNumber(0))
		b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		back := dynamic.New(m.Output)
		if err := dynamic.Unmarshal(b, back); err != nil || !dynamic.Equal(out, back) {
			t.Errorf("codec round trip: %v", err)
		}

		// Resolve falls back to the cache while the server is down.
		sc, err = restarted.Resolve(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if !sc.FromCache || len(sc.Problems) != 1 || sc.Problems[0].Code != CodeOffline {
			t.Errorf("Resolve offline: %+v", sc.Problems)
		}
		sc.Problems = nil
		checkSchema(t, sc)
	}

	// Without a cache, Resolve returns the fetch error.
	r := NewResolver(newInvoker(t, grpctest.NewServer(t, false)), localDir(t.TempDir()))
	_, err := r.Resolve(ctx, FetchOptions{Call: transport.CallOptions{Address: "127.0.0.1:1"}})
	if errs.KindOf(err) != errs.Network {
		t.Errorf("Resolve without cache: %v", err)
	}
	// Invalid options are not hidden by a cache.
	_, err = r.Resolve(ctx, FetchOptions{Call: transport.CallOptions{Address: "127.0.0.1:1"}, Version: "v9"})
	wantCode(t, err, CodeInvalidVersion)
}

func TestCacheProblems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newServer(t, false, grpctest.ReflectionOptions{})
	dir := t.TempDir()
	r := NewResolver(newInvoker(t, srv), localDir(dir))
	path, err := r.Cache.Path(srv.Addr)
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.LoadCached(ctx, srv.Addr)
	if e := wantCode(t, err, CodeNotCached); e.Kind != errs.NotFound {
		t.Errorf("kind %s", e.Kind)
	}

	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt caches are reported and left alone by LoadCached.
	for _, content := range []string{
		"{not json",
		`{"kind": "something.else", "version": 1}`,
		`{"kind": "postsonde.grpc-reflection-cache", "version": 1, "address": "other:1", "apiVersion": "v1"}`,
		`{"kind": "postsonde.grpc-reflection-cache", "version": 1, "address": "` + srv.Addr + `", "apiVersion": "v3"}`,
		`{"kind": "postsonde.grpc-reflection-cache", "version": 1, "address": "` + srv.Addr + `", "apiVersion": "v1", "files": [{"name": "a.proto", "descriptor": "!!"}]}`,
		`{"kind": "postsonde.grpc-reflection-cache", "version": 1, "address": "` + srv.Addr + `", "apiVersion": "v1", "files": [{"name": "", "descriptor": "AA=="}]}`,
		`{"kind": "postsonde.grpc-reflection-cache", "version": 0}`,
	} {
		write(content)
		_, err := r.LoadCached(ctx, srv.Addr)
		e := wantCode(t, err, CodeCacheCorrupt)
		if e.Kind != errs.Parse || e.Details["path"] != path {
			t.Errorf("%s: %+v", content, e)
		}
		if b, _ := os.ReadFile(path); string(b) != content {
			t.Error("LoadCached changed the file")
		}
	}

	// Refresh moves a corrupt cache aside and says so.
	sc, err := r.Refresh(ctx, fetchOptions(srv))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Problems) != 1 || sc.Problems[0].Code != CodeCacheReplaced || sc.Problems[0].Subject != path+".corrupt" {
		t.Errorf("problems %+v", sc.Problems)
	}
	if b, _ := os.ReadFile(path + ".corrupt"); string(b) != `{"kind": "postsonde.grpc-reflection-cache", "version": 0}` {
		t.Errorf("moved aside: %q", b)
	}
	if _, err := r.LoadCached(ctx, srv.Addr); err != nil {
		t.Errorf("after refresh: %v", err)
	}

	// A cache from a newer PostSonde is neither used nor replaced.
	newer := `{"kind": "postsonde.grpc-reflection-cache", "version": 2, "address": "` + srv.Addr + `", "whatever": true}`
	write(newer)
	_, err = r.LoadCached(ctx, srv.Addr)
	if e := wantCode(t, err, CodeCacheNewer); e.Kind != errs.Unsupported {
		t.Errorf("kind %s", e.Kind)
	}
	sc, err = r.Refresh(ctx, fetchOptions(srv))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Problems) != 1 || sc.Problems[0].Code != CodeCacheNotSaved || !strings.Contains(sc.Problems[0].Message, "newer version") {
		t.Errorf("problems %+v", sc.Problems)
	}
	if b, _ := os.ReadFile(path); string(b) != newer {
		t.Error("the newer cache was overwritten")
	}
	checkSchema(t, &Schema{Services: sc.Services})

	// No project open: the local directory error comes through.
	noProject := errs.New(errs.InvalidArgument, "No project is open.").WithCode("workspace.none_open")
	r2 := NewResolver(newInvoker(t, srv), func() (string, error) { return "", noProject })
	if _, err := r2.LoadCached(ctx, srv.Addr); !errors.Is(err, noProject) {
		t.Errorf("LoadCached without project: %v", err)
	}
	sc, err = r2.Refresh(ctx, fetchOptions(srv))
	if err != nil || len(sc.Problems) != 1 || sc.Problems[0].Code != CodeCacheNotSaved {
		t.Errorf("Refresh without project: %v %+v", err, sc)
	}
}

func TestCacheFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := NewCache(localDir(dir))
	snap := &Snapshot{
		Address:   "Example.com:443",
		Version:   V1Alpha,
		FetchedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.FixedZone("x", 7200)),
		Services:  []string{"a.S"},
		Files:     []File{{Name: "a.proto", Descriptor: []byte{0x0a, 0x07, 'a', '.', 'p', 'r', 'o', 't', 'o'}}},
		Problems:  []Problem{{Code: CodeFileNotFound, Subject: "b.proto", Message: "missing"}},
	}
	if moved, err := c.Save(snap); err != nil || moved != "" {
		t.Fatal(moved, err)
	}
	path, _ := c.Path("example.com:443")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "kind": "postsonde.grpc-reflection-cache",
  "version": 1,
  "address": "example.com:443",
  "apiVersion": "v1alpha",
  "fetchedAt": "2026-09-16T08:00:00Z",
  "services": [
    "a.S"
  ],
  "files": [
    {
      "name": "a.proto",
      "descriptor": "CgdhLnByb3Rv"
    }
  ],
  "problems": [
    {
      "code": "grpcreflection.file_not_found",
      "subject": "b.proto",
      "message": "missing"
    }
  ]
}
`
	if string(data) != want {
		t.Errorf("cache file:\n%s", data)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	got, err := c.Load(" EXAMPLE.com:443 ")
	if err != nil {
		t.Fatal(err)
	}
	snap.Address = "example.com:443"
	snap.FetchedAt = snap.FetchedAt.UTC()
	if got.Address != snap.Address || got.Version != snap.Version || !got.FetchedAt.Equal(snap.FetchedAt) ||
		!slices.Equal(got.Services, snap.Services) || len(got.Files) != 1 || string(got.Files[0].Descriptor) != string(snap.Files[0].Descriptor) ||
		!slices.Equal(got.Problems, snap.Problems) {
		t.Errorf("Load = %+v", got)
	}
}
