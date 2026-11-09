import { beforeEach, describe, expect, it, vi } from "vitest";

const sio = vi.hoisted(() => ({
  Connect: vi.fn<(input: unknown) => Promise<unknown>>(),
  Emit: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/socketio", () => ({
  SocketIOService: sio,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import type { ConnectInputDTO, ItemDTO } from "../lib/socketio";
import { createStores } from "../stores";
import { emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { listenNames, reasonText, resetSioSessions, sioLogMessage, sioSessions } from "./sioSessions";
import { closeTabs } from "./tabs";

const AT = "2026-09-19T10:00:00.000Z";

function item(over: Partial<ItemDTO>): ItemDTO {
  return {
    type: "event",
    at: AT,
    direction: "",
    transport: "",
    url: "",
    sid: "",
    pingIntervalMs: 0,
    pingTimeoutMs: 0,
    maxPayload: 0,
    upgrades: null,
    namespace: "/",
    socketId: "",
    event: "",
    args: "",
    ackId: null,
    elapsedMs: 0,
    packetType: "",
    data: "",
    attachments: 0,
    size: 0,
    reason: "",
    error: null,
    tests: null,
    ...over,
  };
}

const input: ConnectInputDTO = {
  requestId: "req_sio",
  url: "http://localhost:3000",
  headers: [],
  path: "",
  namespace: "",
  authPayload: "",
  transport: "auto",
  connectTimeoutMs: null,
  ackTimeoutMs: null,
  verifyTls: null,
  sendCookies: null,
};

beforeEach(() => {
  resetFakeWails();
  resetSioSessions();
  sio.Connect.mockReset();
  sio.Emit.mockReset();
});

describe("sioLogMessage", () => {
  it("shows the handshake, events with their names, acks with their round trip and the end", () => {
    const t = Date.parse(AT);
    expect(
      sioLogMessage(item({ type: "open", transport: "websocket", url: "http://h/socket.io/", sid: "e1", pingIntervalMs: 25000, pingTimeoutMs: 20000, maxPayload: 1000000 })),
    ).toEqual({
      time: t,
      direction: "system",
      kind: "open",
      badge: "websocket",
      text: "Engine.IO handshake with http://h/socket.io/ over WebSocket",
      meta: { transport: "websocket", sid: "e1", pingInterval: "25000 ms", pingTimeout: "20000 ms", maxPayload: 1000000 },
    });
    expect(sioLogMessage(item({ direction: "received", event: "news", args: '[{"a":1},{"$base64":"AQI="}]', attachments: 1, size: 40, ackId: 3 }))).toEqual({
      time: t,
      direction: "received",
      kind: "event",
      badge: "news",
      text: '[{"a":1},{"$base64":"AQI="}]',
      size: 40,
      meta: { event: "news", namespace: "/", ackId: 3, attachments: 1 },
    });
    expect(sioLogMessage(item({ type: "ack", direction: "received", event: "chat", ackId: 0, elapsedMs: 4, args: "[]" }))).toMatchObject({
      kind: "ack",
      badge: "chat #0",
      meta: { ackId: 0, event: "chat", roundTrip: "4 ms" },
    });
    expect(sioLogMessage(item({ type: "packet", direction: "received", packetType: "CONNECT_ERROR", data: '{"message":"nope"}' }))).toMatchObject({
      kind: "connect_error",
      text: '{"message":"nope"}',
    });
    expect(sioLogMessage(item({ type: "ack_timeout", event: "slow", ackId: 2, elapsedMs: 10000 }))).toMatchObject({
      kind: "error",
      text: "No acknowledgement for “slow” (#2) within 10000 ms",
    });
    expect(sioLogMessage(item({ type: "ping", direction: "received" }))).toEqual({ time: t, direction: "received", kind: "ping" });
    expect(sioLogMessage(item({ type: "closed", reason: "io server disconnect" }))).toMatchObject({ kind: "closed", text: "The server disconnected the socket" });
    const error = { kind: "timeout", code: "sioclient.ping_timeout", message: "No ping.", hint: "", details: {}, causes: [] };
    expect(sioLogMessage(item({ type: "closed", reason: "ping timeout", error }))).toMatchObject({ kind: "error", text: "No ping.", meta: { errorCode: "sioclient.ping_timeout" } });
    expect(reasonText("")).toBe("Closed");
    expect(listenNames([" a ", "", "b", "a"])).toEqual(["a", "b"]);
  });
});

describe("SioSession", () => {
  it("connects, follows the connection, filters events, emits and disconnects when its tab closes", async () => {
    sio.Connect.mockResolvedValue({ opId: "op_1" });
    const session = sioSessions().get("req_sio");
    session.setListen(["chat"]);
    session.connect(input);
    session.connect(input);
    expect(session.getState().status).toBe("connecting");
    await settle();
    expect(sio.Connect).toHaveBeenCalledTimes(1);
    expect(sio.Connect).toHaveBeenCalledWith(input);
    emit("sio:op:data", {
      opId: "op_1",
      seq: 1,
      items: [
        item({ type: "open", transport: "polling", url: "http://localhost:3000/socket.io/" }),
        item({ type: "upgrade", transport: "websocket" }),
        item({ type: "connected", transport: "websocket", namespace: "/", socketId: "s1" }),
        item({ direction: "received", event: "chat", args: '["hi"]' }),
        item({ direction: "received", event: "typing", args: "[]" }),
        item({ direction: "sent", event: "typing", args: "[]" }),
      ],
    });
    expect(session.getState()).toMatchObject({ status: "open", transport: "websocket", socketId: "s1", emitted: 1, received: 2, ignored: 1 });
    session.log.flush();
    // The received "typing" event is left out; the sent one is shown.
    expect(session.log.getSnapshot().count).toBe(5);

    sio.Emit.mockResolvedValue({ ackId: null, size: 10, attachments: 0 });
    await expect(session.emit("chat", '["x"]', false)).resolves.toEqual({ ackId: null, size: 10, attachments: 0 });
    expect(sio.Emit).toHaveBeenCalledWith({ opId: "op_1", event: "chat", args: '["x"]', ack: false });

    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_sio"], activeId: "req_sio" });
    closeTabs(stores, ["req_sio"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    expect(sioSessions().peek("req_sio")).toBeUndefined();
    resetSioSessions();
    expect(listenerCount()).toBe(0);
  });

  it("shows why a connection failed, and refuses to emit while not open", async () => {
    sio.Connect.mockResolvedValue({ opId: "op_2" });
    const session = sioSessions().get("req_fail");
    await expect(session.emit("x", "", false)).rejects.toMatchObject({ code: "sioclient.not_open" });
    session.connect(input);
    await settle();
    const error = { kind: "permission_denied", code: "sioclient.connect_error", message: "Refused: nope.", hint: "", details: {}, causes: [] };
    emit("sio:op:data", { opId: "op_2", seq: 1, items: [item({ type: "closed", reason: "connect failed", error })] });
    emit("sio:op:done", { opId: "op_2", seq: 2, status: "error", error, items: 1, batches: 1, elapsedMs: 3 });
    expect(session.getState()).toMatchObject({ status: "closed", reason: "connect failed", error: { code: "sioclient.connect_error" } });

    sio.Connect.mockRejectedValue({ kind: "invalid_argument", code: "sioclient.invalid_url", message: "Bad URL.", hint: "", details: {}, causes: [] });
    session.connect(input);
    await settle();
    expect(session.getState()).toMatchObject({ status: "closed", error: { code: "sioclient.invalid_url" } });
    session.log.flush();
    expect(session.log.getSnapshot().count).toBe(2);
  });
});
