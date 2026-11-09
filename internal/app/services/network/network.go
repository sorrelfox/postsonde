// Package network is the bound service behind the network activity
// indicator and the connection log (PRV-06): it lists and clears the
// connections recorded by the app's netlog.Log and tells the UI about new
// and changed ones with throttled events, so users can check that PostSonde
// connects only where they asked (docs/privacy.md, docs/network.md).
package network

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/netlog"
)

// FlushInterval is the most often EventActivity is sent. While connections
// are open it is also how often their byte counts refresh.
const FlushInterval = 250 * time.Millisecond

// Emit sends one event to the UI.
type Emit func(name string, data any)

// Option configures New.
type Option func(*NetworkService)

// WithInterval replaces FlushInterval (tests).
func WithInterval(d time.Duration) Option {
	return func(s *NetworkService) { s.interval = d }
}

// NetworkService exposes the connection log to the UI.
type NetworkService struct {
	log      *netlog.Log
	emit     Emit
	logger   *slog.Logger
	interval time.Duration

	mu      sync.Mutex
	dirty   map[uint64]struct{}
	timer   *time.Timer
	unsub   func()
	running bool
}

// New returns a NetworkService over log. A nil log means an empty one that
// no dialer records into (tests); a nil logger means slog.Default().
func New(log *netlog.Log, emit Emit, logger *slog.Logger, opts ...Option) *NetworkService {
	if log == nil {
		log = netlog.New(netlog.Options{})
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &NetworkService{
		log:      log,
		emit:     emit,
		logger:   logger.With("service", "network"),
		interval: FlushInterval,
		dirty:    map[uint64]struct{}{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *NetworkService) ServiceName() string { return "network" }

// ServiceStartup starts sending EventActivity.
func (s *NetworkService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.start()
	return nil
}

func (s *NetworkService) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.unsub = s.log.Subscribe(s.changed)
}

// ServiceShutdown stops the events. It is safe to call more than once.
func (s *NetworkService) ServiceShutdown() error {
	s.mu.Lock()
	unsub := s.unsub
	s.running, s.unsub = false, nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	return nil
}

// changed runs on the dialing or closing goroutine: it only marks c and
// arms the flush timer.
func (s *NetworkService) changed(c netlog.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty[c.ID] = struct{}{}
	s.armLocked()
}

func (s *NetworkService) armLocked() {
	if s.running && s.timer == nil {
		s.timer = time.AfterFunc(s.interval, s.flush)
	}
}

// flush sends the changed and the active connections. While any is
// active it arms the timer again, so byte counts keep refreshing; once all
// have ended and been sent it stops, so an idle app sends nothing.
func (s *NetworkService) flush() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	ids := s.dirty
	s.dirty = map[uint64]struct{}{}
	s.timer = nil
	s.mu.Unlock()

	conns := s.log.ActiveConns()
	for id := range ids {
		if slices.ContainsFunc(conns, func(c netlog.Conn) bool { return c.ID == id }) {
			continue
		}
		// A connection dropped from the log since it changed is not sent.
		if c, ok := s.log.Get(id); ok {
			conns = append(conns, c)
		}
	}
	slices.SortFunc(conns, func(a, b netlog.Conn) int { return cmp.Compare(a.ID, b.ID) })
	active := s.log.Active()
	if s.emit != nil && (len(conns) > 0 || len(ids) > 0) {
		s.emit(EventActivity, ActivityEventDTO{Active: active, Conns: toConnDTOs(conns)})
	}
	if active > 0 {
		s.mu.Lock()
		s.armLocked()
		s.mu.Unlock()
	}
}

// List returns the kept connections, oldest first, and how many are active.
func (s *NetworkService) List(context.Context) ListDTO {
	return ListDTO{Conns: toConnDTOs(s.log.Conns()), Active: s.log.Active(), Capacity: s.log.Capacity()}
}

// Clear drops the connections that have ended; open ones stay listed.
func (s *NetworkService) Clear(context.Context) {
	s.log.Clear()
}
