package projectfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func ptr[T any](v T) *T { return &v }

// runtimeHasUnixPerms is false where file modes are not Unix permissions.
var runtimeHasUnixPerms = runtime.GOOS != "windows"

// examples are the files docs/file-format.md shows, one per golden file in
// testdata/examples. They form one small project:
//
//	postsonde.json            workspace
//	health.json               http_minimal
//	import-catalogue.json     http_file
//	environments/production.json  environment
//	users/_folder.json        folder
//	users/create-user.json    http
//	users/upload-avatar.json  http_multipart
//	users/user-by-id.json     graphql
//	users/user-events.json    graphql_subscription
//	echo.json                 tcp
//	dns-probe.json            udp
//	live-feed.json            websocket
//	shop/_folder.json         folder_grpc
//	shop/get-order.json       grpc
//	mocks/_folder.json        folder_server_mock
//	chat/_folder.json         folder_server_websocket
//	prices/_folder.json       folder_server_sse
//	telemetry/_folder.json    folder_server_udp
//	orders-mock/_folder.json  folder_server_grpc_mock
//	live/_folder.json         folder_server_socketio
//	catalog-mock/_folder.json folder_server_graphql_mock
//	price-stream.json         sse
//	chat.json                 socketio
//	login.json                http_extractions
func examples() []struct {
	name string
	file File
} {
	return []struct {
		name string
		file File
	}{
		{"workspace", &Workspace{
			ID:          "ws_7c1e4b9a2f3d5e60",
			Name:        "Acme API",
			Description: "Requests for the Acme public API.",
			Order:       []string{"users", "health.json"},
			Variables: []Variable{
				{Name: "apiVersion", Value: "v2"},
			},
		}},
		{"folder", &Folder{
			ID:    "fld_0b8d2e61c4a9f375",
			Name:  "Users",
			Order: []string{"user-by-id.json", "create-user.json", "upload-avatar.json"},
			Variables: []Variable{
				{Name: "userId", Value: "42", Description: "A user that exists on staging and production"},
			},
			Headers: []KeyValue{
				{Name: "Accept", Value: "application/json"},
			},
			Auth: Auth{Type: AuthInherit},
		}},
		{"http_minimal", &HTTPRequest{
			ID:     "req_5a0f93c7e2b14d68",
			Name:   "Health check",
			Method: "GET",
			URL:    "{{baseUrl}}/health",
			Body:   Body{Mode: BodyNone},
			Auth:   Auth{Type: AuthInherit},
		}},
		{"http", &HTTPRequest{
			ID:          "req_c93e1a7b40f2d856",
			Name:        "Create user",
			Description: "Creates a user in an organisation. Needs the `users:write` scope.",
			Method:      "POST",
			URL:         "{{baseUrl}}/orgs/:org/users?notify=true",
			Params: []KeyValue{
				{Name: "notify", Value: "true"},
				{Name: "dryRun", Value: "true", Description: "Validate only", Disabled: true},
			},
			PathParams: []KeyValue{
				{Name: "org", Value: "acme"},
			},
			Headers: []KeyValue{
				{Name: "Content-Type", Value: "application/json"},
				{Name: "X-Request-Id", Value: "{{$uuid}}", Disabled: true},
			},
			Body: Body{
				Mode: BodyRaw,
				Type: RawJSON,
				Text: "{\n  \"name\": \"Zoë <admin>\",\n  \"email\": \"zoe@example.com\"\n}",
			},
			Auth: Auth{Type: AuthInherit},
			Settings: RequestSettings{
				TimeoutMs:       ptr(int64(30000)),
				FollowRedirects: ptr(false),
			},
			Assertions: []Assertion{
				{Target: "status", Op: "equals", Expected: "201"},
				{Target: "jsonpath", Expr: "$.id", Op: "is_type", Expected: "string"},
				{Target: "header", Expr: "Location", Op: "contains", Expected: "/users/"},
				{Target: "time", Op: "lt", Expected: "{{maxLatency}}", Disabled: true},
			},
		}},
		{"http_multipart", &HTTPRequest{
			ID:     "req_18d4b6f0a3c95e27",
			Name:   "Upload avatar",
			Method: "PUT",
			URL:    "{{baseUrl}}/users/{{userId}}/avatar",
			Body: Body{
				Mode: BodyMultipart,
				Parts: []Part{
					{Name: "caption", Value: "Profile photo"},
					{Name: "image", File: "fixtures/avatar.png", ContentType: "image/png"},
				},
			},
			Auth: Auth{Type: AuthNone},
		}},
		// Auth: a shared {{variable}} reference, then a typed secret
		// kept in local storage.
		{"http_bearer", &HTTPRequest{
			ID:     "req_3b9e0c5d71a4f286",
			Name:   "List orders",
			Method: "GET",
			URL:    "{{baseUrl}}/orders",
			Body:   Body{Mode: BodyNone},
			Auth:   Auth{Type: AuthBearer, Prefix: ptr("Token"), Token: "{{token}}"},
		}},
		{"http_apikey", &HTTPRequest{
			ID:     "req_7d2a64e9b0c1f358",
			Name:   "Weather",
			Method: "GET",
			URL:    "https://weather.example/v1/today",
			Body:   Body{Mode: BodyNone},
			Auth:   Auth{Type: AuthAPIKey, Key: "api_key", In: APIKeyInQuery, LocalSecrets: []string{"value"}},
		}},
		{"http_file", &HTTPRequest{
			ID:     "req_7b2e9c41d0a8f365",
			Name:   "Import catalogue",
			Method: "POST",
			URL:    "{{baseUrl}}/catalogue/import",
			Body: Body{
				Mode:        BodyFile,
				Path:        "fixtures/catalogue.csv",
				ContentType: "text/csv; charset=utf-8",
			},
			Auth: Auth{Type: AuthInherit},
		}},
		// Named body examples (HTTP-17) on a SOAP 1.1 call (HTTP-18): the
		// active example's body is the request's body.
		{"http_body_examples", &HTTPRequest{
			ID:     "req_2e6c9a0d4b7f1358",
			Name:   "Get quote",
			Method: "POST",
			URL:    "{{baseUrl}}/StockQuote",
			Headers: []KeyValue{
				{Name: "Content-Type", Value: "text/xml; charset=utf-8"},
				{Name: "SOAPAction", Value: `"urn:example:GetQuote"`},
			},
			Body: Body{
				Mode: BodyRaw,
				Type: RawXML,
				Text: "<soap:Envelope xmlns:soap=\"http://schemas.xmlsoap.org/soap/envelope/\">\n  <soap:Body>\n    <m:GetQuote xmlns:m=\"urn:example\"><m:Symbol>ACME</m:Symbol></m:GetQuote>\n  </soap:Body>\n</soap:Envelope>",
			},
			BodyExamples: []BodyExample{
				{Name: "Empty", Body: Body{Mode: BodyNone}},
				{Name: "ACME", Active: true},
			},
			Auth: Auth{Type: AuthNone},
		}},
		// Response examples (HTTP-38): a response saved from the history
		// with the request that got it, secrets masked.
		{"http_response_examples", &HTTPRequest{
			ID:     "req_4d1b7e9a0c6f2835",
			Name:   "Get user",
			Method: "GET",
			URL:    "{{baseUrl}}/users/:id",
			PathParams: []KeyValue{
				{Name: "id", Value: "{{userId}}"},
			},
			Body: Body{Mode: BodyNone},
			Auth: Auth{Type: AuthInherit},
			ResponseExamples: []ResponseExample{
				{
					Name:    "200 OK",
					SavedAt: "2026-09-23T10:15:02Z",
					Request: &ExampleRequest{
						Method: "GET",
						URL:    "https://api.acme.example/users/42",
						Headers: []KeyValue{
							{Name: "Host", Value: "api.acme.example"},
							{Name: "Accept", Value: "application/json"},
							{Name: "Authorization", Value: "Bearer ••••••"},
						},
					},
					StatusCode: 200,
					StatusText: "OK",
					Headers: []KeyValue{
						{Name: "Content-Type", Value: "application/json"},
						{Name: "Etag", Value: `"u42-7"`},
					},
					Body: "{\"id\":\"42\",\"name\":\"Zoë\"}",
				},
				{
					Name:       "Not found",
					SavedAt:    "2026-09-23T10:16:40Z",
					StatusCode: 404,
					StatusText: "Not Found",
					Headers: []KeyValue{
						{Name: "Content-Type", Value: "application/problem+json"},
					},
					Body: "{\"title\":\"No such user\"}",
				},
			},
		}},
		// Extraction into variables (TEST-05): the login's token and
		// session go to variables the next requests use; only the rules
		// are stored, never the values.
		{"http_extractions", &HTTPRequest{
			ID:      "req_9b3e6a1d07c4f528",
			Name:    "Log in",
			Method:  "POST",
			URL:     "{{baseUrl}}/login",
			Headers: []KeyValue{{Name: "Content-Type", Value: "application/json"}},
			Body:    Body{Mode: BodyRaw, Type: "json", Text: "{\"user\": \"{{user}}\", \"password\": \"{{password}}\"}"},
			Auth:    Auth{Type: AuthNone},
			Assertions: []Assertion{
				{Target: "status", Op: "equals", Expected: "200"},
			},
			Extractions: []Extraction{
				{Source: "jsonpath", Expr: "$.access_token", Variable: "token", Secret: true},
				{Source: "jsonpath", Expr: "$.user.id", Variable: "userId", Persist: true},
				{Source: "cookie", Expr: "session", Variable: "session", Secret: true},
				{Source: "header", Expr: "X-Request-Id", Variable: "loginRequestId", Scope: "run"},
				{Source: "regex", Expr: "\"expires_in\":(\\d+)", Variable: "expiresIn", Disabled: true},
			},
		}},
		{"graphql", &GraphQLRequest{
			ID:            "req_e4a7c2019b5d83f6",
			Name:          "User by ID",
			URL:           "{{baseUrl}}/graphql",
			Query:         "query UserById($id: ID!) {\n  user(id: $id) {\n    id\n    name\n  }\n}",
			Variables:     "{\n  \"id\": \"{{userId}}\"\n}",
			OperationName: "UserById",
			Auth:          Auth{Type: AuthInherit},
		}},
		{"graphql_subscription", &GraphQLRequest{
			ID:               "req_9c0d4e2b7a15f863",
			Name:             "User events",
			URL:              "{{baseUrl}}/graphql",
			Query:            "subscription UserEvents($id: ID!) {\n  userEvents(id: $id) {\n    type\n    at\n  }\n}",
			Variables:        "{\n  \"id\": \"{{userId}}\"\n}",
			ConnectionParams: "{\n  \"authToken\": \"{{token}}\"\n}",
			Auth:             Auth{Type: AuthInherit},
		}},
		// GQL-35: queries over WebSocket, subscriptions over SSE.
		{"graphql_transport", &GraphQLRequest{
			ID:    "req_8a3f1c6e0b92d547",
			Name:  "Ticker",
			URL:   "{{baseUrl}}/graphql",
			Query: "subscription Ticker {\n  ticker {\n    symbol\n    price\n  }\n}",
			Auth:  Auth{Type: AuthInherit},
			Settings: RequestSettings{
				GraphQLTransport: &GraphQLTransport{
					Query:        GraphQLTransportWS,
					Subscription: GraphQLTransportSSE,
				},
			},
		}},
		// GQL-19: the opt-in schema auto-refresh.
		{"graphql_schema_refresh", &GraphQLRequest{
			ID:    "req_3d7f2a9c5e1b8046",
			Name:  "Catalogue",
			URL:   "{{baseUrl}}/graphql",
			Query: "{\n  products {\n    id\n  }\n}",
			Auth:  Auth{Type: AuthInherit},
			Settings: RequestSettings{
				GraphQLSchemaRefresh: &GraphQLSchemaRefresh{OnSend: true, IntervalMinutes: 30},
			},
		}},
		{"graphql_schema_file", &GraphQLRequest{
			ID:         "req_5b8e1d3c9f07a246",
			Name:       "Products offline",
			URL:        "{{baseUrl}}/graphql",
			Query:      "{\n  products {\n    id\n  }\n}",
			SchemaFile: "schemas/catalogue.graphql",
			Auth:       Auth{Type: AuthInherit},
		}},
		// GQL-06, GQL-07: extensions and files bound to variable paths.
		{"graphql_upload", &GraphQLRequest{
			ID:         "req_2f6a9c1e4b7d0835",
			Name:       "Upload avatar",
			URL:        "{{baseUrl}}/graphql",
			Query:      "mutation Upload($file: Upload!, $docs: [Upload!]!) {\n  upload(file: $file, docs: $docs)\n}",
			Variables:  "{\n  \"docs\": [null, null]\n}",
			Extensions: "{\"persistedQuery\": {\"version\": 1, \"sha256Hash\": \"{{hash}}\"}}",
			Files: []Part{
				{Name: "variables.file", File: "fixtures/avatar.png"},
				{Name: "variables.docs.0", File: "fixtures/a.pdf"},
				{Name: "variables.docs.1", File: "/Users/me/Documents/b.pdf", ContentType: "application/pdf"},
			},
			Auth: Auth{Type: AuthInherit},
		}},
		{"udp", &UDPRequest{
			ID:       "req_5c8e1a7f2b904d63",
			Name:     "DNS probe",
			Address:  "{{dnsHost}}:53",
			SendMode: SendHex,
			Message:  "12 34 01 00 00 01 00 00 00 00 00 00 07 65 78 61 6d 70 6c 65 03 63 6f 6d 00 00 01 00 01",
			Settings: UDPSettings{
				Connected:        true,
				IPVersion:        IPVersion4,
				MaxDatagramBytes: ptr(int64(1500)),
				OpenTimeoutMs:    ptr(int64(5000)),
			},
		}},
		{"http_proxy", &HTTPRequest{
			ID:     "req_2f6a0c8e4b1d9573",
			Name:   "Internal status",
			Method: "GET",
			URL:    "https://status.internal.example/v1",
			Body:   Body{Mode: BodyNone},
			Auth:   Auth{Type: AuthInherit},
			Settings: RequestSettings{
				Proxy: RequestProxy{
					Mode:         ProxyCustom,
					URL:          "socks5h://bob@jump.example.net:1080",
					LocalSecrets: []string{ProxyPasswordMember},
				},
			},
		}},
		{"tcp", &TCPRequest{
			ID:         "req_7d2b5e90c41a6f38",
			Name:       "Echo server",
			Address:    "{{echoHost}}:7000",
			SendMode:   SendHex,
			LineEnding: LineEndingCRLF,
			Message:    "DE AD be ef 0x0a",
			Framing: TCPFraming{
				Mode:            FramingLength,
				PrefixBytes:     2,
				ByteOrder:       "little",
				MaxMessageBytes: ptr(int64(65536)),
			},
			Settings: TCPSettings{
				ConnectTimeoutMs: ptr(int64(5000)),
				TLS:              true,
				VerifyTLS:        ptr(false),
			},
		}},
		{"websocket", &WebSocketRequest{
			ID:   "req_4c9e1a7b20d85f36",
			Name: "Live feed",
			URL:  "wss://{{host}}/feed?room=lobby",
			Params: []KeyValue{
				{Name: "room", Value: "lobby"},
				{Name: "debug", Value: "1", Disabled: true},
			},
			Headers: []KeyValue{
				{Name: "Origin", Value: "https://app.example.com"},
			},
			Subprotocols:  []string{"graphql-transport-ws", "v2.feed"},
			Message:       "{\"type\": \"subscribe\", \"channel\": \"prices\"}",
			MessageFormat: MessageJSON,
			SavedMessages: []WebSocketMessage{
				{Name: "Subscribe", Text: "{\"type\": \"subscribe\", \"token\": \"{{token}}\"}", Format: MessageJSON},
				{Name: "Ping", Text: "ping"},
				{Name: "Hello bytes", Text: "de ad be ef", Format: MessageHex},
				{Name: "Handshake blob", Text: "AAECAw==", Format: MessageBase64},
			},
			Settings: WebSocketSettings{
				ConnectTimeoutMs:     ptr(int64(10000)),
				SendCookies:          ptr(false),
				PingIntervalMs:       ptr(int64(30000)),
				PongTimeoutMs:        ptr(int64(5000)),
				AutoReconnect:        ptr(true),
				ReconnectMaxAttempts: ptr(10),
				ReconnectDelayMs:     ptr(int64(500)),
				ReconnectMaxDelayMs:  ptr(int64(60000)),
			},
		}},
		{"folder_grpc", &Folder{
			ID:   "fld_6e2a9c0d41b7f538",
			Name: "Shop",
			Auth: Auth{Type: AuthInherit},
			GRPC: FolderGRPC{Proto: GRPCProto{
				Files:       []string{"proto/shop/v1/orders.proto"},
				ImportPaths: []string{"proto", "/usr/local/include"},
			}},
		}},
		{"folder_server_mock", &Folder{
			ID:   "fld_93d0b7a2e5c14f68",
			Name: "Mocks",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "mock_http",
				Name: "Users API mock",
				Port: 8080,
				Mock: &ServerMock{Routes: []MockRoute{
					{
						ID:      "rt_4f1a9c2e7b30",
						Method:  "GET",
						Path:    "/users/:id",
						Status:  200,
						Headers: []KeyValue{{Name: "X-Mock", Value: "true"}},
						Body:    Body{Mode: BodyRaw, Type: RawJSON, Text: "{\"id\": 42, \"name\": \"Zoë\"}"},
						Responses: []MockResponse{
							{
								Name:   "admin",
								Status: 200,
								Body:   Body{Mode: BodyRaw, Type: RawJSON, Text: "{\"id\": 1, \"role\": \"admin\"}"},
								Rules:  []MockRule{{Source: "query", Name: "role", Op: "equals", Value: "admin"}, {Source: "header", Name: "X-Debug", Op: "exists"}},
							},
							{Name: "not-found", Status: 404, Body: Body{Mode: BodyNone}},
						},
					},
					{
						ID:     "rt_b62d08e1f5a9",
						Method: "POST",
						Path:   "/users",
						Status: 503,
						Body:   Body{Mode: BodyNone},
						Delay:  MockDelay{Mode: "random", MinMs: 100, MaxMs: 900},
					},
				}},
			},
		}},
		{"folder_server_websocket", &Folder{
			ID:   "fld_2c7e5a91d0b3f846",
			Name: "Chat",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "websocket",
				Host: "localhost",
				Port: 8081,
				WebSocket: &ServerWebSocket{
					Path:         "/ws",
					Subprotocols: []string{"chat"},
					Echo:         true,
					Rules: []ServerWSRule{
						{Match: "exact", Pattern: "ping", Reply: "pong"},
						{Match: "regex", Pattern: `^get (?P<id>\d+)$`, Reply: `{"id": ${id}, "name": "Zoë"}`, DelayMs: 200},
						{Match: "contains", Pattern: "debug", Reply: "debug is off", Disabled: true},
					},
				},
			},
		}},
		{"folder_server_sse", &Folder{
			ID:   "fld_5b8e1d3a7c204f96",
			Name: "Prices",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "sse",
				Port: 8082,
				SSE: &ServerSSE{
					Path:         "/events",
					AllowOrigin:  "*",
					KeepAliveSec: 15,
					RetryMs:      3000,
					AutoID:       true,
					Timers: []ServerSSETimer{
						{Event: "price", Data: "{\"symbol\": \"ACME\", \"at\": {{$timestamp}}}", IntervalMs: 1000},
						{Event: "heartbeat", Data: "{{$isoTimestamp}}", IntervalMs: 30000, Disabled: true},
					},
				},
			},
		}},
		{"folder_server_udp", &Folder{
			ID:   "fld_e7a2c9046d1b3f85",
			Name: "Telemetry",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "udp",
				Port: 9125,
				UDP:  &ServerUDP{Echo: true, FixedReply: true, Reply: "06", ReplyFormat: "hex"},
			},
		}},
		{"folder_server_grpc_mock", &Folder{
			ID:   "fld_3c9a1e7b5d20f846",
			Name: "Orders mock",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "grpc_mock",
				Port: 50051,
				GRPCMock: &ServerGRPCMock{
					Files:       []string{"protos/shop/v1/orders.proto"},
					ImportPaths: []string{"protos"},
					Methods: []ServerGRPCMethod{
						{
							Service:  "shop.v1.Orders",
							Method:   "GetOrder",
							Messages: []string{"{\n  \"id\": \"o-1\",\n  \"total\": {\"currency\": \"EUR\", \"units\": \"42\"}\n}"},
							Headers:  []KeyValue{{Name: "x-mock", Value: "postsonde"}},
							DelayMs:  250,
						},
						{
							Service:    "shop.v1.Orders",
							Method:     "ListOrders",
							Messages:   []string{"{\"id\": \"o-1\"}", "{\"id\": \"o-2\"}"},
							IntervalMs: 500,
						},
						{
							Service:       "shop.v1.Orders",
							Method:        "CancelOrder",
							Status:        5,
							StatusMessage: "no such order",
							Trailers:      []KeyValue{{Name: "x-trace-bin", Value: "AAEC"}},
						},
						{Service: "shop.v1.Orders", Method: "Chat", Bidi: "echo"},
					},
				},
			},
		}},
		{"folder_server_socketio", &Folder{
			ID:   "fld_8d2f6a0c4e1b9537",
			Name: "Live",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "socketio",
				Port: 3000,
				SocketIO: &ServerSocketIO{
					Namespaces:  []string{"/admin", "/chat"},
					Auth:        `{"token":"dev-token"}`,
					AllowOrigin: "http://localhost:5173",
					AckMode:     "none",
					Echo:        true,
					Rules: []ServerSIORule{
						{Match: "exact", Pattern: "ping", Ack: `["pong", {"at": {{$timestamp}}}]`},
						{Match: "regex", Pattern: `^order:`, Emit: "order:updated", EmitArgs: `{"status": "shipped"}`, DelayMs: 500},
						{Match: "contains", Pattern: "debug", Ack: `"off"`, Disabled: true},
					},
				},
			},
		}},
		{"folder_server_graphql_mock", &Folder{
			ID:   "fld_3a9c5e1f7b2d4068",
			Name: "Catalog mock",
			Auth: Auth{Type: AuthInherit},
			Server: &FolderServer{
				Type: "graphql_mock",
				Port: 4000,
				GraphQLMock: &ServerGraphQLMock{
					SchemaFile:  "schema/catalog.graphql",
					ListLength:  new(5),
					NullPercent: 10,
					Seed:        42,
					AllowOrigin: "*",
					Overrides: []ServerGraphQLOverride{
						{Target: "Query.product", Args: `{"id": "1"}`, Value: "{\n  \"id\": \"1\",\n  \"name\": \"Teapot\"\n}"},
						{Target: "Query.product", Args: `{"id": "404"}`, Mode: "error", Error: "Product not found.", Code: "NOT_FOUND"},
						{Target: "DateTime", Value: `"2026-01-01T00:00:00Z"`, Disabled: true},
					},
				},
			},
		}},
		{"grpc", &GRPCRequest{
			ID:      "req_7b3f0e2c9a1d5846",
			Name:    "Get order",
			Address: "{{grpcHost}}:50051",
			Proto:   GRPCProto{Inherit: true},
			Service: "shop.v1.Orders",
			Method:  "GetOrder",
			Message: "{\"id\": \"{{orderId}}\"}",
			Metadata: []KeyValue{
				{Name: "x-request-id", Value: "{{$uuid}}"},
				{Name: "x-debug", Value: "1", Disabled: true},
			},
			Settings: GRPCSettings{
				TLS:        true,
				DeadlineMs: ptr(int64(5000)),
			},
		}},
		{"sse", &SSERequest{
			ID:   "req_2d8f5a1c7e03b964",
			Name: "Price stream",
			URL:  "{{baseUrl}}/prices/stream?symbols=ACME",
			Params: []KeyValue{
				{Name: "symbols", Value: "ACME"},
			},
			Headers: []KeyValue{
				{Name: "Accept", Value: "text/event-stream"},
			},
			Auth: Auth{Type: AuthInherit},
			Settings: RequestSettings{
				TimeoutMs: ptr(int64(10000)),
				Reconnect: &SSEReconnect{Enabled: ptr(true), MaxAttempts: ptr(10)},
			},
		}},
		{"socketio", &SocketIORequest{
			ID:   "req_6b1d0e4f9a2c8d37",
			Name: "Chat",
			URL:  "{{baseUrl}}/chat?room=lobby",
			Params: []KeyValue{
				{Name: "room", Value: "lobby"},
			},
			Headers: []KeyValue{
				{Name: "X-Client", Value: "postsonde"},
			},
			Path:        "/realtime/",
			AuthPayload: `{"token": "{{chatToken}}"}`,
			Transport:   SocketIOUpgrade,
			Event:       "message",
			Args:        `["hello", {"to": "all"}]`,
			Ack:         true,
			SavedEmits: []SocketIOEmit{
				{Name: "Join lobby", Event: "join", Args: `["lobby"]`, Ack: true},
				{Name: "Avatar", Event: "upload", Args: `[{"$base64": "iVBORw0KGgo="}]`},
			},
			Listen: []string{"message", "typing"},
			Settings: SocketIOSettings{
				AckTimeoutMs: ptr(int64(5000)),
			},
		}},
		{"environment", &Environment{
			ID:    "env_92f0d5b8a61c3e47",
			Name:  "Production",
			Color: "#d0342c",
			Variables: []Variable{
				{Name: "baseUrl", Value: "https://api.acme.example"},
				{Name: "token", Secret: true, Description: "Personal access token"},
			},
		}},
	}
}

