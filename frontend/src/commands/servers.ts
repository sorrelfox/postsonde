// Local server commands (SRV-01, SRV-03, MOCK-08): follow the servers and
// their traffic, create, start, stop and remove servers through the Go
// ServersService, and the servers saved in project folders (SRV-04):
// define one on a folder, change where a server listens, and bring the
// list in line with the open project. See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { formatError, toAppError, type AppError } from "../lib/errors";
import * as api from "../lib/servers";
import { TrafficSources } from "../lib/serverTraffic";
import type {
  GrpcBidiMode,
  GrpcMockConfig,
  GraphQLMockConfig,
  LocalServer,
  MockConfig,
  RouteDiagnostic,
  ServerCertificate,
  ServerState,
  ServerTls,
  SioServerConfig,
  SioSocket,
  SseServerConfig,
  SseSubscriber,
  Stores,
  TcpConnection,
  UdpPeer,
  UdpServerConfig,
  WebhookCapture,
  WebhookConfig,
  WsClient,
  WsRuleMatch,
  WsServerConfig,
} from "../stores";

/** The app's traffic logs, one MessageSource per server. Tests pass their own. */
export const trafficSources = new TrafficSources();

const STATES: readonly ServerState[] = ["stopped", "starting", "running", "stopping", "failed"];

function epochMs(rfc3339: string): number | null {
  if (rfc3339 === "") return null;
  const t = Date.parse(rfc3339);
  return Number.isFinite(t) ? t : null;
}

/** The mock routes of a DTO, with null lists as empty ones. */
export function toMockConfig(dto: api.MockHTTPConfigDTO | null | undefined): MockConfig | null {
  if (!dto) return null;
  return {
    strictTrailingSlash: dto.strictTrailingSlash,
    caseInsensitive: dto.caseInsensitive,
    routes: (dto.routes ?? []).map((r) => ({
      id: r.id,
      enabled: r.enabled,
      method: r.method,
      path: r.path,
      status: r.status,
      headers: (r.headers ?? []).map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
      body: { mode: r.body.mode, type: r.body.type, text: r.body.text },
      delay: { mode: r.delay.mode, ms: r.delay.ms, minMs: r.delay.minMs, maxMs: r.delay.maxMs },
      responses: (r.responses ?? []).map((a) => ({
        name: a.name,
        status: a.status,
        headers: (a.headers ?? []).map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
        body: { mode: a.body.mode, type: a.body.type, text: a.body.text },
        rules: (a.rules ?? []).map((u) => ({ source: u.source, name: u.name, op: u.op, value: u.value })),
        templated: a.templated,
      })),
      templated: r.templated,
    })),
  };
}

/** The DTO of mock routes, for UpdateConfig. */
export function toMockConfigDTO(config: MockConfig): api.MockHTTPConfigDTO {
  return {
    strictTrailingSlash: config.strictTrailingSlash,
    caseInsensitive: config.caseInsensitive,
    routes: config.routes.map((r) => ({
      id: r.id,
      enabled: r.enabled,
      method: r.method,
      path: r.path,
      status: r.status,
      headers: r.headers.map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
      body: { ...r.body },
      delay: { ...r.delay },
      responses: r.responses.map((a) => ({
        name: a.name,
        status: a.status,
        headers: a.headers.map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
        body: { ...a.body },
        rules: a.rules.map((u) => ({ ...u })),
        templated: a.templated,
      })),
      templated: r.templated,
    })),
  };
}

/** The settings of a WebSocket server DTO, with a null list as an empty one. */
export function toWsServerConfig(dto: api.WebSocketConfigDTO | null | undefined): WsServerConfig | null {
  if (!dto) return null;
  return {
    path: dto.path,
    subprotocols: dto.subprotocols ?? [],
    echo: dto.echo,
    maxMessageSize: dto.maxMessageSize,
    rules: (dto.rules ?? []).map((r) => ({
      enabled: r.enabled,
      match: toWsRuleMatch(r.match),
      pattern: r.pattern,
      reply: r.reply,
      delayMs: r.delayMs,
    })),
  };
}

/** A rule's match mode from Go; anything else (never sent) reads as exact. */
function toWsRuleMatch(match: string): WsRuleMatch {
  return match === "contains" || match === "regex" ? match : "exact";
}

