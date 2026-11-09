package history

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the history service and its events.
const Domain = "history"

// EventChanged says the open project's history changed. It is a constant
// so RegisterEvent below is visible to the bindings generator.
const EventChanged = Domain + ":changed"

// Actions of ChangedEventDTO.
const (
	ActionAdded   = "added"
	ActionDeleted = "deleted"
	ActionCleared = "cleared"
)

// ChangedEventDTO is the payload of EventChanged.
type ChangedEventDTO struct {
	// Action is "added" (Entry is the new entry), "deleted" (ID is the
	// entry removed) or "cleared" (every entry, or with RequestID the
	// entries of that request). Entries dropped by the retention limit
	// are not announced: the UI drops what a refresh no longer lists.
	Action string      `json:"action"`
	ID     string      `json:"id"`
	Entry  *SummaryDTO `json:"entry"`
	// RequestID is set for "cleared" of one request's entries.
	RequestID string `json:"requestId,omitempty"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedEventDTO](EventChanged)
}
