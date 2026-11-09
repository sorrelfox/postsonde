// Servers store: the local servers of this app run and which one the work
// area shows (SRV-01). commands/servers.ts fills it from the Go
// ServersService and its servers:changed event (docs/servers.md). Traffic is
// not here: it lives in one MessageSource per server (lib/serverTraffic.ts).
// The routes of a mock server being edited are kept as a draft until Go has
// them (commands/mockRoutes.ts). The clients of WebSocket servers (SSRV-01)
// come from servers:clients, with the selected client and the composer's
// text per server. The open connections of TCP servers (SSRV-05) come from
// servers:connections, with the selected connection, the composer and the
// traffic view (text or hex) per server. The requests a webhook receiver
// keeps (MOCK-08) come from servers:captures, with the selected request and
// the replay URL per server. The subscribers of SSE servers (SSRV-04) come
// from servers:subscribers and the recent peers of UDP servers (SSRV-06)
// from servers:peers, each with the selection and the composer per server.
// The sockets of Socket.IO servers (SSRV-03) come from servers:sockets,
// with the selected socket and the emit composer per server.
// A server saved in a project folder (SRV-04)
// names its folder (`folder`); the saved servers that could not be defined
// are in `projectProblems`. This reducer does no I/O.

export type ServerState = "stopped" | "starting" | "running" | "stopping" | "failed";

export interface ServerWarning {
  readonly code: string;
  readonly message: string;
}

export interface ServerProblem {
  readonly code: string;
  readonly message: string;
  readonly hint: string;
}

/** One response header row of a mock route. */
export interface MockHeader {
  readonly enabled: boolean;
  readonly name: string;
  readonly value: string;
}

/** A mock route (MOCK-01): method and path pattern, and its response. */
export interface MockRoute {
  readonly id: string;
  readonly enabled: boolean;
  /** An HTTP method or "ANY". */
  readonly method: string;
  /** A pattern: /users/:id, /users/{id:[0-9]+}, /files/**. */
  readonly path: string;
  readonly status: number;
  readonly headers: readonly MockHeader[];
  /** mode "none" or "raw"; type json, xml, html, text or yaml. */
  readonly body: { readonly mode: string; readonly type: string; readonly text: string };
  /** mode "none", "fixed" (ms) or "random" (minMs to maxMs) (MOCK-04). */
  readonly delay: { readonly mode: string; readonly ms: number; readonly minMs: number; readonly maxMs: number };
  /**
   * The route's other responses (MOCK-05, MOCK-07), tried in order before
   * status, headers and body (the default response).
   */
  readonly responses: readonly MockResponse[];
  /**
   * The default response's body and header values are templates that read
   * the request ({{request.params.id}}) and dynamic values (MOCK-06).
   */
  readonly templated: boolean;
}

/** A condition of a mock response: source, name, operator and value. */
export interface MockRule {
  /** "query", "header", "body" or "json" (a JSONPath into the request body). */
  readonly source: string;
  /** The query parameter, header name or JSONPath; unused for "body". */
  readonly name: string;
  /** equals, not_equals, contains, not_contains, matches, exists or not_exists. */
  readonly op: string;
  readonly value: string;
}

/**
 * One of a route's other responses: answers when all its rules match, or
 * when the X-PostSonde-Response request header names it.
 */
export interface MockResponse {
  readonly name: string;
  readonly status: number;
  readonly headers: readonly MockHeader[];
  readonly body: { readonly mode: string; readonly type: string; readonly text: string };
  readonly rules: readonly MockRule[];
  /** The body and header values are templates (MOCK-06). */
  readonly templated: boolean;
}

/** The routes of a mock server. */
export interface MockConfig {
  readonly routes: readonly MockRoute[];
  readonly strictTrailingSlash: boolean;
  readonly caseInsensitive: boolean;
}

/** A problem with a route; the route is not served. */
export interface RouteDiagnostic {
  readonly code: string;
  readonly routeIndex: number;
  readonly routeId: string;
  /** "pattern" (the path), "method", "id", "status", "header", "body", "delay", "name" or "rule". */
  readonly field: string;
  /** UTF-8 byte range in the path or method. */
  readonly start: number;
  readonly end: number;
  /** The response in the route's responses, else -1 (the route and its default response). */
  readonly responseIndex: number;
  /** The header row for field "header", else -1. */
  readonly headerIndex: number;
  /** The response's rule for field "rule", else -1. */
  readonly ruleIndex: number;
  readonly message: string;
}

/** The settings of a WebSocket server (SSRV-01). */
export interface WsServerConfig {
  /** Where clients connect ("/", "/socket"), or "*" for any path. */
  readonly path: string;
  /** Accepted subprotocols; the first one a client offers that is listed is selected. */
  readonly subprotocols: readonly string[];
  /** Send a message back to its client when no auto-reply rule answers it. */
  readonly echo: boolean;
  /** The largest message received, in bytes; 0 for 16 MiB. */
  readonly maxMessageSize: number;
  /** The auto-reply rules (SSRV-02), in order; the first enabled one that matches a text message answers it. */
  readonly rules: readonly WsAutoReplyRule[];
}

/** How an auto-reply rule matches a text message. */
export type WsRuleMatch = "exact" | "contains" | "regex";

/** An auto-reply rule of a WebSocket server. */
export interface WsAutoReplyRule {
  readonly enabled: boolean;
  readonly match: WsRuleMatch;
  /** The text, or the Go (RE2) regular expression, to match. */
  readonly pattern: string;
  /** The text sent back; with regex, $1, ${1}, $name and ${name} are capture groups and $$ is a $. */
  readonly reply: string;
  /** Holds the reply back, in ms (0 to 60000). */
  readonly delayMs: number;
}

