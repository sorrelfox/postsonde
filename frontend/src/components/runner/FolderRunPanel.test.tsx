import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const runner = vi.hoisted(() => ({
  Start: vi.fn<(input: unknown) => Promise<unknown>>(),
  Plan: vi.fn<(id: string) => Promise<unknown>>(),
  Response: vi.fn<(opId: string, index: number) => Promise<unknown>>(),
  Release: vi.fn<(opId: string) => Promise<void>>(() => Promise.resolve()),
  PickDataFile: vi.fn<(delimiter: string) => Promise<unknown>>(),
  PreviewDataFile: vi.fn<(path: string, delimiter: string) => Promise<unknown>>(),
  Runs: vi.fn<() => Promise<unknown>>(),
  Run: vi.fn<(id: string) => Promise<unknown>>(),
  DeleteRun: vi.fn<(id: string) => Promise<void>>(() => Promise.resolve()),
  ExportRun: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runner", () => ({
  RunnerService: runner,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { resetFolderRuns } from "../../commands/folderRuns";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale } from "../../i18n";
import type { RunItemDTO, RunResultDTO } from "../../lib/runner";
import { createStores, StoreProvider } from "../../stores";
import { accessibleName, expectAccessible } from "../../test/a11y";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { httpResponse } from "../../test/httpResponse";
import { runResult } from "../../test/runResult";
import { FolderRunPanel, parseDelay, parseIterations } from "./FolderRunPanel";

const failedTest = {
  index: 0,
  target: "status",
  expr: "",
  op: "equals",
  expected: "201",
  outcome: "failed",
  actual: "200",
  exists: true,
  message: "Expected status code to equal 201, but it was 200.",
  code: "",
  schemaErrors: [],
};

/** Two iterations of a folder with one of every outcome. */
function results(): RunResultDTO[] {
  const one = (iteration: number, base: number): RunResultDTO[] => [
    runResult(base + 1, { iteration, position: 1, name: "Create", method: "POST", testsPassed: 2 }),
    runResult(base + 2, {
      iteration,
      position: 2,
      name: "Get user",
      path: ["Users"],
      outcome: "failed",
      testsFailed: 1,
      tests: [failedTest],
    }),
    runResult(base + 3, {
      iteration,
      position: 3,
      name: "Undefined",
      outcome: "error",
      statusCode: 0,
      status: "",
      error: { kind: "invalid_argument", code: "template.unresolved", message: "The variable {{nope}} is not defined.", hint: "", details: { names: "nope" }, causes: [] },
    }),
    runResult(base + 4, { iteration, position: 4, name: "Socket", kind: "websocket", method: "", outcome: "skipped", skip: "kind", statusCode: 0, status: "" }),
    runResult(base + 5, { iteration, position: 5, name: "Events", kind: "sse", events: 2 }),
  ];
  return [...one(1, 0), ...one(2, 5)];
}

const done = (status: string) => ({ opId: "op_1", seq: 9, status, error: null, items: 10, batches: 1, elapsedMs: 50 });

const planItem = (n: number, over: Partial<RunItemDTO> = {}): RunItemDTO => ({
  position: n,
  requestId: `req_${n}`,
  name: `Request ${n}`,
  kind: "http",
  method: "GET",
  path: [],
  skip: "",
  ...over,
});

/** The folder's plan: three requests that are sent and a WebSocket one that is not. */
const PLAN = {
  items: [
    planItem(1, { name: "Create", method: "POST" }),
    planItem(2, { name: "Get user", path: ["Users"] }),
    planItem(3, { name: "Socket", kind: "websocket", method: "", skip: "kind" }),
    planItem(4, { name: "List" }),
  ],
  runnable: 3,
};

async function setup() {
  const stores = createStores();
  render(
    <StoreProvider stores={stores}>
      <FolderRunPanel folderId="fld_api" />
    </StoreProvider>,
  );
  // The plan loads.
  await act(settle);
  return stores;
}

/** The checkboxes of the run's requests, in the order shown. */
const requestBoxes = () => within(screen.getByRole("list", { name: "Requests" })).getAllByRole("checkbox") as HTMLInputElement[];

/** The names of the run's requests, in the order shown. */
const requestNames = () => requestBoxes().map((box) => accessibleName(box));

/** The Iterations or the Delay field, found by position so the pseudo-locale test finds them too. */
const input = (name: "Iterations" | "Delay") => document.querySelectorAll("input")[name === "Iterations" ? 0 : 1] as HTMLInputElement;

/** Starts a run of 2 iterations with a 0.5 s delay and lists every result. */
async function runToEnd(status = "ok") {
  runner.Start.mockResolvedValue({ opId: "op_1" });
  fireEvent.change(input("Iterations"), { target: { value: "2" } });
  fireEvent.change(input("Delay"), { target: { value: "0.5" } });
  fireEvent.submit(document.querySelector("form") as HTMLFormElement);
  await act(settle);
  await act(settle);
  act(() => {
    emit("runner:op:progress", { opId: "op_1", seq: 1, done: 0, total: 10, message: "Sending" });
    emit("runner:op:data", { opId: "op_1", seq: 2, items: results() });
    emit("runner:op:done", done(status));
  });
}

beforeEach(() => {
  resetFakeWails();
  resetFolderRuns();
  runner.Start.mockReset();
  runner.Plan.mockReset();
  runner.Plan.mockResolvedValue(PLAN);
  runner.Response.mockReset();
  runner.Release.mockClear();
  runner.PickDataFile.mockReset();
  runner.PreviewDataFile.mockReset();
  runner.Runs.mockReset();
  runner.Runs.mockResolvedValue([]);
  runner.Run.mockReset();
  runner.DeleteRun.mockClear();
  runner.ExportRun.mockReset();
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("parsing the options", () => {
  it("takes whole iterations and seconds of delay in range", () => {
    expect(parseIterations("3")).toBe(3);
    expect(parseIterations(" 10000 ")).toBe(10_000);
    for (const bad of ["0", "10001", "1.5", "", "x"]) expect(parseIterations(bad)).toBeNull();
    expect(parseDelay("0")).toBe(0);
    expect(parseDelay("1.25")).toBe(1250);
    expect(parseDelay("3600")).toBe(3_600_000);
    for (const bad of ["3601", "-1", "", ".", "1s"]) expect(parseDelay(bad)).toBeNull();
  });
});

describe("FolderRunPanel", () => {
  it("offers the options and refuses values out of range", async () => {
    await setup();
    expect(input("Iterations").value).toBe("1");
    expect(input("Delay").value).toBe("0");
    fireEvent.change(input("Iterations"), { target: { value: "0" } });
    expect(screen.getByText("Enter a whole number from 1 to 10000.")).toBeTruthy();
    expect(input("Iterations").getAttribute("aria-invalid")).toBe("true");
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(input("Iterations"), { target: { value: "3" } });
    fireEvent.change(input("Delay"), { target: { value: "abc" } });
    expect(screen.getByText("Enter a number of seconds from 0 to 3600.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(true);
    expectAccessible(document.body);
  });

  it("runs the folder and lists each request of each iteration with its outcome", async () => {
    await setup();
    await runToEnd();
    expect(runner.Start).toHaveBeenCalledWith({ folderId: "fld_api", iterations: 2, delayMs: 500, requestIds: ["req_1", "req_2", "req_4"] });

    expect(screen.getByText("Run finished: 10 results")).toBeTruthy();
    expect(screen.getByText("4 passed · 2 failed · 2 errors · 2 skipped")).toBeTruthy();
    const list = screen.getByRole("list", { name: "Run results" });
    // A heading per iteration, for the eye; each row names its iteration for a screen reader.
    const headings = [...list.querySelectorAll('li[aria-hidden="true"]')].map((li) => li.textContent);
    expect(headings).toEqual(["Iteration 1", "Iteration 2"]);

    const rows = within(list)
      .getAllByRole("listitem")
      .filter((li) => li.dataset.outcome !== undefined);
    expect(rows).toHaveLength(10);
    const [create, get, undef, socket, events] = rows;
    expect(create?.textContent).toContain("Passed");
    expect(create?.textContent).toContain("POST");
    expect(create?.textContent).toContain("2 tests passed");
    expect(get?.textContent).toContain("Failed");
    expect(get?.textContent).toContain("Users");
    expect(get?.textContent).toContain("1 of 1 tests failed");
    expect(get?.textContent).toContain("Expected status code to equal 201, but it was 200.");
    expect(undef?.textContent).toContain("Error");
    expect(undef?.textContent).toContain("The variable {{nope}} is not defined.");
    expect(socket?.textContent).toContain("Skipped");
    expect(socket?.textContent).toContain("Not run: a run sends HTTP, GraphQL and SSE requests only.");
    expect(events?.textContent).toContain("SSE");
    expect(events?.textContent).toContain("2 events");

    // The options stay for the next run.
    expect(input("Iterations").value).toBe("2");
    expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain("Run finished: 10 results. 4 passed · 2 failed · 2 errors · 2 skipped");
    expectAccessible(document.body);
  });

  it("stops a running run", async () => {
    runner.Start.mockResolvedValue({ opId: "op_1" });
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    act(() => {
      emit("runner:op:progress", { opId: "op_1", seq: 1, done: 1, total: 3, message: "Waiting before the next request" });
    });
    expect(screen.getByText("Waiting before the next request: 1 of 3 done")).toBeTruthy();
    expect(input("Iterations").disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => {
      emit("runner:op:data", { opId: "op_1", seq: 2, items: [runResult(1)] });
      emit("runner:op:done", done("cancelled"));
    });
    expect(screen.getByText("Run stopped after 1 of 3")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Run folder" })).toBeTruthy();
  });

  it("shows why a run could not start", async () => {
    runner.Start.mockRejectedValue(
      Object.assign(new Error("empty"), {
        cause: { kind: "invalid_argument", code: "runner.empty", message: "The folder has no requests to run.", hint: "", details: {}, causes: [] },
      }),
    );
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(screen.getByText("The run could not start")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("The folder has no requests to run.");
  });

  it("runs the chosen requests in the order chosen, reordered with the keyboard", async () => {
    await setup();
    expect(requestNames()).toEqual(["POST Create", "GET Get user Users", "WS Socket", "GET List"]);
    const [create, get, socket] = requestBoxes();
    // A request a run does not send cannot be chosen, and says why.
    expect(socket?.disabled).toBe(true);
    expect(socket?.checked).toBe(false);
    expect(accessibleName(socket as HTMLElement)).toBe("WS Socket");
    expect(screen.getByText("Not run: a run sends HTTP, GraphQL and SSE requests only.")).toBeTruthy();
    expect(screen.getByText("3 of 3 chosen")).toBeTruthy();

    // Alt+↓ moves the focused request down; focus stays on it and the move is announced.
    create?.focus();
    fireEvent.keyDown(create as HTMLInputElement, { key: "ArrowDown", altKey: true });
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "ArrowDown", altKey: true });
    expect(requestNames()).toEqual(["GET Get user Users", "WS Socket", "POST Create", "GET List"]);
    expect(document.activeElement).toBe(requestBoxes()[2]);
    expect(screen.getByText("Moved Create to position 3 of 4.")).toBeTruthy();
    // At the top, Alt+↑ does nothing (and is taken).
    const top = requestBoxes()[0] as HTMLInputElement;
    top.focus();
    expect(fireEvent.keyDown(top, { key: "ArrowUp", altKey: true })).toBe(false);
    expect(requestNames()[0]).toBe("GET Get user Users");
    // Plain arrows are left to the browser.
    expect(fireEvent.keyDown(top, { key: "ArrowDown" })).toBe(true);

    // Leave "Get user" out, and move "List" up with the mouse.
    fireEvent.click(get as HTMLInputElement);
    expect(screen.getByText("2 of 3 chosen")).toBeTruthy();
    fireEvent.click(screen.getAllByTitle("Move up")[3] as HTMLElement);
    expect(requestNames()).toEqual(["GET Get user Users", "WS Socket", "GET List", "POST Create"]);
    expectAccessible(document.body);

    // The run sends the chosen requests in that order, and stops at a failure.
    fireEvent.click(screen.getByRole("checkbox", { name: "Stop at the first failure" }));
    runner.Start.mockResolvedValue({ opId: "op_1" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(runner.Start).toHaveBeenCalledWith({ folderId: "fld_api", iterations: 1, delayMs: 0, requestIds: ["req_4", "req_1"], stopOnFailure: true });
    // The choice cannot change while the run runs.
    expect(requestBoxes().every((box) => box.disabled)).toBe(true);
    act(() => {
      emit("runner:op:data", { opId: "op_1", seq: 1, items: [runResult(1, { requestId: "req_4", name: "List", outcome: "failed", testsFailed: 1 })] });
      emit("runner:op:progress", { opId: "op_1", seq: 2, done: 1, total: 2, message: "Stopped at a failure" });
      emit("runner:op:done", { ...done("ok"), items: 1 });
    });
    expect(screen.getByText("Run stopped at a failure after 1 of 2")).toBeTruthy();

    // Reset order puts every request back, in the folder's order.
    fireEvent.click(screen.getByRole("button", { name: "Reset order" }));
    expect(requestNames()).toEqual(["POST Create", "GET Get user Users", "WS Socket", "GET List"]);
    expect(screen.getByText("3 of 3 chosen")).toBeTruthy();
    expect(screen.getByText("Every request is in the run again, in the folder's order.")).toBeTruthy();

    // Nothing chosen: nothing to run.
    fireEvent.click(screen.getByRole("button", { name: "Select none" }));
    expect(screen.getByText("0 of 3 chosen")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Choose at least one request to run.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Select all" }));
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("says when the folder's requests could not be loaded, and still runs the folder", async () => {
    runner.Plan.mockRejectedValue(
      Object.assign(new Error("gone"), {
        cause: { kind: "not_found", code: "collection.not_found", message: "The folder was not found.", hint: "", details: {}, causes: [] },
      }),
    );
    await setup();
    expect(screen.getByRole("alert").textContent).toContain("The folder's requests could not be loaded: The folder was not found.");
    runner.Start.mockResolvedValue({ opId: "op_1" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(runner.Start).toHaveBeenCalledWith({ folderId: "fld_api", iterations: 1, delayMs: 0 });
  });

  it("shows where each request is while the run goes, and stops at once", async () => {
    await setup();
    runner.Start.mockResolvedValue({ opId: "op_1" });
    fireEvent.change(input("Iterations"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    const rowOf = (name: string) => requestBoxes().find((b) => accessibleName(b).includes(name))?.closest("li") as HTMLElement;

    act(() => {
      emit("runner:op:progress", { opId: "op_1", seq: 1, done: 0, total: 6, message: "Sending" });
    });
    expect(rowOf("Create").textContent).toContain("Sending…");
    expect(rowOf("Create").getAttribute("aria-current")).toBe("step");
    expect(rowOf("Get user").textContent).toContain("Queued");
    expect(rowOf("Socket").textContent).not.toContain("Queued");
    const bar = screen.getByRole("progressbar", { name: "Run progress" });
    expect(bar.getAttribute("aria-valuenow")).toBe("0");
    expect(bar.getAttribute("aria-valuemax")).toBe("6");
    expect(screen.getByText("Iteration 1 of 2")).toBeTruthy();

    act(() => {
      emit("runner:op:data", { opId: "op_1", seq: 2, items: [runResult(1, { requestId: "req_1", name: "Create", durationMs: 12 })] });
      emit("runner:op:progress", { opId: "op_1", seq: 3, done: 1, total: 6, message: "Waiting before the next request" });
    });
    expect(rowOf("Create").textContent).toContain("Passed · 12 ms");
    expect(rowOf("Create").getAttribute("aria-current")).toBeNull();
    expect(rowOf("Get user").textContent).toContain("Waiting…");
    expect(bar.getAttribute("aria-valuetext")).toBe("1 of 6 done");

    // The second iteration starts over: the first one's results are not shown as this one's.
    act(() => {
      emit("runner:op:data", {
        opId: "op_1",
        seq: 4,
        items: [runResult(2, { requestId: "req_2", name: "Get user", outcome: "failed" }), runResult(3, { requestId: "req_4", name: "List" })],
      });
      emit("runner:op:progress", { opId: "op_1", seq: 5, done: 3, total: 6, message: "Sending" });
    });
    expect(screen.getByText("Iteration 2 of 2")).toBeTruthy();
    expect(rowOf("Create").textContent).toContain("Sending…");
    expect(rowOf("Get user").textContent).toContain("Queued");

    // Stop: the button says so at once, and Go is told.
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    const stopping = screen.getByRole("button", { name: "Stopping…" }) as HTMLButtonElement;
    expect(stopping.disabled).toBe(true);
    expect(screen.getByText("Stopping the run…")).toBeTruthy();
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => {
      emit("runner:op:done", { ...done("cancelled"), items: 3 });
    });
    expect(screen.getByText("Run stopped after 3 of 6")).toBeTruthy();
    expect(screen.queryByRole("progressbar")).toBeNull();
    // Each request shows its latest result.
    expect(rowOf("Create").textContent).toContain("Passed");
    expect(rowOf("Get user").textContent).toContain("Failed");
    expect(screen.getByRole("button", { name: "Run folder" })).toBeTruthy();
    expectAccessible(document.body);
  });

  it("sums the run up and opens a result's request and response", async () => {
    const stores = await setup();
    const failing = results().map((r) => (r.outcome === "failed" ? { ...r, hasResponse: true } : r));
    runner.Start.mockResolvedValue({ opId: "op_1" });
    runner.Response.mockResolvedValue(
      httpResponse({
        statusCode: 200,
        status: "200 OK",
        tests: [failedTest],
        timeline: {
          hops: [
            {
              kind: "final",
              request: { method: "GET", url: "https://api.test/users/1", proto: "HTTP/1.1", target: "/users/1", headers: [], body: "", bodySize: 0, bodyTruncated: false },
            } as never,
          ],
          events: [],
          masked: false,
        },
      }),
    );
    fireEvent.change(input("Iterations"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    act(() => {
      emit("runner:op:data", { opId: "op_1", seq: 1, items: failing });
      emit("runner:op:done", { ...done("ok"), elapsedMs: 1500 });
    });

    const summary = screen.getByRole("region", { name: "Summary" });
    const fact = (key: string) => summary.querySelector(`[data-fact="${key}"] dd`)?.textContent;
    expect(fact("results")).toBe("10");
    expect(fact("failed")).toBe("2");
    expect(fact("error")).toBe("2");
    expect(fact("run")).toBe("1.50 s");
    expect(fact("average")).toBe("12 ms");
    expect(within(summary).getByText("4 failures")).toBeTruthy();
    const failures = within(summary).getAllByRole("listitem");
    expect(failures.map((li) => li.textContent)).toEqual([
      "FailedGet user (iteration 1)Expected status code to equal 201, but it was 200.",
      "ErrorUndefined (iteration 1)The variable {{nope}} is not defined.",
      "FailedGet user (iteration 2)Expected status code to equal 201, but it was 200.",
      "ErrorUndefined (iteration 2)The variable {{nope}} is not defined.",
    ]);
    expectAccessible(document.body);

    // A failure opens its request and response.
    fireEvent.click(within(summary).getByRole("button", { name: "Get user (iteration 2)" }));
    await act(settle);
    expect(runner.Response).toHaveBeenCalledWith("op_1", 7);
    const dialog = screen.getByRole("dialog", { name: "Result of Get user, iteration 2" });
    expect(dialog.textContent).toContain("GET https://api.test/users/1");
    expect(within(dialog).getByRole("tab", { name: /Tests/ })).toBeTruthy();
    expect(dialog.querySelector("[data-response-status]")?.textContent).toContain("200 OK");
    expectAccessible(dialog);

    // Open request: the request's tab.
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({
      type: "collection/load",
      workspaceId: "ws_1",
      nodes: [{ kind: "request", id: "req_7", parentId: null, name: "Get user", path: "get.json", fileKind: "http", readOnly: "", method: "GET" } as never],
      rootIds: ["req_7"],
    });
    await act(settle);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Open request" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(stores.tabs.getState().activeId).toBe("req_7");
  });

  it("opens a result from the results with the keyboard, and says when its response is gone", async () => {
    await setup();
    await runToEnd();
    const list = screen.getByRole("list", { name: "Run results" });
    const buttons = within(list).getAllByRole("button");
    // One Tab stop; arrows move between the results.
    expect(buttons.filter((b) => b.tabIndex === 0)).toHaveLength(1);
    buttons[0]?.focus();
    fireEvent.keyDown(buttons[0] as HTMLElement, { key: "ArrowDown" });
    expect(document.activeElement).toBe(buttons[1]);
    fireEvent.keyDown(buttons[1] as HTMLElement, { key: "End" });
    expect(document.activeElement).toBe(buttons[buttons.length - 1]);
    expect(buttons[buttons.length - 1]?.tabIndex).toBe(0);
    expect(buttons[0]?.tabIndex).toBe(-1);
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Home" });
    expect(document.activeElement).toBe(buttons[0]);

    // An error result has no response: the dialog says why, and asks Go nothing.
    fireEvent.keyDown(buttons[0] as HTMLElement, { key: "ArrowDown" });
    fireEvent.keyDown(buttons[1] as HTMLElement, { key: "ArrowDown" });
    fireEvent.click(document.activeElement as HTMLElement);
    const dialog = screen.getByRole("dialog", { name: "Result of Undefined, iteration 1" });
    expect(dialog.textContent).toContain("The variable {{nope}} is not defined.");
    expect(runner.Response).not.toHaveBeenCalled();
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    // Focus goes back to the result.
    expect(document.activeElement).toBe(buttons[2]);
  });

  it("says when a result's response is no longer kept, and releases the run's responses on the next run", async () => {
    await setup();
    runner.Start.mockResolvedValue({ opId: "op_1" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    act(() => {
      emit("runner:op:data", { opId: "op_1", seq: 1, items: [runResult(1, { requestId: "req_1", name: "Create", hasResponse: true })] });
      emit("runner:op:done", { ...done("ok"), items: 1 });
    });
    runner.Response.mockRejectedValue(
      Object.assign(new Error("gone"), {
        cause: { kind: "not_found", code: "runner.response_gone", message: "The response of this result is no longer kept.", hint: "", details: {}, causes: [] },
      }),
    );
    fireEvent.click(within(screen.getByRole("list", { name: "Run results" })).getByRole("button", { name: "Create" }));
    expect(screen.getByText("Loading the response…")).toBeTruthy();
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Result of Create" });
    expect(within(dialog).getByRole("alert").textContent).toContain("The response of this result is no longer kept.");
    fireEvent.keyDown(dialog, { key: "Escape" });

    // The next run lets Go drop this one's responses once it has started.
    expect(runner.Release).not.toHaveBeenCalled();
    runner.Start.mockResolvedValue({ opId: "op_2" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(runner.Release).toHaveBeenCalledWith("op_1");
  });

  it("shows every text from the catalogue (pseudo-locale)", async () => {
    setLocale(PSEUDO_LOCALE);
    await setup();
    await runToEnd();
    const roots = [document.body];
    expect(englishLeftovers(roots, ["runner"])).toEqual([]);
    expect(visibleTexts(roots).filter(isPseudo).length).toBeGreaterThan(10);
    // A result's dialog, and the list while a run goes.
    const results = document.querySelectorAll<HTMLButtonElement>("button[data-result]");
    fireEvent.click(results[3] as HTMLButtonElement);
    expect(englishLeftovers([screen.getByRole("dialog")], ["runner"])).toEqual([]);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    runner.Start.mockResolvedValue({ opId: "op_2" });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await act(settle);
    await act(settle);
    act(() => {
      emit("runner:op:progress", { opId: "op_2", seq: 1, done: 1, total: 6, message: "Sending" });
    });
    expect(englishLeftovers(roots, ["runner"])).toEqual([]);
  });
});

/** A data file of 7 rows as Go previews it. */
const USERS = {
  path: "/data/users.csv",
  name: "users.csv",
  format: "csv",
  delimiter: ",",
  columns: ["id", "name"],
  rows: [
    ["1", "Ada"],
    ["2", "Grace"],
    ["3", ""],
  ],
  rowCount: 7,
};

const dataInvalid = (path: string) =>
  Object.assign(new Error("invalid"), {
    cause: {
      kind: "invalid_argument",
      code: "runner.data_invalid",
      message: 'The data file "semi.csv" has a row with a different number of fields than its header row (line 3, column 1).',
      hint: "",
      details: { name: "semi.csv", path, line: "3", column: "1" },
      causes: [],
    },
  });

/** The data file section. */
const dataSection = () => screen.getByRole("region", { name: "Data file" });

describe("data-driven runs", () => {
  it("runs once per row of the chosen data file, with its preview", async () => {
    runner.PickDataFile.mockResolvedValue(USERS);
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Choose data file…" }));
    await act(settle);
    expect(runner.PickDataFile).toHaveBeenCalledWith("");

    const section = dataSection();
    expect(section.textContent).toContain("users.csv · CSV · 7 rows · 2 columns");
    const table = within(section).getByRole("table");
    expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["id", "name"]);
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(table.querySelector("caption")?.textContent).toBe("First 3 of 7 rows");
    // The rows are the iterations.
    expect(input("Iterations").value).toBe("7");
    expect(input("Iterations").disabled).toBe(true);
    expect(screen.getByText("One iteration per row of the data file.")).toBeTruthy();
    expectAccessible(document.body);

    runner.Start.mockResolvedValue({ opId: "op_1" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(runner.Start).toHaveBeenCalledWith({
      folderId: "fld_api",
      iterations: 7,
      delayMs: 0,
      requestIds: ["req_1", "req_2", "req_4"],
      dataFile: "/data/users.csv",
    });
    // Nothing changes while the run runs.
    expect((within(section).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    expect((within(section).getByRole("combobox", { name: "Delimiter" }) as HTMLSelectElement).disabled).toBe(true);
    act(() => {
      emit("runner:op:progress", { opId: "op_1", seq: 1, done: 3, total: 21, message: "Sending" });
    });
    expect(screen.getByText("Iteration 2 of 7")).toBeTruthy();
    act(() => emit("runner:op:done", done("ok")));

    // Another delimiter reads the file again; the run sends it.
    runner.PreviewDataFile.mockResolvedValue({ ...USERS, delimiter: ";", columns: ["id,name"], rows: [["1,Ada"]], rowCount: 7 });
    fireEvent.change(within(section).getByRole("combobox", { name: "Delimiter" }), { target: { value: "semicolon" } });
    await act(settle);
    expect(runner.PreviewDataFile).toHaveBeenCalledWith("/data/users.csv", ";");
    expect(within(section).getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["id,name"]);
    runner.Start.mockResolvedValue({ opId: "op_2" });
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    expect(runner.Start).toHaveBeenLastCalledWith(expect.objectContaining({ dataFile: "/data/users.csv", dataDelimiter: ";" }));
    act(() => emit("runner:op:done", { ...done("ok"), opId: "op_2" }));

    // Removed: the Iterations field counts again.
    fireEvent.click(within(section).getByRole("button", { name: "Remove" }));
    expect(input("Iterations").disabled).toBe(false);
    expect(input("Iterations").value).toBe("1");
    expect(within(dataSection()).queryByRole("table")).toBeNull();
  });

  it("keeps the data file for the session, and a cancelled dialog changes nothing", async () => {
    runner.PickDataFile.mockResolvedValue(USERS);
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Choose data file…" }));
    await act(settle);
    runner.PickDataFile.mockResolvedValue({ path: "", name: "", format: "", delimiter: "", columns: null, rows: null, rowCount: 0 });
    fireEvent.click(screen.getByRole("button", { name: "Choose another…" }));
    await act(settle);
    expect(dataSection().textContent).toContain("users.csv · CSV · 7 rows · 2 columns");
  });

  it("says why a data file cannot be read, and reads it again with another delimiter", async () => {
    runner.PickDataFile.mockRejectedValue(dataInvalid("/data/semi.csv"));
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Choose data file…" }));
    await act(settle);
    const section = dataSection();
    expect(within(section).getByRole("alert").textContent).toContain("has a row with a different number of fields");
    expect(section.textContent).toContain("semi.csv");
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Choose a data file that can be read, or remove it.")).toBeTruthy();

    runner.PreviewDataFile.mockResolvedValue({ ...USERS, path: "/data/semi.csv", name: "semi.csv", delimiter: ";" });
    fireEvent.change(within(section).getByRole("combobox", { name: "Delimiter" }), { target: { value: "semicolon" } });
    await act(settle);
    expect(runner.PreviewDataFile).toHaveBeenCalledWith("/data/semi.csv", ";");
    expect(within(section).queryByRole("alert")).toBeNull();
    expect((screen.getByRole("button", { name: "Run folder" }) as HTMLButtonElement).disabled).toBe(false);
    expectAccessible(document.body);
  });

  it("shows the data file's texts from the catalogue (pseudo-locale) (data)", async () => {
    setLocale(PSEUDO_LOCALE);
    runner.PickDataFile.mockRejectedValue(dataInvalid("/data/semi.csv"));
    await setup();
    fireEvent.click(document.querySelector<HTMLButtonElement>("section button") as HTMLButtonElement);
    await act(settle);
    expect(englishLeftovers([document.body], ["runner"])).toEqual([]);
  });

  it("shows the data file's texts from the catalogue (pseudo-locale)", async () => {
    setLocale(PSEUDO_LOCALE);
    runner.PickDataFile.mockResolvedValue(USERS);
    await setup();
    fireEvent.click(document.querySelector<HTMLButtonElement>("section button") as HTMLButtonElement);
    await act(settle);
    expect(document.querySelector("table")).not.toBeNull();
    expect(englishLeftovers([document.body], ["runner"])).toEqual([]);
  });
});

/** A past run of the folder as Go lists it: the two iterations of results(). */
const PAST = {
  id: "op_past",
  folderId: "fld_api",
  folder: "API",
  project: "Shop",
  startedAt: 1_790_000_000_000,
  endedAt: 1_790_000_002_500,
  elapsedMs: 2500,
  status: "stopped",
  iterations: 2,
  requests: 5,
  total: 12,
  delayMs: 0,
  stopOnFailure: true,
  dataFile: "users.csv",
  dataRows: 2,
  results: 10,
  passed: 4,
  failed: 2,
  errors: 2,
  skipped: 2,
  totalMs: 120,
  omitted: 0,
};

/** The Past runs section. */
const historySection = () => screen.getByRole("region", { name: "Past runs" });

describe("reports and run history", () => {
  it("saves the run that ended as a report, and says where or why not", async () => {
    await setup();
    await runToEnd();
    const group = screen.getByRole("group", { name: "Save report" });
    runner.ExportRun.mockResolvedValue({ saved: true, path: "/reports/API-run.xml", size: 900 });
    fireEvent.click(within(group).getByRole("button", { name: "JUnit XML" }));
    await act(settle);
    expect(runner.ExportRun).toHaveBeenCalledWith({ runId: "op_1", format: "junit" });
    expect(screen.getByText("Report saved to /reports/API-run.xml.")).toBeTruthy();

    // Cancelled: nothing said.
    runner.ExportRun.mockResolvedValue({ saved: false, path: "", size: 0 });
    fireEvent.click(within(group).getByRole("button", { name: "HTML" }));
    await act(settle);
    expect(runner.ExportRun).toHaveBeenLastCalledWith({ runId: "op_1", format: "html" });
    expect(screen.queryByText(/Report saved/)).toBeNull();

    runner.ExportRun.mockRejectedValue(
      Object.assign(new Error("save"), {
        cause: { kind: "io", code: "runner.save_failed", message: "The report could not be saved to /ro/x.json.", hint: "", details: {}, causes: [] },
      }),
    );
    fireEvent.click(within(group).getByRole("button", { name: "JSON" }));
    await act(settle);
    expect(screen.getByRole("alert").textContent).toContain("The report could not be saved to /ro/x.json.");
    expectAccessible(document.body);
  });

  it("lists the folder's past runs, reloaded when a run ends", async () => {
    runner.Runs.mockResolvedValue([PAST, { ...PAST, id: "op_other", folderId: "fld_other" }]);
    await setup();
    const section = historySection();
    const rows = within(section).getAllByRole("listitem");
    expect(rows).toHaveLength(1);
    expect(section.textContent).toContain("1 run");
    expect(rows[0]?.textContent).toContain("Stopped at a failure");
    expect(rows[0]?.textContent).toContain("4 passed · 2 failed · 2 errors · 2 skipped");
    expect(rows[0]?.textContent).toContain("Data file users.csv, 2 rows");
    expect(within(section).getByRole("button", { name: /^Open the run of / })).toBeTruthy();
    const calls = runner.Runs.mock.calls.length;
    await runToEnd();
    await act(settle);
    expect(runner.Runs.mock.calls.length).toBeGreaterThan(calls);
    expectAccessible(document.body);
  });

  it("says when the folder has no past runs, or they cannot be read", async () => {
    await setup();
    expect(historySection().textContent).toContain("No past runs of this folder yet.");
    cleanup();
    resetFolderRuns();
    runner.Runs.mockRejectedValue(
      Object.assign(new Error("unavailable"), {
        cause: { kind: "unsupported", code: "runner.history_unavailable", message: "The run history is not available.", hint: "", details: {}, causes: [] },
      }),
    );
    await setup();
    expect(within(historySection()).getByRole("alert").textContent).toContain("The run history is not available.");
    expect(historySection().textContent).not.toContain("Loading the past runs…");
  });

  it("reopens a past run in place of the latest one, without responses, and goes back", async () => {
    runner.Runs.mockResolvedValue([PAST]);
    runner.Run.mockResolvedValue({ summary: PAST, results: results() });
    await setup();
    await runToEnd();
    const open = within(historySection()).getByRole("button", { name: /^Open the run of / });
    fireEvent.click(open);
    await act(settle);
    expect(runner.Run).toHaveBeenCalledWith("op_past");

    // The past run's heading takes the focus; the latest run's results are hidden.
    const heading = screen.getByRole("heading", { name: /^Past run of / });
    expect(document.activeElement).toBe(heading);
    const view = screen.getByRole("region", { name: heading.textContent ?? "" });
    expect(view.textContent).toContain("Run stopped at a failure after 10 of 12");
    expect(view.textContent).toContain("Responses are not kept in the run history");
    expect(within(historySection()).getAllByRole("listitem")[0]?.getAttribute("aria-current")).toBe("true");
    const summary = within(view).getByRole("region", { name: "Summary" });
    expect(summary.querySelector('[data-fact="results"] dd')?.textContent).toBe("10");
    expect(screen.getAllByRole("list", { name: "Run results" })).toHaveLength(1);
    expectAccessible(document.body);

    // A result opens without asking Go for a response.
    fireEvent.click(within(summary).getByRole("button", { name: "Get user (iteration 2)" }));
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Result of Get user, iteration 2" });
    expect(dialog.textContent).toContain("The response of a past run is not kept in the run history.");
    expect(dialog.textContent).toContain("Expected status code to equal 201, but it was 200.");
    expect(runner.Response).not.toHaveBeenCalled();
    fireEvent.keyDown(dialog, { key: "Escape" });

    // Its report.
    runner.ExportRun.mockResolvedValue({ saved: true, path: "/r/past.json", size: 10 });
    fireEvent.click(within(view).getByRole("button", { name: "JSON" }));
    await act(settle);
    expect(runner.ExportRun).toHaveBeenCalledWith({ runId: "op_past", format: "json" });

    // Back: the latest run again, focus on the Open button.
    fireEvent.click(within(view).getByRole("button", { name: "Back to the latest run" }));
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    });
    expect(screen.queryByRole("heading", { name: /^Past run of / })).toBeNull();
    expect(screen.getByText("Run finished: 10 results")).toBeTruthy();
    expect(document.activeElement).toBe(open);
  });

  it("deletes a past run, closing it when it is shown", async () => {
    runner.Runs.mockResolvedValue([PAST]);
    runner.Run.mockResolvedValue({ summary: PAST, results: results() });
    await setup();
    fireEvent.click(within(historySection()).getByRole("button", { name: /^Open the run of / }));
    await act(settle);
    expect(screen.getByRole("heading", { name: /^Past run of / })).toBeTruthy();
    runner.Runs.mockResolvedValue([]);
    fireEvent.click(within(historySection()).getByRole("button", { name: /^Delete the run of / }));
    await act(settle);
    await act(settle);
    expect(runner.DeleteRun).toHaveBeenCalledWith("op_past");
    expect(screen.queryByRole("heading", { name: /^Past run of / })).toBeNull();
    expect(historySection().textContent).toContain("No past runs of this folder yet.");
  });

  it("says when a past run is gone", async () => {
    runner.Runs.mockResolvedValue([PAST]);
    runner.Run.mockRejectedValue(
      Object.assign(new Error("gone"), {
        cause: { kind: "not_found", code: "runner.run_not_found", message: "The run is no longer in the run history.", hint: "", details: {}, causes: [] },
      }),
    );
    await setup();
    fireEvent.click(within(historySection()).getByRole("button", { name: /^Open the run of / }));
    await act(settle);
    expect(within(historySection()).getByRole("alert").textContent).toContain("The run is no longer in the run history.");
  });

  it("shows the history, a past run and the report texts from the catalogue (pseudo-locale)", async () => {
    setLocale(PSEUDO_LOCALE);
    runner.Runs.mockResolvedValue([PAST]);
    runner.Run.mockResolvedValue({ summary: { ...PAST, omitted: 3 }, results: results() });
    await setup();
    await runToEnd();
    expect(englishLeftovers([document.body], ["runner"])).toEqual([]);
    const historyButtons = document.querySelectorAll<HTMLButtonElement>("ul button");
    fireEvent.click(historyButtons[0] as HTMLButtonElement);
    await act(settle);
    expect(document.activeElement?.tagName).toBe("H3");
    expect(englishLeftovers([document.body], ["runner"])).toEqual([]);
    runner.ExportRun.mockResolvedValue({ saved: true, path: "/r/x.html", size: 1 });
    fireEvent.click(document.querySelectorAll<HTMLButtonElement>('[role="group"] button')[2] as HTMLButtonElement);
    await act(settle);
    expect(englishLeftovers([document.body], ["runner"])).toEqual([]);
  });
});
