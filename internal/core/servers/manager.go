package servers

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// Defaults for Options.
const (
	// DefaultContentLimit caps the content of one event: enough for the
	// payloads people read in a log, small enough that a batch of events
	// sent to the UI stays a few megabytes.
	DefaultContentLimit = 64 << 10
	// DefaultRecentEvents and DefaultRecentBytes bound the traffic kept in
	// Go per server, for a UI that reloads and asks for it again.
	DefaultRecentEvents = 1000
	DefaultRecentBytes  = 8 << 20
	// DefaultCaptures and DefaultCaptureBytes bound the requests kept whole
	// per server for replay (Event.Capture): the newest ones win.
	DefaultCaptures     = 200
	DefaultCaptureBytes = 64 << 20
	// DefaultStopTimeout is how long Stop lets work in progress finish
	// before closing connections.
	DefaultStopTimeout = 3 * time.Second
)

// Options configure a Manager.
type Options struct {
	// Types are the kinds of server the Manager can run.
	Types []Type
	// OnChange is called after every change of a server (added, state,
	// removed), in the order the changes happened. It must not call the
	// Manager's Add, Start, Stop, Remove or StopAll.
	OnChange func(s Server, removed bool)
	// OnTraffic is called with every event, in Seq order per server. It
	// must return quickly (hand the event to a stream.Batcher) and must not
	// call the Manager.
	OnTraffic func(e Event)
	// ContentLimit caps Event.Content; 0 means DefaultContentLimit.
	ContentLimit int
	// RecentEvents and RecentBytes bound the events kept per server
	// (Traffic); 0 means the defaults.
	RecentEvents int
	RecentBytes  int
	// Captures and CaptureBytes bound the requests kept whole per server
	// (Capture); 0 means the defaults.
	Captures     int
	CaptureBytes int
	// StopTimeout bounds the graceful part of Stop; 0 means
	// DefaultStopTimeout.
	StopTimeout time.Duration
	// Certificates gives a TLS server its certificate at every start
	// (SRV-05); nil makes TLS servers fail to start
	// (servers.tls_unavailable).
	Certificates CertificateSource
	// Now is the clock; nil means time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Manager holds the servers of this app run, keyed by ID. Servers are kept
// in memory only. It is safe for concurrent use.
type Manager struct {
	opts   Options
	types  map[string]Type
	logger *slog.Logger

	// notifyMu keeps OnChange calls in the order of the changes.
	notifyMu sync.Mutex

	mu      sync.Mutex
	servers map[string]*entry
	order   []string // IDs in the order added
	closed  bool
}

type entry struct {
	id string // never changes

	// opMu serializes Start, Stop and Remove of this server.
	opMu sync.Mutex

	// Guarded by Manager.mu.
	snap    Server
	typ     Type
	inst    Instance
	run     *runSink
	removed bool

	// evMu guards the traffic: sequence numbers, the recent buffer and
	// the order of OnTraffic calls.
	evMu     sync.Mutex
	seq      uint64
	recent   recentBuffer
	captures captureBuffer
}

// NewManager returns a Manager for opts.
func NewManager(opts Options) *Manager {
	if opts.ContentLimit <= 0 {
		opts.ContentLimit = DefaultContentLimit
	}
	if opts.RecentEvents <= 0 {
		opts.RecentEvents = DefaultRecentEvents
	}
	if opts.RecentBytes <= 0 {
		opts.RecentBytes = DefaultRecentBytes
	}
	if opts.Captures <= 0 {
		opts.Captures = DefaultCaptures
	}
	if opts.CaptureBytes <= 0 {
		opts.CaptureBytes = DefaultCaptureBytes
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = DefaultStopTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.OnChange == nil {
		opts.OnChange = func(Server, bool) {}
	}
	if opts.OnTraffic == nil {
		opts.OnTraffic = func(Event) {}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		opts:    opts,
		types:   map[string]Type{},
		logger:  logger,
		servers: map[string]*entry{},
	}
	for _, t := range opts.Types {
		m.types[t.Name()] = t
	}
	return m
}

// Types returns the registered types in the order given.
func (m *Manager) Types() []Type { return slices.Clone(m.opts.Types) }

// Add defines a server, stopped. It checks the type, host and port; LAN
// access is checked when the server starts.
func (m *Manager) Add(spec Spec) (Server, error) {
	spec, err := m.Check(spec)
	if err != nil {
		return Server{}, err
	}
	typ := m.types[spec.Type]

	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Server{}, shuttingDown()
	}
	id := newID()
	e := &entry{id: id, typ: typ, snap: Server{ID: id, Spec: spec, TypeLabel: typ.Label(), State: StateStopped}}
	e.recent = recentBuffer{maxEvents: m.opts.RecentEvents, maxBytes: m.opts.RecentBytes}
	e.captures = captureBuffer{maxCaptures: m.opts.Captures, maxBytes: m.opts.CaptureBytes}
	m.servers[e.snap.ID] = e
	m.order = append(m.order, e.snap.ID)
	snap := m.snapshotLocked(e)
	m.mu.Unlock()
	m.opts.OnChange(snap, false)
	return snap, nil
}

// Check returns spec as Add would keep it, without defining a server: the
// type, port and host checked, the name trimmed (empty gets the type's
// label), the configuration normalized and the TLS settings checked
// (their files are read at start). It is how a definition is
// checked before it is saved (SRV-04).
func (m *Manager) Check(spec Spec) (Spec, error) {
	typ, ok := m.types[spec.Type]
	if !ok {
		return Spec{}, unknownType(spec.Type)
	}
	if spec.Port < 0 || spec.Port > 65535 {
		return Spec{}, errs.New(errs.InvalidArgument, fmt.Sprintf("The port must be between 0 and 65535, not %d.", spec.Port)).
			WithCode(listen.CodeInvalidPort).WithHint("Enter a port from 1 to 65535, or 0 for any free one.")
	}
	if _, err := listen.ParseHost("tcp", spec.Host); err != nil {
		return Spec{}, err
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		spec.Name = typ.Label()
	}
	config, err := normalizeConfig(typ, spec.Config)
	if err != nil {
		return Spec{}, err
	}
	spec.Config = config
	if spec.TLS, err = checkTLS(spec.TLS); err != nil {
		return Spec{}, err
	}
	if _, packet := typ.(PacketType); packet && spec.TLS != nil {
		return Spec{}, tlsNotSupported(typ.Label())
	}
	return spec, nil
}

// Redefine replaces the definition of the server id, keeping its ID,
// state and traffic, and returns the server. The type never changes
// (servers.type_changed: remove the server and add another). The host,
// port, LAN access and TLS can change only while the server does not hold a
// socket (servers.active otherwise); the name, owner and configuration can
// change at any time, and a running server applies a new configuration at
// once, as UpdateConfig does. It checks spec as Add does and calls
// OnChange.
func (m *Manager) Redefine(id string, spec Spec) (Server, error) {
	e, err := m.entry(id)
	if err != nil {
		return Server{}, err
	}
	spec, err = m.Check(spec)
	if err != nil {
		return Server{}, err
	}
	if spec.Type != e.typ.Name() {
		return Server{}, typeChanged(id)
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	if e.removed {
		m.mu.Unlock()
		return Server{}, notFound(id)
	}
	cur := e.snap.Spec
	if e.snap.State.Active() && (cur.Host != spec.Host || cur.Port != spec.Port || cur.AllowLAN != spec.AllowLAN || !sameTLS(cur.TLS, spec.TLS)) {
		m.mu.Unlock()
		return Server{}, active(id)
	}
	e.snap.Spec = spec
	inst := e.inst
	snap := m.snapshotLocked(e)
	m.mu.Unlock()
	if r, ok := inst.(Reconfigurable); ok {
		r.Reconfigure(spec.Config)
	}
	m.opts.OnChange(snap, false)
	return snap, nil
}

// List returns every server in the order added.
func (m *Manager) List() []Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Server, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.snapshotLocked(m.servers[id]))
	}
	return out
}

