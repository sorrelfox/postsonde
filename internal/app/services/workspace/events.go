package workspace

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the workspace service and prefixes its events.
const Domain = "workspace"

// EventChanged is sent whenever the open workspace changes: after Open and
// Create, after Close, and when the last workspace is reopened at startup.
// It is a constant so RegisterEvent below is visible to the bindings
// generator.
const EventChanged = Domain + ":changed"

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[CurrentDTO](EventChanged)
}
