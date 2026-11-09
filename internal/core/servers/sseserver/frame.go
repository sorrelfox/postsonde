package sseserver

import (
	"strconv"
	"strings"
)

// Frame returns m as a text/event-stream event (WHATWG HTML, "Server-sent
// events"): an event line when the type is set, an id line when the ID is
// set, a retry line when RetryMs is positive, one data line per line of
// the data (CRLF, CR and LF all end a line, so a client gets the lines
// joined by LF) and a blank line that dispatches it. Empty data is one
// empty data line, so the event is still dispatched. The type and ID must
// be one line (CheckMessage).
func Frame(m Message) []byte {
	var b strings.Builder
	b.Grow(len(m.Data) + len(m.Event) + len(m.ID) + 32)
	if m.Event != "" {
		b.WriteString("event: ")
		b.WriteString(m.Event)
		b.WriteByte('\n')
	}
	if m.ID != "" {
		b.WriteString("id: ")
		b.WriteString(m.ID)
		b.WriteByte('\n')
	}
	if m.RetryMs > 0 {
		b.WriteString("retry: ")
		b.WriteString(strconv.Itoa(m.RetryMs))
		b.WriteByte('\n')
	}
	data := strings.ReplaceAll(m.Data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	for line := range strings.SplitSeq(data, "\n") {
		if line == "" {
			b.WriteString("data:\n")
			continue
		}
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return []byte(b.String())
}
