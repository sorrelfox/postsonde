import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  Repeat: vi.fn<(input: { requestId: string; count: number; intervalMs: number; allowUnresolved?: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);

import { formatDurationMs, formatTimeoutMs, statusTone } from "../lib/http";
import { createStores } from "../stores";
import { deferred, emit, listenerCount, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { confirmQueue } from "./dialogs";
import { MAX_RUN_RESULTS, addRunResults, confirmUnresolved, httpSends, resetHttpSends } from "./httpSends";
import { closeTabs } from "./tabs";
import { httpResponse as response } from "../test/httpResponse";


const done = (status: string, error: unknown = null) => ({ opId: "op_1", seq: 9, status, error, items: 1, batches: 1, elapsedMs: 50 });

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  http.Send.mockReset();
  http.Repeat.mockReset();
});

describe("HttpSend", () => {
  it("prepares, sends, follows the stage and keeps the response", async () => {
    http.Send.mockResolvedValue({ opId: "op_1" });
    const prepared = deferred<void>();
    const send = httpSends().get("req_1");
    send.send(() => prepared.promise);
    expect(send.getState().status).toBe("starting");
    await settle();
    // Nothing is sent before the edits reached Go.
    expect(http.Send).not.toHaveBeenCalled();
    prepared.resolve();
    await settle();
    expect(http.Send).toHaveBeenCalledWith({ requestId: "req_1" });
    expect(send.getState().status).toBe("running");

    send.send();
    expect(http.Send).toHaveBeenCalledTimes(1);

    emit("http:op:progress", { opId: "op_1", seq: 1, done: 0, total: 0, message: "Waiting for the response" });
    expect(send.getState().stage).toBe("Waiting for the response");
    // The body's bytes as they arrive (HTTP-37).
    emit("http:op:progress", { opId: "op_1", seq: 2, done: 3_000_000, total: 50_000_000, message: "Receiving the response" });
    expect(send.getState()).toMatchObject({ stage: "Receiving the response", received: 3_000_000, total: 50_000_000 });
    emit("http:op:data", { opId: "op_1", seq: 3, items: [response()] });
    emit("http:op:done", done("ok"));
    expect(send.getState()).toMatchObject({ status: "ok", stage: "", error: null, response: response() });
  });

  it("ends with the error of a request Go refuses or a timeout", async () => {
    const refused = Object.assign(new Error("refused"), {
      cause: { kind: "invalid_argument", code: "request.url_missing", message: "The request has no URL.", hint: "", details: {}, causes: [] },
    });
    http.Send.mockRejectedValue(refused);
    const send = httpSends().get("req_1");
    send.send();
    await settle();
    await settle();
    expect(send.getState()).toMatchObject({ status: "error", error: { code: "request.url_missing" } });

    http.Send.mockResolvedValue({ opId: "op_1" });
    send.send();
    expect(send.getState().error).toBeNull();
    await settle();
    const timeout = { kind: "timeout", code: "httpclient.timeout", message: "timed out. The request's timeout is 1 s.", hint: "Increase it.", details: {}, causes: [] };
    emit("http:op:done", done("error", timeout));
    expect(send.getState()).toMatchObject({ status: "error", error: { kind: "timeout", code: "httpclient.timeout" }, response: null });
  });

  it("cancels: before it is sent, while it runs, and when its tab closes", async () => {
    const prepared = deferred<void>();
    const send = httpSends().get("req_1");
    send.send(() => prepared.promise);
    send.cancel();
    expect(send.getState().status).toBe("cancelled");
    prepared.resolve();
    await settle();
    expect(http.Send).not.toHaveBeenCalled();

    http.Send.mockResolvedValue({ opId: "op_1" });
    send.send();
    await settle();
    send.cancel();
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    emit("http:op:done", done("cancelled"));
    expect(send.getState().status).toBe("cancelled");

    opsCancel.mockClear();
    send.send();
    await settle();
    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_1"], activeId: "req_1" });
    closeTabs(stores, ["req_1"]);
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    expect(httpSends().get("req_1")).not.toBe(send);

    resetHttpSends();
    expect(listenerCount()).toBe(0);
  });
});