/** The response of a webhook receiver DTO, with a null list as an empty one. */
export function toWebhookConfig(dto: api.WebhookConfigDTO | null | undefined): WebhookConfig | null {
  if (!dto) return null;
  return {
    status: dto.status,
    headers: (dto.headers ?? []).map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
    body: dto.body,
  };
}

/** The settings of an SSE server DTO, with a null list as an empty one. */
export function toSseConfig(dto: api.SSEConfigDTO | null | undefined): SseServerConfig | null {
  if (!dto) return null;
  return {
    path: dto.path,
    allowOrigin: dto.allowOrigin,
    keepAliveSec: dto.keepAliveSec,
    retryMs: dto.retryMs,
    autoId: dto.autoId,
    timers: (dto.timers ?? []).map((t) => ({ enabled: t.enabled, event: t.event, data: t.data, id: t.id, intervalMs: t.intervalMs })),
  };
}

/** The .proto files and canned responses of a gRPC mock server DTO, with null lists as empty ones. */
export function toGrpcMockConfig(dto: api.GRPCMockConfigDTO | null | undefined): GrpcMockConfig | null {
  if (!dto) return null;
  const rows = (list: readonly api.MockHeaderDTO[] | null | undefined) => (list ?? []).map((h) => ({ enabled: h.enabled, name: h.name, value: h.value }));
  return {
    files: dto.files ?? [],
    importPaths: dto.importPaths ?? [],
    reflection: dto.reflection,
    methods: (dto.methods ?? []).map((m) => ({
      service: m.service,
      method: m.method,
      messages: m.messages ?? [],
      status: m.status,
      statusMessage: m.statusMessage,
      headers: rows(m.headers),
      trailers: rows(m.trailers),
      delayMs: m.delayMs,
      intervalMs: m.intervalMs,
      bidi: (m.bidi === "stream" || m.bidi === "echo" ? m.bidi : "each") as GrpcBidiMode,
    })),
  };
}

/** The schema and overrides of a GraphQL mock server DTO, with null lists as empty ones. */
export function toGraphQLMockConfig(dto: api.GraphQLMockConfigDTO | null | undefined): GraphQLMockConfig | null {
  if (!dto) return null;
  return {
    path: dto.path,
    schemaFile: dto.schemaFile,
    schemaText: dto.schemaText,
    introspection: dto.introspection,
    listLength: dto.listLength,
    nullPercent: dto.nullPercent,
    seed: dto.seed,
    allowOrigin: dto.allowOrigin,
    overrides: (dto.overrides ?? []).map((o) => ({
      enabled: o.enabled,
      target: o.target,
      args: o.args,
      mode: o.mode === "error" ? "error" : "value",
      value: o.value,
      error: o.error,
      code: o.code,
    })),
  };
}

/** The settings of a Socket.IO server DTO, with null lists as empty ones. */
export function toSioConfig(dto: api.SocketIOConfigDTO | null | undefined): SioServerConfig | null {
  if (!dto) return null;
  const transports = dto.transports === "websocket" || dto.transports === "polling" ? dto.transports : "";
  const ackMode = dto.ackMode === "empty" || dto.ackMode === "none" ? dto.ackMode : "echo";
  return {
    path: dto.path,
    namespaces: dto.namespaces ?? [],
    auth: dto.auth,
    transports,
    allowOrigin: dto.allowOrigin,
    pingIntervalMs: dto.pingIntervalMs,
    pingTimeoutMs: dto.pingTimeoutMs,
    maxPayload: dto.maxPayload,
    echo: dto.echo,
    ackMode,
    rules: (dto.rules ?? []).map((r) => ({
      enabled: r.enabled,
      match: r.match === "contains" || r.match === "regex" ? r.match : "exact",
      pattern: r.pattern,
      ack: r.ack,
      emit: r.emit,
      emitArgs: r.emitArgs,
      delayMs: r.delayMs,
    })),
  };
}

export function toSioSocket(dto: api.SIOSocketDTO): SioSocket {
  return {
    id: dto.id,
    clientId: dto.clientId,
    sid: dto.sid,
    namespace: dto.namespace,
    transport: dto.transport,
    peer: dto.peer,
    target: dto.target,
    origin: dto.origin,
    userAgent: dto.userAgent,
    auth: dto.auth,
    rooms: dto.rooms ?? [],
    connectedAt: epochMs(dto.connectedAt),
    eventsIn: dto.eventsIn,
    eventsOut: dto.eventsOut,
  };
}

