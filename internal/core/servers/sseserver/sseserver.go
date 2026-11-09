// Package sseserver is the Server-Sent Events server type (SSRV-04): an
// endpoint that keeps text/event-stream responses open, lists its
// subscribers, pushes events to one or all of them from the app or on a
// timer, keeps the streams alive with comment lines and answers CORS so a
// browser page on another origin can subscribe with EventSource. Every
// subscription, event and close goes to the traffic log. The HTTP side
// (connections, stop) is httpbase. See docs/servers.md.
package sseserver

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TypeName is the Spec.Type of the SSE server.
const TypeName = "sse"

// DefaultWriteTimeout bounds writing one event to a subscriber that does
// not read.
const DefaultWriteTimeout = 10 * time.Second

// KeepAlive is the comment written to keep a stream open.
const KeepAlive = ": keep-alive\n\n"

// Who ended a subscription (servers.SSEInfo.InitiatedBy).
const (
	InitiatedByClient = "client"
	InitiatedByServer = "server"
)

// Error codes of the subscriber operations.
const (
	CodeSubscriberNotFound = "sseserver.subscriber_not_found"
	CodeSendFailed         = "sseserver.send_failed"
)

// Options change timeouts and the clock, for tests.
type Options struct {
	// WriteTimeout: 0 means DefaultWriteTimeout.
	WriteTimeout time.Duration
	// KeepAliveUnit is the unit of Config.KeepAliveSec: 0 means a second.
	KeepAliveUnit time.Duration
	// Now is the clock of dynamic values ({{$timestamp}}); nil means
	// time.Now.
	Now func() time.Time
}

// Type is the SSE server type.
type Type struct {
	opts Options
}

