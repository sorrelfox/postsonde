package proxyscope

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/settings"
)

func ptr[T any](v T) *T { return &v }

func mustSet(t *testing.T, s *Store, in Input) {
	t.Helper()
	if err := s.Set(in, nil); err != nil {
		t.Fatalf("Set(%+v): %v", in, err)
	}
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// TestResolvePrecedence (SOCKS-08): the most specific scope that sets a
// proxy or "no proxy" wins: the active environment, then the project, then
// the app's settings; inherit passes the choice on.
func TestResolvePrecedence(t *testing.T) {
	t.Parallel()
	app := settings.Defaults().Network
	app.ProxyMode = settings.ProxyModeManual
	app.ProxyURL = "socks5h://app.lan:1080"
	app.ProxyBypass = []string{"*.app-bypass.test"}
	appURL, _ := netx.ParseProxyURL(app.ProxyURL)
	project := Scope{Mode: ModeManual, URL: "http://bob@project.lan:3128", Bypass: []string{"10.0.0.0/8"}}
	env := Scope{Mode: ModeManual, URL: "socks5://env.lan:1080"}

	tests := []struct {
		name      string
		app       settings.Network
		active    Active
		wantProxy string
		wantLevel string
		wantPass  string
	}{
		{"nothing set: the app", app, Active{}, appURL.String(), LevelApp, ""},
		{"inherit everywhere: the app", app, Active{Project: Scope{Mode: ModeInherit}, EnvironmentID: "env_a", Environment: Scope{}}, appURL.String(), LevelApp, ""},
		{"project wins over the app", app, Active{Project: project, ProjectPassword: "pw"}, "http://bob@project.lan:3128", LevelProject, "pw"},
		{"environment wins over the project", app, Active{Project: project, EnvironmentID: "env_a", Environment: env}, "socks5://env.lan:1080", LevelEnvironment, ""},
		{"environment inherits: the project", app, Active{Project: project, EnvironmentID: "env_a"}, "http://bob@project.lan:3128", LevelProject, ""},
		{"environment off beats a project proxy", app, Active{Project: project, EnvironmentID: "env_a", Environment: Scope{Mode: ModeOff, URL: "socks5://kept.lan"}}, "", LevelEnvironment, ""},
		{"project off beats the app", app, Active{Project: Scope{Mode: ModeOff}}, "", LevelProject, ""},
		{"environment proxy beats project off", app, Active{Project: Scope{Mode: ModeOff}, EnvironmentID: "env_a", Environment: env}, "socks5://env.lan:1080", LevelEnvironment, ""},
		{"a scope with no active environment is ignored", app, Active{Environment: env}, appURL.String(), LevelApp, ""},
		{"project proxy while the app says off", settings.Defaults().Network, Active{Project: project}, "http://bob@project.lan:3128", LevelProject, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(tt.app, "", nil, nil, tt.active)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config.Proxy.String() != tt.wantProxy || got.Level != tt.wantLevel {
				t.Errorf("Resolve = %q from %s, want %q from %s", got.Config.Proxy.String(), got.Level, tt.wantProxy, tt.wantLevel)
			}
			if got.Config.Proxy.Password != tt.wantPass {
				t.Errorf("password = %q, want %q", got.Config.Proxy.Password, tt.wantPass)
			}
		})
	}

	// A scope's bypass list joins the app's; the app's proxy has only its own.
	got, _ := Resolve(app, "", nil, nil, Active{Project: project})
	if b := got.Config.Bypass.String(); b != ".app-bypass.test,10.0.0.0/8" {
		t.Errorf("project bypass = %q", b)
	}
	got, _ = Resolve(app, "", nil, nil, Active{})
	if b := got.Config.Bypass.String(); b != ".app-bypass.test" {
		t.Errorf("app bypass = %q", b)
	}
	// A SOCKS4 scope never gets a password.
	got, _ = Resolve(app, "", nil, nil, Active{Project: Scope{Mode: ModeManual, URL: "socks4a://id@p.lan"}, ProjectPassword: "pw"})
	if got.Config.Proxy.Password != "" {
		t.Error("a SOCKS4 scope got a password")
	}
}

