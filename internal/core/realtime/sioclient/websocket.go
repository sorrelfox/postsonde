package sioclient

import (
	"context"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// wsTransport is the Engine.IO WebSocket transport: one packet per
// message. Until attach, received packets queue for next (the open packet,
// the probe's pong); then they go to the client.
type wsTransport struct {
	cl      *wsclient.Client
	maxSize int
	// v3 writes and reads Engine.IO 3 binary messages (Socket.IO v2).
	v3 bool

	mu      sync.Mutex
	handler func(engineio.Packet)
	queue   []engineio.Packet
	arrived chan struct{}
	down    func(error)
	dead    bool
	deadErr error
	// decodeErr is why the transport was aborted: a message that is not
	// an Engine.IO packet.
	decodeErr error
}

func (w *wsTransport) name() string { return "websocket" }

// dialWebSocket opens a WebSocket to the Engine.IO endpoint, joining the
// session sid when it is not "" (an upgrade).
func (c *Client) dialWebSocket(ctx context.Context, sid string) (*wsTransport, error) {
	w := &wsTransport{arrived: make(chan struct{}, 1), maxSize: c.o.MaxMessageSize, v3: c.v2}
	o := wsclient.Options{
		URL:                c.t.url(true, sid, c.eio()),
		Headers:            c.headers,
		Dialer:             c.o.Dialer,
		RootCAs:            c.o.RootCAs,
		InsecureSkipVerify: c.o.InsecureSkipVerify,
		ClientCertificate:  c.o.ClientCertificate,
		Jar:                c.o.Jar,
		HandshakeTimeout:   -1, // ctx bounds it
		MaxMessageSize:     int64(c.o.MaxMessageSize),
		OnMessage:          w.onMessage,
		OnState: func(ch wsclient.StateChange) {
			if ch.State == wsclient.StateClosed {
				w.closed(ch.Err)
			}
		},
		Now: c.o.Now,
	}
	cl, err := wsclient.New(o)
	if err != nil {
		return nil, err
	}
	w.cl = cl
	if err := cl.Connect(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

// openWebSocket connects over WebSocket and reads the open packet.
func (c *Client) openWebSocket(ctx context.Context) (*wsTransport, engineio.Handshake, error) {
	w, err := c.dialWebSocket(ctx, "")
	if err != nil {
		return nil, engineio.Handshake{}, err
	}
	p, err := w.next(ctx)
	if err != nil {
		w.abort()
		return nil, engineio.Handshake{}, err
	}
	if p.Type != engineio.Open {
		w.abort()
		return nil, engineio.Handshake{}, errs.New(errs.Protocol, "The server did not start with an Engine.IO open packet; the first packet was "+p.Type.String()+".").
			WithCode(CodeInvalidHandshake).
			WithHint(versionHint(c.v2))
	}
	hs, err := engineio.ParseHandshake(p.Data)
	if err != nil {
		w.abort()
		return nil, engineio.Handshake{}, err
	}
	return w, hs, nil
}

func (w *wsTransport) onMessage(m wsclient.Message) {
	decode := engineio.DecodePacket
	if w.v3 {
		decode = engineio.DecodePacketV3
	}
	p, err := decode(m.Data, m.Opcode == wsframe.OpBinary, engineio.Options{MaxSize: w.maxSize})
	if err != nil {
		w.mu.Lock()
		w.decodeErr = err
		w.mu.Unlock()
		w.cl.Abort()
		return
	}
	w.mu.Lock()
	h := w.handler
	if h == nil {
		w.queue = append(w.queue, p)
		w.mu.Unlock()
		select {
		case w.arrived <- struct{}{}:
		default:
		}
		return
	}
	w.mu.Unlock()
	h(p)
}

// next returns the next queued packet, before attach.
func (w *wsTransport) next(ctx context.Context) (engineio.Packet, error) {
	for {
		w.mu.Lock()
		if len(w.queue) > 0 {
			p := w.queue[0]
			w.queue = w.queue[1:]
			w.mu.Unlock()
			return p, nil
		}
		dead, err := w.dead, w.deadErr
		w.mu.Unlock()
		if dead {
			if err == nil {
				err = errs.New(errs.Network, "The server closed the WebSocket during the Engine.IO handshake.").WithCode(CodeTransportError)
			}
			return engineio.Packet{}, err
		}
		select {
		case <-w.arrived:
		case <-w.cl.Done():
		case <-ctx.Done():
			return engineio.Packet{}, ctx.Err()
		}
	}
}

// attach hands received packets and the transport's end to c, the queued
// packets first.
func (w *wsTransport) attach(c *Client) {
	w.mu.Lock()
	w.down = func(err error) { c.transportDown(w, err) }
	w.handler = c.receive
	q := w.queue
	w.queue = nil
	dead, err := w.dead, w.deadErr
	w.mu.Unlock()
	for _, p := range q {
		c.receive(p)
	}
	if dead {
		c.transportDown(w, err)
	}
}

func (w *wsTransport) closed(err error) {
	w.mu.Lock()
	if w.decodeErr != nil {
		err = w.decodeErr
	}
	w.dead, w.deadErr = true, err
	d := w.down
	w.mu.Unlock()
	select {
	case w.arrived <- struct{}{}:
	default:
	}
	if d != nil {
		// Not on the callback's goroutine: the client may abort.
		go d(err)
	}
}

func (w *wsTransport) send(pkts []engineio.Packet) error {
	encode := engineio.EncodePacket
	if w.v3 {
		encode = engineio.EncodePacketV3
	}
	for _, p := range pkts {
		data, binary, err := encode(p)
		if err != nil {
			return err
		}
		op := wsframe.OpText
		if binary {
			op = wsframe.OpBinary
		}
		if err := w.cl.Send(op, data); err != nil {
			return err
		}
	}
	return nil
}

func (w *wsTransport) close(ctx context.Context) {
	if err := w.cl.Close(ctx, wsframe.CloseNormalClosure, ""); err != nil {
		w.cl.Abort()
	}
}

func (w *wsTransport) abort() { w.cl.Abort() }