/** A client connected to a WebSocket server. */
export interface WsClient {
  /** The connection ID ("3") shown in the traffic; restarts with the server. */
  readonly id: string;
  readonly peer: string;
  /** The path and query it connected to. */
  readonly target: string;
  readonly subprotocol: string;
  /** Epoch ms, or null. */
  readonly connectedAt: number | null;
  readonly origin: string;
  readonly userAgent: string;
  readonly messagesIn: number;
  readonly messagesOut: number;
  readonly bytesIn: number;
  readonly bytesOut: number;
}

/** The composer of a WebSocket server's Clients tab. */
export interface WsComposerState {
  readonly format: "text" | "json";
  readonly message: string;
}

/** A connection open on a TCP server (SSRV-05). */
export interface TcpConnection {
  /** The connection ID ("3") shown in the traffic; restarts with the server. */
  readonly id: string;
  readonly peer: string;
  /** The server's end of the connection. */
  readonly local: string;
  /** Epoch ms, or null. */
  readonly connectedAt: number | null;
  readonly bytesIn: number;
  readonly bytesOut: number;
  /** Reads (chunks received) and sends. */
  readonly messagesIn: number;
  readonly messagesOut: number;
}

/** The composer of a TCP server's Connections tab. */
export interface TcpComposerState {
  readonly format: "text" | "hex";
  readonly message: string;
  /** Appended to a text message. */
  readonly lineEnding: "none" | "lf" | "crlf";
}

/** A timer of an SSE server: an event sent to every subscriber every intervalMs. */
export interface SseTimer {
  readonly enabled: boolean;
  /** The event type; "" for message. */
  readonly event: string;
  /** The data; may hold dynamic values such as {{$timestamp}}. */
  readonly data: string;
  /** The event ID; "" for none (or the next number with autoId). */
  readonly id: string;
  readonly intervalMs: number;
}

/** The settings of an SSE server (SSRV-04). */
export interface SseServerConfig {
  /** Where subscribers connect ("/events"), or "*" for any path. */
  readonly path: string;
  /** The origin a browser page may subscribe from (CORS): "" for none, "*" for any. */
  readonly allowOrigin: string;
  /** Seconds between keep-alive comments; 0 for none. */
  readonly keepAliveSec: number;
  /** The retry sent to new subscribers, in ms; 0 for none. */
  readonly retryMs: number;
  /** Number the events sent without an ID. */
  readonly autoId: boolean;
  readonly timers: readonly SseTimer[];
}

/** A subscriber of an SSE server. */
export interface SseSubscriber {
  /** The subscriber number ("3") shown in the traffic; restarts with the server. */
  readonly id: string;
  readonly peer: string;
  /** The path and query it subscribed to. */
  readonly target: string;
  /** Epoch ms, or null. */
  readonly connectedAt: number | null;
  /** The Last-Event-ID it sent (a reconnecting EventSource), or "". */
  readonly lastEventId: string;
  readonly origin: string;
  readonly userAgent: string;
  readonly eventsSent: number;
  readonly bytesSent: number;
}

/** The event composer of an SSE server's Subscribers tab. */
export interface SseComposerState {
  readonly format: "text" | "json";
  /** The data. */
  readonly message: string;
  readonly event: string;
  readonly id: string;
  /** The retry field, as typed ("" for none). */
  readonly retry: string;
}

/** How a Socket.IO server's auto-reply rule matches an event name. */
export type SioRuleMatch = "exact" | "contains" | "regex";

/** An auto-reply rule of a Socket.IO server (SSRV-03). */
export interface SioRule {
  readonly enabled: boolean;
  readonly match: SioRuleMatch;
  /** The event name, or the text or regular expression to find in it. */
  readonly pattern: string;
  /** The acknowledgement's arguments as JSON text ("" for none). */
  readonly ack: string;
  /** The event emitted back ("" for none) and its arguments as JSON text. */
  readonly emit: string;
  readonly emitArgs: string;
  readonly delayMs: number;
}

/** How a Socket.IO server acknowledges events no rule answers. */
export type SioAckMode = "echo" | "empty" | "none";

/** The settings of a Socket.IO server (SSRV-03). */
export interface SioServerConfig {
  /** The Engine.IO path ("/socket.io/"). */
  readonly path: string;
  /** The namespaces accepted besides "/"; empty for any. */
  readonly namespaces: readonly string[];
  /** A JSON object the CONNECT auth must contain, or "". */
  readonly auth: string;
  /** "" for both transports, or "websocket" or "polling" only. */
  readonly transports: "" | "websocket" | "polling";
  /** The origin a browser page may connect from (CORS): "" for none, "*" for any. */
  readonly allowOrigin: string;
  readonly pingIntervalMs: number;
  readonly pingTimeoutMs: number;
  /** The largest payload accepted, in bytes. */
  readonly maxPayload: number;
  /** Emit events no rule answers back to their socket. */
  readonly echo: boolean;
  readonly ackMode: SioAckMode;
  readonly rules: readonly SioRule[];
}

/** A socket connected to a Socket.IO server: one client in one namespace. */
export interface SioSocket {
  /** "3" for client 3 in "/", "3/admin" in another namespace; restarts with the server. */
  readonly id: string;
  readonly clientId: string;
  /** The socket ID the client got. */
  readonly sid: string;
  readonly namespace: string;
  readonly transport: string;
  readonly peer: string;
  readonly target: string;
  readonly origin: string;
  readonly userAgent: string;
  /** The auth payload of its CONNECT, or "". */
  readonly auth: string;
  readonly rooms: readonly string[];
  /** Epoch ms, or null. */
  readonly connectedAt: number | null;
  readonly eventsIn: number;
  readonly eventsOut: number;
}

