package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/app/services/console"
	appcookies "github.com/sorrelfox/postsonde/internal/app/services/cookies"
	"github.com/sorrelfox/postsonde/internal/app/services/environments"
	"github.com/sorrelfox/postsonde/internal/app/services/format"
	historyservice "github.com/sorrelfox/postsonde/internal/app/services/history"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/network"
	"github.com/sorrelfox/postsonde/internal/app/services/runtimevars"
	"github.com/sorrelfox/postsonde/internal/app/services/secrets"
	"github.com/sorrelfox/postsonde/internal/app/services/servers"
	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/app/services/system"
	"github.com/sorrelfox/postsonde/internal/app/services/themes"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// The privacy promises (PRIVACY.md): no traffic the user did not
// start (PRV-01, PRV-02, PRV-05), startup and every local feature work
// with no network (APP-03), and nothing asks for an account (APP-02).

// offlineNet stands in for a machine without a network, for the whole
// process: every DNS lookup through the default resolver fails (and is
// recorded), and the standard proxy variables point at a listener that
// records whoever connects, so a stray http.DefaultTransport or
// http.ProxyFromEnvironment shows up. The app's own dialer
// (netx.NewDirect) resolves through net.DefaultResolver too, so a user
// request to a host name fails like it would offline.
//
// It changes process globals: tests using it must not be parallel (they
// then run alone, before the package's parallel tests resume).
type offlineNet struct {
	mu      sync.Mutex
	lookups []string
	proxy   *recordingListener
}

func newOfflineNet(t *testing.T) *offlineNet {
	t.Helper()
	o := &offlineNet{proxy: newRecordingListener(t)}
	saved := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(_ context.Context, network, address string) (net.Conn, error) {
			o.mu.Lock()
			o.lookups = append(o.lookups, network+" "+address)
			o.mu.Unlock()
			return nil, errors.New("offlineNet: no network")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = saved })
	proxy := "http://" + o.proxy.addr
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(k, proxy)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	return o
}

// dnsQueries returns the DNS queries attempted (network and server).
func (o *offlineNet) dnsQueries() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.lookups)
}

// recordingListener accepts and counts connections on loopback.
type recordingListener struct {
	addr     string
	mu       sync.Mutex
	accepted int
}

func newRecordingListener(t *testing.T) *recordingListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	r := &recordingListener{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.accepted++
			r.mu.Unlock()
			c.Close()
		}
	}()
	return r
}

func (r *recordingListener) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accepted
}

// headlessApp is every bound service, started as Wails starts them
// (ServiceStartup in order, ServiceShutdown in reverse at cleanup), with
// no window and no webview.
type headlessApp struct {
	svcs        []application.Service
	logs        *lockedBuffer
	system      *system.SystemService
	settings    *settings.SettingsService
	workspace   *workspace.WorkspaceService
	collection  *collection.CollectionService
	envs        *environments.EnvironmentsService
	history     *historyservice.HistoryService
	http        *httpservice.HTTPService
	network     *network.NetworkService
	console     *console.ConsoleService
	secrets     *secrets.SecretsService
	cookies     *appcookies.CookiesService
	runtimeVars *runtimevars.RuntimeVarsService
	themes      *themes.ThemesService
	servers     *servers.ServersService
	format      *format.FormatService
}