// TestExamples checks the example files against testdata/examples/* and
// that each one survives a read/write cycle byte for byte. Regenerate with:
//
//	go test ./internal/core/storage/projectfile -run TestExamples -update
func TestExamples(t *testing.T) {
	for _, tt := range examples() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := reflect.ValueOf(tt.file).Elem().Interface()
			got, err := Marshal(tt.file)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if after := reflect.ValueOf(tt.file).Elem().Interface(); !reflect.DeepEqual(before, after) {
				t.Errorf("Marshal modified its argument")
			}
			golden.Assert(t, "examples/"+tt.name, got)

			// Read, then write: identical bytes, and the value that was
			// written, with its head filled in.
			decoded, err := Decode(got)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			want := reflect.New(reflect.TypeOf(tt.file).Elem())
			want.Elem().Set(reflect.ValueOf(tt.file).Elem())
			setHead(want.Interface().(File), Head{SchemaVersion: CurrentVersion, Kind: tt.file.FileKind()})
			if !reflect.DeepEqual(decoded, want.Interface()) {
				t.Errorf("Decode =\n%+v\nwant\n%+v", decoded, want.Interface())
			}
			again, err := Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal after Decode: %v", err)
			}
			if !bytes.Equal(again, got) {
				t.Errorf("round trip changed the file:\nfirst:\n%s\nagain:\n%s", got, again)
			}
		})
	}
}