// Get returns the server id.
func (m *Manager) Get(id string) (Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.servers[id]
	if !ok {
		return Server{}, notFound(id)
	}
	return m.snapshotLocked(e), nil
}

// Start starts the server id and returns its state: running, or failed
// with the error also returned. Starting a server that is already starting,
// running or stopping does nothing. ctx bounds opening the socket only.
func (m *Manager) Start(ctx context.Context, id string) (Server, error) {
	e, err := m.entry(id)
	if err != nil {
		return Server{}, err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()

	var spec Spec
	var begin error
	snap := m.update(e, func(s *Server) {
		switch {
		case e.removed:
			begin = notFound(id)
		case m.closed:
			begin = shuttingDown()
		case s.State.Active():
			begin = errAlreadyActive
		default:
			spec = s.Spec
			s.State = StateStarting
			s.Addrs, s.Scope, s.Warning, s.Err, s.Cert = nil, "", nil, nil, nil
		}
	})
	if begin == errAlreadyActive {
		return snap, nil
	}
	if begin != nil {
		return snap, begin
	}

	// The certificate is loaded first, so a missing file never opens a
	// socket.
	var cert *ServerCertificate
	if spec.TLS != nil {
		if m.opts.Certificates == nil {
			return m.startFailed(e, tlsUnavailable())
		}
		if cert, err = m.opts.Certificates(ctx, spec); err != nil {
			return m.startFailed(e, err)
		}
	}
	lspec := listen.Spec{Host: spec.Host, Port: spec.Port, AllowLAN: spec.AllowLAN, DualStack: true}
	var ln net.Listener
	var conns []net.PacketConn
	var info listen.Info
	pt, packet := e.typ.(PacketType)
	if packet {
		conns, info, err = listen.ListenPackets(ctx, lspec)
	} else {
		ln, info, err = listen.Listen(ctx, lspec)
	}
	if err != nil {
		return m.startFailed(e, err)
	}
	// Logged before the type starts, so it comes before any connection.
	text := "Listening on " + addrList(info.Addrs)
	if packet {
		text = "Listening for UDP datagrams on " + addrList(info.Addrs)
	}
	if cert != nil {
		ln = tls.NewListener(ln, tlsConfig(e.typ, cert.Certificate))
		text += " " + describeCert(cert)
	}
	if info.Warning != nil {
		text += ". " + info.Warning.Message
	}
	m.record(e, Event{Direction: System, Kind: KindStarted, Summary: text})
	run := &runSink{m: m, e: e}
	var inst Instance
	if packet {
		inst, err = pt.StartPacket(spec, conns, run)
	} else {
		inst, err = e.typ.Start(spec, ln, run)
	}
	if err != nil {
		run.close()
		if _, ok := errors.AsType[*errs.Error](err); !ok {
			err = startFailed(err)
		}
		return m.startFailed(e, err)
	}

	snap = m.update(e, func(s *Server) {
		s.State = StateRunning
		s.Addrs, s.Scope, s.Warning = info.Addrs, info.Scope, info.Warning
		s.Cert = leafOf(cert)
		s.StartedAt = m.opts.Now()
		e.inst, e.run = inst, run
	})
	m.logger.Info("server started", "id", id, "type", spec.Type, "addresses", addrList(info.Addrs), "scope", string(info.Scope))
	go m.watch(e, inst)
	return snap, nil
}

var errAlreadyActive = errors.New("already active")

func (m *Manager) startFailed(e *entry, err error) (Server, error) {
	snap := m.update(e, func(s *Server) {
		s.State = StateFailed
		s.Err = err
		s.StoppedAt = m.opts.Now()
		s.Addrs, s.Scope, s.Warning, s.Cert = nil, "", nil, nil
	})
	m.logger.Warn("server failed to start", "id", snap.ID, "type", snap.Spec.Type, "err", err)
	m.record(e, Event{Direction: System, Kind: KindError, Summary: errorSummary(err)})
	return snap, err
}

// watch marks the server failed when inst ends without Stop.
func (m *Manager) watch(e *entry, inst Instance) {
	<-inst.Done()
	e.opMu.Lock()
	defer e.opMu.Unlock()
	m.mu.Lock()
	current := e.inst == inst
	m.mu.Unlock()
	if !current {
		return // stopped through Stop
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.StopTimeout)
	_ = inst.Stop(ctx)
	cancel()
	m.mu.Lock()
	run := e.run
	m.mu.Unlock()
	run.close()

	err := serveFailed(inst.Err())
	snap := m.update(e, func(s *Server) {
		s.State = StateFailed
		s.Err = err
		s.StoppedAt = m.opts.Now()
		s.Addrs, s.Scope, s.Warning, s.Cert = nil, "", nil, nil
		e.inst, e.run = nil, nil
	})
	m.logger.Warn("server stopped unexpectedly", "id", snap.ID, "type", snap.Spec.Type, "err", err)
	m.record(e, Event{Direction: System, Kind: KindError, Summary: errorSummary(err)})
}

// Stop stops the server id and returns its state. Work in progress gets
// Options.StopTimeout to finish, then connections are closed. Stopping a
// server that is not running does nothing. Stop finishes even when ctx is
// cancelled, so a socket is never left half-closed.
func (m *Manager) Stop(ctx context.Context, id string) (Server, error) {
	e, err := m.entry(id)
	if err != nil {
		return Server{}, err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	return m.stopLocked(ctx, e), nil
}

// stopLocked stops e; the caller holds e.opMu.
func (m *Manager) stopLocked(ctx context.Context, e *entry) Server {
	var inst Instance
	var run *runSink
	snap := m.update(e, func(s *Server) {
		if s.State != StateRunning || e.inst == nil {
			return
		}
		s.State = StateStopping
		inst, run = e.inst, e.run
	})
	if inst == nil {
		return snap
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.opts.StopTimeout)
	if err := inst.Stop(sctx); err != nil {
		m.logger.Warn("server did not stop cleanly", "id", snap.ID, "err", err)
	}
	cancel()
	run.close()
	snap = m.update(e, func(s *Server) {
		s.State = StateStopped
		s.StoppedAt = m.opts.Now()
		s.Addrs, s.Scope, s.Warning, s.Cert = nil, "", nil, nil
		e.inst, e.run = nil, nil
	})
	m.logger.Info("server stopped", "id", snap.ID, "type", snap.Spec.Type)
	m.record(e, Event{Direction: System, Kind: KindStopped, Summary: "Stopped"})
	return snap
}

// Remove stops the server id if it runs and forgets it with its traffic.
func (m *Manager) Remove(ctx context.Context, id string) error {
	e, err := m.entry(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	m.stopLocked(ctx, e)

	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	if e.removed {
		m.mu.Unlock()
		return notFound(id)
	}
	e.removed = true
	delete(m.servers, id)
	m.order = slices.DeleteFunc(m.order, func(v string) bool { return v == id })
	snap := m.snapshotLocked(e)
	m.mu.Unlock()
	m.opts.OnChange(snap, true)
	return nil
}

// StopAll stops every server, in parallel, and refuses to start any more.
// The app calls it when it quits.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	entries := make([]*entry, 0, len(m.order))
	for _, id := range m.order {
		entries = append(entries, m.servers[id])
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Go(func() {
			e.opMu.Lock()
			defer e.opMu.Unlock()
			m.stopLocked(ctx, e)
		})
	}
	wg.Wait()
}

// UpdateConfig replaces the configuration of the server id and returns
// the server. A running server applies it at once, without restarting:
// requests in progress finish with the old configuration. It waits for a
// start or stop of the server in progress. Changing the configuration is
// not a state change, so OnChange is not called.
func (m *Manager) UpdateConfig(id string, config any) (Server, error) {
	e, err := m.entry(id)
	if err != nil {
		return Server{}, err
	}
	config, err = normalizeConfig(e.typ, config)
	if err != nil {
		return Server{}, err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	m.mu.Lock()
	if e.removed {
		m.mu.Unlock()
		return Server{}, notFound(id)
	}
	e.snap.Spec.Config = config
	inst := e.inst
	snap := m.snapshotLocked(e)
	m.mu.Unlock()
	if r, ok := inst.(Reconfigurable); ok {
		r.Reconfigure(config)
	}
	return snap, nil
}

// Running returns the instance of the server id while it runs, for a type
// with operations of its own (the WebSocket server's clients). A server
// that is not running is servers.not_running.
func (m *Manager) Running(id string) (Instance, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.removed {
		return nil, notFound(id)
	}
	if e.inst == nil || e.snap.State != StateRunning {
		return nil, notRunning(id)
	}
	return e.inst, nil
}

// normalizeConfig checks config with typ, or drops it when typ has no
// configuration.
func normalizeConfig(typ Type, config any) (any, error) {
	ct, ok := typ.(ConfigurableType)
	if !ok {
		return nil, nil
	}
	return ct.NormalizeConfig(config)
}

// Traffic returns the most recent events of the server id, oldest first:
// at most Options.RecentEvents, and Options.RecentBytes of content.
func (m *Manager) Traffic(id string) ([]Event, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	e.evMu.Lock()
	defer e.evMu.Unlock()
	return e.recent.list(), nil
}

// ClearTraffic forgets the recent events and the captures of the server
// id. Sequence numbers go on.
func (m *Manager) ClearTraffic(id string) error {
	e, err := m.entry(id)
	if err != nil {
		return err
	}
	e.evMu.Lock()
	defer e.evMu.Unlock()
	e.recent.clear()
	e.captures.clear()
	return nil
}

// Captures returns the requests the server id keeps whole (Event.Capture),
// oldest first: at most Options.Captures, and Options.CaptureBytes. They
// share their bytes with the Manager; treat them as read-only.
func (m *Manager) Captures(id string) ([]Capture, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	e.evMu.Lock()
	defer e.evMu.Unlock()
	return e.captures.list(), nil
}

// Capture returns the request of the server id's request event seq,
// servers.capture_not_found when it is not kept (never captured, dropped
// for newer ones, or cleared). Treat it as read-only.
func (m *Manager) Capture(id string, seq uint64) (Capture, error) {
	e, err := m.entry(id)
	if err != nil {
		return Capture{}, err
	}
	e.evMu.Lock()
	defer e.evMu.Unlock()
	c, ok := e.captures.get(seq)
	if !ok {
		return Capture{}, captureNotFound(id, seq)
	}
	return c, nil
}

func (m *Manager) entry(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.servers[id]
	if !ok {
		return nil, notFound(id)
	}
	return e, nil
}

// update changes e's snapshot and reports the change. fn may leave the
// snapshot as is; then nothing is reported.
func (m *Manager) update(e *entry, fn func(s *Server)) Server {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	before := e.snap
	fn(&e.snap)
	changed := before.State != e.snap.State || !errors.Is(before.Err, e.snap.Err) || !before.StartedAt.Equal(e.snap.StartedAt)
	snap := m.snapshotLocked(e)
	removed := e.removed
	m.mu.Unlock()
	if changed && !removed {
		m.opts.OnChange(snap, false)
	}
	return snap
}

func (m *Manager) snapshotLocked(e *entry) Server {
	s := e.snap
	s.Addrs = slices.Clone(s.Addrs)
	if s.Warning != nil {
		w := *s.Warning
		s.Warning = &w
	}
	e.evMu.Lock()
	s.Events = e.seq
	e.evMu.Unlock()
	return s
}

// record numbers ev, keeps it and passes it on.
func (m *Manager) record(e *entry, ev Event) {
	ev.normalize(m.opts.ContentLimit, m.opts.Now())
	e.evMu.Lock()
	defer e.evMu.Unlock()
	e.seq++
	ev.ServerID = e.id
	ev.Seq = e.seq
	if c := ev.Capture; c != nil {
		ev.Capture = nil
		kept := *c
		kept.Seq, kept.Time, kept.Peer = ev.Seq, ev.Time, ev.Peer
		ev.Captured = e.captures.add(kept)
	}
	e.recent.add(ev)
	m.opts.OnTraffic(ev)
}

// runSink is the Sink of one run of a server. It drops events once the run
// is over, so a late callback cannot log into the next run.
type runSink struct {
	m *Manager
	e *entry

	mu     sync.RWMutex
	closed bool
}

func (r *runSink) Emit(ev Event) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	r.m.record(r.e, ev)
}

func (r *runSink) ContentLimit() int { return r.m.opts.ContentLimit }

// close waits for emits in progress and drops later ones.
func (r *runSink) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func addrList(addrs []netip.AddrPort) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, " and ")
}

