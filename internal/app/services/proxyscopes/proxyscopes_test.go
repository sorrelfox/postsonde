package proxyscopes

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/proxyscope"
	"github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

const secret = "sc0pe-s3cret"

type recorder struct {
	mu     sync.Mutex
	events []ChangedDTO
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != EventChanged {
		panic("unexpected event " + name)
	}
	r.events = append(r.events, data.(ChangedDTO))
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// world is an open project with environments Local and Staging (active)
// and the service over it; the app's proxy is manual app.lan.
type world struct {
	svc     *ProxyScopesService
	rec     *recorder
	m       *workspace.Manager
	project string
	envs    *coreenvironments.Store
	local   coreenvironments.Environment
	staging coreenvironments.Environment
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{rec: &recorder{}, project: filepath.Join(tempDir(t), "shop-api")}
	w.m = workspace.New(tempDir(t))
	ws, err := w.m.Create(w.project, "Shop API")
	if err != nil {
		t.Fatal(err)
	}
	w.envs = coreenvironments.New(ws.Root, ws.LocalDir, false)
	if w.local, err = w.envs.Create("Local"); err != nil {
		t.Fatal(err)
	}
	if w.staging, err = w.envs.Create("Staging"); err != nil {
		t.Fatal(err)
	}
	if err := w.envs.SetActive(w.staging.ID); err != nil {
		t.Fatal(err)
	}
	app := settings.Defaults().Network
	app.ProxyMode, app.ProxyURL = settings.ProxyModeManual, "socks5h://app.lan:1080"
	resolve := func(context.Context) (proxyscope.Effective, error) {
		cur := w.m.Current()
		a, err := proxyscope.New(cur.LocalDir).Active(coreenvironments.New(cur.Root, cur.LocalDir, true).ActiveID())
		if err != nil {
			return proxyscope.Effective{}, err
		}
		return proxyscope.Resolve(app, "", nil, nil, a)
	}
	w.svc = New(w.m, resolve, w.rec.emit, nil)
	return w
}

func codeOf(err error) (string, string) {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code, e.Details["field"]
	}
	return "", ""
}

