package app

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/sorrelfox/postsonde/internal/app/services/system"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// The native side of the app theme (UI-04; docs/frontend.md, "Themes").
//
// The UI applies the theme itself, in the web view. The native window frame
// (the macOS title bar, the Windows caption, the GTK header bar) keeps
// following the OS theme: Wails v3.0.0 sets it only when a window is
// created (MacWindow.Appearance, WindowsWindow.Theme) and has no call to
// change it later, and on macOS a fixed appearance would also pin the web
// view's prefers-color-scheme, which would break "System". What Go does:
//   - it paints the window background, seen before the page's first paint,
//     in the saved theme's --bg0, so a dark app does not flash white;
//   - it forwards OS theme changes to the UI as system:themeChanged.

// The --bg0 token of each theme (frontend/src/styles/tokens.css).
var (
	darkBackground  = application.NewRGB(0x0e, 0x10, 0x13)
	lightBackground = application.NewRGB(0xee, 0xf0, 0xf3)
)

// savedTheme returns ui.theme from the settings file at path: "system",
// "dark" or "light". A missing, unreadable or invalid file gives the
// default, "system"; the settings service reports its problems. A theme
// locked by the admin policy (SET-02) wins over the file.
func savedTheme(path string, policy coresettings.Policy) string {
	s := coresettings.Defaults()
	if path != "" {
		s, _, _ = coresettings.Load(path)
	}
	return policy.Enforce(s).UI.Theme
}

// windowBackground is the window background for a theme preference. osDark
// is the OS theme, used for "system".
func windowBackground(theme string, osDark bool) application.RGBA {
	switch theme {
	case coresettings.ThemeLight:
		return lightBackground
	case coresettings.ThemeDark:
		return darkBackground
	}
	if osDark {
		return darkBackground
	}
	return lightBackground
}

// followOSTheme keeps every window's background in step with the saved
// theme and the OS, and tells the UI about OS theme changes. The OS theme
// is only known once the app runs (Env.IsDarkMode needs the platform app),
// so a "system" window opened before that starts on the dark background,
// the UI's own fallback, and is corrected when the app has started;
// windows opened later start right (windowHost.open).
func followOSTheme(a *application.App, windows *windowHost, settingsPath string, policy coresettings.Policy) {
	update := func() bool {
		dark := a.Env.IsDarkMode()
		bg := windowBackground(savedTheme(settingsPath, policy), dark)
		windows.each(func(w *application.WebviewWindow) { w.SetBackgroundColour(bg) })
		return dark
	}
	a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		windows.markStarted()
		update()
	})
	a.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) {
		emitEvent(system.EventThemeChanged, system.ThemeEventDTO{Dark: update()})
	})
}
