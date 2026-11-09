// Package console is the bound service behind the in-app console (DBG-06):
// it lists and clears the log records kept by the app's logging.Ring and
// streams new ones to the UI as batched events. Records are redacted before
// they reach the ring (docs/privacy.md).
package console

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/logging"
	"github.com/sorrelfox/postsonde/internal/core/stream"
)

// Batching of EventEntries. A log line only needs to look live, and a burst
// of debug records (a busy server) must not flood the web view.
const (
	FlushInterval = 100 * time.Millisecond
	MaxBatch      = 250
)

// Emit sends one event to the UI.
type Emit func(name string, data any)

// ConsoleService exposes the app log to the UI.
type ConsoleService struct {
	ring   *logging.Ring
	emit   Emit
	logger *slog.Logger

	mu      sync.Mutex
	batcher *stream.Batcher[logging.Entry]
	unsub   func()
}

// New returns a ConsoleService over ring. A nil ring means an empty one
// that nothing logs into (tests); a nil logger means slog.Default().
func New(ring *logging.Ring, emit Emit, logger *slog.Logger) *ConsoleService {
	if ring == nil {
		ring = logging.NewRing(logging.RingOptions{})
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ConsoleService{ring: ring, emit: emit, logger: logger.With("service", "console")}
}

// ServiceName names the service in Wails logs.
func (s *ConsoleService) ServiceName() string { return "console" }

// ServiceStartup starts streaming new records as EventEntries.
func (s *ConsoleService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.start()
	return nil
}

func (s *ConsoleService) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batcher != nil {
		return
	}
	b := stream.New(context.Background(), stream.Options[logging.Entry]{
		Interval: FlushInterval,
		MaxItems: MaxBatch,
	}, s.flush)
	s.batcher = b
	// Add fails only once the batcher is closed, which happens after
	// unsubscribing.
	s.unsub = s.ring.Subscribe(func(e logging.Entry) { _ = b.Add(e) })
}

// ServiceShutdown stops the stream, flushing what is pending.
func (s *ConsoleService) ServiceShutdown() error {
	s.mu.Lock()
	b, unsub := s.batcher, s.unsub
	s.batcher, s.unsub = nil, nil
	s.mu.Unlock()
	if b != nil {
		unsub()
		b.Close()
	}
	return nil
}

// flush sends one batch. It runs on the batcher's goroutine, never under
// the ring's lock, so the emit may log.
func (s *ConsoleService) flush(batch []logging.Entry) {
	if s.emit == nil || len(batch) == 0 {
		return
	}
	out := make([]EntryDTO, len(batch))
	for i, e := range batch {
		// Secrets registered since the record was logged are masked too.
		out[i] = toEntryDTO(s.ring.Redact(e))
	}
	s.emit(EventEntries, EntriesEventDTO{Entries: out})
}

// List returns the kept records, oldest first.
func (s *ConsoleService) List(context.Context) ListDTO {
	entries := s.ring.Entries()
	out := make([]EntryDTO, len(entries))
	for i, e := range entries {
		out[i] = toEntryDTO(e)
	}
	return ListDTO{Entries: out, Capacity: s.ring.Capacity()}
}

// Clear drops the kept records. The log file is not touched.
func (s *ConsoleService) Clear(context.Context) {
	s.ring.Clear()
}
