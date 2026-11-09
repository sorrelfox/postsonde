package transportws

import (
	"encoding/json"
	"sync"
	"time"
)

// ResultKind is the kind of a Result.
type ResultKind string

// Result kinds. Every kind but ResultNext is terminal: it is the last
// Result of a subscription.
const (
	ResultNext     ResultKind = "next"     // an ExecutionResult
	ResultError    ResultKind = "error"    // the operation failed: GraphQL errors
	ResultComplete ResultKind = "complete" // the server completed the operation
	ResultClosed   ResultKind = "closed"   // the session ended first
)

// Result is one outcome of a subscription.
type Result struct {
	Kind ResultKind
	At   time.Time
	// Payload is the message payload as received: the ExecutionResult for
	// next, the array of GraphQL errors for error.
	Payload json.RawMessage
	// Errors are the decoded GraphQL errors: the "errors" of a next
	// payload, or the error payload. HasErrors is len(Errors) > 0.
	Errors    []GraphQLError
	HasErrors bool
	// Err is why the session ended, for ResultClosed.
	Err error
}

func (r Result) terminal() bool { return r.Kind != ResultNext }

// Subscription is one operation started with Session.Subscribe. Results
// arrive on C in order; C is closed after the terminal Result, or on
// Unsubscribe. Results are queued without limit so a slow reader never
// stalls the connection; read C promptly or Unsubscribe.
type Subscription struct {
	s  *Session
	id string
	c  chan Result

	mu       sync.Mutex
	queue    []Result
	terminal bool // a terminal Result was queued, or Unsubscribe was called
	stopped  bool // stop is closed
	wake     chan struct{}
	stop     chan struct{}
}

func newSubscription(s *Session, id string) *Subscription {
	sub := &Subscription{
		s:    s,
		id:   id,
		c:    make(chan Result),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
	}
	go sub.pump()
	return sub
}

// ID returns the protocol id of the subscription.
func (sub *Subscription) ID() string { return sub.id }

// C returns the channel of results.
func (sub *Subscription) C() <-chan Result { return sub.c }

// push queues r unless the subscription already ended.
func (sub *Subscription) push(r Result) {
	sub.mu.Lock()
	if sub.terminal {
		sub.mu.Unlock()
		return
	}
	sub.queue = append(sub.queue, r)
	if r.terminal() {
		sub.terminal = true
	}
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func (sub *Subscription) pump() {
	defer close(sub.c)
	for {
		sub.mu.Lock()
		if len(sub.queue) == 0 {
			done := sub.terminal
			sub.mu.Unlock()
			if done {
				// The terminal Result was delivered, or Unsubscribe
				// dropped the queue.
				return
			}
			select {
			case <-sub.wake:
			case <-sub.stop:
				return
			}
			continue
		}
		r := sub.queue[0]
		sub.queue[0] = Result{}
		sub.queue = sub.queue[1:]
		sub.mu.Unlock()
		select {
		case sub.c <- r:
		case <-sub.stop:
			return
		}
	}
}

// stopDelivery ends delivery without sending anything: queued results are
// dropped and C is closed. It reports whether the subscription had not
// ended yet.
func (sub *Subscription) stopDelivery() bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	active := !sub.terminal
	sub.terminal = true
	if !sub.stopped {
		sub.stopped = true
		sub.queue = nil
		close(sub.stop)
	}
	return active
}

// Unsubscribe stops the subscription: C is closed (results not yet read
// are dropped) and complete is sent for its id, unless the server already
// ended it (error or complete) or the session ended. It returns an error
// only if sending complete failed.
func (sub *Subscription) Unsubscribe() error {
	s := sub.s
	s.mu.Lock()
	active := s.subs[sub.id] == sub
	if active {
		delete(s.subs, sub.id)
	}
	stopped := sub.stopDelivery()
	open := s.state == StateReady
	s.mu.Unlock()
	if !active || !stopped || !open {
		return nil
	}
	err := s.send(outMessage{Type: s.o.Protocol.stopType(), ID: sub.id})
	if err != nil && s.isEnded() {
		return nil
	}
	return err
}
