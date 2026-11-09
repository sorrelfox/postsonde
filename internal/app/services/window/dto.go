package window

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the window service and prefixes its events.
const Domain = "window"

// EventOpenProject asks one window to open a project: PostSonde was
// started again with a project folder while another project is open
// (Launch). Every window receives it; only the one named by Window acts.
// It is a constant so RegisterEvent below is visible to the bindings
// generator.
const EventOpenProject = Domain + ":open-project"

// WindowDTO is the result of Current.
type WindowDTO struct {
	// ID is the calling window's ID; events meant for one window name it.
	// 0 when the call came from no window.
	ID uint `json:"id"`
	// OpenPath is the project folder this window was opened for, to open
	// once its page has loaded; "" for none. Returned by the first call
	// only.
	OpenPath string `json:"openPath"`
}

// OpenProjectDTO is the payload of window:open-project.
type OpenProjectDTO struct {
	// Window is the ID of the window that should open the project.
	Window uint `json:"window"`
	// Path is the project folder, absolute.
	Path string `json:"path"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[OpenProjectDTO](EventOpenProject)
}
