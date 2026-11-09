package system

import "github.com/wailsapp/wails/v3/pkg/application"

// EventThemeChanged is sent when the OS switches between light and dark
// while the app runs (UI-04). The app layer emits it from the Wails
// common:ThemeChanged application event, which does not reach the web view
// by itself. The UI uses it next to prefers-color-scheme, which some web
// views (WebKitGTK) do not update. It is a constant so RegisterEvent below
// is visible to the bindings generator.
const EventThemeChanged = "system:themeChanged"

// ThemeEventDTO is the payload of EventThemeChanged.
type ThemeEventDTO struct {
	// Dark is true when the OS now uses a dark theme.
	Dark bool `json:"dark"`
}

func init() {
	application.RegisterEvent[ThemeEventDTO](EventThemeChanged)
}
