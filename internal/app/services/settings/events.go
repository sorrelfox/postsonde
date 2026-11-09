package settings

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the settings service and prefixes its events.
const Domain = "settings"

// EventChanged is sent after every Update that changed something and
// whenever the file changes on disk. It is a constant so RegisterEvent
// below is visible to the bindings generator.
const EventChanged = Domain + ":changed"

// Registering the event types its payload in the generated bindings
// (Events.On("settings:changed", ...) gets a typed ev.data) and makes Wails
// reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedEventDTO](EventChanged)
}
