import { beforeEach, describe, expect, it, vi } from "vitest";

const ws = vi.hoisted(() => ({
  Connect: vi.fn<(input: unknown) => Promise<unknown>>(),
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<(input: unknown) => Promise<void>>(),
  Ping: vi.fn<(input: unknown) => Promise<unknown>>(),
  CloseAll: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket", () => ({
  WebSocketService: ws,
}));

import type { RequestDTO } from "../lib/collection";
import { connectInput, type ConnectInputDTO, type StateEventDTO } from "../lib/websocket";
import { createStores } from "../stores";
import { deferred, emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import { confirmQueue } from "./dialogs";
import { closeTabs } from "./tabs";
import { closeCodeText, formatDelay, formatRtt, reconnectText, resetWsSessions, wsSessions } from "./wsSessions";

const input = (over: Partial<ConnectInputDTO> = {}): ConnectInputDTO => ({
  requestId: "req_feed",
  url: "ws://localhost:8080/feed",
  headers: [],
  subprotocols: [],
  connectTimeoutMs: null,
  verifyTls: null,
  sendCookies: null,
  ...over,
});

function stateEvent(over: Partial<StateEventDTO>): StateEventDTO {
  return {
    connId: "ws_1",
    requestId: "req_feed",
    url: "ws://localhost:8080/feed",
    seq: 1,
    attempt: 1,
    state: "connecting",
    at: "2026-09-16T10:00:00.000Z",
    handshake: null,
    close: null,
    error: null,
    reconnect: null,
    tests: null,
    ...over,
  };
}

const closeInfo = (over: Partial<NonNullable<StateEventDTO["close"]>> = {}): NonNullable<StateEventDTO["close"]> => ({
  code: 1000,
  reason: "",
  wasClean: true,
  initiatedBy: "local",
  sent: { code: 1000, reason: "" },
  received: { code: 1000, reason: "" },
  messagesSent: 0,
  messagesReceived: 0,
  bytesSent: 0,
  bytesReceived: 0,
  durationMs: 0,
  ...over,
});

beforeEach(() => {
  resetWsSessions();
  resetFakeWails();
  ws.Connect.mockReset();
  ws.Send.mockReset();
  ws.Close.mockReset().mockResolvedValue(undefined);
  ws.Ping.mockReset();
});

describe("connectInput", () => {
  it("takes the handshake options of a request, without blank rows and subprotocols", () => {
    const request = {
      id: "req_feed",
      url: " ws://localhost:8080/feed ",
      headers: [
        { name: "X-A", value: "1", description: "", enabled: true, extra: "" },
        { name: "", value: "", description: "", enabled: true, extra: "" },
      ],
      subprotocols: [" chat ", ""],
      settings: { connectTimeoutMs: 0, verifyTls: false, sendCookies: false },
    } as unknown as RequestDTO;
    expect(connectInput(request)).toEqual({
      requestId: "req_feed",
      url: "ws://localhost:8080/feed",
      headers: [
        { name: "X-A", value: "1", enabled: true },
        { name: "", value: "", enabled: false },
      ],
      subprotocols: ["chat"],
      connectTimeoutMs: 0,
      verifyTls: false,
      sendCookies: false,
    });
    const withKeepalive = {
      ...request,
      settings: { ...request.settings, pingIntervalMs: 15000, pongTimeoutMs: 0, autoReconnect: true, reconnectMaxAttempts: null },
    } as RequestDTO;
    expect(connectInput(withKeepalive)).toMatchObject({ pingIntervalMs: 15000, pongTimeoutMs: 0, autoReconnect: true });
    expect(connectInput(withKeepalive)).not.toHaveProperty("reconnectMaxAttempts");
  });
});

describe("WsSession", () => {
  it("follows the connection's events, also those that arrive before Connect resolves", async () => {
    const started = deferred<unknown>();
    ws.Connect.mockReturnValue(started.promise);
    const session = wsSessions().get("req_feed");
    const connecting = session.connect(input());
    expect(ws.Connect).toHaveBeenCalledWith(input());
    expect(session.getState().status).toBe("connecting");

    emit("ws:state", stateEvent({ state: "connecting" }));
    emit("ws:state", stateEvent({ connId: "ws_old", state: "closed", seq: 9 }));
    emit(
      "ws:state",
      stateEvent({
        state: "open",
        seq: 2,
        handshake: {
          url: "ws://localhost:8080/feed",
          requestHeaders: [],
          response: { statusCode: 101, status: "101 Switching Protocols", headers: [], body: "", bodyTruncated: false },
          subprotocol: "chat",
          localAddr: "",
          remoteAddr: "127.0.0.1:8080",
          tlsVersion: "",
          durationMs: 2,
        },
      }),
    );
    started.resolve({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    await connecting;
    expect(session.getState()).toMatchObject({ status: "open", connId: "ws_1", handshake: { subprotocol: "chat" } });

    emit("ws:message", {
      connId: "ws_1",
      requestId: "req_feed",
      seq: 3,
      messages: [
        { direction: "sent", opcode: "text", at: "2026-09-16T10:00:01.000Z", text: '{"a":1}', data: "", size: 7 },
        { direction: "received", opcode: "binary", at: "2026-09-16T10:00:01.001Z", text: "", data: btoa("\x01\x02"), size: 2 },
      ],
    });
    expect(session.getState()).toMatchObject({ messagesSent: 1, messagesReceived: 1 });
    session.log.flush();
    const log = [0, 1, 2, 3].map((seq) => session.log.get(seq));
    expect(log.map((m) => [m?.direction, m?.kind, m?.text])).toEqual([
      ["system", "connecting", "Connecting to ws://localhost:8080/feed"],
      ["system", "connected", "Connected to ws://localhost:8080/feed (subprotocol chat)"],
      ["sent", "text", '{"a":1}'],
      ["received", "binary", undefined],
    ]);
    expect([...(log[3]?.data ?? [])]).toEqual([1, 2]);
    expect(log[2]?.time).toBe(Date.parse("2026-09-16T10:00:01.000Z"));

    emit(
      "ws:state",
      stateEvent({ state: "closed", seq: 4, close: closeInfo({ code: 4001, reason: "bye", initiatedBy: "remote", messagesSent: 1, messagesReceived: 1 }) }),
    );
    expect(session.getState()).toMatchObject({ status: "closed", error: null, close: { code: 4001 } });
    session.log.flush();
    expect(session.log.get(4)?.text).toBe("Closed by the server: 4001 “bye” · 1 message sent, 1 message received");
  });

  it("shows a rejected upgrade and refused input", async () => {
    ws.Connect.mockResolvedValueOnce({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    const session = wsSessions().get("req_feed");
    await session.connect(input());
    emit(
      "ws:state",
      stateEvent({
        state: "closed",
        error: { kind: "protocol", code: "wsclient.upgrade_rejected", message: "The server refused the upgrade.", hint: "", details: {}, causes: [] },
        handshake: {
          url: "ws://localhost:8080/feed",
          requestHeaders: [],
          response: { statusCode: 403, status: "403 Forbidden", headers: [], body: "no", bodyTruncated: false },
          subprotocol: "",
          localAddr: "",
          remoteAddr: "",
          tlsVersion: "",
          durationMs: 1,
        },
        close: closeInfo({ code: 1006, initiatedBy: "none", wasClean: false, sent: null, received: null }),
      }),
    );
    expect(session.getState()).toMatchObject({ error: { code: "wsclient.upgrade_rejected" }, handshake: { response: { statusCode: 403 } } });
    session.log.flush();
    expect(session.log.get(0)).toMatchObject({ kind: "error", text: "The server refused the upgrade.", meta: { status: "403 Forbidden" } });

    const refusal = Object.assign(new Error("bad"), {
      cause: { kind: "invalid_argument", code: "wsclient.scheme_unsupported", message: "Not a WebSocket scheme.", hint: "Use ws://.", details: {}, causes: [] },
    });
    ws.Connect.mockRejectedValueOnce(refusal);
    await expect(session.connect(input({ url: "http://x" }))).rejects.toMatchObject({ code: "wsclient.scheme_unsupported" });
    expect(session.getState()).toMatchObject({ status: "idle", error: { message: "Not a WebSocket scheme." } });
  });

  it("closes with a code, also when asked while connecting, and when the tab closes", async () => {
    const started = deferred<unknown>();
    ws.Connect.mockReturnValue(started.promise);
    const session = wsSessions().get("req_feed");
    const connecting = session.connect(input());
    await session.disconnect(4000, "early");
    expect(ws.Close).not.toHaveBeenCalled();
    started.resolve({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    emit("ws:state", stateEvent({ state: "connecting" }));
    await connecting;
    expect(ws.Close).toHaveBeenCalledWith({ connId: "ws_1", code: 4000, reason: "early" });

    // A code Go refuses rejects.
    ws.Close.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "invalid_argument", code: "wsframe.invalid_argument", message: "The close code 1006 must not be sent.", hint: "", details: {}, causes: [] } }),
    );
    await expect(session.disconnect(1006)).rejects.toMatchObject({ code: "wsframe.invalid_argument" });

    ws.Close.mockClear();
    emit("ws:state", stateEvent({ state: "closed", close: closeInfo() }));
    ws.Connect.mockResolvedValue({ connId: "ws_2", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    await session.connect(input());
    emit("ws:state", stateEvent({ connId: "ws_2", state: "open" }));
    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_feed"], activeId: "req_feed" });
    closeTabs(stores, ["req_feed"]);
    await settle();
    expect(ws.Close).toHaveBeenCalledWith({ connId: "ws_2", code: 1001, reason: "" });
    expect(wsSessions().peek("req_feed")).toBeUndefined();

    resetWsSessions();
    expect(listenerCount()).toBe(0);
  });

  it("refuses to send without an open connection and passes Go's send errors on", async () => {
    const session = wsSessions().get("req_feed");
    await expect(session.send("x")).rejects.toMatchObject({ code: "ws.not_open" });
    ws.Connect.mockResolvedValue({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    await session.connect(input());
    emit("ws:state", stateEvent({ state: "open" }));
    ws.Send.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "network", code: "wsframe.write_failed", message: "write failed", hint: "", details: {}, causes: [] } }),
    );
    await expect(session.send("a")).rejects.toMatchObject({ code: "wsframe.write_failed" });
    ws.Send.mockResolvedValueOnce({ bytes: 1 });
    await expect(session.send("a")).resolves.toEqual({ bytes: 1 });
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "a" });
    // Binary formats are passed on; JSON is text to Go.
    ws.Send.mockResolvedValue({ bytes: 2 });
    await session.send("00 ff", "hex");
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "00 ff", format: "hex" });
    await session.send("AP8=", "base64");
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "AP8=", format: "base64" });
    await session.send("{}", "json");
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "{}" });
  });

  it("pings, logs pings and pongs with the round trip, and does not count them as messages (WS-09)", async () => {
    const session = wsSessions().get("req_feed");
    await expect(session.ping("x")).rejects.toMatchObject({ code: "ws.not_open" });
    ws.Connect.mockResolvedValue({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    await session.connect(input());
    emit("ws:state", stateEvent({ state: "open" }));
    ws.Ping.mockResolvedValueOnce({ bytes: 5 });
    await expect(session.ping("hello")).resolves.toEqual({ bytes: 5 });
    expect(ws.Ping).toHaveBeenCalledWith({ connId: "ws_1", payload: "hello" });
    ws.Ping.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "invalid_argument", code: "ws.ping_too_long", message: "A ping payload can be at most 125 bytes.", hint: "", details: {}, causes: [] } }),
    );
    await expect(session.ping("x".repeat(126))).rejects.toMatchObject({ code: "ws.ping_too_long" });

    emit("ws:message", {
      connId: "ws_1",
      requestId: "req_feed",
      seq: 3,
      messages: [
        { direction: "sent", opcode: "ping", at: "2026-09-16T10:00:01.000Z", text: "hello", data: btoa("hello"), size: 5 },
        { direction: "received", opcode: "pong", at: "2026-09-16T10:00:01.012Z", text: "hello", data: btoa("hello"), size: 5, rttMs: 12.345 },
        { direction: "sent", opcode: "ping", at: "2026-09-16T10:00:02.000Z", text: "", data: "", size: 0, keepalive: true },
        { direction: "received", opcode: "pong", at: "2026-09-16T10:00:02.001Z", text: "", data: "", size: 0, keepalive: true, rttMs: 1.5 },
        { direction: "received", opcode: "ping", at: "2026-09-16T10:00:03.000Z", text: "", data: btoa("\xff"), size: 1 },
      ],
    });
    expect(session.getState()).toMatchObject({ messagesSent: 0, messagesReceived: 0, lastRttMs: 1.5 });
    session.log.flush();
    const log = [1, 2, 3, 4, 5].map((seq) => session.log.get(seq));
    expect(log.map((m) => [m?.direction, m?.kind, m?.text, m?.badge])).toEqual([
      ["sent", "ping", "hello", undefined],
      ["received", "pong", "hello", "12.3 ms"],
      ["sent", "ping", "", "keepalive"],
      ["received", "pong", "", "keepalive · 1.50 ms"],
      ["received", "ping", undefined, undefined],
    ]);
    expect([...(log[4]?.data ?? [])]).toEqual([0xff]);
    expect(log[1]?.meta).toEqual({ rttMs: 12.345 });
  });

  it("follows auto-reconnect: waits, reconnects, and cancels (WS-10)", async () => {
    ws.Connect.mockResolvedValue({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    const session = wsSessions().get("req_feed");
    await session.connect(input({ autoReconnect: true }));
    emit("ws:state", stateEvent({ state: "connecting" }));
    emit("ws:state", stateEvent({ state: "open", seq: 2 }));
    const dropped = {
      kind: "network",
      code: "wsframe.abnormal_closure",
      message: "The connection ended without a close frame.",
      hint: "",
      details: {},
      causes: [],
    };
    emit(
      "ws:state",
      stateEvent({
        state: "closed",
        seq: 3,
        error: dropped,
        close: closeInfo({ code: 1006, initiatedBy: "none", wasClean: false, sent: null, received: null }),
        reconnect: { attempt: 1, maxAttempts: 5, delayMs: 1000, stopped: "" },
      }),
    );
    expect(session.getState()).toMatchObject({ status: "reconnecting", reconnect: { attempt: 1 }, error: { code: "wsframe.abnormal_closure" } });
    expect(session.active).toBe(true);
    emit("ws:state", stateEvent({ state: "connecting", seq: 4, attempt: 2 }));
    expect(session.getState()).toMatchObject({ status: "connecting", attempt: 2, reconnect: null, error: null });
    emit("ws:state", stateEvent({ state: "open", seq: 5, attempt: 2 }));
    emit(
      "ws:state",
      stateEvent({
        state: "closed",
        seq: 6,
        attempt: 2,
        close: closeInfo({ code: 1012, initiatedBy: "remote" }),
        reconnect: { attempt: 1, maxAttempts: 5, delayMs: 1000, stopped: "" },
      }),
    );
    await session.disconnect();
    expect(ws.Close).toHaveBeenCalledWith({ connId: "ws_1", code: 1000, reason: "" });
    emit("ws:state", stateEvent({ state: "closed", seq: 7, attempt: 2, reconnect: { attempt: 1, maxAttempts: 5, delayMs: 0, stopped: "manual" } }));
    expect(session.getState()).toMatchObject({ status: "closed", close: { code: 1012 }, reconnect: { stopped: "manual" } });
    expect(session.active).toBe(false);
    session.log.flush();
    const texts = Array.from({ length: session.log.getSnapshot().count }, (_, i) => session.log.get(i)).map((m) => `${m?.kind}: ${m?.text}`);
    expect(texts).toEqual([
      "connecting: Connecting to ws://localhost:8080/feed",
      "connected: Connected to ws://localhost:8080/feed",
      "error: The connection ended without a close frame.",
      "reconnect: Reconnecting in 1 s (attempt 1 of 5)",
      "connecting: Reconnecting to ws://localhost:8080/feed (attempt 2)",
      "connected: Connected to ws://localhost:8080/feed",
      "close: Closed by the server: 1012 Service restart · 0 messages sent, 0 messages received",
      "reconnect: Reconnecting in 1 s (attempt 1 of 5)",
      "reconnect: Reconnect cancelled",
    ]);

    // Giving up.
    ws.Connect.mockResolvedValue({ connId: "ws_2", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    await session.connect(input({ autoReconnect: true }));
    emit(
      "ws:state",
      stateEvent({
        connId: "ws_2",
        state: "closed",
        attempt: 4,
        error: dropped,
        close: closeInfo({ code: 1006, initiatedBy: "none" }),
        reconnect: { attempt: 3, maxAttempts: 3, delayMs: 0, stopped: "max_attempts" },
      }),
    );
    expect(session.getState()).toMatchObject({ status: "closed", error: { code: "wsframe.abnormal_closure" } });
    session.log.flush();
    const last = session.log.get(session.log.getSnapshot().nextSeq - 1);
    expect(last).toMatchObject({ kind: "reconnect", text: "Gave up reconnecting after 3 attempts", meta: { stopped: "max_attempts" } });
  });

  it("formats reconnect notes and round trips", () => {
    expect(formatDelay(250)).toBe("250 ms");
    expect(formatDelay(1500)).toBe("1.5 s");
    expect(formatDelay(30000)).toBe("30 s");
    expect(formatRtt(0.25)).toBe("0.25 ms");
    expect(formatRtt(42.25)).toBe("42.3 ms");
    expect(formatRtt(250.4)).toBe("250 ms");
    expect(reconnectText({ attempt: 2, maxAttempts: 0, delayMs: 2000, stopped: "" })).toBe("Reconnecting in 2 s (attempt 2)");
    expect(reconnectText({ attempt: 0, maxAttempts: 5, delayMs: 0, stopped: "close_code" })).toMatch(/^Not reconnecting/);
    expect(reconnectText({ attempt: 1, maxAttempts: 5, delayMs: 0, stopped: "error" })).toBe("Not reconnecting after this error");
  });

  it("names close codes", () => {
    expect(closeCodeText(1000, "")).toBe("1000 Normal closure");
    expect(closeCodeText(4000, "done")).toBe("4000 “done”");
  });
});

describe("undefined variables (WS-11)", () => {
  const unresolved = (names: string) =>
    Object.assign(new Error("refused"), {
      cause: {
        kind: "invalid_argument",
        code: "template.unresolved",
        message: "Variables are not defined.",
        hint: "",
        details: { names },
        causes: [],
      },
    });
  let detach: () => void = () => {};
  beforeEach(() => {
    detach = confirmQueue.attach();
    return () => detach();
  });

  async function answer(confirmed: boolean) {
    await settle();
    await settle();
    const asked = confirmQueue.current();
    expect(asked).not.toBeNull();
    confirmQueue.settle(asked!.id, confirmed);
    return asked!;
  }

  it("asks before connecting, and connects anyway with the references as written", async () => {
    ws.Connect.mockRejectedValueOnce(unresolved("host")).mockResolvedValueOnce({ connId: "ws_1", requestId: "req_feed", url: "ws://{{host}}/" });
    const session = wsSessions().get("req_feed");
    const connecting = session.connect(input({ url: "ws://{{host}}/" }));
    const asked = await answer(true);
    expect(asked).toMatchObject({ title: "Connect with an undefined variable?", confirmLabel: "Connect anyway" });
    await connecting;
    expect(ws.Connect).toHaveBeenLastCalledWith({ ...input({ url: "ws://{{host}}/" }), allowUnresolved: true });
    expect(session.getState()).toMatchObject({ status: "connecting", connId: "ws_1", error: null });
  });

  it("goes back to how it was when connecting is declined", async () => {
    ws.Connect.mockRejectedValueOnce(unresolved("host,token"));
    const session = wsSessions().get("req_feed");
    const connecting = session.connect(input({ url: "ws://{{host}}/" }));
    expect(session.getState().status).toBe("connecting");
    const asked = await answer(false);
    expect(asked.title).toBe("Connect with 2 undefined variables?");
    await connecting;
    expect(session.getState()).toMatchObject({ status: "idle", error: null, connId: null });
    expect(session.active).toBe(false);
    expect(ws.Connect).toHaveBeenCalledTimes(1);
  });

  it("asks before sending: Send anyway sends as written, Cancel sends nothing", async () => {
    ws.Connect.mockResolvedValue({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed" });
    const session = wsSessions().get("req_feed");
    await session.connect(input());
    emit("ws:state", stateEvent({ state: "open" }));

    ws.Send.mockRejectedValueOnce(unresolved("id")).mockResolvedValueOnce({ bytes: 6 });
    const sending = session.send("{{id}}");
    expect((await answer(true)).confirmLabel).toBe("Send anyway");
    await expect(sending).resolves.toEqual({ bytes: 6 });
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "{{id}}", allowUnresolved: true });

    ws.Send.mockRejectedValueOnce(unresolved("id"));
    const declined = session.send("{{id}}");
    await answer(false);
    await expect(declined).resolves.toBeNull();
    expect(ws.Send).toHaveBeenCalledTimes(3);
  });
});