/** The replies of a UDP server DTO. */
export function toUdpConfig(dto: api.UDPConfigDTO | null | undefined): UdpServerConfig | null {
  if (!dto) return null;
  return { echo: dto.echo, fixedReply: dto.fixedReply, reply: dto.reply, replyFormat: dto.replyFormat === "hex" ? "hex" : "text" };
}

export function toSseSubscriber(dto: api.SSESubscriberDTO): SseSubscriber {
  return {
    id: dto.id,
    peer: dto.peer,
    target: dto.target,
    connectedAt: epochMs(dto.connectedAt),
    lastEventId: dto.lastEventId,
    origin: dto.origin,
    userAgent: dto.userAgent,
    eventsSent: dto.eventsSent,
    bytesSent: dto.bytesSent,
  };
}

export function toUdpPeer(dto: api.UDPPeerDTO): UdpPeer {
  return {
    addr: dto.addr,
    local: dto.local,
    firstSeen: epochMs(dto.firstSeen),
    lastSeen: epochMs(dto.lastSeen),
    datagramsIn: dto.datagramsIn,
    datagramsOut: dto.datagramsOut,
    bytesIn: dto.bytesIn,
    bytesOut: dto.bytesOut,
  };
}

export function toWebhookCapture(dto: api.CaptureSummaryDTO): WebhookCapture {
  return {
    seq: dto.seq,
    time: epochMs(dto.time),
    peer: dto.peer,
    method: dto.method,
    target: dto.target,
    contentType: dto.contentType,
    size: dto.size,
    truncated: dto.truncated,
  };
}

export function toWsClient(dto: api.WSClientDTO): WsClient {
  return {
    id: dto.id,
    peer: dto.peer,
    target: dto.target,
    subprotocol: dto.subprotocol,
    connectedAt: epochMs(dto.connectedAt),
    origin: dto.origin,
    userAgent: dto.userAgent,
    messagesIn: dto.messagesIn,
    messagesOut: dto.messagesOut,
    bytesIn: dto.bytesIn,
    bytesOut: dto.bytesOut,
  };
}

export function toTcpConnection(dto: api.TCPConnectionDTO): TcpConnection {
  return {
    id: dto.id,
    peer: dto.peer,
    local: dto.local,
    connectedAt: epochMs(dto.connectedAt),
    bytesIn: dto.bytesIn,
    bytesOut: dto.bytesOut,
    messagesIn: dto.messagesIn,
    messagesOut: dto.messagesOut,
  };
}

/** How a server serves TLS (SRV-05), from its DTO. */
export function toServerTls(dto: api.ServerTLSDTO | null | undefined): ServerTls | null {
  if (!dto) return null;
  return {
    certificate: dto.certificate === "files" ? "files" : "generated",
    certFile: dto.certFile,
    keyFile: dto.keyFile,
    pfxFile: dto.pfxFile,
    hasPassphrase: dto.hasPassphrase,
  };
}

function toServerCertificate(dto: api.ServerCertDTO | null | undefined): ServerCertificate | null {
  if (!dto) return null;
  return {
    subject: dto.subject,
    issuer: dto.issuer,
    notBefore: epochMs(dto.notBefore),
    notAfter: epochMs(dto.notAfter),
    sha256: dto.sha256,
    names: dto.names ?? [],
  };
}

/**
 * The TLS input that keeps a server's TLS as it is: the same certificate
 * and files, and the stored passphrase (null).
 */
export function keepTls(tls: ServerTls | null): ServerTlsInput | null {
  if (tls === null) return null;
  return { certificate: tls.certificate, certFile: tls.certFile, keyFile: tls.keyFile, pfxFile: tls.pfxFile, passphrase: null };
}

function toDiagnostics(dtos: readonly api.RouteDiagnosticDTO[] | null | undefined): RouteDiagnostic[] {
  return (dtos ?? []).map((d) => ({ ...d }));
}

