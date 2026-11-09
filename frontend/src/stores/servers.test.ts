import { describe, expect, it } from "vitest";
import {
  initialServersState,
  isServerActive,
  selectMockConfig,
  selectSelectedServer,
  selectSioSelectionGone,
  selectSioTargetSocket,
  selectTcpComposer,
  selectTcpConnections,
  selectTcpSelectionGone,
  selectTcpTargetConnection,
  selectTcpView,
  selectWebhookCapture,
  selectWebhookCaptures,
  selectWebhookReplayUrl,
  selectWsClients,
  selectWsComposer,
  selectWsSelectionGone,
  selectWsTargetClient,
  selectSseSelectionGone,
  selectSseTargetSubscriber,
  selectUdpSelectionGone,
  selectUdpTargetPeer,
  type SseSubscriber,
  type UdpPeer,
  serversReducer,
  type SioSocket,
  type TcpConnection,
  type WebhookCapture,
  type WsClient,
  type MockConfig,
  type LocalServer,
  type ServersState,
} from "./servers";

function server(id: string, over: Partial<LocalServer> = {}): LocalServer {
  return {
    id,
    type: "echo_http",
    typeLabel: "Echo HTTP",
    name: id,
    host: "",
    port: 0,
    allowLan: false,
    tls: null,
    certificate: null,
    state: "stopped",
    addresses: [],
    boundPort: 0,
    scope: "",
    warning: null,
    startedAt: null,
    stoppedAt: null,
    error: null,
    events: 0,
    mock: null,
    webSocket: null,
    webhook: null,
    sse: null,
    udp: null,
    grpcMock: null,
    socketIo: null,
    graphqlMock: null,
    diagnostics: [],
    folder: null,
    ...over,
  };
}

const types = [{ name: "echo_http", label: "Echo HTTP" }];

