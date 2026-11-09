package transportws

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Defaults for zero Options fields.
const (
	// DefaultConnectionInitTimeout is how long the client waits for
	// connection_ack after sending connection_init (the reference client's
	// and server's default).
	DefaultConnectionInitTimeout = 3 * time.Second
	// DefaultCloseTimeout bounds the close handshake the session starts on
	// its own (invalid message, ack timeout, subprotocol not selected).
	DefaultCloseTimeout = 5 * time.Second
)

// Options configure a Session.
type Options struct {
	// Protocol is the wire protocol: ProtocolTransportWS (also when
	// empty) or ProtocolLegacy.
	Protocol Protocol
	// WS configures the WebSocket connection. Subprotocols is replaced by
	// the protocol's (graphql-transport-ws or graphql-ws). OnState,
	// OnMessage and OnFrame are still called (after the session's own
	// handling).
	WS wsclient.Options
	// ConnectionParams is the payload of connection_init: a JSON object or
	// null. Empty sends connection_init without a payload.
	ConnectionParams json.RawMessage
	// ConnectionInitTimeout is how long to wait for connection_ack before
	// closing with 4408. Zero means DefaultConnectionInitTimeout; negative
	// waits forever.
	ConnectionInitTimeout time.Duration
	// PingInterval, if positive, sends a ping at this interval once
	// connection_init was sent. Off by default; pongs are only logged.
	// The legacy protocol has no ping and ignores it.
	PingInterval time.Duration
	// EchoPingPayload sends the payload of the server's ping back in the
	// pong. By default pongs carry no payload.
	EchoPingPayload bool
	// CloseTimeout bounds the close handshakes the session starts itself.
	// Zero means DefaultCloseTimeout.
	CloseTimeout time.Duration
	// OnEvent is called for every protocol message sent and received, in
	// order, serialized. Received events run on the read goroutine, which
	// waits for them; OnEvent must not call Close.
	OnEvent func(Event)
}

// State is the state of a Session once Dial returned.
type State string

// Session states.
const (
	StateInitialising State = "initialising" // connection_init sent, waiting for connection_ack
	StateReady        State = "ready"        // acknowledged; subscriptions run
	StateClosing      State = "closing"      // closing the WebSocket connection
	StateClosed       State = "closed"
)

// Direction says whether an Event was sent or received.
type Direction string

// Directions.
const (
	Sent     Direction = "sent"
	Received Direction = "received"
)

// Note marks a received message that was not acted on.
type Note string

// Notes.
const (
	// NoteUnknownID: next, error or complete for an id with no active
	// subscription (e.g. crossing an Unsubscribe); ignored, as the
	// protocol allows.
	NoteUnknownID Note = "unknown_id"
	// NoteIgnored: received after the session started closing because of
	// a protocol violation.
	NoteIgnored Note = "ignored"
)

// Event is one protocol message sent or received.
type Event struct {
	At        time.Time
	Direction Direction
	// Type, ID and Payload are the parsed message; empty when a received
	// message could not be parsed.
	Type    MessageType
	ID      string
	Payload json.RawMessage
	// Data is the message as on the wire.
	Data []byte
	// Binary is set for a binary message (invalid in this protocol).
	Binary bool
	// Err is set for a received message that broke the protocol; the
	// session closes with 4400.
	Err  error
	Note Note
}

// Session is one GraphQL over WebSocket connection. Its methods are safe
// for concurrent use.
type Session struct {
	o      Options
	client *wsclient.Client
	now    func() time.Time

	evMu   sync.Mutex // serializes OnEvent
	sendMu sync.Mutex // keeps sent events in wire order

	mu         sync.Mutex
	state      State
	ackPayload json.RawMessage
	acked      chan struct{}
	ackTimer   *time.Timer
	nextID     uint64
	subs       map[string]*Subscription
	failErr    error // set when the session closes itself
	localClose bool
	err        error
	done       chan struct{}
}

