import { beforeEach, describe, expect, it, vi } from "vitest";

const grpc = vi.hoisted(() => ({
  Invoke: vi.fn<(input: unknown) => Promise<unknown>>(),
  SendMessage: vi.fn<(input: { opId: string; message: string }) => Promise<unknown>>(),
  CloseSend: vi.fn<(input: { opId: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: grpc,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import type { CallEventDTO, InvokeInputDTO } from "../lib/grpc";
import { createStores } from "../stores";
import { emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { callLogMessage, grpcCalls, resetGrpcCalls } from "./grpcCalls";
import { closeTabs } from "./tabs";

const AT = "2026-09-18T10:00:00.000Z";

function ev(over: Partial<CallEventDTO>): CallEventDTO {
  return {
    type: "request",
    at: AT,
    method: "",
    metadata: [],
    json: "",
    size: 0,
    index: 0,
    decodeError: "",
    raw: "",
    deadlineMs: 0,
    transport: "grpc",
    compression: "identity",
    clientStreaming: false,
    status: null,
    messages: 0,
    sent: 0,
    elapsedMs: 0,
    tests: null,
    ...over,
  };
}

const input: InvokeInputDTO = {
  requestId: "req_order",
  files: ["a.proto"],
  importPaths: [],
  address: "localhost:50051",
  transport: "grpc",
  compression: "identity",
  tls: false,
  verifyTls: null,
  connectTimeoutMs: null,
  deadlineMs: null,
  service: "shop.v1.Orders",
  method: "Watch",
  message: "{}",
  metadata: [],
};

beforeEach(() => {
  resetFakeWails();
  resetGrpcCalls();
  grpc.Invoke.mockReset();
  grpc.SendMessage.mockReset();
  grpc.CloseSend.mockReset();
});

describe("callLogMessage", () => {
  it("maps call events to log messages", () => {
    const t = Date.parse(AT);
    expect(
      callLogMessage(
        ev({
          type: "request",
          method: "/shop.v1.Orders/Watch",
          json: "{}",
          size: 0,
          deadlineMs: 500,
          metadata: [
            { key: "x-a", value: "1", binary: false },
            { key: "x-b-bin", value: "AAE=", binary: true },
          ],
        }),
      ),
    ).toEqual({
      time: t,
      direction: "sent",
      kind: "message",
      text: "{}",
      size: 0,
      meta: { method: "/shop.v1.Orders/Watch", deadline: "500 ms", "metadata x-a": "1", "metadata x-b-bin": "AAE= (Base64)" },
    });
    expect(callLogMessage(ev({ type: "headers", metadata: [{ key: "content-type", value: "application/grpc", binary: false }] }))).toEqual({
      time: t,
      direction: "system",
      kind: "headers",
      text: "content-type: application/grpc",
    });
    expect(callLogMessage(ev({ type: "message", index: 2, json: '{"a":1}', size: 2 }))).toEqual({
      time: t,
      direction: "received",
      kind: "message",
      badge: "#2",
      text: '{"a":1}',
      size: 2,
    });
    const bad = callLogMessage(ev({ type: "message", index: 0, decodeError: "not valid", raw: "CgVh", size: 3 }));
    expect(bad).toMatchObject({ direction: "received", badge: "#0", size: 3, meta: { error: "not valid" } });
    expect([...(bad.data ?? [])]).toEqual([0x0a, 0x05, 0x61]);
    expect(
      callLogMessage(
        ev({ type: "end", status: { code: 5, name: "NOT_FOUND", message: "gone", details: "", rich: null }, messages: 1, elapsedMs: 7, metadata: [{ key: "t", value: "v", binary: false }] }),
      ),
    ).toEqual({
      time: t,
      direction: "system",
      kind: "status",
      badge: "NOT_FOUND",
      text: "NOT_FOUND: gone",
      meta: { messages: 1, sent: 0, elapsed: "7 ms", code: 5, "trailer t": "v" },
    });
    // A stream from the client: the request opens it, and each sent message and the half-close are items of their own.
    expect(callLogMessage(ev({ type: "request", method: "/shop.v1.Orders/Chat", clientStreaming: true }))).toEqual({
      time: t,
      direction: "system",
      kind: "open",
      text: "Stream opened: /shop.v1.Orders/Chat",
      size: 0,
      meta: { method: "/shop.v1.Orders/Chat" },
    });
    expect(callLogMessage(ev({ type: "sent", index: 1, json: '{"q":1}', size: 2 }))).toEqual({
      time: t,
      direction: "sent",
      kind: "message",
      badge: "#1",
      text: '{"q":1}',
      size: 2,
    });
    expect(callLogMessage(ev({ type: "half-close", index: 1 }))).toMatchObject({
      direction: "system",
      kind: "half-close",
      text: "Request stream ended after 1 message",
      meta: { sent: 1 },
    });
    const detail = { typeUrl: "type.googleapis.com/google.rpc.ErrorInfo", type: "google.rpc.ErrorInfo", known: true, json: "{}", error: "", raw: "" };
    const rich = { code: 3, name: "INVALID_ARGUMENT", message: "bad", error: "" };
    expect(
      callLogMessage(
        ev({
          type: "end",
          status: { code: 3, name: "INVALID_ARGUMENT", message: "bad", details: "AA==", rich: { ...rich, details: [detail, { ...detail, type: "", typeUrl: "x/" }] } },
        }),
      ).meta,
    ).toMatchObject({ "error details": "google.rpc.ErrorInfo, x/" });
    expect(
      callLogMessage(
        ev({ type: "end", status: { code: 3, name: "INVALID_ARGUMENT", message: "bad", details: "AA==", rich: { ...rich, details: [], error: "not a Status" } } }),
      ).meta,
    ).toMatchObject({ "error details": "not a Status" });
  });
});

describe("GrpcCall", () => {
  it("runs a call, ignores a second invoke while running, and is cancelled when its tab closes", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_1" });
    const call = grpcCalls().get("req_order");
    call.invoke(input);
    call.invoke(input);
    expect(grpc.Invoke).toHaveBeenCalledTimes(1);
    expect(call.getState()).toMatchObject({ status: "starting", method: "shop.v1.Orders/Watch" });
    await settle();
    expect(call.getState().status).toBe("running");
    emit("grpc:op:data", {
      opId: "op_1",
      seq: 1,
      items: [ev({ type: "request" }), ev({ type: "headers", metadata: [{ key: "a", value: "b", binary: false }] }), ev({ type: "message" })],
    });
    expect(call.getState()).toMatchObject({ messages: 1, headers: [{ key: "a", value: "b", binary: false }], trailers: null });
    call.log.flush();
    expect(call.log.getSnapshot().count).toBe(3);

    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_order"], activeId: "req_order" });
    closeTabs(stores, ["req_order"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    expect(grpcCalls().peek("req_order")).toBeUndefined();

    resetGrpcCalls();
    expect(listenerCount()).toBe(0);
  });

  it("keeps the call type, the start, the first message and the bytes received", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_2" });
    const call = grpcCalls().get("req_bytes");
    call.invoke(input, { kind: "server_stream", now: 1000 });
    expect(call.getState()).toMatchObject({ kind: "server_stream", startedAt: 1000, receivedBytes: 0, firstMessage: null });
    await settle();
    emit("grpc:op:data", {
      opId: "op_2",
      seq: 1,
      items: [ev({ type: "message", json: '{"a":1}', size: 7 }), ev({ type: "message", json: "", decodeError: "bad", raw: "AA==", size: 1 })],
    });
    expect(call.getState()).toMatchObject({ messages: 2, receivedBytes: 8, firstMessage: { json: '{"a":1}', decodeError: "", raw: "", size: 7 } });
    emit("grpc:op:data", { opId: "op_2", seq: 2, items: [ev({ type: "message", json: "{}", size: 2 })] });
    expect(call.getState()).toMatchObject({ messages: 3, receivedBytes: 10, firstMessage: { json: '{"a":1}' } });
    resetGrpcCalls();
  });

  it("sends messages on a stream from the client and ends it", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_3" });
    const call = grpcCalls().get("req_chat");
    // Nothing to send before the call runs.
    expect(await call.send("{}")).toBe(false);
    call.invoke({ ...input, method: "Chat" }, { kind: "bidi" });
    expect(call.getState().clientStreaming).toBe(true);
    expect(call.canSend).toBe(false);
    await settle();
    expect(call.canSend).toBe(true);

    grpc.SendMessage.mockResolvedValueOnce({ index: 0, size: 2 });
    expect(await call.send('{"q":1}')).toBe(true);
    expect(grpc.SendMessage).toHaveBeenCalledWith({ opId: "op_3", message: '{"q":1}' });
    expect(call.getState()).toMatchObject({ sending: 0, sendError: null });

    // A refusal is kept for the controls until the next send.
    grpc.SendMessage.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "invalid_argument", code: "grpc.invalid_message", message: "Not a valid Req.", hint: "", details: {}, causes: [] } }),
    );
    expect(await call.send("{")).toBe(false);
    expect(call.getState().sendError?.message).toBe("Not a valid Req.");

    emit("grpc:op:data", {
      opId: "op_3",
      seq: 1,
      items: [ev({ type: "request", clientStreaming: true }), ev({ type: "sent", index: 0, size: 2 }), ev({ type: "message", size: 4 })],
    });
    expect(call.getState()).toMatchObject({ sent: 1, sentBytes: 2, messages: 1, halfClosed: false });

    grpc.CloseSend.mockResolvedValueOnce(undefined);
    expect(await call.closeSend()).toBe(true);
    expect(grpc.CloseSend).toHaveBeenCalledWith({ opId: "op_3" });
    expect(call.getState().sendError).toBeNull();
    emit("grpc:op:data", { opId: "op_3", seq: 2, items: [ev({ type: "half-close", index: 1 })] });
    expect(call.getState().halfClosed).toBe(true);
    expect(call.canSend).toBe(false);
    expect(await call.send("{}")).toBe(false);
    expect(grpc.SendMessage).toHaveBeenCalledTimes(2);
    call.log.flush();
    expect(call.log.getSnapshot().kinds).toEqual(["open", "message", "half-close"]);
    resetGrpcCalls();
  });

  it("does not send on unary calls and server streams", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_4" });
    const call = grpcCalls().get("req_watch");
    call.invoke(input, { kind: "server_stream" });
    await settle();
    expect(call.canSend).toBe(false);
    expect(await call.closeSend()).toBe(false);
    expect(grpc.CloseSend).not.toHaveBeenCalled();
    resetGrpcCalls();
  });
});