describe("serversReducer", () => {
  it("loads servers and types", () => {
    let s = serversReducer(initialServersState, { type: "servers/loading" });
    expect(s.status).toBe("loading");
    expect(serversReducer(s, { type: "servers/loading" })).toBe(s);
    s = serversReducer(s, { type: "servers/loaded", servers: [server("a"), server("b")], types });
    expect(s).toMatchObject({ status: "ready", ids: ["a", "b"], types, error: null });
    s = serversReducer(s, { type: "servers/failed", error: "boom" });
    expect(s).toMatchObject({ status: "error", error: "boom", ids: ["a", "b"] });
  });

  it("replaces the list, keeping the selection only while the server exists", () => {
    let s: ServersState = serversReducer(initialServersState, { type: "servers/set", servers: [server("a"), server("b")] });
    s = serversReducer(s, { type: "servers/select", id: "b" });
    expect(selectSelectedServer(s)?.id).toBe("b");
    s = serversReducer(s, { type: "servers/set", servers: [server("b", { state: "running" }), server("a")] });
    expect(s.ids).toEqual(["b", "a"]);
    expect(selectSelectedServer(s)?.state).toBe("running");
    s = serversReducer(s, { type: "servers/set", servers: [server("a")] });
    expect(s.selectedId).toBeNull();
    expect(selectSelectedServer(s)).toBeNull();
  });

  it("puts one server and ignores unknown selections", () => {
    const a = server("a");
    let s = serversReducer(initialServersState, { type: "servers/put", server: a });
    expect(s.ids).toEqual(["a"]);
    expect(serversReducer(s, { type: "servers/put", server: a })).toBe(s);
    s = serversReducer(s, { type: "servers/put", server: server("a", { state: "failed" }) });
    expect(s.ids).toEqual(["a"]);
    expect(s.byId.a.state).toBe("failed");
    expect(serversReducer(s, { type: "servers/select", id: "nope" })).toBe(s);
    expect(serversReducer(s, { type: "servers/select", id: null })).toBe(s);
  });

  it("keeps mock route drafts until Go has them", () => {
    const saved: MockConfig = { routes: [], strictTrailingSlash: false, caseInsensitive: false };
    const draft: MockConfig = { ...saved, caseInsensitive: true };
    const newer: MockConfig = { ...saved, strictTrailingSlash: true };
    let s = serversReducer(initialServersState, { type: "servers/set", servers: [server("m", { type: "mock_http", mock: saved })] });
    expect(selectMockConfig(s, "m")).toBe(saved);
    expect(serversReducer(s, { type: "servers/mockDraft", id: "nope", config: draft })).toBe(s);

    s = serversReducer(s, { type: "servers/mockDraft", id: "m", config: draft });
    expect(selectMockConfig(s, "m")).toBe(draft);
    s = serversReducer(s, { type: "servers/mockFailed", id: "m", error: "Too many routes." });
    expect(s.mockErrors.m).toBe("Too many routes.");
    // A newer edit stays when an older one is confirmed.
    s = serversReducer(s, { type: "servers/mockDraft", id: "m", config: newer });
    s = serversReducer(s, { type: "servers/mockSaved", id: "m", config: draft });
    expect(selectMockConfig(s, "m")).toBe(newer);
    expect(s.mockErrors.m).toBeUndefined();
    s = serversReducer(s, { type: "servers/mockSaved", id: "m", config: newer });
    expect(s.mockDrafts).toEqual({});
    expect(serversReducer(s, { type: "servers/mockSaved", id: "m", config: newer })).toBe(s);

    // Removing the server drops its draft.
    s = serversReducer(s, { type: "servers/mockDraft", id: "m", config: draft });
    s = serversReducer(s, { type: "servers/set", servers: [] });
    expect(s.mockDrafts).toEqual({});
    expect(selectMockConfig(s, "m")).toBeNull();
  });

  it("keeps the clients, the selected client and the composer of WebSocket servers", () => {
    const client = (id: string): WsClient => ({
      id,
      peer: "127.0.0.1:1",
      target: "/",
      subprotocol: "",
      connectedAt: null,
      origin: "",
      userAgent: "",
      messagesIn: 0,
      messagesOut: 0,
      bytesIn: 0,
      bytesOut: 0,
    });
    let s = serversReducer(initialServersState, { type: "servers/set", servers: [server("w", { type: "websocket", state: "running" })] });
    expect(selectWsClients(s, "w")).toBe(selectWsClients(s, "other"));
    expect(selectWsTargetClient(s, "w")).toBeNull();
    expect(serversReducer(s, { type: "servers/wsClients", id: "nope", clients: [client("1")] })).toBe(s);

    s = serversReducer(s, { type: "servers/wsClients", id: "w", clients: [client("1"), client("2")] });
    expect(selectWsTargetClient(s, "w")?.id).toBe("1");
    s = serversReducer(s, { type: "servers/wsSelectClient", id: "w", clientId: "2" });
    expect(selectWsTargetClient(s, "w")?.id).toBe("2");
    expect(serversReducer(s, { type: "servers/wsSelectClient", id: "w", clientId: "2" })).toBe(s);
    expect(selectWsSelectionGone(s, "w")).toBe(false);
    // The selected client left: no target, never another client, until one is selected.
    s = serversReducer(s, { type: "servers/wsClients", id: "w", clients: [client("1")] });
    expect(selectWsTargetClient(s, "w")).toBeNull();
    expect(selectWsSelectionGone(s, "w")).toBe(true);
    s = serversReducer(s, { type: "servers/wsSelectClient", id: "w", clientId: "1" });
    expect(selectWsTargetClient(s, "w")?.id).toBe("1");
    expect(selectWsSelectionGone(s, "w")).toBe(false);
    s = serversReducer(s, { type: "servers/wsSelectClient", id: "w", clientId: "2" });

    expect(selectWsComposer(s, "w")).toEqual({ format: "text", message: "" });
    s = serversReducer(s, { type: "servers/wsComposer", id: "w", composer: { format: "json", message: "{}" } });
    expect(selectWsComposer(s, "w")).toEqual({ format: "json", message: "{}" });

    // A stopped server has no clients; a removed one loses everything.
    s = serversReducer(s, { type: "servers/set", servers: [server("w", { type: "websocket", state: "stopped" })] });
    expect(selectWsClients(s, "w")).toEqual([]);
    expect(s.wsSelectedClient.w).toBe("2");
    s = serversReducer(s, { type: "servers/set", servers: [] });
    expect(s.wsSelectedClient).toEqual({});
    expect(s.wsComposer).toEqual({});
  });

  it("keeps the connections, the selected connection, the composer and the view of TCP servers", () => {
    const conn = (id: string): TcpConnection => ({
      id,
      peer: "127.0.0.1:1",
      local: "127.0.0.1:7000",
      connectedAt: null,
      bytesIn: 0,
      bytesOut: 0,
      messagesIn: 0,
      messagesOut: 0,
    });
    let s = serversReducer(initialServersState, { type: "servers/set", servers: [server("t", { type: "tcp", state: "running" })] });
    expect(selectTcpConnections(s, "t")).toBe(selectTcpConnections(s, "other"));
    expect(selectTcpTargetConnection(s, "t")).toBeNull();
    expect(serversReducer(s, { type: "servers/tcpConnections", id: "nope", connections: [conn("1")] })).toBe(s);

    s = serversReducer(s, { type: "servers/tcpConnections", id: "t", connections: [conn("1"), conn("2")] });
    expect(selectTcpTargetConnection(s, "t")?.id).toBe("1");
    s = serversReducer(s, { type: "servers/tcpSelectConnection", id: "t", connectionId: "2" });
    expect(selectTcpTargetConnection(s, "t")?.id).toBe("2");
    expect(serversReducer(s, { type: "servers/tcpSelectConnection", id: "t", connectionId: "2" })).toBe(s);
    // The selected connection closed: no target, never another connection.
    s = serversReducer(s, { type: "servers/tcpConnections", id: "t", connections: [conn("1")] });
    expect(selectTcpTargetConnection(s, "t")).toBeNull();
    expect(selectTcpSelectionGone(s, "t")).toBe(true);
    // Cleared, the selection falls back to the first connection again.
    const cleared = serversReducer(s, { type: "servers/tcpSelectConnection", id: "t", connectionId: null });
    expect(selectTcpTargetConnection(cleared, "t")?.id).toBe("1");
    expect(selectTcpSelectionGone(cleared, "t")).toBe(false);

    expect(selectTcpComposer(s, "t")).toEqual({ format: "text", message: "", lineEnding: "none" });
    s = serversReducer(s, { type: "servers/tcpComposer", id: "t", composer: { format: "hex", message: "DE AD", lineEnding: "lf" } });
    expect(selectTcpComposer(s, "t").format).toBe("hex");

    expect(selectTcpView(s, "t")).toBe("text");
    expect(serversReducer(s, { type: "servers/tcpView", id: "t", view: "text" })).toBe(s);
    s = serversReducer(s, { type: "servers/tcpView", id: "t", view: "hex" });
    expect(selectTcpView(s, "t")).toBe("hex");

    // A stopped server has no connections; a removed one loses everything.
    s = serversReducer(s, { type: "servers/set", servers: [server("t", { type: "tcp", state: "stopped" })] });
    expect(selectTcpConnections(s, "t")).toEqual([]);
    expect(s.tcpSelectedConnection.t).toBe("2");
    s = serversReducer(s, { type: "servers/set", servers: [] });
    expect(s.tcpSelectedConnection).toEqual({});
    expect(s.tcpComposer).toEqual({});
    expect(s.tcpView).toEqual({});
  });

  it("keeps the requests, the selected request and the replay URL of webhook receivers", () => {
    const capture = (seq: number): WebhookCapture => ({
      seq,
      time: null,
      peer: "127.0.0.1:1",
      method: "POST",
      target: "/hook",
      contentType: "",
      size: 0,
      truncated: false,
    });
    let s = serversReducer(initialServersState, { type: "servers/set", servers: [server("a", { state: "running" })] });
    expect(selectWebhookCaptures(s, "a")).toBe(selectWebhookCaptures(s, "b"));
    expect(selectWebhookCapture(s, "a")).toBeNull();
    expect(serversReducer(s, { type: "servers/webhookCaptures", id: "nope", captures: [capture(1)] })).toBe(s);
    s = serversReducer(s, { type: "servers/webhookCaptures", id: "a", captures: [capture(1), capture(4)] });
    expect(selectWebhookCapture(s, "a")?.seq).toBe(4); // the newest by default
    s = serversReducer(s, { type: "servers/webhookSelect", id: "a", seq: 1 });
    expect(selectWebhookCapture(s, "a")?.seq).toBe(1);
    expect(serversReducer(s, { type: "servers/webhookSelect", id: "a", seq: 1 })).toBe(s);
    // A selected request that is no longer kept falls back to the newest.
    s = serversReducer(s, { type: "servers/webhookCaptures", id: "a", captures: [capture(4)] });
    expect(selectWebhookCapture(s, "a")?.seq).toBe(4);
    s = serversReducer(s, { type: "servers/webhookSelect", id: "a", seq: null });
    expect(s.webhookSelected).toEqual({});

    expect(selectWebhookReplayUrl(s, "a")).toBe("");
    s = serversReducer(s, { type: "servers/webhookReplayUrl", id: "a", url: "u" });
    expect(selectWebhookReplayUrl(s, "a")).toBe("u");

    // Stopping keeps them; removing the server drops them.
    s = serversReducer(s, { type: "servers/set", servers: [server("a")] });
    expect(selectWebhookCaptures(s, "a")).toHaveLength(1);
    s = serversReducer(s, { type: "servers/set", servers: [] });
    expect(s.webhookCaptures).toEqual({});
    expect(s.webhookReplayUrl).toEqual({});
  });

  it("never sends to an SSE subscriber or a UDP peer other than the one selected", () => {
    let s = serversReducer(initialServersState, {
      type: "servers/set",
      servers: [server("e", { type: "sse", state: "running" }), server("u", { type: "udp", state: "running" })],
    });
    const sub = (id: string) => ({ id }) as SseSubscriber;
    s = serversReducer(s, { type: "servers/sseSubscribers", id: "e", subscribers: [sub("1"), sub("2")] });
    expect(selectSseTargetSubscriber(s, "e")?.id).toBe("1");
    s = serversReducer(s, { type: "servers/sseSelect", id: "e", subscriberId: "2" });
    s = serversReducer(s, { type: "servers/sseSubscribers", id: "e", subscribers: [sub("1")] });
    expect(selectSseTargetSubscriber(s, "e")).toBeNull();
    expect(selectSseSelectionGone(s, "e")).toBe(true);

    const peer = (addr: string) => ({ addr }) as UdpPeer;
    s = serversReducer(s, { type: "servers/udpPeers", id: "u", peers: [peer("a:1"), peer("b:2")] });
    expect(selectUdpTargetPeer(s, "u")?.addr).toBe("a:1");
    s = serversReducer(s, { type: "servers/udpSelect", id: "u", peer: "b:2" });
    s = serversReducer(s, { type: "servers/udpPeers", id: "u", peers: [peer("a:1")] });
    expect(selectUdpTargetPeer(s, "u")).toBeNull();
    expect(selectUdpSelectionGone(s, "u")).toBe(true);
    // Listed again once it sends another datagram.
    s = serversReducer(s, { type: "servers/udpPeers", id: "u", peers: [peer("b:2"), peer("a:1")] });
    expect(selectUdpTargetPeer(s, "u")?.addr).toBe("b:2");
    expect(selectUdpSelectionGone(s, "u")).toBe(false);
  });

  it("never emits to a Socket.IO socket other than the one selected", () => {
    const socket = (id: string) => ({ id, clientId: id }) as SioSocket;
    let s = serversReducer(initialServersState, { type: "servers/set", servers: [server("i", { type: "socketio", state: "running" })] });
    expect(selectSioTargetSocket(s, "i")).toBeNull();
    s = serversReducer(s, { type: "servers/sioSockets", id: "i", sockets: [socket("1"), socket("2")] });
    // Nothing selected: the first socket, shown selected.
    expect(selectSioTargetSocket(s, "i")?.id).toBe("1");
    s = serversReducer(s, { type: "servers/sioSelect", id: "i", socketId: "2" });
    expect(selectSioTargetSocket(s, "i")?.id).toBe("2");
    expect(selectSioSelectionGone(s, "i")).toBe(false);
    // The selected socket disconnected: no target until one is selected.
    s = serversReducer(s, { type: "servers/sioSockets", id: "i", sockets: [socket("1")] });
    expect(selectSioTargetSocket(s, "i")).toBeNull();
    expect(selectSioSelectionGone(s, "i")).toBe(true);
    s = serversReducer(s, { type: "servers/sioSelect", id: "i", socketId: "1" });
    expect(selectSioTargetSocket(s, "i")?.id).toBe("1");
    s = serversReducer(s, { type: "servers/sioSelect", id: "i", socketId: null });
    expect(selectSioSelectionGone(s, "i")).toBe(false);
  });

  it("tells active states apart", () => {
    expect(["starting", "running", "stopping"].every((st) => isServerActive(st as LocalServer["state"]))).toBe(true);
    expect(isServerActive("stopped") || isServerActive("failed")).toBe(false);
  });
});
