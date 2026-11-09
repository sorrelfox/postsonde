package console

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the console's events.
const Domain = "console"

// EventEntries carries the log records taken since the last one, batched:
// at most one event per FlushInterval (or per MaxBatch records). It is a
// constant so RegisterEvent below is visible to the bindings generator.
const EventEntries = Domain + ":entries"

// EntriesEventDTO is the payload of EventEntries.
type EntriesEventDTO struct {
	// Entries are the new records, in Seq order; never empty.
	Entries []EntryDTO `json:"entries"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[EntriesEventDTO](EventEntries)
}
