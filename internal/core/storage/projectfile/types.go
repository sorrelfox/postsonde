package projectfile

import (
	"bytes"
	"encoding/json"

	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// Unknown holds the members of a JSON object that this version has no
// field for, as raw JSON. Every object type in a project file keeps them
// and writes them back after its known members, sorted by key, so members
// added by a newer PostSonde or by hand survive a load/save cycle.
type Unknown map[string]json.RawMessage

// Workspace is the workspace marker, postsonde.json at the project root.
// The root directory itself is the top of the request tree: its folders
// are the collections.
type Workspace struct {
	Head
	// ID identifies the workspace. Local-only data about it (open tabs,
	// the active environment, secrets, history) is kept outside the
	// project, keyed by this ID.
	ID string `json:"id"`
	// Name is the display name.
	Name string `json:"name"`
	// Description is optional Markdown notes.
	Description string `json:"description,omitempty"`
	// Order lists the entry names of the root's children (request file
	// names and folder directory names) in manual order.
	Order []string `json:"order,omitempty"`
	// Variables are the global variables, available to every request.
	Variables []Variable `json:"variables,omitempty"`
	Unknown   Unknown    `json:"-"`
}

// Folder is a folder's own settings, _folder.json in its directory. The
// directory holds the folder's requests and subfolders.
type Folder struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Order lists the entry names of the folder's children in manual
	// order, as Workspace.Order does.
	Order []string `json:"order,omitempty"`
	// Variables, Headers and Auth are inherited by everything inside the
	// folder.
	Variables []Variable `json:"variables,omitempty"`
	Headers   []KeyValue `json:"headers,omitempty"`
	Auth      Auth       `json:"auth"`
	// GRPC holds the proto setup the folder's gRPC requests can inherit.
	GRPC FolderGRPC `json:"grpc,omitzero"`
	// Server is the folder's local server (SRV-04), or nil. It is only a
	// definition: opening the project never starts it.
	Server  *FolderServer `json:"server,omitempty"`
	Unknown Unknown       `json:"-"`
}

// HTTPRequest is an HTTP request file (kind "http").
type HTTPRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Method is the method token as typed: GET, POST, QUERY, a custom one.
	Method string `json:"method"`
	// URL is the URL as shown in the editor, with {{variables}} unresolved
	// and the enabled query parameters in its query string.
	URL string `json:"url"`
	// Params is the query-parameter table, including disabled rows.
	Params []KeyValue `json:"params,omitempty"`
	// PathParams are the values of the :name and {name} placeholders in
	// URL.
	PathParams []KeyValue `json:"pathParams,omitempty"`
	Headers    []KeyValue `json:"headers,omitempty"`
	Body       Body       `json:"body"`
	// BodyExamples are the request's named bodies (HTTP-17), in order;
	// the one marked Active is Body and has no body of its own. Empty:
	// Body is the only, unnamed body.
	BodyExamples []BodyExample   `json:"bodyExamples,omitempty"`
	Auth         Auth            `json:"auth"`
	Settings     RequestSettings `json:"settings,omitzero"`
	// Assertions are checked against every response (TEST-01), in order.
	Assertions []Assertion `json:"assertions,omitempty"`
	// Extractions set variables from every response (TEST-05), in order.
	// Empty: none.
	Extractions []Extraction `json:"extractions,omitempty"`
	// ResponseExamples are responses saved with the request (HTTP-38), in
	// order. Empty: none.
	ResponseExamples []ResponseExample `json:"responseExamples,omitempty"`
	Unknown          Unknown           `json:"-"`
}

// GraphQLRequest is a GraphQL request file (kind "graphql").
type GraphQLRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Method is empty for the default, POST with a JSON body, or "GET".
	Method string `json:"method,omitempty"`
	// URL is the endpoint.
	URL string `json:"url"`
	// Query is the GraphQL document as typed.
	Query string `json:"query"`
	// Variables is the variables editor's JSON text as typed. It is kept as
	// text, not parsed, so a half-typed value auto-saves as it is.
	Variables string `json:"variables,omitempty"`
	// OperationName picks the operation to run when Query has several.
	OperationName string `json:"operationName,omitempty"`
	// ConnectionParams is the connection params editor's JSON text as
	// typed (GQL-33): the connection_init payload when an operation runs
	// over WebSocket. Text, like Variables.
	ConnectionParams string `json:"connectionParams,omitempty"`
	// SchemaFile is a local SDL file the editor takes the schema from
	// instead of introspection (GQL-11): relative to the project root with
	// "/" when inside it, else absolute. Empty: introspection.
	SchemaFile string `json:"schemaFile,omitempty"`
	// Extensions is the extensions editor's JSON text as typed (GQL-06):
	// the request's "extensions" object (a persisted query's hash, say).
	// Text, like Variables.
	Extensions string `json:"extensions,omitempty"`
	// Files are the files uploaded with the GraphQL multipart request
	// spec (GQL-07), stored as multipart parts are: Name is the object
	// path the file binds to ("variables.input.files.0"), File the file
	// (relative to the project root with "/" when inside it, else
	// absolute), ContentType, Description and Disabled as for a part;
	// Value is unused. A request with enabled files is sent as
	// multipart/form-data.
	Files    []Part          `json:"files,omitempty"`
	Headers  []KeyValue      `json:"headers,omitempty"`
	Auth     Auth            `json:"auth"`
	Settings RequestSettings `json:"settings,omitzero"`
	// Assertions are checked against every response sent over HTTP
	// (TEST-01), in order; not subscriptions.
	Assertions []Assertion `json:"assertions,omitempty"`
	// Extractions set variables from every response sent over HTTP
	// (TEST-05), as for HTTPRequest; not from subscription events.
	Extractions []Extraction `json:"extractions,omitempty"`
	// ResponseExamples are responses saved with the request (HTTP-38), as
	// for HTTPRequest.
	ResponseExamples []ResponseExample `json:"responseExamples,omitempty"`
	Unknown          Unknown           `json:"-"`
}

// Assertion is a check on the response of an HTTP or GraphQL request
// (TEST-01, TEST-02, TEST-03): Target (with Expr) · Op · Expected, as
// internal/core/assert evaluates it. Values are stored as typed:
// {{variables}} in Expr and Expected are rendered when the response is
// checked. A target or operator this version does not know is kept.
type Assertion struct {
	// Target is "status", "header", "cookie", "jsonpath", "xpath",
	// "regex", "body", "time" or "size".
	Target string `json:"target"`
	// Expr is the header or cookie name, the JSONPath, the XPath or the
	// regular expression; left out for the other targets.
	Expr string `json:"expr,omitempty"`
	// Op is "equals", "not_equals", "contains", "not_contains", "exists",
	// "not_exists", "is_empty", "gt", "lt", "gte", "lte", "matches",
	// "is_type" or "matches_schema".
	Op string `json:"op"`
	// Expected is the value to compare with, as typed; for
	// "matches_schema", a JSON Schema or the path of a schema file.
	Expected string `json:"expected,omitempty"`
	// Messages is set on the assertions of a stream (websocket,
	// socketio, grpc and sse requests, TEST-08): which received messages
	// the target is read from, "any", "every", "none", "first", "last" or
	// "nth"; left out to check the stream as a whole. One this version
	// does not know is kept.
	Messages string `json:"messages,omitempty"`
	// Index is the message "nth" checks, counted from 1; left out for 0.
	Index int `json:"index,omitempty"`
	// Disabled assertions are kept but not checked.
	Disabled bool    `json:"disabled,omitempty"`
	Unknown  Unknown `json:"-"`
}

// Extraction is a rule that sets a variable from every response of an
// http or graphql request (TEST-05): the value a source finds, in the
// runtime scope or the run's. Only the rule is stored, never a value.
type Extraction struct {
	// Source is "jsonpath", "xpath", "regex", "header", "cookie" or
	// "status".
	Source string `json:"source"`
	// Expr is the JSONPath, the XPath, the regular expression, or the
	// header or cookie name; left out for "status".
	Expr string `json:"expr,omitempty"`
	// Variable is the name of the variable set.
	Variable string `json:"variable"`
	// Scope is "" (left out) for the runtime variables, or "run" for the
	// variables of a folder run only.
	Scope string `json:"scope,omitempty"`
	// Secret masks the value wherever it is shown or recorded.
	Secret bool `json:"secret,omitempty"`
	// Persist keeps a runtime variable after the project is closed.
	Persist bool `json:"persist,omitempty"`
	// Disabled rules are kept but not applied.
	Disabled bool    `json:"disabled,omitempty"`
	Unknown  Unknown `json:"-"`
}

// Send modes of a TCP request, for TCPRequest.SendMode.
const (
	SendText = "text"
	SendHex  = "hex"
)

// Line endings a TCP request appends to text messages, for
// TCPRequest.LineEnding.
const (
	LineEndingNone = "none"
	LineEndingLF   = "lf"
	LineEndingCRLF = "crlf"
)

