package app

import "github.com/wailsapp/wails/v3/pkg/application"

// What the web views and Wails do on their own, turned off (PRV-01,
// PRV-02, PRV-03; the audit is in docs/privacy.md#audit-what-could-phone-home).

// webview2BrowserArgs are Chromium switches for the WebView2 browser
// process on Windows (application.WindowsOptions.AdditionalBrowserArgs,
// shared by every window). They turn off what the embedded Edge runtime
// would fetch in the background: component updates, the network
// services Chromium runs without a page asking (variations, safe
// browsing lists, ...), Domain Reliability reports, hyperlink auditing
// pings, and the upload of metrics. Wails itself already adds
// --disable-features=msSmartScreenProtection (no SmartScreen URL
// lookups). The runtime may ignore a switch; what it really sends is
// checked by hand (docs/privacy.md#manual-checks).
var webview2BrowserArgs = []string{
	"--disable-background-networking",
	"--disable-component-update",
	"--disable-domain-reliability",
	"--no-pings",
	"--metrics-recording-only",
}

// applicationMenuRoles are the menus of the macOS menu bar: Wails'
// default menu (application.DefaultApplicationMenu) without its Help
// menu, whose only item, "Learn More", loads https://wails.io into the
// app's window. Windows and Linux windows get no menu bar unless one is
// set, so applicationMenu is used on macOS only.
var applicationMenuRoles = []application.Role{
	application.AppMenu,
	application.FileMenu,
	application.EditMenu,
	application.ViewMenu,
	application.WindowMenu,
}

// applicationMenu builds the macOS menu bar of applicationMenuRoles. It
// needs the application (the app menu is named after it): call it after
// application.New.
func applicationMenu() *application.Menu {
	m := application.NewMenu()
	for _, r := range applicationMenuRoles {
		m.AddRole(r)
	}
	return m
}
