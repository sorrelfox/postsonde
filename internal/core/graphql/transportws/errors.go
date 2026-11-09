package transportws

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Error codes. WebSocket errors (handshake, transport, close timeout) keep
// their wsclient.* and wsframe.* codes. See docs/graphql-subscriptions.md.
const (
	// Arguments are not valid; nothing was sent.
	CodeInvalidArgument = "transportws.invalid_argument"
	// The server accepted the upgrade without selecting graphql-transport-ws.
	CodeSubprotocolNotSelected = "transportws.subprotocol_not_selected"
	// The server sent a message that breaks the protocol; closed with 4400.
	CodeInvalidMessage = "transportws.invalid_message"
	// No connection_ack within ConnectionInitTimeout; closed with 4408.
	CodeAckTimeout = "transportws.ack_timeout"
	// The server closed the connection (details code and reason).
	CodeClosedByServer = "transportws.closed_by_server"
	// The session was closed with Session.Close, or is closed.
	CodeSessionClosed = "transportws.session_closed"
	// Subscribe's context ended while waiting for connection_ack.
	CodeCancelled = "transportws.cancelled"
	// The legacy protocol's server answered connection_init with
	// connection_error (kind permission_denied).
	CodeConnectionError = "transportws.connection_error"
)

func invalidArgument(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidArgument)
}

func invalidMessage(p Protocol, problem string) *errs.Error {
	return errs.New(errs.Protocol, "The server sent an invalid "+string(p)+" message: "+problem+".").
		WithCode(CodeInvalidMessage).
		WithHint("PostSonde closed the connection with 4400. Check the server's GraphQL over WebSocket implementation.")
}

func subprotocolNotSelected(p Protocol, got string) *errs.Error {
	want := p.Subprotocol()
	msg := "The server did not select the " + want + " subprotocol."
	if got != "" {
		msg = "The server selected the subprotocol " + strconv.Quote(got) + " instead of " + want + "."
	}
	hint := "The server may only support the legacy subscriptions-transport-ws protocol (graphql-ws): pick it in the Connection tab, or check that this URL is a GraphQL WebSocket endpoint."
	if p.legacy() {
		hint = "The server may only support the newer graphql-transport-ws protocol: pick it in the Connection tab, or check that this URL is a GraphQL WebSocket endpoint."
	}
	return errs.New(errs.Protocol, msg).
		WithCode(CodeSubprotocolNotSelected).
		WithHint(hint)
}

// connectionError is the legacy protocol's connection_error: the server
// refused connection_init.
func connectionError(payload []byte) *errs.Error {
	msg := "The server refused the connection (connection_error)."
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &e) == nil && e.Message != "" {
		msg = "The server refused the connection: " + strings.TrimSuffix(e.Message, ".") + "."
	}
	return errs.New(errs.PermissionDenied, msg).
		WithCode(CodeConnectionError).
		WithHint("Check the connection params, e.g. an auth token.")
}

func pingUnsupported() *errs.Error {
	return errs.New(errs.Unsupported, "The subscriptions-transport-ws protocol has no ping message.").WithCode(CodeInvalidArgument)
}

func ackTimeout() *errs.Error {
	return errs.New(errs.Timeout, "The server did not acknowledge the connection in time.").
		WithCode(CodeAckTimeout).
		WithHint("PostSonde closed the connection with 4408. Check the connection params, or raise the connection init timeout.")
}

func sessionClosed() *errs.Error {
	return errs.New(errs.Cancelled, "The GraphQL WebSocket session was closed.").WithCode(CodeSessionClosed)
}

func cancelled(err error) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), "Stopped waiting for the server to acknowledge the connection.").
		WithCode(CodeCancelled)
}

// closedByServer describes a close started by the server.
func closedByServer(code wsframe.StatusCode, reason string) *errs.Error {
	kind := errs.Network
	hint := ""
	switch code {
	case CloseUnauthorized:
		kind = errs.PermissionDenied
		hint = "The server received a message before accepting the connection. Check the connection params."
	case CloseForbidden:
		kind = errs.PermissionDenied
		hint = "The server rejected the connection. Check the connection params, e.g. an auth token."
	case CloseBadRequest, CloseSubprotocolNotAcceptable, CloseSubscriberAlreadyExists,
		CloseTooManyInitRequests, wsframe.CloseProtocolError, wsframe.CloseUnsupportedData,
		wsframe.CloseInvalidPayload:
		kind = errs.Protocol
	case CloseConnectionInitTimeout:
		kind = errs.Timeout
	}
	msg := "The server closed the GraphQL WebSocket connection with " + closeCodeText(code)
	if reason != "" {
		msg += ": " + reason
	}
	e := errs.New(kind, msg+".").
		WithCode(CodeClosedByServer).
		WithDetail("code", strconv.Itoa(int(code))).
		WithDetail("reason", reason)
	if hint != "" {
		e = e.WithHint(hint)
	}
	return e
}
