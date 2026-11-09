import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  Repeat: vi.fn<(input: { requestId: string; count: number; intervalMs: number }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { EditorView } from "@codemirror/view";
import { httpSends, resetHttpSends } from "../../commands/httpSends";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { HttpResponsePanel } from "./HttpResponsePanel";
import type { ResponseDTO } from "../../lib/http";
import { httpResponse } from "../../test/httpResponse";
import { createStores, StoreProvider } from "../../stores";

/** The panel as the app has it: inside the stores (the body filter reads the workspace id). */
function renderPanel() {
  return render(
    <StoreProvider stores={createStores()}>
      <HttpResponsePanel id="req_1" />
    </StoreProvider>,
  );
}

function response(over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({
    proto: "HTTP/2.0",
    protocol: "HTTP/2",
    headers: [
      { name: "Content-Type", value: "application/json" },
      { name: "X-Request-Id", value: "abc" },
    ],
    size: 2048,
    headerSize: 312,
    durationMs: 1234,
    timeoutMs: 30000,
    timeoutSource: "settings",
    ...over,
  });
}

const bodyText = () =>
  (EditorView.findFromDOM(document.querySelector("[data-response-for] .cm-editor") as HTMLElement) as EditorView).state.doc.toString();

const done = (status: string, error: unknown = null) => ({ opId: "op_1", seq: 9, status, error, items: 1, batches: 1, elapsedMs: 5 });

async function start(): Promise<void> {
  act(() => httpSends().get("req_1").send());
  await act(settle);
}

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  http.Send.mockReset().mockResolvedValue({ opId: "op_1" });
  http.Repeat.mockReset().mockResolvedValue({ opId: "op_1" });
});

describe("HttpResponsePanel", () => {
  it("invites a send before the first one", () => {
    renderPanel();
    expect(screen.getByText("No response yet")).toBeTruthy();
  });

  it("shows the stage with Cancel while the request runs", async () => {
    renderPanel();
    await start();
    expect(screen.getByRole("status").textContent).toContain("Sending the request");
    act(() => emit("http:op:progress", { opId: "op_1", seq: 1, done: 0, total: 0, message: "Waiting for the response" }));
    expect(screen.getByRole("status").textContent).toContain("Waiting for the response");
    expect(screen.queryByRole("progressbar")).toBeNull();

    // The body's bytes as they arrive (HTTP-37): with a bar when the size is known.
    act(() => emit("http:op:progress", { opId: "op_1", seq: 2, done: 5 * 1024 * 1024, total: 50 * 1024 * 1024, message: "Receiving the response" }));
    expect(screen.getByRole("status").textContent).toContain("5.0 MB of 50.0 MB");
    expect(screen.getByRole("progressbar").getAttribute("value")).toBe(String(5 * 1024 * 1024));
    act(() => emit("http:op:progress", { opId: "op_1", seq: 3, done: 2048, total: 0, message: "Receiving the response" }));
    expect(screen.getByRole("status").textContent).toContain("2.0 KB received");
    expect(screen.queryByRole("progressbar")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => emit("http:op:done", done("cancelled")));
    expect(screen.getByText("Request cancelled")).toBeTruthy();
  });

  it("shows status, time, sizes, protocol, timeout, headers and the body, pretty or raw", async () => {
    renderPanel();
    await start();
    act(() => {
      emit("http:op:data", { opId: "op_1", seq: 1, items: [response()] });
      emit("http:op:done", done("ok"));
    });
    const status = document.querySelector("[data-response-status]") as HTMLElement;
    expect(status.textContent).toContain("200 OK");
    expect(status.textContent).toContain("1.23 s");
    expect(status.textContent).toContain("2.0 KB");
    expect(status.textContent).toContain("312 B headers");
    expect(status.textContent).toContain("HTTP/2");
    expect(status.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("ok");
    expect(status.textContent).toContain("Timeout 30 s (default)");
    expect(bodyText()).toBe('{\n  "a": 1\n}');

    fireEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(bodyText()).toBe('{"a":1}');

    const headersTab = screen.getByRole("tab", { name: /Headers/ });
    expect(headersTab.textContent).toBe("Headers2");
    fireEvent.click(headersTab);
    const table = screen.getByRole("table", { name: "Response headers" });
    expect(table.textContent).toContain("X-Request-Id");
    expect(table.textContent).toContain("abc");
  });

  it("shows a timeout as a clear error with its hint", async () => {
    renderPanel();
    await start();
    act(() =>
      emit(
        "http:op:done",
        done("error", {
          kind: "timeout",
          code: "httpclient.timeout",
          message: "The request to example.com:443 timed out while waiting for the response. The request's timeout is 2 s.",
          hint: "Increase the timeout in the request's Settings tab, or check that the server is responding.",
          details: { timeoutMs: "2000", timeoutSource: "request" },
          causes: [],
        }),
      ),
    );
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("Request timed out");
    expect(alert.textContent).toContain("The request's timeout is 2 s.");
    expect(alert.textContent).toContain("Increase the timeout");
  });

  it("notes a clipped or binary body and a response without a body", async () => {
    renderPanel();
    await start();
    act(() => {
      emit("http:op:data", {
        opId: "op_1",
        seq: 1,
        items: [response({ body: "", bodyPretty: "", bodyClipped: true, bodyShown: 1 << 20, bodyBinary: true, headers: null, warnings: null })],
      });
      emit("http:op:done", done("ok"));
    });
    expect(screen.getByText(/Only the first 1.0 MB/)).toBeTruthy();
    expect(screen.getByText(/not valid UTF-8/)).toBeTruthy();
    expect(screen.getByText("The response has no body.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();
  });

  it("lists a run's results above its latest response, with Stop (HTTP-27)", async () => {
    renderPanel();
    act(() => httpSends().get("req_1").repeat({ count: 0, intervalMs: 1000 }));
    await act(settle);
    expect(screen.getByText("Sending 1")).toBeTruthy();
    const result = (index: number, over: Record<string, unknown> = {}) => ({
      index,
      startedAt: 1_700_000_000_000,
      durationMs: 100 * index,
      statusCode: 200,
      status: "200 OK",
      size: 2048,
      testsPassed: 1,
      testsFailed: 0,
      error: null,
      response: null,
      ...over,
    });
    act(() => {
      emit("http.repeat:op:data", {
        opId: "op_1",
        seq: 1,
        items: [
          result(1, { response: response() }),
          result(2, { statusCode: 0, status: "", error: { kind: "network", code: "netx.refused", message: "Connection refused.", hint: "", details: {}, causes: [] } }),
        ],
      });
      emit("http.repeat:op:progress", { opId: "op_1", seq: 2, done: 2, total: 0, message: "Waiting for the next send" });
    });
    expect(screen.getByText("Waiting to send 3")).toBeTruthy();
    expect(screen.getByText(/1 response · 1 failed · avg 100 ms/)).toBeTruthy();
    const list = screen.getByRole("list", { name: "Run results, newest first" });
    const rows = list.querySelectorAll("li");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain("Connection refused.");
    expect(rows[1]?.textContent).toContain("200 OK");
    expect(rows[1]?.textContent).toContain("1 test passed");
    expect(screen.getByText("Response of send 1")).toBeTruthy();
    expect(bodyText()).toContain(`"a": 1`);

    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => emit("http.repeat:op:done", done("cancelled")));
    expect(screen.getByText("Run stopped after 2 sends")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    expect(screen.getByRole("status").textContent).toContain("Run stopped after 2 sends");
  });
});
