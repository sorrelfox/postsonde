package environments

import "github.com/wailsapp/wails/v3/pkg/application"

// EventChanged is sent after a call changed what {{variables}} resolve to:
// an environment was created, renamed, deleted, had its variables saved,
// or another one was made active. The editors reload the variables of the
// open requests on it (ENV-11). It is a constant so RegisterEvent below is
// visible to the bindings generator.
const EventChanged = Domain + ":changed"

// ChangedDTO is the payload of EventChanged.
type ChangedDTO struct {
	// ActiveID is the active environment's ID after the change, "" for
	// none.
	ActiveID string `json:"activeId"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedDTO](EventChanged)
}
