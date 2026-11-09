package servers

import (
	"slices"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

// CodeNotWebSocket: a client operation on a server that is not a WebSocket
// server.
const CodeNotWebSocket = "servers.not_websocket"

// clientsDelay coalesces the servers:clients events of a busy server: the
// client list goes out at most once per delay, with the latest counts.
const clientsDelay = 200 * time.Millisecond

func invalidMessage(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(wsserver.CodeInvalidMessage)
}

// clientsNotifier collects the servers whose clients changed and publishes
// each once per delay. Publishing is serialized, so the events of a server
// arrive in order.
type clientsNotifier struct {
	delay   time.Duration
	publish func(serverID string)

	mu     sync.Mutex
	dirty  map[string]bool
	timer  *time.Timer
	closed bool

	publishMu sync.Mutex
}

func newClientsNotifier(delay time.Duration, publish func(string)) *clientsNotifier {
	return &clientsNotifier{delay: delay, publish: publish, dirty: map[string]bool{}}
}

// mark notes that the clients of serverID changed. It never blocks for
// long: the manager calls it for every traffic event.
func (n *clientsNotifier) mark(serverID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	n.dirty[serverID] = true
	if n.timer == nil {
		n.timer = time.AfterFunc(n.delay, n.flush)
	}
}

func (n *clientsNotifier) flush() {
	n.publishMu.Lock()
	defer n.publishMu.Unlock()
	n.mu.Lock()
	ids := make([]string, 0, len(n.dirty))
	for id := range n.dirty {
		ids = append(ids, id)
	}
	n.dirty = map[string]bool{}
	n.timer = nil
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return
	}
	slices.Sort(ids)
	for _, id := range ids {
		n.publish(id)
	}
}

// close drops pending and later changes.
func (n *clientsNotifier) close() {
	n.mu.Lock()
	n.closed = true
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()
	n.publishMu.Lock() // wait for a publish in progress
	n.publishMu.Unlock()
}
