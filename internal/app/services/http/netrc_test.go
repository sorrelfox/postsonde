package http

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// tempNetrc is a netrc.Env whose NETRC names a file in a temporary
// directory: the process environment and the user's home are never used.
// It counts the files opened.
type tempNetrc struct {
	path  string
	mu    sync.Mutex
	reads int
}

func newTempNetrc(t *testing.T, text string, mode fs.FileMode) *tempNetrc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return &tempNetrc{path: path}
}

func (n *tempNetrc) env(t *testing.T) netrc.Env {
	return netrc.Env{
		Getenv: func(k string) string {
			if k == "NETRC" {
				return n.path
			}
			return ""
		},
		HomeDir: func() (string, error) { t.Error("the home directory was looked up"); return "", fs.ErrNotExist },
		ReadFile: func(path string) ([]byte, fs.FileMode, error) {
			n.mu.Lock()
			n.reads++
			n.mu.Unlock()
			return netrc.ReadOSFile(path)
		},
	}
}

func (n *tempNetrc) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reads
}

// TestSendNetrc (AUTH-19): a request inheriting a netrc auth from its
// folder sends the Basic credentials of its host's entry, read from the
// file at send time; the password is masked in the timeline, the history,
// the copied curl command and the HAR export; the header preview does not
// read the file; the Check call names the entry without the password.
func TestSendNetrc(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	st, opt := historyStore(t)
	opt(f.svc)
	nrc := newTempNetrc(t, "machine elsewhere.example login x password y\n"+
		"machine 127.0.0.1\n  login alice\n  password \"n3trc s3cret\"\n"+
		"macdef init\nmachine 127.0.0.1 login evil password evil\n\n", 0o600)
	WithNetrc(nrc.env(t))(f.svc)

	f.write(t, "api/_folder.json", `{"kind": "folder", "id": "fld_api", "name": "API", "auth": {"type": "netrc"}}`)
	f.write(t, "api/get.json", `{"kind": "http", "id": "req_get", "name": "Get", "method": "GET", "url": "`+f.servers.URL+`/x", "auth": {"type": "inherit"}}`)
	f.reload(t)

	if nrc.count() != 0 {
		t.Fatalf("the netrc file was read before a send: %d", nrc.count())
	}
	// The header preview is computed after every edit: it does not read.
	p, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range p.Headers {
		if h.Name == "Authorization" && h.Source == PreviewFromAuth && h.Value == "Basic "+httpclient.Mask {
			found = true
		}
	}
	if !found || nrc.count() != 0 {
		t.Errorf("preview headers = %+v, reads %d", p.Headers, nrc.count())
	}

	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, started.OpID)
	if want := "/x|" + request.AuthorizationHeader; !strings.HasPrefix(resp.Body, "/x|Basic ") || !strings.Contains(resp.Body, "|alice:n3trc s3cret|") {
		t.Errorf("server got %s (want %s…)", resp.Body, want)
	}
	if nrc.count() != 1 {
		t.Errorf("reads = %d, want 1", nrc.count())
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("warnings = %+v", resp.Warnings)
	}
	timeline, _ := json.Marshal(resp.Timeline)
	if strings.Contains(string(timeline), "n3trc") || strings.Contains(string(timeline), "YWxpY2U6bjN0cmMgczNjcmV0") || !resp.Timeline.Masked {
		t.Errorf("timeline shows the credentials: %s", timeline)
	}
	waitEntries(t, st, 1)
	if files := historyFiles(t, st); strings.Contains(files, "n3trc") || strings.Contains(files, "YWxpY2U6bjN0cmMgczNjcmV0") {
		t.Errorf("the history holds the credentials:\n%s", files)
	}

	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Masked || strings.Contains(c.Command, "n3trc") || strings.Contains(c.Command, "YWxpY2U6") {
		t.Errorf("curl = %s", c.Command)
	}
	h, _, err := f.svc.buildHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_get"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(h); strings.Contains(string(data), "n3trc") || strings.Contains(string(data), "YWxpY2U6") {
		t.Errorf("HAR holds the credentials: %s", data)
	}

	check, err := f.svc.NetrcCheck(t.Context(), NetrcCheckInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	if check.Entry != "127.0.0.1" || check.Line != 2 || check.Login != "alice" || !check.HasPassword || check.Path != nrc.path ||
		check.Source != netrc.SourceEnv || check.FolderName != "API" || check.Host != "127.0.0.1" || check.Insecure {
		t.Errorf("check = %+v", check)
	}
	if data, _ := json.Marshal(check); strings.Contains(string(data), "n3trc") {
		t.Errorf("check shows the password: %s", data)
	}
	loc, err := f.svc.NetrcFile(t.Context(), NetrcFileInputDTO{RequestID: "req_get"})
	if err != nil || loc.Path != nrc.path || loc.Source != netrc.SourceEnv {
		t.Errorf("file = %+v, %v", loc, err)
	}
	loc, err = f.svc.NetrcFile(t.Context(), NetrcFileInputDTO{RequestID: "req_get", File: "auth/netrc"})
	if err != nil || loc.Path != filepath.Join(f.dir, "auth", "netrc") || loc.Source != netrc.SourceRequest {
		t.Errorf("project file = %+v, %v", loc, err)
	}
}

