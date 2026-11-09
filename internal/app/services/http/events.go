package http

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// Event names of sends, <domain>:op:<phase> (docs/events.md). They are
// constants so application.RegisterEvent below is visible to the bindings
// generator.
const (
	EventData     = Domain + ":op:" + operation.PhaseData
	EventProgress = Domain + ":op:" + operation.PhaseProgress
	EventDone     = Domain + ":op:" + operation.PhaseDone
)

// Event names of repeat runs (HTTP-27).
const (
	EventRepeatData     = RepeatDomain + ":op:" + operation.PhaseData
	EventRepeatProgress = RepeatDomain + ":op:" + operation.PhaseProgress
	EventRepeatDone     = RepeatDomain + ":op:" + operation.PhaseDone
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[operation.DataEventDTO[ResponseDTO]](EventData)
	application.RegisterEvent[operation.ProgressEventDTO](EventProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventDone)
	application.RegisterEvent[operation.DataEventDTO[RepeatResultDTO]](EventRepeatData)
	application.RegisterEvent[operation.ProgressEventDTO](EventRepeatProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventRepeatDone)
}
