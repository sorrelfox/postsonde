import { beforeEach, describe, expect, it, vi } from "vitest";

const runner = vi.hoisted(() => ({
  Start: vi.fn<(input: { folderId: string; iterations: number; delayMs: number; prompts?: Record<string, string> }) => Promise<unknown>>(),
  Plan: vi.fn<(id: string) => Promise<unknown>>(),
  Response: vi.fn<(opId: string, index: number) => Promise<unknown>>(),
  Release: vi.fn<(opId: string) => Promise<void>>(() => Promise.resolve()),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runner", () => ({
  RunnerService: runner,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import { createStores } from "../stores";
import { deferred, emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { runResult } from "../test/runResult";
import { MAX_FOLDER_RUN_RESULTS, addFolderRunResults, folderRuns, openFolderRun, resetFolderRuns, type FolderRunState } from "./folderRuns";
import { promptQueue } from "./prompts";
import { closeTabs } from "./tabs";

const done = (status: string, error: unknown = null) => ({ opId: "op_1", seq: 9, status, error, items: 1, batches: 1, elapsedMs: 50 });

beforeEach(() => {
  resetFakeWails();
  resetFolderRuns();
  runner.Start.mockReset();
  runner.Plan.mockReset();
  runner.Response.mockReset();
  runner.Release.mockClear();
});

describe("addFolderRunResults", () => {
  it("adds the results and counts each outcome", () => {
    const state = folderRuns().get("fld_1").getState();
    const next = addFolderRunResults(state, [
      runResult(1),
      runResult(2, { outcome: "failed" }),
      runResult(3, { outcome: "error" }),
      runResult(4, { outcome: "skipped" }),
      runResult(5),
    ]);
    expect(next.counts).toEqual({ passed: 2, failed: 1, error: 1, skipped: 1 });
    expect(next.done).toBe(5);
    expect(next.results.map((r) => r.index)).toEqual([1, 2, 3, 4, 5]);
    expect(addFolderRunResults(next, [])).toBe(next);
  });

  it("times the requests sent, not the skipped ones (RUN-07)", () => {
    const state = addFolderRunResults(folderRuns().get("fld_1").getState(), [
      runResult(1, { durationMs: 30, name: "A" }),
      runResult(2, { durationMs: 900, outcome: "skipped" }),
      runResult(3, { durationMs: 70, name: "C", iteration: 2 }),
      runResult(4, { durationMs: 5, outcome: "error" }),
    ]);
    expect(state.timing).toEqual({ sent: 3, totalMs: 105, minMs: 5, maxMs: 70, slowest: { index: 3, name: "C", iteration: 2 } });
  });

  it("keeps the latest results only, and counts them all", () => {
    let state: FolderRunState = folderRuns().get("fld_1").getState();
    const batch = Array.from({ length: MAX_FOLDER_RUN_RESULTS + 10 }, (_, i) => runResult(i + 1));
    state = addFolderRunResults(state, batch);
    expect(state.results).toHaveLength(MAX_FOLDER_RUN_RESULTS);
    expect(state.results[0]?.index).toBe(11);
    expect(state.counts.passed).toBe(MAX_FOLDER_RUN_RESULTS + 10);
  });
});

describe("FolderRun", () => {
  it("prepares, starts, follows the progress and lists the results", async () => {
    runner.Start.mockResolvedValue({ opId: "op_1" });
    const prepared = deferred<void>();
    const run = folderRuns().get("fld_1");
    run.start({ iterations: 2, delayMs: 500 }, () => prepared.promise);
    expect(run.getState()).toMatchObject({ status: "starting", options: { iterations: 2, delayMs: 500 } });
    await settle();
    // Nothing is sent before the edits reached Go.
    expect(runner.Start).not.toHaveBeenCalled();
    prepared.resolve();
    await settle();
    expect(runner.Start).toHaveBeenCalledWith({ folderId: "fld_1", iterations: 2, delayMs: 500 });
    expect(run.getState().status).toBe("running");

    // A second start while it runs does nothing.
    run.start({ iterations: 1, delayMs: 0 });
    expect(runner.Start).toHaveBeenCalledTimes(1);

    emit("runner:op:progress", { opId: "op_1", seq: 1, done: 0, total: 4, message: "Sending" });
    emit("runner:op:data", { opId: "op_1", seq: 2, items: [runResult(1), runResult(2, { outcome: "failed", testsFailed: 1 })] });
    emit("runner:op:progress", { opId: "op_1", seq: 3, done: 2, total: 4, message: "Waiting before the next request" });
    expect(run.getState()).toMatchObject({ done: 2, total: 4, waiting: true, counts: { passed: 1, failed: 1 } });
    emit("runner:op:data", { opId: "op_1", seq: 4, items: [runResult(3, { iteration: 2 }), runResult(4, { iteration: 2, outcome: "skipped" })] });
    emit("runner:op:done", done("ok"));
    expect(run.getState()).toMatchObject({ status: "ok", waiting: false, done: 4, error: null, counts: { passed: 2, failed: 1, skipped: 1 } });
    expect(run.getState().results).toHaveLength(4);
  });

  it("asks for the {{$prompt}} values once, then starts with them", async () => {
    const detach = promptQueue.attach();
    const refused = Object.assign(new Error("prompt"), {
      cause: {
        kind: "invalid_argument",
        code: "template.prompt",
        message: "The run asks for the value \"Token\" before it starts.",
        hint: "",
        details: { prompts: JSON.stringify([{ label: "Token", secret: true }]) },
        causes: [],
      },
    });
    runner.Start.mockRejectedValueOnce(refused).mockResolvedValueOnce({ opId: "op_1" });
    const run = folderRuns().get("fld_1");
    run.start({ iterations: 1, delayMs: 0 });
    await settle();
    await settle();
    const asked = promptQueue.current();
    expect(asked).toMatchObject({ purpose: "run", fields: [{ label: "Token", secret: true }] });
    promptQueue.settle(asked?.id ?? 0, { Token: "s3cret" });
    await settle();
    await settle();
    expect(runner.Start).toHaveBeenLastCalledWith({ folderId: "fld_1", iterations: 1, delayMs: 0, prompts: { Token: "s3cret" } });
    expect(run.getState().status).toBe("running");
    detach();
  });

  it("goes back to the state before when the prompt is cancelled", async () => {
    const detach = promptQueue.attach();
    const refused = Object.assign(new Error("prompt"), {
      cause: { kind: "invalid_argument", code: "template.prompt", message: "m", hint: "", details: { prompts: '[{"label":"A"}]' }, causes: [] },
    });
    runner.Start.mockRejectedValue(refused);
    const run = folderRuns().get("fld_1");
    const before = run.getState();
    run.start({ iterations: 3, delayMs: 0 });
    await settle();
    await settle();
    promptQueue.settle(promptQueue.current()?.id ?? 0, null);
    await settle();
    await settle();
    expect(run.getState()).toEqual(before);
    detach();
  });

  it("ends with the error Go refuses the run with", async () => {
    runner.Start.mockRejectedValue(
      Object.assign(new Error("empty"), {
        cause: { kind: "invalid_argument", code: "runner.empty", message: "The folder has no requests to run.", hint: "", details: {}, causes: [] },
      }),
    );
    const run = folderRuns().get("fld_1");
    run.start({ iterations: 1, delayMs: 0 });
    await settle();
    await settle();
    expect(run.getState()).toMatchObject({ status: "error", error: { code: "runner.empty" } });
  });

  it("stops: before it starts, while it runs, and when its tab closes", async () => {
    const prepared = deferred<void>();
    const run = folderRuns().get("fld_1");
    run.start({ iterations: 1, delayMs: 0 }, () => prepared.promise);
    run.cancel();
    expect(run.getState().status).toBe("cancelled");
    prepared.resolve();
    await settle();
    expect(runner.Start).not.toHaveBeenCalled();

    runner.Start.mockResolvedValue({ opId: "op_1" });
    run.start({ iterations: 1, delayMs: 0 });
    await settle();
    expect(run.getState().opId).toBe("op_1");
    run.cancel();
    // Stopping at once, before Go answers.
    expect(run.getState().stopping).toBe(true);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    emit("runner:op:done", done("cancelled"));
    expect(run.getState()).toMatchObject({ status: "cancelled", stopping: false, elapsedMs: 50 });

    opsCancel.mockClear();
    run.start({ iterations: 1, delayMs: 0 });
    await settle();
    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["fld_1"], activeId: "fld_1" });
    closeTabs(stores, ["fld_1"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    expect(folderRuns().get("fld_1")).not.toBe(run);
    // Its results are gone from the UI: Go drops their responses.
    expect(runner.Release).toHaveBeenCalledWith("op_1");

    resetFolderRuns();
    expect(listenerCount()).toBe(0);
  });
});

describe("the requests of the runs (RUN-02, RUN-04)", () => {
  const item = (id: string, skip = "") => ({ position: 0, requestId: id, name: id, kind: "http", method: "GET", path: [], skip });

  it("loads the plan, keeps the choice across loads, and runs the chosen requests in order", async () => {
    runner.Plan.mockResolvedValueOnce({ items: [item("a"), item("b"), item("c")], runnable: 3 });
    const run = folderRuns().get("fld_1");
    run.loadPlan();
    await settle();
    expect(run.getState().plan?.map((it) => it.requestId)).toEqual(["a", "b", "c"]);
    expect(run.move("c", -1)).toBe(true);
    expect(run.move("c", -1)).toBe(true);
    expect(run.move("c", -1)).toBe(false);
    run.setIncluded(["b"], false);
    expect(run.getState().selection).toEqual({ order: ["c", "a", "b"], excluded: { b: true } });

    // A request added after "a" in the tree; "b" removed. A slower,
    // older load that lands last is dropped.
    const slow = deferred<unknown>();
    runner.Plan.mockReturnValueOnce(slow.promise).mockResolvedValueOnce({ items: [item("a"), item("new"), item("c")], runnable: 3 });
    run.loadPlan();
    run.loadPlan();
    await settle();
    slow.resolve({ items: [item("a")], runnable: 1 });
    await settle();
    expect(run.getState().selection.order).toEqual(["c", "a", "new"]);

    runner.Start.mockResolvedValue({ opId: "op_1" });
    run.start({ iterations: 1, delayMs: 0, stopOnFailure: true });
    await settle();
    expect(runner.Start).toHaveBeenCalledWith({ folderId: "fld_1", iterations: 1, delayMs: 0, requestIds: ["c", "a", "new"], stopOnFailure: true });
    // A run keeps the plan and the choice.
    expect(run.getState().plan).toHaveLength(3);
    emit("runner:op:data", { opId: "op_1", seq: 1, items: [runResult(1, { outcome: "failed" })] });
    emit("runner:op:progress", { opId: "op_1", seq: 2, done: 1, total: 3, message: "Stopped at a failure" });
    emit("runner:op:done", done("ok"));
    expect(run.getState()).toMatchObject({ status: "ok", stopped: true, done: 1, total: 3 });

    run.resetSelection();
    expect(run.getState().selection).toEqual({ order: [], excluded: {} });
  });

  it("keeps the last plan when a load fails", async () => {
    runner.Plan.mockResolvedValueOnce({ items: [item("a")], runnable: 1 }).mockRejectedValueOnce(
      Object.assign(new Error("gone"), { cause: { kind: "not_found", code: "collection.not_found", message: "Gone.", hint: "", details: {}, causes: [] } }),
    );
    const run = folderRuns().get("fld_1");
    run.loadPlan();
    await settle();
    run.loadPlan();
    await settle();
    expect(run.getState()).toMatchObject({ plan: [{ requestId: "a" }], planError: { code: "collection.not_found" } });
  });
});

describe("openFolderRun", () => {
  it("opens the folder's tab on its Run sub-tab", () => {
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({
      type: "collection/load",
      workspaceId: "ws_1",
      nodes: [{ kind: "folder", id: "fld_1", parentId: null, name: "API", path: "api", fileKind: "", readOnly: "", childIds: [] }],
      rootIds: ["fld_1"],
    });
    openFolderRun(stores, "fld_1");
    expect(stores.tabs.getState().activeId).toBe("fld_1");
    expect(stores.drafts.getState().subTabs["fld_1"]).toBe("run");
  });
});
