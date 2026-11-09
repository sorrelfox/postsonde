package grpc

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// call is one running call.
//
// Items reach the UI in the order things happened on the wire (GRPC-22).
// A message is sent with the send lock held; while its bytes are being
// written, items from the receiving side (a reply the server sent in
// answer) are held back and emitted after the "sent" item. The lock is
// never held while waiting to receive, so a send blocked by HTTP/2 flow
// control does not stop the call from reading.
type call struct {
	stream          *transport.Stream
	pool            *desc.Pool
	method          *desc.Method
	path            string
	md              transport.Metadata
	masker          *httpclient.SecretMasker // the auth's secrets in md (AUTH-31); nil without any
	req             []byte                   // unary and server streaming: the one message
	reqJSON         string
	deadlineMs      int64
	transport       string // "grpc", "grpc-web" or "grpc-web-text"
	compression     string // "identity" or "gzip"
	single          bool   // one response message expected (unary, client streaming)
	clientStreaming bool   // messages are sent with SendMessage (client streaming, bidi)

	// opened is closed once the request item is out and emit is set.
	opened chan struct{}
	emit   *operation.Emitter[CallEventDTO]
	// onEnd is called once when the call has ended.
	onEnd func(*call)

	// requestID and checks check the request's assertions against log,
	// the response messages, when the call ends (TEST-08).
	requestID string
	checks    streamcheck.Loader
	log       *streamcheck.Log

	// sendMu serializes SendMessage and CloseSend, and the end of the call.
	sendMu     sync.Mutex
	halfClosed bool
	ended      bool
	sent       int

	// evMu orders items: sending is set while a message is being written,
	// and held keeps the items that arrived meanwhile.
	evMu    sync.Mutex
	sending bool
	held    []CallEventDTO
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// event stamps ev with the time and emits it, or holds it back while a
// message is being sent. Stamping here keeps the times in the order of the
// items.
func (c *call) event(ev CallEventDTO) error {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	ev.At = now()
	if c.sending {
		c.held = append(c.held, ev)
		return nil
	}
	return c.emit.Send(ev)
}

// run is the operation: send the request (or, for a stream from the
// client, open it), then report the headers, each response message and
// the end. Its error is the call's.
func (c *call) run(ctx context.Context, emit *operation.Emitter[CallEventDTO]) error {
	start := time.Now()
	stop := context.AfterFunc(ctx, c.stream.Close)
	defer stop()
	defer func() {
		c.stream.Close()
		c.sendMu.Lock()
		c.ended = true
		c.sendMu.Unlock()
		if c.onEnd != nil {
			c.onEnd(c)
		}
	}()

	c.emit = emit
	err := emit.Send(CallEventDTO{
		Type: CallRequest, At: now(), Method: c.path, Metadata: maskMetadata(toMetadataDTOs(c.md), c.masker),
		JSON: c.reqJSON, Size: len(c.req), DeadlineMs: c.deadlineMs, ClientStreaming: c.clientStreaming,
		Transport: c.transport, Compression: c.compression,
	})
	close(c.opened)
	if err != nil {
		return err
	}
	if !c.clientStreaming {
		if err := c.stream.Send(c.req); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		c.stream.CloseSend()
	}

	header := http.Header{}
	if h, err := c.stream.Header(); err == nil && len(h.Get(transport.HeaderStatus)) == 0 {
		metadataHeader(header, h)
		if err := c.event(CallEventDTO{Type: CallHeaders, Metadata: toMetadataDTOs(h)}); err != nil {
			return err
		}
	}

	n := 0
	var last []byte
	var callErr error
	for {
		b, err := c.stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			callErr = err
			break
		}
		ev := c.message(n, b)
		body := []byte(ev.JSON)
		if ev.DecodeError != "" {
			body = b
		}
		c.log.Add(body, nil, time.Now())
		last = body
		if err := c.event(ev); err != nil {
			return err
		}
		n++
	}
	if callErr == nil && c.single && n != 1 {
		callErr = cardinalityErr(n)
	}
	// The stream has ended, so a send in progress returns: wait for it,
	// so its item comes before the end, and refuse later ones.
	c.sendMu.Lock()
	c.ended = true
	sent := c.sent
	if !c.clientStreaming {
		sent = 1
	}
	c.sendMu.Unlock()
	status := transport.StatusFromError(callErr)
	elapsed := time.Since(start)
	// The whole call, for the assertions without a message scope: the
	// status code, the headers and trailers, the last message, the time.
	metadataHeader(header, c.stream.Trailer())
	header.Set("grpc-status", strconv.Itoa(int(status.Code)))
	if status.Message != "" {
		header.Set("grpc-message", status.Message)
	}
	c.log.SetResponse(assert.Response{Status: int(status.Code), Header: header, Body: last, Time: elapsed})
	if err := c.event(CallEventDTO{
		Type: CallEnd, Metadata: toMetadataDTOs(c.stream.Trailer()),
		Status: toStatusDTO(status, c.pool), Messages: n, Sent: sent,
		ElapsedMs: elapsed.Milliseconds(),
		Tests:     runChecks(ctx, c.checks, c.requestID, c.log, c.masker.MaskText),
	}); err != nil {
		return err
	}
	return callErr
}

