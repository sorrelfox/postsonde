import { beforeEach, describe, expect, it, vi } from "vitest";

const sse = vi.hoisted(() => ({
  Connect: vi.fn<(input: { requestId: string; allowUnresolved: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse", () => ({
  SSEService: sse,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import type { StreamItemDTO } from "../lib/sse";
import { createStores } from "../stores";
import { emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { resetSseStreams, sseStreams, streamLogMessage } from "./sseStreams";
import { closeTabs } from "./tabs";

const AT = "2026-09-19T10:00:00.000Z";

function item(over: Partial<StreamItemDTO>): StreamItemDTO {
  return {
    type: "event",
    at: AT,
    method: "",
    url: "",
    status: 0,
    statusText: "",
    proto: "",
    headers: null,
    index: 0,
    event: "",
    id: "",
    data: "",
    truncated: false,
    size: 0,
    retryMs: null,
    line: 0,
    text: "",
    attempt: 0,
    maxAttempts: 0,
    delayMs: 0,
    reason: "",
    events: 0,
    bytes: 0,
    reconnects: 0,
    discarded: false,
    elapsedMs: 0,
    tests: null,
    ...over,
  };
}

beforeEach(() => {
  resetFakeWails();
  resetSseStreams();
  sse.Connect.mockReset();
});

describe("streamLogMessage", () => {
  it("shows each event's event, id, data and retry fields (SSE-02)", () => {
    const t = Date.parse(AT);
    expect(
      streamLogMessage(
        item({
          type: "open",
          method: "GET",
          url: "http://x/events",
          status: 200,
          statusText: "200 OK",
          proto: "HTTP/1.1",
          headers: [{ name: "Content-Type", value: "text/event-stream" }],
        }),
      ),
    ).toEqual({
      time: t,
      direction: "system",
      kind: "open",
      badge: "200",
      text: "HTTP/1.1 200 OK\nContent-Type: text/event-stream",
      meta: { method: "GET", url: "http://x/events" },
    });
    expect(streamLogMessage(item({ type: "event", index: 2, event: "price", id: "7", data: '{"px":1}', size: 8, retryMs: 3000, line: 4 }))).toEqual({
      time: t,
      direction: "received",
      kind: "event",
      badge: "price",
      text: '{"px":1}',
      size: 8,
      meta: { event: "price", index: 2, id: "7", retry: "3000 ms", line: 4 },
    });
    expect(streamLogMessage(item({ type: "event", index: 1, event: "message", data: "abc", size: 10, truncated: true })).meta).toEqual({
      event: "message",
      index: 1,
      shown: "the first 3 of 10 bytes",
    });
    expect(streamLogMessage(item({ type: "comment", text: " ping", line: 9 }))).toMatchObject({ direction: "received", kind: "comment", text: " ping" });
    expect(streamLogMessage(item({ type: "retry", retryMs: 1500, line: 2 }))).toMatchObject({ kind: "retry", text: "1500 ms", meta: { retry: "1500 ms" } });
    expect(streamLogMessage(item({ type: "notice", text: "not a stream" }))).toMatchObject({ direction: "system", kind: "notice", text: "not a stream" });
    expect(streamLogMessage(item({ type: "end", reason: "closed", events: 1, bytes: 20, id: "7", discarded: true, elapsedMs: 40 }))).toEqual({
      time: t,
      direction: "system",
      kind: "closed",
      text: "The server ended the stream after 1 event. An incomplete event at the end was dropped.",
      meta: { events: 1, bytes: 20, elapsed: "40 ms", "last event id": "7" },
    });
  });
});

describe("SseStream", () => {
  it("connects, shows the stream live, ignores a second connect and stops when its tab closes", async () => {
    sse.Connect.mockResolvedValue({ opId: "op_1" });
    const stream = sseStreams().get("req_sse");
    stream.connect(undefined, { now: 1000 });
    stream.connect();
    expect(stream.getState()).toMatchObject({ status: "starting", startedAt: 1000 });
    await settle();
    expect(sse.Connect).toHaveBeenCalledTimes(1);
    expect(sse.Connect).toHaveBeenCalledWith({ requestId: "req_sse", allowUnresolved: false, lastEventId: "" });
    expect(stream.getState().status).toBe("running");
    emit("sse:op:data", {
      opId: "op_1",
      seq: 1,
      items: [
        item({ type: "open", status: 200, statusText: "200 OK", headers: [{ name: "A", value: "b" }] }),
        item({ type: "retry", retryMs: 2000 }),
        item({ type: "event", index: 1, event: "message", id: "1", data: "hello", size: 5 }),
      ],
    });
    expect(stream.getState()).toMatchObject({
      head: { status: 200, headers: [{ name: "A", value: "b" }] },
      events: 1,
      dataBytes: 5,
      lastEventId: "1",
      retryMs: 2000,
    });
    stream.log.flush();
    expect(stream.log.getSnapshot().count).toBe(3);

    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_sse"], activeId: "req_sse" });
    closeTabs(stores, ["req_sse"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    expect(sseStreams().peek("req_sse")).toBeUndefined();
    resetSseStreams();
    expect(listenerCount()).toBe(0);
  });

  it("stops, and records the end and the outcome", async () => {
    sse.Connect.mockResolvedValue({ opId: "op_2" });
    const stream = sseStreams().get("req_stop");
    stream.connect();
    await settle();
    stream.stop();
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_2");
    emit("sse:op:data", { opId: "op_2", seq: 1, items: [item({ type: "end", reason: "stopped", id: "4", elapsedMs: 900 })] });
    emit("sse:op:done", { opId: "op_2", seq: 2, status: "cancelled", error: null, items: 1, batches: 1, elapsedMs: 901 });
    expect(stream.getState()).toMatchObject({ status: "cancelled", endReason: "stopped", lastEventId: "4", elapsedMs: 900 });
  });

  it("logs the error of a stream that fails before it opens", async () => {
    sse.Connect.mockResolvedValue({ opId: "op_3" });
    const stream = sseStreams().get("req_fail");
    stream.connect();
    await settle();
    const error = { kind: "network", code: "netx.connection_refused", message: "Refused.", hint: "Is it running?", details: {}, causes: [] };
    emit("sse:op:done", { opId: "op_3", seq: 1, status: "error", error, items: 0, batches: 0, elapsedMs: 3 });
    expect(stream.getState()).toMatchObject({ status: "error", head: null, error: { code: "netx.connection_refused" } });
    stream.log.flush();
    expect(stream.log.getSnapshot().count).toBe(1);
  });

  it("follows reconnects: the wait, the attempts made and the resumed ID (SSE-03)", async () => {
    sse.Connect.mockResolvedValue({ opId: "op_4" });
    const stream = sseStreams().get("req_re");
    stream.connect();
    await settle();
    const open = item({ type: "open", status: 200, statusText: "200 OK" });
    stream.handleItems([open, item({ type: "event", id: "3", data: "x", size: 1 })]);
    stream.handleItems([item({ type: "reconnect", id: "3", attempt: 1, maxAttempts: 0, delayMs: 500, reason: "closed", text: "The server ended the stream." })], 10_000);
    expect(stream.getState()).toMatchObject({ reconnect: { attempt: 1, maxAttempts: 0, delayMs: 500, until: 10_500 }, reconnects: 0 });
    // A failed attempt is followed by the next wait.
    stream.handleItems([item({ type: "reconnect", id: "3", attempt: 2, maxAttempts: 0, delayMs: 1000, reason: "error", text: "Refused." })], 11_000);
    expect(stream.getState()).toMatchObject({ reconnect: { attempt: 2 }, reconnects: 1 });
    stream.handleItems([open]);
    expect(stream.getState()).toMatchObject({ reconnect: null, reconnects: 2 });
    stream.handleItems([item({ type: "end", reason: "stopped", id: "3", reconnects: 2 })]);
    expect(stream.getState()).toMatchObject({ reconnect: null, reconnects: 2, lastEventId: "3" });
    stream.log.flush();
    const texts = [...Array(stream.log.getSnapshot().count).keys()].map((i) => stream.log.get(i)?.text);
    expect(texts).toContain("The server ended the stream. Reconnecting in 500 ms with Last-Event-ID 3 (attempt 1).");
    emit("sse:op:done", { opId: "op_4", seq: 1, status: "cancelled", error: null, items: 5, batches: 1, elapsedMs: 3 });

    // Resuming sends the last event ID and keeps the log and the counts.
    sse.Connect.mockResolvedValue({ opId: "op_5" });
    stream.connect(undefined, { resume: true });
    await settle();
    expect(sse.Connect).toHaveBeenLastCalledWith({ requestId: "req_re", allowUnresolved: false, lastEventId: "3" });
    expect(stream.getState()).toMatchObject({ status: "running", events: 1, lastEventId: "3", reconnects: 2 });
    stream.handleItems([item({ type: "end", reason: "closed", id: "4", reconnects: 1 })]);
    expect(stream.getState().reconnects).toBe(3);
    stream.log.flush();
    expect(stream.log.getSnapshot().count).toBeGreaterThan(4);
  });

  it("logs a reconnect with its attempt and delay", () => {
    expect(streamLogMessage(item({ type: "reconnect", id: "", attempt: 2, maxAttempts: 5, delayMs: 6000, reason: "error", text: "Refused." }))).toMatchObject({
      direction: "system",
      kind: "reconnect",
      text: "Refused. Reconnecting in 6000 ms (attempt 2 of 5).",
      meta: { attempt: "2 of 5", delay: "6000 ms" },
    });
    expect(streamLogMessage(item({ type: "end", reason: "closed", events: 0, reconnects: 3 })).meta).toMatchObject({ reconnects: 3 });
  });
});