// New returns the SSE server type.
func New(opts Options) Type {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.KeepAliveUnit <= 0 {
		opts.KeepAliveUnit = time.Second
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "SSE" }

// ALPN offers HTTP/1.1 over TLS (SRV-05); HTTP/2 is not served.
func (Type) ALPN() []string { return httpbase.ALPN() }

// NormalizeConfig takes a Config, a *Config or nil (DefaultConfig) and
// returns a normalized Config (Normalize).
func (Type) NormalizeConfig(config any) (any, error) {
	switch c := config.(type) {
	case nil:
		return DefaultConfig(), nil
	case Config:
		return Normalize(c)
	case *Config:
		if c == nil {
			return DefaultConfig(), nil
		}
		return Normalize(*c)
	}
	return nil, errs.New(errs.Internal, fmt.Sprintf("An SSE server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start serves subscribers on ln.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	inst := &Instance{base: httpbase.New(sink), opts: t.opts, subs: map[string]*subscriber{}}
	inst.Reconfigure(cfg)
	inst.base.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

// Message is one event to send. Event "" is the default type
// ("message"); ID "" sends none (or the next number with Config.AutoID);
// RetryMs 0 sends no retry field. Event, Data and ID may hold dynamic
// values ({{$timestamp}}, {{$uuid}}), rendered when it is sent.
type Message struct {
	Event   string
	Data    string
	ID      string
	RetryMs int
}

// Subscriber is a snapshot of a connected subscriber.
type Subscriber struct {
	// ID identifies the subscriber within one run of the server ("1",
	// "2", …), as in the traffic.
	ID string
	// ConnID is the connection it subscribed on.
	ConnID uint64
	Peer   string
	// Target is the request target (path and query).
	Target      string
	ConnectedAt time.Time
	// LastEventID is the Last-Event-ID header it sent: the ID of the last
	// event an EventSource saw before it reconnected.
	LastEventID string
	Origin      string
	UserAgent   string
	// EventsSent and BytesSent count the events written to it (not the
	// keep-alive comments).
	EventsSent uint64
	BytesSent  uint64
}

// SendResult says how many subscribers an event reached, and the ID it
// was sent with.
type SendResult struct {
	Sent   int
	Failed int
	ID     string
}

// Hub is the running SSE server's subscriber operations, implemented by
// *Instance.
type Hub interface {
	Subscribers() []Subscriber
	// Send sends m to the subscriber id, or to every subscriber for "".
	Send(subscriberID string, m Message) (SendResult, error)
	// Disconnect ends the subscriber's response; it returns once it is
	// gone. An EventSource reconnects after its retry time.
	Disconnect(subscriberID string) error
}

var _ Hub = (*Instance)(nil)
var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of an SSE server. It implements servers.Instance,
// servers.Reconfigurable and Hub.
type Instance struct {
	base *httpbase.Instance
	opts Options
	cfg  atomic.Pointer[Config]
	ids  atomic.Uint64 // the last automatic event ID

	mu       sync.Mutex
	subs     map[string]*subscriber
	nextSub  uint64
	stopping bool
	running  sync.WaitGroup // subscriber handlers, added under mu while not stopping

	// timerMu guards the timers of the configuration in use.
	timerMu     sync.Mutex
	timerCancel context.CancelFunc
	timers      sync.WaitGroup

	stopOnce sync.Once
	stopErr  error
}

// subscriber is one open event stream.
type subscriber struct {
	info Subscriber // fixed fields

	eventsSent, bytesSent atomic.Uint64

	jobs     chan job      // writes, done by the handler
	kick     chan struct{} // closed to end the stream from the server
	kickOnce sync.Once
	stopKick atomic.Bool   // kicked because the server stops
	done     chan struct{} // closed once the stream ended and is logged
}

type job struct {
	data  []byte
	event bool // an event, counted; false for a keep-alive
	res   chan error
}

func (s *subscriber) snapshot() Subscriber {
	out := s.info
	out.EventsSent = s.eventsSent.Load()
	out.BytesSent = s.bytesSent.Load()
	return out
}

func (s *subscriber) end(stopping bool) {
	s.kickOnce.Do(func() {
		s.stopKick.Store(stopping)
		close(s.kick)
	})
}

func (i *Instance) Done() <-chan struct{} { return i.base.Done() }
func (i *Instance) Err() error            { return i.base.Err() }

// Reconfigure applies config (a normalized Config): the path and CORS to
// new requests, the keep-alive and retry to new subscribers, the timers
// and automatic IDs at once (the timers restart).
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	i.cfg.Store(&c)
	i.restartTimers(c.Timers)
}

// restartTimers stops the running timers and starts timers, unless the
// server stops.
func (i *Instance) restartTimers(timers []Timer) {
	i.timerMu.Lock()
	defer i.timerMu.Unlock()
	if i.timerCancel != nil {
		i.timerCancel()
		i.timers.Wait()
		i.timerCancel = nil
	}
	i.mu.Lock()
	stopping := i.stopping
	i.mu.Unlock()
	if stopping {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.timerCancel = cancel
	for n, t := range timers {
		if !t.Enabled {
			continue
		}
		i.timers.Go(func() { i.runTimer(ctx, n, t) })
	}
}

// runTimer sends t's event to every subscriber every interval until ctx
// ends. A tick without subscribers sends nothing.
func (i *Instance) runTimer(ctx context.Context, n int, t Timer) {
	tick := time.NewTicker(time.Duration(t.IntervalMs) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_, _ = i.send("", Message{Event: t.Event, Data: t.Data, ID: t.ID}, fmt.Sprintf("timer %d", n+1))
		}
	}
}

// Stop stops the timers, refuses new subscribers, ends every stream, then
// stops the HTTP server.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		list := i.sortedSubs()
		i.mu.Unlock()
		i.restartTimers(nil)
		for _, s := range list {
			s.end(true)
		}
		i.running.Wait()
		i.stopErr = i.base.Stop(ctx)
	})
	return i.stopErr
}

// sortedSubs lists the subscribers by ID; the caller holds mu.
func (i *Instance) sortedSubs() []*subscriber {
	list := make([]*subscriber, 0, len(i.subs))
	for _, s := range i.subs {
		list = append(list, s)
	}
	slices.SortFunc(list, func(a, b *subscriber) int { return cmp.Compare(subNumber(a.info.ID), subNumber(b.info.ID)) })
	return list
}

func subNumber(id string) uint64 {
	n, _ := strconv.ParseUint(id, 10, 64)
	return n
}