// TCPRequest is a raw TCP connection file (kind "tcp", TCP-01): where to
// connect and the message being composed.
type TCPRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Address is "host:port" as typed, with {{variables}} unresolved.
	Address string `json:"address"`
	// SendMode is how Message is turned into bytes: "text" (UTF-8, the
	// default, written as a missing member) or "hex". A mode this version
	// does not know is kept.
	SendMode string `json:"sendMode,omitempty"`
	// LineEnding is appended to text messages: "none" (the default, written
	// as a missing member), "lf" or "crlf".
	LineEnding string `json:"lineEnding,omitempty"`
	// Message is the composer's draft as typed.
	Message string `json:"message,omitempty"`
	// Framing is how received bytes are split into messages and sent
	// messages are framed (TCP-03); left out for no framing.
	Framing  TCPFraming  `json:"framing,omitzero"`
	Settings TCPSettings `json:"settings,omitzero"`
	Unknown  Unknown     `json:"-"`
}

// Framing modes of a TCP request, for TCPFraming.Mode.
const (
	FramingNone    = "none"
	FramingNewline = "newline"
	FramingLength  = "length"
)

// TCPFraming is a TCP request's message framing (TCP-03). Empty members
// are the defaults; values this version does not know are kept, and the
// connection refuses them.
type TCPFraming struct {
	// Mode is "none" (the default, written as a missing member), "newline"
	// or "length".
	Mode string `json:"mode,omitempty"`
	// Delimiter is what newline framing appends to sent messages: "lf"
	// (the default) or "crlf". Received lines may end in either.
	Delimiter string `json:"delimiter,omitempty"`
	// PrefixBytes is the size of the length prefix: 1, 2, 4 (the default)
	// or 8.
	PrefixBytes int `json:"prefixBytes,omitempty"`
	// ByteOrder of the length prefix: "big" (the default) or "little".
	ByteOrder string `json:"byteOrder,omitempty"`
	// MaxMessageBytes limits a received message; missing means 1 MiB.
	MaxMessageBytes *int64  `json:"maxMessageBytes,omitempty"`
	Unknown         Unknown `json:"-"`
}

// TCPSettings override the app's settings for one TCP request. A nil
// member means "use the app setting"; when all are unset the member is
// left out of the file.
type TCPSettings struct {
	// ConnectTimeoutMs bounds DNS, connect and the TLS handshake in
	// milliseconds; 0 means no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs,omitempty"`
	// TLS connects with TLS (TCP-02) instead of plain TCP.
	TLS bool `json:"tls,omitempty"`
	// VerifyTLS false turns off certificate verification with TLS.
	VerifyTLS *bool   `json:"verifyTls,omitempty"`
	Unknown   Unknown `json:"-"`
}

// Message formats of a WebSocket request's composer, for
// WebSocketRequest.MessageFormat and WebSocketMessage.Format. Text and
// JSON are sent as text messages; hex and Base64 spell the bytes of a
// binary message (WS-04).
const (
	MessageText   = "text"
	MessageJSON   = "json"
	MessageHex    = "hex"
	MessageBase64 = "base64"
)

// WebSocketRequest is a WebSocket connection file (kind "websocket",
// WS-01…WS-03): the handshake (URL, query rows, headers, subprotocols) and
// the message being composed.
type WebSocketRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// URL is the ws:// or wss:// URL as shown in the editor, with
	// {{variables}} unresolved and the enabled query parameters in its
	// query string.
	URL string `json:"url"`
	// Params is the query-parameter table, including disabled rows.
	Params  []KeyValue `json:"params,omitempty"`
	Headers []KeyValue `json:"headers,omitempty"`
	// Subprotocols are offered in Sec-WebSocket-Protocol, in order.
	Subprotocols []string `json:"subprotocols,omitempty"`
	// Message is the composer's draft as typed.
	Message string `json:"message,omitempty"`
	// MessageFormat is "text" (the default, written as a missing member),
	// "json" (validated and beautified in the editor; sent as a text
	// message), "hex" or "base64" (Message spells the bytes of a binary
	// message). A format this version does not know is kept.
	MessageFormat string `json:"messageFormat,omitempty"`
	// SavedMessages are named messages kept with the connection and sent
	// with one click (WS-07), in the order the editor lists them. Left out
	// when there are none.
	SavedMessages []WebSocketMessage `json:"savedMessages,omitempty"`
	// Auth is sent with the upgrade request (AUTH-31). A file without the
	// member, as written before PostSonde applied auth to handshakes,
	// reads as "none" so it connects as it did; "none" is written as a
	// missing member.
	Auth     Auth              `json:"auth,omitzero"`
	Settings WebSocketSettings `json:"settings,omitzero"`
	// Assertions are checked against the received messages when the
	// connection closes, and on demand (TEST-08). Left out when none.
	Assertions []Assertion `json:"assertions,omitempty"`
	Unknown    Unknown     `json:"-"`
}

// WebSocketMessage is a saved message of a WebSocket request (WS-07).
type WebSocketMessage struct {
	// Name is the label shown in the list; names need not be unique.
	Name string `json:"name"`
	// Text is the message as typed, with {{variables}} unresolved; they
	// are resolved when a text or JSON message is sent (WS-11). For hex
	// and Base64 it is the bytes as typed, sent as written.
	Text string `json:"text"`
	// Format is "text" (the default, written as a missing member), "json",
	// "hex" or "base64", as MessageFormat. A format this version does not
	// know is kept.
	Format  string  `json:"format,omitempty"`
	Unknown Unknown `json:"-"`
}

// WebSocketSettings override the app's settings for one WebSocket request.
// A nil member means "use the app setting"; when all are nil the member is
// left out of the file.
type WebSocketSettings struct {
	// ConnectTimeoutMs bounds DNS, connect, TLS and the upgrade handshake
	// in milliseconds; 0 means no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs,omitempty"`
	// VerifyTLS false turns off certificate verification for wss URLs.
	VerifyTLS *bool `json:"verifyTls,omitempty"`
	// SendCookies false leaves the workspace cookie jar out of the
	// handshake: no cookies sent, none stored.
	SendCookies *bool `json:"sendCookies,omitempty"`
	// PingIntervalMs sends a keepalive ping this often while connected
	// (WS-09); missing or 0 sends none.
	PingIntervalMs *int64 `json:"pingIntervalMs,omitempty"`
	// PongTimeoutMs closes the connection when a keepalive ping is not
	// answered in time; missing means 10 s, 0 never gives up.
	PongTimeoutMs *int64 `json:"pongTimeoutMs,omitempty"`
	// AutoReconnect connects again with back-off when the connection
	// drops (WS-10); missing means off.
	AutoReconnect *bool `json:"autoReconnect,omitempty"`
	// ReconnectMaxAttempts limits the reconnects in a row; missing means
	// 5, 0 no limit.
	ReconnectMaxAttempts *int `json:"reconnectMaxAttempts,omitempty"`
	// ReconnectDelayMs is the first wait, doubled for each further
	// reconnect (missing: 1000); ReconnectMaxDelayMs caps it (missing:
	// 30000).
	ReconnectDelayMs    *int64  `json:"reconnectDelayMs,omitempty"`
	ReconnectMaxDelayMs *int64  `json:"reconnectMaxDelayMs,omitempty"`
	Unknown             Unknown `json:"-"`
}

// Where a Socket.IO request's auth goes, for SocketIORequest.AuthIn
// (AUTH-31).
const (
	// SocketIOAuthHandshake sends it in the headers or query string of
	// the transport requests (the default, written as a missing member).
	SocketIOAuthHandshake = "handshake"
	// SocketIOAuthPayload adds it to the CONNECT packet's auth payload.
	SocketIOAuthPayload = "payload"
)

// HandshakeAuthOf returns a, the auth of a websocket, socketio or grpc
// request, with the type "" read as "none": for these kinds the zero Auth
// (a request built without one, a file without the member) sends nothing,
// as before auth applied to handshakes (AUTH-31). An HTTP request's ""
// is "inherit" instead. Requests created in the app start as "inherit".
func HandshakeAuthOf(a Auth) Auth {
	if a.Type == "" {
		a.Type = AuthNone
	}
	return a
}

// handshakeAuthOut returns a, the auth of a websocket, socketio or grpc
// request, as it is written: "none" (or "") without anything else, the
// default, as the zero Auth, which omitzero leaves out.
func handshakeAuthOut(a Auth) Auth {
	a = HandshakeAuthOf(a)
	if a.Type == AuthNone && len(a.Unknown) == 0 {
		return Auth{}
	}
	return a
}

