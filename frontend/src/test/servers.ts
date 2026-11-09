// Fixtures for the local server tests (SRV-01).
import type { CaptureSummaryDTO, ServerDTO, SIOSocketDTO, SSESubscriberDTO, TCPConnectionDTO, TrafficDTO, UDPPeerDTO, WSClientDTO } from "../lib/servers";

/** A running WebSocket server (SSRV-01) on /ws. */
export function wsServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "websocket",
    typeLabel: "WebSocket",
    name: "WebSocket",
    config: { mockHttp: null, webSocket: { path: "/ws", subprotocols: ["chat"], echo: false, maxMessageSize: 0, rules: null }, webhook: null },
    ...over,
  });
}

export function wsClientDTO(over: Partial<WSClientDTO> = {}): WSClientDTO {
  return {
    id: "1",
    connId: 1,
    peer: "127.0.0.1:60001",
    target: "/ws",
    subprotocol: "chat",
    connectedAt: "2026-09-16T10:00:05Z",
    origin: "",
    userAgent: "websocat",
    messagesIn: 2,
    messagesOut: 1,
    bytesIn: 10,
    bytesOut: 5,
    ...over,
  };
}

/** A running TCP server (SSRV-05). */
export function tcpServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({ type: "tcp", typeLabel: "TCP", name: "TCP", ...over });
}

export function tcpConnectionDTO(over: Partial<TCPConnectionDTO> = {}): TCPConnectionDTO {
  return {
    id: "1",
    connId: 1,
    peer: "127.0.0.1:60001",
    local: "127.0.0.1:52341",
    connectedAt: "2026-09-16T10:00:05Z",
    bytesIn: 12,
    bytesOut: 4,
    messagesIn: 2,
    messagesOut: 1,
    ...over,
  };
}

/** A running webhook receiver (MOCK-08) answering 202. */
export function webhookServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "webhook",
    typeLabel: "Webhook receiver",
    name: "Webhook receiver",
    config: { mockHttp: null, webSocket: null, webhook: { status: 202, headers: [], body: "" } },
    ...over,
  });
}

/** A running SSE server (SSRV-04) on /events with automatic IDs. */
export function sseServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "sse",
    typeLabel: "SSE",
    name: "SSE",
    config: {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      sse: { path: "/events", allowOrigin: "", keepAliveSec: 15, retryMs: 0, autoId: true, timers: null },
    },
    ...over,
  });
}

export function sseSubscriberDTO(over: Partial<SSESubscriberDTO> = {}): SSESubscriberDTO {
  return {
    id: "1",
    connId: 1,
    peer: "127.0.0.1:60001",
    target: "/events",
    connectedAt: "2026-09-16T10:00:05Z",
    lastEventId: "",
    origin: "",
    userAgent: "curl/8.7",
    eventsSent: 3,
    bytesSent: 60,
    ...over,
  };
}

/** A running UDP server (SSRV-06) that echoes. */
export function udpServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "udp",
    typeLabel: "UDP",
    name: "UDP",
    config: { mockHttp: null, webSocket: null, webhook: null, udp: { echo: true, fixedReply: false, reply: "", replyFormat: "text" } },
    ...over,
  });
}

/** A running gRPC mock server (SSRV-07) serving proto/orders.proto, with a canned GetOrder. */
export function grpcMockServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "grpc_mock",
    typeLabel: "gRPC mock",
    name: "gRPC mock",
    config: {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      grpcMock: {
        files: ["proto/orders.proto"],
        importPaths: [],
        reflection: true,
        methods: [
          {
            service: "shop.v1.Orders",
            method: "GetOrder",
            messages: ['{"id": "o-1"}'],
            status: 0,
            statusMessage: "",
            headers: [],
            trailers: [],
            delayMs: 0,
            intervalMs: 0,
            bidi: "each",
          },
        ],
      },
    },
    ...over,
  });
}

/** A running GraphQL mock server (SSRV-08) with pasted SDL and one override. */
export function graphqlMockServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "graphql_mock",
    typeLabel: "GraphQL mock",
    name: "GraphQL mock",
    config: {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      graphqlMock: {
        path: "/graphql",
        schemaFile: "",
        schemaText: "type Query { user(id: ID!): User }\ntype User { id: ID! name: String! }",
        introspection: true,
        listLength: 2,
        nullPercent: 0,
        seed: 0,
        allowOrigin: "",
        overrides: [{ enabled: true, target: "Query.user", args: '{"id": "1"}', mode: "value", value: '{"name": "Ada"}', error: "", code: "" }],
      },
    },
    ...over,
  });
}

