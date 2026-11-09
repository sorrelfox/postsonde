package graphql

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/incremental"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportsse"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportws"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Subscriptions (GQL-30..GQL-35): Subscribe runs the GraphQL request's
// operation over the transport its settings pick for the operation's type,
// WebSocket with the graphql-transport-ws protocol (the default) or the
// legacy subscriptions-transport-ws one, or Server-Sent Events, as a
// graphql.subscription operation, and streams every protocol message and
// connection change as SubscriptionEventDTO items; StopSubscription (or
// OpsService.Cancel) stops it. Queries and mutations set to run over
// WebSocket run the same way and end with a result item. See
// docs/graphql-subscriptions.md.

// CodeSubscriptionError: the server answered the subscription with an
// error message (GraphQL errors, e.g. a validation failure; kind
// protocol). Details: count, the number of errors.
const CodeSubscriptionError = "graphql.subscription_error"

// CodeRunsOverHTTP: Subscribe was asked to run an operation whose
// transport is HTTP (kind invalid_argument); it is sent with
// HTTPService.Send instead.
const CodeRunsOverHTTP = "graphql.runs_over_http"

// SubscriptionTarget is what a subscription connects to and sends: the
// transport, the WebSocket options of the handshake (URL, headers,
// dialer, TLS, cookie jar, timeout) and the connection_init payload, or
// the SSE request, and the operation's payload.
type SubscriptionTarget struct {
	// Transport is projectfile.GraphQLTransportWS, GraphQLTransportLegacyWS
	// or GraphQLTransportSSE ("" is ws); GraphQLTransportHTTP is refused.
	Transport string
	// OperationType is the type of the operation that runs ("query",
	// "mutation", "subscription"; "" when it cannot be told). A query or
	// mutation ends with a result item.
	OperationType    string
	WS               wsclient.Options
	ConnectionParams json.RawMessage
	Request          transportws.SubscribeRequest
	// SSE sends the GraphQL over SSE request, for GraphQLTransportSSE.
	SSE *SSETarget
}

// SSETarget is a GraphQL over SSE request ready to send.
type SSETarget struct {
	// Method and URL are the request line and Body the JSON body ("" for
	// GET), secrets masked, for the log.
	Method, URL, Body string
	// Open sends the request and calls fn with the response once its
	// head is in (httpclient.Client.SendStream through the app's HTTP
	// client); cancelling ctx ends it.
	Open func(ctx context.Context, fn httpclient.StreamFunc) (*httpclient.Response, error)
}

// NewSubscription loads GraphQL request id and prepares running it over
// WebSocket or SSE (http.NewGraphQLSubscription). It fails when the
// request cannot be built, with template.unresolved for undefined
// {{variables}} unless allowUnresolved.
type NewSubscription func(ctx context.Context, id string, allowUnresolved bool) (SubscriptionTarget, error)

// Option configures a GraphQLService.
type Option func(*GraphQLService)

// WithSubscriptions lets the service run subscriptions (Subscribe).
// Without it Subscribe fails with CodeUnavailable.
func WithSubscriptions(newSubscription NewSubscription) Option {
	return func(s *GraphQLService) { s.newSubscription = newSubscription }
}

// withSubscriptionTimeouts shortens the connection_init and close
// timeouts, for tests.
func withSubscriptionTimeouts(ack, close time.Duration) Option {
	return func(s *GraphQLService) { s.ackTimeout, s.closeTimeout = ack, close }
}

const (
	// maxSubscriptionItems caps the events in one data event: payloads
	// can be large.
	maxSubscriptionItems = 256
	// subscriptionCloseTimeout bounds the close handshake when a
	// subscription stops or ends.
	subscriptionCloseTimeout = 3 * time.Second
)

// Transport names in SubscriptionEventDTO.Transport.
const (
	TransportNameWS     = transportws.Subprotocol       // graphql-transport-ws
	TransportNameLegacy = transportws.LegacySubprotocol // graphql-ws
	TransportNameSSE    = "sse"
)

// subscriptions tracks the running subscription operations, so
// StopSubscription stops nothing else.
type subscriptions struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (t *subscriptions) add(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ids == nil {
		t.ids = map[string]bool{}
	}
	t.ids[id] = true
}

func (t *subscriptions) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.ids, id)
}

func (t *subscriptions) has(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ids[id]
}

