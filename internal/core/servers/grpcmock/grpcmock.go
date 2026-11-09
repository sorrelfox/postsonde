// Package grpcmock is the gRPC mock server type (SSRV-07): it serves the
// methods of .proto files with canned responses written as JSON (the
// protobuf JSON mapping of the output type), a status with trailers, an
// optional delay, and streams of messages for streaming methods. It
// speaks gRPC over HTTP/2 (h2c with prior knowledge in plaintext, h2 over
// TLS, which the Manager adds) and gRPC-Web on the same port, serves
// server reflection (grpc.reflection.v1 and v1alpha) so clients such as
// grpcurl list the services without the files, and logs every call:
// method, metadata, the request and response messages as JSON and the
// status. The files are linked with the repository's own proto linker,
// the messages go through its JSON mapping and codec, and the wire is the
// gRPC transport's framing; HTTP serving is httpbase. See
// docs/servers.md.
package grpcmock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection/reflectwire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the gRPC mock server.
const TypeName = "grpc_mock"

// Options configure the type.
type Options struct {
	// Resolve makes a stored .proto path or import path absolute:
	// relative paths are relative to the open project's root (the service
	// supplies it). It may refuse a path, as the service does one outside
	// the allowed directories (PRV-10). Nil accepts absolute paths only.
	Resolve func(stored string) (string, error)
	// Access, when not nil, returns what the .proto files and their
	// imports are read through (linker.Options.Access): the service's
	// allowlist, read at every link. Nil reads any file.
	Access func() linker.FileAccess
}

// Type is the gRPC mock server type.
type Type struct {
	opts Options
}

// New returns the gRPC mock server type.
func New(opts Options) Type { return Type{opts: opts} }

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "gRPC mock" }

// ALPN offers HTTP/2 over TLS, which gRPC needs, and HTTP/1.1 for
// gRPC-Web clients that do not speak HTTP/2.
func (Type) ALPN() []string { return []string{"h2", "http/1.1"} }

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
	return nil, errs.New(errs.Internal, fmt.Sprintf("A gRPC mock server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start links the .proto files and serves them on ln. Files with errors
// fail the start (grpcmock.proto_failed); canned messages that do not fit
// their type are logged as errors, and those methods end with INTERNAL.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	c := cfg.(Config)
	sc, err := link(c, t.opts)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	stop, cancel := context.WithCancel(context.Background())
	inst := &Instance{base: httpbase.New(sink), opts: t.opts, stop: stop, stopCalls: cancel}
	st := compile(c, sc)
	inst.state.Store(st)
	inst.report(st, false)
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetHTTP2(true)
	p.SetUnencryptedHTTP2(true)
	inst.base.ServeProtocols(ln, http.HandlerFunc(inst.serveHTTP), &p)
	return inst, nil
}

var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of a gRPC mock server. It implements
// servers.Instance and servers.Reconfigurable.
type Instance struct {
	base  *httpbase.Instance
	opts  Options
	state atomic.Pointer[state]
	calls atomic.Uint64

	// stop ends every call in progress when the server stops.
	stop      context.Context
	stopCalls context.CancelFunc
	// running counts the calls in progress, so Stop returns after their
	// last event; it is added to under mu while not stopping.
	mu       sync.Mutex
	stopping bool
	running  sync.WaitGroup

	stopOnce sync.Once
	stopErr  error
}

func (i *Instance) Done() <-chan struct{} { return i.base.Done() }
func (i *Instance) Err() error            { return i.base.Err() }

// Stop ends the calls in progress (UNAVAILABLE, "the server is
// stopping"), then shuts the HTTP server down.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		i.mu.Unlock()
		i.stopCalls()
		i.stopErr = i.base.Stop(ctx)
		i.running.Wait()
	})
	return i.stopErr
}

// Reconfigure links the .proto files again and serves the new canned
// responses; calls in progress finish with the ones they started with.
// When the files now have errors, the error is logged and the server
// keeps the definitions it had, with the new responses.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	old := i.state.Load()
	sc, err := link(c, i.opts)
	if err != nil {
		i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError,
			Summary: errText(err) + " The server keeps serving the definitions it loaded before."})
		sc = old.schema
	}
	st := compile(c, sc)
	i.state.Store(st)
	i.report(st, true)
}