// Socket.IO transports, for SocketIORequest.Transport (SIO-01).
const (
	// SocketIOAuto connects over WebSocket and falls back to long-polling
	// (the default, written as a missing member).
	SocketIOAuto = "auto"
	// SocketIOWebSocket connects over WebSocket only.
	SocketIOWebSocket = "websocket"
	// SocketIOPolling uses HTTP long-polling only.
	SocketIOPolling = "polling"
	// SocketIOUpgrade starts with long-polling and upgrades to WebSocket.
	SocketIOUpgrade = "upgrade"
)

// SocketIODefaultPath is the handshake path of Socket.IO servers, written
// as a missing member.
const SocketIODefaultPath = "/socket.io/"

// SocketIORequest is a Socket.IO connection file (kind "socketio", SIO-01,
// SIO-03…SIO-07): the server URL and handshake (query rows, headers, path,
// namespace, auth payload, transport) and the emit being composed.
type SocketIORequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// URL is the server's http(s):// or ws(s):// URL as shown in the
	// editor, {{variables}} unresolved, with the enabled query parameters
	// in its query string. Its path is the namespace when Namespace is
	// empty.
	URL string `json:"url"`
	// Params is the query-parameter table, including disabled rows.
	Params  []KeyValue `json:"params,omitempty"`
	Headers []KeyValue `json:"headers,omitempty"`
	// Path is the Engine.IO handshake path; "" (and "/socket.io/") is the
	// default.
	Path string `json:"path,omitempty"`
	// Namespace is the namespace to connect to ("/admin"); "" is the URL's
	// path, or the main namespace "/".
	Namespace string `json:"namespace,omitempty"`
	// AuthPayload is the JSON object sent as the CONNECT packet's auth
	// (SIO-07), as typed, {{variables}} unresolved; "" sends none.
	AuthPayload string `json:"authPayload,omitempty"`
	// Transport is "auto" (the default, written as a missing member),
	// "websocket", "polling" or "upgrade". One this version does not know
	// is kept.
	Transport string `json:"transport,omitempty"`
	// Version is the server's Socket.IO version (SIO-02): "v2" for a
	// socket.io v2 server (Engine.IO 3); left out for v3 and v4. One this
	// version does not know is kept.
	Version string `json:"version,omitempty"`
	// Event, Args and Ack are the composer's draft: the event name, its
	// arguments as JSON text (an array holds one argument per element;
	// {"$base64": "…"} and {"$hex": "…"} objects are binary arguments) and
	// whether to ask for an acknowledgement.
	Event string `json:"event,omitempty"`
	Args  string `json:"args,omitempty"`
	Ack   bool   `json:"ack,omitempty"`
	// SavedEmits are named emits kept with the connection and sent with
	// one click, in the order the editor lists them.
	SavedEmits []SocketIOEmit `json:"savedEmits,omitempty"`
	// Listen are the event names shown in the log (SIO-04); none shows
	// every event.
	Listen []string `json:"listen,omitempty"`
	// Auth is sent with the handshake (AUTH-31), as for WebSocketRequest:
	// a missing member reads as, and "none" is written as, no auth.
	Auth Auth `json:"auth,omitzero"`
	// AuthIn is where Auth's credentials go: "handshake" (the default,
	// written as a missing member) in the headers or query of the
	// transport requests, or "payload" as members of the CONNECT packet's
	// auth payload. One this version does not know is kept and refused.
	AuthIn   string           `json:"authIn,omitempty"`
	Settings SocketIOSettings `json:"settings,omitzero"`
	// Assertions are checked against the received events when the
	// connection ends, and on demand (TEST-08). Left out when none.
	Assertions []Assertion `json:"assertions,omitempty"`
	Unknown    Unknown     `json:"-"`
}

// SocketIOEmit is a saved emit of a Socket.IO request.
type SocketIOEmit struct {
	// Name is the label shown in the list.
	Name string `json:"name"`
	// Event is the event name, Args its arguments as for
	// SocketIORequest.Args, {{variables}} unresolved.
	Event   string  `json:"event"`
	Args    string  `json:"args,omitempty"`
	Ack     bool    `json:"ack,omitempty"`
	Unknown Unknown `json:"-"`
}

// SocketIOSettings override the defaults for one Socket.IO request. A nil
// member means "use the default"; when all are nil the member is left out
// of the file.
type SocketIOSettings struct {
	// ConnectTimeoutMs bounds the handshake and the namespace CONNECT in
	// milliseconds; 0 means no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs,omitempty"`
	// AckTimeoutMs is how long an emit waits for its acknowledgement.
	AckTimeoutMs *int64 `json:"ackTimeoutMs,omitempty"`
	// VerifyTLS false turns off certificate verification.
	VerifyTLS *bool `json:"verifyTls,omitempty"`
	// SendCookies false leaves the workspace cookie jar out.
	SendCookies *bool   `json:"sendCookies,omitempty"`
	Unknown     Unknown `json:"-"`
}

// GRPCRequest is a gRPC call file (kind "grpc", GRPC-01, GRPC-02, GRPC-10):
// the server address, the .proto files the service is described by, the
// chosen service and method, and the message and metadata being composed.
type GRPCRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Address is "host:port" as typed, with {{variables}} unresolved.
	Address string `json:"address"`
	// Proto is the request's own proto setup; with Proto.Inherit set the
	// nearest folder above that has one is used instead.
	Proto GRPCProto `json:"proto,omitzero"`
	// Service is the fully-qualified service name ("shop.v1.Orders").
	Service string `json:"service,omitempty"`
	// Method is the method's simple name ("GetOrder").
	Method string `json:"method,omitempty"`
	// Message is the request message editor's JSON text as typed. It is
	// kept as text, so a half-typed message auto-saves as it is.
	Message string `json:"message,omitempty"`
	// Metadata are the request metadata rows, including disabled ones.
	Metadata []KeyValue `json:"metadata,omitempty"`
	// Auth is sent in the call's metadata (AUTH-31), as for
	// WebSocketRequest: a missing member reads as, and "none" is written
	// as, no auth.
	Auth     Auth         `json:"auth,omitzero"`
	Settings GRPCSettings `json:"settings,omitzero"`
	// Assertions are checked against the call's response messages and
	// status when it ends, and on demand (TEST-08). Left out when none.
	Assertions []Assertion `json:"assertions,omitempty"`
	Unknown    Unknown     `json:"-"`
}

// SSERequest is a Server-Sent Events stream file (kind "sse", SSE-01,
// SSE-02): an HTTP request whose response is read as a text/event-stream.
// Its members are those of HTTPRequest, in the same order, so the two
// convert into each other (HTTP); only Method is optional here: "" (the
// default, written as a missing member for "GET" too) connects with GET.
type SSERequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Method is "" for GET, or another method token (POST for a streaming
	// API that takes a body).
	Method string `json:"method,omitempty"`
	// URL is the URL as shown in the editor, with {{variables}} unresolved
	// and the enabled query parameters in its query string.
	URL        string     `json:"url"`
	Params     []KeyValue `json:"params,omitempty"`
	PathParams []KeyValue `json:"pathParams,omitempty"`
	Headers    []KeyValue `json:"headers,omitempty"`
	Body       Body       `json:"body,omitzero"`
	// BodyExamples are the named bodies, as for HTTPRequest.
	BodyExamples []BodyExample   `json:"bodyExamples,omitempty"`
	Auth         Auth            `json:"auth"`
	Settings     RequestSettings `json:"settings,omitzero"`
	// Assertions are checked against the received events when the
	// stream ends, and on demand (TEST-08); they are kept for the HTTP
	// request it converts to.
	Assertions []Assertion `json:"assertions,omitempty"`
	// Extractions are kept for the HTTP request it converts to, like
	// Assertions: nothing is extracted from an event stream.
	Extractions []Extraction `json:"extractions,omitempty"`
	// ResponseExamples are kept for the HTTP request it converts to; an
	// event stream has no history to save one from.
	ResponseExamples []ResponseExample `json:"responseExamples,omitempty"`
	Unknown          Unknown           `json:"-"`
}

// HTTP returns the request as the HTTP request it is sent as: the same
// members, with the method "GET" when it is empty.
func (r SSERequest) HTTP() HTTPRequest {
	h := HTTPRequest(r)
	h.Head = Head{}
	if h.Method == "" {
		h.Method = "GET"
	}
	return h
}

// GRPCProto is a proto setup: the .proto files to load and the import
// roots to resolve their imports through. Paths inside the project are
// relative to its root with "/" separators; others are absolute
// (docs/file-format.md).
type GRPCProto struct {
	// Files are the .proto files whose services can be called, in order.
	Files []string `json:"files,omitempty"`
	// ImportPaths are the import roots, searched in order.
	ImportPaths []string `json:"importPaths,omitempty"`
	// Inherit, on a request, uses the nearest folder's setup instead of
	// Files and ImportPaths (which are kept). Folders ignore it.
	Inherit bool    `json:"inherit,omitempty"`
	Unknown Unknown `json:"-"`
}