/** The emit composer of a Socket.IO server's Sockets tab. */
export interface SioComposerState {
  readonly format: "text" | "json";
  /** The arguments, as JSON text. */
  readonly message: string;
  readonly event: string;
  /** Ask for an acknowledgement. */
  readonly ack: boolean;
  /** The room "Emit to room" sends to. */
  readonly room: string;
}

/** How a UDP server answers datagrams (SSRV-06). */
export interface UdpServerConfig {
  readonly echo: boolean;
  readonly fixedReply: boolean;
  readonly reply: string;
  readonly replyFormat: "text" | "hex";
}

/** The bidirectional modes of a gRPC mock method: answer each message, send a stream, or echo. */
export type GrpcBidiMode = "each" | "stream" | "echo";

/** The canned response of one method of a gRPC mock server (SSRV-07). */
export interface GrpcMockMethod {
  /** The fully-qualified service and the method's name. */
  readonly service: string;
  readonly method: string;
  /** The response messages as JSON text (unary and client streaming send the first). */
  readonly messages: readonly string[];
  /** The gRPC status code (0 is OK) and its message. */
  readonly status: number;
  readonly statusMessage: string;
  /** Response headers and trailers; values of -bin keys are Base64. */
  readonly headers: readonly MockHeader[];
  readonly trailers: readonly MockHeader[];
  readonly delayMs: number;
  /** The pause between streamed messages. */
  readonly intervalMs: number;
  readonly bidi: GrpcBidiMode;
}

/** The .proto files and canned responses of a gRPC mock server. */
export interface GrpcMockConfig {
  /** Stored paths: relative to the project root, or absolute. */
  readonly files: readonly string[];
  readonly importPaths: readonly string[];
  /** Serve server reflection (grpc.reflection.v1 and v1alpha). */
  readonly reflection: boolean;
  readonly methods: readonly GrpcMockMethod[];
}

/** How a GraphQL mock override answers: with a JSON value, or with a field error. */
export type GraphQLMockMode = "value" | "error";

/** A static response of a GraphQL mock server (SSRV-08). */
export interface GraphQLMockOverride {
  readonly enabled: boolean;
  /** "Type.field", or a scalar or enum type for all its values. */
  readonly target: string;
  /** A JSON object the field's arguments must match; "" for any call. */
  readonly args: string;
  readonly mode: GraphQLMockMode;
  /** JSON text; members an object leaves out are generated. */
  readonly value: string;
  /** The field error's message and extensions.code. */
  readonly error: string;
  readonly code: string;
}

/** The schema, generated-data settings and overrides of a GraphQL mock server. */
export interface GraphQLMockConfig {
  /** The endpoint ("/graphql"). */
  readonly path: string;
  /** A stored path (SDL or introspection JSON); "" uses schemaText. */
  readonly schemaFile: string;
  /** Pasted SDL or introspection JSON. */
  readonly schemaText: string;
  /** Answer __schema and __type. */
  readonly introspection: boolean;
  /** Items in a generated list (0–100). */
  readonly listLength: number;
  /** How often a nullable field is generated as null (0–100). */
  readonly nullPercent: number;
  readonly seed: number;
  /** The CORS origin allowed: "*", an origin, or "" for none. */
  readonly allowOrigin: string;
  readonly overrides: readonly GraphQLMockOverride[];
}

/** A peer a UDP server heard from recently. */
export interface UdpPeer {
  /** The peer's address, what a datagram is sent to. */
  readonly addr: string;
  /** The server's address it sent to. */
  readonly local: string;
  /** Epoch ms, or null. */
  readonly firstSeen: number | null;
  readonly lastSeen: number | null;
  readonly datagramsIn: number;
  readonly datagramsOut: number;
  readonly bytesIn: number;
  readonly bytesOut: number;
}

/** The response a webhook receiver answers every request with (MOCK-08). */
export interface WebhookConfig {
  /** 200–599. */
  readonly status: number;
  readonly headers: readonly MockHeader[];
  readonly body: string;
}

/** A request a webhook receiver keeps whole, as the list shows it. */
export interface WebhookCapture {
  /** The seq of its request in the traffic; names it for Capture and Replay. */
  readonly seq: number;
  /** Epoch ms, or null. */
  readonly time: number | null;
  readonly peer: string;
  readonly method: string;
  /** The request target as sent: path and query. */
  readonly target: string;
  readonly contentType: string;
  /** The body's length; truncated when only its first bytes were kept (it cannot be replayed). */
  readonly size: number;
  readonly truncated: boolean;
}

/** The project folder whose _folder.json defines a saved server (SRV-04). */
export interface ServerFolder {
  readonly workspaceId: string;
  readonly folderId: string;
  /** The folder's name when the server was last loaded from it. */
  readonly folderName: string;
}

/** How a server serves TLS (SRV-05). */
export interface ServerTls {
  /** "generated": a certificate of PostSonde's local CA; "files": the user's own. */
  readonly certificate: "generated" | "files";
  /** PEM certificate and key, or a PKCS#12 file (absolute paths); "" when unused. */
  readonly certFile: string;
  readonly keyFile: string;
  readonly pfxFile: string;
  /** A passphrase is stored for the file (the passphrase itself stays in Go). */
  readonly hasPassphrase: boolean;
}

/** The certificate a running TLS server presents. */
export interface ServerCertificate {
  readonly subject: string;
  readonly issuer: string;
  /** Epoch ms, or null. */
  readonly notBefore: number | null;
  readonly notAfter: number | null;
  /** SHA-256 fingerprint, lower-case hex. */
  readonly sha256: string;
  /** The DNS names and IP addresses it is valid for. */
  readonly names: readonly string[];
}

/** A folder whose saved server could not be defined (an unknown kind, a bad port). */
export interface FolderServerProblem {
  readonly folderId: string;
  readonly folderName: string;
  readonly message: string;
}