// report logs the methods that cannot be served.
func (i *Instance) report(st *state, changed bool) {
	for _, p := range st.problems {
		i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError, Summary: p})
	}
	if changed {
		return
	}
	if len(st.schema.methods) == 0 {
		i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError,
			Summary: "No .proto files define a service yet: every call answers UNIMPLEMENTED."})
	}
}

func errText(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}

// call is one call being served, for its traffic events.
type call struct {
	inst   *Instance
	r      *http.Request
	s      *stream
	peer   string
	connID uint64
	info   servers.GRPCInfo
	st     *state
	in     int // request messages logged
	out    int // response messages sent
}

func (c *call) grpc() *servers.GRPCInfo {
	g := c.info
	return &g
}

// serveHTTP answers one request: a gRPC or gRPC-Web call, or a plain HTTP
// error for anything else.
func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	if i.stopping {
		i.mu.Unlock()
		i.httpError(w, r, http.StatusServiceUnavailable, "the server is stopping", nil)
		return
	}
	i.running.Add(1)
	i.mu.Unlock()
	defer i.running.Done()
	ct := r.Header.Get("Content-Type")
	web, text := transport.WebContentType(ct)
	grpc := isGRPCContentType(ct)
	switch {
	case r.Method != http.MethodPost:
		i.httpError(w, r, http.StatusMethodNotAllowed, "gRPC calls are POST requests", map[string]string{"Allow": http.MethodPost})
		return
	case !web && !grpc:
		i.httpError(w, r, http.StatusUnsupportedMediaType, "not a gRPC request: Content-Type "+strconv.Quote(ct), nil)
		return
	}
	var timeout time.Duration
	var timeoutErr error
	if v := r.Header.Get("Grpc-Timeout"); v != "" {
		timeout, timeoutErr = transport.ParseTimeout(v)
	}
	webType := ""
	if web {
		webType = ct
		if i := strings.IndexByte(webType, ';'); i >= 0 {
			webType = strings.TrimSpace(webType[:i])
		}
	}
	st := i.state.Load()
	c := &call{inst: i, r: r, peer: r.RemoteAddr, connID: i.base.ConnID(r), st: st}
	c.info = servers.GRPCInfo{Call: i.calls.Add(1), Method: r.URL.Path, Web: web}
	md := st.schema.methods[r.URL.Path]
	if md != nil {
		c.info.Kind = methodKind(md)
	}
	i.base.Emit(servers.Event{
		Peer: c.peer, ConnID: c.connID, Direction: servers.In, Kind: servers.KindRequest,
		Summary: c.requestSummary(),
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Headers: httpbase.RequestHeaders(r)},
		GRPC:    c.grpc(),
	})
	s, err := newStream(w, r, i.stop, timeout, webType, text)
	defer s.cancel(nil)
	c.s = s
	if err != nil {
		c.end(transport.Status{Code: transport.Internal, Message: "reading the gRPC-Web request: " + err.Error()}, nil)
		return
	}
	if timeoutErr != nil {
		c.end(transport.Status{Code: transport.Internal, Message: "malformed grpc-timeout: " + timeoutErr.Error()}, nil)
		return
	}
	if sub := contentSubtype(ct); sub != "" && sub != "proto" {
		c.end(transport.Status{Code: transport.Unimplemented, Message: "the content-subtype " + strconv.Quote(sub) + " is not supported; the mock speaks protobuf"}, nil)
		return
	}
	if isReflection(r.URL.Path) {
		if !st.cfg.Reflection {
			c.end(transport.Status{Code: transport.Unimplemented, Message: "server reflection is turned off on this mock server"}, nil)
			return
		}
		c.info.Kind = "bidi"
		c.serveReflection()
		return
	}
	if md == nil {
		c.end(transport.Status{Code: transport.Unimplemented, Message: "unknown method " + r.URL.Path}, nil)
		return
	}
	a := st.answers[r.URL.Path]
	if a == nil {
		c.end(transport.Status{Code: transport.Unimplemented, Message: "no canned response is set for " + r.URL.Path + " in PostSonde"}, nil)
		return
	}
	c.serve(a)
}

