import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const sse = vi.hoisted(() => ({
  Connect: vi.fn<(input: { requestId: string; allowUnresolved: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse", () => ({
  SSEService: sse,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { resetSseStreams, sseStreams } from "../../commands/sseStreams";
import type { StreamItemDTO } from "../../lib/sse";
import { StoreProvider } from "../../stores";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { SseResponsePanel } from "./SseResponsePanel";

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

const status = () => document.querySelector("[data-stream-status]")?.textContent;

function renderPanel() {
  return render(
    <StoreProvider>
      <SseResponsePanel id="req_sse" />
    </StoreProvider>,
  );
}

const data = (seq: number, items: StreamItemDTO[]) => act(() => emit("sse:op:data", { opId: "op_1", seq, items }));

async function connect(): Promise<void> {
  act(() => sseStreams().get("req_sse").connect());
  await act(settle);
}

beforeEach(() => {
  resetFakeWails();
  resetSseStreams();
  sse.Connect.mockReset().mockResolvedValue({ opId: "op_1" });
});

describe("SseResponsePanel", () => {
  it("says it is not connected before the first connect", () => {
    renderPanel();
    expect(screen.getByText("Not connected")).toBeTruthy();
  });

  it("shows the stream live: status, events with their fields, headers, and Stop (SSE-01, SSE-02)", async () => {
    renderPanel();
    await connect();
    expect(status()).toBe("Connecting…");
    act(() =>
      emit("sse:op:data", {
        opId: "op_1",
        seq: 1,
        items: [
          item({ type: "open", method: "GET", url: "http://x/e", status: 200, statusText: "200 OK", proto: "HTTP/1.1", headers: [{ name: "Content-Type", value: "text/event-stream" }] }),
          item({ type: "retry", retryMs: 3000 }),
          item({ type: "event", index: 1, event: "price", id: "42", data: '{"px":1}', size: 8 }),
        ],
      }),
    );
    act(() => sseStreams().get("req_sse").log.flush());
    expect(status()).toBe("Connected · 1 event");
    expect(screen.getByText("id 42")).toBeTruthy();
    expect(screen.getByText("retry 3000 ms")).toBeTruthy();
    expect(screen.getByRole("tab", { name: /Events/ }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("price")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: /Headers/ }));
    expect(screen.getByRole("table", { name: "Response headers" }).textContent).toContain("text/event-stream");

    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => {
      emit("sse:op:data", { opId: "op_1", seq: 2, items: [item({ type: "end", reason: "stopped", events: 1, id: "42", elapsedMs: 50 })] });
      emit("sse:op:done", { opId: "op_1", seq: 3, status: "cancelled", error: null, items: 4, batches: 2, elapsedMs: 51 });
    });
    expect(status()).toBe("Stopped · 1 event");
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("shows the error of a stream that failed before the server answered", async () => {
    renderPanel();
    await connect();
    const error = { kind: "network", code: "netx.connection_refused", message: "The connection was refused.", hint: "Is the server running?", details: {}, causes: [] };
    act(() => emit("sse:op:done", { opId: "op_1", seq: 1, status: "error", error, items: 0, batches: 0, elapsedMs: 3 }));
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("The connection was refused.");
    expect(alert.textContent).toContain("Is the server running?");
  });

  it("shows a reconnect wait, counts reconnects and resumes an ended stream with its last event ID (SSE-03)", async () => {
    renderPanel();
    await connect();
    const open = item({ type: "open", status: 200, statusText: "200 OK", proto: "HTTP/1.1", headers: [] });
    data(1, [open, item({ type: "event", index: 1, event: "message", id: "7", data: "a", size: 1 })]);
    data(2, [item({ type: "reconnect", id: "7", attempt: 1, maxAttempts: 5, delayMs: 3000, reason: "error", text: "The connection dropped." })]);
    expect(status()).toBe("Reconnecting, attempt 1 of 5 · 1 event");
    expect(document.querySelector("[data-reconnect-wait]")?.textContent).toContain("attempt 1 of 5");
    act(() => sseStreams().get("req_sse").log.flush());
    expect(screen.getByText(/Reconnecting in 3000 ms with Last-Event-ID 7 \(attempt 1 of 5\)/)).toBeTruthy();

    data(3, [open, item({ type: "event", index: 2, event: "message", id: "8", data: "b", size: 1 })]);
    expect(status()).toBe("Connected · 2 events");
    expect(screen.getByText("1 reconnect")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();

    act(() => {
      emit("sse:op:data", { opId: "op_1", seq: 4, items: [item({ type: "end", reason: "closed", events: 2, id: "8", reconnects: 1 })] });
      emit("sse:op:done", { opId: "op_1", seq: 5, status: "ok", error: null, items: 6, batches: 4, elapsedMs: 9 });
    });
    sse.Connect.mockResolvedValue({ opId: "op_2" });
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    await act(settle);
    expect(sse.Connect).toHaveBeenLastCalledWith({ requestId: "req_sse", allowUnresolved: false, lastEventId: "8" });
    // The log and the counts carry on.
    expect(status()).toBe("Connecting…");
    expect(screen.getByText("2 events")).toBeTruthy();
  });

  it("merges the events' data into one text, by type and JSON field (SSE-04)", async () => {
    renderPanel();
    await connect();
    const ev = (index: number, event: string, d: string) => item({ type: "event", index, event, data: d, size: d.length });
    data(1, [
      item({ type: "open", status: 200, statusText: "200 OK", proto: "HTTP/1.1", headers: [] }),
      ev(1, "message", '{"choices":[{"delta":{"content":"The"}}]}'),
      ev(2, "message", '{"choices":[{"delta":{"content":" answer"}}]}'),
      ev(3, "usage", '{"tokens":2}'),
      ev(4, "message", '{"choices":[{"delta":{"content":" is 42."}}]}'),
      ev(5, "message", "[DONE]"),
    ]);
    act(() => sseStreams().get("req_sse").log.flush());
    fireEvent.click(screen.getByRole("tab", { name: /Merged/ }));
    const text = () => document.querySelector("[data-merged-text]")?.textContent;
    expect(text()).toBe("The answer is 42.");
    expect(document.querySelector("[data-merge-info]")?.textContent).toContain("3 of 5 events merged");

    // The view follows the stream live.
    data(2, [ev(6, "message", '{"choices":[{"delta":{"content":" Done"}}]}')]);
    act(() => sseStreams().get("req_sse").log.flush());
    expect(text()).toBe("The answer is 42. Done");

    fireEvent.change(screen.getByLabelText("Events"), { target: { value: "usage" } });
    fireEvent.change(screen.getByLabelText("Text"), { target: { value: "field" } });
    fireEvent.change(screen.getByLabelText("JSON field"), { target: { value: "tokens" } });
    expect(text()).toBe("2");

    fireEvent.change(screen.getByLabelText("Events"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Text"), { target: { value: "data" } });
    fireEvent.change(screen.getByLabelText("Join with"), { target: { value: "newline" } });
    expect(text()?.split("\n")).toHaveLength(6);
    expect(text()?.split("\n")[4]).toBe("[DONE]");
  });
});
