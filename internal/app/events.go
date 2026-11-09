package app

import "github.com/wailsapp/wails/v3/pkg/application"

// emitEvent sends an operation event (internal/app/operation) to every
// window through the Wails event manager. The services are built before the
// application exists, so the app is looked up when an event is sent; events
// sent before it exists (none are, in practice) are dropped.
func emitEvent(name string, data any) {
	if a := application.Get(); a != nil {
		a.Event.Emit(name, data)
	}
}