// Dial connects to the server, checks that it selected the protocol's
// subprotocol and sends connection_init. It returns
// without waiting for connection_ack (see Acked and Subscribe); ctx bounds
// the WebSocket handshake. A server that answers without the subprotocol
// is closed with 4406 and Dial fails with CodeSubprotocolNotSelected.
func Dial(ctx context.Context, o Options) (*Session, error) {
	params, ok := checkObject(o.ConnectionParams)
	if !ok {
		return nil, invalidArgument("The connection params must be a JSON object.").
			WithHint("Enter a JSON object like {\"authToken\": \"...\"}, or leave the connection params empty.")
	}
	if o.ConnectionInitTimeout == 0 {
		o.ConnectionInitTimeout = DefaultConnectionInitTimeout
	}
	if o.CloseTimeout <= 0 {
		o.CloseTimeout = DefaultCloseTimeout
	}
	switch o.Protocol {
	case "":
		o.Protocol = ProtocolTransportWS
	case ProtocolTransportWS, ProtocolLegacy:
	default:
		return nil, invalidArgument("Unknown GraphQL over WebSocket protocol " + strconv.Quote(string(o.Protocol)) + ".")
	}
	if o.Protocol.legacy() {
		o.PingInterval = 0
	}
	s := &Session{
		o:     o,
		now:   o.WS.Now,
		state: StateInitialising,
		acked: make(chan struct{}),
		subs:  map[string]*Subscription{},
		done:  make(chan struct{}),
	}
	if s.now == nil {
		s.now = time.Now
	}

	wo := o.WS
	wo.Subprotocols = []string{o.Protocol.Subprotocol()}
	userState, userMessage := o.WS.OnState, o.WS.OnMessage
	wo.OnState = func(ch wsclient.StateChange) {
		if ch.State == wsclient.StateClosed {
			s.finish(ch)
		}
		if userState != nil {
			userState(ch)
		}
	}
	wo.OnMessage = func(m wsclient.Message) {
		s.receive(m)
		if userMessage != nil {
			userMessage(m)
		}
	}
	client, err := wsclient.New(wo)
	if err != nil {
		return nil, err
	}
	s.client = client
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}

	if got := client.Subprotocol(); got != o.Protocol.Subprotocol() {
		e := subprotocolNotSelected(o.Protocol, got)
		s.fail(e, CloseSubprotocolNotAcceptable, "Subprotocol not acceptable", false)
		<-s.done
		return nil, e
	}

	if o.ConnectionInitTimeout > 0 {
		s.mu.Lock()
		s.ackTimer = time.AfterFunc(o.ConnectionInitTimeout, s.ackTimedOut)
		s.mu.Unlock()
	}
	if err := s.send(outMessage{Type: TypeConnectionInit, Payload: params}); err != nil {
		// The connection closed right after the upgrade (e.g. the server
		// closed with 4406): report why.
		<-s.done
		if e := s.Err(); e != nil {
			return nil, e
		}
		return nil, err
	}
	if o.PingInterval > 0 {
		go s.pingLoop(o.PingInterval)
	}
	return s, nil
}

// Client returns the WebSocket client, for the handshake and close
// details.
func (s *Session) Client() *wsclient.Client { return s.client }

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Acked is closed when connection_ack arrived.
func (s *Session) Acked() <-chan struct{} { return s.acked }

// AckPayload returns the payload of connection_ack (nil if it had none or
// has not arrived).
func (s *Session) AckPayload() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ackPayload
}

// Done is closed when the session ended; every subscription has ended by
// then.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns why the session ended: nil if it has not ended or ended
// with Close; CodeInvalidMessage, CodeAckTimeout, CodeClosedByServer (for
// every close the server started, 1000 included) or a wsclient/wsframe
// error when the connection failed.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// CloseInfo returns how the WebSocket connection ended, nil until closed.
func (s *Session) CloseInfo() *wsclient.CloseInfo { return s.client.CloseInfo() }

// Close closes the WebSocket connection with code and reason (as
// wsclient.Client.Close) and waits for it to end. Active subscriptions end
// with a ResultClosed carrying CodeSessionClosed. It must not be called
// from OnEvent or a WebSocket callback.
func (s *Session) Close(ctx context.Context, code wsframe.StatusCode, reason string) error {
	if _, err := wsframe.AppendClosePayload(nil, code, reason); err != nil {
		return err
	}
	s.mu.Lock()
	if s.state == StateClosed {
		s.mu.Unlock()
		return nil
	}
	terminate := s.o.Protocol.legacy() && s.state != StateClosing && s.failErr == nil
	s.localClose = true
	s.state = StateClosing
	s.stopAckTimerLocked()
	s.mu.Unlock()
	if terminate {
		// The legacy protocol says goodbye before the close frame.
		_ = s.send(outMessage{Type: TypeConnectionTerminate})
	}
	err := s.client.Close(ctx, code, reason)
	<-s.done
	return err
}