/** How a TCP server's traffic shows the bytes. */
export type TcpTrafficView = "text" | "hex";

export interface LocalServer {
  readonly id: string;
  readonly type: string;
  readonly typeLabel: string;
  readonly name: string;
  /** As defined: "" is localhost. */
  readonly host: string;
  /** As defined: 0 picks a free port at every start. */
  readonly port: number;
  readonly allowLan: boolean;
  /** How it serves TLS (SRV-05), or null for plain text. */
  readonly tls: ServerTls | null;
  /** The certificate it presents while it runs with TLS, else null. */
  readonly certificate: ServerCertificate | null;
  readonly state: ServerState;
  /** Bound addresses while running ("127.0.0.1:8080", "[::1]:8080"). */
  readonly addresses: readonly string[];
  readonly boundPort: number;
  /** "loopback", "lan", "all_interfaces" while running, else "". */
  readonly scope: string;
  readonly warning: ServerWarning | null;
  /** Epoch ms of the last start (uptime) and of the last stop or failure, or null. */
  readonly startedAt: number | null;
  readonly stoppedAt: number | null;
  readonly error: ServerProblem | null;
  /** Traffic events so far. */
  readonly events: number;
  /** The routes of a mock server as Go has them, or null for other types. */
  readonly mock: MockConfig | null;
  /** The settings of a WebSocket server as Go has them, or null for other types. */
  readonly webSocket: WsServerConfig | null;
  /** The response of a webhook receiver as Go has it, or null for other types. */
  readonly webhook: WebhookConfig | null;
  /** The settings of an SSE server as Go has them, or null for other types. */
  readonly sse: SseServerConfig | null;
  /** The replies of a UDP server as Go has them, or null for other types. */
  readonly udp: UdpServerConfig | null;
  /** The .proto files and canned responses of a gRPC mock server as Go has them, or null for other types. */
  readonly grpcMock: GrpcMockConfig | null;
  /** The settings of a Socket.IO server as Go has them, or null for other types. */
  readonly socketIo: SioServerConfig | null;
  /** The schema and overrides of a GraphQL mock server as Go has them, or null for other types. */
  readonly graphqlMock: GraphQLMockConfig | null;
  /** The problems of those routes. */
  readonly diagnostics: readonly RouteDiagnostic[];
  /** The project folder that defines it (SRV-04), or null for a server made in the Servers view. */
  readonly folder: ServerFolder | null;
}

export interface ServerType {
  readonly name: string;
  readonly label: string;
}

export interface ServersState {
  readonly status: "idle" | "loading" | "ready" | "error";
  /** Server ids in the order created. */
  readonly ids: readonly string[];
  readonly byId: Readonly<Record<string, LocalServer>>;
  readonly types: readonly ServerType[];
  /** The server whose traffic the work area shows, or null. */
  readonly selectedId: string | null;
  readonly error: string | null;
  /** Mock routes edited but not yet confirmed by Go, per server. */
  readonly mockDrafts: Readonly<Record<string, MockConfig>>;
  /** Why the last update of a server's routes failed, per server. */
  readonly mockErrors: Readonly<Record<string, string>>;
  /** The connected clients of each running WebSocket server. */
  readonly wsClients: Readonly<Record<string, readonly WsClient[]>>;
  /** The client selected in each WebSocket server's Clients tab. */
  readonly wsSelectedClient: Readonly<Record<string, string>>;
  /** The composer of each WebSocket server. */
  readonly wsComposer: Readonly<Record<string, WsComposerState>>;
  /** The open connections of each running TCP server. */
  readonly tcpConnections: Readonly<Record<string, readonly TcpConnection[]>>;
  /** The connection selected in each TCP server's Connections tab. */
  readonly tcpSelectedConnection: Readonly<Record<string, string>>;
  /** The composer of each TCP server. */
  readonly tcpComposer: Readonly<Record<string, TcpComposerState>>;
  /** How each TCP server's traffic is shown. */
  readonly tcpView: Readonly<Record<string, TcpTrafficView>>;
  /** The requests each webhook receiver keeps, oldest first. */
  readonly webhookCaptures: Readonly<Record<string, readonly WebhookCapture[]>>;
  /** The request selected in each webhook receiver's Requests tab (its seq). */
  readonly webhookSelected: Readonly<Record<string, number>>;
  /** The URL each webhook receiver replays to. */
  readonly webhookReplayUrl: Readonly<Record<string, string>>;
  /** The subscribers of each running SSE server. */
  readonly sseSubscribers: Readonly<Record<string, readonly SseSubscriber[]>>;
  /** The subscriber selected in each SSE server's Subscribers tab. */
  readonly sseSelected: Readonly<Record<string, string>>;
  /** The event composer of each SSE server. */
  readonly sseComposer: Readonly<Record<string, SseComposerState>>;
  /** The recent peers of each running UDP server, most recent first. */
  readonly udpPeers: Readonly<Record<string, readonly UdpPeer[]>>;
  /** The peer selected in each UDP server's Peers tab (its address). */
  readonly udpSelected: Readonly<Record<string, string>>;
  /** The composer of each UDP server. */
  readonly udpComposer: Readonly<Record<string, TcpComposerState>>;
  /** The sockets of each running Socket.IO server. */
  readonly sioSockets: Readonly<Record<string, readonly SioSocket[]>>;
  /** The socket selected in each Socket.IO server's Sockets tab. */
  readonly sioSelected: Readonly<Record<string, string>>;
  /** The emit composer of each Socket.IO server. */
  readonly sioComposer: Readonly<Record<string, SioComposerState>>;
  /** The saved servers of the open project that could not be defined, from the last sync. */
  readonly projectProblems: readonly FolderServerProblem[];
}