describe("runs (HTTP-27)", () => {
  const result = (index: number, over: Record<string, unknown> = {}) => ({
    index,
    startedAt: 1_700_000_000_000 + index,
    durationMs: 10 * index,
    statusCode: 200,
    status: "200 OK",
    size: 5,
    testsPassed: 0,
    testsFailed: 0,
    error: null,
    response: null,
    ...over,
  });

  it("starts a run, collects its results and shows the latest response it carried", async () => {
    http.Repeat.mockResolvedValue({ opId: "op_1" });
    const send = httpSends().get("req_1");
    send.repeat({ count: 3, intervalMs: 500 });
    await settle();
    expect(http.Repeat).toHaveBeenCalledWith({ requestId: "req_1", count: 3, intervalMs: 500 });
    expect(send.getState()).toMatchObject({ status: "running", run: { sent: 0, options: { count: 3, intervalMs: 500 } } });
    expect(send.runOptions).toEqual({ count: 3, intervalMs: 500 });

    // A single send does nothing while the run goes on.
    send.send();
    expect(http.Send).not.toHaveBeenCalled();

    emit("http.repeat:op:progress", { opId: "op_1", seq: 1, done: 0, total: 3, message: "Waiting for the response" });
    expect(send.getState()).toMatchObject({ stage: "Waiting for the response", received: 0, total: 0 });
    emit("http.repeat:op:data", {
      opId: "op_1",
      seq: 2,
      items: [result(1, { response: response() }), result(2, { statusCode: 500, status: "500 Internal Server Error" })],
    });
    emit("http.repeat:op:data", {
      opId: "op_1",
      seq: 3,
      items: [
        result(3, {
          statusCode: 0,
          status: "",
          error: { kind: "network", code: "netx.refused", message: "refused", hint: "", details: {}, causes: [] },
        }),
      ],
    });
    emit("http.repeat:op:done", done("ok"));
    const state = send.getState();
    expect(state.status).toBe("ok");
    expect(state.response).toEqual(response());
    expect(state.run).toMatchObject({ sent: 3, responses: 2, failed: 1, problems: 1, totalMs: 30, minMs: 10, maxMs: 20, responseIndex: 1 });
    expect(state.run?.results.map((r) => r.index)).toEqual([1, 2, 3]);

    // A single send afterwards replaces the run.
    http.Send.mockResolvedValue({ opId: "op_2" });
    send.send();
    expect(send.getState().run).toBeNull();
  });

  it("stops a run by cancelling its operation", async () => {
    http.Repeat.mockResolvedValue({ opId: "op_1" });
    const send = httpSends().get("req_1");
    send.repeat({ count: 0, intervalMs: 1000 });
    await settle();
    emit("http.repeat:op:data", { opId: "op_1", seq: 1, items: [result(1)] });
    send.cancel();
    await settle();
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    emit("http.repeat:op:done", done("cancelled"));
    expect(send.getState()).toMatchObject({ status: "cancelled", run: { sent: 1 } });
  });

  it("keeps the latest results and counts all of them", () => {
    const empty = { options: { count: 0, intervalMs: 100 }, results: [], sent: 0, responses: 0, failed: 0, problems: 0, totalMs: 0, minMs: 0, maxMs: 0, responseIndex: 0 };
    let run = addRunResults(
      empty,
      Array.from({ length: MAX_RUN_RESULTS }, (_, i) => result(i + 1)),
    );
    run = addRunResults(run, [result(MAX_RUN_RESULTS + 1)]);
    expect(run.sent).toBe(MAX_RUN_RESULTS + 1);
    expect(run.results).toHaveLength(MAX_RUN_RESULTS);
    expect(run.results[0]?.index).toBe(2);
  });
});

describe("undefined variables (ENV-11)", () => {
  const unresolved = () =>
    Object.assign(new Error("refused"), {
      cause: {
        kind: "invalid_argument",
        code: "template.unresolved",
        message: "2 variables are not defined: {{base}}, {{token}}.",
        hint: "",
        details: { names: "base,token", problems: "2" },
        causes: [],
      },
    });
  let detach: () => void = () => {};
  beforeEach(() => {
    detach = confirmQueue.attach();
    return () => detach();
  });

  it("asks, and sends anyway with the references as written", async () => {
    http.Send.mockRejectedValueOnce(unresolved()).mockResolvedValueOnce({ opId: "op_1" });
    const send = httpSends().get("req_1");
    send.send();
    await settle();
    await settle();
    const asked = confirmQueue.current();
    expect(asked).toMatchObject({ title: "Send with 2 undefined variables?", confirmLabel: "Send anyway", cancelLabel: "Cancel" });
    expect(String(asked?.message)).toContain("{{base}}, {{token}}");
    expect(send.getState().status).toBe("starting");

    confirmQueue.settle(asked!.id, true);
    await settle();
    await settle();
    expect(http.Send).toHaveBeenLastCalledWith({ requestId: "req_1", allowUnresolved: true });
    expect(send.getState().status).toBe("running");
  });

  it("goes back to the previous state on Cancel, and asks again next time", async () => {
    http.Send.mockResolvedValueOnce({ opId: "op_1" });
    const send = httpSends().get("req_1");
    send.send();
    await settle();
    emit("http:op:data", { opId: "op_1", seq: 1, items: [response()] });
    emit("http:op:done", { ...done("ok"), seq: 2 });
    const previous = send.getState();

    for (let i = 0; i < 2; i++) {
      http.Send.mockRejectedValueOnce(unresolved());
      send.send();
      await settle();
      await settle();
      const asked = confirmQueue.current();
      expect(asked).not.toBeNull();
      confirmQueue.settle(asked!.id, false);
      await settle();
      await settle();
      expect(send.getState()).toEqual(previous);
    }
    expect(http.Send).toHaveBeenCalledTimes(3);
  });

  it("words one name in the singular", async () => {
    const answer = confirmUnresolved(["host"]);
    expect(confirmQueue.current()).toMatchObject({ title: "Send with an undefined variable?" });
    expect(String(confirmQueue.current()?.message)).toMatch(/^\{\{host\}\} is not defined/);
    confirmQueue.settle(confirmQueue.current()!.id, false);
    await expect(answer).resolves.toBe(false);
  });
});

describe("lib/http", () => {
  it("formats timeouts, durations and status tones", () => {
    expect([250, 1000, 1500, 30000, 90000, 120000, 5400000].map(formatTimeoutMs)).toEqual([
      "250 ms",
      "1 s",
      "1.5 s",
      "30 s",
      "1 min 30 s",
      "2 min",
      "1 h 30 min",
    ]);
    expect([42, 1234, 125000].map(formatDurationMs)).toEqual(["42 ms", "1.23 s", "2 min 5 s"]);
    expect([100, 204, 301, 404, 503].map(statusTone)).toEqual(["other", "ok", "redirect", "client", "server"]);
  });
});
