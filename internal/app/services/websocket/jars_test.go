package websocket

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	appcookies "github.com/sorrelfox/postsonde/internal/app/services/cookies"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestHandshakeJarPerWorkspaceAndEnvironment checks CK-03 for
// WebSocket: the handshake takes its cookies from the jar of the open
// project and, with separate cookies per environment, of the active
// environment, looked up on each connect. The echo server sets
// "seen=1" on every handshake.
func TestHandshakeJarPerWorkspaceAndEnvironment(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	m := workspace.New(resolvedTempDir(t))
	pathA, pathB := filepath.Join(resolvedTempDir(t), "a"), filepath.Join(resolvedTempDir(t), "b")
	if _, err := m.Create(pathB, "Project B"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(pathA, "Project A"); err != nil {
		t.Fatal(err)
	}
	jars := appcookies.NewJars(cookies.NewRegistry(cookies.Options{SaveDelay: time.Hour}), m)
	s, ev := newService(t, WorkspaceJars(jars))
	ctx := t.Context()

	envs := func() *environments.Store {
		w := m.Current()
		return environments.New(w.Root, w.LocalDir, false)
	}
	create := func(name string) string {
		t.Helper()
		env, err := envs().Create(name)
		if err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	activate := func(id string) {
		t.Helper()
		if err := envs().SetActive(id); err != nil {
			t.Fatal(err)
		}
	}
	// handshakeCookie connects, closes, and returns the Cookie header the
	// server received.
	handshakeCookie := func() string {
		t.Helper()
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: wsURL(srv, "/")})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,open" })
		if _, err := s.CloseAll(ctx); err != nil {
			t.Fatal(err)
		}
		return recvSeen(t, seenCh).header.Get("Cookie")
	}

	x, y := create("X"), create("Y")

	// Off: environments share the project's jar.
	activate(x)
	if got := handshakeCookie(); got != "" {
		t.Fatalf("first handshake sent %q", got)
	}
	activate(y)
	if got := handshakeCookie(); got != "seen=1" {
		t.Fatalf("env Y with the setting off sent %q, want the shared cookie", got)
	}

	// On: X and Y have separate jars.
	if err := jars.SetPerEnvironment(true); err != nil {
		t.Fatal(err)
	}
	activate(x)
	if got := handshakeCookie(); got != "" {
		t.Fatalf("env X's first handshake sent %q", got)
	}
	if got := handshakeCookie(); got != "seen=1" {
		t.Fatalf("env X's second handshake sent %q", got)
	}
	activate(y)
	if got := handshakeCookie(); got != "" {
		t.Fatalf("env Y sent %q, want no Cookie header", got)
	}

	// Workspace B sends none of A's cookies. The workspace change saved
	// A's jars.
	localA := m.Current().LocalDir
	if _, err := m.Open(pathB); err != nil {
		t.Fatal(err)
	}
	OnWorkspaceChanged(s)
	if got := handshakeCookie(); got != "" {
		t.Fatalf("workspace B sent %q, want no Cookie header", got)
	}
	for _, name := range []string{cookies.FileName, filepath.Join(cookies.JarsDir, environments.SecretsKey(x)+".json")} {
		if _, err := os.Stat(filepath.Join(localA, name)); err != nil {
			t.Errorf("workspace A's jar %s was not saved: %v", name, err)
		}
	}
}