export const initialServersState: ServersState = {
  status: "idle",
  ids: [],
  byId: {},
  types: [],
  selectedId: null,
  error: null,
  mockDrafts: {},
  mockErrors: {},
  wsClients: {},
  wsSelectedClient: {},
  wsComposer: {},
  tcpConnections: {},
  tcpSelectedConnection: {},
  tcpComposer: {},
  tcpView: {},
  webhookCaptures: {},
  webhookSelected: {},
  webhookReplayUrl: {},
  sseSubscribers: {},
  sseSelected: {},
  sseComposer: {},
  udpPeers: {},
  udpSelected: {},
  udpComposer: {},
  sioSockets: {},
  sioSelected: {},
  sioComposer: {},
  projectProblems: [],
};

export type ServersAction =
  | { type: "servers/loading" }
  | { type: "servers/loaded"; servers: readonly LocalServer[]; types: readonly ServerType[] }
  | { type: "servers/failed"; error: string }
  /** The whole list, from servers:changed. */
  | { type: "servers/set"; servers: readonly LocalServer[] }
  /** Adds or replaces one server (a create result). */
  | { type: "servers/put"; server: LocalServer }
  | { type: "servers/select"; id: string | null }
  /** Routes edited in the UI, to be sent to Go. */
  | { type: "servers/mockDraft"; id: string; config: MockConfig }
  /** Go has `config`: the draft is dropped when it is still that one. */
  | { type: "servers/mockSaved"; id: string; config: MockConfig }
  | { type: "servers/mockFailed"; id: string; error: string }
  /** The clients of a WebSocket server, from servers:clients or a load. */
  | { type: "servers/wsClients"; id: string; clients: readonly WsClient[] }
  | { type: "servers/wsSelectClient"; id: string; clientId: string | null }
  | { type: "servers/wsComposer"; id: string; composer: WsComposerState }
  /** The connections of a TCP server, from servers:connections or a load. */
  | { type: "servers/tcpConnections"; id: string; connections: readonly TcpConnection[] }
  | { type: "servers/tcpSelectConnection"; id: string; connectionId: string | null }
  | { type: "servers/tcpComposer"; id: string; composer: TcpComposerState }
  | { type: "servers/tcpView"; id: string; view: TcpTrafficView }
  /** The requests a webhook receiver keeps, from servers:captures or a load. */
  | { type: "servers/webhookCaptures"; id: string; captures: readonly WebhookCapture[] }
  | { type: "servers/webhookSelect"; id: string; seq: number | null }
  | { type: "servers/webhookReplayUrl"; id: string; url: string }
  /** The subscribers of an SSE server, from servers:subscribers or a load. */
  | { type: "servers/sseSubscribers"; id: string; subscribers: readonly SseSubscriber[] }
  | { type: "servers/sseSelect"; id: string; subscriberId: string | null }
  | { type: "servers/sseComposer"; id: string; composer: SseComposerState }
  /** The recent peers of a UDP server, from servers:peers or a load. */
  | { type: "servers/udpPeers"; id: string; peers: readonly UdpPeer[] }
  | { type: "servers/udpSelect"; id: string; peer: string | null }
  | { type: "servers/udpComposer"; id: string; composer: TcpComposerState }
  /** The sockets of a Socket.IO server, from servers:sockets or a load. */
  | { type: "servers/sioSockets"; id: string; sockets: readonly SioSocket[] }
  | { type: "servers/sioSelect"; id: string; socketId: string | null }
  | { type: "servers/sioComposer"; id: string; composer: SioComposerState }
  /** The saved servers that could not be defined, from a project sync. */
  | { type: "servers/projectProblems"; problems: readonly FolderServerProblem[] };

function without<T>(record: Readonly<Record<string, T>>, id: string): Readonly<Record<string, T>> {
  if (!(id in record)) return record;
  const next = { ...record };
  delete next[id];
  return next;
}

function onlyIds<T>(record: Readonly<Record<string, T>>, byId: Readonly<Record<string, unknown>>): Readonly<Record<string, T>> {
  const stale = Object.keys(record).filter((id) => byId[id] === undefined);
  return stale.length === 0 ? record : stale.reduce((r, id) => without(r, id), record);
}

function withList(state: ServersState, servers: readonly LocalServer[]): ServersState {
  const byId: Record<string, LocalServer> = {};
  const ids: string[] = [];
  for (const s of servers) {
    if (byId[s.id] === undefined) ids.push(s.id);
    byId[s.id] = s;
  }
  const selectedId = state.selectedId !== null && byId[state.selectedId] ? state.selectedId : null;
  // A server that does not run has no clients or connections.
  const running: Record<string, LocalServer> = {};
  for (const s of servers) if (s.state === "running") running[s.id] = s;
  return {
    ...state,
    ids,
    byId,
    selectedId,
    mockDrafts: onlyIds(state.mockDrafts, byId),
    mockErrors: onlyIds(state.mockErrors, byId),
    wsClients: onlyIds(state.wsClients, running),
    wsSelectedClient: onlyIds(state.wsSelectedClient, byId),
    wsComposer: onlyIds(state.wsComposer, byId),
    tcpConnections: onlyIds(state.tcpConnections, running),
    tcpSelectedConnection: onlyIds(state.tcpSelectedConnection, byId),
    tcpComposer: onlyIds(state.tcpComposer, byId),
    tcpView: onlyIds(state.tcpView, byId),
    // Kept requests outlive a stop: they can still be inspected and replayed.
    webhookCaptures: onlyIds(state.webhookCaptures, byId),
    webhookSelected: onlyIds(state.webhookSelected, byId),
    webhookReplayUrl: onlyIds(state.webhookReplayUrl, byId),
    sseSubscribers: onlyIds(state.sseSubscribers, running),
    sseSelected: onlyIds(state.sseSelected, byId),
    sseComposer: onlyIds(state.sseComposer, byId),
    udpPeers: onlyIds(state.udpPeers, running),
    udpSelected: onlyIds(state.udpSelected, byId),
    udpComposer: onlyIds(state.udpComposer, byId),
    sioSockets: onlyIds(state.sioSockets, running),
    sioSelected: onlyIds(state.sioSelected, byId),
    sioComposer: onlyIds(state.sioComposer, byId),
  };
}

