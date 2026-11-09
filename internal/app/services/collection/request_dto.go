package collection

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeInvalidRequest: UpdateRequest got a request that cannot be a request
// file: an unknown kind, or an extra member that is not a JSON object
// (kind invalid_argument). Details: id, field.
const CodeInvalidRequest = "collection.invalid_request"

// RequestDTO is the content of a request file, as the request editor edits
// it (docs/file-format.md). One shape serves every kind: the members of
// the other kinds are left empty and ignored.
//
// Extra fields carry the members of a file object that this version has no
// field for, as the text of a JSON object ("" when there are none). The UI
// passes them back unchanged, so hand-written or newer members survive an
// edit.
type RequestDTO struct {
	// ID is the request's ID; it names the file UpdateRequest saves.
	ID string `json:"id"`
	// Kind is "http", "graphql", "tcp", "udp", "websocket", "grpc", "sse"
	// or "socketio".
	// Changing it changes the request's kind. An sse request has the
	// members of an http request.
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Method is the HTTP method of an http or sse request; for graphql "" (POST
	// with a JSON body) or "GET"; for grpc the method's simple name.
	Method string `json:"method"`
	URL    string `json:"url"`
	// Params are http, sse, websocket and socketio only; PathParams are
	// http and sse only.
	Params     []KeyValueDTO `json:"params"`
	PathParams []KeyValueDTO `json:"pathParams"`
	Headers    []KeyValueDTO `json:"headers"`
	// Body is http and sse only. With body examples it is the active
	// example's body: the one that is sent.
	Body BodyDTO `json:"body"`
	// BodyExamples are the named bodies of an http or sse request
	// (HTTP-17), in order, never null; empty when Body is the only body.
	// Exactly one is Active when there are any: its Body is empty and
	// the request's Body is its body.
	BodyExamples []BodyExampleDTO `json:"bodyExamples"`
	// Query, Variables, OperationName and ConnectionParams are graphql
	// only. ConnectionParams is the connection_init payload's JSON text
	// (GQL-33).
	Query            string `json:"query"`
	Variables        string `json:"variables"`
	OperationName    string `json:"operationName"`
	ConnectionParams string `json:"connectionParams"`
	// SchemaFile is graphql only: the SDL file the schema comes from
	// (GQL-11), as stored; "" for introspection.
	SchemaFile string `json:"schemaFile"`
	// Extensions and Files are graphql only, left out when empty:
	// the request's extensions object as JSON text (GQL-06), and the files
	// uploaded with the GraphQL multipart request spec (GQL-07), stored as
	// multipart parts (Name the variable path, File the file as stored,
	// Value unused).
	Extensions string    `json:"extensions,omitempty"`
	Files      []PartDTO `json:"files,omitempty"`
	// Address is tcp and grpc only ("host:port"). SendMode and LineEnding
	// are tcp only. SendMode is "text" or "hex" ("" reads as "text"),
	// LineEnding "none", "lf" or "crlf" ("" reads as "none").
	Address    string `json:"address"`
	SendMode   string `json:"sendMode"`
	LineEnding string `json:"lineEnding"`
	// Framing is tcp only (TCP-03): how received bytes are split into
	// messages. Set for every tcp request, left out for the other kinds.
	Framing *TCPFramingDTO `json:"framing,omitempty"`
	// UDP is udp only (UDP-01): the socket settings. Address, SendMode
	// ("text", "hex" or "base64") and Message are a udp request's too.
	UDP *UDPOptionsDTO `json:"udp,omitempty"`
	// Message is the composer's draft, tcp, websocket, grpc and socketio
	// only (for grpc the request message's JSON text, for socketio the
	// emit's arguments as JSON text).
	Message string `json:"message"`
	// Subprotocols and MessageFormat are websocket only. MessageFormat is
	// "text", "json", "hex" or "base64" ("" reads as "text").
	Subprotocols  []string `json:"subprotocols"`
	MessageFormat string   `json:"messageFormat"`
	// SavedMessages are the saved messages of a websocket request (WS-07),
	// or the saved emits of a socketio request, in order. Unlike the other
	// lists it is left out when empty, and for the other kinds.
	SavedMessages []SavedMessageDTO `json:"savedMessages,omitempty"`
	// Path, Namespace, AuthPayload, Transport, SocketIOVersion, Event, Ack
	// and Listen are socketio only (SIO-01…SIO-07): the handshake path,
	// the namespace, the auth payload's JSON text, the transport ("" reads
	// as "auto"), the server's Socket.IO version ("v2", or "" for v3/v4),
	// the composer's event name and ack switch, and the events the log
	// shows. Left out when empty, and for the other kinds.
	Path        string `json:"path,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	AuthPayload string `json:"authPayload,omitempty"`
	Transport   string `json:"transport,omitempty"`
	// SocketIOVersion is "v2" or "" (socketio only).
	SocketIOVersion string   `json:"socketIoVersion,omitempty"`
	Event           string   `json:"event,omitempty"`
	Ack             bool     `json:"ack,omitempty"`
	Listen          []string `json:"listen,omitempty"`
	// Service, Proto and Metadata are grpc only. Service is the
	// fully-qualified service name.
	Service  string        `json:"service"`
	Proto    GRPCProtoDTO  `json:"proto"`
	Metadata []KeyValueDTO `json:"metadata"`
	// Auth is http, graphql, sse, websocket, socketio and grpc (not tcp).
	// On websocket, socketio and grpc it is sent with the handshake
	// (AUTH-31); a type "" reads as "none" there, as a file without the
	// member does.
	Auth AuthDTO `json:"auth"`
	// AuthIn is socketio only: where the auth goes, "handshake" (also "")
	// or "payload" (the CONNECT packet's auth payload). Left out when "".
	AuthIn string `json:"authIn,omitempty"`
	// Settings: timeoutMs, followRedirects, maxRedirects,
	// followOriginalMethod, forwardAuthorization, urlEncoding, httpVersion
	// and storeCookies are http, graphql and sse only; verifyTls is http,
	// graphql, sse, websocket, socketio, grpc and tcp; connectTimeoutMs is
	// tcp, websocket, socketio and grpc; sendCookies is http, graphql, sse,
	// websocket and socketio; tls is grpc and tcp; deadlineMs is grpc only;
	// ackTimeoutMs is socketio only.
	Settings RequestSettingsDTO `json:"settings"`
	// Assertions are http, graphql, sse, websocket, socketio and grpc: the
	// checks run against every response (TEST-01), or against a stream's
	// messages (TEST-08), in order. Like SavedMessages it is left out when
	// empty.
	Assertions []AssertionDTO `json:"assertions,omitempty"`
	// Extractions are http, graphql and sse only: the rules that set
	// variables from every response (TEST-05), in order. Like Assertions
	// it is left out when empty.
	Extractions []ExtractionDTO `json:"extractions,omitempty"`
	// ResponseExamples are http and graphql only: the responses saved with
	// the request (HTTP-38, response_examples.go), in order. Like
	// Assertions it is left out when empty.
	ResponseExamples []ResponseExampleDTO `json:"responseExamples,omitempty"`
	Extra            string               `json:"extra"`
	// Version is the version of this content (Request only; windows.go):
	// pass it as UpdateRequest's BaseVersion. UpdateRequest ignores it
	// here; left out while 0.
	Version int `json:"version,omitempty"`
}

// AssertionDTO is an assertion of an http or graphql request (TEST-01):
// target · operator · expected, as stored (docs/file-format.md#assertions).
type AssertionDTO struct {
	// Target is "status", "header", "cookie", "jsonpath", "xpath",
	// "regex", "body", "time" or "size".
	Target string `json:"target"`
	// Expr is the header or cookie name, JSONPath, XPath or regular
	// expression of the targets that take one.
	Expr string `json:"expr"`
	// Op is the operator: "equals", "not_equals", "contains", … "is_type".
	Op string `json:"op"`
	// Expected is the value as typed, {{variables}} unresolved.
	Expected string `json:"expected"`
	// Messages is the message scope of an assertion of a websocket,
	// socketio, grpc or sse request (TEST-08): "any", "every", "none",
	// "first", "last" or "nth"; "" checks the stream as a whole. Left out
	// when "".
	Messages string `json:"messages,omitempty"`
	// Index is the message "nth" checks, from 1; left out when 0.
	Index   int    `json:"index,omitempty"`
	Enabled bool   `json:"enabled"`
	Extra   string `json:"extra"`
}

// ExtractionDTO is an extraction rule of an http or graphql request
// (TEST-05), as stored (docs/file-format.md#extractions).
type ExtractionDTO struct {
	// Source is "jsonpath", "xpath", "regex", "header", "cookie" or
	// "status".
	Source string `json:"source"`
	// Expr is the JSONPath, XPath, regular expression, or header or
	// cookie name, {{variables}} unresolved.
	Expr string `json:"expr"`
	// Variable is the name of the variable set.
	Variable string `json:"variable"`
	// Scope is "runtime" or "run"; "" reads as "runtime" and is stored as
	// left out.
	Scope   string `json:"scope"`
	Secret  bool   `json:"secret"`
	Persist bool   `json:"persist"`
	Enabled bool   `json:"enabled"`
	Extra   string `json:"extra"`
}

// KeyValueDTO is a row of a key/value table.
type KeyValueDTO struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Extra       string `json:"extra"`
}

// SavedMessageDTO is a saved message of a websocket request (WS-07), or
// a saved emit of a socketio request.
type SavedMessageDTO struct {
	Name string `json:"name"`
	// Text is the message as typed, {{variables}} unresolved; for an emit
	// its arguments' JSON text.
	Text string `json:"text"`
	// Format is "text", "json", "hex" or "base64" ("" reads as "text");
	// "" for an emit.
	Format string `json:"format"`
	// Event and Ack are an emit's event name and ack switch; left out for
	// websocket messages.
	Event string `json:"event,omitempty"`
	Ack   bool   `json:"ack,omitempty"`
	Extra string `json:"extra"`
}

// BodyDTO is an http request's body; Mode says which members are used.
type BodyDTO struct {
	// Mode is "none", "raw", "urlencoded", "multipart", "file", or a mode
	// of a newer PostSonde; "" is written as "none".
	Mode   string        `json:"mode"`
	Type   string        `json:"type"`
	Text   string        `json:"text"`
	Fields []KeyValueDTO `json:"fields"`
	Parts  []PartDTO     `json:"parts"`
	Path   string        `json:"path"`
	// ContentType is the file body's Content-Type override, "" to guess
	// it from the file name.
	ContentType string `json:"contentType"`
	Extra       string `json:"extra"`
}

// BodyExampleDTO is a named body of a request (HTTP-17).
type BodyExampleDTO struct {
	Name string `json:"name"`
	// Active marks the example whose body is the request's Body; its own
	// Body is empty (mode "").
	Active bool    `json:"active"`
	Body   BodyDTO `json:"body"`
	Extra  string  `json:"extra"`
}

// PartDTO is a multipart form part.
type PartDTO struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	File        string `json:"file"`
	ContentType string `json:"contentType"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Extra       string `json:"extra"`
}

// GRPCProtoDTO is a gRPC request's or folder's proto setup: .proto files
// and import roots, stored relative to the project root when inside it.
type GRPCProtoDTO struct {
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Inherit uses the nearest folder's setup instead (requests only).
	Inherit bool   `json:"inherit"`
	Extra   string `json:"extra"`
}

// AuthDTO (a request's authentication) is in auth_dto.go.

// RequestSettingsDTO overrides app settings for one request; null means
// "use the app setting".
type RequestSettingsDTO struct {
	TimeoutMs       *int64 `json:"timeoutMs"`
	FollowRedirects *bool  `json:"followRedirects"`
	MaxRedirects    *int   `json:"maxRedirects"`
	// FollowOriginalMethod and ForwardAuthorization are http and graphql
	// only (HTTP-22).
	FollowOriginalMethod *bool `json:"followOriginalMethod"`
	ForwardAuthorization *bool `json:"forwardAuthorization"`
	VerifyTLS            *bool `json:"verifyTls"`
	// ConnectTimeoutMs is tcp, websocket and grpc only.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs"`
	// URLEncoding (HTTP-24): "auto", "rfc3986" or "none"; "" is auto.
	URLEncoding string `json:"urlEncoding"`
	// HTTPVersion (HTTP-25): "auto", "http1" or "http2"; "" is auto.
	HTTPVersion string `json:"httpVersion"`
	// SendCookies false leaves the cookie jar's cookies out of the request
	// (HTTP-26); on a websocket request it leaves the jar out entirely.
	SendCookies *bool `json:"sendCookies"`
	// StoreCookies false keeps the response's cookies out of the jar
	// (HTTP-26).
	StoreCookies *bool `json:"storeCookies"`
	// OmitHeaders are the automatic headers not sent (HTTP-09):
	// "Content-Type" or the name of an auth header; http only. Left out
	// when empty.
	OmitHeaders []string `json:"omitHeaders,omitempty"`
	// TLS is grpc and tcp only: true connects with TLS; null or false is
	// plaintext.
	TLS *bool `json:"tls"`
	// DeadlineMs is grpc only: the call's deadline.
	DeadlineMs *int64 `json:"deadlineMs"`
	// Transport is grpc only (GRPC-25): "grpc", "grpc-web" or
	// "grpc-web-text"; left out (or "") for grpc.
	Transport string `json:"transport,omitempty"`
	// Compression is grpc only (GRPC-24): "identity" or "gzip"; left out
	// (or "") for none.
	Compression string `json:"compression,omitempty"`
	// Reconnect is sse only: the reconnection policy (SSE-03). Left out
	// (null) when the file has none, which does not reconnect.
	Reconnect *SSEReconnectDTO `json:"reconnect,omitempty"`
	// GraphQLTransport is graphql only: the transport of each operation
	// type (GQL-35). Left out (null) when the file has none: the defaults.
	GraphQLTransport *GraphQLTransportDTO `json:"graphqlTransport,omitempty"`
	// GraphQLSchemaRefresh is graphql only: the opt-in schema
	// auto-refresh (GQL-19). Left out (null) when the file has none: the
	// schema is fetched only when the user asks.
	GraphQLSchemaRefresh *GraphQLSchemaRefreshDTO `json:"graphqlSchemaRefresh,omitempty"`
	// AckTimeoutMs is socketio only: how long an emit waits for its
	// acknowledgement. Left out when null.
	AckTimeoutMs *int64 `json:"ackTimeoutMs,omitempty"`
	// PingIntervalMs, PongTimeoutMs (WS-09), AutoReconnect,
	// ReconnectMaxAttempts, ReconnectDelayMs and ReconnectMaxDelayMs
	// (WS-10) are websocket only, left out when null.
	PingIntervalMs       *int64 `json:"pingIntervalMs,omitempty"`
	PongTimeoutMs        *int64 `json:"pongTimeoutMs,omitempty"`
	AutoReconnect        *bool  `json:"autoReconnect,omitempty"`
	ReconnectMaxAttempts *int   `json:"reconnectMaxAttempts,omitempty"`
	ReconnectDelayMs     *int64 `json:"reconnectDelayMs,omitempty"`
	ReconnectMaxDelayMs  *int64 `json:"reconnectMaxDelayMs,omitempty"`
	// Proxy is every kind's own proxy
	// (docs/network.md#a-requests-own-proxy). Left out (null) when the
	// request inherits the proxy in effect and keeps nothing else.
	Proxy *RequestProxyDTO `json:"proxy,omitempty"`
	Extra string           `json:"extra"`
}

// SSEReconnectDTO is an sse request's reconnection policy (SSE-03); a null
// member means its default (docs/file-format.md#sse-request-kind-sse).
type SSEReconnectDTO struct {
	// Enabled reconnects with Last-Event-ID when the stream ends or drops.
	Enabled *bool `json:"enabled"`
	// MaxAttempts is how many attempts in a row may fail; 0 is no limit.
	MaxAttempts *int `json:"maxAttempts"`
	// DelayMs is the reconnection time until the server sends retry.
	DelayMs *int64 `json:"delayMs"`
	Extra   string `json:"extra"`
}

// TCPFramingDTO is a tcp request's message framing (TCP-03), as stored:
// "" reads as the default of each member (docs/file-format.md).
type TCPFramingDTO struct {
	// Mode is "none", "newline" or "length" ("" reads as "none").
	Mode string `json:"mode"`
	// Delimiter is "lf" or "crlf" ("" reads as "lf"); newline only.
	Delimiter string `json:"delimiter"`
	// PrefixBytes is 1, 2, 4 or 8 (0 reads as 4); length only.
	PrefixBytes int `json:"prefixBytes"`
	// ByteOrder is "big" or "little" ("" reads as "big"); length only.
	ByteOrder string `json:"byteOrder"`
	// MaxMessageBytes limits a received message; null is 1 MiB.
	MaxMessageBytes *int64 `json:"maxMessageBytes"`
	Extra           string `json:"extra"`
}

// GraphQLTransportDTO is a graphql request's transport per operation type
// (GQL-35): "http", "ws", "ws-legacy" or "sse", "" for the default (http
// for queries and mutations, ws for subscriptions;
// docs/file-format.md#settings).
type GraphQLTransportDTO struct {
	Query        string `json:"query"`
	Mutation     string `json:"mutation"`
	Subscription string `json:"subscription"`
	Extra        string `json:"extra"`
}

// GraphQLSchemaRefreshDTO is a graphql request's schema auto-refresh
// (GQL-19, docs/file-format.md#settings): OnSend refreshes after a send
// whose response shows the server's schema changed, IntervalMinutes
// every so many minutes while the editor is open (0: never).
type GraphQLSchemaRefreshDTO struct {
	OnSend          bool   `json:"onSend"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Extra           string `json:"extra"`
}

// UpdateRequestInputDTO is what UpdateRequest takes.
type UpdateRequestInputDTO struct {
	Request RequestDTO `json:"request"`
	// Revision is the UI's draft revision. It is only passed back in the
	// collection:saved and collection:save-failed events, so the UI knows
	// which edit reached the disk.
	Revision int `json:"revision"`
	// BaseVersion is the version of the request the edit was made from
	// (RequestDTO.Version from Request, or the last UpdateRequest result):
	// an older one than the service holds is refused with
	// collection.edited_elsewhere (windows.go). Null is not checked.
	BaseVersion *int `json:"baseVersion,omitempty"`
}

// UpdateResultDTO is the result of UpdateRequest and UpdateFolder.
type UpdateResultDTO struct {
	// Version is the item's version with this edit: the next edit's
	// BaseVersion.
	Version int `json:"version"`
}

// toRequestDTO maps a request file. f is *projectfile.HTTPRequest,
// *projectfile.GraphQLRequest, *projectfile.TCPRequest,
// *projectfile.WebSocketRequest, *projectfile.GRPCRequest,
// *projectfile.SSERequest or *projectfile.SocketIORequest. Lists of the
// other kinds are empty, not null.
func toRequestDTO(f projectfile.File) RequestDTO {
	dto := kindRequestDTO(f)
	if dto.Kind == "" {
		return dto
	}
	if dto.Metadata == nil {
		dto.Metadata = make([]KeyValueDTO, 0)
	}
	if dto.BodyExamples == nil {
		dto.BodyExamples = make([]BodyExampleDTO, 0)
	}
	if dto.Proto.Files == nil {
		dto.Proto.Files = make([]string, 0)
	}
	if dto.Proto.ImportPaths == nil {
		dto.Proto.ImportPaths = make([]string, 0)
	}
	return dto
}

func kindRequestDTO(f projectfile.File) RequestDTO {
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		return RequestDTO{
			ID:           r.ID,
			Kind:         string(projectfile.KindHTTP),
			Name:         r.Name,
			Description:  r.Description,
			Method:       r.Method,
			URL:          r.URL,
			Params:       toKeyValueDTOs(r.Params),
			PathParams:   toKeyValueDTOs(r.PathParams),
			Headers:      toKeyValueDTOs(r.Headers),
			Body:         toBodyDTO(r.Body),
			BodyExamples: toBodyExampleDTOs(r.BodyExamples),
			Subprotocols: make([]string, 0),
			Auth:         toAuthDTO(r.Auth),
			Settings:     toSettingsDTO(r.Settings),
			Assertions:   toAssertionDTOs(r.Assertions),
			Extractions:  toExtractionDTOs(r.Extractions),
			Extra:        extraText(r.Unknown),

			ResponseExamples: toResponseExampleDTOs(r.ResponseExamples),
		}
	case *projectfile.SSERequest:
		// The members of an HTTP request (SSE-01); the method reads as
		// GET when the file leaves it out.
		h := r.HTTP()
		dto := kindRequestDTO(&h)
		dto.Kind = string(projectfile.KindSSE)
		return dto
	case *projectfile.GraphQLRequest:
		return RequestDTO{
			ID:               r.ID,
			Kind:             string(projectfile.KindGraphQL),
			Name:             r.Name,
			Description:      r.Description,
			Method:           r.Method,
			URL:              r.URL,
			Params:           make([]KeyValueDTO, 0),
			PathParams:       make([]KeyValueDTO, 0),
			Headers:          toKeyValueDTOs(r.Headers),
			Body:             BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
			Query:            r.Query,
			Variables:        r.Variables,
			OperationName:    r.OperationName,
			ConnectionParams: r.ConnectionParams,
			SchemaFile:       r.SchemaFile,
			Extensions:       r.Extensions,
			Files:            toPartDTOs(r.Files),
			Subprotocols:     make([]string, 0),
			Auth:             toAuthDTO(r.Auth),
			Settings:         toSettingsDTO(r.Settings),
			Assertions:       toAssertionDTOs(r.Assertions),
			Extractions:      toExtractionDTOs(r.Extractions),
			ResponseExamples: toResponseExampleDTOs(r.ResponseExamples),
			Extra:            extraText(r.Unknown),
		}
	case *projectfile.TCPRequest:
		return RequestDTO{
			ID:          r.ID,
			Kind:        string(projectfile.KindTCP),
			Name:        r.Name,
			Description: r.Description,
			Params:      make([]KeyValueDTO, 0),
			PathParams:  make([]KeyValueDTO, 0),
			Headers:     make([]KeyValueDTO, 0),
			Body:        BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
			Address:     r.Address,
			SendMode:    r.SendMode,
			LineEnding:  r.LineEnding,
			Framing: &TCPFramingDTO{
				Mode:            r.Framing.Mode,
				Delimiter:       r.Framing.Delimiter,
				PrefixBytes:     r.Framing.PrefixBytes,
				ByteOrder:       r.Framing.ByteOrder,
				MaxMessageBytes: clonePtr(r.Framing.MaxMessageBytes),
				Extra:           extraText(r.Framing.Unknown),
			},
			Message:      r.Message,
			Subprotocols: make([]string, 0),
			Settings: RequestSettingsDTO{
				ConnectTimeoutMs: clonePtr(r.Settings.ConnectTimeoutMs),
				TLS:              trueOrNil(r.Settings.TLS),
				VerifyTLS:        clonePtr(r.Settings.VerifyTLS),
				Proxy:            toProxyDTO(r.Settings.Proxy),
				Extra:            extraText(r.Settings.Unknown),
			},
			Extra: extraText(r.Unknown),
		}
	case *projectfile.UDPRequest:
		return udpRequestDTO(r)
	case *projectfile.WebSocketRequest:
		return RequestDTO{
			ID:            r.ID,
			Kind:          string(projectfile.KindWebSocket),
			Name:          r.Name,
			Description:   r.Description,
			URL:           r.URL,
			Params:        toKeyValueDTOs(r.Params),
			PathParams:    make([]KeyValueDTO, 0),
			Headers:       toKeyValueDTOs(r.Headers),
			Body:          BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
			Message:       r.Message,
			Subprotocols:  append(make([]string, 0, len(r.Subprotocols)), r.Subprotocols...),
			MessageFormat: r.MessageFormat,
			SavedMessages: toSavedMessageDTOs(r.SavedMessages),
			Assertions:    toAssertionDTOs(r.Assertions),
			Auth:          toAuthDTO(projectfile.HandshakeAuthOf(r.Auth)),
			Settings: RequestSettingsDTO{
				VerifyTLS:            clonePtr(r.Settings.VerifyTLS),
				ConnectTimeoutMs:     clonePtr(r.Settings.ConnectTimeoutMs),
				SendCookies:          clonePtr(r.Settings.SendCookies),
				PingIntervalMs:       clonePtr(r.Settings.PingIntervalMs),
				PongTimeoutMs:        clonePtr(r.Settings.PongTimeoutMs),
				AutoReconnect:        clonePtr(r.Settings.AutoReconnect),
				ReconnectMaxAttempts: clonePtr(r.Settings.ReconnectMaxAttempts),
				ReconnectDelayMs:     clonePtr(r.Settings.ReconnectDelayMs),
				ReconnectMaxDelayMs:  clonePtr(r.Settings.ReconnectMaxDelayMs),
				Proxy:                toProxyDTO(r.Settings.Proxy),
				Extra:                extraText(r.Settings.Unknown),
			},
			Extra: extraText(r.Unknown),
		}
	case *projectfile.SocketIORequest:
		return RequestDTO{
			ID:              r.ID,
			Kind:            string(projectfile.KindSocketIO),
			Name:            r.Name,
			Description:     r.Description,
			URL:             r.URL,
			Params:          toKeyValueDTOs(r.Params),
			PathParams:      make([]KeyValueDTO, 0),
			Headers:         toKeyValueDTOs(r.Headers),
			Body:            BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
			Message:         r.Args,
			Subprotocols:    make([]string, 0),
			SavedMessages:   toSavedEmitDTOs(r.SavedEmits),
			Path:            r.Path,
			Namespace:       r.Namespace,
			AuthPayload:     r.AuthPayload,
			Transport:       r.Transport,
			SocketIOVersion: r.Version,
			Event:           r.Event,
			Ack:             r.Ack,
			Listen:          cloneStrings(r.Listen),
			Assertions:      toAssertionDTOs(r.Assertions),
			Auth:            toAuthDTO(projectfile.HandshakeAuthOf(r.Auth)),
			AuthIn:          r.AuthIn,
			Settings: RequestSettingsDTO{
				VerifyTLS:        clonePtr(r.Settings.VerifyTLS),
				ConnectTimeoutMs: clonePtr(r.Settings.ConnectTimeoutMs),
				SendCookies:      clonePtr(r.Settings.SendCookies),
				AckTimeoutMs:     clonePtr(r.Settings.AckTimeoutMs),
				Proxy:            toProxyDTO(r.Settings.Proxy),
				Extra:            extraText(r.Settings.Unknown),
			},
			Extra: extraText(r.Unknown),
		}
	case *projectfile.GRPCRequest:
		var tls *bool
		if r.Settings.TLS {
			tls = new(bool)
			*tls = true
		}
		return RequestDTO{
			ID:           r.ID,
			Kind:         string(projectfile.KindGRPC),
			Name:         r.Name,
			Description:  r.Description,
			Method:       r.Method,
			Params:       make([]KeyValueDTO, 0),
			PathParams:   make([]KeyValueDTO, 0),
			Headers:      make([]KeyValueDTO, 0),
			Body:         BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
			Address:      r.Address,
			Message:      r.Message,
			Subprotocols: make([]string, 0),
			Service:      r.Service,
			Proto:        toGRPCProtoDTO(r.Proto),
			Metadata:     toKeyValueDTOs(r.Metadata),
			Assertions:   toAssertionDTOs(r.Assertions),
			Auth:         toAuthDTO(projectfile.HandshakeAuthOf(r.Auth)),
			Settings: RequestSettingsDTO{
				VerifyTLS:        clonePtr(r.Settings.VerifyTLS),
				ConnectTimeoutMs: clonePtr(r.Settings.ConnectTimeoutMs),
				TLS:              tls,
				DeadlineMs:       clonePtr(r.Settings.DeadlineMs),
				Transport:        r.Settings.Transport,
				Compression:      r.Settings.Compression,
				Proxy:            toProxyDTO(r.Settings.Proxy),
				Extra:            extraText(r.Settings.Unknown),
			},
			Extra: extraText(r.Unknown),
		}
	}
	return RequestDTO{}
}

// grpcDefault writes a grpc setting's default value as a missing member.
func grpcDefault(v, def string) string {
	if v == def {
		return ""
	}
	return v
}

// toGRPCProtoDTO maps a proto setup; its lists are never null.
func toGRPCProtoDTO(p projectfile.GRPCProto) GRPCProtoDTO {
	return GRPCProtoDTO{
		Files:       append(make([]string, 0, len(p.Files)), p.Files...),
		ImportPaths: append(make([]string, 0, len(p.ImportPaths)), p.ImportPaths...),
		Inherit:     p.Inherit,
		Extra:       extraText(p.Unknown),
	}
}

// grpcProto builds a proto setup; empty lists are left out of the file.
func (c *converter) grpcProto(field string, p GRPCProtoDTO) projectfile.GRPCProto {
	out := projectfile.GRPCProto{Inherit: p.Inherit, Unknown: c.extra(field+".extra", p.Extra)}
	if len(p.Files) > 0 {
		out.Files = append([]string(nil), p.Files...)
	}
	if len(p.ImportPaths) > 0 {
		out.ImportPaths = append([]string(nil), p.ImportPaths...)
	}
	return out
}

// toFile builds the request file of in. Values are checked by
// projectfile.Marshal; this only fails on what cannot be mapped.
func (in RequestDTO) toFile() (projectfile.File, error) {
	c := converter{id: in.ID}
	var f projectfile.File
	switch projectfile.Kind(in.Kind) {
	case projectfile.KindHTTP, projectfile.KindSSE:
		h := &projectfile.HTTPRequest{
			Head:         projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindHTTP},
			ID:           in.ID,
			Name:         in.Name,
			Description:  in.Description,
			Method:       in.Method,
			URL:          in.URL,
			Params:       c.keyValues("params", in.Params),
			PathParams:   c.keyValues("pathParams", in.PathParams),
			Headers:      c.keyValues("headers", in.Headers),
			Body:         c.body("body", in.Body),
			BodyExamples: c.bodyExamples(in.BodyExamples),
			Auth:         c.auth(in.Auth),
			Settings:     c.settings(in.Settings),
			Assertions:   c.assertions(in.Assertions),
			Extractions:  c.extractions(in.Extractions),
			Unknown:      c.extra("extra", in.Extra),

			ResponseExamples: c.responseExamples(in.ResponseExamples),
		}
		f = h
		if in.Kind == string(projectfile.KindSSE) {
			// An SSE request has the members of an HTTP request (SSE-01).
			sse := projectfile.SSERequest(*h)
			sse.Head.Kind = projectfile.KindSSE
			f = &sse
		}
	case projectfile.KindGraphQL:
		f = &projectfile.GraphQLRequest{
			Head:             projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindGraphQL},
			ID:               in.ID,
			Name:             in.Name,
			Description:      in.Description,
			Method:           in.Method,
			URL:              in.URL,
			Query:            in.Query,
			Variables:        in.Variables,
			OperationName:    in.OperationName,
			ConnectionParams: in.ConnectionParams,
			SchemaFile:       in.SchemaFile,
			Extensions:       in.Extensions,
			Files:            c.parts("files", in.Files),
			Headers:          c.keyValues("headers", in.Headers),
			Auth:             c.auth(in.Auth),
			Settings:         c.settings(in.Settings),
			Assertions:       c.assertions(in.Assertions),
			Extractions:      c.extractions(in.Extractions),
			ResponseExamples: c.responseExamples(in.ResponseExamples),
			Unknown:          c.extra("extra", in.Extra),
		}
	case projectfile.KindTCP:
		f = &projectfile.TCPRequest{
			Head:        projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindTCP},
			ID:          in.ID,
			Name:        in.Name,
			Description: in.Description,
			Address:     in.Address,
			SendMode:    in.SendMode,
			LineEnding:  in.LineEnding,
			Message:     in.Message,
			Framing:     c.tcpFraming(in.Framing),
			Settings: projectfile.TCPSettings{
				ConnectTimeoutMs: clonePtr(in.Settings.ConnectTimeoutMs),
				TLS:              in.Settings.TLS != nil && *in.Settings.TLS,
				VerifyTLS:        clonePtr(in.Settings.VerifyTLS),
				Proxy:            c.proxy(in.Settings.Proxy),
				Unknown:          c.extra("settings.extra", in.Settings.Extra),
			},
			Unknown: c.extra("extra", in.Extra),
		}
	case projectfile.KindUDP:
		f = c.udpRequest(in)
	case projectfile.KindWebSocket:
		var subprotocols []string
		if len(in.Subprotocols) > 0 {
			subprotocols = append(subprotocols, in.Subprotocols...)
		}
		f = &projectfile.WebSocketRequest{
			Head:          projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindWebSocket},
			ID:            in.ID,
			Name:          in.Name,
			Description:   in.Description,
			URL:           in.URL,
			Params:        c.keyValues("params", in.Params),
			Headers:       c.keyValues("headers", in.Headers),
			Subprotocols:  subprotocols,
			Message:       in.Message,
			MessageFormat: in.MessageFormat,
			SavedMessages: c.savedMessages(in.SavedMessages),
			Assertions:    c.assertions(in.Assertions),
			Auth:          c.handshakeAuth(in.Auth),
			Settings: projectfile.WebSocketSettings{
				ConnectTimeoutMs:     clonePtr(in.Settings.ConnectTimeoutMs),
				VerifyTLS:            clonePtr(in.Settings.VerifyTLS),
				SendCookies:          clonePtr(in.Settings.SendCookies),
				PingIntervalMs:       clonePtr(in.Settings.PingIntervalMs),
				PongTimeoutMs:        clonePtr(in.Settings.PongTimeoutMs),
				AutoReconnect:        clonePtr(in.Settings.AutoReconnect),
				ReconnectMaxAttempts: clonePtr(in.Settings.ReconnectMaxAttempts),
				ReconnectDelayMs:     clonePtr(in.Settings.ReconnectDelayMs),
				ReconnectMaxDelayMs:  clonePtr(in.Settings.ReconnectMaxDelayMs),
				Proxy:                c.proxy(in.Settings.Proxy),
				Unknown:              c.extra("settings.extra", in.Settings.Extra),
			},
			Unknown: c.extra("extra", in.Extra),
		}
	case projectfile.KindSocketIO:
		f = &projectfile.SocketIORequest{
			Head:        projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindSocketIO},
			ID:          in.ID,
			Name:        in.Name,
			Description: in.Description,
			URL:         in.URL,
			Params:      c.keyValues("params", in.Params),
			Headers:     c.keyValues("headers", in.Headers),
			Path:        in.Path,
			Namespace:   in.Namespace,
			AuthPayload: in.AuthPayload,
			Transport:   in.Transport,
			Version:     in.SocketIOVersion,
			Event:       in.Event,
			Args:        in.Message,
			Ack:         in.Ack,
			SavedEmits:  c.savedEmits(in.SavedMessages),
			Listen:      cloneStrings(in.Listen),
			Assertions:  c.assertions(in.Assertions),
			Auth:        c.handshakeAuth(in.Auth),
			AuthIn:      in.AuthIn,
			Settings: projectfile.SocketIOSettings{
				ConnectTimeoutMs: clonePtr(in.Settings.ConnectTimeoutMs),
				AckTimeoutMs:     clonePtr(in.Settings.AckTimeoutMs),
				VerifyTLS:        clonePtr(in.Settings.VerifyTLS),
				SendCookies:      clonePtr(in.Settings.SendCookies),
				Proxy:            c.proxy(in.Settings.Proxy),
				Unknown:          c.extra("settings.extra", in.Settings.Extra),
			},
			Unknown: c.extra("extra", in.Extra),
		}
	case projectfile.KindGRPC:
		f = &projectfile.GRPCRequest{
			Head:        projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindGRPC},
			ID:          in.ID,
			Name:        in.Name,
			Description: in.Description,
			Address:     in.Address,
			Proto:       c.grpcProto("proto", in.Proto),
			Service:     in.Service,
			Method:      in.Method,
			Message:     in.Message,
			Metadata:    c.keyValues("metadata", in.Metadata),
			Assertions:  c.assertions(in.Assertions),
			Auth:        c.handshakeAuth(in.Auth),
			Settings: projectfile.GRPCSettings{
				TLS:              in.Settings.TLS != nil && *in.Settings.TLS,
				VerifyTLS:        clonePtr(in.Settings.VerifyTLS),
				ConnectTimeoutMs: clonePtr(in.Settings.ConnectTimeoutMs),
				DeadlineMs:       clonePtr(in.Settings.DeadlineMs),
				Transport:        grpcDefault(in.Settings.Transport, projectfile.GRPCTransportGRPC),
				Compression:      grpcDefault(in.Settings.Compression, projectfile.GRPCCompressionIdentity),
				Proxy:            c.proxy(in.Settings.Proxy),
				Unknown:          c.extra("settings.extra", in.Settings.Extra),
			},
			Unknown: c.extra("extra", in.Extra),
		}
	default:
		return nil, errs.New(errs.InvalidArgument, "The request has a kind PostSonde cannot save.").
			WithCode(CodeInvalidRequest).
			WithHint(`Use "http", "graphql", "tcp", "udp", "websocket", "grpc", "sse" or "socketio".`).
			WithDetail("id", in.ID).
			WithDetail("field", "kind")
	}
	if c.err != nil {
		return nil, c.err
	}
	return f, nil
}

// converter maps DTO parts to file parts and keeps the first error.
type converter struct {
	id  string
	err error
}

func (c *converter) extra(field, text string) projectfile.Unknown {
	if text == "" || c.err != nil {
		return nil
	}
	var u projectfile.Unknown
	if err := json.Unmarshal([]byte(text), &u); err != nil || u == nil {
		c.err = errs.New(errs.InvalidArgument, "The request's extra members are not a JSON object.").
			WithCode(CodeInvalidRequest).
			WithHint("This is a bug in PostSonde; reopen the request.").
			WithDetail("id", c.id).
			WithDetail("field", field)
		return nil
	}
	if len(u) == 0 {
		return nil
	}
	return u
}

func (c *converter) keyValues(field string, rows []KeyValueDTO) []projectfile.KeyValue {
	if len(rows) == 0 {
		return nil
	}
	out := make([]projectfile.KeyValue, len(rows))
	for i, r := range rows {
		out[i] = projectfile.KeyValue{
			Name:        r.Name,
			Value:       r.Value,
			Description: r.Description,
			Disabled:    !r.Enabled,
			Unknown:     c.extra(field+"["+strconv.Itoa(i)+"].extra", r.Extra),
		}
	}
	return out
}

// savedMessages maps the saved messages of a websocket request; none is
// left out of the file.
func (c *converter) savedMessages(msgs []SavedMessageDTO) []projectfile.WebSocketMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]projectfile.WebSocketMessage, len(msgs))
	for i, m := range msgs {
		out[i] = projectfile.WebSocketMessage{
			Name:    m.Name,
			Text:    m.Text,
			Format:  m.Format,
			Unknown: c.extra("savedMessages["+strconv.Itoa(i)+"].extra", m.Extra),
		}
	}
	return out
}

// savedEmits maps the saved emits of a socketio request; none is left out
// of the file.
func (c *converter) savedEmits(msgs []SavedMessageDTO) []projectfile.SocketIOEmit {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]projectfile.SocketIOEmit, len(msgs))
	for i, m := range msgs {
		out[i] = projectfile.SocketIOEmit{
			Name:    m.Name,
			Event:   m.Event,
			Args:    m.Text,
			Ack:     m.Ack,
			Unknown: c.extra("savedMessages["+strconv.Itoa(i)+"].extra", m.Extra),
		}
	}
	return out
}

// cloneStrings copies a list; nil when it is empty, so the member is left
// out.
func cloneStrings(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return append([]string(nil), list...)
}

// assertions maps the assertions of a request; none is left out of the
// file.
func (c *converter) assertions(list []AssertionDTO) []projectfile.Assertion {
	if len(list) == 0 {
		return nil
	}
	out := make([]projectfile.Assertion, len(list))
	for i, a := range list {
		out[i] = projectfile.Assertion{
			Target:   a.Target,
			Expr:     a.Expr,
			Op:       a.Op,
			Expected: a.Expected,
			Messages: a.Messages,
			Index:    max(a.Index, 0),
			Disabled: !a.Enabled,
			Unknown:  c.extra("assertions["+strconv.Itoa(i)+"].extra", a.Extra),
		}
	}
	return out
}

// extractions maps the extraction rules of an http, graphql or sse
// request; none is left out of the file, and so is the runtime scope.
func (c *converter) extractions(list []ExtractionDTO) []projectfile.Extraction {
	if len(list) == 0 {
		return nil
	}
	out := make([]projectfile.Extraction, len(list))
	for i, e := range list {
		scope := e.Scope
		if scope == ScopeRuntime {
			scope = ""
		}
		out[i] = projectfile.Extraction{
			Source:   e.Source,
			Expr:     e.Expr,
			Variable: e.Variable,
			Scope:    scope,
			Secret:   e.Secret,
			Persist:  e.Persist,
			Disabled: !e.Enabled,
			Unknown:  c.extra("extractions["+strconv.Itoa(i)+"].extra", e.Extra),
		}
	}
	return out
}

// ScopeRuntime is ExtractionDTO.Scope of a rule that sets a runtime
// variable, the one a file leaves out.
const ScopeRuntime = "runtime"

// body builds a body; field names it in errors ("body",
// "bodyExamples[1].body").
func (c *converter) body(field string, b BodyDTO) projectfile.Body {
	return projectfile.Body{
		Mode:        b.Mode,
		Type:        b.Type,
		Text:        b.Text,
		Fields:      c.keyValues(field+".fields", b.Fields),
		Parts:       c.parts(field+".parts", b.Parts),
		Path:        b.Path,
		ContentType: b.ContentType,
		Unknown:     c.extra(field+".extra", b.Extra),
	}
}

// bodyExamples builds the body examples (HTTP-17). The active example's
// body is the request's body, so its own is not written; an inactive one
// with mode "" is written as mode "none".
func (c *converter) bodyExamples(list []BodyExampleDTO) []projectfile.BodyExample {
	if len(list) == 0 {
		return nil
	}
	out := make([]projectfile.BodyExample, len(list))
	for i, e := range list {
		field := "bodyExamples[" + strconv.Itoa(i) + "]"
		out[i] = projectfile.BodyExample{
			Name:    e.Name,
			Active:  e.Active,
			Unknown: c.extra(field+".extra", e.Extra),
		}
		if !e.Active {
			out[i].Body = c.body(field+".body", e.Body)
			if out[i].Body.Mode == "" {
				out[i].Body.Mode = projectfile.BodyNone
			}
		}
	}
	return out
}

// parts converts the parts of the list field ("body.parts", "files").
func (c *converter) parts(field string, parts []PartDTO) []projectfile.Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]projectfile.Part, len(parts))
	for i, p := range parts {
		out[i] = projectfile.Part{
			Name:        p.Name,
			Value:       p.Value,
			File:        p.File,
			ContentType: p.ContentType,
			Description: p.Description,
			Disabled:    !p.Enabled,
			Unknown:     c.extra(field+"["+strconv.Itoa(i)+"].extra", p.Extra),
		}
	}
	return out
}

func (c *converter) settings(s RequestSettingsDTO) projectfile.RequestSettings {
	return projectfile.RequestSettings{
		TimeoutMs:            clonePtr(s.TimeoutMs),
		FollowRedirects:      clonePtr(s.FollowRedirects),
		MaxRedirects:         clonePtr(s.MaxRedirects),
		FollowOriginalMethod: clonePtr(s.FollowOriginalMethod),
		ForwardAuthorization: clonePtr(s.ForwardAuthorization),
		VerifyTLS:            clonePtr(s.VerifyTLS),
		URLEncoding:          s.URLEncoding,
		HTTPVersion:          s.HTTPVersion,
		SendCookies:          clonePtr(s.SendCookies),
		StoreCookies:         clonePtr(s.StoreCookies),
		OmitHeaders:          omitHeaders(s.OmitHeaders),
		Reconnect:            c.reconnect(s.Reconnect),
		GraphQLTransport:     c.graphQLTransport(s.GraphQLTransport),
		GraphQLSchemaRefresh: c.graphQLSchemaRefresh(s.GraphQLSchemaRefresh),
		Proxy:                c.proxy(s.Proxy),
		Unknown:              c.extra("settings.extra", s.Extra),
	}
}

// reconnect builds an sse request's reconnection policy; nil for none.
func (c *converter) reconnect(r *SSEReconnectDTO) *projectfile.SSEReconnect {
	if r == nil {
		return nil
	}
	return &projectfile.SSEReconnect{
		Enabled:     clonePtr(r.Enabled),
		MaxAttempts: clonePtr(r.MaxAttempts),
		DelayMs:     clonePtr(r.DelayMs),
		Unknown:     c.extra("settings.reconnect.extra", r.Extra),
	}
}

// tcpFraming builds a tcp request's framing; a nil DTO is no framing.
func (c *converter) tcpFraming(f *TCPFramingDTO) projectfile.TCPFraming {
	if f == nil {
		return projectfile.TCPFraming{}
	}
	return projectfile.TCPFraming{
		Mode:            f.Mode,
		Delimiter:       f.Delimiter,
		PrefixBytes:     f.PrefixBytes,
		ByteOrder:       f.ByteOrder,
		MaxMessageBytes: clonePtr(f.MaxMessageBytes),
		Unknown:         c.extra("framing.extra", f.Extra),
	}
}

// trueOrNil is a pointer to true for true, nil for false: a flag stored
// as a missing member when off.
func trueOrNil(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}

// graphQLTransport builds a graphql request's transports; nil for none.
func (c *converter) graphQLTransport(t *GraphQLTransportDTO) *projectfile.GraphQLTransport {
	if t == nil {
		return nil
	}
	return &projectfile.GraphQLTransport{
		Query:        t.Query,
		Mutation:     t.Mutation,
		Subscription: t.Subscription,
		Unknown:      c.extra("settings.graphqlTransport.extra", t.Extra),
	}
}

// toGraphQLTransportDTO maps a graphql request's transports; nil for none.
func toGraphQLTransportDTO(t *projectfile.GraphQLTransport) *GraphQLTransportDTO {
	if t == nil {
		return nil
	}
	return &GraphQLTransportDTO{
		Query:        t.Query,
		Mutation:     t.Mutation,
		Subscription: t.Subscription,
		Extra:        extraText(t.Unknown),
	}
}

// graphQLSchemaRefresh builds a graphql request's schema auto-refresh;
// nil when the DTO is nil or everything is off and nothing unknown is
// kept, so the member is left out of the file.
func (c *converter) graphQLSchemaRefresh(r *GraphQLSchemaRefreshDTO) *projectfile.GraphQLSchemaRefresh {
	if r == nil {
		return nil
	}
	out := &projectfile.GraphQLSchemaRefresh{
		OnSend:          r.OnSend,
		IntervalMinutes: max(r.IntervalMinutes, 0),
		Unknown:         c.extra("settings.graphqlSchemaRefresh.extra", r.Extra),
	}
	if !out.OnSend && out.IntervalMinutes == 0 && len(out.Unknown) == 0 {
		return nil
	}
	return out
}

// toGraphQLSchemaRefreshDTO maps a schema auto-refresh; nil for none.
func toGraphQLSchemaRefreshDTO(r *projectfile.GraphQLSchemaRefresh) *GraphQLSchemaRefreshDTO {
	if r == nil {
		return nil
	}
	return &GraphQLSchemaRefreshDTO{
		OnSend:          r.OnSend,
		IntervalMinutes: r.IntervalMinutes,
		Extra:           extraText(r.Unknown),
	}
}

// toReconnectDTO maps a reconnection policy; nil for none.
func toReconnectDTO(r *projectfile.SSEReconnect) *SSEReconnectDTO {
	if r == nil {
		return nil
	}
	return &SSEReconnectDTO{
		Enabled:     clonePtr(r.Enabled),
		MaxAttempts: clonePtr(r.MaxAttempts),
		DelayMs:     clonePtr(r.DelayMs),
		Extra:       extraText(r.Unknown),
	}
}

func toKeyValueDTOs(rows []projectfile.KeyValue) []KeyValueDTO {
	out := make([]KeyValueDTO, len(rows))
	for i, r := range rows {
		out[i] = KeyValueDTO{
			Name:        r.Name,
			Value:       r.Value,
			Description: r.Description,
			Enabled:     !r.Disabled,
			Extra:       extraText(r.Unknown),
		}
	}
	return out
}

// toSavedMessageDTOs maps saved messages; nil when there are none, so the
// member is left out.
func toSavedMessageDTOs(msgs []projectfile.WebSocketMessage) []SavedMessageDTO {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]SavedMessageDTO, len(msgs))
	for i, m := range msgs {
		out[i] = SavedMessageDTO{Name: m.Name, Text: m.Text, Format: m.Format, Extra: extraText(m.Unknown)}
	}
	return out
}

// toSavedEmitDTOs maps the saved emits of a socketio request; nil when
// there are none, so the member is left out.
func toSavedEmitDTOs(list []projectfile.SocketIOEmit) []SavedMessageDTO {
	if len(list) == 0 {
		return nil
	}
	out := make([]SavedMessageDTO, len(list))
	for i, e := range list {
		out[i] = SavedMessageDTO{Name: e.Name, Text: e.Args, Event: e.Event, Ack: e.Ack, Extra: extraText(e.Unknown)}
	}
	return out
}

// toAssertionDTOs maps assertions; nil when there are none, so the member
// is left out.
func toAssertionDTOs(list []projectfile.Assertion) []AssertionDTO {
	if len(list) == 0 {
		return nil
	}
	out := make([]AssertionDTO, len(list))
	for i, a := range list {
		out[i] = AssertionDTO{
			Target:   a.Target,
			Expr:     a.Expr,
			Op:       a.Op,
			Expected: a.Expected,
			Messages: a.Messages,
			Index:    a.Index,
			Enabled:  !a.Disabled,
			Extra:    extraText(a.Unknown),
		}
	}
	return out
}

// toExtractionDTOs maps extraction rules; nil when there are none, so the
// member is left out. A rule without a scope has "runtime".
func toExtractionDTOs(list []projectfile.Extraction) []ExtractionDTO {
	if len(list) == 0 {
		return nil
	}
	out := make([]ExtractionDTO, len(list))
	for i, e := range list {
		scope := e.Scope
		if scope == "" {
			scope = ScopeRuntime
		}
		out[i] = ExtractionDTO{
			Source:   e.Source,
			Expr:     e.Expr,
			Variable: e.Variable,
			Scope:    scope,
			Secret:   e.Secret,
			Persist:  e.Persist,
			Enabled:  !e.Disabled,
			Extra:    extraText(e.Unknown),
		}
	}
	return out
}

// toBodyDTO maps a body; its lists are never null.
func toBodyDTO(b projectfile.Body) BodyDTO {
	return BodyDTO{
		Mode:   b.Mode,
		Type:   b.Type,
		Text:   b.Text,
		Fields: toKeyValueDTOs(b.Fields),
		Parts:  toPartDTOs(b.Parts),
		Path:   b.Path,
		// The file body's Content-Type override.
		ContentType: b.ContentType,
		Extra:       extraText(b.Unknown),
	}
}

// toBodyExampleDTOs maps the body examples (HTTP-17) so that exactly one
// is active when there are any: with none marked in the file, the
// request's body becomes an unnamed first example; with several, the
// first marked one is active and the others are inactive examples with
// the body they have (none when the file left it out). The active one's
// body is empty.
func toBodyExampleDTOs(list []projectfile.BodyExample) []BodyExampleDTO {
	out := make([]BodyExampleDTO, 0, len(list)+1)
	if len(list) == 0 {
		return out
	}
	if !slices.ContainsFunc(list, func(e projectfile.BodyExample) bool { return e.Active }) {
		out = append(out, BodyExampleDTO{Active: true, Body: toBodyDTO(projectfile.Body{})})
	}
	active := len(out) > 0
	for _, e := range list {
		dto := BodyExampleDTO{Name: e.Name, Extra: extraText(e.Unknown)}
		if e.Active && !active {
			active = true
			dto.Active = true
			dto.Body = toBodyDTO(projectfile.Body{})
		} else {
			dto.Body = toBodyDTO(e.Body)
			if dto.Body.Mode == "" {
				dto.Body.Mode = projectfile.BodyNone
			}
		}
		out = append(out, dto)
	}
	return out
}

func toPartDTOs(parts []projectfile.Part) []PartDTO {
	out := make([]PartDTO, len(parts))
	for i, p := range parts {
		out[i] = PartDTO{
			Name:        p.Name,
			Value:       p.Value,
			File:        p.File,
			ContentType: p.ContentType,
			Description: p.Description,
			Enabled:     !p.Disabled,
			Extra:       extraText(p.Unknown),
		}
	}
	return out
}

func toSettingsDTO(s projectfile.RequestSettings) RequestSettingsDTO {
	return RequestSettingsDTO{
		TimeoutMs:            clonePtr(s.TimeoutMs),
		FollowRedirects:      clonePtr(s.FollowRedirects),
		MaxRedirects:         clonePtr(s.MaxRedirects),
		FollowOriginalMethod: clonePtr(s.FollowOriginalMethod),
		ForwardAuthorization: clonePtr(s.ForwardAuthorization),
		VerifyTLS:            clonePtr(s.VerifyTLS),
		URLEncoding:          s.URLEncoding,
		HTTPVersion:          s.HTTPVersion,
		SendCookies:          clonePtr(s.SendCookies),
		StoreCookies:         clonePtr(s.StoreCookies),
		OmitHeaders:          omitHeaders(s.OmitHeaders),
		Reconnect:            toReconnectDTO(s.Reconnect),
		GraphQLTransport:     toGraphQLTransportDTO(s.GraphQLTransport),
		GraphQLSchemaRefresh: toGraphQLSchemaRefreshDTO(s.GraphQLSchemaRefresh),
		Proxy:                toProxyDTO(s.Proxy),
		Extra:                extraText(s.Unknown),
	}
}

// extraText encodes unknown members as a JSON object, keys sorted; "" when
// there are none.
func extraText(u projectfile.Unknown) string {
	if len(u) == 0 {
		return ""
	}
	data, err := json.Marshal(u)
	if err != nil {
		// Unknown holds valid JSON read from a file; this cannot happen.
		return ""
	}
	return string(data)
}

// clonePtr copies *p, so a DTO and a file never share a value.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// omitHeaders copies settings.omitHeaders without blank names; nil when
// none is left, so the member is left out.
func omitHeaders(names []string) []string {
	var out []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