/** A running Socket.IO server (SSRV-03) with the defaults and one rule. */
export function sioServerDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "socketio",
    typeLabel: "Socket.IO",
    name: "Socket.IO",
    config: {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      socketIo: {
        path: "/socket.io/",
        namespaces: null,
        auth: "",
        transports: "",
        allowOrigin: "",
        pingIntervalMs: 25000,
        pingTimeoutMs: 20000,
        maxPayload: 1000000,
        echo: false,
        ackMode: "echo",
        rules: [{ enabled: true, match: "exact", pattern: "ping", ack: '["pong"]', emit: "", emitArgs: "", delayMs: 0 }],
      },
    },
    ...over,
  });
}

export function sioSocketDTO(over: Partial<SIOSocketDTO> = {}): SIOSocketDTO {
  return {
    id: "1",
    clientId: "1",
    sid: "fjKYg1eVmvKzwVSiAAAD",
    namespace: "/",
    session: "Z3cq0WmDdBdnFAXfAAAB",
    transport: "websocket",
    peer: "127.0.0.1:60001",
    target: "/socket.io/?EIO=4&transport=websocket",
    origin: "",
    userAgent: "node",
    auth: "",
    rooms: null,
    connectedAt: "2026-09-16T10:00:05Z",
    eventsIn: 2,
    eventsOut: 1,
    ...over,
  };
}

export function udpPeerDTO(over: Partial<UDPPeerDTO> = {}): UDPPeerDTO {
  return {
    addr: "127.0.0.1:60001",
    local: "127.0.0.1:52341",
    firstSeen: "2026-09-16T10:00:05Z",
    lastSeen: "2026-09-16T10:00:06Z",
    datagramsIn: 2,
    datagramsOut: 2,
    bytesIn: 10,
    bytesOut: 10,
    ...over,
  };
}

/** A request a webhook receiver keeps. */
export function captureSummaryDTO(over: Partial<CaptureSummaryDTO> = {}): CaptureSummaryDTO {
  return {
    seq: 3,
    time: "2026-09-16T10:00:02Z",
    peer: "127.0.0.1:60003",
    method: "POST",
    target: "/hooks/pay?id=7",
    contentType: "application/json",
    size: 16,
    truncated: false,
    ...over,
  };
}

export function serverDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return {
    id: "srv_1",
    type: "echo_http",
    typeLabel: "Echo HTTP",
    name: "Echo HTTP",
    host: "localhost",
    port: 0,
    allowLan: false,
    tls: null,
    certificate: null,
    state: "stopped",
    addresses: [],
    boundPort: 0,
    scope: "",
    warning: null,
    startedAt: "",
    stoppedAt: "",
    error: null,
    events: 0,
    config: { mockHttp: null, webSocket: null, webhook: null },
    diagnostics: [],
    folder: null,
    ...over,
  };
}

export function runningDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return serverDTO({
    state: "running",
    addresses: ["127.0.0.1:52341", "[::1]:52341"],
    boundPort: 52341,
    scope: "loopback",
    startedAt: "2026-09-16T10:00:00Z",
    ...over,
  });
}

export function trafficDTO(over: Partial<TrafficDTO> = {}): TrafficDTO {
  return {
    serverId: "srv_1",
    seq: 1,
    time: "2026-09-16T10:00:01.250Z",
    peer: "127.0.0.1:60000",
    connId: 1,
    direction: "in",
    kind: "request",
    summary: "GET /users?id=1",
    text: "",
    dataBase64: "",
    binary: false,
    size: 0,
    truncated: false,
    http: {
      method: "GET",
      target: "/users?id=1",
      proto: "HTTP/1.1",
      status: 0,
      headers: [
        { name: "Host", value: "127.0.0.1:52341" },
        { name: "Accept", value: "*/*" },
      ],
      route: null,
      delayMs: 0,
    },
    ws: null,
    tcp: null,
    captured: false,
    ...over,
  };
}
