// Calls into the Go ServersService (internal/app/services/servers): the
// local servers (SRV-01, SRV-03, MOCK-08) and the servers saved in project
// folders (SRV-04). Servers are defined, started and stopped here; their
// state arrives as servers:changed and their traffic as batched
// servers:traffic events. See docs/servers.md.
import { Events } from "@wailsio/runtime";
import {
  ServersService,
  type CaptureDTO,
  type CaptureSummaryDTO,
  type CapturesEventDTO,
  type ChangedEventDTO,
  type ClientsEventDTO,
  type ConfigDTO,
  type ConnectionsEventDTO,
  type CreateInputDTO,
  type FolderLinkDTO,
  type FolderServerInputDTO,
  type FolderServerProblemDTO,
  type GraphQLDTO,
  type GraphQLMockConfigDTO,
  type GraphQLMockDescriptionDTO,
  type GraphQLMockOverrideDTO,
  type GraphQLMockProblemDTO,
  type GraphQLMockTargetDTO,
  type GRPCDTO,
  type GRPCMockConfigDTO,
  type GRPCMockMethodDTO,
  type HeaderDTO,
  type HTTPDTO,
  type LocalCADTO,
  type MockHeaderDTO,
  type MockHTTPConfigDTO,
  type MockOpenAPIDiagnosticDTO,
  type MockOpenAPIInputDTO,
  type MockOpenAPIResultDTO,
  type MockOpenAPIRouteDTO,
  type MockRouteDTO,
  type PeersEventDTO,
  type ProjectServersDTO,
  type RouteDiagnosticDTO,
  type RouteMatchDTO,
  type ServerCertDTO,
  type ServerDTO,
  type ServerSettingsDTO,
  type ServerTLSDTO,
  type ServerTLSInputDTO,
  type SIODTO,
  type SIOEmitInputDTO,
  type SIOEmitResultDTO,
  type SIORuleDTO,
  type SIOSocketDTO,
  type SocketIOConfigDTO,
  type SocketsEventDTO,
  type SSEConfigDTO,
  type SSEDTO,
  type SSESendInputDTO,
  type SSESendResultDTO,
  type SSESubscriberDTO,
  type SSETimerDTO,
  type SubscribersEventDTO,
  type TCPConnectionDTO,
  type TCPDTO,
  type TCPSendResultDTO,
  type TrafficDTO,
  type TrafficEventDTO,
  type TypeDTO,
  type UDPConfigDTO,
  type UDPDTO,
  type UDPPeerDTO,
  type UDPSendResultDTO,
  type WarningDTO,
  type WebSocketConfigDTO,
  type WebhookConfigDTO,
  type WSBroadcastResultDTO,
  type WSClientDTO,
  type WSDTO,
  type WSMessageDTO,
  type WSRuleDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers";
import type { CancellablePromise } from "./calls";
import type { StartedDTO } from "./operations";
import type { TcpLineEnding, TcpSendMode } from "./tcp";

export type {
  CaptureDTO,
  CaptureSummaryDTO,
  CapturesEventDTO,
  ChangedEventDTO,
  ClientsEventDTO,
  ConfigDTO,
  ConnectionsEventDTO,
  CreateInputDTO,
  FolderLinkDTO,
  FolderServerInputDTO,
  FolderServerProblemDTO,
  GraphQLDTO,
  GraphQLMockConfigDTO,
  GraphQLMockDescriptionDTO,
  GraphQLMockOverrideDTO,
  GraphQLMockProblemDTO,
  GraphQLMockTargetDTO,
  GRPCDTO,
  GRPCMockConfigDTO,
  GRPCMockMethodDTO,
  HeaderDTO,
  HTTPDTO,
  LocalCADTO,
  MockHeaderDTO,
  MockHTTPConfigDTO,
  MockOpenAPIDiagnosticDTO,
  MockOpenAPIInputDTO,
  MockOpenAPIResultDTO,
  MockOpenAPIRouteDTO,
  MockRouteDTO,
  PeersEventDTO,
  ProjectServersDTO,
  RouteDiagnosticDTO,
  RouteMatchDTO,
  ServerCertDTO,
  ServerDTO,
  ServerSettingsDTO,
  ServerTLSDTO,
  ServerTLSInputDTO,
  SIODTO,
  SIOEmitInputDTO,
  SIOEmitResultDTO,
  SIORuleDTO,
  SIOSocketDTO,
  SocketIOConfigDTO,
  SocketsEventDTO,
  SSEConfigDTO,
  SSEDTO,
  SSESendInputDTO,
  SSESendResultDTO,
  SSESubscriberDTO,
  SSETimerDTO,
  SubscribersEventDTO,
  TCPConnectionDTO,
  TCPDTO,
  TCPSendResultDTO,
  TrafficDTO,
  TrafficEventDTO,
  TypeDTO,
  UDPConfigDTO,
  UDPDTO,
  UDPPeerDTO,
  UDPSendResultDTO,
  WarningDTO,
  WebSocketConfigDTO,
  WebhookConfigDTO,
  WSBroadcastResultDTO,
  WSClientDTO,
  WSDTO,
  WSMessageDTO,
  WSRuleDTO,
};

/** Sent by Go after every change of a server; carries every server. */
export const SERVERS_CHANGED_EVENT = "servers:changed";
/** Traffic of the running servers, in batches (at most 20 a second). */
export const SERVERS_TRAFFIC_EVENT = "servers:traffic";
/** The clients of one WebSocket server after they changed (at most five a second). */
export const SERVERS_CLIENTS_EVENT = "servers:clients";
/** The open connections of one TCP server after they changed (at most five a second). */
export const SERVERS_CONNECTIONS_EVENT = "servers:connections";
/** The requests one webhook receiver keeps, after they changed (at most five a second). */
export const SERVERS_CAPTURES_EVENT = "servers:captures";
/** The subscribers of one SSE server after they changed (at most five a second). */
export const SERVERS_SUBSCRIBERS_EVENT = "servers:subscribers";
/** The recent peers of one UDP server after they changed (at most five a second). */
export const SERVERS_PEERS_EVENT = "servers:peers";
/** The sockets of one Socket.IO server after they changed (at most five a second). */
export const SERVERS_SOCKETS_EVENT = "servers:sockets";

/** The Echo HTTP server type (internal/core/servers/echo). */
export const ECHO_HTTP_TYPE = "echo_http";
/** The Mock HTTP server type (internal/core/servers/mockhttp). */
export const MOCK_HTTP_TYPE = "mock_http";
/** The WebSocket server type (internal/core/servers/wsserver, SSRV-01). */
export const WEBSOCKET_TYPE = "websocket";
/** The TCP server type (internal/core/servers/tcpserver, SSRV-05). */
export const TCP_TYPE = "tcp";
/** The webhook receiver type (internal/core/servers/webhook, MOCK-08). */
export const WEBHOOK_TYPE = "webhook";
/** The SSE server type (internal/core/servers/sseserver, SSRV-04). */
export const SSE_TYPE = "sse";
/** The UDP server type (internal/core/servers/udpserver, SSRV-06). */
export const UDP_TYPE = "udp";
/** The gRPC mock server type (internal/core/servers/grpcmock, SSRV-07). */
export const GRPC_MOCK_TYPE = "grpc_mock";
/** The Socket.IO server type (internal/core/servers/sioserver, SSRV-03). */
export const SOCKETIO_TYPE = "socketio";
/** The GraphQL mock server type (internal/core/servers/graphqlmock, SSRV-08). */
export const GRAPHQL_MOCK_TYPE = "graphql_mock";

/** Error codes the UI reacts to (internal/core/servers, internal/core/netx/listen). */
export const SERVER_CODES = {
  notFound: "servers.not_found",
  addressInUse: "listen.address_in_use",
  lanNotAllowed: "listen.lan_not_allowed",
  invalidHost: "listen.invalid_host",
  invalidPort: "listen.invalid_port",
  notRunning: "servers.not_running",
  clientNotFound: "wsserver.client_not_found",
  connectionNotFound: "tcpserver.connection_not_found",
  subscriberNotFound: "sseserver.subscriber_not_found",
  peerNotFound: "udpserver.peer_not_found",
  socketNotFound: "sioserver.socket_not_found",
  captureNotFound: "servers.capture_not_found",
  captureTruncated: "servers.capture_truncated",
  invalidReplayUrl: "servers.invalid_replay_url",
  active: "servers.active",
  folderHasServer: "servers.folder_has_server",
  pickUnavailable: "servers.pick_unavailable",
  revealUnavailable: "servers.reveal_unavailable",
} as const;

/** The kinds of certificate file PickTLSFile asks for. */
export type TlsFileKind = "cert" | "key" | "pfx";

/**
 * Shows the native dialog for a server certificate file (SRV-05): a PEM
 * certificate, its PEM key or a PKCS#12 file. Resolves with the absolute
 * path, or "" when the user cancels.
 */
export function pickTlsFile(kind: TlsFileKind): CancellablePromise<string> {
  return ServersService.PickTLSFile(kind);
}

/**
 * The local CA that signs the generated certificates (SRV-05), created in
 * the data directory the first time: its ca.pem path, fingerprint, PEM
 * and whether PostSonde's own clients trust it (CA bundles setting).
 */
export function getLocalCA(): CancellablePromise<LocalCADTO> {
  return ServersService.LocalCA();
}

/** Shows the local CA's ca.pem in the file manager. */
export function revealLocalCA(): CancellablePromise<void> {
  return ServersService.RevealLocalCA();
}

/** The kinds of server that can be created. */
export function listServerTypes(): CancellablePromise<TypeDTO[] | null> {
  return ServersService.Types();
}

/** Every server, in the order created. */
export function listServers(): CancellablePromise<ServerDTO[] | null> {
  return ServersService.List();
}

/** Defines a server, stopped. */
export function createServer(input: CreateInputDTO): CancellablePromise<ServerDTO> {
  return ServersService.Create(input);
}

/**
 * Replaces a server's configuration (a mock server's routes); a running
 * server applies it at once. Resolves with the server and the diagnostics
 * of its routes.
 */
export function updateServerConfig(id: string, config: ConfigDTO): CancellablePromise<ServerDTO> {
  return ServersService.UpdateConfig(id, config);
}

/**
 * Shows the native dialog to choose an OpenAPI or Swagger document (MOCK-09).
 * Resolves with its path, or "" when the user cancels.
 */
export function pickOpenApiFile(): CancellablePromise<string> {
  return ServersService.PickOpenAPIFile();
}

/**
 * Reads an OpenAPI document and generates one mock route per operation,
 * merged into `input.config` ("merge" keeps existing routes, "replace" drops
 * them). Nothing changes on the server: apply the result's config with
 * updateServerConfig. Rejects with the reader's error (openapi.*) when the
 * file is not an OpenAPI document.
 */
export function generateMockRoutes(input: MockOpenAPIInputDTO): CancellablePromise<MockOpenAPIResultDTO> {
  return ServersService.GenerateMockRoutes(input);
}

/**
 * Loads the schema of a GraphQL mock configuration as the server would and
 * checks its overrides (SSRV-08): the counts, the fields and types overrides
 * can answer, the problems and the schema as SDL. Nothing changes on any
 * server. Rejects when the schema does not load (graphqlmock.schema_failed,
 * fileaccess.*).
 */
export function describeGraphQLMock(config: GraphQLMockConfigDTO): CancellablePromise<GraphQLMockDescriptionDTO> {
  return ServersService.DescribeGraphQLMock(config);
}

/**
 * Shows the native dialog for a GraphQL mock's schema file (SDL or
 * introspection JSON). Resolves with the path to store (relative to the
 * project when inside it), or "" when the user cancels.
 */
export function pickGraphQLSchemaFile(): CancellablePromise<string> {
  return ServersService.PickGraphQLSchemaFile();
}

/**
 * Starts a server. Rejects when it cannot listen (`listen.address_in_use`,
 * `listen.lan_not_allowed`, …); the server is then "failed" with that error.
 */
export function startServer(id: string): CancellablePromise<ServerDTO> {
  return ServersService.Start(id);
}

/**
 * Changes a server's name, host, port and LAN access. The address cannot
 * change while it runs (`servers.active`). A saved server's folder file is
 * written too (SRV-04).
 */
export function updateServerSettings(id: string, settings: ServerSettingsDTO): CancellablePromise<ServerDTO> {
  return ServersService.UpdateServer(id, settings);
}

/**
 * Defines a server in a folder of the open project (SRV-04): it is saved
 * in the folder's _folder.json and returned stopped.
 */
export function createFolderServer(input: FolderServerInputDTO): CancellablePromise<ServerDTO> {
  return ServersService.CreateFolderServer(input);
}

/**
 * Brings the servers in line with the open project's saved server configs
 * (SRV-04): defines them, stopped, and drops those whose definition is
 * gone. Nothing is started.
 */
export function syncProjectServers(): CancellablePromise<ProjectServersDTO> {
  return ServersService.SyncProject();
}

/** Stops a server; requests in progress get a few seconds to finish. */
export function stopServer(id: string): CancellablePromise<ServerDTO> {
  return ServersService.Stop(id);
}

/** Stops a server if needed and deletes it with its traffic. */
export function removeServer(id: string): CancellablePromise<void> {
  return ServersService.Remove(id);
}

/** The recent traffic Go keeps for a server (up to 1,000 events), oldest first. */
export function getServerTraffic(id: string): CancellablePromise<TrafficDTO[] | null> {
  return ServersService.Traffic(id);
}

/** Forgets the recent traffic Go keeps for a server. */
export function clearServerTraffic(id: string): CancellablePromise<void> {
  return ServersService.ClearTraffic(id);
}

/** The clients connected to a WebSocket server, oldest first ([] when it is not running). */
export function listWsClients(serverId: string): CancellablePromise<WSClientDTO[] | null> {
  return ServersService.Clients(serverId);
}

/** Sends one message to a client of a running WebSocket server. */
export function sendToWsClient(serverId: string, clientId: string, message: WSMessageDTO): CancellablePromise<void> {
  return ServersService.SendToClient({ serverId, clientId, message });
}

/** Sends one message to every client of a running WebSocket server. */
export function broadcastToWsClients(serverId: string, message: WSMessageDTO): CancellablePromise<WSBroadcastResultDTO> {
  return ServersService.Broadcast({ serverId, message });
}

/**
 * Closes a client's connection with a close code (0 for 1000) and reason;
 * resolves once the client is gone. An invalid code rejects with
 * `wsframe.invalid_argument`.
 */
export function disconnectWsClient(serverId: string, clientId: string, code: number, reason: string): CancellablePromise<void> {
  return ServersService.DisconnectClient({ serverId, clientId, code, reason });
}

/** The open connections of a TCP server, oldest first ([] when it is not running). */
export function listTcpConnections(serverId: string): CancellablePromise<TCPConnectionDTO[] | null> {
  return ServersService.Connections(serverId);
}

/**
 * Sends text (with the line ending) or hex bytes to a connection of a
 * running TCP server. Resolves with the number of bytes sent; bad hex
 * rejects with a `hexbytes.*` error.
 */
export function sendToTcpConnection(
  serverId: string,
  connectionId: string,
  mode: TcpSendMode,
  text: string,
  lineEnding: TcpLineEnding,
): CancellablePromise<TCPSendResultDTO> {
  return ServersService.SendToConnection({ serverId, connectionId, mode, text, lineEnding });
}

/** Closes a connection of a TCP server; resolves once it is closed. */
export function closeTcpConnection(serverId: string, connectionId: string): CancellablePromise<void> {
  return ServersService.CloseConnection({ serverId, connectionId });
}

/** The subscribers of an SSE server, oldest first ([] when it is not running). */
export function listSseSubscribers(serverId: string): CancellablePromise<SSESubscriberDTO[] | null> {
  return ServersService.Subscribers(serverId);
}

/**
 * Sends an event to one subscriber of a running SSE server, or to all when
 * `subscriberId` is "". Its type, data and ID may hold dynamic values
 * ({{$timestamp}}), rendered by Go. Resolves with how many subscribers it
 * reached and the ID it carried; a type or ID with a line break rejects
 * with `sseserver.invalid_message`.
 */
export function sendSseEvent(input: SSESendInputDTO): CancellablePromise<SSESendResultDTO> {
  return ServersService.SendEvent(input);
}

/** Ends a subscriber's stream; resolves once it is gone. */
export function disconnectSseSubscriber(serverId: string, subscriberId: string): CancellablePromise<void> {
  return ServersService.DisconnectSubscriber({ serverId, subscriberId });
}

/** The peers a UDP server heard from recently, most recent first ([] when it is not running). */
export function listUdpPeers(serverId: string): CancellablePromise<UDPPeerDTO[] | null> {
  return ServersService.Peers(serverId);
}

/**
 * Sends text (with the line ending) or hex bytes as one datagram to a
 * recent peer of a running UDP server. Resolves with its size.
 */
export function sendUdpDatagram(
  serverId: string,
  peer: string,
  mode: TcpSendMode,
  text: string,
  lineEnding: TcpLineEnding,
): CancellablePromise<UDPSendResultDTO> {
  return ServersService.SendDatagram({ serverId, peer, mode, text, lineEnding });
}

/** The sockets of a Socket.IO server, by client and namespace ([] when it is not running). */
export function listSioSockets(serverId: string): CancellablePromise<SIOSocketDTO[] | null> {
  return ServersService.Sockets(serverId);
}

/**
 * Emits an event to one socket of a running Socket.IO server, to the
 * sockets in a room, or to every socket (both "" ). The arguments are
 * JSON text ({"$base64": "…"} is binary; dynamic values are rendered by
 * Go). Resolves with how many sockets it reached.
 */
export function emitToSioSockets(input: SIOEmitInputDTO): CancellablePromise<SIOEmitResultDTO> {
  return ServersService.EmitToSockets(input);
}

/** Sends DISCONNECT to a socket, or closes its client's whole connection. */
export function disconnectSioSocket(serverId: string, socketId: string, closeClient: boolean): CancellablePromise<void> {
  return ServersService.DisconnectSocket({ serverId, socketId, closeClient });
}

/** Puts a socket in exactly these rooms. */
export function setSioSocketRooms(serverId: string, socketId: string, rooms: string[]): CancellablePromise<void> {
  return ServersService.SetSocketRooms({ serverId, socketId, rooms });
}

/** Calls `callback` with every servers:sockets event. Returns the unsubscribe function. */
export function onServersSockets(callback: (event: SocketsEventDTO) => void): () => void {
  return Events.On(SERVERS_SOCKETS_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:subscribers event. Returns the unsubscribe function. */
export function onServersSubscribers(callback: (event: SubscribersEventDTO) => void): () => void {
  return Events.On(SERVERS_SUBSCRIBERS_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:peers event. Returns the unsubscribe function. */
export function onServersPeers(callback: (event: PeersEventDTO) => void): () => void {
  return Events.On(SERVERS_PEERS_EVENT, (ev) => callback(ev.data));
}

/** The requests a webhook receiver keeps (up to 200), oldest first. */
export function listWebhookCaptures(serverId: string): CancellablePromise<CaptureSummaryDTO[] | null> {
  return ServersService.Captures(serverId);
}

/** One kept request in full; rejects with `servers.capture_not_found` once it is no longer kept. */
export function getWebhookCapture(serverId: string, seq: number): CancellablePromise<CaptureDTO> {
  return ServersService.Capture({ serverId, seq });
}

/**
 * Starts replaying a kept request to `url` (MOCK-08). Resolves with the
 * operation ID at once: the replay is an HTTP send, so its progress,
 * response and end arrive as http:op:* events (lib/http.ts, HTTP_DOMAIN).
 */
export function replayWebhookRequest(serverId: string, seq: number, url: string): CancellablePromise<StartedDTO> {
  return ServersService.Replay({ serverId, seq, url });
}

/** Calls `callback` with every servers:captures event. Returns the unsubscribe function. */
export function onServersCaptures(callback: (event: CapturesEventDTO) => void): () => void {
  return Events.On(SERVERS_CAPTURES_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:connections event. Returns the unsubscribe function. */
export function onServersConnections(callback: (event: ConnectionsEventDTO) => void): () => void {
  return Events.On(SERVERS_CONNECTIONS_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:clients event. Returns the unsubscribe function. */
export function onServersClients(callback: (event: ClientsEventDTO) => void): () => void {
  return Events.On(SERVERS_CLIENTS_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:changed event. Returns the unsubscribe function. */
export function onServersChanged(callback: (event: ChangedEventDTO) => void): () => void {
  return Events.On(SERVERS_CHANGED_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every servers:traffic batch. Returns the unsubscribe function. */
export function onServersTraffic(callback: (event: TrafficEventDTO) => void): () => void {
  return Events.On(SERVERS_TRAFFIC_EVENT, (ev) => callback(ev.data));
}