// message decodes response message n for the UI; a message that does not
// decode is kept as bytes.
func (c *call) message(n int, b []byte) CallEventDTO {
	ev := CallEventDTO{Type: CallMessage, Index: n, Size: len(b)}
	msg := dynamic.New(c.method.Output)
	err := (dynamic.UnmarshalOptions{Resolver: c.pool}).Unmarshal(b, msg)
	if err == nil {
		var js []byte
		js, err = (protojson.MarshalOptions{Indent: "  ", Resolver: c.pool, AllowPartial: true}).Marshal(msg)
		ev.JSON = string(js)
	}
	if err != nil {
		ev.JSON = ""
		ev.DecodeError = "The message is not a valid " + c.method.Output.FullName + ": " + err.Error()
		ev.Raw = base64.StdEncoding.EncodeToString(b)
	}
	return ev
}

// send writes one message of a stream from the client and emits its
// "sent" item, before any item received while it was being written.
func (c *call) send(b []byte, js string) (SentMessageDTO, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.ended {
		return SentMessageDTO{}, notRunning()
	}
	if c.halfClosed {
		return SentMessageDTO{}, errs.New(errs.InvalidArgument, "The request stream is already ended; no more messages can be sent.").
			WithCode(CodeSendClosed)
	}
	at := now()
	c.evMu.Lock()
	c.sending = true
	c.evMu.Unlock()

	err := c.stream.Send(b)

	c.evMu.Lock()
	defer c.evMu.Unlock()
	c.sending = false
	held := c.held
	c.held = nil
	out := SentMessageDTO{Index: c.sent, Size: len(b)}
	var emitErr error
	if err == nil {
		emitErr = c.emit.Send(CallEventDTO{Type: CallSent, At: at, JSON: js, Size: len(b), Index: c.sent})
		c.sent++
	}
	if len(held) > 0 {
		if e := c.emit.Send(held...); emitErr == nil {
			emitErr = e
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		return SentMessageDTO{}, notRunning()
	case err != nil:
		return SentMessageDTO{}, err
	case emitErr != nil:
		// The operation finished meanwhile: the message went out, but the
		// call is over.
		return SentMessageDTO{}, notRunning()
	}
	return out, nil
}

// closeSend half-closes a stream from the client and emits its
// "half-close" item. Closing twice is not an error.
func (c *call) closeSend() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.ended {
		return notRunning()
	}
	if c.halfClosed {
		return nil
	}
	c.halfClosed = true
	if err := c.stream.CloseSend(); err != nil {
		return notRunning()
	}
	if err := c.event(CallEventDTO{Type: CallHalfClose, Index: c.sent}); err != nil {
		return notRunning()
	}
	return nil
}

func notRunning() *errs.Error {
	return errs.New(errs.InvalidArgument, "The call is not running.").WithCode(CodeCallNotRunning).
		WithHint("Start the call again to send messages.")
}