// isGRPCContentType reports whether ct is application/grpc, optionally
// with "+subtype" or ";params".
func isGRPCContentType(ct string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(ct), transport.ContentTypeGRPC)
	return ok && (rest == "" || rest[0] == '+' || rest[0] == ';')
}

// contentSubtype is the "+subtype" of a gRPC content type, or "".
func contentSubtype(ct string) string {
	rest, ok := strings.CutPrefix(strings.ToLower(ct), transport.ContentTypeGRPC+"+")
	if !ok {
		return ""
	}
	if i := strings.IndexByte(rest, ';'); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

func (c *call) requestSummary() string {
	s := "Call " + strconv.FormatUint(c.info.Call, 10) + ": " + c.info.Method
	switch c.info.Kind {
	case "unary":
		s += " (unary)"
	case "server_stream":
		s += " (server streaming)"
	case "client_stream":
		s += " (client streaming)"
	case "bidi":
		s += " (bidirectional streaming)"
	}
	if c.info.Web {
		s += " over gRPC-Web"
	}
	return s
}

// httpError answers a request that is not a gRPC call.
func (i *Instance) httpError(w http.ResponseWriter, r *http.Request, status int, why string, headers map[string]string) {
	peer, connID := i.base.ReadRequest(r, nil)
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	body := "This is a gRPC mock server: " + why + ".\n"
	http.Error(w, strings.TrimSuffix(body, "\n"), status)
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: strconv.Itoa(status) + " " + http.StatusText(status) + " (" + why + ")",
		Content: []byte(body),
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: status, Headers: httpbase.HeaderList(w.Header())},
	})
}

// recv reads and logs the next request message.
func (c *call) recv() ([]byte, error) {
	b, err := c.s.recv()
	if err != nil {
		return nil, err
	}
	c.logMessage(servers.In, c.in, b)
	c.in++
	return b, nil
}

