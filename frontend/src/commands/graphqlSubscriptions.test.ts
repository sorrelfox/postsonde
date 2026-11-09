import { beforeEach, describe, expect, it, vi } from "vitest";

const graphql = vi.hoisted(() => ({
  Subscribe: vi.fn<(input: unknown) => Promise<unknown>>(),
  StopSubscription: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphql,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import type { RequestSettingsDTO } from "../lib/collection";
import {
  runningOperationType,
  runsOverSocket,
  runsSubscription,
  subscriptionLogMessage,
  transportChoices,
  transportFor,
  transportPatch,
  type SubscriptionEventDTO,
} from "../lib/graphqlSubscription";
import { createStores } from "../stores";
import { emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { confirmQueue } from "./dialogs";
import { graphqlSubscriptions, resetGraphqlSubscriptions } from "./graphqlSubscriptions";
import { closeTabs } from "./tabs";

const AT = "2026-09-19T10:00:00.000Z";

function ev(over: Partial<SubscriptionEventDTO>): SubscriptionEventDTO {
  return {
    at: AT,
    kind: "message",
    direction: "received",
    type: "",
    id: "",
    payload: "",
    size: 0,
    binary: false,
    errorCount: 0,
    note: "",
    url: "",
    subprotocol: "",
    close: null,
    error: null,
    ...over,
  };
}

const done = (opId: string, status: string, seq = 9) => ({ opId, seq, status, error: null, items: 0, batches: 0, elapsedMs: 42 });

beforeEach(() => {
  resetFakeWails();
  resetGraphqlSubscriptions();
  graphql.Subscribe.mockReset();
  graphql.StopSubscription.mockReset().mockResolvedValue(true);
});

describe("runningOperationType", () => {
  it("is the only operation's type, or the picked one's", () => {
    expect(runningOperationType([], "")).toBeNull();
    expect(runningOperationType([{ name: "", type: "subscription" }], "X")).toBe("subscription");
    const ops = [
      { name: "A", type: "query" },
      { name: "B", type: "subscription" },
    ];
    expect(runningOperationType(ops, "")).toBeNull();
    expect(runningOperationType(ops, "B")).toBe("subscription");
    expect(runningOperationType(ops, "C")).toBeNull();
    expect(runsSubscription(ops, "B")).toBe(true);
    expect(runsSubscription(ops, "A")).toBe(false);
  });
});

describe("transport per operation (GQL-35)", () => {
  const settings = (graphqlTransport: RequestSettingsDTO["graphqlTransport"]) => ({ graphqlTransport }) as RequestSettingsDTO;

  it("picks the stored transport or the default, as Go does", () => {
    expect(transportFor(settings(undefined), "query")).toBe("http");
    expect(transportFor(settings(null), "subscription")).toBe("ws");
    expect(transportFor(settings(null), null)).toBe("http");
    const all = settings({ query: "ws", mutation: "sse", subscription: "ws-legacy", extra: "" });
    expect(transportFor(all, "query")).toBe("ws");
    expect(transportFor(all, null)).toBe("ws");
    expect(transportFor(all, "mutation")).toBe("sse");
    expect(transportFor(all, "subscription")).toBe("ws-legacy");
  });

  it("runs subscriptions and queries over WebSocket through the slot, the rest with Send", () => {
    expect(runsOverSocket(settings(null), "subscription")).toBe(true);
    expect(runsOverSocket(settings({ query: "", mutation: "", subscription: "sse", extra: "" }), "subscription")).toBe(true);
    expect(runsOverSocket(settings(null), "query")).toBe(false);
    expect(runsOverSocket(settings({ query: "ws-legacy", mutation: "sse", subscription: "", extra: "" }), "query")).toBe(true);
    expect(runsOverSocket(settings({ query: "ws-legacy", mutation: "sse", subscription: "", extra: "" }), "mutation")).toBe(false);
  });

  it("stores only what differs from the defaults", () => {
    expect(transportPatch(null, "query", "ws")).toEqual({ query: "ws", mutation: "", subscription: "", extra: "" });
    expect(transportPatch({ query: "ws", mutation: "", subscription: "", extra: "" }, "query", "")).toBeNull();
    expect(transportPatch({ query: "ws", mutation: "", subscription: "", extra: '{"x":1}' }, "query", "")).toEqual({ query: "", mutation: "", subscription: "", extra: '{"x":1}' });
    expect(transportChoices("subscription")).not.toContain("http");
  });
});

describe("subscriptionLogMessage", () => {
  it("maps the events of legacy WebSocket and SSE runs", () => {
    expect(subscriptionLogMessage(ev({ kind: "connecting", direction: "", url: "http://h/g", method: "POST", transport: "sse", operationType: "subscription" }))).toMatchObject({
      kind: "connecting",
      text: "Sending POST http://h/g",
      meta: { url: "http://h/g", transport: "sse", operation: "subscription" },
    });
    expect(subscriptionLogMessage(ev({ kind: "open", direction: "", status: 200, contentType: "text/event-stream" }))).toMatchObject({
      kind: "open",
      text: "HTTP 200 · text/event-stream",
    });
    expect(subscriptionLogMessage(ev({ type: "keep-alive", payload: " ping", size: 6 }))).toMatchObject({ kind: "keep-alive", direction: "received" });
    expect(subscriptionLogMessage(ev({ type: "data", id: "1", payload: '{"errors":[{}]}', errorCount: 1 }))).toMatchObject({ kind: "data", badge: "1 error" });
    expect(subscriptionLogMessage(ev({ type: "response", payload: "{}", note: "not_event_stream" }))).toMatchObject({
      meta: { note: "the server answered without an event stream" },
    });
  });

  it("counts legacy data and SSE responses as events, and keeps the result of a query", () => {
    const slot = graphqlSubscriptions().get("req_1");
    slot.handleItems([
      ev({ kind: "connecting", direction: "", transport: "sse", operationType: "query" }),
      ev({ kind: "open", direction: "", status: 200, contentType: "text/event-stream" }),
      ev({ type: "next", payload: '{"data":{"a":1},"hasNext":true}', size: 10 }),
      ev({ type: "data", id: "1", payload: '{"data":{}}', size: 4, errorCount: 1 }),
      ev({ type: "data", id: "9", payload: '{"data":{}}', size: 4, note: "unknown_id" }),
    ]);
    expect(slot.getState()).toMatchObject({ transport: "sse", operationType: "query", connected: true, events: 2, errors: 1, receivedBytes: 14, result: '{"data":{}}' });
    slot.handleItems([ev({ kind: "result", direction: "", payload: '{"data":{"a":1,"b":2}}' }), ev({ kind: "closed", direction: "" })]);
    expect(slot.getState()).toMatchObject({ result: '{"data":{"a":1,"b":2}}', connected: false });
    slot.log.flush();
    const { firstSeq, nextSeq } = slot.log.getSnapshot();
    const kinds = Array.from({ length: nextSeq - firstSeq }, (_, i) => slot.log.get(firstSeq + i)?.kind);
    expect(kinds).toEqual(["connecting", "open", "next", "data", "data", "closed"]);
  });
});

describe("subscriptionLogMessage (graphql-transport-ws)", () => {
  it("maps the events of the log (GQL-34)", () => {
    const t = Date.parse(AT);
    expect(subscriptionLogMessage(ev({ kind: "connecting", direction: "", url: "wss://h/g" }))).toEqual({
      time: t,
      direction: "system",
      kind: "connecting",
      text: "Connecting to wss://h/g",
      meta: { url: "wss://h/g" },
    });
    expect(subscriptionLogMessage(ev({ kind: "open", direction: "", subprotocol: "graphql-transport-ws" }))).toMatchObject({
      direction: "system",
      kind: "open",
      text: "Connected with graphql-transport-ws",
    });
    expect(subscriptionLogMessage(ev({ direction: "sent", type: "connection_init", payload: '{"t":1}', size: 40 }))).toEqual({
      time: t,
      direction: "sent",
      kind: "connection_init",
      badge: undefined,
      text: '{"t":1}',
      size: 40,
      meta: undefined,
    });
    expect(subscriptionLogMessage(ev({ type: "next", id: "1", payload: '{"data":null,"errors":[{},{}]}', errorCount: 2, size: 50 }))).toMatchObject({
      direction: "received",
      kind: "next",
      badge: "2 errors",
      meta: { id: "1", errors: 2 },
    });
    expect(subscriptionLogMessage(ev({ type: "next", id: "1", payload: "{}", note: "unknown_id" }))).toMatchObject({
      badge: "id 1",
      meta: { note: expect.stringContaining("ignored") },
    });
    const err = { kind: "protocol", code: "transportws.invalid_message", message: "bad message", hint: "", details: {}, causes: [] };
    expect(subscriptionLogMessage(ev({ payload: "nope", size: 4, error: err }))).toMatchObject({ kind: "invalid", text: "nope", meta: { error: "bad message" } });
    expect(
      subscriptionLogMessage(
        ev({ kind: "closed", direction: "", close: { code: 4403, codeName: "forbidden", reason: "no", wasClean: true, initiatedBy: "remote" } }),
      ),
    ).toEqual({
      time: t,
      direction: "system",
      kind: "closed",
      text: "Closed 4403 (forbidden): no",
      meta: { code: 4403, reason: "no", clean: true, "initiated by": "remote" },
    });
    expect(
      subscriptionLogMessage(
        ev({ kind: "closed", direction: "", close: { code: 1006, codeName: "abnormal closure", reason: "", wasClean: false, initiatedBy: "none" }, error: err }),
      ),
    ).toMatchObject({ text: "bad message", meta: { error: "bad message" } });
  });
});

describe("GraphqlSubscription", () => {
  it("streams events into the log, stops through Go and restarts with a fresh log", async () => {
    graphql.Subscribe.mockResolvedValueOnce({ opId: "op_1" }).mockResolvedValueOnce({ opId: "op_2" });
    const sub = graphqlSubscriptions().get("req_sub");
    sub.start(undefined, { now: 1000 });
    sub.start();
    expect(sub.getState()).toMatchObject({ status: "starting", startedAt: 1000 });
    await settle();
    expect(graphql.Subscribe).toHaveBeenCalledTimes(1);
    expect(graphql.Subscribe).toHaveBeenCalledWith({ requestId: "req_sub", allowUnresolved: false });
    await settle();
    expect(sub.getState().status).toBe("running");

    emit("graphql.subscription:op:data", {
      opId: "op_1",
      seq: 1,
      items: [
        ev({ kind: "connecting", direction: "" }),
        ev({ kind: "open", direction: "" }),
        ev({ direction: "sent", type: "connection_init" }),
        ev({ type: "connection_ack" }),
        ev({ direction: "sent", type: "subscribe", id: "1" }),
        ev({ type: "next", id: "1", payload: "{}", size: 10 }),
        ev({ type: "next", id: "1", payload: "{}", size: 5, errorCount: 1 }),
      ],
    });
    expect(sub.getState()).toMatchObject({ connected: true, events: 2, errors: 1, receivedBytes: 15 });
    sub.log.flush();
    expect(sub.log.getSnapshot().count).toBe(7);

    sub.stop();
    await settle();
    expect(graphql.StopSubscription).toHaveBeenCalledWith({ opId: "op_1" });
    const close = { code: 1000, codeName: "normal closure", reason: "", wasClean: true, initiatedBy: "local" };
    emit("graphql.subscription:op:data", { opId: "op_1", seq: 2, items: [ev({ direction: "sent", type: "complete", id: "1" }), ev({ kind: "closed", direction: "", close })] });
    emit("graphql.subscription:op:done", done("op_1", "cancelled", 3));
    expect(sub.getState()).toMatchObject({ status: "cancelled", connected: false, close, elapsedMs: 42, events: 2 });

    sub.start();
    expect(sub.getState()).toMatchObject({ status: "starting", events: 0, close: null });
    sub.log.flush();
    expect(sub.log.getSnapshot().count).toBe(0);
    await settle();
    emit("graphql.subscription:op:done", done("op_2", "ok", 1));
    expect(sub.getState().status).toBe("ok");
    resetGraphqlSubscriptions();
    expect(listenerCount()).toBe(0);
  });

  it("logs a refused start and is stopped when its tab closes", async () => {
    graphql.Subscribe.mockRejectedValueOnce(Object.assign(new Error("refused"), {
      cause: { kind: "invalid_argument", code: "request.graphql_ws_scheme", message: "Subscriptions need an http, https, ws or wss endpoint.", hint: "", details: {}, causes: [] },
    }));
    const sub = graphqlSubscriptions().get("req_sub");
    sub.start();
    await settle();
    await settle();
    expect(sub.getState()).toMatchObject({ status: "error", error: { code: "request.graphql_ws_scheme" } });
    sub.log.flush();
    expect(sub.log.getSnapshot().count).toBe(1);

    graphql.Subscribe.mockResolvedValueOnce({ opId: "op_3" });
    sub.start();
    await settle();
    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_sub"], activeId: "req_sub" });
    closeTabs(stores, ["req_sub"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_3");
    expect(graphqlSubscriptions().peek("req_sub")).toBeUndefined();
  });

  describe("undefined variables (ENV-11)", () => {
    let detach: () => void = () => {};
    beforeEach(() => {
      detach = confirmQueue.attach();
      return () => detach();
    });

    it("asks, and subscribes anyway", async () => {
      graphql.Subscribe.mockRejectedValueOnce(Object.assign(new Error("refused"), {
        cause: { kind: "invalid_argument", code: "template.unresolved", message: "", hint: "", details: { names: "token" }, causes: [] },
      })).mockResolvedValueOnce({ opId: "op_4" });
      const sub = graphqlSubscriptions().get("req_sub");
      sub.start();
      await settle();
      await settle();
      const asked = confirmQueue.current();
      expect(asked).toMatchObject({ title: "Subscribe with an undefined variable?", confirmLabel: "Subscribe anyway" });
      confirmQueue.settle(asked!.id, true);
      await settle();
      await settle();
      expect(graphql.Subscribe).toHaveBeenLastCalledWith({ requestId: "req_sub", allowUnresolved: true });
      expect(sub.getState().status).toBe("running");
    });
  });
});
