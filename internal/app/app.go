package app

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/datadir"
	"github.com/sorrelfox/postsonde/internal/core/logging"
	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

const (
	appName        = "PostSonde"
	appDescription = "Local-only desktop API testing tool"

	// appID is PostSonde's application ID: the reverse DNS of the GitHub
	// repository, as Flathub asks for GitHub-hosted apps.
	// On Linux it is the GTK application ID and, through it, the program
	// name, so both the Wayland app_id and the X11 WM_CLASS; the desktop
	// file is build/linux/<appID>.desktop and names it as StartupWMClass.
	// It is also the bundle identifier (build/config.yml, Info.plist) and
	// starts the single-instance lock ID (windows.go).
	appID = "io.github.sorrelfox.PostSonde"

	// Default and minimum size of the main window, in logical pixels. The
	// minimum leaves room for a sidebar plus a request/response split.
	defaultWidth  = 1280
	defaultHeight = 800
	minWidth      = 960
	minHeight     = 600
)

// Options is what cmd/postsonde passes to New: what the application needs
// before the app directories are set up.
type Options struct {
	// Assets is the built frontend, with index.html at its root.
	Assets fs.FS
	// ContentSecurityPolicy is sent with every asset server response
	// (frontend.ContentSecurityPolicy; docs/privacy.md). Required: New
	// refuses to start the UI without one.
	ContentSecurityPolicy string
	// WebviewDataDir is where WebView2 keeps its profile on Windows. Empty
	// is WebView2's default (postsonde.exe in the roaming app data
	// folder); portable mode sets it inside the app folder.
	WebviewDataDir string
	// InstanceID turns the single-instance lock on (APP-06, InstanceID,
	// docs/windows.md): starting PostSonde again while it runs with the
	// same ID opens a window in the running process, handing it the
	// command line, and the new process exits inside New. Empty runs
	// without the lock.
	InstanceID string
}

// Config is what cmd/postsonde passes to Run once the app directories are
// set up.
type Config struct {
	// Version is the build version (main.version), shown in the UI.
	Version string
	// Logger is handed to the services; nil means slog.Default().
	Logger *slog.Logger
	// SettingsPath is the settings file, settings.json in the config
	// directory (docs/settings.md). Empty keeps the settings in memory
	// only, which is what tests want.
	SettingsPath string
	// Policy is the admin policy that locks settings (SET-02,
	// coresettings.LoadPolicy of coresettings.SystemPolicyPath; docs/settings.md).
	// The zero value, what tests want, locks nothing.
	Policy coresettings.Policy
	// DataDir is the app data directory (appdirs.Dirs.Data). Workspaces
	// keep their local side files and the recent list under it
	// (docs/workspace.md). Empty disables opening workspaces, which is what
	// tests of other services want.
	DataDir string
	// Console is the ring of recent log records the Logger also writes to,
	// shown by the in-app console (DBG-06, ConsoleService). Nil gives the
	// console an empty ring.
	Console *logging.Ring
	// DataLocation is the data directory setting and portable mode
	// (datadir.Start, DataDirService). Nil: the settings screen shows no
	// data directory choice, which is what tests want.
	DataLocation *datadir.Store
	// Relaunch starts PostSonde again. DataDirService.Restart quits the
	// app and calls it after every service has shut down. Nil disables
	// restarting.
	Relaunch func()
	// OpenPath is a project folder to open in the first window (the
	// command line's; window.ProjectArg). Empty reopens the last project.
	OpenPath string
	// Getenv reads the process environment (os.Getenv): the proxy of
	// ALL_PROXY (SOCKS-04, docs/network.md#proxy-settings). Nil reads
	// nothing, which is what tests want.
	Getenv func(string) string
	// SystemProxy reads the operating system's proxy settings for the
	// "system" proxy mode (NET-01, sysproxy.Default(os.Getenv).Read). Nil
	// reads none, so tests never read this machine's settings.
	SystemProxy func(context.Context) (sysproxy.Settings, error)
	// Keychain is the OS keychain that keeps the key of the encrypted
	// secrets besides the master password
	// (keychain.Default(DataDir)). Nil uses none, so tests never touch
	// this machine's keychain.
	Keychain keychain.KeyStore

	// windows are the app's windows (Run sets it); nil in tests of the
	// services, which then open no window.
	windows *windowHost
}

// restartRequested is set by DataDirService.Restart before it quits the
// app; PostShutdown then calls Config.Relaunch.
var restartRequested atomic.Bool

// requestRestart quits the app so that it starts again (Config.Relaunch).
func requestRestart() {
	restartRequested.Store(true)
	if a := application.Get(); a != nil {
		a.Quit()
	}
}

