package reflection

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection/reflectwire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
)

var allFiles = []string{
	"admin/v1/admin.proto",
	"google/protobuf/empty.proto",
	"google/protobuf/timestamp.proto",
	"greet/v1/greeter.proto",
	"greet/v1/types.proto",
}

func requestLog(srv *grpctest.Server) []string {
	var out []string
	for _, r := range srv.ReflectionRequests() {
		out = append(out, r.Kind.String()+" "+r.Name)
	}
	return out
}

func TestFetch(t *testing.T) {
	for _, withTLS := range []bool{false, true} {
		for _, withDeps := range []bool{false, true} {
			name := "h2c"
			if withTLS {
				name = "tls"
			}
			if withDeps {
				name += "/deps-included"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				srv := newServer(t, withTLS, grpctest.ReflectionOptions{WithDependencies: withDeps})
				inv := newInvoker(t, srv)
				c := NewClient(inv)
				fixed := time.Date(2026, 9, 16, 12, 0, 0, 0, time.FixedZone("x", 3600))
				c.Now = func() time.Time { return fixed }
				snap, err := c.Fetch(context.Background(), fetchOptions(srv))
				if err != nil {
					t.Fatal(err)
				}
				if snap.Version != V1 || snap.Address != srv.Addr || !snap.FetchedAt.Equal(fixed) || snap.FetchedAt.Location() != time.UTC {
					t.Errorf("snapshot %s %s %v", snap.Version, snap.Address, snap.FetchedAt)
				}
				if !slices.Equal(snap.Services, []string{"admin.v1.Admin", "greet.v1.Greeter"}) &&
					!slices.Equal(snap.Services, []string{"greet.v1.Greeter", "admin.v1.Admin"}) {
					t.Errorf("services %q", snap.Services)
				}
				if got := fileNames(snap); !slices.Equal(got, allFiles) {
					t.Errorf("files %q", got)
				}
				if len(snap.Problems) > 0 {
					t.Errorf("problems %+v", snap.Problems)
				}
				log := requestLog(srv)
				if log[0] != "list_services *" {
					t.Errorf("first request %q", log[0])
				}
				// No file is asked for twice or after it arrived.
				seen := map[string]bool{}
				for _, l := range log {
					if seen[l] {
						t.Errorf("request repeated: %q in %q", l, log)
					}
					seen[l] = true
				}
				byName := 0
				for _, l := range log {
					if strings.HasPrefix(l, "file_by_filename") {
						byName++
					}
				}
				if withDeps && byName != 0 || !withDeps && byName != 3 {
					t.Errorf("file_by_filename requests: %q", log)
				}
				if got := inv.calls(); !slices.Equal(got, []string{reflectwire.MethodV1}) {
					t.Errorf("calls %q", got)
				}
				if c.RememberedVersion(strings.ToUpper(srv.Addr)) != V1 {
					t.Error("version not remembered")
				}
			})
		}
	}
}

func TestFetchV1Alpha(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, grpctest.ReflectionOptions{NoV1: true})
	inv := newInvoker(t, srv)
	c := NewClient(inv)
	ctx := context.Background()

	snap, err := c.Fetch(ctx, fetchOptions(srv))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != V1Alpha || !slices.Equal(fileNames(snap), allFiles) {
		t.Errorf("fallback snapshot %s %q", snap.Version, fileNames(snap))
	}
	if got := inv.calls(); !slices.Equal(got, []string{reflectwire.MethodV1, reflectwire.MethodV1Alpha}) {
		t.Errorf("calls %q", got)
	}
	// The next fetch starts with the version that worked.
	if _, err := c.Fetch(ctx, fetchOptions(srv)); err != nil {
		t.Fatal(err)
	}
	if got := inv.calls()[2:]; !slices.Equal(got, []string{reflectwire.MethodV1Alpha}) {
		t.Errorf("second fetch calls %q", got)
	}

	// Forcing a version.
	o := fetchOptions(srv)
	o.Version = V1Alpha
	if snap, err := c.Fetch(ctx, o); err != nil || snap.Version != V1Alpha {
		t.Errorf("forced v1alpha: %v", err)
	}
	o.Version = V1
	_, err = c.Fetch(ctx, o)
	wantCode(t, err, CodeUnsupported)
	if !strings.Contains(err.Error(), "does not support server reflection v1") {
		t.Errorf("message %q", err)
	}
	o.Version = "v2"
	wantCode(t, fetchErr(c, o), CodeInvalidVersion)

	// A server with a v1 service only works too, and is remembered.
	srv1 := newServer(t, false, grpctest.ReflectionOptions{NoV1Alpha: true})
	c1 := NewClient(newInvoker(t, srv1))
	c1.RememberVersion(srv1.Addr, V1Alpha)
	if snap, err := c1.Fetch(ctx, fetchOptions(srv1)); err != nil || snap.Version != V1 || c1.RememberedVersion(srv1.Addr) != V1 {
		t.Errorf("v1 only: %v", err)
	}
}