export function toLocalServer(dto: api.ServerDTO): LocalServer {
  return {
    id: dto.id,
    type: dto.type,
    typeLabel: dto.typeLabel,
    name: dto.name,
    host: dto.host,
    port: dto.port,
    allowLan: dto.allowLan,
    tls: toServerTls(dto.tls),
    certificate: toServerCertificate(dto.certificate),
    state: STATES.includes(dto.state as ServerState) ? (dto.state as ServerState) : "failed",
    addresses: dto.addresses ?? [],
    boundPort: dto.boundPort,
    scope: dto.scope,
    warning: dto.warning ? { code: dto.warning.code, message: dto.warning.message } : null,
    startedAt: epochMs(dto.startedAt),
    stoppedAt: epochMs(dto.stoppedAt),
    error: dto.error ? { code: dto.error.code, message: dto.error.message, hint: dto.error.hint } : null,
    events: dto.events,
    mock: toMockConfig(dto.config?.mockHttp),
    webSocket: toWsServerConfig(dto.config?.webSocket),
    webhook: toWebhookConfig(dto.config?.webhook),
    sse: toSseConfig(dto.config?.sse),
    udp: toUdpConfig(dto.config?.udp),
    grpcMock: toGrpcMockConfig(dto.config?.grpcMock),
    socketIo: toSioConfig(dto.config?.socketIo),
    graphqlMock: toGraphQLMockConfig(dto.config?.graphqlMock),
    diagnostics: toDiagnostics(dto.diagnostics),
    folder: dto.folder
      ? { workspaceId: dto.folder.workspaceId, folderId: dto.folder.folderId, folderName: dto.folder.folderName }
      : null,
  };
}

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Loads the servers, their types, the recent traffic Go keeps, the
 * clients of running WebSocket servers, the connections of running TCP
 * servers, the requests webhook receivers keep, the subscribers of running
 * SSE servers, the peers of running UDP servers and the sockets of running
 * Socket.IO servers, then follows servers:changed, servers:traffic,
 * servers:clients, servers:connections, servers:captures,
 * servers:subscribers, servers:peers and servers:sockets. Live traffic that arrives
 * while loading is applied after the recent traffic; each event is applied
 * once. Returns the function that stops following.
 */
