package demo

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// Domain names the demo service and prefixes its events.
const Domain = "demo"

// Event names, <domain>:op:<phase> (docs/events.md). They are constants so
// application.RegisterEvent below is visible to the bindings generator.
const (
	EventData     = Domain + ":op:" + operation.PhaseData
	EventProgress = Domain + ":op:" + operation.PhaseProgress
	EventDone     = Domain + ":op:" + operation.PhaseDone
)

// Registering the events types their payloads in the generated bindings
// (Events.On("demo:op:data", ...) gets a typed ev.data) and makes Wails
// reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[operation.DataEventDTO[ItemDTO]](EventData)
	application.RegisterEvent[operation.ProgressEventDTO](EventProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventDone)
}
