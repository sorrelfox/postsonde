import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { StartedDTO } from "../lib/operations";
import { deferred, emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { useOperation } from "./useOperation";

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

interface Item {
  n: number;
}

/** Renders the hook with a start function the test resolves. */
function setup(keep?: number) {
  const calls: { args: [number]; call: ReturnType<typeof deferred<StartedDTO>> }[] = [];
  const start = vi.fn((size: number) => {
    const call = deferred<StartedDTO>();
    calls.push({ args: [size], call });
    return call.promise;
  });
  const hook = renderHook(() => useOperation<Item, [number]>("t", start, { keep }));
  return { hook, start, calls };
}

async function started(resolve: () => void) {
  await act(async () => {
    resolve();
    await settle();
  });
}

const items = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => ({ n: from + i }));

beforeEach(() => {
  resetFakeWails();
});

describe("useOperation", () => {
  it("counts batches and items, keeps the latest, and finishes", async () => {
    const { hook, start, calls } = setup(3);
    expect(hook.result.current.status).toBe("idle");

    act(() => hook.result.current.start(5));
    expect(start).toHaveBeenCalledWith(5);
    expect(hook.result.current.status).toBe("starting");
    expect(hook.result.current.running).toBe(true);

    await started(() => calls[0].call.resolve({ opId: "op_a" }));
    expect(hook.result.current.status).toBe("running");
    expect(hook.result.current.opId).toBe("op_a");

    act(() => {
      emit("t:op:data", { opId: "op_a", seq: 1, items: items(0, 4) });
      emit("t:op:progress", { opId: "op_a", seq: 2, done: 4, total: 5, message: "" });
      emit("t:op:data", { opId: "op_a", seq: 3, items: items(4, 5) });
    });
    const r = hook.result.current;
    expect(r.received).toBe(5);
    expect(r.batches).toBe(2);
    expect(r.items.map((i) => i.n)).toEqual([2, 3, 4]);
    expect(r.progress?.done).toBe(4);
    expect(r.gaps).toBe(0);

    act(() =>
      emit("t:op:done", { opId: "op_a", seq: 4, status: "ok", error: null,items: 5, batches: 2, elapsedMs: 30 }),
    );
    expect(hook.result.current.status).toBe("ok");
    expect(hook.result.current.running).toBe(false);
    expect(hook.result.current.result?.elapsedMs).toBe(30);
    expect(hook.result.current.finishedAt).not.toBeNull();
    expect(listenerCount()).toBe(0);
  });

  it("passes every batch to onItems, beyond keep", async () => {
    const onItems = vi.fn();
    const start = vi.fn(() => Promise.resolve({ opId: "op_i" }));
    const hook = renderHook(() => useOperation<Item, []>("t", start, { keep: 1, onItems }));
    act(() => hook.result.current.start());
    await started(() => undefined);
    act(() => {
      emit("t:op:data", { opId: "op_i", seq: 1, items: items(0, 3) });
      emit("t:op:data", { opId: "op_other", seq: 1, items: items(9, 10) });
      emit("t:op:data", { opId: "op_i", seq: 2, items: items(3, 4) });
    });
    expect(onItems.mock.calls.map(([batch]) => (batch as Item[]).map((i) => i.n))).toEqual([[0, 1, 2], [3]]);
    expect(hook.result.current.items.map((i) => i.n)).toEqual([3]);
  });

  it("counts missed events", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.resolve({ opId: "op_a" }));
    act(() => {
      emit("t:op:data", { opId: "op_a", seq: 1, items: items(0, 1) });
      emit("t:op:data", { opId: "op_a", seq: 3, items: items(1, 2) });
    });
    expect(hook.result.current.gaps).toBe(1);
  });

  it("cancels by ID and reports the cancelled outcome", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.resolve({ opId: "op_a" }));
    act(() => hook.result.current.cancel());
    expect(opsCancel).toHaveBeenCalledWith("op_a");
    act(() =>
      emit("t:op:done", { opId: "op_a", seq: 1, status: "cancelled", error: null,items: 0, batches: 0, elapsedMs: 5 }),
    );
    expect(hook.result.current.status).toBe("cancelled");
  });

  it("reports a failed start", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.reject(new Error("ratePerSec must be between 1 and 100000")));
    expect(hook.result.current.status).toBe("error");
    expect(hook.result.current.error?.message).toBe("ratePerSec must be between 1 and 100000");
    expect(hook.result.current.opId).toBeNull();
  });

  it("cancels the running operation on unmount and unsubscribes", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.resolve({ opId: "op_a" }));
    hook.unmount();
    expect(opsCancel).toHaveBeenCalledWith("op_a");
    expect(listenerCount()).toBe(0);
  });

  it("cancels on unmount even before the ID is known", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    hook.unmount();
    expect(opsCancel).not.toHaveBeenCalled();
    await act(async () => {
      calls[0].call.resolve({ opId: "op_a" });
      await settle();
    });
    expect(opsCancel).toHaveBeenCalledWith("op_a");
  });

  it("does not cancel on unmount after the operation finished", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.resolve({ opId: "op_a" }));
    act(() =>
      emit("t:op:done", { opId: "op_a", seq: 1, status: "ok", error: null,items: 0, batches: 0, elapsedMs: 1 }),
    );
    hook.unmount();
    expect(opsCancel).not.toHaveBeenCalled();
  });

  it("starting again cancels the previous operation and ignores its events", async () => {
    const { hook, calls } = setup();
    act(() => hook.result.current.start(1));
    await started(() => calls[0].call.resolve({ opId: "op_a" }));

    act(() => hook.result.current.start(2));
    expect(opsCancel).toHaveBeenCalledWith("op_a");
    await started(() => calls[1].call.resolve({ opId: "op_b" }));
    act(() => {
      emit("t:op:data", { opId: "op_a", seq: 1, items: items(0, 9) });
      emit("t:op:data", { opId: "op_b", seq: 1, items: items(0, 2) });
    });
    expect(hook.result.current.opId).toBe("op_b");
    expect(hook.result.current.received).toBe(2);
  });
});