export function followServers(stores: Stores, sources: TrafficSources = trafficSources): () => void {
  let stopped = false;
  let pending: api.TrafficDTO[] | null = [];

  const offChanged = api.onServersChanged((ev) => {
    const servers = (ev.servers ?? []).map(toLocalServer);
    stores.servers.dispatch({ type: "servers/set", servers });
    sources.retain(servers.map((s) => s.id));
  });
  const offTraffic = api.onServersTraffic((ev) => {
    if (pending !== null) pending.push(...(ev.events ?? []));
    else sources.append(ev.events);
  });
  // Clients events carry the whole list, so the latest one wins; those
  // that arrive while loading replace what the load returns.
  const liveClients = new Set<string>();
  const offClients = api.onServersClients((ev) => {
    liveClients.add(ev.serverId);
    stores.servers.dispatch({ type: "servers/wsClients", id: ev.serverId, clients: (ev.clients ?? []).map(toWsClient) });
  });
  const liveConnections = new Set<string>();
  const offConnections = api.onServersConnections((ev) => {
    liveConnections.add(ev.serverId);
    stores.servers.dispatch({
      type: "servers/tcpConnections",
      id: ev.serverId,
      connections: (ev.connections ?? []).map(toTcpConnection),
    });
  });

  const liveCaptures = new Set<string>();
  const offCaptures = api.onServersCaptures((ev) => {
    liveCaptures.add(ev.serverId);
    stores.servers.dispatch({ type: "servers/webhookCaptures", id: ev.serverId, captures: (ev.captures ?? []).map(toWebhookCapture) });
  });

  const liveSubscribers = new Set<string>();
  const offSubscribers = api.onServersSubscribers((ev) => {
    liveSubscribers.add(ev.serverId);
    stores.servers.dispatch({ type: "servers/sseSubscribers", id: ev.serverId, subscribers: (ev.subscribers ?? []).map(toSseSubscriber) });
  });
  const livePeers = new Set<string>();
  const offPeers = api.onServersPeers((ev) => {
    livePeers.add(ev.serverId);
    stores.servers.dispatch({ type: "servers/udpPeers", id: ev.serverId, peers: (ev.peers ?? []).map(toUdpPeer) });
  });

  const liveSockets = new Set<string>();
  const offSockets = api.onServersSockets((ev) => {
    liveSockets.add(ev.serverId);
    stores.servers.dispatch({ type: "servers/sioSockets", id: ev.serverId, sockets: (ev.sockets ?? []).map(toSioSocket) });
  });

  const load = async () => {
    stores.servers.dispatch({ type: "servers/loading" });
    try {
      const [list, types] = await Promise.all([api.listServers(), api.listServerTypes()]);
      if (stopped) return;
      const servers = (list ?? []).map(toLocalServer);
      const recent = await Promise.all(
        servers.map((s) => api.getServerTraffic(s.id).then((events) => events ?? [], () => [])),
      );
      const clients = await Promise.all(
        servers.map((s) =>
          s.type === api.WEBSOCKET_TYPE && s.state === "running"
            ? api.listWsClients(s.id).then((list) => list ?? [], () => [])
            : Promise.resolve(null),
        ),
      );
      const connections = await Promise.all(
        servers.map((s) =>
          s.type === api.TCP_TYPE && s.state === "running"
            ? api.listTcpConnections(s.id).then((list) => list ?? [], () => [])
            : Promise.resolve(null),
        ),
      );
      const captures = await Promise.all(
        servers.map((s) =>
          s.type === api.WEBHOOK_TYPE ? api.listWebhookCaptures(s.id).then((list) => list ?? [], () => []) : Promise.resolve(null),
        ),
      );
      const subscribers = await Promise.all(
        servers.map((s) =>
          s.type === api.SSE_TYPE && s.state === "running"
            ? api.listSseSubscribers(s.id).then((list) => list ?? [], () => [])
            : Promise.resolve(null),
        ),
      );
      const peers = await Promise.all(
        servers.map((s) =>
          s.type === api.UDP_TYPE && s.state === "running"
            ? api.listUdpPeers(s.id).then((list) => list ?? [], () => [])
            : Promise.resolve(null),
        ),
      );
      const sockets = await Promise.all(
        servers.map((s) =>
          s.type === api.SOCKETIO_TYPE && s.state === "running"
            ? api.listSioSockets(s.id).then((list) => list ?? [], () => [])
            : Promise.resolve(null),
        ),
      );
      if (stopped) return;
      stores.servers.dispatch({ type: "servers/loaded", servers, types: types ?? [] });
      for (const events of recent) sources.append(events);
      servers.forEach((s, i) => {
        const list = clients[i];
        if (list && !liveClients.has(s.id)) {
          stores.servers.dispatch({ type: "servers/wsClients", id: s.id, clients: list.map(toWsClient) });
        }
        const conns = connections[i];
        if (conns && !liveConnections.has(s.id)) {
          stores.servers.dispatch({ type: "servers/tcpConnections", id: s.id, connections: conns.map(toTcpConnection) });
        }
        const kept = captures[i];
        if (kept && !liveCaptures.has(s.id)) {
          stores.servers.dispatch({ type: "servers/webhookCaptures", id: s.id, captures: kept.map(toWebhookCapture) });
        }
        const subs = subscribers[i];
        if (subs && !liveSubscribers.has(s.id)) {
          stores.servers.dispatch({ type: "servers/sseSubscribers", id: s.id, subscribers: subs.map(toSseSubscriber) });
        }
        const recentPeers = peers[i];
        if (recentPeers && !livePeers.has(s.id)) {
          stores.servers.dispatch({ type: "servers/udpPeers", id: s.id, peers: recentPeers.map(toUdpPeer) });
        }
        const socks = sockets[i];
        if (socks && !liveSockets.has(s.id)) {
          stores.servers.dispatch({ type: "servers/sioSockets", id: s.id, sockets: socks.map(toSioSocket) });
        }
      });
    } catch (err: unknown) {
      if (stopped) return;
      const error = toAppError(err);
      console.warn(`servers: ${error.message}`);
      stores.servers.dispatch({ type: "servers/failed", error: error.message });
    } finally {
      if (!stopped) {
        const queued = pending ?? [];
        pending = null;
        sources.append(queued);
      }
    }
  };
  void load();

  return () => {
    stopped = true;
    offChanged();
    offTraffic();
    offClients();
    offConnections();
    offCaptures();
    offSubscribers();
    offPeers();
    offSockets();
  };
}

/**
 * How a server is to serve TLS (SRV-05): the generated certificate, or the
 * user's files. `passphrase` null keeps the one stored for the file, ""
 * removes it; it is kept in Go's local secret storage, never in a project.
 */
export interface ServerTlsInput {
  certificate: "generated" | "files";
  certFile: string;
  keyFile: string;
  pfxFile: string;
  passphrase: string | null;
}

export interface NewServerInput {
  type: string;
  name?: string;
  host: string;
  port: number;
  allowLan: boolean;
  /** null or left out: plain text. */
  tls?: ServerTlsInput | null;
}

