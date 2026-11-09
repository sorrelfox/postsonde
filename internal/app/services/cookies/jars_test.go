package cookies

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// createEnv creates an environment in the open project and returns its ID.
func createEnv(t *testing.T, m *workspace.Manager, name string) string {
	t.Helper()
	w := m.Current()
	env, err := environments.New(w.Root, w.LocalDir, false).Create(name)
	if err != nil {
		t.Fatal(err)
	}
	return env.ID
}

func activate(t *testing.T, m *workspace.Manager, id string) {
	t.Helper()
	w := m.Current()
	if err := environments.New(w.Root, w.LocalDir, false).SetActive(id); err != nil {
		t.Fatal(err)
	}
}

func openProject(t *testing.T, m *workspace.Manager, path string) {
	t.Helper()
	if _, err := m.Open(path); err != nil {
		t.Fatal(err)
	}
}

// cookieServer sets "session=<project>" on /login and answers every
// request with the Cookie header it received.
func cookieServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("set"); v != "" {
			w.Header().Add("Set-Cookie", "session="+v+"; Path=/")
		}
		io.WriteString(w, r.Header.Get("Cookie"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sendWith sends GET srv+path with the jar Jars gives now, as the HTTP
// service does, and returns the Cookie header the server received.
func sendWith(t *testing.T, jars *Jars, srv *httptest.Server, path string) string {
	t.Helper()
	jar, err := jars.Jar()
	if err != nil {
		t.Fatal(err)
	}
	c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{}).WithCookieJar(jar)
	defer c.CloseIdleConnections()
	resp, err := c.Send(t.Context(), httpclient.Request{Method: http.MethodGet, URL: srv.URL + path, Settings: httpclient.DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	return string(resp.Body)
}

// TestJarsPerWorkspaceAndEnvironment checks CK-03 for HTTP:
// cookies set in workspace A / environment X are not sent from workspace
// B, nor from environment Y with separate cookies per environment on;
// with it off, environments share the project's jar.
func TestJarsPerWorkspaceAndEnvironment(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	pathA, pathB := filepath.Join(tempDir(t), "a"), filepath.Join(tempDir(t), "b")
	if _, err := m.Create(pathB, "Project B"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(pathA, "Project A"); err != nil {
		t.Fatal(err)
	}
	jars := NewJars(core.NewRegistry(core.Options{SaveDelay: time.Hour}), m)
	t.Cleanup(func() { _ = jars.CloseAll() })
	svc := New(jars, nil)
	ctx := t.Context()
	srv := cookieServer(t)
	x, y := createEnv(t, m, "X"), createEnv(t, m, "Y")

	// Off (the default): X and Y share project A's jar.
	activate(t, m, x)
	sendWith(t, jars, srv, "/login?set=shared")
	activate(t, m, y)
	if got := sendWith(t, jars, srv, "/"); got != "session=shared" {
		t.Fatalf("env Y with the setting off sent %q, want the shared cookie", got)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (JarDTO{Workspace: "Project A"}); list.Jar != want || len(list.Cookies) != 1 {
		t.Fatalf("List = %+v", list)
	}

	// On: X gets its own jar, Y another; the shared jar is kept for "no
	// environment".
	list, err = svc.SetPerEnvironment(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := (JarDTO{Workspace: "Project A", PerEnvironment: true, EnvironmentID: y, Environment: "Y"}); list.Jar != want || len(list.Cookies) != 0 {
		t.Fatalf("SetPerEnvironment = %+v", list)
	}
	activate(t, m, x)
	if got := sendWith(t, jars, srv, "/login?set=x"); got != "" {
		t.Fatalf("env X's first send carried %q", got)
	}
	if got := sendWith(t, jars, srv, "/"); got != "session=x" {
		t.Fatalf("env X sent %q", got)
	}
	activate(t, m, y)
	if got := sendWith(t, jars, srv, "/"); got != "" {
		t.Fatalf("env Y sent %q, want no Cookie header", got)
	}
	h, err := jars.Header(mustURL(t, srv.URL+"/"))
	if err != nil || h != "" {
		t.Fatalf("Header in env Y = %q, %v", h, err)
	}
	activate(t, m, "")
	if got := sendWith(t, jars, srv, "/"); got != "session=shared" {
		t.Fatalf("no environment sent %q, want the shared jar", got)
	}
	list, err = svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (JarDTO{Workspace: "Project A", PerEnvironment: true}); list.Jar != want {
		t.Fatalf("List without environment = %+v", list.Jar)
	}

	// Clearing clears only the jar shown.
	activate(t, m, x)
	if err := svc.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sendWith(t, jars, srv, "/"); got != "" {
		t.Fatalf("env X after Clear sent %q", got)
	}
	sendWith(t, jars, srv, "/login?set=x2")

	// Workspace B, with or without an environment, sends nothing of A's.
	openProject(t, m, pathB)
	if err := jars.CloseAll(); err != nil { // as on workspace:changed
		t.Fatal(err)
	}
	if got := sendWith(t, jars, srv, "/"); got != "" {
		t.Fatalf("workspace B sent %q, want no Cookie header", got)
	}
	activate(t, m, createEnv(t, m, "X"))
	if got := sendWith(t, jars, srv, "/"); got != "" {
		t.Fatalf("workspace B's env sent %q", got)
	}
	list, err = svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (JarDTO{Workspace: "Project B"}); list.Jar != want {
		t.Fatalf("workspace B's List = %+v", list.Jar)
	}

	// Back in A, the setting and every jar survived.
	openProject(t, m, pathA)
	if err := jars.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if got := sendWith(t, jars, srv, "/"); got != "session=x2" {
		t.Fatalf("env X after reopening sent %q", got)
	}
	activate(t, m, y)
	if got := sendWith(t, jars, srv, "/"); got != "" {
		t.Fatalf("env Y after reopening sent %q", got)
	}
	if _, err := os.Stat(filepath.Join(m.Current().LocalDir, core.JarsDir, x+".json")); err != nil {
		t.Errorf("env X's jar file: %v", err)
	}

	// Off again: every environment is back on the shared jar.
	if _, err := svc.SetPerEnvironment(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := sendWith(t, jars, srv, "/"); got != "session=shared" {
		t.Fatalf("env Y with the setting off again sent %q", got)
	}
}

// TestSharedJarMigration: a cookies.json from before CK-03 is the shared
// jar, used with the setting off and by "no environment" with it on.
func TestSharedJarMigration(t *testing.T) {
	t.Parallel()
	m, jars, svc := setup(t)
	local := m.Current().LocalDir
	old, err := core.Open(local, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	old.Jar().SetCookieLines(mustURL(t, "http://example.com/"), []string{"old=1"})
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if h, err := jars.Header(mustURL(t, "http://example.com/")); err != nil || h != "old=1" {
		t.Fatalf("Header = %q, %v", h, err)
	}
	if _, err := svc.SetPerEnvironment(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if h, _ := jars.Header(mustURL(t, "http://example.com/")); h != "old=1" {
		t.Fatalf("no environment with the setting on: Header = %q", h)
	}
	activate(t, m, createEnv(t, m, "Staging"))
	if h, _ := jars.Header(mustURL(t, "http://example.com/")); h != "" {
		t.Fatalf("a new environment's jar: Header = %q", h)
	}
}
