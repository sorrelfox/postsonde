// Command postsonde is the entry point of the PostSonde desktop app.
//
// It stays thin: it resolves the app directories (POSTSONDE_HOME, portable
// mode, the chosen data directory; package datadir), takes the
// single-instance lock (a second start opens a window in the running
// PostSonde and exits; docs/windows.md), opens the log file, and hands the
// embedded frontend, the logger, the settings path, the data directory
// and the project folder of the command line to internal/app, which sets
// up the Wails application and its windows.
//
//	postsonde [project folder]
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/frontend"
	"github.com/sorrelfox/postsonde/internal/app"
	"github.com/sorrelfox/postsonde/internal/app/services/window"
	"github.com/sorrelfox/postsonde/internal/core/datadir"
	"github.com/sorrelfox/postsonde/internal/core/logging"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
	"github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// version is the build version. Release builds set it with:
//
//	go build -ldflags "-X main.version=v1.2.3" ./cmd/postsonde
//
// The Taskfile passes VERSION through the same flag.
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Fprintf(os.Stdout, "postsonde %s\n", version)
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// relaunchEnv names the PostSonde a restart (relaunch) came from, by
// process ID: the new process waits for it to exit, so that it gets the
// single-instance lock instead of handing its command line to the process
// that is quitting.
const relaunchEnv = "POSTSONDE_RELAUNCHED_FROM"

// run resolves the app directories, takes the single-instance lock,
// creates the directories, carrying out a data directory change scheduled
// by the previous run (datadir.Start), sets up logging, and runs the app
// until it quits.
func run() error {
	if pid, err := strconv.Atoi(os.Getenv(relaunchEnv)); err == nil {
		_ = os.Unsetenv(relaunchEnv)
		waitForExit(pid, 10*time.Second)
	}
	env := datadir.HostEnv()
	// Where the directories are, without creating or moving anything: a
	// second start of PostSonde must hand over to the running one before
	// it touches them (app.New exits it then).
	resolved, err := datadir.Resolve(env)
	if err != nil {
		return err
	}
	a, err := app.New(app.Options{
		Assets: frontend.Assets,
		// The UI's CSP, from frontend/csp.json (docs/privacy.md).
		ContentSecurityPolicy: frontend.ContentSecurityPolicy,
		WebviewDataDir:        webviewDataDir(resolved),
		// One running PostSonde per set of app directories.
		InstanceID: app.InstanceID(resolved.Dirs.Config),
	})
	if err != nil {
		return err
	}

	start, err := datadir.Start(env, time.Now())
	if err != nil {
		return err
	}
	dirs := start.Dirs

	// The in-app console keeps the recent records (DBG-06), redacted like
	// the file.
	redactor := logging.NewRedactor()
	ring := logging.NewRing(logging.RingOptions{Redactor: redactor, OpFromContext: userOp})
	logger, closeLog := openLogger(dirs.Logs, redactor, ring)
	defer closeLog()
	slog.SetDefault(logger)
	logger.Info("starting", "version", version, "mode", start.Mode,
		"config", dirs.Config, "data", dirs.Data, "logs", dirs.Logs)
	if start.Moved != nil {
		logger.Info("data directory moved", "from", start.Moved.DataDir, "to", dirs.Data, "entries", start.Moved.Entries)
	}
	for _, p := range start.Problems {
		logger.Warn("data directory problem", "code", p.Code, "err", p)
	}

	// Without a usable config directory the settings stay in memory.
	settingsPath := ""
	if dirs.Config != "" {
		settingsPath = filepath.Join(dirs.Config, settings.FileName)
	}
	// The admin policy (SET-02) is read from its fixed system location in
	// every mode, portable and POSTSONDE_HOME included, so neither gets
	// around it. It is read once: a change applies at the next start.
	policy := settings.LoadPolicy(settings.SystemPolicyPath())
	if policy.Exists {
		logger.Info("admin policy", "path", policy.Path, "locked", policy.LockedKeys(),
			"requireTlsVerification", policy.RequireTLSVerification)
	}
	for _, p := range policy.Problems {
		logger.Error("admin policy problem", "code", p.Code, "details", p.Details, "err", p)
	}
	// A project folder on the command line opens in the first window.
	cwd, _ := os.Getwd()
	openPath := window.ProjectArg(os.Args[1:], cwd)
	if openPath != "" {
		logger.Info("opening the project of the command line", "path", openPath)
	}

	return a.Run(app.Config{
		Version:      version,
		Logger:       logger,
		SettingsPath: settingsPath,
		Policy:       policy,
		DataDir:      dirs.Data,
		Console:      ring,
		DataLocation: datadir.NewStore(start),
		Relaunch:     relaunch,
		OpenPath:     openPath,
		Getenv:       os.Getenv,
		SystemProxy:  sysproxy.Default(os.Getenv).Read,
		Keychain:     keychain.Default(dirs.Data),
	})
}

// webviewDataDir is where WebView2 keeps its profile (Windows only; other
// web views cannot be redirected): in portable mode and under
// POSTSONDE_HOME the app folder's cache, when it can be written, else
// WebView2's default. It runs before the single-instance lock, so it only
// creates that folder.
func webviewDataDir(r datadir.Resolution) string {
	if (r.Mode != datadir.ModePortable && r.Mode != datadir.ModeEnv) || r.Dirs.Cache == "" {
		return ""
	}
	dir := filepath.Join(r.Dirs.Cache, "webview2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return ""
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return dir
}

// relaunch starts PostSonde again with the same arguments and environment
// (SET-03: a data directory change takes effect on restart). It runs after
// the app has shut down its services. An AppImage is started again through
// its image file, since its mount goes away with this process.
func relaunch() {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("restart failed: executable unknown", "err", err)
		return
	}
	if img := os.Getenv("APPIMAGE"); runtime.GOOS == "linux" && img != "" {
		exe = img
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	// The new process waits for this one to exit and release the
	// single-instance lock.
	cmd.Env = append(os.Environ(), relaunchEnv+"="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		slog.Error("restart failed", "exe", exe, "err", err)
		return
	}
	_ = cmd.Process.Release()
}

// userOp is the console ring's OpFromContext: the user operation a
// record's context belongs to (netx.WithUserOp).
func userOp(ctx context.Context) (id, kind string, ok bool) {
	op, ok := netx.UserOpFrom(ctx)
	return op.ID, op.Kind, ok
}

// openLogger returns the app logger, writing to the rotating log file in
// dir (and to stderr in dev builds) and to ring. If there is no log
// directory or the file cannot be opened the app still runs, logging to
// stderr.
func openLogger(dir string, redactor *logging.Redactor, ring *logging.Ring) (*slog.Logger, func()) {
	if dir == "" {
		return logging.New(logging.Options{Redactor: redactor, Ring: ring}), func() {}
	}
	f, err := logging.OpenFile(logging.FileOptions{Path: filepath.Join(dir, logging.FileName)})
	if err != nil {
		logger := logging.New(logging.Options{Redactor: redactor, Ring: ring})
		logger.Warn("could not open the log file; logging to stderr", "err", err)
		return logger, func() {}
	}
	var out io.Writer = f
	if version == "dev" {
		out = io.MultiWriter(f, os.Stderr)
	}
	return logging.New(logging.Options{Output: out, Redactor: redactor, Ring: ring}), func() { _ = f.Close() }
}