// Subscribe runs GraphQL request in.RequestID over the transport its
// settings pick for the operation that runs (GQL-35) and returns the
// operation's ID at once (GQL-30..GQL-34).
//
// Over WebSocket the endpoint's scheme becomes ws or wss; the request's
// headers, auth, TLS setting, timeout, cookies and environment apply to
// the handshake; the connection params are the connection_init payload
// (GQL-33); the protocol is graphql-transport-ws or, for "ws-legacy",
// subscriptions-transport-ws (GQL-31). Over SSE (GQL-32) the operation is
// POSTed (or sent with GET) with Accept: text/event-stream like a send,
// and the event stream read.
//
// Every protocol message sent and received, the connection opening and
// closing arrive as graphql.subscription:op:data items
// (SubscriptionEventDTO), batched every 50 ms; a query or mutation's
// merged result as a last "result" item; then graphql.subscription:op:done:
// "ok" when the server completed the operation, "error" when the
// connection failed, the server closed it or answered with an error
// (CodeSubscriptionError), "cancelled" after StopSubscription or
// OpsService.Cancel, which stop the operation and close the connection
// first.
//
// It fails at once when the request cannot be built: an unknown ID, a
// request of another kind, undefined {{variables}} (template.unresolved,
// unless in.AllowUnresolved), invalid variables or connection params, an
// endpoint that is not http(s) or ws(s), a transport this version does not
// know, or an operation that runs over HTTP (CodeRunsOverHTTP).
func (s *GraphQLService) Subscribe(ctx context.Context, in SubscribeInputDTO) (operation.StartedDTO, error) {
	if err := interrupted(ctx, "Starting the subscription"); err != nil {
		return operation.StartedDTO{}, err
	}
	if s.newSubscription == nil || s.ops == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "GraphQL subscriptions are not available.").WithCode(CodeUnavailable)
	}
	target, err := s.newSubscription(ctx, in.RequestID, in.AllowUnresolved)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	switch target.Transport {
	case "":
		target.Transport = projectfile.GraphQLTransportWS
	case projectfile.GraphQLTransportWS, projectfile.GraphQLTransportLegacyWS:
	case projectfile.GraphQLTransportSSE:
		if target.SSE == nil {
			return operation.StartedDTO{}, errs.New(errs.Internal, "The GraphQL over SSE request was not built.")
		}
	case projectfile.GraphQLTransportHTTP:
		return operation.StartedDTO{}, errs.New(errs.InvalidArgument, "This operation runs over HTTP: send it instead.").
			WithCode(CodeRunsOverHTTP).
			WithHint("Pick WebSocket or Server-Sent Events for it in the Connection tab to run it here.")
	default:
		return operation.StartedDTO{}, errs.New(errs.InvalidArgument, "Unknown GraphQL transport "+strconv.Quote(target.Transport)+".")
	}
	r := &subscriptionRun{s: s, target: target, ready: make(chan string, 1)}
	started, err := operation.Start(s.ops, operation.Spec{Domain: SubscriptionDomain, Kind: "subscribe", MaxItems: maxSubscriptionItems}, r.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The subscription could not be started.")
	}
	s.subs.add(started.OpID)
	r.ready <- started.OpID
	url := target.WS.URL
	if target.SSE != nil {
		url = target.SSE.URL
	}
	s.logger.Info("graphql subscription started", "opId", started.OpID, "requestId", in.RequestID,
		"transport", target.Transport, "operationType", target.OperationType, "url", redactURL(url))
	return started, nil
}

// StopSubscription stops the running subscription in.OpID: over
// WebSocket complete (stop in the legacy protocol) is sent for it and the
// connection closed with 1000; over SSE the request is cancelled. The
// operation then ends "cancelled". It reports whether the subscription
// was running; an unknown or finished one (or another kind of operation)
// returns false.
func (s *GraphQLService) StopSubscription(ctx context.Context, in StopSubscriptionInputDTO) (bool, error) {
	if err := interrupted(ctx, "Stopping the subscription"); err != nil {
		return false, err
	}
	if s.ops == nil || !s.subs.has(in.OpID) {
		return false, nil
	}
	return s.ops.Cancel(in.OpID), nil
}

// subscriptionRun is one running subscription.
type subscriptionRun struct {
	s      *GraphQLService
	target SubscriptionTarget
	ready  chan string // receives the operation ID once it is tracked

	send func(SubscriptionEventDTO)
	// merged is the result of a query or mutation (GQL-35): its next
	// payloads merged, as incremental delivery over HTTP would be.
	merged incremental.Result
}

// single reports whether the operation is a query or mutation: it ends
// with a result item.
func (r *subscriptionRun) single() bool {
	return r.target.OperationType == "query" || r.target.OperationType == "mutation"
}

func (r *subscriptionRun) now() time.Time {
	if r.target.WS.Now != nil {
		return r.target.WS.Now()
	}
	return time.Now()
}

// addResult merges a result payload of a query or mutation.
func (r *subscriptionRun) addResult(payload json.RawMessage) {
	if r.single() && len(payload) > 0 {
		_, _ = r.merged.Apply(payload)
	}
}