// TestResolveFailsClosed: a scope that is set but cannot be used is an
// error naming the scope, never a direct connection.
func TestResolveFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		active Active
		source string
	}{
		{"manual project without a URL", Active{Project: Scope{Mode: ModeManual}}, LevelProject},
		{"invalid environment URL", Active{EnvironmentID: "env_a", Environment: Scope{Mode: ModeManual, URL: "socks9://x"}}, LevelEnvironment},
		{"unknown mode", Active{Project: Scope{Mode: "pac"}}, LevelProject},
	} {
		got, err := Resolve(settings.Defaults().Network, "", nil, nil, tt.active)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != netx.CodeProxyInvalidURL || e.Details["source"] != tt.source || !got.Config.Proxy.IsZero() {
			t.Errorf("%s: Resolve = %+v, %v; want %s from %s", tt.name, got, err, netx.CodeProxyInvalidURL, tt.source)
		}
	}
	bad := Scope{Mode: ModeManual, URL: "socks5h://p.lan", Bypass: []string{"bad*entry"}}
	if _, err := Resolve(settings.Defaults().Network, "", nil, nil, Active{Project: bad}); codeOf(err) != netx.CodeProxyInvalidBypass {
		t.Errorf("bad bypass: %v", err)
	}
}

// TestSetKeepsCredentialsLocal (SOCKS-10): a password typed into a scope's
// URL or password field goes to the local secret side file, never to the
// scopes file; a changed user forgets it; inherit removes the scope.
func TestSetKeepsCredentialsLocal(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(local)
	const secret = "pr0xy-s3cret"
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeManual, URL: "http://alice:" + secret + "@proxy.lan:3128", Bypass: []string{" .corp ", ""}})
	mustSet(t, s, Input{Level: LevelEnvironment, EnvironmentID: "env_a", Mode: ModeManual, URL: "socks5h://bob@env.lan", Password: ptr(secret + "2")})

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("the scopes file holds a password: %s", data)
	}
	info, _ := os.Stat(s.Path())
	if info.Mode().Perm() != 0o600 {
		t.Errorf("scopes file mode %v", info.Mode().Perm())
	}
	c, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if c.Project.URL != "http://alice@proxy.lan:3128" || strings.Join(c.Project.Bypass, ",") != ".corp" {
		t.Errorf("project scope = %+v", c.Project)
	}
	pw, err := requestsecrets.New(local).Read(SecretsID)
	if err != nil {
		t.Fatal(err)
	}
	if pw["project.password"] != secret || pw["environment.env_a.password"] != secret+"2" {
		t.Errorf("side file = %v", pw)
	}
	a, err := s.Active("env_a")
	if err != nil || a.ProjectPassword != secret || a.EnvironmentPassword != secret+"2" {
		t.Fatalf("Active = %+v, %v", a, err)
	}
	if !HasPassword(c.Project, pw, LevelProject, "") || !HasPassword(c.Environments["env_a"], pw, LevelEnvironment, "env_a") {
		t.Error("HasPassword is false")
	}

	// Switching to off keeps the URL and the password; another user
	// forgets it; a URL without a user drops it.
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeOff, URL: "http://alice@proxy.lan:3128"})
	if pw, _ := s.Passwords(); pw["project.password"] != secret {
		t.Error("off dropped the password")
	}
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeManual, URL: "http://carol@proxy.lan:3128"})
	if pw, _ := s.Passwords(); pw["project.password"] != "" {
		t.Error("another user kept the password")
	}
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeManual, URL: "http://carol@proxy.lan:3128", Password: ptr("pw")})
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeManual, URL: "http://proxy.lan:3128"})
	if pw, _ := s.Passwords(); pw["project.password"] != "" {
		t.Error("a URL without a user kept the password")
	}

	// Inherit without anything kept removes the scope; with no scope and
	// no password left, both files are gone.
	mustSet(t, s, Input{Level: LevelProject, Mode: ModeInherit})
	if err := s.Set(Input{Level: LevelEnvironment, EnvironmentID: "env_a", Mode: ModeInherit}, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path(), requestsecrets.New(local).Path(SecretsID)} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists: %v", p, err)
		}
	}
}