// logMessage logs a message as JSON of its type, or as bytes when it does
// not decode.
func (c *call) logMessage(dir servers.Direction, index int, b []byte) {
	g := c.grpc()
	g.Index = index
	what := "Request"
	md := c.st.schema.methods[c.info.Method]
	if dir == servers.Out {
		what = "Response"
	}
	summary := fmt.Sprintf("%s message %d of call %d", what, index+1, c.info.Call)
	content := b
	if md != nil {
		typ := md.Input
		if dir == servers.Out {
			typ = md.Output
		}
		if typ != nil {
			g.Type = typ.FullName
			if js, err := decodeJSON(c.st.schema.pool, typ, b); err == nil {
				content = js
				summary += " (" + typ.FullName + ", " + plural(len(b), "byte") + ")"
			} else {
				summary += fmt.Sprintf(" (%s, does not decode as %s: %s)", plural(len(b), "byte"), typ.FullName, err)
			}
		}
	}
	c.inst.base.Emit(servers.Event{
		Peer: c.peer, ConnID: c.connID, Direction: dir, Kind: servers.KindMessage,
		Summary: summary, Content: content, GRPC: g,
	})
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// send writes and logs one response message.
func (c *call) send(b []byte) error {
	// Logged before the write, so it comes before what the client does
	// with it.
	c.logMessage(servers.Out, c.out, b)
	c.out++
	return c.s.send(b)
}

// end finishes the call with st and logs it.
func (c *call) end(st transport.Status, trailers transport.Metadata) {
	c.s.finish(st, trailers)
	g := c.grpc()
	g.Code, g.CodeName, g.Message = int(st.Code), st.Code.String(), st.Message
	g.Trailers = metadataHeaders(trailers)
	summary := st.Code.String()
	if st.Message != "" {
		summary += ": " + st.Message
	}
	summary += fmt.Sprintf(" (call %d, %s received, %s sent)", c.info.Call, plural(c.in, "message"), plural(c.out, "message"))
	c.inst.base.Emit(servers.Event{
		Peer: c.peer, ConnID: c.connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: summary,
		HTTP:    &servers.HTTPInfo{Method: c.r.Method, Target: c.r.RequestURI, Proto: c.r.Proto, Status: http.StatusOK, Headers: c.s.sentHeaders()},
		GRPC:    g,
	})
}

// fail ends the call after an error while it was served.
func (c *call) fail(err error) {
	st := statusOf(err)
	// A client gives up at its own deadline, which comes just before the
	// server's timer (grpc-timeout is rounded up): that cancellation is
	// the deadline.
	if st.Code == transport.Canceled && !c.s.deadline.IsZero() && time.Until(c.s.deadline) < deadlineSlack {
		st = transport.Status{Code: transport.DeadlineExceeded, Message: "deadline exceeded"}
	}
	c.end(st, nil)
}

// deadlineSlack is how close to its deadline a cancelled call counts as
// timed out.
const deadlineSlack = 50 * time.Millisecond

// statusOf is the status a call ends with after err.
func statusOf(err error) transport.Status {
	var tooLarge *transport.FrameTooLargeError
	switch {
	case errors.Is(err, errStopping):
		return transport.Status{Code: transport.Unavailable, Message: "the server is stopping"}
	case errors.Is(err, context.DeadlineExceeded):
		return transport.Status{Code: transport.DeadlineExceeded, Message: "deadline exceeded"}
	case errors.Is(err, context.Canceled):
		return transport.Status{Code: transport.Canceled, Message: "the client cancelled the call"}
	case errors.As(err, &tooLarge):
		return transport.Status{Code: transport.ResourceExhausted, Message: err.Error()}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return transport.Status{Code: transport.Internal, Message: "the request ended inside a message"}
	}
	return transport.Status{Code: transport.Internal, Message: err.Error()}
}

// serve answers a call with its canned answer.
func (c *call) serve(a *answer) {
	c.s.header = a.headers
	if a.problem != "" {
		c.end(transport.Status{Code: transport.Internal, Message: "PostSonde mock: " + a.problem}, a.trailers)
		return
	}
	delay := time.Duration(a.cfg.DelayMs) * time.Millisecond
	interval := time.Duration(a.cfg.IntervalMs) * time.Millisecond
	md := a.method
	switch {
	case !md.ClientStreaming:
		// Unary and server streaming: one request message.
		if _, err := c.recv(); err != nil {
			if err == io.EOF {
				c.end(transport.Status{Code: transport.Internal, Message: "the request has no message"}, nil)
				return
			}
			c.fail(err)
			return
		}
		if err := c.drain(); err != nil {
			c.fail(err)
			return
		}
		if err := c.s.wait(delay); err != nil {
			c.fail(err)
			return
		}
		c.reply(a, md.ServerStreaming, interval)
	case !md.ServerStreaming:
		// Client streaming: every message until the half-close.
		if err := c.drain(); err != nil {
			c.fail(err)
			return
		}
		if err := c.s.wait(delay); err != nil {
			c.fail(err)
			return
		}
		c.reply(a, false, interval)
	default:
		c.bidi(a, delay, interval)
	}
}

// drain reads and logs request messages until the client half-closes.
func (c *call) drain() error {
	for {
		if _, err := c.recv(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// reply sends the canned messages (all of them for a stream, else the
// first or an empty message) and the status. A call that fails sends no
// message unless it streams.
func (c *call) reply(a *answer, stream bool, interval time.Duration) {
	if stream {
		for k, b := range a.messages {
			if k > 0 {
				if err := c.s.wait(interval); err != nil {
					c.fail(err)
					return
				}
			}
			if err := c.send(b); err != nil {
				c.fail(err)
				return
			}
		}
	} else if a.status.OK() {
		var b []byte
		if len(a.messages) > 0 {
			b = a.messages[0]
		}
		if err := c.send(b); err != nil {
			c.fail(err)
			return
		}
	}
	c.end(a.status, a.trailers)
}

// bidi answers a bidirectional call as its mode says.
func (c *call) bidi(a *answer, delay, interval time.Duration) {
	// Headers go first, so a client that waits for them before sending
	// is not stuck.
	if err := c.s.sendHeader(); err != nil {
		c.fail(err)
		return
	}
	if a.cfg.Bidi == BidiStream {
		// Read (and log) on another goroutine while the stream is sent.
		read := make(chan error, 1)
		go func() { read <- c.drain() }()
		// abort ends the reading early: closing the body wakes the read.
		abort := func(err error) {
			_ = c.r.Body.Close()
			<-read
			c.fail(err)
		}
		if err := c.s.wait(delay); err != nil {
			abort(err)
			return
		}
		for k, b := range a.messages {
			if k > 0 {
				if err := c.s.wait(interval); err != nil {
					abort(err)
					return
				}
			}
			if err := c.send(b); err != nil {
				abort(err)
				return
			}
		}
		if err := <-read; err != nil {
			c.fail(err)
			return
		}
		c.end(a.status, a.trailers)
		return
	}
	next := 0
	for {
		msg, err := c.recv()
		if err == io.EOF {
			c.end(a.status, a.trailers)
			return
		}
		if err != nil {
			c.fail(err)
			return
		}
		reply := msg
		if a.cfg.Bidi != BidiEcho {
			if len(a.messages) == 0 {
				continue
			}
			reply = a.messages[next%len(a.messages)]
			next++
		}
		if err := c.s.wait(delay); err != nil {
			c.fail(err)
			return
		}
		if err := c.send(reply); err != nil {
			c.fail(err)
			return
		}
	}
}

// serveReflection answers server reflection requests until the client
// half-closes. The requests and answers are logged as text, not as JSON.
func (c *call) serveReflection() {
	idx := c.st.schema.reflection()
	sent := map[string]bool{}
	for {
		b, err := c.s.recv()
		if err == io.EOF {
			c.end(transport.Status{}, nil)
			return
		}
		if err != nil {
			c.fail(err)
			return
		}
		req, err := reflectwire.UnmarshalRequest(b)
		if err != nil {
			c.end(transport.Status{Code: transport.InvalidArgument, Message: err.Error()}, nil)
			return
		}
		g := c.grpc()
		g.Index, g.Type = c.in, "grpc.reflection.ServerReflectionRequest"
		c.in++
		c.inst.base.Emit(servers.Event{
			Peer: c.peer, ConnID: c.connID, Direction: servers.In, Kind: servers.KindMessage,
			Summary: fmt.Sprintf("Reflection request %d of call %d: %s %s", g.Index+1, c.info.Call, req.Kind, req.Name),
			Content: []byte(req.Kind.String() + " " + req.Name), GRPC: g,
		})
		resp, ok := idx.answer(req, sent)
		if !ok {
			c.end(transport.Status{Code: transport.InvalidArgument, Message: "invalid reflection request"}, nil)
			return
		}
		g = c.grpc()
		g.Index, g.Type = c.out, "grpc.reflection.ServerReflectionResponse"
		c.out++
		c.inst.base.Emit(servers.Event{
			Peer: c.peer, ConnID: c.connID, Direction: servers.Out, Kind: servers.KindMessage,
			Summary: fmt.Sprintf("Reflection response %d of call %d: %s", g.Index+1, c.info.Call, reflectionSummary(resp)),
			Content: []byte(reflectionSummary(resp)), GRPC: g,
		})
		if err := c.s.send(reflectwire.MarshalResponse(resp)); err != nil {
			c.fail(err)
			return
		}
	}
}

func reflectionSummary(r reflectwire.Response) string {
	switch r.Kind {
	case reflectwire.ResponseListServices:
		return "services " + strings.Join(r.Services, ", ")
	case reflectwire.ResponseFileDescriptor:
		names := make([]string, 0, len(r.Files))
		for _, b := range r.Files {
			if n, _, err := reflectwire.FileInfo(b); err == nil {
				names = append(names, n)
			}
		}
		return "files " + strings.Join(names, ", ")
	case reflectwire.ResponseAllExtensionNumbers:
		return fmt.Sprintf("%d extension numbers of %s", len(r.ExtensionNumbers), r.BaseTypeName)
	case reflectwire.ResponseError:
		return fmt.Sprintf("error %s: %s", transport.Code(r.ErrorCode), r.ErrorMessage)
	}
	return r.Kind.String()
}