// GRPCSettings override the app's settings for one gRPC request. A nil
// member means "use the app setting"; when all are unset the member is
// left out of the file.
type GRPCSettings struct {
	// TLS connects with TLS instead of plaintext HTTP/2 (h2c).
	TLS bool `json:"tls,omitempty"`
	// VerifyTLS false turns off certificate verification with TLS.
	VerifyTLS *bool `json:"verifyTls,omitempty"`
	// ConnectTimeoutMs bounds DNS, connect and the TLS handshake in
	// milliseconds; 0 means no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs,omitempty"`
	// DeadlineMs is the call's deadline in milliseconds (grpc-timeout);
	// 0 means none.
	DeadlineMs *int64 `json:"deadlineMs,omitempty"`
	// Transport is how the call goes on the wire (GRPC-25):
	// GRPCTransportGRPC (also when missing), GRPCTransportWeb or
	// GRPCTransportWebText.
	Transport string `json:"transport,omitempty"`
	// Compression is the request compression (GRPC-24):
	// GRPCCompressionIdentity (also when missing) or GRPCCompressionGzip.
	Compression string  `json:"compression,omitempty"`
	Unknown     Unknown `json:"-"`
}

// Values of GRPCSettings.Transport and GRPCSettings.Compression.
const (
	GRPCTransportGRPC       = "grpc"
	GRPCTransportWeb        = "grpc-web"
	GRPCTransportWebText    = "grpc-web-text"
	GRPCCompressionIdentity = "identity"
	GRPCCompressionGzip     = "gzip"
)

// FolderGRPC is a folder's gRPC defaults, shared by the gRPC requests
// inside it that inherit them.
type FolderGRPC struct {
	// Proto is the folder's proto setup; Inherit is not used.
	Proto   GRPCProto `json:"proto,omitzero"`
	Unknown Unknown   `json:"-"`
}

// FolderServer is a saved server config (SRV-04): a local server defined
// in a folder's _folder.json. The members are those of the Servers view's
// server (docs/servers.md); the one of Mock, WebSocket, Webhook, SSE and UDP that
// matches Type holds its configuration, and nil gets the type's default.
type FolderServer struct {
	// Type is the kind of server: "mock_http", "echo_http", "websocket",
	// "tcp", "webhook", "sse", "udp", "grpc_mock", "socketio" or "graphql_mock".
	Type string `json:"type"`
	// Name is a label; "" shows the type's label.
	Name string `json:"name,omitempty"`
	// Host is the bind address: "" or "localhost" for loopback, or an IP
	// address. Anything but loopback needs AllowLAN.
	Host string `json:"host,omitempty"`
	// Port is 1–65535, or 0 (left out) for a free port at every start.
	Port     int  `json:"port,omitempty"`
	AllowLAN bool `json:"allowLan,omitempty"`
	// TLS makes the server serve TLS (SRV-05); left out for plain text.
	TLS *ServerTLS `json:"tls,omitempty"`
	// Mock is the HTTP mock's routes (type mock_http).
	Mock *ServerMock `json:"mock,omitempty"`
	// WebSocket is the WebSocket server's settings (type websocket).
	WebSocket *ServerWebSocket `json:"webSocket,omitempty"`
	// Webhook is the webhook receiver's response (type webhook).
	Webhook *ServerWebhook `json:"webhook,omitempty"`
	// SSE is the SSE server's settings (type sse).
	SSE *ServerSSE `json:"sse,omitempty"`
	// UDP is the UDP server's replies (type udp).
	UDP *ServerUDP `json:"udp,omitempty"`
	// GRPCMock is the gRPC mock server's .proto files and canned
	// responses (type grpc_mock).
	GRPCMock *ServerGRPCMock `json:"grpcMock,omitempty"`
	// SocketIO is the Socket.IO server's settings and auto-reply rules
	// (type socketio).
	SocketIO *ServerSocketIO `json:"socketIo,omitempty"`
	// GraphQLMock is the GraphQL mock server's schema, data settings and
	// overrides (type graphql_mock).
	GraphQLMock *ServerGraphQLMock `json:"graphqlMock,omitempty"`
	Unknown     Unknown            `json:"-"`
}

// ServerGraphQLMock is the configuration of a GraphQL mock server
// (SSRV-08). Members left out take the defaults of a new server, except
// the schema: a file without schemaFile and schemaText serves none.
type ServerGraphQLMock struct {
	// Path is the endpoint ("/graphql" when left out).
	Path string `json:"path,omitempty"`
	// SchemaFile is an SDL file or a saved introspection result (.json),
	// stored as a GraphQL request's schemaFile (relative to the project
	// root with "/", or absolute). SchemaText, used when there is no
	// file, is pasted SDL or introspection JSON.
	SchemaFile string `json:"schemaFile,omitempty"`
	SchemaText string `json:"schemaText,omitempty"`
	// NoIntrospection turns __schema and __type off.
	NoIntrospection bool `json:"noIntrospection,omitempty"`
	// ListLength is the length of generated lists (2 when left out),
	// NullPercent how often a nullable field is null, Seed what varies
	// the generated data.
	ListLength  *int  `json:"listLength,omitempty"`
	NullPercent int   `json:"nullPercent,omitempty"`
	Seed        int64 `json:"seed,omitempty"`
	// AllowOrigin is the CORS origin allowed: "*" for any, left out for
	// none.
	AllowOrigin string `json:"allowOrigin,omitempty"`
	// Overrides are the static responses, first match wins.
	Overrides []ServerGraphQLOverride `json:"overrides,omitempty"`
	Unknown   Unknown                 `json:"-"`
}

// ServerGraphQLOverride is a static response of a GraphQL mock server:
// Target "Type.field" (with Args, a JSON object its arguments must match)
// or a scalar or enum type, answered with Value (JSON) or, Mode "error",
// a field error with Error and Code.
type ServerGraphQLOverride struct {
	Target string `json:"target"`
	Args   string `json:"args,omitempty"`
	// Mode is "error", or left out for "value".
	Mode     string  `json:"mode,omitempty"`
	Value    string  `json:"value,omitempty"`
	Error    string  `json:"error,omitempty"`
	Code     string  `json:"code,omitempty"`
	Disabled bool    `json:"disabled,omitempty"`
	Unknown  Unknown `json:"-"`
}

// ServerSocketIO is the configuration of a Socket.IO server (SSRV-03).
// Members left out take the defaults of a new server.
type ServerSocketIO struct {
	// Path is the Engine.IO path ("/socket.io/" when left out).
	Path string `json:"path,omitempty"`
	// Namespaces are the namespaces accepted besides "/"; left out for
	// any namespace.
	Namespaces []string `json:"namespaces,omitempty"`
	// Auth is a JSON object whose members a CONNECT's auth payload must
	// have; left out for none.
	Auth string `json:"auth,omitempty"`
	// Transports is "websocket" or "polling" to accept one only; left out
	// for both.
	Transports string `json:"transports,omitempty"`
	// AllowOrigin is the CORS origin of the long-polling responses: "*"
	// for any, left out for none.
	AllowOrigin string `json:"allowOrigin,omitempty"`
	// PingIntervalMs and PingTimeoutMs are the heartbeat (25000 and 20000
	// when left out).
	PingIntervalMs int `json:"pingIntervalMs,omitempty"`
	PingTimeoutMs  int `json:"pingTimeoutMs,omitempty"`
	// MaxPayload is the largest payload accepted, in bytes (1000000 when
	// left out).
	MaxPayload int `json:"maxPayload,omitempty"`
	// Echo emits events no rule answers back to their socket.
	Echo bool `json:"echo,omitempty"`
	// AckMode is "empty" or "none"; left out, acknowledgements echo the
	// event's arguments.
	AckMode string `json:"ackMode,omitempty"`
	// Rules are the auto-reply rules, in order.
	Rules   []ServerSIORule `json:"rules,omitempty"`
	Unknown Unknown         `json:"-"`
}

// ServerSIORule is an auto-reply rule of a Socket.IO server: an event
// whose name matches Pattern (Match "exact", "contains" or "regex") is
// acknowledged with Ack and answered with the event Emit and its
// EmitArgs, after DelayMs.
type ServerSIORule struct {
	Match    string  `json:"match"`
	Pattern  string  `json:"pattern"`
	Ack      string  `json:"ack,omitempty"`
	Emit     string  `json:"emit,omitempty"`
	EmitArgs string  `json:"emitArgs,omitempty"`
	DelayMs  int     `json:"delayMs,omitempty"`
	Disabled bool    `json:"disabled,omitempty"`
	Unknown  Unknown `json:"-"`
}

// ServerGRPCMock is the configuration of a gRPC mock server (SSRV-07).
type ServerGRPCMock struct {
	// Files and ImportPaths are the .proto files served and their import
	// roots, stored as a gRPC request's (relative to the project root with
	// "/", or absolute).
	Files       []string `json:"files,omitempty"`
	ImportPaths []string `json:"importPaths,omitempty"`
	// NoReflection turns the server reflection service off.
	NoReflection bool `json:"noReflection,omitempty"`
	// Methods are the canned responses, one per method.
	Methods []ServerGRPCMethod `json:"methods,omitempty"`
	Unknown Unknown            `json:"-"`
}