// TestSetDropsDeletedEnvironments: a change drops the scopes and passwords
// of environments keep says are gone.
func TestSetDropsDeletedEnvironments(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	mustSet(t, s, Input{Level: LevelEnvironment, EnvironmentID: "env_gone", Mode: ModeManual, URL: "socks5h://u:pw@a.lan"})
	mustSet(t, s, Input{Level: LevelEnvironment, EnvironmentID: "env_kept", Mode: ModeOff})
	if err := s.Set(Input{Level: LevelProject, Mode: ModeOff}, func(id string) bool { return id == "env_kept" }); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Read()
	if _, ok := c.Environments["env_gone"]; ok || c.Environments["env_kept"].Mode != ModeOff {
		t.Errorf("environments = %+v", c.Environments)
	}
	if pw, _ := s.Passwords(); len(pw) != 0 {
		t.Errorf("passwords = %v", pw)
	}
}

func TestSetErrors(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	for _, tt := range []struct {
		in    Input
		code  string
		field string
	}{
		{Input{Level: "folder", Mode: ModeOff}, CodeInvalid, "level"},
		{Input{Level: LevelEnvironment, Mode: ModeOff}, CodeInvalid, "environmentId"},
		{Input{Level: LevelProject, Mode: "system"}, CodeInvalid, "mode"},
		{Input{Level: LevelProject, Mode: ModeManual}, netx.CodeProxyInvalidURL, "url"},
		{Input{Level: LevelProject, Mode: ModeManual, URL: "ftp://x"}, netx.CodeProxyInvalidURL, "url"},
		{Input{Level: LevelProject, Mode: ModeManual, URL: "socks5h://p.lan", Password: ptr("pw")}, netx.CodeProxyInvalidURL, "password"},
		{Input{Level: LevelProject, Mode: ModeManual, URL: "socks4://id@p.lan", Password: ptr("pw")}, netx.CodeProxyInvalidURL, "password"},
		{Input{Level: LevelProject, Mode: ModeManual, URL: "socks5h://u@p.lan", Password: ptr(strings.Repeat("x", 256))}, netx.CodeProxyInvalidURL, "password"},
		{Input{Level: LevelProject, Mode: ModeManual, URL: "socks5h://p.lan", Bypass: []string{"10.0.0.0/99"}}, netx.CodeProxyInvalidBypass, "bypass"},
	} {
		err := s.Set(tt.in, nil)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != tt.code || e.Details["field"] != tt.field {
			t.Errorf("Set(%+v) = %v, want %s on %s", tt.in, err, tt.code, tt.field)
		}
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused change wrote the file")
	}
}

// TestBrokenFile: a scopes file that cannot be parsed fails Read, Active
// and Set, and is left as it is.
func TestBrokenFile(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	s := New(local)
	if err := os.WriteFile(filepath.Join(local, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Active(""); codeOf(err) != CodeLocalDataFailed {
		t.Errorf("Active: %v", err)
	}
	if err := s.Set(Input{Level: LevelProject, Mode: ModeOff}, nil); codeOf(err) != CodeLocalDataFailed {
		t.Errorf("Set: %v", err)
	}
	if data, _ := os.ReadFile(s.Path()); string(data) != "{" {
		t.Errorf("file = %q", data)
	}
	// No file: no scopes, nothing read.
	if a, err := New(t.TempDir()).Active("env_a"); err != nil || a.Decides() {
		t.Errorf("no file: %+v, %v", a, err)
	}
}
