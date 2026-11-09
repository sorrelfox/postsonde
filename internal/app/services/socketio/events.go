package socketio

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// Domain names the Socket.IO service and its operations.
const Domain = "sio"

// Event names of connections, <domain>:op:<phase> (docs/events.md). They
// are constants so application.RegisterEvent below is visible to the
// bindings generator.
const (
	EventData     = Domain + ":op:" + operation.PhaseData
	EventProgress = Domain + ":op:" + operation.PhaseProgress
	EventDone     = Domain + ":op:" + operation.PhaseDone
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type. Connections
// send no progress; the name is registered so the domain has all three.
func init() {
	application.RegisterEvent[operation.DataEventDTO[ItemDTO]](EventData)
	application.RegisterEvent[operation.ProgressEventDTO](EventProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventDone)
}