// sendResult sends the merged result of a query or mutation.
func (r *subscriptionRun) sendResult() {
	if !r.single() {
		return
	}
	if text := r.merged.JSON(); text != "" {
		r.send(SubscriptionEventDTO{At: formatAt(r.now()), Kind: EventKindResult, Payload: text, Size: len(text), ErrorCount: r.merged.Errors()})
	}
}

func (r *subscriptionRun) run(ctx context.Context, emit *operation.Emitter[SubscriptionEventDTO]) error {
	id := <-r.ready
	defer r.s.subs.remove(id)
	r.send = func(ev SubscriptionEventDTO) { _ = emit.Send(ev) }
	if r.target.Transport == projectfile.GraphQLTransportSSE {
		return r.runSSE(ctx)
	}
	return r.runWS(ctx)
}

func (r *subscriptionRun) runWS(ctx context.Context) error {
	protocol := transportws.ProtocolTransportWS
	if r.target.Transport == projectfile.GraphQLTransportLegacyWS {
		protocol = transportws.ProtocolLegacy
	}
	o := transportws.Options{
		Protocol:              protocol,
		WS:                    r.target.WS,
		ConnectionParams:      r.target.ConnectionParams,
		ConnectionInitTimeout: r.s.ackTimeout,
		CloseTimeout:          r.s.closeTimeout,
		OnEvent:               func(ev transportws.Event) { r.send(toMessageEventDTO(ev)) },
	}
	o.WS.OnState = func(ch wsclient.StateChange) {
		switch ch.State {
		case wsclient.StateOpen:
			r.send(SubscriptionEventDTO{At: formatAt(ch.At), Kind: EventKindOpen, Subprotocol: protocol.Subprotocol()})
		case wsclient.StateClosed:
			r.send(toClosedEventDTO(ch))
		}
	}
	r.send(SubscriptionEventDTO{
		At: formatAt(r.now()), Kind: EventKindConnecting, URL: redactUserinfo(r.target.WS.URL),
		Transport: protocol.Subprotocol(), OperationType: r.target.OperationType,
	})

	sess, err := transportws.Dial(ctx, o)
	if err != nil {
		return err
	}
	closeSession := func() {
		cctx, cancel := context.WithTimeout(context.Background(), r.closeTimeout())
		defer cancel()
		_ = sess.Close(cctx, wsframe.CloseNormalClosure, "")
	}
	sub, err := sess.Subscribe(ctx, r.target.Request)
	if err != nil {
		closeSession()
		return err
	}
	for {
		select {
		case res, ok := <-sub.C():
			if !ok {
				closeSession()
				return nil
			}
			switch res.Kind {
			case transportws.ResultNext:
				r.addResult(res.Payload)
				continue
			case transportws.ResultComplete:
				r.sendResult()
				closeSession()
				return nil
			case transportws.ResultError:
				closeSession()
				return subscriptionError(res.Errors)
			default: // closed: the session ended first
				return res.Err
			}
		case <-ctx.Done():
			// Stop: complete (or stop) the operation, then close the
			// connection.
			_ = sub.Unsubscribe()
			closeSession()
			return ctx.Err()
		}
	}
}

// runSSE runs the operation over Server-Sent Events (GQL-32): one request,
// its event stream read until complete; stopping cancels the request.
func (r *subscriptionRun) runSSE(ctx context.Context) error {
	t := r.target.SSE
	r.send(SubscriptionEventDTO{
		At: formatAt(r.now()), Kind: EventKindConnecting, URL: redactUserinfo(t.URL), Method: t.Method,
		Transport: TransportNameSSE, OperationType: r.target.OperationType,
	})
	r.send(SubscriptionEventDTO{At: formatAt(r.now()), Kind: EventKindMessage, Direction: "sent", Type: "request", Payload: t.Body, Size: len(t.Body)})

	var out transportsse.Outcome
	_, err := t.Open(ctx, func(head *httpclient.Response, stream io.Reader) error {
		ct := headerValue(head.Headers, "Content-Type")
		r.send(SubscriptionEventDTO{At: formatAt(r.now()), Kind: EventKindOpen, Status: head.StatusCode, ContentType: ct})
		out = transportsse.Read(transportsse.Head{Status: head.StatusCode, ContentType: ct}, stream, transportsse.Options{
			Now: r.target.WS.Now,
			OnEvent: func(ev transportsse.Event) {
				if ev.Type == transportsse.TypeNext || ev.Type == transportsse.TypeResponse {
					r.addResult(ev.Payload)
				}
				r.send(toSSEEventDTO(ev))
			},
		})
		return nil
	})
	closed := SubscriptionEventDTO{Kind: EventKindClosed}
	finish := func(fail error) error {
		closed.At = formatAt(r.now())
		if fail != nil && ctx.Err() == nil {
			dto := apperr.ToDTO(fail)
			closed.Error = &dto
		}
		r.send(closed)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail
	}
	switch {
	case ctx.Err() != nil:
		return finish(nil)
	case err != nil:
		return finish(err)
	}
	switch out.Kind {
	case transportsse.OutcomeComplete:
		r.sendResult()
		return finish(nil)
	case transportsse.OutcomeRejected:
		return finish(subscriptionError(transportws.DecodeErrors(out.Errors)))
	default:
		return finish(out.Err)
	}
}