export function serversReducer(state: ServersState, action: ServersAction): ServersState {
  switch (action.type) {
    case "servers/loading":
      return state.status === "loading" ? state : { ...state, status: "loading", error: null };
    case "servers/loaded":
      return { ...withList(state, action.servers), types: action.types, status: "ready", error: null };
    case "servers/failed":
      return { ...state, status: "error", error: action.error };
    case "servers/set":
      return withList(state, action.servers);
    case "servers/put": {
      const { server } = action;
      if (state.byId[server.id] === server) return state;
      const ids = state.byId[server.id] ? state.ids : [...state.ids, server.id];
      return { ...state, ids, byId: { ...state.byId, [server.id]: server } };
    }
    case "servers/select": {
      const id = action.id !== null && state.byId[action.id] ? action.id : null;
      return id === state.selectedId ? state : { ...state, selectedId: id };
    }
    case "servers/mockDraft":
      if (!state.byId[action.id]) return state;
      return { ...state, mockDrafts: { ...state.mockDrafts, [action.id]: action.config } };
    case "servers/mockSaved": {
      const mockErrors = without(state.mockErrors, action.id);
      const mockDrafts = state.mockDrafts[action.id] === action.config ? without(state.mockDrafts, action.id) : state.mockDrafts;
      return mockErrors === state.mockErrors && mockDrafts === state.mockDrafts ? state : { ...state, mockDrafts, mockErrors };
    }
    case "servers/mockFailed":
      if (!state.byId[action.id]) return state;
      return { ...state, mockErrors: { ...state.mockErrors, [action.id]: action.error } };
    case "servers/wsClients":
      if (!state.byId[action.id]) return state;
      return { ...state, wsClients: { ...state.wsClients, [action.id]: action.clients } };
    case "servers/wsSelectClient":
      if (action.clientId === null) {
        const wsSelectedClient = without(state.wsSelectedClient, action.id);
        return wsSelectedClient === state.wsSelectedClient ? state : { ...state, wsSelectedClient };
      }
      if (!state.byId[action.id] || state.wsSelectedClient[action.id] === action.clientId) return state;
      return { ...state, wsSelectedClient: { ...state.wsSelectedClient, [action.id]: action.clientId } };
    case "servers/wsComposer":
      if (!state.byId[action.id]) return state;
      return { ...state, wsComposer: { ...state.wsComposer, [action.id]: action.composer } };
    case "servers/tcpConnections":
      if (!state.byId[action.id]) return state;
      return { ...state, tcpConnections: { ...state.tcpConnections, [action.id]: action.connections } };
    case "servers/tcpSelectConnection":
      if (action.connectionId === null) {
        const tcpSelectedConnection = without(state.tcpSelectedConnection, action.id);
        return tcpSelectedConnection === state.tcpSelectedConnection ? state : { ...state, tcpSelectedConnection };
      }
      if (!state.byId[action.id] || state.tcpSelectedConnection[action.id] === action.connectionId) return state;
      return { ...state, tcpSelectedConnection: { ...state.tcpSelectedConnection, [action.id]: action.connectionId } };
    case "servers/tcpComposer":
      if (!state.byId[action.id]) return state;
      return { ...state, tcpComposer: { ...state.tcpComposer, [action.id]: action.composer } };
    case "servers/tcpView":
      if (!state.byId[action.id] || selectTcpView(state, action.id) === action.view) return state;
      return { ...state, tcpView: { ...state.tcpView, [action.id]: action.view } };
    case "servers/webhookCaptures":
      if (!state.byId[action.id]) return state;
      return { ...state, webhookCaptures: { ...state.webhookCaptures, [action.id]: action.captures } };
    case "servers/webhookSelect":
      if (action.seq === null) {
        const webhookSelected = without(state.webhookSelected, action.id);
        return webhookSelected === state.webhookSelected ? state : { ...state, webhookSelected };
      }
      if (!state.byId[action.id] || state.webhookSelected[action.id] === action.seq) return state;
      return { ...state, webhookSelected: { ...state.webhookSelected, [action.id]: action.seq } };
    case "servers/webhookReplayUrl":
      if (!state.byId[action.id] || state.webhookReplayUrl[action.id] === action.url) return state;
      return { ...state, webhookReplayUrl: { ...state.webhookReplayUrl, [action.id]: action.url } };
    case "servers/sseSubscribers":
      if (!state.byId[action.id]) return state;
      return { ...state, sseSubscribers: { ...state.sseSubscribers, [action.id]: action.subscribers } };
    case "servers/sseSelect":
      if (action.subscriberId === null) {
        const sseSelected = without(state.sseSelected, action.id);
        return sseSelected === state.sseSelected ? state : { ...state, sseSelected };
      }
      if (!state.byId[action.id] || state.sseSelected[action.id] === action.subscriberId) return state;
      return { ...state, sseSelected: { ...state.sseSelected, [action.id]: action.subscriberId } };
    case "servers/sseComposer":
      if (!state.byId[action.id]) return state;
      return { ...state, sseComposer: { ...state.sseComposer, [action.id]: action.composer } };
    case "servers/udpPeers":
      if (!state.byId[action.id]) return state;
      return { ...state, udpPeers: { ...state.udpPeers, [action.id]: action.peers } };
    case "servers/udpSelect":
      if (action.peer === null) {
        const udpSelected = without(state.udpSelected, action.id);
        return udpSelected === state.udpSelected ? state : { ...state, udpSelected };
      }
      if (!state.byId[action.id] || state.udpSelected[action.id] === action.peer) return state;
      return { ...state, udpSelected: { ...state.udpSelected, [action.id]: action.peer } };
    case "servers/udpComposer":
      if (!state.byId[action.id]) return state;
      return { ...state, udpComposer: { ...state.udpComposer, [action.id]: action.composer } };
    case "servers/sioSockets":
      if (!state.byId[action.id]) return state;
      return { ...state, sioSockets: { ...state.sioSockets, [action.id]: action.sockets } };
    case "servers/sioSelect":
      if (action.socketId === null) {
        const sioSelected = without(state.sioSelected, action.id);
        return sioSelected === state.sioSelected ? state : { ...state, sioSelected };
      }
      if (!state.byId[action.id] || state.sioSelected[action.id] === action.socketId) return state;
      return { ...state, sioSelected: { ...state.sioSelected, [action.id]: action.socketId } };
    case "servers/sioComposer":
      if (!state.byId[action.id]) return state;
      return { ...state, sioComposer: { ...state.sioComposer, [action.id]: action.composer } };
    case "servers/projectProblems":
      if (action.problems.length === 0 && state.projectProblems.length === 0) return state;
      return { ...state, projectProblems: action.problems };
  }
}