// TestExamplesDocumented keeps docs/file-format.md in step with the code:
// every example file must appear in it verbatim.
func TestExamplesDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "file-format.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range examples() {
		example, err := os.ReadFile(golden.Path("examples/" + tt.name))
		if err != nil {
			t.Fatal(err)
		}
		block := "```json\n" + string(example) + "```\n"
		if !bytes.Contains(doc, []byte(block)) {
			t.Errorf("docs/file-format.md does not show %s as a json block; copy it from testdata/examples", golden.Path("examples/"+tt.name))
		}
	}
}

// TestUnknownMembers loads a hand-edited file with members this version
// does not know at every level, including a body mode and an auth type it
// does not know. The first save normalizes the file (known members in
// schema order, unknown ones after them sorted, defaults left out); after
// that, read/write cycles are byte-identical.
//
//	go test ./internal/core/storage/projectfile -run TestUnknownMembers -update
//
// TestExtractionsRoundTrip (TEST-05): extraction rules of http, graphql
// and sse requests load and save unchanged, a source and scope this
// version does not know and an unknown member of a rule included.
func TestExtractionsRoundTrip(t *testing.T) {
	for _, kind := range []string{"http", "graphql", "sse"} {
		t.Run(kind, func(t *testing.T) {
			input := `{"schemaVersion": 1, "kind": "` + kind + `", "id": "r", "name": "R", "method": "GET", "url": "u", "query": "{ a }", "auth": {"type": "none"},
				"extractions": [
					{"source": "jsonpath", "expr": "$.token", "variable": "token", "secret": true, "persist": true},
					{"source": "status", "variable": "code", "scope": "run", "disabled": true},
					{"source": "grpc-trailer", "expr": "x", "variable": "later", "scope": "suite", "zNote": {"kept": [1]}}
				]}`
			f, err := Decode([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			var list []Extraction
			switch r := f.(type) {
			case *HTTPRequest:
				list = r.Extractions
			case *GraphQLRequest:
				list = r.Extractions
			case *SSERequest:
				list = r.Extractions
			}
			want := []Extraction{
				{Source: "jsonpath", Expr: "$.token", Variable: "token", Secret: true, Persist: true},
				{Source: "status", Variable: "code", Scope: "run", Disabled: true},
				{Source: "grpc-trailer", Expr: "x", Variable: "later", Scope: "suite"},
			}
			if len(list) != len(want) {
				t.Fatalf("extractions = %+v", list)
			}
			for i := range want {
				got := list[i]
				got.Unknown = nil
				if !reflect.DeepEqual(got, want[i]) {
					t.Errorf("extraction %d = %+v", i, list[i])
				}
			}
			if note := string(list[2].Unknown["zNote"]); strings.ReplaceAll(note, " ", "") != `{"kept":[1]}` || len(list[0].Unknown) != 0 {
				t.Errorf("unknown members = %v, %v", list[0].Unknown, list[2].Unknown)
			}
			out, err := Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Decode(out)
			if err != nil {
				t.Fatal(err)
			}
			if out2, _ := Marshal(again); !bytes.Equal(out, out2) {
				t.Errorf("not stable:\n%s\n%s", out, out2)
			}
			if !strings.Contains(string(out), `"zNote"`) || !strings.Contains(string(out), `"scope": "suite"`) {
				t.Errorf("lost members:\n%s", out)
			}
		})
	}
}

func TestUnknownMembers(t *testing.T) {
	input := []byte(`{
	"zFuture": {"b": 1, "a": [true]},
	"kind": "http", "schemaVersion": 1.0,
	"name": "Hand edited", "id": "req_1",
	"method": "GET", "url": "https://example.com/?q=<a&b>",
	"headers": [{"name": "Accept", "value": "*/*", "disabled": false, "origin": "import"}],
	"params": [],
	"body": {"mode": "graphql-multipart", "operations": {"query": "{ a }"}, "map": {}},
	"auth": {"type": "edgegrid", "authId": "api", "username": "zoë", "prefix": "Bearer", "disabled": false},
	"settings": {"timeoutMs": 0, "retries": 3},
	"aNote": "kept"
}`)

	save := func(data []byte) []byte {
		t.Helper()
		f, err := Decode(data)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		out, err := Marshal(f)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return out
	}

	first := save(input)
	golden.Assert(t, "unknown_members", first)
	for range 3 {
		if again := save(first); !bytes.Equal(again, first) {
			t.Fatalf("round trip not stable:\nfirst:\n%s\nagain:\n%s", first, again)
		}
	}
}

func TestDecodeKinds(t *testing.T) {
	tests := []struct {
		data string
		want File
	}{
		{`{"kind":"workspace","id":"w"}`, &Workspace{}},
		{`{"kind":"folder","id":"f"}`, &Folder{}},
		{`{"kind":"http","id":"r"}`, &HTTPRequest{}},
		{`{"kind":"graphql","id":"g"}`, &GraphQLRequest{}},
		{`{"kind":"tcp","id":"t"}`, &TCPRequest{}},
		{`{"kind":"udp","id":"u"}`, &UDPRequest{}},
		{`{"kind":"websocket","id":"w"}`, &WebSocketRequest{}},
		{`{"kind":"grpc","id":"g"}`, &GRPCRequest{}},
		{`{"kind":"sse","id":"s"}`, &SSERequest{}},
		{`{"kind":"socketio","id":"s"}`, &SocketIORequest{}},
		{`{"kind":"environment","id":"e"}`, &Environment{}},
	}
	for _, tt := range tests {
		t.Run(string(tt.want.FileKind()), func(t *testing.T) {
			t.Parallel()
			f, err := Decode([]byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(f) != reflect.TypeOf(tt.want) {
				t.Fatalf("Decode = %T, want %T", f, tt.want)
			}
			// A missing schemaVersion counts as version 1.
			if h := f.head(); h.SchemaVersion != CurrentVersion || h.Kind != tt.want.FileKind() {
				t.Errorf("head = %+v", h)
			}
		})
	}
}

// wantErr is the part of an *errs.Error the tests check.
type wantErr struct {
	kind    errs.Kind
	code    string
	details map[string]string
}

func checkErr(t *testing.T, err error, want wantErr) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v (%T), want an *errs.Error", err, err)
	}
	if e.Kind != want.kind || e.Code != want.code {
		t.Errorf("kind, code = %q, %q; want %q, %q (%v)", e.Kind, e.Code, want.kind, want.code, err)
	}
	if e.Hint == "" {
		t.Errorf("no hint: %v", err)
	}
	for k, v := range want.details {
		if e.Details[k] != v {
			t.Errorf("details[%q] = %q, want %q", k, e.Details[k], v)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	parse := wantErr{kind: errs.Parse, code: CodeParseFailed}
	tests := []struct {
		name   string
		data   string
		decode func([]byte) error
		want   wantErr
	}{
		{name: "empty", data: ``, want: parse},
		{name: "syntax error", data: "{\n  \"kind\": \"http\",\n  oops\n}", want: wantErr{kind: errs.Parse, code: CodeParseFailed, details: map[string]string{"line": "3", "column": "3"}}},
		{name: "array", data: `[1]`, want: parse},
		{name: "null", data: `null`, want: parse},
		{name: "no kind", data: `{"schemaVersion":1,"id":"x"}`, want: wantErr{kind: errs.Parse, code: CodeParseFailed, details: map[string]string{"field": "kind"}}},
		{name: "empty kind", data: `{"kind":""}`, want: parse},
		{name: "kind not a string", data: `{"kind":1}`, want: parse},
		{name: "version zero", data: `{"schemaVersion":0,"kind":"http"}`, want: wantErr{kind: errs.Parse, code: CodeParseFailed, details: map[string]string{"field": "schemaVersion"}}},
		{name: "version fraction", data: `{"schemaVersion":1.5,"kind":"http"}`, want: parse},
		{name: "version string", data: `{"schemaVersion":"1","kind":"http"}`, want: parse},
		{name: "version huge", data: `{"schemaVersion":1e300,"kind":"http"}`, want: parse},
		{name: "member of wrong type", data: `{"kind":"http","headers":"Accept: */*"}`, want: parse},
		{name: "nested member of wrong type", data: `{"kind":"http","body":{"parts":[{"name":1}]}}`, want: parse},
		{name: "unknown kind", data: `{"schemaVersion":1,"kind":"mqtt","id":"x"}`, want: wantErr{kind: errs.Unsupported, code: CodeUnknownKind, details: map[string]string{"kind": "mqtt"}}},
		{
			name:   "wrong kind",
			data:   `{"kind":"http","id":"x"}`,
			decode: func(b []byte) error { _, err := DecodeWorkspace(b); return err },
			want:   wantErr{kind: errs.Parse, code: CodeWrongKind, details: map[string]string{"kind": "http", "expected": "workspace"}},
		},
		{
			name:   "folder expected",
			data:   `{"kind":"environment","id":"x"}`,
			decode: func(b []byte) error { _, err := DecodeFolder(b); return err },
			want:   wantErr{kind: errs.Parse, code: CodeWrongKind, details: map[string]string{"expected": "folder"}},
		},
		{
			name:   "environment expected",
			data:   `{"kind":"folder","id":"x"}`,
			decode: func(b []byte) error { _, err := DecodeEnvironment(b); return err },
			want:   wantErr{kind: errs.Parse, code: CodeWrongKind, details: map[string]string{"expected": "environment"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decode := tt.decode
			if decode == nil {
				decode = func(b []byte) error { _, err := Decode(b); return err }
			}
			err := decode([]byte(tt.data))
			if err == nil {
				t.Fatal("no error")
			}
			checkErr(t, err, tt.want)
		})
	}
}

func TestTypedDecoders(t *testing.T) {
	t.Parallel()
	if w, err := DecodeWorkspace([]byte(`{"kind":"workspace","id":"w","name":"W"}`)); err != nil || w.Name != "W" {
		t.Errorf("DecodeWorkspace = %+v, %v", w, err)
	}
	if f, err := DecodeFolder([]byte(`{"kind":"folder","id":"f","name":"F"}`)); err != nil || f.Name != "F" {
		t.Errorf("DecodeFolder = %+v, %v", f, err)
	}
	if e, err := DecodeEnvironment([]byte(`{"kind":"environment","id":"e","name":"E"}`)); err != nil || e.Name != "E" {
		t.Errorf("DecodeEnvironment = %+v, %v", e, err)
	}
}

func TestMigrations(t *testing.T) {
	// A made-up history: version 1 called HTTP requests "rest" and the URL
	// "address"; version 2 renamed both; version 3 moved "notes" into
	// "description".
	steps := map[int]migration{
		1: func(kind Kind, m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
			if kind == "rest" {
				m["kind"] = json.RawMessage(`"http"`)
				m["url"] = m["address"]
				delete(m, "address")
			}
			return m, nil
		},
		2: func(kind Kind, m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
			if kind != KindHTTP {
				return nil, errors.New("step 2 saw the kind from before step 1")
			}
			if notes, ok := m["notes"]; ok {
				m["description"] = notes
				delete(m, "notes")
			}
			return m, nil
		},
	}

	t.Run("upgrades step by step", func(t *testing.T) {
		t.Parallel()
		f, err := decode([]byte(`{"schemaVersion":1,"kind":"rest","id":"r","method":"GET","address":"https://x","notes":"n","extra":true}`), nil, steps, 3)
		if err != nil {
			t.Fatal(err)
		}
		r, ok := f.(*HTTPRequest)
		if !ok {
			t.Fatalf("decoded %T", f)
		}
		if r.SchemaVersion != 3 || r.Kind != KindHTTP || r.URL != "https://x" || r.Description != "n" {
			t.Errorf("decoded %+v", r)
		}
		if string(r.Unknown["extra"]) != "true" || len(r.Unknown) != 1 {
			t.Errorf("unknown = %v", r.Unknown)
		}
	})

	t.Run("current version is not migrated", func(t *testing.T) {
		t.Parallel()
		f, err := decode([]byte(`{"schemaVersion":3,"kind":"http","id":"r","notes":"n"}`), nil, steps, 3)
		if err != nil {
			t.Fatal(err)
		}
		if r := f.(*HTTPRequest); r.Description != "" || r.Unknown["notes"] == nil {
			t.Errorf("decoded %+v", r)
		}
	})

	t.Run("wrong kind is checked after the upgrade", func(t *testing.T) {
		t.Parallel()
		_, err := decode([]byte(`{"schemaVersion":1,"kind":"rest","id":"r"}`), []Kind{KindWorkspace}, steps, 3)
		checkErr(t, err, wantErr{kind: errs.Parse, code: CodeWrongKind, details: map[string]string{"kind": "http"}})
	})

	failures := []struct {
		name  string
		steps map[int]migration
	}{
		{name: "missing step", steps: map[int]migration{1: steps[1]}},
		{name: "step fails", steps: map[int]migration{
			1: steps[1],
			2: func(Kind, map[string]json.RawMessage) (map[string]json.RawMessage, error) {
				return nil, errors.New("boom")
			},
		}},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decode([]byte(`{"schemaVersion":1,"kind":"rest","id":"r"}`), nil, tt.steps, 3)
			checkErr(t, err, wantErr{kind: errs.Parse, code: CodeMigrationFailed, details: map[string]string{"version": "1"}})
		})
	}

	t.Run("every older version has a step", func(t *testing.T) {
		t.Parallel()
		for v := 1; v < CurrentVersion; v++ {
			if migrations[v] == nil {
				t.Errorf("no migration from version %d", v)
			}
		}
	})
}

func TestNewerVersion(t *testing.T) {
	t.Parallel()
	data := []byte(`{"schemaVersion":99,"kind":"folder","id":"f","name":"Later","order":["a"],"newThing":{}}`)
	f, err := DecodeFolder(data)
	if err != nil {
		t.Fatalf("a newer file must still load: %v", err)
	}
	if !f.Newer() || f.SchemaVersion != 99 || f.Name != "Later" {
		t.Errorf("decoded %+v", f)
	}
	_, err = Marshal(f)
	checkErr(t, err, wantErr{kind: errs.Unsupported, code: CodeNewerVersion, details: map[string]string{"version": "99", "supported": "1"}})

	path := filepath.Join(t.TempDir(), FolderFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err = WriteFile(path, f)
	checkErr(t, err, wantErr{kind: errs.Unsupported, code: CodeNewerVersion, details: map[string]string{"path": path}})
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Errorf("the newer file was changed:\n%s", got)
	}
}

func TestMarshalInvalid(t *testing.T) {
	const secret = "s3cr3t-value"
	tests := []struct {
		name  string
		file  File
		field string
	}{
		{name: "workspace without id", file: &Workspace{Name: "W"}, field: "id"},
		{name: "blank id", file: &Folder{ID: "  "}, field: "id"},
		{name: "http without method", file: &HTTPRequest{ID: "r", Method: " "}, field: "method"},
		{name: "graphql without id", file: &GraphQLRequest{}, field: "id"},
		{name: "secret value in environment", file: &Environment{ID: "e", Variables: []Variable{
			{Name: "a", Value: "x"},
			{Name: "token", Value: secret, Secret: true},
		}}, field: "variables[1].value"},
		{name: "secret value in folder", file: &Folder{ID: "f", Variables: []Variable{{Name: "t", Value: secret, Secret: true}}}, field: "variables[0].value"},
		{name: "secret value in workspace", file: &Workspace{ID: "w", Variables: []Variable{{Name: "t", Value: secret, Secret: true}}}, field: "variables[0].value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := Marshal(tt.file)
			if err == nil {
				t.Fatalf("Marshal = %s, want an error", data)
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidValue || e.Details["field"] != tt.field {
				t.Errorf("error = %#v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(e.Hint, secret) || strings.Contains(e.Details["field"], secret) {
				t.Errorf("the error reveals the secret: %v", err)
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		for _, f := range []File{nil, (*HTTPRequest)(nil)} {
			if _, err := Marshal(f); errs.KindOf(err) != errs.Internal {
				t.Errorf("Marshal(%#v) error = %v, want internal", f, err)
			}
		}
	})

	t.Run("secret without value is written", func(t *testing.T) {
		t.Parallel()
		data, err := Marshal(&Environment{ID: "e", Variables: []Variable{{Name: "token", Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"secret": true`)) {
			t.Errorf("Marshal = %s", data)
		}
	})
}

func TestMarshalDefaults(t *testing.T) {
	t.Parallel()
	// Head is set from the type, and empty body mode and auth type get
	// their defaults.
	data, err := Marshal(&HTTPRequest{Head: Head{SchemaVersion: 0, Kind: "graphql"}, ID: "r", Name: "N", Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "http",
  "id": "r",
  "name": "N",
  "method": "GET",
  "url": "",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "inherit"
  }
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
}

// TestUDPDefaults: the default send mode and settings are written as
// missing members; unknown values and members are kept.
func TestUDPDefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&UDPRequest{ID: "u", Name: "N", Address: "localhost:53", SendMode: SendText})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "udp",
  "id": "u",
  "name": "N",
  "address": "localhost:53"
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	f, err := Decode([]byte(`{"kind":"udp","id":"u","sendMode":"cbor","settings":{"ipVersion":"ipv5","openTimeoutMs":0,"ttl":4},"retries":2}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*UDPRequest)
	if r.SendMode != "cbor" || r.Settings.IPVersion != "ipv5" || r.Settings.Connected || r.Settings.OpenTimeoutMs == nil || *r.Settings.OpenTimeoutMs != 0 ||
		r.Settings.MaxDatagramBytes != nil || string(r.Settings.Unknown["ttl"]) != "4" || string(r.Unknown["retries"]) != "2" {
		t.Errorf("Decode = %+v", r)
	}
	data, err = Marshal(f)
	if err != nil || !strings.Contains(string(data), `"ttl": 4`) || !strings.Contains(string(data), `"retries": 2`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
}

// TestTCPDefaults: the default send mode and line ending are written as
// missing members, and a missing member decodes as empty (the default).
func TestTCPDefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&TCPRequest{ID: "t", Name: "N", Address: "localhost:7", SendMode: SendText, LineEnding: LineEndingNone, Framing: TCPFraming{Mode: FramingNone}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "tcp",
  "id": "t",
  "name": "N",
  "address": "localhost:7"
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	f, err := Decode([]byte(`{"kind":"tcp","id":"t","sendMode":"base64","lineEnding":"lf","settings":{"connectTimeoutMs":0,"keepAlive":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*TCPRequest)
	if r.SendMode != "base64" || r.LineEnding != LineEndingLF || r.Settings.ConnectTimeoutMs == nil || *r.Settings.ConnectTimeoutMs != 0 || string(r.Settings.Unknown["keepAlive"]) != "true" {
		t.Errorf("Decode = %+v", r)
	}
	// A file without framing or TLS (TCP-01) reads as no framing, plain
	// TCP; unknown framing values and members are kept.
	if r.Framing.Mode != "" || r.Settings.TLS || r.Settings.VerifyTLS != nil {
		t.Errorf("Decode = %+v", r)
	}
	f, err = Decode([]byte(`{"kind":"tcp","id":"t","framing":{"mode":"cobs","escape":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.(*TCPRequest); r.Framing.Mode != "cobs" || string(r.Framing.Unknown["escape"]) != "true" {
		t.Errorf("Decode = %+v", r)
	}
	data, err = Marshal(f)
	if err != nil || !strings.Contains(string(data), `"framing": {
    "mode": "cobs",
    "escape": true
  }`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
}

// TestWebSocketDefaults: the text format is written as a missing member,
// also for a saved message, empty lists are left out, and unknown settings
// and saved-message members survive.
func TestWebSocketDefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&WebSocketRequest{ID: "w", Name: "N", URL: "ws://localhost:8080", MessageFormat: MessageText, Subprotocols: []string{}, SavedMessages: []WebSocketMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "websocket",
  "id": "w",
  "name": "N",
  "url": "ws://localhost:8080"
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	f, err := Decode([]byte(`{"kind":"websocket","id":"w","messageFormat":"xml","subprotocols":["a"],"settings":{"sendCookies":true,"pingMs":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*WebSocketRequest)
	if r.MessageFormat != "xml" || len(r.Subprotocols) != 1 || r.Settings.SendCookies == nil || !*r.Settings.SendCookies || string(r.Settings.Unknown["pingMs"]) != "5" ||
		r.Settings.PingIntervalMs != nil || r.Settings.AutoReconnect != nil || r.Settings.ReconnectMaxAttempts != nil {
		t.Errorf("Decode = %+v", r)
	}
	data, err = Marshal(&WebSocketRequest{ID: "w", SavedMessages: []WebSocketMessage{{Name: "a", Text: "x", Format: MessageText}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"savedMessages": [
    {
      "name": "a",
      "text": "x"
    }
  ]`) {
		t.Errorf("Marshal = %s", data)
	}
	f, err = Decode([]byte(`{"kind":"websocket","id":"w","savedMessages":[{"name":"a","text":"x","format":"cbor","repeat":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := f.(*WebSocketRequest).SavedMessages
	if len(m) != 1 || m[0].Name != "a" || m[0].Format != "cbor" || string(m[0].Unknown["repeat"]) != "3" {
		t.Errorf("Decode saved messages = %+v", m)
	}
	data, err = Marshal(f)
	if err != nil || !strings.Contains(string(data), `"repeat": 3`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
}

// TestSSEDefaults: GET and an empty body are written as missing members,
// another method and a body are kept, and HTTP converts with GET for an
// empty method.
func TestSSEDefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&SSERequest{ID: "s", Name: "N", Method: "GET", URL: "http://localhost/events", Body: Body{Mode: BodyNone}, Params: []KeyValue{}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "sse",
  "id": "s",
  "name": "N",
  "url": "http://localhost/events",
  "auth": {
    "type": "inherit"
  }
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	data, err = Marshal(&SSERequest{ID: "s", Method: "POST", Body: Body{Mode: BodyRaw, Type: RawJSON, Text: "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"method": "POST"`) || !strings.Contains(string(data), `"mode": "raw"`) {
		t.Errorf("Marshal = %s", data)
	}
	f, err := Decode([]byte(`{"kind":"sse","id":"s","url":"u","lastEventId":"7","body":{"mode":"none"}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*SSERequest)
	if string(r.Unknown["lastEventId"]) != `"7"` {
		t.Errorf("Unknown = %v", r.Unknown)
	}
	h := r.HTTP()
	if h.Method != "GET" || h.URL != "u" || h.ID != "s" || h.Head != (Head{}) {
		t.Errorf("HTTP() = %+v", h)
	}
	data, err = Marshal(f)
	if err != nil || !strings.Contains(string(data), `"lastEventId": "7"`) || strings.Contains(string(data), `"body"`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
}

// TestGraphQLTransport (GQL-35): a file written before the per-operation
// transport loads without it and is written back unchanged; the member's
// unknown values and members survive a round trip, and an empty choice is
// written as an empty object.
func TestGraphQLTransport(t *testing.T) {
	t.Parallel()
	old := `{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "g",
  "name": "N",
  "url": "http://localhost/graphql",
  "query": "subscription { a }",
  "connectionParams": "{}",
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "timeoutMs": 5000
  }
}
`
	f, err := Decode([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.(*GraphQLRequest); r.Settings.GraphQLTransport != nil {
		t.Errorf("GraphQLTransport = %+v, want nil", r.Settings.GraphQLTransport)
	}
	data, err := Marshal(f)
	if err != nil || string(data) != old {
		t.Errorf("Marshal =\n%s\nwant\n%s (%v)", data, old, err)
	}

	f, err = Decode([]byte(`{"kind":"graphql","id":"g","url":"u","query":"q","settings":{"graphqlTransport":{"query":"sse","mutation":"quic","future":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	gt := f.(*GraphQLRequest).Settings.GraphQLTransport
	if gt == nil || gt.Query != GraphQLTransportSSE || gt.Mutation != "quic" || gt.Subscription != "" || string(gt.Unknown["future"]) != "1" {
		t.Fatalf("GraphQLTransport = %+v", gt)
	}
	data, err = Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `  "settings": {
    "graphqlTransport": {
      "query": "sse",
      "mutation": "quic",
      "future": 1
    }
  }`
	if !strings.Contains(string(data), want) {
		t.Errorf("Marshal =\n%s\nwant it to contain\n%s", data, want)
	}

	data, err = Marshal(&GraphQLRequest{ID: "g", Settings: RequestSettings{GraphQLTransport: &GraphQLTransport{}}})
	if err != nil || !strings.Contains(string(data), `"graphqlTransport": {}`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
}

// TestSocketIODefaults: the default transport and path are written as
// missing members, others are kept, and unknown members of the request,
// its saved emits and settings survive.
func TestSocketIODefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&SocketIORequest{ID: "s", Name: "N", URL: "http://localhost:3000", Path: "/socket.io/", Transport: "auto", Params: []KeyValue{}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "socketio",
  "id": "s",
  "name": "N",
  "url": "http://localhost:3000"
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	f, err := Decode([]byte(`{"kind":"socketio","id":"s","url":"u","transport":"webtransport","parser":"msgpack",` +
		`"savedEmits":[{"name":"a","event":"e","retries":2}],"settings":{"ackTimeoutMs":1,"reconnect":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*SocketIORequest)
	if r.Transport != "webtransport" || string(r.Unknown["parser"]) != `"msgpack"` ||
		string(r.SavedEmits[0].Unknown["retries"]) != "2" || string(r.Settings.Unknown["reconnect"]) != "true" || *r.Settings.AckTimeoutMs != 1 {
		t.Errorf("decoded = %+v", r)
	}
	data, err = Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"transport": "webtransport"`, `"parser": "msgpack"`, `"retries": 2`, `"reconnect": true`} {
		if !strings.Contains(string(data), s) {
			t.Errorf("Marshal lost %s:\n%s", s, data)
		}
	}
}

// TestGRPCDefaults: empty lists, a false inherit flag and plaintext are
// written as missing members, an empty proto setup and empty settings are
// left out, and unknown members of the setup and the folder's grpc object
// survive.
func TestGRPCDefaults(t *testing.T) {
	t.Parallel()
	data, err := Marshal(&GRPCRequest{ID: "g", Name: "N", Address: "localhost:50051", Proto: GRPCProto{Files: nil, ImportPaths: nil}, Metadata: []KeyValue{}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "kind": "grpc",
  "id": "g",
  "name": "N",
  "address": "localhost:50051"
}
`
	if string(data) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", data, want)
	}
	f, err := Decode([]byte(`{"kind":"grpc","id":"g","proto":{"files":["a.proto"],"inherit":true,"descriptorSet":"x.pb"},"settings":{"tls":true,"compression":"gzip","transport":"grpc-web-text","keepalive":30}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*GRPCRequest)
	if !r.Proto.Inherit || len(r.Proto.Files) != 1 || string(r.Proto.Unknown["descriptorSet"]) != `"x.pb"` || !r.Settings.TLS || r.Settings.Compression != GRPCCompressionGzip ||
		r.Settings.Transport != GRPCTransportWebText || string(r.Settings.Unknown["keepalive"]) != "30" || r.Settings.Unknown["compression"] != nil {
		t.Errorf("Decode = %+v", r)
	}

	folder, err := DecodeFolder([]byte(`{"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},"grpc":{"proto":{"importPaths":["proto"]},"reflection":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(folder.GRPC.Proto.ImportPaths) != 1 || string(folder.GRPC.Unknown["reflection"]) != "true" {
		t.Errorf("DecodeFolder = %+v", folder)
	}
	folder.GRPC = FolderGRPC{}
	data, err = Marshal(folder)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "grpc") {
		t.Errorf("folder without a setup =\n%s", data)
	}
}

// TestFolderServer covers the saved server config (SRV-04): a folder
// without one (every file written before it) has none and is written
// without the member, members a newer version adds at any level survive a
// load/save cycle, and a server needs a type.
func TestFolderServer(t *testing.T) {
	t.Parallel()
	old := []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"}}`)
	folder, err := DecodeFolder(old)
	if err != nil {
		t.Fatal(err)
	}
	if folder.Server != nil {
		t.Errorf("Server = %+v, want nil", folder.Server)
	}
	data, err := Marshal(folder)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "server") {
		t.Errorf("folder without a server =\n%s", data)
	}

	input := []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"mock_http","tls":{"cert":"c.pem"},"mock":{"routes":[{"id":"r","method":"GET","path":"/","status":200,
		"body":{"mode":"none"},"delay":{"mode":"fixed","ms":5,"jitter":1},"match":{"query":"a"}}],"fallback":"proxy"},
		"webSocket":{"path":"/ws","autoReply":[{"match":"ping"}]},"webhook":{"status":204,"forward":true}}}`)
	folder, err = DecodeFolder(input)
	if err != nil {
		t.Fatal(err)
	}
	s := folder.Server
	if s == nil || s.Type != "mock_http" || s.Mock == nil || len(s.Mock.Routes) != 1 || s.Mock.Routes[0].Delay.Ms != 5 ||
		s.WebSocket == nil || s.WebSocket.Path != "/ws" || s.Webhook == nil || s.Webhook.Status != 204 {
		t.Fatalf("DecodeFolder server = %+v", s)
	}
	first, err := Marshal(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`"tls"`, `"fallback"`, `"jitter"`, `"match"`, `"autoReply"`, `"forward"`} {
		if !strings.Contains(string(first), member) {
			t.Errorf("unknown member %s lost:\n%s", member, first)
		}
	}
	again, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("round trip changed the file:\n%s\n%s", first, second)
	}

	folder.Server = &FolderServer{Type: " "}
	var e *errs.Error
	if _, err := Marshal(folder); !errors.As(err, &e) || e.Code != CodeInvalidValue || e.Details["field"] != "server.type" {
		t.Errorf("Marshal of a server without a type = %v, want %s", err, CodeInvalidValue)
	}
}

func TestWriteReadFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range examples() {
		path := filepath.Join(dir, tt.name+Ext)
		if err := WriteFile(path, tt.file); err != nil {
			t.Fatalf("WriteFile %s: %v", tt.name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want, _ := Marshal(tt.file); !bytes.Equal(data, want) {
			t.Errorf("%s: file differs from Marshal", tt.name)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtimeHasUnixPerms && fi.Mode().Perm() != FilePerm {
			t.Errorf("%s: mode %v, want %v", tt.name, fi.Mode().Perm(), FilePerm)
		}
		f, err := ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", tt.name, err)
		}
		if f.FileKind() != tt.file.FileKind() || f.head().SchemaVersion != CurrentVersion {
			t.Errorf("%s: ReadFile = %+v", tt.name, f)
		}
	}

	missing := filepath.Join(dir, "missing.json")
	_, err := ReadFile(missing)
	checkErr(t, err, wantErr{kind: errs.NotFound, code: CodeReadFailed, details: map[string]string{"path": missing}})

	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"kind":`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadFile(broken)
	checkErr(t, err, wantErr{kind: errs.Parse, code: CodeParseFailed, details: map[string]string{"path": broken}})

	noDir := filepath.Join(dir, "no-such-dir", "x.json")
	err = WriteFile(noDir, &HTTPRequest{ID: "r", Method: "GET"})
	checkErr(t, err, wantErr{kind: errs.NotFound, code: CodeSaveFailed, details: map[string]string{"path": noDir}})

	err = WriteFile(filepath.Join(dir, "invalid.json"), &HTTPRequest{Method: "GET"})
	checkErr(t, err, wantErr{kind: errs.InvalidArgument, code: CodeInvalidValue, details: map[string]string{"field": "id"}})
}

func TestNewID(t *testing.T) {
	t.Parallel()
	prefixes := map[Kind]string{
		KindWorkspace:   WorkspaceIDPrefix,
		KindFolder:      FolderIDPrefix,
		KindHTTP:        RequestIDPrefix,
		KindGraphQL:     RequestIDPrefix,
		KindTCP:         RequestIDPrefix,
		KindWebSocket:   RequestIDPrefix,
		KindGRPC:        RequestIDPrefix,
		KindSSE:         RequestIDPrefix,
		KindSocketIO:    RequestIDPrefix,
		KindEnvironment: EnvironmentIDPrefix,
		"mqtt":          RequestIDPrefix,
	}
	seen := map[string]bool{}
	for kind, prefix := range prefixes {
		for range 100 {
			id := NewID(kind)
			rest, ok := strings.CutPrefix(id, prefix)
			if !ok || len(rest) != 16 || strings.Trim(rest, "0123456789abcdef") != "" {
				t.Fatalf("NewID(%q) = %q", kind, id)
			}
			if seen[id] {
				t.Fatalf("NewID returned %q twice", id)
			}
			seen[id] = true
		}
	}
}

func TestKinds(t *testing.T) {
	t.Parallel()
	for _, k := range Kinds() {
		if got, want := k.IsRequest(), k == KindHTTP || k == KindGraphQL || k == KindTCP || k == KindUDP || k == KindWebSocket || k == KindGRPC || k == KindSSE ||
			k == KindSocketIO; got != want {
			t.Errorf("%q.IsRequest() = %v", k, got)
		}
	}
}