// A host without an entry fails the send with the host named; a file
// others can read is used, with a warning on Unix (Windows has no such
// permission bits).
func TestSendNetrcErrorsAndWarning(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	nrc := newTempNetrc(t, "machine 127.0.0.1 login bob password b0b\n", 0o644)
	WithNetrc(nrc.env(t))(f.svc)
	f.write(t, "get.json", `{"kind": "http", "id": "req_get", "name": "Get", "method": "GET", "url": "`+f.servers.URL+`/y", "auth": {"type": "netrc"}}`)
	f.write(t, "other.json", `{"kind": "http", "id": "req_other", "name": "Other", "method": "GET", "url": "http://localhost:1/z", "auth": {"type": "netrc"}}`)
	f.write(t, "missing.json", `{"kind": "http", "id": "req_missing", "name": "Missing", "method": "GET", "url": "`+f.servers.URL+`/z", "auth": {"type": "netrc", "netrcFile": "nope/netrc"}}`)
	f.reload(t)

	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	resp := f.rec.response(t, started.OpID)
	if !strings.Contains(resp.Body, "|bob:b0b|") {
		t.Errorf("server got %s", resp.Body)
	}
	if runtime.GOOS == "windows" {
		if len(resp.Warnings) != 0 {
			t.Errorf("warnings = %+v, want none on Windows", resp.Warnings)
		}
	} else if len(resp.Warnings) != 1 || resp.Warnings[0].Code != request.WarningNetrcInsecure {
		t.Errorf("warnings = %+v", resp.Warnings)
	}

	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_other"})
	if netrcCode(err) != request.CodeAuthNetrcNoEntry || !strings.Contains(err.Error(), "localhost") {
		t.Errorf("no entry: err = %v", err)
	}
	if _, err := f.svc.NetrcCheck(t.Context(), NetrcCheckInputDTO{RequestID: "req_other"}); netrcCode(err) != request.CodeAuthNetrcNoEntry {
		t.Errorf("check, no entry: err = %v", err)
	}
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_missing"})
	if netrcCode(err) != request.CodeAuthNetrcFailed || errs.KindOf(err) != errs.NotFound || !strings.Contains(err.Error(), filepath.Join(f.dir, "nope", "netrc")) {
		t.Errorf("missing: err = %v", err)
	}
}

// The netrc file is never opened for another auth type: not on send, the
// header preview, curl, HAR, or the netrc check (which refuses).
func TestNetrcNotOpenedForOtherAuthTypes(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	WithNetrc(netrc.Env{
		Getenv:  func(k string) string { t.Errorf("netrc env %s read", k); return "" },
		HomeDir: func() (string, error) { t.Error("netrc home looked up"); return "", fs.ErrNotExist },
		ReadFile: func(p string) ([]byte, fs.FileMode, error) {
			t.Errorf("netrc file %s opened", p)
			return nil, 0, fs.ErrPermission
		},
	})(f.svc)
	auths := []string{
		`{"type": "none"}`,
		`{"type": "basic", "username": "u", "password": "{{p}}"}`,
		`{"type": "bearer", "token": "t"}`,
		`{"type": "apikey", "key": "X-Key", "value": "v"}`,
		`{"type": "digest", "username": "u", "password": "p"}`,
		`{"type": "jwt", "secret": "s"}`,
		`{"type": "oauth1", "consumerKey": "k", "consumerSecret": "s"}`,
		`{"type": "netrc", "disabled": true}`,
		`{"type": "inherit"}`,
	}
	for i, a := range auths {
		id := "req_" + string(rune('a'+i))
		f.write(t, id+".json", `{"kind": "http", "id": "`+id+`", "name": "R", "method": "GET", "url": "`+f.servers.URL+`/", "auth": `+a+`}`)
	}
	f.reload(t)
	for i := range auths {
		id := "req_" + string(rune('a'+i))
		started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: id, AllowUnresolved: true})
		if err != nil {
			t.Fatalf("%s: %v", auths[i], err)
		}
		f.rec.wait(t, started.OpID)
		if _, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: id}); err != nil {
			t.Error(err)
		}
		if _, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: id}); err != nil {
			t.Error(err)
		}
		if _, err := f.svc.NetrcCheck(t.Context(), NetrcCheckInputDTO{RequestID: id}); netrcCode(err) != CodeNoNetrc {
			t.Errorf("%s: check err = %v", auths[i], err)
		}
	}
	if _, _, err := f.svc.buildHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_a", "req_b", "req_c"}}, time.Now()); err != nil {
		t.Error(err)
	}
}

// netrcCode returns the code of an *errs.Error, "" for another error.
func netrcCode(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
