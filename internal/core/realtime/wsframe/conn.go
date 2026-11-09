package wsframe

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Role is the side of the connection. It decides masking (RFC 6455 §5.1):
// a client masks every frame it sends and fails on masked frames from the
// server; a server sends unmasked frames and fails on unmasked frames from
// the client.
type Role uint8

const (
	RoleClient Role = iota + 1
	RoleServer
)

func (r Role) String() string {
	switch r {
	case RoleClient:
		return "client"
	case RoleServer:
		return "server"
	}
	return "role(" + strconv.Itoa(int(r)) + ")"
}

// Direction says whether a frame passed to Options.OnFrame was received or
// sent.
type Direction uint8

const (
	Received Direction = iota + 1
	Sent
)

// DefaultMaxMessageSize is the default limit of a reassembled message (32 MiB).
const DefaultMaxMessageSize = 32 << 20

// Options configure a Conn.
type Options struct {
	// Role is RoleClient or RoleServer. Required.
	Role Role
	// Limits apply to every received frame.
	Limits Limits
	// MaxMessageSize limits a reassembled data message in bytes. 0 means
	// DefaultMaxMessageSize; a negative value means no limit.
	MaxMessageSize int64
	// DisableAutoPong stops ReadMessage from answering pings. The
	// application then sees pings through OnFrame and answers with Pong.
	DisableAutoPong bool
	// OnFrame, if set, is called for every frame read (after it was
	// validated as a frame, before message-level checks) and every frame
	// written. Payload is unmasked and only valid during the call. It runs
	// on the reading or writing goroutine and must not block; it may call
	// Pong or Ping.
	OnFrame func(dir Direction, f Frame)
	// Rand is the source of mask keys. nil means crypto/rand.
	Rand io.Reader
	// Reader, if set, is read instead of the connection, for bytes the
	// upgrade handshake already buffered. It must read from the connection.
	Reader *bufio.Reader
}

// Conn is the message layer of a WebSocket connection over an
// io.ReadWriter, usually the net.Conn after the upgrade handshake.
//
// One goroutine may read (ReadMessage) while others write: writes are
// serialized frame by frame, so Ping, Pong and Close interleave between
// the fragments of a message sent with NextWriter, and data messages never
// interleave with each other.
//
// Conn never closes the transport. After ReadMessage fails, or after Close
// returns, close the net.Conn; that also releases a read still blocked in
// the transport.
type Conn struct {
	rw       io.ReadWriter
	br       io.Reader
	role     Role
	lim      Limits
	maxMsg   int64
	autoPong bool
	onFrame  func(Direction, Frame)
	rand     io.Reader

	readMu  sync.Mutex
	hb      [MaxHeaderSize]byte
	ctrl    [MaxControlPayload]byte
	readErr error
	text    utf8Validator

	dataMu sync.Mutex // held by one data message writer at a time

	writeMu   sync.Mutex // held while one frame is written
	wbuf      []byte
	closeSent bool
	writeErr  error

	closeOnce sync.Once
	closeRecv chan struct{} // closed when the peer's close frame arrives
	peerClose *CloseError   // set before closeRecv is closed
}

// NewConn returns a Conn over rw. It panics if o.Role is not RoleClient or
// RoleServer.
func NewConn(rw io.ReadWriter, o Options) *Conn {
	if o.Role != RoleClient && o.Role != RoleServer {
		panic("wsframe: NewConn needs RoleClient or RoleServer")
	}
	c := &Conn{
		rw:        rw,
		br:        bufio.NewReaderSize(rw, 4096),
		role:      o.Role,
		lim:       o.Limits,
		autoPong:  !o.DisableAutoPong,
		onFrame:   o.OnFrame,
		rand:      o.Rand,
		closeRecv: make(chan struct{}),
	}
	if o.Reader != nil {
		c.br = o.Reader
	}
	if c.rand == nil {
		c.rand = rand.Reader
	}
	switch {
	case o.MaxMessageSize == 0:
		c.maxMsg = DefaultMaxMessageSize
	case o.MaxMessageSize < 0:
		c.maxMsg = math.MaxInt
	default:
		c.maxMsg = min(o.MaxMessageSize, math.MaxInt)
	}
	return c
}

// Role returns the connection's role.
func (c *Conn) Role() Role { return c.role }

