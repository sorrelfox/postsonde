import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  deferred,
  emit,
  FakeCancelError,
  fakeRuntime,
  listenerCount,
  opsCancel,
  resetFakeWails,
  settle,
} from "../test/fakeWails";
import {
  operationEventName,
  startOperation,
  type OperationHandlers,
  type OperationResult,
  type StartedDTO,
} from "./operations";

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

const data = (opId: string, seq: number, items: number[] | null) => emit("t:op:data", { opId, seq, items });
const progress = (opId: string, seq: number, done: number) =>
  emit("t:op:progress", { opId, seq, done, total: 10, message: "" });
const done = (opId: string, seq: number, status: string, error: unknown = null) =>
  emit("t:op:done", { opId, seq, status, error, items: 3, batches: 2, elapsedMs: 40 });
const boom = {
  kind: "network",
  code: "t.boom",
  message: "boom",
  hint: "Try again.",
  details: { address: "x:1" },
  causes: ["dial tcp x:1: refused"],
};

function recording() {
  const log: string[] = [];
  const handlers: OperationHandlers<number> = {
    onStarted: (id) => log.push(`started ${id}`),
    onData: (items) => log.push(`data ${items.join(",")}`),
    onProgress: (e) => log.push(`progress ${e.done}`),
    onGap: (expected, got) => log.push(`gap ${expected}->${got}`),
    onDone: (r) =>
      log.push(`done ${r.opId} ${r.outcome}${r.error ? ` ${r.error.kind}:${r.error.message}` : ""} items=${r.items}`),
  };
  return { log, handlers };
}

/** Starts an operation of domain "t" whose start call the test resolves. */
function begin() {
  const call = deferred<StartedDTO>();
  const { log, handlers } = recording();
  const handle = startOperation<number>("t", () => call.promise, handlers);
  return { call, log, handle };
}

beforeEach(() => {
  resetFakeWails();
});

describe("operationEventName", () => {
  it("builds <domain>:op:<phase>", () => {
    expect(operationEventName("demo", "data")).toBe("demo:op:data");
    expect(operationEventName("http", "done")).toBe("http:op:done");
  });
});

