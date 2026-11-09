package graphql

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/operation"
)

// Event names of schema fetches, <domain>:op:<phase> (docs/events.md).
// They are constants so application.RegisterEvent below is visible to the
// bindings generator.
const (
	EventData     = Domain + ":op:" + operation.PhaseData
	EventProgress = Domain + ":op:" + operation.PhaseProgress
	EventDone     = Domain + ":op:" + operation.PhaseDone
)

// SubscriptionDomain names the subscription operations (GQL-34): their
// items are SubscriptionEventDTO, so they have a domain segment of their
// own (docs/events.md).
const SubscriptionDomain = Domain + ".subscription"

// Event names of subscriptions.
const (
	EventSubscriptionData     = SubscriptionDomain + ":op:" + operation.PhaseData
	EventSubscriptionProgress = SubscriptionDomain + ":op:" + operation.PhaseProgress
	EventSubscriptionDone     = SubscriptionDomain + ":op:" + operation.PhaseDone
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[operation.DataEventDTO[SchemaInfoDTO]](EventData)
	application.RegisterEvent[operation.ProgressEventDTO](EventProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventDone)
	application.RegisterEvent[operation.DataEventDTO[SubscriptionEventDTO]](EventSubscriptionData)
	application.RegisterEvent[operation.ProgressEventDTO](EventSubscriptionProgress)
	application.RegisterEvent[operation.DoneEventDTO](EventSubscriptionDone)
}