// PeerClose returns the close frame received from the peer, or nil.
func (c *Conn) PeerClose() *CloseError {
	select {
	case <-c.closeRecv:
		return c.peerClose
	default:
		return nil
	}
}

// ReadMessage reads the next data message, reassembling fragments and
// handling control frames in between. op is OpText or OpBinary.
//
// When the peer closes, ReadMessage echoes the close frame (unless this side
// already sent one) and returns an error with code CodePeerClosed wrapping
// a *CloseError. A protocol violation sends a close frame with the matching
// code (1002, 1007, 1009) and returns the error. Errors are sticky: every
// later call returns the same error.
func (c *Conn) ReadMessage() (op Opcode, data []byte, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return c.readMessage()
}

func (c *Conn) readMessage() (Opcode, []byte, error) {
	if c.readErr != nil {
		return 0, nil, c.readErr
	}
	op, data, err := c.readMessageFrames()
	if err != nil {
		c.readErr = err
		if code, ok := failsConnection(err); ok {
			c.sendCloseBestEffort(code)
		}
		return 0, nil, err
	}
	return op, data, nil
}

func (c *Conn) readMessageFrames() (Opcode, []byte, error) {
	var (
		msgOp Opcode
		msg   []byte
		open  bool
	)
	for {
		h, err := readHeader(c.br, c.hb[:], c.lim)
		if err != nil {
			return 0, nil, c.readError(err, true)
		}
		switch {
		case c.role == RoleServer && !h.Masked:
			return 0, nil, protocolError("The client sent an unmasked frame.")
		case c.role == RoleClient && h.Masked:
			return 0, nil, protocolError("The server sent a masked frame.")
		}

		if h.Opcode.IsControl() {
			p := c.ctrl[:h.Length]
			if _, err := io.ReadFull(c.br, p); err != nil {
				return 0, nil, c.readError(unexpected(err), false)
			}
			if h.Masked {
				Mask(h.MaskKey, 0, p)
			}
			c.hook(Received, h, p)
			if err := c.handleControl(h.Opcode, p); err != nil {
				return 0, nil, err
			}
			continue
		}

		if h.Opcode == OpContinuation {
			if !open {
				return 0, nil, protocolError("A continuation frame arrived without a message to continue.")
			}
		} else {
			if open {
				return 0, nil, protocolError("A new " + h.Opcode.String() + " message started before the fragmented message ended.")
			}
			open, msgOp = true, h.Opcode
			c.text.reset()
		}
		if h.Length > c.maxMsg-int64(len(msg)) {
			return 0, nil, failure(CodeTooBig, CloseMessageTooBig,
				"The message is larger than the limit of "+strconv.FormatInt(c.maxMsg, 10)+" bytes.")
		}
		start := len(msg)
		msg, err = readPayload(c.br, h.Length, msg)
		if err != nil {
			return 0, nil, c.readError(err, false)
		}
		p := msg[start:]
		if h.Masked {
			Mask(h.MaskKey, 0, p)
		}
		c.hook(Received, h, p)
		if msgOp == OpText && !c.text.write(p) {
			return 0, nil, failure(CodeInvalidUTF8, CloseInvalidPayload, "The text message is not valid UTF-8.")
		}
		if h.Fin {
			if msgOp == OpText && !c.text.complete() {
				return 0, nil, failure(CodeInvalidUTF8, CloseInvalidPayload, "The text message ends in the middle of a UTF-8 sequence.")
			}
			if msg == nil {
				msg = []byte{}
			}
			return msgOp, msg, nil
		}
	}
}

// readError maps an error from reading a header or payload. atBoundary is
// true when nothing of the current frame was read yet.
func (c *Conn) readError(err error, atBoundary bool) error {
	if _, ok := errors.AsType[*errs.Error](err); ok {
		return err
	}
	switch {
	case atBoundary && errors.Is(err, io.EOF):
		return errs.Wrap(err, errs.Network, "The WebSocket connection ended without a close frame.").
			WithCode(CodeAbnormalClosure).
			WithDetail(detailCloseCode, strconv.Itoa(int(CloseAbnormalClosure)))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errs.Wrap(err, errs.Network, "The WebSocket connection ended in the middle of a frame.").
			WithCode(CodeUnexpectedEOF).
			WithDetail(detailCloseCode, strconv.Itoa(int(CloseAbnormalClosure)))
	}
	return wrapIO(err, CodeReadFailed, "Could not read from the WebSocket connection.")
}

