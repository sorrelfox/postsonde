package http

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// accessLayout is a project folder, an allowed directory and a folder
// outside both, each holding name, plus the links project/escape (to the
// outside file) and project/outdir (to the outside folder).
type accessLayout struct {
	project, allowed, outside string
}

func newAccessLayout(t *testing.T, name, content string) accessLayout {
	t.Helper()
	l := accessLayout{t.TempDir(), t.TempDir(), t.TempDir()}
	for _, dir := range []string{l.project, l.allowed, l.outside} {
		writeFile(t, filepath.Join(dir, name), content)
	}
	for target, link := range map[string]string{filepath.Join(l.outside, name): "escape", l.outside: "outdir"} {
		if err := os.Symlink(target, filepath.Join(l.project, link)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symbolic links need privileges on Windows:", err)
			}
			t.Fatal(err)
		}
	}
	return l
}

// paths are what each limited feature is tried with, and whether the
// allowlist lets it through.
func (l accessLayout) paths(name string) []struct {
	name, path string
	allowed    bool
} {
	return []struct {
		name, path string
		allowed    bool
	}{
		{"project-relative", name, true},
		{"allowed directory", filepath.Join(l.allowed, name), true},
		{"outside", filepath.Join(l.outside, name), false},
		{"link escaping", "escape", false},
		{"folder link escaping", "outdir/" + name, false},
	}
}

func (l accessLayout) workspace() *coreworkspace.Workspace {
	return &coreworkspace.Workspace{Root: l.project, File: &projectfile.Workspace{}}
}

func wantRefused(t *testing.T, err error, subject string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != fileaccess.CodeNotAllowed || e.Kind != errs.PermissionDenied {
		t.Fatalf("err = %v, want %s", err, fileaccess.CodeNotAllowed)
	}
	if !strings.HasPrefix(e.Message, subject+" ") || !strings.Contains(e.Message, "outside the directories allowed for file access") || e.Hint != fileaccess.HintAllow {
		t.Errorf("message %q, hint %q", e.Message, e.Hint)
	}
}

// PRV-10: a JWT key file is read from the project folder or an allowed
// directory only; a refused one stops the send before anything goes out.
// The preview of the Auth tab refuses it the same way.
func TestSendJWTKeyFileAllowlist(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	l := newAccessLayout(t, "ed.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	dirs := &allowlist{}
	dirs.set(l.allowed)
	src := httpSource(srv.URL)
	src.Workspace = l.workspace()
	f := newFixture(t, src, nil, WithAllowedDirs(dirs.get))
	for _, tc := range l.paths("ed.pem") {
		t.Run(tc.name, func(t *testing.T) {
			f.src.Auth = projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: tc.path}
			before := hits.Load()
			started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
			_, perr := f.svc.JWTPreview(t.Context(), JWTPreviewInputDTO{RequestID: "req_1"})
			if tc.allowed {
				if err != nil || perr != nil {
					t.Fatalf("Send = %v, JWTPreview = %v", err, perr)
				}
				if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
					t.Fatalf("done = %+v", done)
				}
				return
			}
			wantRefused(t, err, "The JWT key file")
			wantRefused(t, perr, "The JWT key file")
			if hits.Load() != before {
				t.Error("the request was sent")
			}
		})
	}
}