// TestScopesFlow (SOCKS-08): the scopes of the project and its
// environments, the proxy in effect and the scope it comes from follow
// every change; the most specific scope wins.
func TestScopesFlow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()

	got, err := w.svc.Scopes(ctx)
	if err != nil || !got.Open || got.Project.Mode != "inherit" || len(got.Environments) != 2 || got.Error != nil {
		t.Fatalf("Scopes = %+v, %v", got, err)
	}
	if got.Environments[0].Name != "Local" || got.Environments[0].Active || !got.Environments[1].Active || got.Environments[1].Scope.Mode != "inherit" {
		t.Errorf("environments = %+v", got.Environments)
	}
	if got.Effective.Proxy != "socks5h://app.lan:1080" || got.Effective.Level != "app" || got.Effective.Source != "settings" {
		t.Errorf("effective = %+v", got.Effective)
	}

	got, err = w.svc.SetScope(ctx, ScopeInputDTO{Level: "project", Mode: "manual", URL: "http://alice:" + secret + "@project.lan:3128", Bypass: []string{"*.corp"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.URL != "http://alice@project.lan:3128" || !got.Project.HasPassword || got.Effective.Proxy != "http://alice@project.lan:3128" ||
		got.Effective.Level != "project" || strings.Join(got.Effective.Bypass, ",") != ".corp" || !got.Effective.RemoteDNS {
		t.Errorf("after the project scope: %+v", got)
	}

	// The active environment's scope wins; another environment's does not
	// apply.
	got, err = w.svc.SetScope(ctx, ScopeInputDTO{Level: "environment", EnvironmentID: w.local.ID, Mode: "manual", URL: "socks5h://local.lan:1080"})
	if err != nil || got.Effective.Level != "project" {
		t.Errorf("an inactive environment's scope: %+v, %v", got.Effective, err)
	}
	got, err = w.svc.SetScope(ctx, ScopeInputDTO{Level: "environment", EnvironmentID: w.staging.ID, Mode: "off"})
	if err != nil || got.Effective.Proxy != "" || got.Effective.Level != "environment" || len(got.Effective.Bypass) != 0 {
		t.Errorf("staging off: %+v, %v", got.Effective, err)
	}
	if err := w.envs.SetActive(w.local.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = w.svc.Scopes(ctx)
	if got.Effective.Proxy != "socks5h://local.lan:1080" || got.Effective.Level != "environment" {
		t.Errorf("local active: %+v", got.Effective)
	}
	if w.rec.count() != 3 {
		t.Errorf("%d events, want 3", w.rec.count())
	}

	// A scope that cannot be used: connections fail, and the DTO says
	// which scope.
	if err := os.WriteFile(filepath.Join(mustLocal(t, w.m), proxyscope.FileName),
		[]byte(`{"version": 1, "environments": {"`+w.local.ID+`": {"mode": "manual", "url": "nope"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = w.svc.Scopes(ctx)
	if got.Effective.Error == nil || got.Effective.Error.Code != netx.CodeProxyInvalidURL || got.Effective.Level != "environment" || got.Effective.Proxy != "" {
		t.Errorf("broken scope: %+v", got.Effective)
	}
}

func mustLocal(t *testing.T, m *workspace.Manager) string {
	t.Helper()
	dir, err := m.LocalDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestScopeCredentialsNeverInProjectFiles (SOCKS-10): passwords typed into
// a scope's URL or password field are in no project file and never come
// back in a DTO; they are in the local secret side file.
func TestScopeCredentialsNeverInProjectFiles(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	pw := secret + "-field"
	var dtos []ScopesDTO
	for _, in := range []ScopeInputDTO{
		{Level: "project", Mode: "manual", URL: "socks5h://alice:" + secret + "@project.lan"},
		{Level: "environment", EnvironmentID: w.staging.ID, Mode: "manual", URL: "http://bob@staging.lan:3128", Password: &pw},
		{Level: "environment", EnvironmentID: w.local.ID, Mode: "off", URL: "https://carol:" + secret + "-off@local.lan"},
	} {
		got, err := w.svc.SetScope(ctx, in)
		if err != nil {
			t.Fatalf("SetScope(%+v): %v", in, err)
		}
		dtos = append(dtos, got)
	}
	got, _ := w.svc.Scopes(ctx)
	dtos = append(dtos, got)
	for _, d := range dtos {
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), secret) {
			t.Errorf("a DTO holds a password: %s", b)
		}
	}
	if !got.Project.HasPassword || !got.Environments[1].Scope.HasPassword || !got.Environments[0].Scope.HasPassword {
		t.Errorf("HasPassword: %+v", got)
	}

	found := 0
	err := filepath.WalkDir(w.project, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		found++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) || strings.Contains(string(data), "project.lan") {
			t.Errorf("project file %s holds a scope: %s", path, data)
		}
		return nil
	})
	if err != nil || found == 0 {
		t.Fatalf("walk: %v, %d files", err, found)
	}
	local := mustLocal(t, w.m)
	if strings.HasPrefix(local, w.project) {
		t.Fatal("the local directory is inside the project")
	}
	scopes, _ := os.ReadFile(filepath.Join(local, proxyscope.FileName))
	if strings.Contains(string(scopes), secret) || !strings.Contains(string(scopes), "project.lan") {
		t.Errorf("scopes file: %s", scopes)
	}
	side, _ := os.ReadFile(filepath.Join(local, "secrets", "requests", proxyscope.SecretsID+".json"))
	for _, want := range []string{secret, pw, secret + "-off"} {
		if !strings.Contains(string(side), want) {
			t.Errorf("the side file lacks a password: %s", side)
		}
	}
}

func TestSetScopeErrors(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for _, tt := range []struct {
		in          ScopeInputDTO
		code, field string
	}{
		{ScopeInputDTO{Level: "environment", EnvironmentID: "env_gone", Mode: "off"}, coreenvironments.CodeNotFound, "environmentId"},
		{ScopeInputDTO{Level: "request", Mode: "off"}, proxyscope.CodeInvalid, "level"},
		{ScopeInputDTO{Level: "project", Mode: "manual"}, netx.CodeProxyInvalidURL, "url"},
		{ScopeInputDTO{Level: "project", Mode: "manual", URL: "socks5h://p.lan", Bypass: []string{"a..b"}}, netx.CodeProxyInvalidBypass, "bypass"},
	} {
		_, err := w.svc.SetScope(t.Context(), tt.in)
		if code, field := codeOf(err); code != tt.code || field != tt.field {
			t.Errorf("SetScope(%+v) = %v, want %s on %s", tt.in, err, tt.code, tt.field)
		}
	}
	if w.rec.count() != 0 {
		t.Error("a refused change sent an event")
	}
}

func TestNoProject(t *testing.T) {
	t.Parallel()
	svc := New(workspace.New(tempDir(t)), func(context.Context) (proxyscope.Effective, error) {
		return proxyscope.Effective{Level: proxyscope.LevelApp}, nil
	}, nil, nil)
	got, err := svc.Scopes(t.Context())
	if err != nil || got.Open || got.Environments == nil || got.Effective.Level != "app" || got.Effective.Bypass == nil {
		t.Errorf("Scopes = %+v, %v", got, err)
	}
	if _, err := svc.SetScope(t.Context(), ScopeInputDTO{Level: "project", Mode: "off"}); err == nil {
		t.Error("SetScope without a project succeeded")
	}
}

// TestRefreshTimer: WithRefresh runs between startup and shutdown only.
func TestRefreshTimer(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	svc := New(workspace.New(tempDir(t)), nil, nil, nil, WithRefresh(5*time.Millisecond, func(context.Context) { n.Add(1) }))
	if err := svc.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	stopped := n.Load()
	if stopped < 2 {
		t.Fatalf("%d refreshes", stopped)
	}
	time.Sleep(20 * time.Millisecond)
	if n.Load() != stopped {
		t.Error("refreshed after shutdown")
	}
}