// Subscribers returns the connected subscribers, oldest first.
func (i *Instance) Subscribers() []Subscriber {
	i.mu.Lock()
	list := i.sortedSubs()
	i.mu.Unlock()
	out := make([]Subscriber, 0, len(list))
	for _, s := range list {
		out = append(out, s.snapshot())
	}
	return out
}

func (i *Instance) subscriber(id string) (*subscriber, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, ok := i.subs[id]
	if !ok {
		return nil, errs.New(errs.NotFound, "This subscriber is no longer connected.").
			WithCode(CodeSubscriberNotFound).WithDetail("subscriberId", id)
	}
	return s, nil
}

// Send renders m's dynamic values and writes the event to the subscriber
// id, or to every subscriber for "". The event is logged once, before it
// is written; a write that fails is an error event of its subscriber.
// Sending to everyone while nobody is subscribed sends and logs nothing.
func (i *Instance) Send(subscriberID string, m Message) (SendResult, error) {
	return i.send(subscriberID, m, "")
}

func (i *Instance) send(subscriberID string, m Message, source string) (SendResult, error) {
	if err := CheckMessage(m); err != nil {
		return SendResult{}, err
	}
	var targets []*subscriber
	if subscriberID != "" {
		s, err := i.subscriber(subscriberID)
		if err != nil {
			return SendResult{}, err
		}
		targets = []*subscriber{s}
	} else {
		i.mu.Lock()
		targets = i.sortedSubs()
		i.mu.Unlock()
	}
	if len(targets) == 0 {
		return SendResult{}, nil
	}
	r := template.Resolver{Now: i.opts.Now}
	m.Event = oneLine(r.Render(m.Event).Text)
	m.ID = oneLine(r.Render(m.ID).Text)
	m.Data = r.Render(m.Data).Text
	if m.ID == "" && i.cfg.Load().AutoID {
		m.ID = strconv.FormatUint(i.ids.Add(1), 10)
	}
	frame := Frame(m)

	info := &servers.SSEInfo{Event: m.Event, ID: m.ID, Retry: m.RetryMs, Recipients: len(targets), Source: source}
	ev := servers.Event{Direction: servers.Out, Kind: servers.KindMessage, Content: []byte(m.Data), SSE: info}
	who := fmt.Sprintf("%d subscribers", len(targets))
	switch {
	case subscriberID != "":
		who = "subscriber " + subscriberID
		info.SubscriberID = subscriberID
		ev.Peer, ev.ConnID = targets[0].info.Peer, targets[0].info.ConnID
	case len(targets) == 1:
		who = "1 subscriber"
	}
	ev.Summary = eventSummary(m) + " to " + who
	if source != "" {
		ev.Summary += " (" + source + ")"
	}
	i.base.Emit(ev)

	var sent, failed atomic.Int64
	var wg sync.WaitGroup
	for _, s := range targets {
		wg.Go(func() {
			if i.write(s, frame, true) == nil {
				sent.Add(1)
			} else {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	res := SendResult{Sent: int(sent.Load()), Failed: int(failed.Load()), ID: m.ID}
	if subscriberID != "" && res.Failed > 0 {
		return res, errs.New(errs.Network, fmt.Sprintf("The event could not be sent to subscriber %s.", subscriberID)).
			WithCode(CodeSendFailed).WithDetail("subscriberId", subscriberID)
	}
	return res, nil
}

// write hands data to s's handler and waits for the write.
func (i *Instance) write(s *subscriber, data []byte, event bool) error {
	j := job{data: data, event: event, res: make(chan error, 1)}
	select {
	case s.jobs <- j:
		return <-j.res
	case <-s.done:
		return errs.New(errs.NotFound, "This subscriber is no longer connected.").
			WithCode(CodeSubscriberNotFound).WithDetail("subscriberId", s.info.ID)
	}
}

// Disconnect ends the subscriber's stream and returns once it is gone.
func (i *Instance) Disconnect(subscriberID string) error {
	s, err := i.subscriber(subscriberID)
	if err != nil {
		return err
	}
	s.end(false)
	<-s.done
	return nil
}

// eventSummary is `Event “tick” id 5`.
func eventSummary(m Message) string {
	out := "Event"
	if m.Event != "" {
		out += " “" + m.Event + "”"
	}
	if m.ID != "" {
		out += " id " + m.ID
	}
	return out
}

// oneLine keeps a rendered type or ID one line: a dynamic value never
// holds a line break, but a user's variable could.
func oneLine(s string) string {
	for n, r := range s {
		if r == '\r' || r == '\n' || r == 0 {
			return s[:n]
		}
	}
	return s
}

// serveHTTP answers a preflight, refuses what is not a subscription and
// serves a subscription until it ends.
func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := i.cfg.Load()
	h := w.Header()
	if cfg.Path != AnyPath && cfg.Path != r.URL.Path {
		i.reject(w, r, http.StatusNotFound, "no SSE endpoint on this path",
			fmt.Sprintf("No event stream at %s. This server sends events at %s.\n", r.URL.Path, cfg.Path))
		return
	}
	if r.Method == http.MethodOptions {
		i.preflight(w, r, cfg)
		return
	}
	if r.Method != http.MethodGet {
		h.Set("Allow", "GET, OPTIONS")
		i.reject(w, r, http.StatusMethodNotAllowed, "subscribe with GET", "Subscribe to the event stream with a GET request.\n")
		return
	}
	i.mu.Lock()
	stopping := i.stopping
	i.mu.Unlock()
	if stopping {
		i.reject(w, r, http.StatusServiceUnavailable, "the server is stopping", "The server is stopping.\n")
		return
	}
	i.subscribe(w, r, cfg)
}

// setCORS adds the CORS headers of cfg for the request r.
func setCORS(h http.Header, r *http.Request, cfg *Config) {
	switch cfg.AllowOrigin {
	case "":
		return
	case OriginAny:
		if origin := r.Header.Get("Origin"); origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
		} else {
			h.Set("Access-Control-Allow-Origin", "*")
		}
	default:
		h.Set("Access-Control-Allow-Origin", cfg.AllowOrigin)
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	h.Add("Vary", "Origin")
}

// preflight answers a CORS preflight request: 204, with the CORS headers
// when the server allows other origins.
func (i *Instance) preflight(w http.ResponseWriter, r *http.Request, cfg *Config) {
	peer, connID := i.base.ReadRequest(r, nil)
	h := w.Header()
	why := "CORS preflight; other origins are not allowed"
	if cfg.AllowOrigin != "" {
		setCORS(h, r, cfg)
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
			h.Set("Access-Control-Allow-Headers", req)
		}
		h.Set("Access-Control-Max-Age", "600")
		why = "CORS preflight"
	}
	w.WriteHeader(http.StatusNoContent)
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: fmt.Sprintf("204 No Content (%s)", why),
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: http.StatusNoContent, Headers: httpbase.HeaderList(h)},
	})
}