// Ping sends a ping with an optional payload (a JSON object or null). The
// legacy protocol has no ping: it fails with CodeInvalidArgument.
func (s *Session) Ping(payload json.RawMessage) error {
	if s.o.Protocol.legacy() {
		return pingUnsupported()
	}
	p, ok := checkObject(payload)
	if !ok {
		return invalidArgument("The ping payload must be a JSON object.")
	}
	return s.send(outMessage{Type: TypePing, Payload: p})
}

// SubscribeRequest is the payload of a subscribe message. Variables and
// Extensions are JSON objects (or null); empty leaves them out, as does
// an empty OperationName.
type SubscribeRequest struct {
	Query         string
	OperationName string
	Variables     json.RawMessage
	Extensions    json.RawMessage
}

// Subscribe waits until the server acknowledged the connection (or ctx
// ends, or the session ends), then sends subscribe with a new id and
// returns the Subscription. ctx bounds the wait only, not the
// subscription. Any operation can be sent: a query or mutation gets one
// next and complete.
func (s *Session) Subscribe(ctx context.Context, req SubscribeRequest) (*Subscription, error) {
	vars, ok := checkObject(req.Variables)
	if !ok {
		return nil, invalidArgument("The variables must be a JSON object.")
	}
	ext, ok := checkObject(req.Extensions)
	if !ok {
		return nil, invalidArgument("The extensions must be a JSON object.")
	}
	payload, err := encode(subscribePayload{Query: req.Query, OperationName: req.OperationName, Variables: vars, Extensions: ext})
	if err != nil {
		return nil, invalidArgument("The subscribe payload could not be encoded.")
	}

	select {
	case <-s.acked:
	case <-s.done:
		return nil, s.endErr()
	case <-ctx.Done():
		return nil, cancelled(ctx.Err())
	}

	s.mu.Lock()
	if s.state != StateReady {
		s.mu.Unlock()
		return nil, s.endErr()
	}
	id := ""
	for {
		s.nextID++
		id = strconv.FormatUint(s.nextID, 10)
		if _, taken := s.subs[id]; !taken {
			break
		}
	}
	sub := newSubscription(s, id)
	s.subs[id] = sub
	s.mu.Unlock()

	if err := s.send(outMessage{Type: s.o.Protocol.subscribeType(), ID: id, Payload: payload}); err != nil {
		s.mu.Lock()
		if s.subs[id] == sub {
			delete(s.subs, id)
		}
		s.mu.Unlock()
		sub.stopDelivery()
		if e := s.endErr(); s.isEnded() && e != nil {
			return nil, e
		}
		return nil, err
	}
	return sub, nil
}

func (s *Session) isEnded() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// endErr is the error for operations on an ended or closing session.
func (s *Session) endErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.failErr != nil {
		return s.failErr
	}
	return sessionClosed()
}

