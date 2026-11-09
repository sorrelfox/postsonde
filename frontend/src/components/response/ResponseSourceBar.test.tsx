import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  StoredResponse: vi.fn<(input: unknown) => Promise<unknown>>(),
  CompareResponses: vi.fn<(input: unknown) => unknown>(),
}));
const history = vi.hoisted(() => ({
  List: vi.fn<(input: unknown) => Promise<unknown>>(),
  Get: vi.fn<(id: string) => Promise<unknown>>(),
  Example: vi.fn<(input: { id: string; name: string }) => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  UpdateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const servers = vi.hoisted(() => ({
  MockRouteFromExample: vi.fn<(input: unknown) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: http }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({ ServersService: servers }));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { httpSends, resetHttpSends } from "../../commands/httpSends";
import { resetResponseHistories } from "../../commands/responseHistory";
import type { RequestDTO } from "../../lib/collection";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { HttpResponsePanel } from "./HttpResponsePanel";

const entry = (id: string, status = "200 OK") => ({
  id,
  time: "2026-09-23T10:15:02Z",
  kind: "http",
  requestId: "req_1",
  requestName: "Get user",
  method: "GET",
  url: "https://api.test/u",
  outcome: "ok",
  statusCode: Number(status.slice(0, 3)),
  status,
  errorCode: "",
  error: "",
  durationMs: 12,
  requestSize: 0,
  responseSize: 2,
  masked: false,
});

const saved: ResponseExampleDTO = {
  name: "Found",
  savedAt: "2026-09-23T10:15:02Z",
  request: null,
  statusCode: 200,
  statusText: "OK",
  headers: [],
  body: "{}",
  bodyEncoding: "",
  bodyClipped: false,
  bodySize: 0,
  extra: "",
};

function renderPanel(examples: ResponseExampleDTO[] = []): Stores {
  const stores = createStores();
  const request = { id: "req_1", kind: "http", name: "Get user", responseExamples: examples } as unknown as RequestDTO;
  stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: request as unknown as Record<string, unknown> });
  render(
    <StoreProvider stores={stores}>
      <HttpResponsePanel id="req_1" />
    </StoreProvider>,
  );
  return stores;
}

const select = () => screen.getByRole("combobox", { name: "Response to show" }) as HTMLSelectElement;
const saveButton = () => screen.getByRole("button", { name: "Save as example" }) as HTMLButtonElement;

async function sendAndRespond(historyId: string): Promise<void> {
  act(() => httpSends().get("req_1").send());
  await act(settle);
  act(() => {
    emit("http:op:data", { opId: "op_1", seq: 1, items: [httpResponse({ historyId })] });
    emit("http:op:done", { opId: "op_1", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
  });
  await act(settle);
}

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  resetResponseHistories();
  http.Send.mockReset().mockResolvedValue({ opId: "op_1" });
  http.StoredResponse.mockReset().mockResolvedValue(httpResponse({ statusCode: 404, status: "404 Not Found", body: "old" }));
  history.List.mockReset().mockResolvedValue({ entries: [entry("h_1", "404 Not Found")], total: 1, all: 1 });
  history.Get.mockReset().mockResolvedValue({
    ...entry("h_1", "404 Not Found"),
    request: { method: "GET", url: "https://api.test/u", proto: "HTTP/1.1", headers: [], body: { text: "", base64: false, size: 0, clipped: false } },
    response: {
      proto: "HTTP/1.1", statusCode: 404, status: "404 Not Found", finalUrl: "https://api.test/u", redirects: 0,
      headers: [], contentType: "", body: { text: "old", base64: false, size: 3, clipped: false }, truncated: false,
    },
    trimmed: false,
  });
  history.Example.mockReset().mockResolvedValue({ ...saved, name: "200 OK" });
  collection.UpdateRequest.mockReset().mockResolvedValue({ version: 1 });
  http.CompareResponses.mockReset().mockImplementation(() => Object.assign(new Promise(() => {}), { cancel: vi.fn() }));
  servers.MockRouteFromExample.mockReset().mockImplementation(() => Object.assign(new Promise(() => {}), { cancel: vi.fn() }));
});

describe("ResponseSource (HTTP-38, HTTP-39)", () => {
  it("is left out without earlier responses, examples or a response to save", async () => {
    history.List.mockResolvedValue({ entries: [], total: 0, all: 0 });
    renderPanel();
    await act(settle);
    expect(screen.queryByRole("combobox", { name: "Response to show" })).toBeNull();
    expect(screen.getByText("No response yet")).toBeTruthy();
  });

  it("shows an earlier response until the next send", async () => {
    renderPanel([saved]);
    await act(settle);
    const options = Array.from(select().options).map((o) => o.textContent);
    expect(options[0]).toBe("Latest response");
    expect(options[1]).toContain("404 Not Found");
    expect(options[2]).toBe("Found");
    expectAccessible();

    act(() => {
      fireEvent.change(select(), { target: { value: "h:h_1" } });
    });
    await act(settle);
    expect(history.Get).toHaveBeenCalledWith("h_1");
    expect(screen.getByText(/^Sent /)).toBeTruthy();
    expect(screen.getByRole("tablist", { name: "Response sections" })).toBeTruthy();
    // A stored response has no test results.
    expect(screen.queryByRole("tab", { name: /^Tests/ })).toBeNull();

    // The next send shows itself.
    await sendAndRespond("h_2");
    expect(select().value).toBe("latest");
    expect(screen.queryByText(/^Sent /)).toBeNull();
  });

  it("saves the latest response as an example", async () => {
    const stores = renderPanel();
    await act(settle);
    await sendAndRespond("h_2");
    // The latest response is not listed twice.
    expect(Array.from(select().options).some((o) => o.value === "h:h_2")).toBe(false);
    expect(saveButton().disabled).toBe(false);
    await act(async () => {
      fireEvent.click(saveButton());
      await settle();
    });
    expect(history.Example).toHaveBeenCalledWith({ id: "h_2", name: "" });
    const request = stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO;
    expect(request.responseExamples?.map((e) => e.name)).toEqual(["200 OK"]);
    expect(Array.from(select().options).map((o) => o.textContent)).toContain("200 OK");
  });

  it("cannot save a response the history does not have", async () => {
    renderPanel();
    await act(settle);
    await sendAndRespond("");
    expect(saveButton().disabled).toBe(true);
  });

  it("shows an example, without Save", async () => {
    renderPanel([saved]);
    await act(settle);
    act(() => {
      fireEvent.change(select(), { target: { value: "e:0" } });
    });
    await act(settle);
    expect(http.StoredResponse).toHaveBeenCalledWith(expect.objectContaining({ statusCode: 200, status: "OK", body: "{}" }));
    expect(screen.getByText("Saved example")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save as example" })).toBeNull();
  });

  it("makes a mock route of the response shown (MOCK-03)", async () => {
    renderPanel([saved]);
    await act(settle);
    const mock = () => screen.getByRole("button", { name: "Create mock route…" }) as HTMLButtonElement;
    // The latest send has no response yet: nothing to mock.
    expect(mock().disabled).toBe(true);
    await sendAndRespond("h_2");
    expect(mock().disabled).toBe(false);
    await act(async () => {
      fireEvent.click(mock());
      await settle();
    });
    expect(screen.getByRole("dialog", { name: "Create mock route" })).toBeTruthy();
    // Go makes the example of the history entry, masked.
    expect(history.Example).toHaveBeenCalledWith({ id: "h_2", name: "" });
    expect(servers.MockRouteFromExample).toHaveBeenCalledWith(expect.objectContaining({ example: { ...saved, name: "200 OK" }, mode: "route" }));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
      await settle();
    });
    expect(screen.queryByRole("dialog")).toBeNull();

    // A saved example is mocked as it is.
    act(() => {
      fireEvent.change(select(), { target: { value: "e:0" } });
    });
    await act(settle);
    history.Example.mockClear();
    await act(async () => {
      fireEvent.click(mock());
      await settle();
    });
    expect(history.Example).not.toHaveBeenCalled();
    expect(servers.MockRouteFromExample).toHaveBeenLastCalledWith(expect.objectContaining({ example: saved }));
  });

  it("compares two of the responses and examples (HTTP-40)", async () => {
    history.List.mockResolvedValue({ entries: [], total: 0, all: 0 });
    renderPanel([saved]);
    await act(settle);
    const compare = () => screen.getByRole("button", { name: "Compare…" }) as HTMLButtonElement;
    // One example alone: nothing to compare it with.
    expect(compare().disabled).toBe(true);
    await sendAndRespond("h_2");
    expect(compare().disabled).toBe(false);
    await act(async () => {
      fireEvent.click(compare());
      await settle();
    });
    const dialog = screen.getByRole("dialog", { name: "Compare responses" });
    // The example on the left, the latest response on the right.
    expect((within(dialog).getByLabelText("Left") as HTMLSelectElement).value).toBe("e:0");
    expect((within(dialog).getByLabelText("Right") as HTMLSelectElement).value).toBe("h:h_2");
    expect(Array.from((within(dialog).getByLabelText("Right") as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
      "Latest response",
      "Found",
    ]);
    expect(history.Get).toHaveBeenCalledWith("h_2");
    expect(http.CompareResponses).toHaveBeenCalledWith({
      left: expect.objectContaining({ statusCode: 200, body: "{}" }),
      right: expect.objectContaining({ statusCode: 404, body: "old" }),
    });
  });
});