// remember registers a running call that SendMessage and CloseSend can
// reach, unless it has already ended.
func (s *GRPCService) remember(opID string, c *call) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	c.sendMu.Lock()
	ended := c.ended
	c.sendMu.Unlock()
	if !ended {
		s.calls[opID] = c
		return
	}
	s.ended.Put(opID, endedCall{requestID: c.requestID, log: c.log, mask: c.masker.MaskText})
}

// forget drops an ended call, keeping its messages for CheckAssertions.
func (s *GRPCService) forget(c *call) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	for id, x := range s.calls {
		if x == c {
			delete(s.calls, id)
			s.ended.Put(id, endedCall{requestID: c.requestID, log: c.log, mask: c.masker.MaskText})
		}
	}
}

// running returns the running call opID for SendMessage or CloseSend.
func (s *GRPCService) running(opID string) (*call, error) {
	s.callsMu.Lock()
	c := s.calls[strings.TrimSpace(opID)]
	s.callsMu.Unlock()
	if c == nil {
		return nil, notRunning().WithDetail("opId", opID)
	}
	return c, nil
}

// waitOpened waits until the call's request item is out, so the items of
// SendMessage and CloseSend come after it.
func waitOpened(ctx context.Context, c *call) error {
	select {
	case <-c.opened:
		return nil
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Sending was interrupted.")
	}
}

// SendMessage sends one message on the request stream of a running
// client-streaming or bidi call (GRPC-13, GRPC-14). The message is protobuf
// JSON of the method's input type (blank is {}). It returns once the
// transport has taken the bytes (HTTP/2 flow control applies), after the
// "sent" item is queued for the UI. It fails for a call that is not
// running (grpc.call_not_running), a unary or server-streaming call, after
// CloseSend (grpc.send_closed), and for a message that does not fit the
// type (grpc.invalid_message, nothing sent).
func (s *GRPCService) SendMessage(ctx context.Context, in SendMessageInputDTO) (SentMessageDTO, error) {
	c, err := s.running(in.OpID)
	if err != nil {
		return SentMessageDTO{}, err
	}
	if !c.clientStreaming {
		return SentMessageDTO{}, notClientStreaming()
	}
	if err := checkMessageVariables(in.Message); err != nil {
		return SentMessageDTO{}, err
	}
	b, js, err := encodeRequest(c.pool, c.method, in.Message)
	if err != nil {
		return SentMessageDTO{}, err
	}
	if err := waitOpened(ctx, c); err != nil {
		return SentMessageDTO{}, err
	}
	out, err := c.send(b, js)
	if err == nil {
		s.logger.Debug("grpc message sent", "opId", in.OpID, "index", out.Index, "size", out.Size)
	}
	return out, err
}

// CloseSend ends the request stream of a running client-streaming or bidi
// call (half-close): the server learns that no more messages come. The
// call keeps running until the server ends it; a client-streaming method
// answers now. Closing again is not an error.
func (s *GRPCService) CloseSend(ctx context.Context, in CallRefDTO) error {
	c, err := s.running(in.OpID)
	if err != nil {
		return err
	}
	if !c.clientStreaming {
		return notClientStreaming()
	}
	if err := waitOpened(ctx, c); err != nil {
		return err
	}
	return c.closeSend()
}

func notClientStreaming() error {
	return errs.New(errs.InvalidArgument, "The call does not stream from the client; its one message was sent with the request.").
		WithCode(CodeNotClientStreaming)
}

// cardinalityErr is the error of an OK unary or client-streaming call that
// did not get exactly one message: INTERNAL, as grpc-go and
// transport.Client.Unary report it.
func cardinalityErr(n int) error {
	msg, text := "The server sent no response message.", "cardinality violation: no response message for a unary call"
	if n > 1 {
		msg, text = "The server sent more than one response message.", "cardinality violation: several response messages for a unary call"
	}
	st := transport.Status{Code: transport.Internal, Message: text}
	return errs.Wrap(&transport.StatusError{Status: st}, errs.Protocol, msg).
		WithCode(transport.CodeCardinality).
		WithDetail("grpc_status", st.Code.String()).
		WithDetail("grpc_code", strconv.FormatUint(uint64(st.Code), 10))
}
