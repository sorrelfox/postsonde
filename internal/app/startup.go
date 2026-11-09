package app

import (
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Startup time (APP-05, docs/footprint.md): the log says how long the
// first window took until its page was up, so cold start can be read off
// the log file on every OS, without tools.

// processStart is when this package was initialised: a few milliseconds
// after the process started, once the dynamic loader and the Go runtime
// have run. scripts/measure-footprint.sh measures from the exec instead.
var processStart = time.Now()

// startupMessage is the log record of logStartup; scripts/measure-footprint.sh
// waits for it.
const startupMessage = "first window ready"

// logStartup logs startupMessage once, with the milliseconds since
// processStart, when w's page has loaded the Wails runtime
// (WindowRuntimeReady): the page's script has run, so the UI renders next.
func logStartup(w *application.WebviewWindow, logger *slog.Logger) {
	if w == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	var once sync.Once
	w.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		once.Do(func() { logStartupTime(logger, time.Since(processStart)) })
	})
}

// logStartupTime writes the startup record: d in whole milliseconds.
func logStartupTime(logger *slog.Logger, d time.Duration) {
	logger.Info(startupMessage, "startupMs", d.Milliseconds())
}