// send encodes m, reports it and writes it.
func (s *Session) send(m outMessage) error {
	data, err := encode(m)
	if err != nil {
		return invalidArgument("The message could not be encoded.")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.emit(Event{At: s.now(), Direction: Sent, Type: m.Type, ID: m.ID, Payload: m.Payload, Data: data})
	return s.client.SendText(string(data))
}

func (s *Session) emit(ev Event) {
	if s.o.OnEvent == nil {
		return
	}
	s.evMu.Lock()
	defer s.evMu.Unlock()
	s.o.OnEvent(ev)
}

// receive handles one WebSocket message; it runs on the read goroutine.
func (s *Session) receive(wm wsclient.Message) {
	ev := Event{At: wm.At, Direction: Received, Data: wm.Data, Binary: wm.Opcode == wsframe.OpBinary}
	s.mu.Lock()
	failing := s.failErr != nil
	s.mu.Unlock()

	var m inMessage
	problem := ""
	if ev.Binary {
		problem = "a binary message was received; the protocol uses text messages"
	} else {
		m, problem = parse(wm.Data, s.o.Protocol)
		ev.Type, ev.ID, ev.Payload = m.Type, m.ID, m.Payload
	}
	if failing {
		ev.Note = NoteIgnored
		s.emit(ev)
		return
	}
	if problem != "" {
		e := invalidMessage(s.o.Protocol, problem)
		ev.Err = e
		s.emit(ev)
		s.fail(e, CloseBadRequest, "Invalid message received", true)
		return
	}

	switch m.Type {
	case TypeConnectionAck:
		if isClosed(s.acked) {
			e := invalidMessage(s.o.Protocol, "connection_ack was received twice")
			ev.Err = e
			s.emit(ev)
			s.fail(e, CloseBadRequest, "Invalid message received", true)
			return
		}
		s.emit(ev)
		s.mu.Lock()
		s.stopAckTimerLocked()
		if s.state == StateInitialising {
			s.state = StateReady
		}
		if string(m.Payload) != "null" {
			s.ackPayload = m.Payload
		}
		close(s.acked)
		s.mu.Unlock()

	case TypePing:
		s.emit(ev)
		pong := outMessage{Type: TypePong}
		if s.o.EchoPingPayload {
			pong.Payload = m.Payload
		}
		_ = s.send(pong)

	case TypePong, TypeKeepAlive:
		s.emit(ev)

	case TypeConnectionError:
		// Legacy: the server refused connection_init; it closes the
		// connection itself, the client too.
		e := connectionError(m.Payload)
		ev.Err = e
		s.emit(ev)
		s.fail(e, wsframe.CloseNormalClosure, "Connection error", true)

	case TypeNext, TypeData, TypeError, TypeComplete:
		// A legacy error without an id (a message the server could not
		// read) belongs to no operation: logged as unknown_id below.
		if !isClosed(s.acked) && m.ID != "" {
			e := invalidMessage(s.o.Protocol, string(m.Type)+" was received before connection_ack")
			ev.Err = e
			s.emit(ev)
			s.fail(e, CloseBadRequest, "Invalid message received", true)
			return
		}
		s.mu.Lock()
		sub := s.subs[m.ID]
		if sub == nil {
			s.mu.Unlock()
			ev.Note = NoteUnknownID
			s.emit(ev)
			return
		}
		s.mu.Unlock()
		// The event goes out before the result, so logs show the message
		// first.
		s.emit(ev)
		r := Result{At: wm.At, Payload: m.Payload}
		switch m.Type {
		case TypeNext, TypeData:
			r.Kind = ResultNext
			r.Errors = resultErrors(m.Payload)
			r.HasErrors = len(r.Errors) > 0
		case TypeError:
			r.Kind = ResultError
			r.Errors = decodeErrors(m.Payload)
			r.HasErrors = true
		case TypeComplete:
			r.Kind = ResultComplete
		}
		s.mu.Lock()
		if s.subs[m.ID] == sub {
			if r.terminal() {
				delete(s.subs, m.ID)
			}
			sub.push(r)
		}
		s.mu.Unlock()
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// fail closes the session because of err with a close code; async must be
// set on the read goroutine, which the close handshake needs.
func (s *Session) fail(err error, code wsframe.StatusCode, reason string, async bool) {
	s.mu.Lock()
	if s.failErr != nil || s.state == StateClosed {
		s.mu.Unlock()
		return
	}
	s.failErr = err
	s.state = StateClosing
	s.stopAckTimerLocked()
	s.mu.Unlock()
	closeConn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.o.CloseTimeout)
		defer cancel()
		_ = s.client.Close(ctx, code, reason)
	}
	if async {
		go closeConn()
		return
	}
	closeConn()
}

func (s *Session) ackTimedOut() {
	if isClosed(s.acked) {
		return
	}
	s.fail(ackTimeout(), CloseConnectionInitTimeout, "Connection initialisation timeout", false)
}

func (s *Session) stopAckTimerLocked() {
	if s.ackTimer != nil {
		s.ackTimer.Stop()
	}
}

func (s *Session) pingLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = s.send(outMessage{Type: TypePing})
		case <-s.done:
			return
		}
	}
}

// finish runs when the WebSocket connection closed.
func (s *Session) finish(ch wsclient.StateChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopAckTimerLocked()
	var err error
	switch {
	case s.failErr != nil:
		err = s.failErr
	case ch.Err != nil:
		err = ch.Err
	case s.localClose:
	case ch.Close != nil && ch.Close.Received != nil && ch.Close.InitiatedBy == wsclient.InitiatorRemote:
		err = closedByServer(ch.Close.Received.Code, ch.Close.Received.Reason)
	default:
		// Client().Abort(): ended locally, like Close.
	}
	s.err = err
	s.state = StateClosed
	subErr := err
	if subErr == nil {
		subErr = sessionClosed()
	}
	for id, sub := range s.subs {
		sub.push(Result{Kind: ResultClosed, At: ch.At, Err: subErr})
		delete(s.subs, id)
	}
	close(s.done)
}