// PRV-10: the schema file of a matches_schema assertion outside the
// allowed directories makes the assertion invalid with
// fileaccess.not_allowed; the response itself is not affected.
func TestSendSchemaFileAllowlist(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"u7"}`)
	}))
	t.Cleanup(srv.Close)
	l := newAccessLayout(t, "user.json", `{"required":["id"]}`)
	dirs := &allowlist{}
	dirs.set(l.allowed)
	src := httpSource(srv.URL)
	src.Workspace = l.workspace()
	f := newFixture(t, src, nil, WithAllowedDirs(dirs.get))
	for _, tc := range l.paths("user.json") {
		t.Run(tc.name, func(t *testing.T) {
			f.src.Request.Assertions = []projectfile.Assertion{{Target: "body", Op: "matches_schema", Expected: tc.path}}
			id := f.send(t)
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			tests := f.rec.response(t, id).Tests
			if len(tests) != 1 {
				t.Fatalf("tests = %+v", tests)
			}
			got := tests[0]
			if tc.allowed {
				if got.Outcome != "passed" {
					t.Errorf("result = %+v", got)
				}
				return
			}
			if got.Outcome != "invalid" || got.Code != fileaccess.CodeNotAllowed ||
				!strings.HasPrefix(got.Message, "The schema file ") || !strings.Contains(got.Message, "outside the directories allowed") {
				t.Errorf("result = %+v", got)
			}
		})
	}
}

// PRV-10: a netrc file named by the auth is limited; NetrcCheck and a send
// refuse one outside. The default file in the home directory is not.
func TestNetrcFileAllowlist(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	t.Cleanup(srv.Close)
	l := newAccessLayout(t, "netrc", "default login anna password pw\n")
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".netrc"), "default login home password h\n")
	env := netrc.Env{Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return home, nil }, GOOS: "linux"}
	dirs := &allowlist{}
	dirs.set(l.allowed)
	src := httpSource(srv.URL)
	src.Workspace = l.workspace()
	f := newFixture(t, src, nil, WithAllowedDirs(dirs.get), WithNetrc(env))
	for _, tc := range append(l.paths("netrc"), struct {
		name, path string
		allowed    bool
	}{"home directory", "", true}) {
		t.Run(tc.name, func(t *testing.T) {
			f.src.Auth = projectfile.Auth{Type: projectfile.AuthNetrc, NetrcFile: tc.path}
			got, err := f.svc.NetrcCheck(t.Context(), NetrcCheckInputDTO{RequestID: "req_1"})
			_, serr := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
			if tc.allowed {
				want := "anna"
				if tc.path == "" {
					want = "home"
				}
				if err != nil || serr != nil || got.Login != want {
					t.Fatalf("NetrcCheck = %+v, %v; Send = %v", got, err, serr)
				}
				return
			}
			wantRefused(t, err, "The .netrc file")
			wantRefused(t, serr, "The .netrc file")
			var e *errs.Error
			if errors.As(err, &e) && e.Details[request.DetailAuthField] != "netrcFile" {
				t.Errorf("details = %v", e.Details)
			}
		})
	}
}

// An existing project whose files are all inside its folder works as
// before with an empty allowlist: {{$file}}, a JWT key file, a netrc file
// and a schema file, all relative to the project.
func TestProjectFilesNeedNoAllowlist(t *testing.T) {
	t.Parallel()
	echo := echoServer(t)
	ws, _, _, _ := envWorkspace(t, echo.URL)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws.Root, "keys", "ed.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	writeFile(t, filepath.Join(ws.Root, "fixtures", "env.txt"), "from-file")
	writeFile(t, filepath.Join(ws.Root, "schemas", "any.json"), `{}`)
	writeFile(t, filepath.Join(ws.Root, "auth", "netrc"), "default login anna password pw\n")
	src := httpSource("{{base}}/x")
	src.Workspace = ws
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{$file fixtures/env.txt}}"}}
	src.Request.Assertions = []projectfile.Assertion{{Target: "body", Op: "matches_schema", Expected: "schemas/any.json"}}
	src.Auth = projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: "keys/ed.pem"}
	f := newFixture(t, src, nil, WithAllowedDirs(func() []string { return nil }),
		WithNetrc(netrc.Env{Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return "", fs.ErrNotExist }, GOOS: "linux"}))
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if tests := f.rec.response(t, id).Tests; len(tests) != 1 || tests[0].Outcome != "passed" {
		t.Errorf("tests = %+v", tests)
	}
	if got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"}); got["env"] != "from-file" {
		t.Errorf("server received %v", got)
	}
	f.src.Auth = projectfile.Auth{Type: projectfile.AuthNetrc, NetrcFile: "auth/netrc"}
	if got, err := f.svc.NetrcCheck(t.Context(), NetrcCheckInputDTO{RequestID: "req_1"}); err != nil || got.Login != "anna" {
		t.Errorf("NetrcCheck = %+v, %v", got, err)
	}
}