/** The server the work area shows, or null. */
export function selectSelectedServer(state: ServersState): LocalServer | null {
  return state.selectedId === null ? null : (state.byId[state.selectedId] ?? null);
}

/** The routes the editor shows for a server: the draft being sent, else Go's. */
export function selectMockConfig(state: ServersState, id: string): MockConfig | null {
  return state.mockDrafts[id] ?? state.byId[id]?.mock ?? null;
}

const NO_CLIENTS: readonly WsClient[] = [];

/** The connected clients of a WebSocket server (a stable empty list when none). */
export function selectWsClients(state: ServersState, id: string): readonly WsClient[] {
  return state.wsClients[id] ?? NO_CLIENTS;
}

/**
 * The item a server pane sends to: the one selected, else the first when
 * none was, else null. A selected item that left the list gives null
 * rather than another client's, until one is selected again, so a send
 * never reaches a client that was not selected.
 */
function selectedTarget<T>(items: readonly T[], selected: string | undefined, key: (item: T) => string): T | null {
  if (selected === undefined) return items[0] ?? null;
  return items.find((item) => key(item) === selected) ?? null;
}

/** Whether the item selected in a server pane has left its list. */
function selectionGone<T>(items: readonly T[], selected: string | undefined, key: (item: T) => string): boolean {
  return selected !== undefined && !items.some((item) => key(item) === selected);
}

/**
 * The client the Clients tab sends to: the one selected, else the first
 * when none was, else null; null when the selected one disconnected
 * (selectedTarget).
 */
export function selectWsTargetClient(state: ServersState, id: string): WsClient | null {
  return selectedTarget(selectWsClients(state, id), state.wsSelectedClient[id], (c) => c.id);
}

/** Whether the client selected in the Clients tab has disconnected. */
export function selectWsSelectionGone(state: ServersState, id: string): boolean {
  return selectionGone(selectWsClients(state, id), state.wsSelectedClient[id], (c) => c.id);
}

const EMPTY_COMPOSER: WsComposerState = { format: "text", message: "" };

/** The composer of a WebSocket server. */
export function selectWsComposer(state: ServersState, id: string): WsComposerState {
  return state.wsComposer[id] ?? EMPTY_COMPOSER;
}

const NO_CONNECTIONS: readonly TcpConnection[] = [];

/** The open connections of a TCP server (a stable empty list when none). */
export function selectTcpConnections(state: ServersState, id: string): readonly TcpConnection[] {
  return state.tcpConnections[id] ?? NO_CONNECTIONS;
}

/**
 * The connection the Connections tab sends to: the one selected, else the
 * first when none was, else null; null when the selected one closed.
 */
export function selectTcpTargetConnection(state: ServersState, id: string): TcpConnection | null {
  return selectedTarget(selectTcpConnections(state, id), state.tcpSelectedConnection[id], (c) => c.id);
}

/** Whether the connection selected in the Connections tab has closed. */
export function selectTcpSelectionGone(state: ServersState, id: string): boolean {
  return selectionGone(selectTcpConnections(state, id), state.tcpSelectedConnection[id], (c) => c.id);
}

const EMPTY_TCP_COMPOSER: TcpComposerState = { format: "text", message: "", lineEnding: "none" };

/** The composer of a TCP server. */
export function selectTcpComposer(state: ServersState, id: string): TcpComposerState {
  return state.tcpComposer[id] ?? EMPTY_TCP_COMPOSER;
}

/** How a TCP server's traffic is shown; text by default. */
export function selectTcpView(state: ServersState, id: string): TcpTrafficView {
  return state.tcpView[id] ?? "text";
}

const NO_CAPTURES: readonly WebhookCapture[] = [];

/** The requests a webhook receiver keeps, oldest first (a stable empty list when none). */
export function selectWebhookCaptures(state: ServersState, id: string): readonly WebhookCapture[] {
  return state.webhookCaptures[id] ?? NO_CAPTURES;
}

/**
 * The request the Requests tab shows: the one selected while it is kept,
 * else the newest, else null.
 */
export function selectWebhookCapture(state: ServersState, id: string): WebhookCapture | null {
  const captures = selectWebhookCaptures(state, id);
  const selected = state.webhookSelected[id];
  return captures.find((c) => c.seq === selected) ?? captures[captures.length - 1] ?? null;
}