describe("startOperation", () => {
  it("subscribes to the domain's three events before starting", () => {
    let subscribedAtStart = 0;
    startOperation("demo", () => {
      subscribedAtStart = listenerCount();
      return new Promise<StartedDTO>(() => {});
    }, {});
    expect(fakeRuntime.Events.On.mock.calls.map((c) => c[0])).toEqual([
      "demo:op:data",
      "demo:op:progress",
      "demo:op:done",
    ]);
    expect(subscribedAtStart).toBe(3);
  });

  it("delivers events in order and ends with done", async () => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    data("op_a", 1, [1, 2]);
    progress("op_a", 2, 2);
    data("op_a", 3, [3]);
    done("op_a", 4, "ok");
    expect(log).toEqual(["started op_a", "data 1,2", "progress 2", "data 3", "done op_a ok items=3"]);
    expect(listenerCount()).toBe(0);

    data("op_a", 5, [4]); // after done: ignored
    expect(log).toHaveLength(5);
  });

  it("buffers events that arrive before the start call resolves", async () => {
    const { call, log } = begin();
    data("op_a", 1, [1]);
    data("op_other", 1, [99]);
    progress("op_a", 2, 1);
    expect(log).toEqual([]);

    call.resolve({ opId: "op_a" });
    await settle();
    expect(log).toEqual(["started op_a", "data 1", "progress 1"]);
  });

  it("delivers a done event that beat the start call", async () => {
    const { call, log } = begin();
    data("op_a", 1, [1]);
    done("op_a", 2, "ok");
    call.resolve({ opId: "op_a" });
    await settle();
    expect(log).toEqual(["started op_a", "data 1", "done op_a ok items=3"]);
    expect(listenerCount()).toBe(0);
  });

  it("ignores other operations of the same domain", async () => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    data("op_b", 1, [7]);
    done("op_b", 2, "ok");
    expect(log).toEqual(["started op_a"]);
    expect(listenerCount()).toBe(3);
  });

  it("reports gaps and drops duplicates", async () => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    data("op_a", 1, [1]);
    data("op_a", 3, [3]);
    data("op_a", 3, [3]);
    data("op_a", 2, [2]);
    expect(log).toEqual(["started op_a", "data 1", "gap 2->3", "data 3"]);
  });

  it("treats null items as an empty batch and ignores malformed payloads", async () => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    data("op_a", 1, null);
    emit("t:op:data", "garbage");
    emit("t:op:data", { opId: "op_a" });
    expect(log).toEqual(["started op_a", "data "]);
  });

  it.each([
    ["ok", "done op_a ok items=3"],
    ["cancelled", "done op_a cancelled items=3"],
    ["error", "done op_a error network:boom items=3"],
    ["weird", "done op_a error network:boom items=3"],
  ])("maps done status %s", async (status, want) => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    done("op_a", 1, status, status === "ok" || status === "cancelled" ? null : boom);
    expect(log[log.length - 1]).toBe(want);
  });

  it("passes the done event's ErrorDTO on as an AppError", async () => {
    const { call } = begin();
    const results: OperationResult[] = [];
    startOperation<number>("t", () => call.promise, { onDone: (r) => results.push(r) });
    call.resolve({ opId: "op_a" });
    await settle();
    done("op_a", 1, "error", boom);
    expect(results[0].error).toEqual(boom);
  });

  it("reports an error outcome without an ErrorDTO as internal", async () => {
    const { call, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    done("op_a", 1, "error", null);
    expect(log[log.length - 1]).toBe("done op_a error internal:Something went wrong inside PostSonde. items=3");
  });

  it("reports a failed start as an error and unsubscribes", async () => {
    const { call, log } = begin();
    call.reject(new Error("rate too high"));
    await settle();
    expect(log).toEqual(["done null error internal:rate too high items=0"]);
    expect(listenerCount()).toBe(0);
  });

  it("reports a cancelled start call as cancelled", async () => {
    const { call, log } = begin();
    call.reject(new FakeCancelError());
    await settle();
    expect(log).toEqual(["done null cancelled items=0"]);
  });

  it("reports a start function that throws", async () => {
    const { log, handlers } = recording();
    startOperation<number>("t", () => {
      throw new Error("no binding");
    }, handlers);
    await settle();
    expect(log).toEqual(["done null error internal:no binding items=0"]);
  });
});

describe("cancel", () => {
  it("cancels by ID once running", async () => {
    const { call, handle } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    handle.cancel();
    handle.cancel(); // sent once
    expect(opsCancel.mock.calls).toEqual([["op_a"]]);
    expect(handle.opId()).toBe("op_a");
  });

  it("sends a cancel asked for before the ID was known", async () => {
    const { call, handle } = begin();
    handle.cancel();
    expect(opsCancel).not.toHaveBeenCalled();
    call.resolve({ opId: "op_a" });
    await settle();
    expect(opsCancel.mock.calls).toEqual([["op_a"]]);
  });

  it("still cancels after dispose if the start resolves later", async () => {
    const { call, handle, log } = begin();
    handle.cancel();
    handle.dispose();
    call.resolve({ opId: "op_a" });
    await settle();
    expect(opsCancel.mock.calls).toEqual([["op_a"]]);
    expect(log).toEqual([]);
  });

  it("does nothing after done", async () => {
    const { call, handle } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    done("op_a", 1, "ok");
    handle.cancel();
    expect(opsCancel).not.toHaveBeenCalled();
  });

  it("ignores a failing cancel call", async () => {
    opsCancel.mockImplementationOnce(() => Promise.reject(new Error("gone")));
    const { call, handle } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    handle.cancel();
    await settle(); // an unhandled rejection would fail the run
    expect(opsCancel).toHaveBeenCalledTimes(1);
  });
});

describe("dispose", () => {
  it("unsubscribes and stops all callbacks", async () => {
    const { call, handle, log } = begin();
    call.resolve({ opId: "op_a" });
    await settle();
    handle.dispose();
    expect(listenerCount()).toBe(0);
    data("op_a", 1, [1]);
    expect(log).toEqual(["started op_a"]);
  });
});
