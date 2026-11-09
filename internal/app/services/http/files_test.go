package http

import (
	"encoding/base64"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func errCode(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// allowlist is a settings list the test can change between sends.
type allowlist struct {
	mu   sync.Mutex
	dirs []string
}

func (a *allowlist) get() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.dirs)
}

func (a *allowlist) set(dirs ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dirs = dirs
}

// DYN-08, PRV-10: {{$file}} inserts files of the project folder and of the
// allowed directories into what goes on the wire, read at send time, and
// refuses any other file before anything is sent.
func TestSendFileContents(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	echo := echoServer(t)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		hits.Add(1)
		echo.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	ws, _, _, _ := envWorkspace(t, srv.URL)
	allowed, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(ws.Root, "fixtures", "user.json"), `{"name": "Ada {{not a var}}"}`)
	writeFile(t, filepath.Join(allowed, "token.txt"), "t0ken-from-file")
	writeFile(t, filepath.Join(allowed, "logo.bin"), "\x89PNG\x00\xff")
	writeFile(t, filepath.Join(outside, "secret.txt"), "do-not-send")

	dirs := &allowlist{}
	src := httpSource("{{base}}/users")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: `{{$file "` + filepath.Join(allowed, "token.txt") + `"}}`}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{"user": {{$file fixtures/user.json}}, "logo": "{{$file ` + filepath.Join(allowed, "logo.bin") + ` | base64}}"}`}
	src.Workspace = ws
	f := newFixture(t, src, nil, WithAllowedDirs(dirs.get))

	// The allowed directory is not in the settings yet: refused, with the
	// path, before anything is sent. The project's own file is fine.
	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != fileaccess.CodeNotAllowed || e.Kind != errs.PermissionDenied ||
		!strings.Contains(e.Message, "token.txt") || !strings.Contains(e.Message, "outside the directories allowed") ||
		e.Details["reference"] == "" || e.Hint == "" {
		t.Fatalf("Send = %#v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the request was sent")
	}

	// Allowed now: the files are read at send time and reach the wire.
	dirs.set(allowed)
	got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"})
	wantBody := `{"user": {"name": "Ada {{not a var}}"}, "logo": "` + base64.StdEncoding.EncodeToString([]byte("\x89PNG\x00\xff")) + `"}`
	if got["env"] != "t0ken-from-file" || got["body"] != wantBody {
		t.Fatalf("server received %v, want body %s", got, wantBody)
	}
	// A change to the file is picked up by the next send.
	writeFile(t, filepath.Join(allowed, "token.txt"), "t0ken-2")
	if got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"}); got["env"] != "t0ken-2" {
		t.Fatalf("second send: %v", got)
	}

	sent := hits.Load()
	for _, tt := range []struct{ name, ref, code string }{
		{"outside", filepath.Join(outside, "secret.txt"), fileaccess.CodeNotAllowed},
		{"traversal out of the project", "../" + filepath.Base(outside) + "/secret.txt", fileaccess.CodeNotAllowed},
		{"missing", "fixtures/nope.json", fileaccess.CodeNotFound},
		{"binary without a filter", filepath.Join(allowed, "logo.bin"), template.CodeFileBinary},
	} {
		f.src.Request.Body.Text = `{{$file "` + tt.ref + `"}}`
		_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", AllowUnresolved: true})
		if errCode(err) != tt.code {
			t.Errorf("%s: Send = %v, want %s", tt.name, err, tt.code)
		}
	}
	if hits.Load() != sent {
		t.Error("a refused file was sent")
	}
}

// Copy as curl reads the file, as it is asked for; the header preview,
// computed after every edit, leaves the reference as written.
func TestFileContentsInPreviews(t *testing.T) {
	t.Parallel()
	ws, _, _, _ := envWorkspace(t, "http://example.test")
	outside := t.TempDir()
	writeFile(t, filepath.Join(ws.Root, "id.txt"), "id-from-file")
	writeFile(t, filepath.Join(outside, "secret.txt"), "do-not-read")

	src := httpSource("{{base}}/x")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Id", Value: "{{$file id.txt}}"}}
	src.Workspace = ws
	f := newFixture(t, src, nil)

	out, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil || !strings.Contains(out.Command, "X-Id: id-from-file") {
		t.Fatalf("curl = %q, %v", out.Command, err)
	}
	preview, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(preview.Headers, func(h PreviewHeaderDTO) bool { return h.Name == "X-Id" }); i < 0 ||
		preview.Headers[i].Value != "{{$file id.txt}}" || !slices.Contains(preview.Unresolved, "$file id.txt") {
		t.Errorf("preview = %+v", preview)
	}

	// A file outside the allowlist: curl refuses it, the preview does not
	// read it.
	f.src.Request.Headers[0].Value = `{{$file "` + filepath.Join(outside, "secret.txt") + `"}}`
	if _, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"}); errCode(err) != fileaccess.CodeNotAllowed {
		t.Errorf("curl outside = %v", err)
	}
	if _, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_1"}); err != nil {
		t.Errorf("preview outside = %v", err)
	}
}