/** The URL a webhook receiver replays to; "" until one is typed. */
export function selectWebhookReplayUrl(state: ServersState, id: string): string {
  return state.webhookReplayUrl[id] ?? "";
}

const NO_SUBSCRIBERS: readonly SseSubscriber[] = [];

/** The subscribers of an SSE server (a stable empty list when none). */
export function selectSseSubscribers(state: ServersState, id: string): readonly SseSubscriber[] {
  return state.sseSubscribers[id] ?? NO_SUBSCRIBERS;
}

/**
 * The subscriber the Subscribers tab sends to with "Send to subscriber":
 * the one selected, else the first when none was, else null; null when
 * the selected one disconnected.
 */
export function selectSseTargetSubscriber(state: ServersState, id: string): SseSubscriber | null {
  return selectedTarget(selectSseSubscribers(state, id), state.sseSelected[id], (s) => s.id);
}

/** Whether the subscriber selected in the Subscribers tab has disconnected. */
export function selectSseSelectionGone(state: ServersState, id: string): boolean {
  return selectionGone(selectSseSubscribers(state, id), state.sseSelected[id], (s) => s.id);
}

const EMPTY_SSE_COMPOSER: SseComposerState = { format: "text", message: "", event: "", id: "", retry: "" };

/** The event composer of an SSE server. */
export function selectSseComposer(state: ServersState, id: string): SseComposerState {
  return state.sseComposer[id] ?? EMPTY_SSE_COMPOSER;
}

const NO_PEERS: readonly UdpPeer[] = [];

/** The recent peers of a UDP server, most recent first (a stable empty list when none). */
export function selectUdpPeers(state: ServersState, id: string): readonly UdpPeer[] {
  return state.udpPeers[id] ?? NO_PEERS;
}

/**
 * The peer the Peers tab sends to: the one selected, else the most recent
 * when none was, else null; null while the selected one is not listed
 * (it is again when it sends another datagram).
 */
export function selectUdpTargetPeer(state: ServersState, id: string): UdpPeer | null {
  return selectedTarget(selectUdpPeers(state, id), state.udpSelected[id], (p) => p.addr);
}

/** Whether the peer selected in the Peers tab is no longer listed. */
export function selectUdpSelectionGone(state: ServersState, id: string): boolean {
  return selectionGone(selectUdpPeers(state, id), state.udpSelected[id], (p) => p.addr);
}

/** The composer of a UDP server. */
export function selectUdpComposer(state: ServersState, id: string): TcpComposerState {
  return state.udpComposer[id] ?? EMPTY_TCP_COMPOSER;
}

const NO_SOCKETS: readonly SioSocket[] = [];

/** The sockets of a Socket.IO server (a stable empty list when none). */
export function selectSioSockets(state: ServersState, id: string): readonly SioSocket[] {
  return state.sioSockets[id] ?? NO_SOCKETS;
}

/**
 * The socket the Sockets tab emits to with "Emit to socket": the one
 * selected, else the first when none was, else null. A selected socket
 * that disconnected gives null rather than another client's socket, until
 * one is selected again (socket IDs are not reused).
 */
export function selectSioTargetSocket(state: ServersState, id: string): SioSocket | null {
  return selectedTarget(selectSioSockets(state, id), state.sioSelected[id], (s) => s.id);
}

/** Whether the socket selected in the Sockets tab has disconnected. */
export function selectSioSelectionGone(state: ServersState, id: string): boolean {
  return selectionGone(selectSioSockets(state, id), state.sioSelected[id], (s) => s.id);
}

const EMPTY_SIO_COMPOSER: SioComposerState = { format: "json", message: "", event: "", ack: false, room: "" };

/** The emit composer of a Socket.IO server. */
export function selectSioComposer(state: ServersState, id: string): SioComposerState {
  return state.sioComposer[id] ?? EMPTY_SIO_COMPOSER;
}

/** The server saved in folder `folderId` of project `workspaceId`, or null. */
export function selectFolderServer(state: ServersState, workspaceId: string | null, folderId: string): LocalServer | null {
  if (workspaceId === null) return null;
  for (const id of state.ids) {
    const s = state.byId[id];
    if (s?.folder && s.folder.workspaceId === workspaceId && s.folder.folderId === folderId) return s;
  }
  return null;
}

/** Why the saved server of folder `folderId` could not be defined, or null. */
export function selectFolderServerProblem(state: ServersState, folderId: string): FolderServerProblem | null {
  return state.projectProblems.find((p) => p.folderId === folderId) ?? null;
}

/**
 * The badge the tree shows on folder `folderId` while its saved server
 * listens: the kind and the bound port (“WS :8081”); null otherwise.
 */
export function selectFolderServerBadge(state: ServersState, workspaceId: string | null, folderId: string): string | null {
  const s = selectFolderServer(state, workspaceId, folderId);
  return s !== null && s.state === "running" ? `${serverKindBadge(s.type)} :${s.boundPort}` : null;
}

const KIND_BADGES: Readonly<Record<string, string>> = {
  mock_http: "MOCK",
  echo_http: "HTTP",
  websocket: "WS",
  tcp: "TCP",
  webhook: "HOOK",
  sse: "SSE",
  udp: "UDP",
  grpc_mock: "GRPC",
  socketio: "SIO",
  graphql_mock: "GQL",
};

/** The short kind of server shown in badges: WS, TCP, MOCK, HTTP, HOOK, SSE, UDP, GRPC, SIO, GQL. */
export function serverKindBadge(type: string): string {
  return KIND_BADGES[type] ?? type.toUpperCase();
}

/** Whether a server holds (or is about to hold) its port. */
export function isServerActive(state: ServerState): boolean {
  return state === "starting" || state === "running" || state === "stopping";
}