/**
 * Creates a server, selects it and starts it. Rejects only when it could
 * not be created; a server that could not start stays in the list as
 * failed, and `startError` says why.
 */
export async function createAndStartServer(
  stores: Stores,
  input: NewServerInput,
): Promise<{ server: LocalServer; startError: AppError | null }> {
  const created = toLocalServer(
    await run(() =>
      api.createServer({
        type: input.type,
        name: input.name ?? "",
        host: input.host,
        port: input.port,
        allowLan: input.allowLan,
        tls: input.tls ?? null,
        config: { mockHttp: null, webSocket: null, webhook: null },
      }),
    ),
  );
  stores.servers.dispatch({ type: "servers/put", server: created });
  stores.servers.dispatch({ type: "servers/select", id: created.id });
  try {
    return { server: await startServer(stores, created.id), startError: null };
  } catch (err: unknown) {
    const startError = toAppError(err);
    return { server: stores.servers.getState().byId[created.id] ?? created, startError };
  }
}

/**
 * Starts a server. The transitions arrive as servers:changed; the result is
 * put too. A failure leaves the server "failed" with the error (from the
 * event) and rejects.
 */
export async function startServer(stores: Stores, id: string): Promise<LocalServer> {
  const server = toLocalServer(await run(() => api.startServer(id)));
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/**
 * Brings the servers in line with the open project's saved server configs
 * (SRV-04): Go defines them, stopped (nothing is started), and the changes
 * arrive as servers:changed. The saved servers that could not be defined
 * go to `projectProblems`. A failure is logged.
 */
export async function syncProjectServers(stores: Stores): Promise<void> {
  try {
    const res = await api.syncProjectServers();
    stores.servers.dispatch({
      type: "servers/projectProblems",
      problems: (res?.problems ?? []).map((p) => ({
        folderId: p.folderId,
        folderName: p.folderName,
        message: p.error.hint ? `${p.error.message} ${p.error.hint}` : p.error.message,
      })),
    });
  } catch (err: unknown) {
    console.warn(`servers: ${formatError(err)}`);
  }
}

/** Defines a server of kind `type` in folder `folderId` of project `workspaceId`, saved in its _folder.json, stopped. */
export async function createFolderServer(
  stores: Stores,
  input: { workspaceId: string; folderId: string; type: string },
): Promise<LocalServer> {
  const server = toLocalServer(
    await run(() =>
      api.createFolderServer({
        workspaceId: input.workspaceId,
        folderId: input.folderId,
        server: {
          type: input.type,
          name: "",
          host: "localhost",
          port: 0,
          allowLan: false,
          tls: null,
          config: { mockHttp: null, webSocket: null, webhook: null },
        },
      }),
    ),
  );
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

export interface ServerSettings {
  name: string;
  host: string;
  port: number;
  allowLan: boolean;
  /** null serves plain text; keepTls(server.tls) leaves TLS as it is. */
  tls: ServerTlsInput | null;
}

/**
 * Changes a server's name, host, port, LAN access and TLS (saved in its
 * folder for a saved server). Certificate files are read by Go now, so a
 * bad file or passphrase rejects here.
 */
export async function updateServerSettings(stores: Stores, id: string, settings: ServerSettings): Promise<LocalServer> {
  const server = toLocalServer(await run(() => api.updateServerSettings(id, settings)));
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** Stops a server. */
export async function stopServer(stores: Stores, id: string): Promise<LocalServer> {
  const server = toLocalServer(await run(() => api.stopServer(id)));
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** Stops and deletes a server with its log. */
export async function removeServer(stores: Stores, id: string, sources: TrafficSources = trafficSources): Promise<void> {
  await run(() => api.removeServer(id));
  const state = stores.servers.getState();
  stores.servers.dispatch({ type: "servers/set", servers: state.ids.filter((x) => x !== id).map((x) => state.byId[x]) });
  sources.remove(id);
}

/** Empties a server's log, here and in Go (so a reloaded UI does not bring it back). */
export async function clearServerTraffic(id: string, sources: TrafficSources = trafficSources): Promise<void> {
  sources.clear(id);
  await run(() => api.clearServerTraffic(id));
}

/** Shows a server's traffic in the work area; null closes it. */
export function selectServer(stores: Stores, id: string | null): void {
  stores.servers.dispatch({ type: "servers/select", id });
}