// App is the application between New and Run.
type App struct {
	wails   *application.App
	windows *windowHost
	// relaunch is Config.Relaunch, set by Run.
	relaunch atomic.Pointer[func()]
}

// New creates the application. It writes nothing: cmd/postsonde calls it
// before it sets up the app directories, so that with the single-instance
// lock (Options.InstanceID) a second start of PostSonde hands its command
// line to the running one and exits in here, before it could move the
// data directory, rotate the log or read the settings the running one
// uses.
func New(opts Options) (*App, error) {
	if strings.TrimSpace(opts.ContentSecurityPolicy) == "" {
		return nil, errors.New("app: Options.ContentSecurityPolicy is empty")
	}
	a := &App{}
	// The host exists before the application so the lock's callback can
	// reach it; launches wait there until Run has the services.
	a.windows = newWindowHost(nil)
	a.wails = application.New(appOptions(opts, a))
	a.windows.app = a.wails
	// The menu bar without Wails' Help menu, which would load
	// https://wails.io into the window (privacy.go). Run installs it.
	if runtime.GOOS == "darwin" {
		a.wails.Menu.Set(applicationMenu())
	}
	return a, nil
}

// Run binds the services, opens the first window and blocks until the
// application quits.
func (a *App) Run(cfg Config) error {
	if cfg.Relaunch != nil {
		a.relaunch.Store(&cfg.Relaunch)
	}
	a.windows.settingsPath, a.windows.policy = cfg.SettingsPath, cfg.Policy
	cfg.windows = a.windows
	for _, s := range services(cfg) {
		a.wails.RegisterService(s)
	}
	// The OS theme is not known before Run, so "system" starts dark and
	// followOSTheme corrects it (theme.go).
	// The log records when its page is up (startup.go, APP-05).
	logStartup(a.windows.openMain(cfg.OpenPath), cfg.Logger)
	followOSTheme(a.wails, a.windows, cfg.SettingsPath, cfg.Policy)
	return a.wails.Run()
}

func appOptions(opts Options, a *App) application.Options {
	o := application.Options{
		Name:        appName,
		Description: appDescription,
		// The services are registered by Run (services.go), after the
		// single-instance lock. Each carries the error marshaler
		// (serviceOptions); Options.MarshalError is not used for bound
		// methods in v3.0.0.
		Assets: application.AssetOptions{
			// In `wails3 dev` this proxies to the Vite dev server instead.
			Handler: application.AssetFileServerFS(opts.Assets),
			// Strict CSP header on every response (docs/privacy.md).
			Middleware: cspMiddleware(effectivePolicy(opts.ContentSecurityPolicy)),
		},
		// Closing the last window quits, on macOS too (docs/windows.md).
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Replaces Wails' default org.wails.postsonde. With ProgramName
		// left empty, Wails also passes it to g_set_prgname, so the window
		// matches the desktop entry under Wayland and X11 alike
		// (docs/packaging.md#desktop-entry-and-window-matching).
		Linux: application.LinuxOptions{
			ApplicationID: appID,
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: opts.WebviewDataDir,
			// No background traffic of the WebView2 runtime (privacy.go).
			AdditionalBrowserArgs: webview2BrowserArgs,
		},
		// After the services saved their state: start again if the data
		// directory change asked for it (SET-03).
		PostShutdown: func() {
			if !restartRequested.Load() || a == nil {
				return
			}
			if relaunch := a.relaunch.Load(); relaunch != nil {
				(*relaunch)()
			}
		},
	}
	if opts.InstanceID != "" && singleInstanceAvailable(runtime.GOOS, os.Getenv, fileExists) {
		o.SingleInstance = &application.SingleInstanceOptions{
			UniqueID: opts.InstanceID,
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if a != nil {
					a.windows.secondInstance(d)
				}
			},
		}
	}
	return o
}

// mainWindowOptions are the options of every window (windowHost.open
// names them). background is shown until the page paints
// (windowBackground).
func mainWindowOptions(background application.RGBA) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:             "main",
		Title:            appName,
		Width:            defaultWidth,
		Height:           defaultHeight,
		MinWidth:         minWidth,
		MinHeight:        minHeight,
		URL:              "/",
		BackgroundColour: background,
		// Files dropped on the window are imported (IMP-11, filedrop.go).
		EnableFileDrop: true,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarDefault,
			// No Safe Browsing lookups of the pages WKWebView loads (the
			// default; kept explicit, docs/privacy.md).
			EnableFraudulentWebsiteWarnings: false,
		},
	}
}