func (c *Conn) handleControl(op Opcode, p []byte) error {
	switch op {
	case OpPing:
		if !c.autoPong {
			return nil
		}
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		if c.closeSent {
			return nil
		}
		return c.writeFrameLocked(Header{Fin: true, Opcode: OpPong}, p)
	case OpPong:
		return nil
	case OpClose:
		code, reason, err := ParseClosePayload(p)
		if err != nil {
			return err
		}
		ce := &CloseError{Code: code, Reason: reason}
		c.closeOnce.Do(func() {
			c.peerClose = ce
			close(c.closeRecv)
		})
		c.writeMu.Lock()
		if !c.closeSent {
			// Echo the status code (§5.5.1); an empty close gets an empty one.
			var echo []byte
			if code != CloseNoStatus {
				echo, _ = AppendClosePayload(nil, code, "")
			}
			c.closeSent = true
			_ = c.writeFrameLocked(Header{Fin: true, Opcode: OpClose}, echo)
		}
		c.writeMu.Unlock()
		return errs.Wrap(ce, errs.Network, "The peer closed the WebSocket connection.").
			WithCode(CodePeerClosed).
			WithDetail(detailCloseCode, strconv.Itoa(int(code)))
	}
	return nil
}

// sendCloseBestEffort sends a close frame with code unless one was sent.
func (c *Conn) sendCloseBestEffort(code StatusCode) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closeSent {
		return
	}
	c.closeSent = true
	p, _ := AppendClosePayload(nil, code, "")
	_ = c.writeFrameLocked(Header{Fin: true, Opcode: OpClose}, p)
}

func (c *Conn) hook(dir Direction, h Header, p []byte) {
	if c.onFrame != nil {
		h.Length = int64(len(p))
		c.onFrame(dir, Frame{Header: h, Payload: p})
	}
}

// writeFrameLocked writes one frame, masking it for a client. c.writeMu
// must be held.
func (c *Conn) writeFrameLocked(h Header, payload []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	h.Masked = c.role == RoleClient
	if h.Masked {
		if _, err := io.ReadFull(c.rand, h.MaskKey[:]); err != nil {
			return errs.Wrap(err, errs.Internal, "Could not generate a WebSocket mask key.")
		}
	}
	h.Length = int64(len(payload))
	return c.writeRawLocked(Frame{Header: h, Payload: payload})
}

func (c *Conn) writeRawLocked(f Frame) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	buf, err := AppendFrame(c.wbuf[:0], f)
	if err != nil {
		return err
	}
	_, err = c.rw.Write(buf)
	if cap(buf) <= 64<<10 {
		c.wbuf = buf[:0]
	} else {
		c.wbuf = nil
	}
	if err != nil {
		c.writeErr = wrapIO(err, CodeWriteFailed, "Could not write to the WebSocket connection.")
		return c.writeErr
	}
	c.hook(Sent, f.Header, f.Payload)
	return nil
}

var errCloseSent = errs.New(errs.InvalidArgument, "The WebSocket connection is closing; nothing more can be sent.").
	WithCode(CodeCloseSent)

func (c *Conn) writeDataFrame(op Opcode, fin bool, p []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closeSent {
		return errCloseSent
	}
	return c.writeFrameLocked(Header{Fin: fin, Opcode: op}, p)
}

func checkDataOpcode(op Opcode) error {
	if op != OpText && op != OpBinary {
		return invalidArgument("A message must be text or binary, not " + op.String() + ".")
	}
	return nil
}

// WriteMessage sends data as a single-frame text or binary message. Text
// must be valid UTF-8.
func (c *Conn) WriteMessage(op Opcode, data []byte) error {
	if err := checkDataOpcode(op); err != nil {
		return err
	}
	if op == OpText && !utf8.Valid(data) {
		return invalidArgument("The text message is not valid UTF-8.")
	}
	c.dataMu.Lock()
	defer c.dataMu.Unlock()
	return c.writeDataFrame(op, true, data)
}

// NextWriter starts a fragmented message. Every non-empty Write sends one
// frame; Close sends the final frame (with an empty payload) and must be
// called to let other messages be sent. Control frames may be sent while the
// writer is open. For text, the written bytes must form valid UTF-8.
func (c *Conn) NextWriter(op Opcode) (*MessageWriter, error) {
	if err := checkDataOpcode(op); err != nil {
		return nil, err
	}
	c.dataMu.Lock()
	return &MessageWriter{c: c, op: op}, nil
}