// ServerGRPCMethod is the canned response of one method of a gRPC mock
// server.
type ServerGRPCMethod struct {
	// Service is the fully-qualified service, Method the method's name.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Messages are the response messages as JSON text, kept as typed.
	Messages []string `json:"messages,omitempty"`
	// Status is the gRPC status code (left out for OK) and StatusMessage
	// its message.
	Status        int    `json:"status,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
	// Headers and Trailers are the response metadata; "-bin" values are
	// Base64.
	Headers  []KeyValue `json:"headers,omitempty"`
	Trailers []KeyValue `json:"trailers,omitempty"`
	// DelayMs holds the answer back; IntervalMs spaces streamed messages.
	DelayMs    int `json:"delayMs,omitempty"`
	IntervalMs int `json:"intervalMs,omitempty"`
	// Bidi is how a bidirectional method answers: "each" (left out),
	// "stream" or "echo".
	Bidi    string  `json:"bidi,omitempty"`
	Unknown Unknown `json:"-"`
}

// ServerSSE is the configuration of an SSE server (SSRV-04).
type ServerSSE struct {
	// Path is where subscribers connect ("/events" when left out), or "*"
	// for any path.
	Path string `json:"path,omitempty"`
	// AllowOrigin is the CORS origin allowed to subscribe: "*" for any,
	// left out for none.
	AllowOrigin string `json:"allowOrigin,omitempty"`
	// KeepAliveSec is the keep-alive comment interval; left out for none.
	KeepAliveSec int `json:"keepAliveSec,omitempty"`
	// RetryMs is sent to new subscribers as the retry field; left out for
	// none.
	RetryMs int `json:"retryMs,omitempty"`
	// AutoID numbers the events sent without an ID.
	AutoID bool `json:"autoId,omitempty"`
	// Timers send an event to every subscriber at an interval.
	Timers  []ServerSSETimer `json:"timers,omitempty"`
	Unknown Unknown          `json:"-"`
}

// ServerSSETimer is a timer of an SSE server: the event Event with Data
// and ID (dynamic values allowed) every IntervalMs.
type ServerSSETimer struct {
	Event      string  `json:"event,omitempty"`
	Data       string  `json:"data"`
	ID         string  `json:"id,omitempty"`
	IntervalMs int     `json:"intervalMs"`
	Disabled   bool    `json:"disabled,omitempty"`
	Unknown    Unknown `json:"-"`
}

// ServerUDP is how a UDP server answers datagrams (SSRV-06).
type ServerUDP struct {
	// Echo sends every datagram back to its sender.
	Echo bool `json:"echo,omitempty"`
	// FixedReply sends Reply to the sender of every datagram.
	FixedReply bool   `json:"fixedReply,omitempty"`
	Reply      string `json:"reply,omitempty"`
	// ReplyFormat is "text" (left out) or "hex".
	ReplyFormat string  `json:"replyFormat,omitempty"`
	Unknown     Unknown `json:"-"`
}

// ServerMock is the configuration of an HTTP mock server (MOCK-01).
type ServerMock struct {
	Routes []MockRoute `json:"routes,omitempty"`
	// StrictTrailingSlash and CaseInsensitive are the path matching
	// options of every route (docs/mock-routes.md).
	StrictTrailingSlash bool    `json:"strictTrailingSlash,omitempty"`
	CaseInsensitive     bool    `json:"caseInsensitive,omitempty"`
	Unknown             Unknown `json:"-"`
}

// MockRoute is one route of an HTTP mock server: a method and a path
// pattern, and the response it gets.
type MockRoute struct {
	// ID identifies the route ("rt_" and 12 hex digits).
	ID string `json:"id"`
	// Method is an HTTP method or "ANY".
	Method string `json:"method"`
	// Path is a pattern: /users/:id, /users/{id:[0-9]+}, /files/**.
	Path   string `json:"path"`
	Status int    `json:"status"`
	// Headers are the response headers; disabled rows are not sent.
	Headers []KeyValue `json:"headers,omitempty"`
	// Body is the response body: mode "none" or "raw" of a request body.
	Body Body `json:"body"`
	// Delay holds the response back (MOCK-04); left out for none.
	Delay MockDelay `json:"delay,omitzero"`
	// Responses are the route's other responses (MOCK-05, MOCK-07), tried
	// in order before Status, Headers and Body, the default response.
	Responses []MockResponse `json:"responses,omitempty"`
	// Templated renders the default response's body and header values
	// with the request's values and dynamic values (MOCK-06); left out
	// when false, so bodies saved before it stay literal.
	Templated bool    `json:"templated,omitempty"`
	Disabled  bool    `json:"disabled,omitempty"`
	Unknown   Unknown `json:"-"`
}

// MockResponse is one of a mock route's other responses: picked when all
// its rules match the request, or by name or status with the
// X-PostSonde-Response request header (docs/mock-routes.md).
type MockResponse struct {
	// Name selects the response with X-PostSonde-Response; unique within
	// the route and not "default"; may be left out.
	Name    string     `json:"name,omitempty"`
	Status  int        `json:"status"`
	Headers []KeyValue `json:"headers,omitempty"`
	// Body is mode "none" or "raw", like the route's.
	Body Body `json:"body"`
	// Rules must all match; without rules only the header picks the
	// response.
	Rules []MockRule `json:"rules,omitempty"`
	// Templated renders the body and header values with the request's
	// values (MOCK-06); left out when false.
	Templated bool    `json:"templated,omitempty"`
	Unknown   Unknown `json:"-"`
}

// MockRule is one condition of a mock response.
type MockRule struct {
	// Source is "query", "header", "body" or "json" (a JSONPath into the
	// request body).
	Source string `json:"source"`
	// Name is the query parameter, header name or JSONPath; left out for
	// "body".
	Name string `json:"name,omitempty"`
	// Op is equals, not_equals, contains, not_contains, matches, exists
	// or not_exists.
	Op string `json:"op"`
	// Value is what equals, contains and matches compare with.
	Value   string  `json:"value,omitempty"`
	Unknown Unknown `json:"-"`
}

// MockDelay is a mock route's response delay: mode "none", "fixed" (Ms)
// or "random" (MinMs to MaxMs), in milliseconds.
type MockDelay struct {
	Mode    string  `json:"mode,omitempty"`
	Ms      int     `json:"ms,omitempty"`
	MinMs   int     `json:"minMs,omitempty"`
	MaxMs   int     `json:"maxMs,omitempty"`
	Unknown Unknown `json:"-"`
}

// ServerWebSocket is the configuration of a WebSocket server (SSRV-01).
type ServerWebSocket struct {
	// Path is where clients connect ("/" when left out), or "*" for any.
	Path string `json:"path,omitempty"`
	// Subprotocols are the accepted subprotocols.
	Subprotocols []string `json:"subprotocols,omitempty"`
	// Echo sends a message back to its client when no rule answers it.
	Echo bool `json:"echo,omitempty"`
	// MaxMessageSize is the largest message received, in bytes; 0 (left
	// out) means 16 MiB.
	MaxMessageSize int64 `json:"maxMessageSize,omitempty"`
	// Rules are the auto-reply rules (SSRV-02), in order; left out when
	// there are none.
	Rules   []ServerWSRule `json:"rules,omitempty"`
	Unknown Unknown        `json:"-"`
}

// ServerWSRule is an auto-reply rule of a WebSocket server: a text message
// that matches Pattern (Match "exact", "contains" or "regex") gets Reply,
// after DelayMs.
type ServerWSRule struct {
	Match    string  `json:"match"`
	Pattern  string  `json:"pattern"`
	Reply    string  `json:"reply"`
	DelayMs  int     `json:"delayMs,omitempty"`
	Disabled bool    `json:"disabled,omitempty"`
	Unknown  Unknown `json:"-"`
}

// ServerTLS is how a saved server serves TLS (SRV-05): a certificate
// generated by PostSonde's local CA ("generated"), or the user's
// certificate files ("files": CertFile and KeyFile, PEM, or PFXFile,
// PKCS#12). Paths are absolute. A passphrase is never stored here; it is
// kept in the app's local secret storage.
type ServerTLS struct {
	Certificate string  `json:"certificate,omitempty"`
	CertFile    string  `json:"certFile,omitempty"`
	KeyFile     string  `json:"keyFile,omitempty"`
	PFXFile     string  `json:"pfxFile,omitempty"`
	Unknown     Unknown `json:"-"`
}

// ServerWebhook is the response of a webhook receiver (MOCK-08).
type ServerWebhook struct {
	Status  int        `json:"status"`
	Headers []KeyValue `json:"headers,omitempty"`
	Body    string     `json:"body,omitempty"`
	Unknown Unknown    `json:"-"`
}

// Environment is a named variable set, environments/<name>.json. Which
// environment is active is local state, not part of the project.
type Environment struct {
	Head
	ID   string `json:"id"`
	Name string `json:"name"`
	// Color is an optional accent colour, "#rrggbb".
	Color     string     `json:"color,omitempty"`
	Variables []Variable `json:"variables,omitempty"`
	Unknown   Unknown    `json:"-"`
}

// KeyValue is a row of a key/value table: a header, a query or path
// parameter, a URL-encoded form field.
type KeyValue struct {
	Name        string  `json:"name"`
	Value       string  `json:"value"`
	Description string  `json:"description,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	Unknown     Unknown `json:"-"`
}