// reject answers r with status and a text body and logs both.
func (i *Instance) reject(w http.ResponseWriter, r *http.Request, status int, why, body string) {
	peer, connID := i.base.ReadRequest(r, nil)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	var content []byte
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
		content = []byte(body)
	}
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: fmt.Sprintf("%d %s (%s)", status, http.StatusText(status), why),
		Content: content,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: status, Headers: httpbase.HeaderList(h)},
	})
}

// subscribe opens the event stream and serves it until the client leaves,
// the app disconnects it, a write fails or the server stops.
func (i *Instance) subscribe(w http.ResponseWriter, r *http.Request, cfg *Config) {
	connID := i.base.ConnID(r)
	peer := r.RemoteAddr
	lastID := r.Header.Get("Last-Event-ID")

	i.mu.Lock()
	if i.stopping {
		i.mu.Unlock()
		i.reject(w, r, http.StatusServiceUnavailable, "the server is stopping", "The server is stopping.\n")
		return
	}
	i.nextSub++
	id := strconv.FormatUint(i.nextSub, 10)
	s := &subscriber{
		info: Subscriber{
			ID: id, ConnID: connID, Peer: peer, Target: r.RequestURI, ConnectedAt: time.Now(),
			LastEventID: lastID, Origin: r.Header.Get("Origin"), UserAgent: r.Header.Get("User-Agent"),
		},
		jobs: make(chan job),
		kick: make(chan struct{}),
		done: make(chan struct{}),
	}
	i.running.Add(1)
	i.mu.Unlock()
	defer i.running.Done()

	info := func() *servers.SSEInfo { return &servers.SSEInfo{SubscriberID: id, LastEventID: lastID} }
	summary := r.Method + " " + r.RequestURI
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.In, Kind: servers.KindRequest, Summary: summary,
		HTTP: &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Headers: httpbase.RequestHeaders(r)},
		SSE:  info(),
	})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	setCORS(h, r, cfg)
	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	var first []byte
	if cfg.RetryMs > 0 {
		first = []byte("retry: " + strconv.Itoa(cfg.RetryMs) + "\n\n")
	}
	_ = rc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
	_, err := w.Write(first)
	if err == nil {
		err = rc.Flush()
	}

	resp := "200 OK: subscriber " + id
	if lastID != "" {
		resp += " (Last-Event-ID “" + lastID + "”)"
	}
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse, Summary: resp,
		HTTP: &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: http.StatusOK, Headers: httpbase.HeaderList(h)},
		SSE:  info(),
	})

	i.mu.Lock()
	i.subs[id] = s
	if i.stopping {
		s.end(true) // Stop listed the subscribers before this one
	}
	i.mu.Unlock()

	how := i.serveStream(w, r, rc, s, cfg, err)

	i.mu.Lock()
	delete(i.subs, id)
	i.mu.Unlock()
	close(s.done)

	end := info()
	var text string
	totals := fmt.Sprintf("%d events sent", s.eventsSent.Load())
	if s.eventsSent.Load() == 1 {
		totals = "1 event sent"
	}
	switch how.by {
	case endStop:
		end.InitiatedBy, text = InitiatedByServer, "Closed: the server is stopping ("+totals+")"
	case endServer:
		end.InitiatedBy, text = InitiatedByServer, "Disconnected by the server ("+totals+")"
	case endClient:
		end.InitiatedBy, text = InitiatedByClient, "Subscriber "+id+" left ("+totals+")"
	default:
		i.base.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: fmt.Sprintf("Subscriber %s: writing failed: %v", id, how.err), SSE: info(),
		})
		text = "Connection lost (" + totals + ")"
	}
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindConnectionClose, Summary: text, SSE: end,
	})
	if how.by == endLost {
		panic(http.ErrAbortHandler) // drop the connection: the stream is broken
	}
}