// MessageWriter writes one fragmented message. It is not safe for
// concurrent use.
type MessageWriter struct {
	c       *Conn
	op      Opcode
	started bool
	closed  bool
	text    utf8Validator
}

func (w *MessageWriter) nextOp() Opcode {
	if w.started {
		return OpContinuation
	}
	return w.op
}

// Write sends p as one non-final frame.
func (w *MessageWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, invalidArgument("The message writer is closed.")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.op == OpText && !w.text.write(p) {
		return 0, invalidArgument("The text message is not valid UTF-8.")
	}
	if err := w.c.writeDataFrame(w.nextOp(), false, p); err != nil {
		return 0, err
	}
	w.started = true
	return len(p), nil
}

// Close sends the final frame and releases the message lock. Calling it
// again does nothing.
func (w *MessageWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer w.c.dataMu.Unlock()
	if w.op == OpText && !w.text.complete() {
		return invalidArgument("The text message ends in the middle of a UTF-8 sequence.")
	}
	return w.c.writeDataFrame(w.nextOp(), true, nil)
}

func (c *Conn) writeControl(op Opcode, p []byte) error {
	if len(p) > MaxControlPayload {
		return invalidArgument("A " + op.String() + " payload must be at most 125 bytes.")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closeSent {
		return errCloseSent
	}
	return c.writeFrameLocked(Header{Fin: true, Opcode: op}, p)
}

// Ping sends a ping frame with a payload of at most 125 bytes.
func (c *Conn) Ping(p []byte) error { return c.writeControl(OpPing, p) }

// Pong sends a pong frame with a payload of at most 125 bytes.
func (c *Conn) Pong(p []byte) error { return c.writeControl(OpPong, p) }

// WriteFrame writes f exactly as given (masking, RSV bits, opcode, FIN),
// bypassing role and protocol checks, so a test can send invalid frames. It
// only takes the frame lock, so it can interleave with a fragmented message.
// A final close frame marks the close as sent.
func (c *Conn) WriteFrame(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.writeRawLocked(f); err != nil {
		return err
	}
	if f.Opcode == OpClose {
		c.closeSent = true
	}
	return nil
}

// WriteClose sends a close frame with code and reason (CloseNoStatus with
// an empty reason sends an empty close frame) unless one was already sent,
// and returns without waiting for the peer's close frame. It reports
// whether it sent the frame. Use it when another goroutine reads
// (ReadMessage sees the peer's answer); Close also waits.
func (c *Conn) WriteClose(code StatusCode, reason string) (sent bool, err error) {
	payload, err := AppendClosePayload(nil, code, reason)
	if err != nil {
		return false, err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closeSent {
		return false, nil
	}
	c.closeSent = true
	return true, c.writeFrameLocked(Header{Fin: true, Opcode: OpClose}, payload)
}

// Close runs the close handshake: it sends a close frame with code and
// reason (CloseNoStatus with an empty reason sends an empty close frame),
// unless one was already sent, and waits until the peer's close frame
// arrives or ctx ends.
//
// If another goroutine is in ReadMessage, that call sees the peer's close
// frame; otherwise Close reads (and discards) incoming messages itself. When
// ctx ends first, Close sets a past read deadline if the transport supports
// deadlines, so blocked reads return, and returns an error with code
// CodeCloseTimeout. It returns nil once the peer's close frame arrived,
// including when it arrived before Close was called.
func (c *Conn) Close(ctx context.Context, code StatusCode, reason string) error {
	if _, err := c.WriteClose(code, reason); err != nil {
		return err
	}
	if c.PeerClose() != nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		c.readMu.Lock()
		defer c.readMu.Unlock()
		for {
			if c.PeerClose() != nil {
				done <- nil
				return
			}
			if _, _, err := c.readMessage(); err != nil {
				if c.PeerClose() != nil {
					err = nil
				}
				done <- err
				return
			}
		}
	}()
	select {
	case <-c.closeRecv:
		return nil
	case err := <-done:
		return err
	case <-ctx.Done():
		if c.PeerClose() != nil {
			return nil
		}
		if d, ok := c.rw.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = d.SetReadDeadline(time.Unix(1, 0))
		}
		return errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "The peer did not complete the WebSocket close handshake in time.").
			WithCode(CodeCloseTimeout)
	}
}