func fetchErr(c *Client, o FetchOptions) error {
	_, err := c.Fetch(context.Background(), o)
	return err
}

func wantCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != code {
		t.Fatalf("error %v, want code %s", err, code)
	}
	return e
}

func TestFetchNoReflection(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServer(t, false)
	err := fetchErr(NewClient(newInvoker(t, srv)), fetchOptions(srv))
	e := wantCode(t, err, CodeUnsupported)
	if e.Kind != errs.Unsupported || transport.StatusFromError(err).Code != transport.Unimplemented {
		t.Errorf("kind %s status %v", e.Kind, transport.StatusFromError(err))
	}

	// A server that cannot be reached keeps the transport error.
	addr := srv.Addr
	srv.Close()
	err = fetchErr(NewClient(newInvoker(t, srv)), FetchOptions{Call: transport.CallOptions{Address: addr}})
	if errs.KindOf(err) != errs.Network {
		t.Errorf("closed server: %v (%s)", err, errs.KindOf(err))
	}
}

func TestFetchErrorResponses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("list_services refused", func(t *testing.T) {
		srv := newServer(t, false, grpctest.ReflectionOptions{Errors: map[string]transport.Code{"list_services": transport.PermissionDenied}})
		err := fetchErr(NewClient(newInvoker(t, srv)), fetchOptions(srv))
		e := wantCode(t, err, CodeServerError)
		if e.Details["grpc_status"] != "PERMISSION_DENIED" || !strings.Contains(e.Message, "injected error") {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("service and dependency not found", func(t *testing.T) {
		srv := newServer(t, false, grpctest.ReflectionOptions{Errors: map[string]transport.Code{
			"file_containing_symbol:admin.v1.Admin":        transport.NotFound,
			"file_by_filename:google/protobuf/empty.proto": transport.NotFound,
			"file_by_filename:greet/v1/types.proto":        transport.Internal,
		}})
		snap, err := NewClient(newInvoker(t, srv)).Fetch(ctx, fetchOptions(srv))
		if err != nil {
			t.Fatal(err)
		}
		if got := fileNames(snap); !slices.Equal(got, []string{"greet/v1/greeter.proto"}) {
			t.Errorf("files %q", got)
		}
		// The well-known file has a built-in copy, so it is not a problem.
		want := []Problem{
			{CodeSymbolNotFound, "admin.v1.Admin", "The server did not send the definition of service admin.v1.Admin: NOT_FOUND (injected error for file_containing_symbol:admin.v1.Admin)"},
			{CodeFileNotFound, "greet/v1/types.proto", "The server did not send greet/v1/types.proto: INTERNAL (injected error for file_by_filename:greet/v1/types.proto)"},
		}
		if !slices.Equal(sortProblems(snap.Problems), sortProblems(want)) {
			t.Errorf("problems %+v", snap.Problems)
		}
		sc, err := Link(ctx, snap)
		if err != nil {
			t.Fatal(err)
		}
		if len(sc.Services) != 1 || sc.Services[0].FullName != "greet.v1.Greeter" {
			t.Errorf("services %v", sc.Services)
		}
		if len(sc.Problems) != 2 {
			t.Errorf("schema problems %+v", sc.Problems)
		}
		if len(sc.Diagnostics) == 0 || !strings.Contains(sc.Diagnostics[0].Message, `"greet/v1/types.proto" not found`) {
			t.Errorf("diagnostics %v", sc.Diagnostics)
		}
		// The partially linked method still exists, with unresolved types.
		if m := sc.FindMethod("/greet.v1.Greeter/Hello"); m == nil || m.Input != nil {
			t.Errorf("method %+v", m)
		}
	})

	t.Run("reflection services included", func(t *testing.T) {
		srv := newServer(t, false, grpctest.ReflectionOptions{})
		o := fetchOptions(srv)
		o.IncludeReflectionServices = true
		snap, err := NewClient(newInvoker(t, srv)).Fetch(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(snap.Services, reflectwire.ServiceV1) || len(snap.Problems) != 2 {
			t.Errorf("services %q problems %+v", snap.Services, snap.Problems)
		}
	})
}

func sortProblems(ps []Problem) []Problem {
	out := slices.Clone(ps)
	slices.SortFunc(out, func(a, b Problem) int { return strings.Compare(a.Subject, b.Subject) })
	return out
}

// fdp builds a minimal FileDescriptorProto with a name, dependencies and
// one empty service.
func fdp(name, pkg, service string, deps ...string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, name)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, pkg)
	for _, d := range deps {
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendString(b, d)
	}
	if service != "" {
		var s []byte
		s = protowire.AppendTag(s, 1, protowire.BytesType)
		s = protowire.AppendString(s, service)
		b = protowire.AppendTag(b, 6, protowire.BytesType)
		b = protowire.AppendBytes(b, s)
	}
	return b
}

func TestFetchCycle(t *testing.T) {
	t.Parallel()
	for _, withDeps := range []bool{false, true} {
		srv := newServer(t, false, grpctest.ReflectionOptions{
			WithDependencies: withDeps,
			Files: [][]byte{
				fdp("a.proto", "p", "A", "b.proto"),
				fdp("b.proto", "p", "B", "c.proto", "a.proto"),
				fdp("c.proto", "p", "", "a.proto", "b.proto"),
			},
		})
		snap, err := NewClient(newInvoker(t, srv)).Fetch(context.Background(), fetchOptions(srv))
		if err != nil {
			t.Fatal(err)
		}
		if got := fileNames(snap); !slices.Equal(got, []string{"a.proto", "b.proto", "c.proto"}) {
			t.Errorf("files %q", got)
		}
		n := 0
		for _, r := range srv.ReflectionRequests() {
			if r.Kind == reflectwire.RequestFileByFilename {
				n++
			}
		}
		if withDeps && n != 0 || !withDeps && n != 2 {
			t.Errorf("deps=%v: file_by_filename requests %q", withDeps, requestLog(srv))
		}
		// Linking reports the cycle instead of looping.
		sc, err := Link(context.Background(), snap)
		if err != nil {
			t.Fatal(err)
		}
		if len(sc.Diagnostics) == 0 || !strings.Contains(sc.Diagnostics[0].Message, "import cycle") {
			t.Errorf("diagnostics %v", sc.Diagnostics)
		}
	}
}

func TestFetchProtocolErrors(t *testing.T) {
	t.Parallel()
	cases := map[string]grpctest.Handler{
		"wrong response kind": func(ss *grpctest.ServerStream) transport.Status {
			ss.Recv()
			ss.Send(reflectwire.MarshalResponse(reflectwire.Response{Kind: reflectwire.ResponseFileDescriptor}))
			return transport.Status{}
		},
		"stream ends": func(ss *grpctest.ServerStream) transport.Status {
			ss.Recv()
			return transport.Status{}
		},
		"malformed": func(ss *grpctest.ServerStream) transport.Status {
			ss.Recv()
			ss.Send([]byte{0x0a, 0x09})
			return transport.Status{}
		},
		"services then status": func(ss *grpctest.ServerStream) transport.Status {
			ss.Recv()
			ss.Send(reflectwire.MarshalResponse(reflectwire.Response{Kind: reflectwire.ResponseListServices, Services: []string{"x.S"}}))
			return transport.Status{Code: transport.Unavailable, Message: "going away"}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := grpctest.NewServer(t, false)
			srv.Handle(reflectwire.MethodV1, h)
			err := fetchErr(NewClient(newInvoker(t, srv)), fetchOptions(srv))
			if name == "services then status" {
				if transport.StatusFromError(err).Code != transport.Unavailable {
					t.Errorf("error %v", err)
				}
				return
			}
			e := wantCode(t, err, CodeProtocol)
			if e.Kind != errs.Protocol {
				t.Errorf("kind %s", e.Kind)
			}
		})
	}

	// Too many files.
	srv := grpctest.NewServer(t, false)
	srv.Handle(reflectwire.MethodV1, func(ss *grpctest.ServerStream) transport.Status {
		ss.Recv()
		ss.Send(reflectwire.MarshalResponse(reflectwire.Response{Kind: reflectwire.ResponseListServices, Services: []string{"x.S"}}))
		ss.Recv()
		files := make([][]byte, MaxFiles+1)
		for i := range files {
			files[i] = fdp("f"+strconv.Itoa(i)+".proto", "p", "")
		}
		ss.Send(reflectwire.MarshalResponse(reflectwire.Response{Kind: reflectwire.ResponseFileDescriptor, Files: files}))
		return transport.Status{}
	})
	wantCode(t, fetchErr(NewClient(newInvoker(t, srv)), fetchOptions(srv)), CodeTooManyFiles)
}

func TestFetchCancelled(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, grpctest.ReflectionOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(newInvoker(t, srv)).Fetch(ctx, fetchOptions(srv))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v", err)
	}
}