type endKind int

const (
	endClient endKind = iota
	endServer
	endStop
	endLost
)

type ending struct {
	by  endKind
	err error
}

// serveStream writes the jobs and keep-alives of s until it ends.
func (i *Instance) serveStream(w http.ResponseWriter, r *http.Request, rc *http.ResponseController, s *subscriber, cfg *Config, err error) ending {
	if err != nil {
		return ending{by: endLost, err: err}
	}
	var keepAlive <-chan time.Time
	if cfg.KeepAliveSec > 0 {
		t := time.NewTicker(time.Duration(cfg.KeepAliveSec) * i.opts.KeepAliveUnit)
		defer t.Stop()
		keepAlive = t.C
	}
	write := func(data []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
		if _, err := w.Write(data); err != nil {
			return err
		}
		return rc.Flush()
	}
	failed := func(err error) ending {
		if r.Context().Err() != nil {
			return ending{by: endClient} // the client left while it was written to
		}
		return ending{by: endLost, err: err}
	}
	for {
		select {
		case j := <-s.jobs:
			err := write(j.data)
			if err == nil && j.event {
				s.eventsSent.Add(1)
				s.bytesSent.Add(uint64(len(j.data)))
			}
			j.res <- err
			if err != nil {
				return failed(err)
			}
		case <-keepAlive:
			if err := write([]byte(KeepAlive)); err != nil {
				return failed(err)
			}
		case <-r.Context().Done():
			return ending{by: endClient}
		case <-s.kick:
			if s.stopKick.Load() {
				return ending{by: endStop}
			}
			return ending{by: endServer}
		}
	}
}