func errorSummary(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		if e.Hint != "" {
			return e.Message + " " + e.Hint
		}
		return e.Message
	}
	return err.Error()
}

// recentBuffer keeps the newest events within a count and a content budget.
type recentBuffer struct {
	maxEvents, maxBytes int
	events              []Event
	bytes               int
}

func (b *recentBuffer) add(ev Event) {
	b.events = append(b.events, ev)
	b.bytes += len(ev.Content)
	drop := 0
	for len(b.events)-drop > 1 && (len(b.events)-drop > b.maxEvents || b.bytes > b.maxBytes) {
		b.bytes -= len(b.events[drop].Content)
		drop++
	}
	if drop > 0 {
		clear(b.events[:drop])
		b.events = b.events[drop:]
	}
}

func (b *recentBuffer) list() []Event {
	return slices.Clone(b.events)
}

func (b *recentBuffer) clear() {
	b.events, b.bytes = nil, 0
}

// captureBuffer keeps the newest captures within a count and a byte
// budget. A capture larger than the whole budget is not kept.
type captureBuffer struct {
	maxCaptures, maxBytes int
	captures              []Capture
	bytes                 int
}

func (b *captureBuffer) add(c Capture) bool {
	size := c.bytes()
	if size > b.maxBytes {
		return false
	}
	b.captures = append(b.captures, c)
	b.bytes += size
	drop := 0
	for len(b.captures)-drop > b.maxCaptures || b.bytes > b.maxBytes {
		b.bytes -= b.captures[drop].bytes()
		drop++
	}
	if drop > 0 {
		clear(b.captures[:drop])
		b.captures = b.captures[drop:]
	}
	return true
}

func (b *captureBuffer) get(seq uint64) (Capture, bool) {
	i, ok := slices.BinarySearchFunc(b.captures, seq, func(c Capture, seq uint64) int { return cmp.Compare(c.Seq, seq) })
	if !ok {
		return Capture{}, false
	}
	return b.captures[i], true
}

func (b *captureBuffer) list() []Capture {
	return slices.Clone(b.captures)
}

func (b *captureBuffer) clear() {
	b.captures, b.bytes = nil, 0
}