// Variable is a variable definition.
type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Secret marks a value that must never be written to the project. Its
	// Value is always empty in the file; the value lives in local side
	// files. Marshal refuses a secret variable with a value.
	Secret      bool    `json:"secret,omitempty"`
	Description string  `json:"description,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	Unknown     Unknown `json:"-"`
}

// Body modes.
const (
	BodyNone       = "none"
	BodyRaw        = "raw"
	BodyURLEncoded = "urlencoded"
	BodyMultipart  = "multipart"
	BodyFile       = "file"
)

// Raw body types, for Body.Type.
const (
	RawJSON = "json"
	RawXML  = "xml"
	RawHTML = "html"
	RawText = "text"
	RawYAML = "yaml"
)

// Body is an HTTP request body. Mode says which of the other members are
// used; the others are left out of the file. A mode this version does not
// know is kept with its members.
type Body struct {
	// Mode is "none", "raw", "urlencoded", "multipart" or "file". Empty is
	// written as "none".
	Mode string `json:"mode"`
	// Type is the raw body's language: "json", "xml", "html", "text",
	// "yaml".
	Type string `json:"type,omitempty"`
	// Text is the raw body.
	Text string `json:"text,omitempty"`
	// Fields are the URL-encoded form fields.
	Fields []KeyValue `json:"fields,omitempty"`
	// Parts are the multipart form parts.
	Parts []Part `json:"parts,omitempty"`
	// Path is the file sent as the body. A relative path is relative to
	// the project root.
	Path string `json:"path,omitempty"`
	// ContentType overrides the file body's Content-Type, which is
	// otherwise guessed from the file name's extension.
	ContentType string  `json:"contentType,omitempty"`
	Unknown     Unknown `json:"-"`
}

// BodyExample is a named body of a request (HTTP-17). One example of a
// request's list is Active: its body is the request's Body, which is what
// is sent, and it has none of its own. Switching examples moves the bodies
// between Body and the list; nothing else changes, so a PostSonde that does
// not know examples sends the active body.
type BodyExample struct {
	Name string `json:"name"`
	// Active marks the example whose body is the request's Body.
	Active bool `json:"active,omitempty"`
	// Body is the example's body; left out for the active example.
	Body    Body    `json:"body,omitzero"`
	Unknown Unknown `json:"-"`
}

// ResponseExample is a response saved with a request (HTTP-38): the
// response's status, headers and body, and the request that got it, as it
// was sent. Like a Postman example, it pairs a request with its response;
// the request is the one sent (variables resolved), not the editor's. It
// is saved from the request history, where secrets are masked
// (docs/history.md#secrets), and masked again with every secret PostSonde
// keeps, so it never holds one. Bodies are kept inline up to
// ResponseExampleMaxBody bytes; longer ones are clipped there with
// BodyClipped and BodySize.
type ResponseExample struct {
	// Name is the example's name as the request shows it.
	Name string `json:"name"`
	// SavedAt is when the example was saved, RFC 3339; "" when not known.
	SavedAt string `json:"savedAt,omitempty"`
	// Request is the request as sent; nil when not known.
	Request *ExampleRequest `json:"request,omitempty"`
	// StatusCode is the response's status code, StatusText its reason
	// phrase ("Created").
	StatusCode int    `json:"statusCode"`
	StatusText string `json:"statusText,omitempty"`
	// Headers are the response's header fields in the order received;
	// rows, of which only name and value are used.
	Headers []KeyValue `json:"headers,omitempty"`
	// Body is the response body as text, or its bytes in standard Base64
	// when BodyEncoding is "base64" (a body that is not UTF-8).
	Body         string `json:"body,omitempty"`
	BodyEncoding string `json:"bodyEncoding,omitempty"`
	// BodyClipped is set when Body holds only the first bytes of the
	// body; BodySize then counts all of them.
	BodyClipped bool    `json:"bodyClipped,omitempty"`
	BodySize    int64   `json:"bodySize,omitempty"`
	Unknown     Unknown `json:"-"`
}

// ResponseExampleMaxBody is how much of a body PostSonde writes into a
// response example: 64 KiB, the history's limit. Longer bodies are clipped
// (ResponseExample.BodyClipped); a longer body written by hand is read as
// it is.
const ResponseExampleMaxBody = 64 << 10

// ExampleBodyBase64 is ResponseExample.BodyEncoding for a body kept as
// Base64.
const ExampleBodyBase64 = "base64"

// ExampleRequest is the request of a response example, as it was sent: its
// URL with variables resolved, and every header field written.
type ExampleRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Headers are rows of which only name and value are used.
	Headers []KeyValue `json:"headers,omitempty"`
	// Body is the body sent as a raw body (its type from the
	// Content-Type), or none; left out for none.
	Body    Body    `json:"body,omitzero"`
	Unknown Unknown `json:"-"`
}

// Part is a multipart form part: a text field, or a file when File is set.
type Part struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// File is the path of the file sent as the part's content. A relative
	// path is relative to the project root.
	File string `json:"file,omitempty"`
	// ContentType overrides the part's Content-Type.
	ContentType string  `json:"contentType,omitempty"`
	Description string  `json:"description,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	Unknown     Unknown `json:"-"`
}

// Auth (the authentication of a request or folder) is in auth.go.

// RequestSettings override the app's execution settings for one request.
// A nil member means "use the app setting"; when all are nil the member is
// left out of the file.
type RequestSettings struct {
	// TimeoutMs is the whole-request timeout in milliseconds; 0 means none.
	TimeoutMs       *int64 `json:"timeoutMs,omitempty"`
	FollowRedirects *bool  `json:"followRedirects,omitempty"`
	// MaxRedirects is the most redirects followed; 0 follows none.
	MaxRedirects *int `json:"maxRedirects,omitempty"`
	// FollowOriginalMethod keeps the method and body on a 301, 302 or 303
	// redirect instead of switching to GET.
	FollowOriginalMethod *bool `json:"followOriginalMethod,omitempty"`
	// ForwardAuthorization sends Authorization to another origin on a
	// redirect; missing or false strips it there.
	ForwardAuthorization *bool `json:"forwardAuthorization,omitempty"`
	VerifyTLS            *bool `json:"verifyTls,omitempty"`
	// URLEncoding is how the URL's path and query are percent-encoded
	// (HTTP-24): URLEncodingAuto (also when missing), URLEncodingRFC3986
	// or URLEncodingNone.
	URLEncoding string `json:"urlEncoding,omitempty"`
	// HTTPVersion is the HTTP version offered (HTTP-25): HTTPVersionAuto
	// (also when missing), HTTPVersion1 or HTTPVersion2.
	HTTPVersion string `json:"httpVersion,omitempty"`
	// SendCookies false leaves the cookie jar's cookies out of the request
	// (HTTP-26); missing or true sends them.
	SendCookies *bool `json:"sendCookies,omitempty"`
	// StoreCookies false keeps the response's cookies out of the cookie
	// jar (HTTP-26); missing or true stores them.
	StoreCookies *bool `json:"storeCookies,omitempty"`
	// OmitHeaders names automatic headers not to send (HTTP-09):
	// "Content-Type" for the type the body implies, or the name of a
	// header the auth adds ("Authorization", an API key's header).
	// Compared ignoring case; missing or empty sends them all.
	OmitHeaders []string `json:"omitHeaders,omitempty"`
	// Reconnect is the reconnection policy of an sse request (SSE-03):
	// whether a stream that ends or drops is opened again with
	// Last-Event-ID. Other kinds ignore it. nil (the member left out)
	// does not reconnect.
	Reconnect *SSEReconnect `json:"reconnect,omitempty"`
	// GraphQLTransport is how a graphql request runs each kind of
	// operation (GQL-35): over HTTP, WebSocket or Server-Sent Events.
	// Other kinds ignore it. nil (the member left out) uses the defaults:
	// HTTP for queries and mutations, graphql-transport-ws for
	// subscriptions.
	GraphQLTransport *GraphQLTransport `json:"graphqlTransport,omitempty"`
	// GraphQLSchemaRefresh is when a graphql request's editor fetches its
	// endpoint's schema again by itself (GQL-19). Other kinds ignore it.
	// nil (the member left out) never does: the schema is fetched only
	// from the Fetch schema / Refresh button.
	GraphQLSchemaRefresh *GraphQLSchemaRefresh `json:"graphqlSchemaRefresh,omitempty"`
	Unknown              Unknown               `json:"-"`
}