func headerValue(headers []httpclient.Header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func (r *subscriptionRun) closeTimeout() time.Duration {
	if r.s.closeTimeout > 0 {
		return r.s.closeTimeout
	}
	return subscriptionCloseTimeout
}

// subscriptionError is the error of an operation the server answered
// with an error message.
func subscriptionError(list []transportws.GraphQLError) error {
	msg := "The server rejected the subscription."
	if len(list) > 0 && list[0].Message != "" {
		msg = "The server rejected the subscription: " + strings.TrimSuffix(list[0].Message, ".") + "."
	}
	e := errs.New(errs.Protocol, msg).
		WithCode(CodeSubscriptionError).
		WithDetail("count", strconv.Itoa(len(list)))
	if len(list) > 1 {
		e = e.WithHint("The event log shows all " + strconv.Itoa(len(list)) + " errors.")
	}
	return e
}

// resultErrorCount counts the errors of an ExecutionResult.
func resultErrorCount(payload json.RawMessage) int {
	var r struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(payload, &r) == nil {
		return len(r.Errors)
	}
	return 0
}

// toMessageEventDTO maps a protocol message sent or received.
func toMessageEventDTO(ev transportws.Event) SubscriptionEventDTO {
	out := SubscriptionEventDTO{
		At:        formatAt(ev.At),
		Kind:      EventKindMessage,
		Direction: string(ev.Direction),
		Type:      string(ev.Type),
		ID:        ev.ID,
		Size:      len(ev.Data),
		Note:      string(ev.Note),
	}
	switch {
	case ev.Type == "":
		// A message that could not be parsed: show it as received.
		out.Payload = strings.ToValidUTF8(string(ev.Data), "�")
		out.Binary = ev.Binary
	case len(ev.Payload) > 0:
		out.Payload = string(ev.Payload)
	}
	switch ev.Type {
	case transportws.TypeNext, transportws.TypeData:
		out.ErrorCount = resultErrorCount(ev.Payload)
	case transportws.TypeError:
		out.ErrorCount = len(transportws.DecodeErrors(ev.Payload))
	}
	if ev.Err != nil {
		dto := apperr.ToDTO(ev.Err)
		out.Error = &dto
	}
	return out
}

// toSSEEventDTO maps an event of a GraphQL over SSE stream.
func toSSEEventDTO(ev transportsse.Event) SubscriptionEventDTO {
	out := SubscriptionEventDTO{
		At:        formatAt(ev.At),
		Kind:      EventKindMessage,
		Direction: "received",
		Type:      ev.Type,
		Payload:   ev.Data,
		Size:      ev.Size,
		Note:      string(ev.Note),
	}
	if ev.Payload != nil {
		out.Payload = string(ev.Payload)
		out.ErrorCount = resultErrorCount(ev.Payload)
	}
	if ev.Err != nil {
		dto := apperr.ToDTO(ev.Err)
		out.Error = &dto
	}
	return out
}

// toClosedEventDTO maps the end of the WebSocket connection.
func toClosedEventDTO(ch wsclient.StateChange) SubscriptionEventDTO {
	out := SubscriptionEventDTO{At: formatAt(ch.At), Kind: EventKindClosed}
	if info := ch.Close; info != nil {
		out.Close = &SubscriptionCloseDTO{
			Code:        int(info.Code),
			CodeName:    transportws.CloseCodeName(info.Code),
			Reason:      info.Reason,
			WasClean:    info.WasClean,
			InitiatedBy: string(info.InitiatedBy),
		}
	}
	if ch.Err != nil {
		dto := apperr.ToDTO(ch.Err)
		out.Error = &dto
	}
	return out
}

func formatAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// redactUserinfo drops user:password@ from a URL for the log.
func redactUserinfo(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndexByte(rest[:end], '@'); at >= 0 {
		rest = rest[at+1:]
	}
	return scheme + "://" + rest
}

// redactURL keeps the scheme and host of a URL for the app log: paths
// and queries may hold tokens.
func redactURL(url string) string {
	scheme, rest, ok := strings.Cut(redactUserinfo(url), "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	return scheme + "://" + host
}