// startHeadless starts the services like a first run of the app: an
// empty data directory and settings file, no keychain, and a stub for
// the OS proxy settings of the default "system" proxy mode (reading them
// runs a local command or reads the registry, never the network; the
// test should not depend on this machine's).
func startHeadless(t *testing.T) *headlessApp {
	t.Helper()
	data := t.TempDir()
	a := &headlessApp{logs: &lockedBuffer{}}
	cfg := Config{
		Version:      "v0.0.0-test",
		Logger:       slog.New(slog.NewTextHandler(a.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		SettingsPath: filepath.Join(t.TempDir(), coresettings.FileName),
		DataDir:      data,
		Getenv:       func(string) string { return "" },
		SystemProxy: func(context.Context) (sysproxy.Settings, error) {
			return sysproxy.Settings{}, nil
		},
	}
	a.svcs = services(cfg)
	for _, s := range a.svcs {
		switch v := s.Instance().(type) {
		case *system.SystemService:
			a.system = v
		case *settings.SettingsService:
			a.settings = v
		case *workspace.WorkspaceService:
			a.workspace = v
		case *collection.CollectionService:
			a.collection = v
		case *environments.EnvironmentsService:
			a.envs = v
		case *historyservice.HistoryService:
			a.history = v
		case *httpservice.HTTPService:
			a.http = v
		case *network.NetworkService:
			a.network = v
		case *console.ConsoleService:
			a.console = v
		case *secrets.SecretsService:
			a.secrets = v
		case *appcookies.CookiesService:
			a.cookies = v
		case *runtimevars.RuntimeVarsService:
			a.runtimeVars = v
		case *themes.ThemesService:
			a.themes = v
		case *servers.ServersService:
			a.servers = v
		case *format.FormatService:
			a.format = v
		}
	}
	for name, v := range map[string]any{"system": a.system, "settings": a.settings, "workspace": a.workspace, "collection": a.collection,
		"environments": a.envs, "history": a.history, "http": a.http, "network": a.network, "console": a.console, "secrets": a.secrets,
		"cookies": a.cookies, "runtimevars": a.runtimeVars, "themes": a.themes, "servers": a.servers, "format": a.format} {
		if reflect.ValueOf(v).IsNil() {
			t.Fatalf("the %s service is not bound", name)
		}
	}

	type starter interface {
		ServiceStartup(context.Context, application.ServiceOptions) error
	}
	type stopper interface{ ServiceShutdown() error }
	var started []stopper
	t.Cleanup(func() {
		for _, s := range slices.Backward(started) {
			if err := s.ServiceShutdown(); err != nil {
				t.Errorf("%T.ServiceShutdown: %v", s, err)
			}
		}
	})
	for _, s := range a.svcs {
		if st, ok := s.Instance().(starter); ok {
			if err := st.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
				t.Fatalf("%T.ServiceStartup: %v", s.Instance(), err)
			}
		}
		if sp, ok := s.Instance().(stopper); ok {
			started = append(started, sp)
		}
	}
	return a
}

// useLocally drives what the UI does without sending anything: load the
// settings, create a project, close and open it again, fill it with
// folders, requests and an environment, and read every list the views
// show. Every call must succeed. It returns the ID of an HTTP request
// whose URL is url, for the caller to send.
func (a *headlessApp) useLocally(t *testing.T, url string) string {
	t.Helper()
	ctx := t.Context()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := a.system.Info(ctx)
	must("system info", err)
	_, err = a.settings.Get(ctx)
	must("settings", err)
	problems, err := a.settings.Problems(ctx)
	must("settings problems", err)
	if len(problems) != 0 {
		t.Errorf("first run settings problems: %+v", problems)
	}
	_, err = a.secrets.Status(ctx)
	must("secrets status", err)
	_, err = a.themes.List(ctx)
	must("themes", err)
	_, err = a.workspace.Recent(ctx)
	must("recent projects", err)

	project := filepath.Join(t.TempDir(), "api")
	_, err = a.workspace.Create(ctx, workspace.CreateInputDTO{Path: project})
	must("create project", err)
	must("close project", a.workspace.Close(ctx))
	_, err = a.workspace.Open(ctx, project)
	must("open project", err)
	_, err = a.workspace.Current(ctx)
	must("current project", err)

	folder, err := a.collection.CreateFolder(ctx, collection.CreateFolderInputDTO{Name: "Orders", Index: -1})
	must("create folder", err)
	created, err := a.collection.CreateRequest(ctx, collection.CreateRequestInputDTO{ParentID: folder.NodeID, Name: "List orders", Kind: "http", Index: -1})
	must("create request", err)
	req, err := a.collection.Request(ctx, created.NodeID)
	must("open request", err)
	req.URL = url
	_, err = a.collection.UpdateRequest(ctx, collection.UpdateRequestInputDTO{Request: req})
	must("edit request", err)
	for _, kind := range []string{"graphql", "websocket", "grpc", "tcp", "sse", "socketio"} {
		_, err := a.collection.CreateRequest(ctx, collection.CreateRequestInputDTO{Name: kind, Kind: kind, Index: -1})
		must("create "+kind+" request", err)
	}
	must("save", a.collection.FlushAll(ctx))
	_, err = a.collection.Tree(ctx)
	must("tree", err)
	_, err = a.envs.Create(ctx, "Local")
	must("create environment", err)
	_, err = a.envs.List(ctx)
	must("environments", err)
	_, err = a.envs.GlobalVariables(ctx)
	must("global variables", err)
	_, err = a.runtimeVars.List(ctx)
	must("runtime variables", err)
	_, err = a.cookies.List(ctx)
	must("cookies", err)
	_, err = a.history.List(ctx, historyservice.ListInputDTO{})
	must("history", err)
	_, err = a.servers.List(ctx)
	must("servers", err)
	_, err = a.format.BeautifyJSON(ctx, `{"a":1}`, 2)
	must("format JSON", err)
	a.console.List(ctx)
	a.network.List(ctx)
	return created.NodeID
}

// idle lets the app's timers run (the project watcher polls every
// second) with nothing to do.
func idle() { time.Sleep(2500 * time.Millisecond) }

// noTrafficSoFar fails if anything went out: a dial through the app's
// dialer (the connection log keeps refused ones too), a refusal by the
// guard, a DNS query, or a connection to the environment's proxy.
func (a *headlessApp) noTrafficSoFar(t *testing.T, o *offlineNet, when string) {
	t.Helper()
	if conns := a.network.List(t.Context()).Conns; len(conns) != 0 {
		t.Errorf("%s: the app opened connections nobody asked for: %+v", when, conns)
	}
	if strings.Contains(a.logs.String(), "dial refused") {
		t.Errorf("%s: the guard refused a dial:\n%s", when, a.logs.String())
	}
	if q := o.dnsQueries(); len(q) != 0 {
		t.Errorf("%s: DNS queries %q", when, q)
	}
	if n := o.proxy.count(); n != 0 {
		t.Errorf("%s: %d connections to the HTTP_PROXY of the environment", when, n)
	}
}

// waitForSend waits until the send of requestID is in the history and
// returns its entry.
func (a *headlessApp) waitForSend(t *testing.T, requestID string) historyservice.SummaryDTO {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page, err := a.history.List(t.Context(), historyservice.ListInputDTO{RequestID: requestID})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > 0 {
			return page.Entries[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the send did not reach the history")
	return historyservice.SummaryDTO{}
}

// TestNoTrafficWithoutUserAction (PRV-01, PRV-02, PRV-05): the app,
// started headless with networking watched, opens no connection and
// resolves no name through startup, idling, loading the settings,
// opening a project and the calls the views make; the one request the
// user sends is the only connection, marked with its operation.
func TestNoTrafficWithoutUserAction(t *testing.T) {
	o := newOfflineNet(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"orders":[]}`)
	}))
	t.Cleanup(api.Close)

	a := startHeadless(t)
	idle()
	a.noTrafficSoFar(t, o, "after startup")
	id := a.useLocally(t, api.URL+"/orders")
	idle()
	a.noTrafficSoFar(t, o, "after using the app")

	// The user sends the request: that one connection, and nothing else.
	if _, err := a.http.Send(t.Context(), httpservice.SendInputDTO{RequestID: id}); err != nil {
		t.Fatal(err)
	}
	if got := a.waitForSend(t, id); got.StatusCode != http.StatusOK || got.Error != "" {
		t.Fatalf("send: %+v, want 200", got)
	}
	conns := a.network.List(t.Context()).Conns
	if len(conns) != 1 || conns[0].OpKind != "http.send" || conns[0].OpID == "" || conns[0].Address != api.Listener.Addr().String() {
		t.Errorf("connections after one send = %+v, want the send's", conns)
	}
	if strings.Contains(a.logs.String(), "dial refused") {
		t.Errorf("the guard refused a dial:\n%s", a.logs.String())
	}
	if q := o.dnsQueries(); len(q) != 0 {
		t.Errorf("DNS queries %q for a request to an IP address", q)
	}
	if n := o.proxy.count(); n != 0 {
		t.Errorf("%d connections to HTTP_PROXY, which PostSonde does not use", n)
	}
}

// TestStartupOffline (APP-03): with no network at all (no name
// resolves, the environment's proxy goes nowhere), the app starts and
// every local feature works; a request the user sends fails with an
// error in its response, and the app goes on working.
func TestStartupOffline(t *testing.T) {
	o := newOfflineNet(t)
	a := startHeadless(t)
	id := a.useLocally(t, "https://api.offline.invalid/orders")
	a.noTrafficSoFar(t, o, "offline, before any send")

	if _, err := a.http.Send(t.Context(), httpservice.SendInputDTO{RequestID: id}); err != nil {
		t.Fatalf("starting the send: %v", err)
	}
	if got := a.waitForSend(t, id); got.StatusCode != 0 || got.Error == "" {
		t.Fatalf("an offline send ended %+v, want an error", got)
	}
	conns := a.network.List(t.Context()).Conns
	if len(conns) != 1 || conns[0].OpKind != "http.send" || conns[0].State != "failed" {
		t.Errorf("connections = %+v, want the send's failed dial", conns)
	}
	if len(o.dnsQueries()) == 0 {
		t.Error("the send resolved its host without the default resolver; offlineNet no longer simulates offline")
	}
	// Still usable.
	a.useLocally(t, "https://api.offline.invalid/other")
}

// TestNoAccount (APP-02): no bound service has a method that signs in
// to, registers with or licenses PostSonde; every feature is reachable
// without one (TestStartupOffline drives a first run with nothing set
// up). OAuth 2.0 sign-ins belong to the user's own APIs and are named
// OAuth2*.
func TestNoAccount(t *testing.T) {
	t.Parallel()
	banned := regexp.MustCompile(`(?i)^(log_?in|log_?out|sign_?(in|up|out)|register|account|licen[cs]e|activat|entitle|trial|subscription|upgrade|purchase|telemetry|analytics|crash|updatecheck|checkfor(update|version)|phonehome)`)
	seen := 0
	for _, s := range services(Config{}) {
		typ := reflect.TypeOf(s.Instance())
		for i := range typ.NumMethod() {
			name := typ.Method(i).Name
			seen++
			if banned.MatchString(name) {
				t.Errorf("%s.%s looks like an account, licence or phone-home feature (APP-02, PRV-01, PRV-02)", typ, name)
			}
		}
	}
	if seen < 100 {
		t.Fatalf("only %d bound methods seen; the check would miss them", seen)
	}
}

// TestWebviewPrivacyOptions (PRV-01, PRV-02, PRV-03): the Wails options
// that keep the web views quiet (privacy.go, the audit in
// docs/privacy.md).
func TestWebviewPrivacyOptions(t *testing.T) {
	t.Parallel()
	o := appOptions(Options{Assets: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}}, nil)
	for _, arg := range []string{"--disable-background-networking", "--disable-component-update", "--disable-domain-reliability", "--no-pings", "--metrics-recording-only"} {
		if !slices.Contains(o.Windows.AdditionalBrowserArgs, arg) {
			t.Errorf("WebView2 is started without %s", arg)
		}
	}
	if slices.ContainsFunc(o.Windows.EnabledFeatures, func(f string) bool { return strings.Contains(f, "SmartScreen") }) {
		t.Error("SmartScreen is enabled")
	}
	w := mainWindowOptions(darkBackground)
	if w.Mac.EnableFraudulentWebsiteWarnings {
		t.Error("WKWebView's fraudulent website warnings (Safe Browsing lookups) are on")
	}
	if w.DevToolsEnabled || w.OpenInspectorOnStartup {
		t.Error("the window enables the web inspector")
	}
	if w.URL != "/" || w.HTML != "" {
		t.Errorf("the window loads %q/%q, want the embedded UI", w.URL, w.HTML)
	}

	// The macOS menu bar has no Help menu: Wails' only item there loads
	// https://wails.io into the window.
	if slices.Contains(applicationMenuRoles, application.HelpMenu) {
		t.Error("the menu bar has Wails' Help menu")
	}
	// If Wails' Help menu no longer has that item, this workaround (and
	// docs/privacy.md) can be revisited.
	if application.NewHelpMenu().GetSubmenu().FindByLabel("Learn More") == nil {
		t.Log("Wails' Help menu no longer has Learn More; recheck applicationMenu")
	}
}

// wailsNetworkAPIs are the Wails APIs that reach the network or load a
// page from it by themselves, with the one file each may be used in
// ("" for none): the self-updater (app.Updater, pkg/updater), opening a
// URL in the system browser (only openURL, for the OAuth 2.0 sign-in the
// user asked for; links in notes go through the runtime's Browser.OpenURL
// after a confirmation, in the frontend), and pointing a window at a URL.
var wailsNetworkAPIs = map[string]string{
	"Updater": "",
	"OpenURL": filepath.Join("internal", "app", "dialogs.go"),
	"SetURL":  "",
}

// TestNoWailsPhoneHome (PRV-02): non-test Go code never imports the
// Wails self-updater or calls the APIs above outside their file. Wails
// links pkg/updater into every app, but it stays inert until Init is
// called (docs/privacy.md#audit-what-could-phone-home).
func TestNoWailsPhoneHome(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "wailsapp/wails/v3/pkg/updater") {
				found = append(found, fset.Position(imp.Pos()).String()+": imports the Wails updater")
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if allowed, banned := wailsNetworkAPIs[sel.Sel.Name]; banned && rel != allowed {
				found = append(found, fset.Position(sel.Pos()).String()+": ."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("Wails APIs that reach the network (docs/privacy.md#audit-what-could-phone-home):\n  %s", strings.Join(found, "\n  "))
	}
}