// GraphQLSchemaRefresh is the opt-in schema auto-refresh of a graphql
// request (GQL-19, docs/graphql.md#schema-refresh-gql-19). Each member is
// off when left out, so files without it never refresh by themselves.
type GraphQLSchemaRefresh struct {
	// OnSend refreshes the schema after a send of the request whose
	// response says the server's schema changed: the server rejected the
	// document in validation although the cached schema accepts it.
	OnSend bool `json:"onSend,omitempty"`
	// IntervalMinutes refreshes the schema every so many minutes while
	// the request's editor is open; 0 (left out) never does.
	IntervalMinutes int     `json:"intervalMinutes,omitempty"`
	Unknown         Unknown `json:"-"`
}

// GraphQLTransport picks the transport of each operation type of a
// graphql request (GQL-35, docs/graphql-subscriptions.md). Each member is
// one of the GraphQLTransport* values; "" (the member left out) means the
// default. A value this version does not know is kept.
type GraphQLTransport struct {
	// Query and Mutation default to GraphQLTransportHTTP.
	Query    string `json:"query,omitempty"`
	Mutation string `json:"mutation,omitempty"`
	// Subscription defaults to GraphQLTransportWS; it cannot be
	// GraphQLTransportHTTP.
	Subscription string  `json:"subscription,omitempty"`
	Unknown      Unknown `json:"-"`
}

// Values of the GraphQLTransport members (GQL-35).
const (
	// GraphQLTransportHTTP: GraphQL over HTTP, POST or GET (GQL-02).
	GraphQLTransportHTTP = "http"
	// GraphQLTransportWS: WebSocket with the graphql-transport-ws
	// protocol of graphql-ws (GQL-30).
	GraphQLTransportWS = "ws"
	// GraphQLTransportLegacyWS: WebSocket with Apollo's legacy
	// subscriptions-transport-ws protocol, subprotocol graphql-ws
	// (GQL-31).
	GraphQLTransportLegacyWS = "ws-legacy"
	// GraphQLTransportSSE: GraphQL over Server-Sent Events, graphql-sse's
	// distinct connections mode (GQL-32).
	GraphQLTransportSSE = "sse"
)

// SSEReconnect is how an sse request reconnects (SSE-03,
// docs/sse.md#reconnecting). A nil member means its default.
type SSEReconnect struct {
	// Enabled reconnects when the stream ends or the connection drops;
	// missing or false does not.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxAttempts is how many reconnection attempts in a row may fail
	// (end without delivering any bytes) before giving up; 0 means no
	// limit, missing the default (5).
	MaxAttempts *int `json:"maxAttempts,omitempty"`
	// DelayMs is the reconnection time in milliseconds until the server
	// sets one with a retry field; missing means the default (3000).
	DelayMs *int64  `json:"delayMs,omitempty"`
	Unknown Unknown `json:"-"`
}

// Values of RequestSettings.URLEncoding (HTTP-24).
const (
	// URLEncodingAuto encodes as browsers do (the WHATWG URL Standard).
	URLEncodingAuto = "auto"
	// URLEncodingRFC3986 encodes every character RFC 3986 does not allow.
	URLEncodingRFC3986 = "rfc3986"
	// URLEncodingNone sends the URL as typed, except the bytes a request
	// line cannot carry (control characters, space, non-ASCII).
	URLEncodingNone = "none"
)

// Values of RequestSettings.HTTPVersion (HTTP-25).
const (
	// HTTPVersionAuto offers HTTP/2 and HTTP/1.1 through ALPN on https.
	HTTPVersionAuto = "auto"
	// HTTPVersion1 sends HTTP/1.1 only.
	HTTPVersion1 = "http1"
	// HTTPVersion2 sends HTTP/2 only (h2c with prior knowledge on http).
	HTTPVersion2 = "http2"
)

// The MarshalJSON and UnmarshalJSON methods below keep Unknown. Each
// converts to a local type without methods (so encoding/json does not
// recurse) and lets stablejson split off or append the unknown members.

func isNull(data []byte) bool { return bytes.Equal(bytes.TrimSpace(data), []byte("null")) }

// decodeObject decodes data into dst, a pointer to a method-less struct,
// and returns its unknown members.
func decodeObject(data []byte, dst any) (Unknown, error) {
	extra, err := stablejson.SplitUnknown(data, dst)
	return Unknown(extra), err
}

func (w Workspace) MarshalJSON() ([]byte, error) {
	type plain Workspace
	return stablejson.MarshalWithExtra(plain(w), w.Unknown)
}

func (w *Workspace) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Workspace
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*w = Workspace(p)
	return nil
}

func (f Folder) MarshalJSON() ([]byte, error) {
	type plain Folder
	return stablejson.MarshalWithExtra(plain(f), f.Unknown)
}

func (f *Folder) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Folder
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*f = Folder(p)
	return nil
}

func (r HTTPRequest) MarshalJSON() ([]byte, error) {
	type plain HTTPRequest
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *HTTPRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain HTTPRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = HTTPRequest(p)
	return nil
}

func (r GraphQLRequest) MarshalJSON() ([]byte, error) {
	type plain GraphQLRequest
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *GraphQLRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GraphQLRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = GraphQLRequest(p)
	return nil
}

func (r TCPRequest) MarshalJSON() ([]byte, error) {
	type plain TCPRequest
	// The defaults are written as missing members.
	if r.SendMode == SendText {
		r.SendMode = ""
	}
	if r.LineEnding == LineEndingNone {
		r.LineEnding = ""
	}
	if r.Framing.Mode == FramingNone {
		// So that a framing with only the default mode is left out.
		r.Framing.Mode = ""
	}
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *TCPRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain TCPRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = TCPRequest(p)
	return nil
}

func (f TCPFraming) MarshalJSON() ([]byte, error) {
	type plain TCPFraming
	// The default is written as a missing member.
	if f.Mode == FramingNone {
		f.Mode = ""
	}
	return stablejson.MarshalWithExtra(plain(f), f.Unknown)
}

func (f *TCPFraming) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain TCPFraming
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*f = TCPFraming(p)
	return nil
}

func (s TCPSettings) MarshalJSON() ([]byte, error) {
	type plain TCPSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (s *TCPSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain TCPSettings
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*s = TCPSettings(p)
	return nil
}

func (r WebSocketRequest) MarshalJSON() ([]byte, error) {
	type plain WebSocketRequest
	// The default is written as a missing member.
	if r.MessageFormat == MessageText {
		r.MessageFormat = ""
	}
	r.Auth = handshakeAuthOut(r.Auth)
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *WebSocketRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain WebSocketRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = WebSocketRequest(p)
	return nil
}

func (a Assertion) MarshalJSON() ([]byte, error) {
	type plain Assertion
	return stablejson.MarshalWithExtra(plain(a), a.Unknown)
}

func (a *Assertion) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Assertion
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*a = Assertion(p)
	return nil
}

func (e Extraction) MarshalJSON() ([]byte, error) {
	type plain Extraction
	return stablejson.MarshalWithExtra(plain(e), e.Unknown)
}

func (e *Extraction) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Extraction
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*e = Extraction(p)
	return nil
}

func (m WebSocketMessage) MarshalJSON() ([]byte, error) {
	type plain WebSocketMessage
	// The default is written as a missing member.
	if m.Format == MessageText {
		m.Format = ""
	}
	return stablejson.MarshalWithExtra(plain(m), m.Unknown)
}

func (m *WebSocketMessage) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain WebSocketMessage
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*m = WebSocketMessage(p)
	return nil
}

func (s WebSocketSettings) MarshalJSON() ([]byte, error) {
	type plain WebSocketSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (s *WebSocketSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain WebSocketSettings
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*s = WebSocketSettings(p)
	return nil
}

func (r SocketIORequest) MarshalJSON() ([]byte, error) {
	type plain SocketIORequest
	// The defaults are written as missing members.
	if r.Transport == SocketIOAuto {
		r.Transport = ""
	}
	if r.Path == SocketIODefaultPath {
		r.Path = ""
	}
	if r.AuthIn == SocketIOAuthHandshake {
		r.AuthIn = ""
	}
	r.Auth = handshakeAuthOut(r.Auth)
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *SocketIORequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain SocketIORequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = SocketIORequest(p)
	return nil
}

func (e SocketIOEmit) MarshalJSON() ([]byte, error) {
	type plain SocketIOEmit
	return stablejson.MarshalWithExtra(plain(e), e.Unknown)
}

func (e *SocketIOEmit) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain SocketIOEmit
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*e = SocketIOEmit(p)
	return nil
}

func (s SocketIOSettings) MarshalJSON() ([]byte, error) {
	type plain SocketIOSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (s *SocketIOSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain SocketIOSettings
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*s = SocketIOSettings(p)
	return nil
}

func (r GRPCRequest) MarshalJSON() ([]byte, error) {
	type plain GRPCRequest
	r.Auth = handshakeAuthOut(r.Auth)
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *GRPCRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GRPCRequest
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*r = GRPCRequest(q)
	return nil
}

func (r SSERequest) MarshalJSON() ([]byte, error) {
	type plain SSERequest
	// GET, the default, is written as a missing member.
	if r.Method == "GET" {
		r.Method = ""
	}
	// So is a body of mode none without members.
	if (r.Body.Mode == "" || r.Body.Mode == BodyNone) && r.Body.Type == "" && r.Body.Text == "" &&
		len(r.Body.Fields) == 0 && len(r.Body.Parts) == 0 && r.Body.Path == "" && r.Body.ContentType == "" && len(r.Body.Unknown) == 0 {
		r.Body = Body{}
	}
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *SSERequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain SSERequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = SSERequest(p)
	return nil
}

func (p GRPCProto) MarshalJSON() ([]byte, error) {
	type plain GRPCProto
	return stablejson.MarshalWithExtra(plain(p), p.Unknown)
}

func (p *GRPCProto) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GRPCProto
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*p = GRPCProto(q)
	return nil
}

func (s GRPCSettings) MarshalJSON() ([]byte, error) {
	type plain GRPCSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (s *GRPCSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GRPCSettings
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*s = GRPCSettings(q)
	return nil
}

func (g FolderGRPC) MarshalJSON() ([]byte, error) {
	type plain FolderGRPC
	return stablejson.MarshalWithExtra(plain(g), g.Unknown)
}

func (g *FolderGRPC) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain FolderGRPC
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*g = FolderGRPC(q)
	return nil
}

func (x FolderServer) MarshalJSON() ([]byte, error) {
	type plain FolderServer
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *FolderServer) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain FolderServer
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = FolderServer(q)
	return nil
}

func (x ServerMock) MarshalJSON() ([]byte, error) {
	type plain ServerMock
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerMock) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerMock
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerMock(q)
	return nil
}

func (x MockRoute) MarshalJSON() ([]byte, error) {
	type plain MockRoute
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *MockRoute) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain MockRoute
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = MockRoute(q)
	return nil
}

func (x MockResponse) MarshalJSON() ([]byte, error) {
	type plain MockResponse
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *MockResponse) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain MockResponse
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = MockResponse(q)
	return nil
}

func (x MockRule) MarshalJSON() ([]byte, error) {
	type plain MockRule
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *MockRule) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain MockRule
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = MockRule(q)
	return nil
}

func (x MockDelay) MarshalJSON() ([]byte, error) {
	type plain MockDelay
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *MockDelay) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain MockDelay
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = MockDelay(q)
	return nil
}

func (x ServerWebSocket) MarshalJSON() ([]byte, error) {
	type plain ServerWebSocket
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerWebSocket) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerWebSocket
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerWebSocket(q)
	return nil
}

func (x ServerSocketIO) MarshalJSON() ([]byte, error) {
	type plain ServerSocketIO
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerSocketIO) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerSocketIO
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerSocketIO(q)
	return nil
}

func (x ServerSIORule) MarshalJSON() ([]byte, error) {
	type plain ServerSIORule
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerSIORule) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerSIORule
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerSIORule(q)
	return nil
}

func (x ServerWSRule) MarshalJSON() ([]byte, error) {
	type plain ServerWSRule
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerWSRule) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerWSRule
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerWSRule(q)
	return nil
}

func (x ServerTLS) MarshalJSON() ([]byte, error) {
	type plain ServerTLS
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerTLS) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerTLS
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerTLS(q)
	return nil
}

func (x ServerWebhook) MarshalJSON() ([]byte, error) {
	type plain ServerWebhook
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerWebhook) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerWebhook
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerWebhook(q)
	return nil
}

func (x ServerSSE) MarshalJSON() ([]byte, error) {
	type plain ServerSSE
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerSSE) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerSSE
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerSSE(q)
	return nil
}

func (x ServerSSETimer) MarshalJSON() ([]byte, error) {
	type plain ServerSSETimer
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerSSETimer) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerSSETimer
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerSSETimer(q)
	return nil
}

func (x ServerUDP) MarshalJSON() ([]byte, error) {
	type plain ServerUDP
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerUDP) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerUDP
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerUDP(q)
	return nil
}

func (x ServerGRPCMock) MarshalJSON() ([]byte, error) {
	type plain ServerGRPCMock
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerGRPCMock) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerGRPCMock
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerGRPCMock(q)
	return nil
}

func (x ServerGraphQLMock) MarshalJSON() ([]byte, error) {
	type plain ServerGraphQLMock
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerGraphQLMock) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerGraphQLMock
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerGraphQLMock(q)
	return nil
}

func (x ServerGraphQLOverride) MarshalJSON() ([]byte, error) {
	type plain ServerGraphQLOverride
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerGraphQLOverride) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerGraphQLOverride
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerGraphQLOverride(q)
	return nil
}

func (x ServerGRPCMethod) MarshalJSON() ([]byte, error) {
	type plain ServerGRPCMethod
	return stablejson.MarshalWithExtra(plain(x), x.Unknown)
}

func (x *ServerGRPCMethod) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ServerGRPCMethod
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*x = ServerGRPCMethod(q)
	return nil
}

func (e Environment) MarshalJSON() ([]byte, error) {
	type plain Environment
	return stablejson.MarshalWithExtra(plain(e), e.Unknown)
}

func (e *Environment) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Environment
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*e = Environment(p)
	return nil
}

func (kv KeyValue) MarshalJSON() ([]byte, error) {
	type plain KeyValue
	return stablejson.MarshalWithExtra(plain(kv), kv.Unknown)
}

func (kv *KeyValue) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain KeyValue
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*kv = KeyValue(p)
	return nil
}

func (v Variable) MarshalJSON() ([]byte, error) {
	type plain Variable
	return stablejson.MarshalWithExtra(plain(v), v.Unknown)
}

func (v *Variable) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Variable
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*v = Variable(p)
	return nil
}

func (b Body) MarshalJSON() ([]byte, error) {
	type plain Body
	if b.Mode == "" {
		b.Mode = BodyNone
	}
	return stablejson.MarshalWithExtra(plain(b), b.Unknown)
}

func (b *Body) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Body
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*b = Body(p)
	return nil
}

func (e BodyExample) MarshalJSON() ([]byte, error) {
	type plain BodyExample
	return stablejson.MarshalWithExtra(plain(e), e.Unknown)
}

func (e *BodyExample) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain BodyExample
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*e = BodyExample(p)
	return nil
}

func (e ResponseExample) MarshalJSON() ([]byte, error) {
	type plain ResponseExample
	return stablejson.MarshalWithExtra(plain(e), e.Unknown)
}

func (e *ResponseExample) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ResponseExample
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*e = ResponseExample(p)
	return nil
}

func (r ExampleRequest) MarshalJSON() ([]byte, error) {
	type plain ExampleRequest
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *ExampleRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain ExampleRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = ExampleRequest(p)
	return nil
}

func (p Part) MarshalJSON() ([]byte, error) {
	type plain Part
	return stablejson.MarshalWithExtra(plain(p), p.Unknown)
}

func (p *Part) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Part
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	*p = Part(q)
	return nil
}

func (a Auth) MarshalJSON() ([]byte, error) {
	type plain Auth
	a = a.normalized()
	return stablejson.MarshalWithExtra(plain(a), a.Unknown)
}

func (a *Auth) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain Auth
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*a = Auth(p)
	return nil
}

func (s RequestSettings) MarshalJSON() ([]byte, error) {
	type plain RequestSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (r SSEReconnect) MarshalJSON() ([]byte, error) {
	type plain SSEReconnect
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *SSEReconnect) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain SSEReconnect
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = SSEReconnect(p)
	return nil
}

func (r GraphQLSchemaRefresh) MarshalJSON() ([]byte, error) {
	type plain GraphQLSchemaRefresh
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *GraphQLSchemaRefresh) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GraphQLSchemaRefresh
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = GraphQLSchemaRefresh(p)
	return nil
}

func (t GraphQLTransport) MarshalJSON() ([]byte, error) {
	type plain GraphQLTransport
	return stablejson.MarshalWithExtra(plain(t), t.Unknown)
}

func (t *GraphQLTransport) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain GraphQLTransport
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*t = GraphQLTransport(p)
	return nil
}

func (s *RequestSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain RequestSettings
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*s = RequestSettings(p)
	return nil
}
