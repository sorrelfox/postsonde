import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const history = vi.hoisted(() => ({
  List: vi.fn<(input: unknown) => Promise<unknown>>(),
  Get: vi.fn<(id: string) => Promise<unknown>>(),
  Example: vi.fn<(input: { id: string; name: string }) => Promise<unknown>>(),
}));
const http = vi.hoisted(() => ({
  StoredResponse: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  UpdateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: http }));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));

import type { RequestDTO } from "../lib/collection";
import { entryAsStored, exampleAsStored, uniqueName, type HistoryEntryDTO, type HistorySummaryDTO, type ResponseExampleDTO } from "../lib/responseHistory";
import { createStores, type Stores } from "../stores";
import { emit, resetFakeWails, settle } from "../test/fakeWails";
import { httpResponse } from "../test/httpResponse";
import {
  deleteResponseExample,
  renameResponseExample,
  resetResponseHistories,
  responseHistories,
  saveResponseAsExample,
  showEarlierResponse,
  showResponseExample,
} from "./responseHistory";
import { toaster } from "./toasts";

function summary(id: string, over: Partial<HistorySummaryDTO> = {}): HistorySummaryDTO {
  return {
    id,
    time: "2026-09-23T10:15:02Z",
    kind: "http",
    requestId: "req_1",
    requestName: "Get user",
    environment: "",
    method: "GET",
    url: "https://api.test/u",
    outcome: "ok",
    statusCode: 200,
    status: "200 OK",
    errorCode: "",
    error: "",
    durationMs: 12,
    requestSize: 0,
    responseSize: 2,
    masked: false,
    ...over,
  };
}

function example(name: string, over: Partial<ResponseExampleDTO> = {}): ResponseExampleDTO {
  return {
    name,
    savedAt: "2026-09-23T10:15:02Z",
    request: null,
    statusCode: 200,
    statusText: "OK",
    headers: [{ name: "Content-Type", value: "application/json", description: "", enabled: true, extra: "" }],
    body: "{}",
    bodyEncoding: "",
    bodyClipped: false,
    bodySize: 0,
    extra: "",
    ...over,
  };
}

function withDraft(examples: ResponseExampleDTO[] = []): Stores {
  const stores = createStores();
  const request = { id: "req_1", kind: "http", name: "Get user", responseExamples: examples } as unknown as RequestDTO;
  stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: request as unknown as Record<string, unknown> });
  return stores;
}

const draftExamples = (stores: Stores) =>
  (stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO).responseExamples ?? [];

beforeEach(() => {
  resetFakeWails();
  resetResponseHistories();
  toaster.clear();
  history.List.mockReset().mockResolvedValue({ entries: [summary("h_2"), summary("h_x", { outcome: "error" }), summary("h_1")], total: 3, all: 3 });
  history.Get.mockReset();
  history.Example.mockReset();
  http.StoredResponse.mockReset().mockResolvedValue(httpResponse({ statusCode: 404, status: "404 Not Found" }));
  collection.UpdateRequest.mockReset().mockResolvedValue({ version: 1 });
});

afterEach(() => {
  resetResponseHistories();
});

describe("the earlier responses of a request (HTTP-39)", () => {
  it("lists the request's responses from the history and follows history:changed", async () => {
    const slot = responseHistories().get("req_1");
    const stop = slot.subscribe(() => undefined);
    await settle();
    expect(history.List).toHaveBeenCalledWith(expect.objectContaining({ requestId: "req_1", limit: 20 }));
    expect(slot.getState().entries.map((e) => e.id)).toEqual(["h_2", "h_1"]);

    emit("history:changed", { action: "added", id: "h_3", entry: summary("h_3") });
    emit("history:changed", { action: "added", id: "h_o", entry: summary("h_o", { requestId: "req_2" }) });
    emit("history:changed", { action: "added", id: "h_f", entry: summary("h_f", { outcome: "cancelled" }) });
    expect(slot.getState().entries.map((e) => e.id)).toEqual(["h_3", "h_2", "h_1"]);
    emit("history:changed", { action: "deleted", id: "h_2", entry: null });
    expect(slot.getState().entries.map((e) => e.id)).toEqual(["h_3", "h_1"]);
    // Clearing another request's history (the history panel) leaves this one.
    emit("history:changed", { action: "cleared", id: "", entry: null, requestId: "req_2" });
    expect(slot.getState().entries.map((e) => e.id)).toEqual(["h_3", "h_1"]);
    emit("history:changed", { action: "cleared", id: "", entry: null, requestId: "req_1" });
    expect(slot.getState().entries).toEqual([]);
    emit("history:changed", { action: "added", id: "h_4", entry: summary("h_4") });
    emit("history:changed", { action: "cleared", id: "", entry: null });
    expect(slot.getState().entries).toEqual([]);
    stop();
  });

  it("has no list without a history", async () => {
    history.List.mockRejectedValue({ kind: "invalid_argument", code: "workspace.none_open", message: "No project is open." });
    const slot = responseHistories().get("req_1");
    slot.subscribe(() => undefined);
    await settle();
    expect(slot.getState()).toMatchObject({ loaded: true, entries: [] });
  });

  it("shows an earlier response through its stored view until the next send", async () => {
    const entry: HistoryEntryDTO = {
      ...summary("h_1"),
      request: { method: "GET", url: "https://api.test/u", proto: "HTTP/1.1", headers: [{ name: "Host", value: "api.test" }], body: { text: "", base64: false, size: 0, clipped: false } },
      response: {
        proto: "HTTP/1.1", statusCode: 404, status: "404 Not Found", finalUrl: "https://api.test/u", redirects: 0,
        headers: [{ name: "Content-Type", value: "text/plain" }], contentType: "text/plain",
        body: { text: "nope", base64: false, size: 90000, clipped: true }, truncated: false,
      },
      trimmed: false,
    };
    history.Get.mockResolvedValue(entry);
    showEarlierResponse("req_1", "h_1", entry.time, 1000);
    const slot = responseHistories().get("req_1");
    expect(slot.getState()).toMatchObject({ shown: { kind: "history", id: "h_1" }, since: 1000, status: "loading" });
    await settle();
    expect(http.StoredResponse).toHaveBeenCalledWith(entryAsStored(entry));
    expect(slot.getState()).toMatchObject({ status: "ok", response: { status: "404 Not Found" } });

    http.StoredResponse.mockRejectedValueOnce({ kind: "invalid_argument", code: "http.invalid_stored_response", message: "Bad Base64." });
    showEarlierResponse("req_1", "h_1", entry.time, 1000);
    await settle();
    expect(slot.getState()).toMatchObject({ status: "error", error: { message: "Bad Base64." } });
  });
});

describe("response examples (HTTP-38)", () => {
  it("saves a history entry as an example with a unique name, in one edit", async () => {
    const stores = withDraft([example("200 OK")]);
    history.Example.mockResolvedValue(example("200 OK", { body: '{"id":"42"}' }));
    await saveResponseAsExample(stores, "req_1", "h_1");
    expect(history.Example).toHaveBeenCalledWith({ id: "h_1", name: "" });
    expect(draftExamples(stores).map((e) => e.name)).toEqual(["200 OK", "200 OK (2)"]);
    expect(draftExamples(stores)[1]?.body).toBe('{"id":"42"}');
    expect(toaster.toasts()[0]?.message).toBe("Saved the response as the example “200 OK (2)”.");

    // Show examples opens the Examples sub-tab.
    toaster.runAction(toaster.toasts()[0]!.id);
    expect(stores.drafts.getState().subTabs.req_1).toBe("examples");
  });

  it("says why a response could not be saved", async () => {
    const stores = withDraft();
    history.Example.mockRejectedValue({ kind: "invalid_argument", code: "history.no_response", message: "This send got no response to save as an example." });
    await saveResponseAsExample(stores, "req_1", "h_x");
    expect(draftExamples(stores)).toEqual([]);
    expect(toaster.toasts()[0]?.message).toBe("Could not save the response as an example: This send got no response to save as an example.");
    expect(responseHistories().get("req_1").getState().saving).toBe(false);
  });

  it("renames, shows and deletes examples", async () => {
    const stores = withDraft([example("A"), example("B", { bodyEncoding: "base64", body: "/w==" })]);
    renameResponseExample(stores, "req_1", 0, "Happy path");
    expect(draftExamples(stores).map((e) => e.name)).toEqual(["Happy path", "B"]);

    showResponseExample(stores, "req_1", 1, null);
    await settle();
    expect(http.StoredResponse).toHaveBeenCalledWith(expect.objectContaining({ body: "/w==", bodyBase64: true, statusCode: 200, status: "OK" }));
    const slot = responseHistories().get("req_1");
    expect(slot.getState().shown).toEqual({ kind: "example", index: 1, name: "B" });

    deleteResponseExample(stores, "req_1", 1);
    expect(draftExamples(stores).map((e) => e.name)).toEqual(["Happy path"]);
    expect(slot.getState().shown).toEqual({ kind: "latest" });
  });
});

describe("stored views", () => {
  it("maps an example with its request", () => {
    const stored = exampleAsStored(
      example("Created", {
        statusCode: 201,
        statusText: "Created",
        bodyClipped: true,
        bodySize: 90000,
        request: {
          method: "POST",
          url: "https://api.test/u",
          headers: [{ name: "Content-Type", value: "application/json", description: "", enabled: true, extra: "" }],
          body: { mode: "raw", type: "json", text: '{"a":1}', fields: [], parts: [], path: "", contentType: "", extra: "" },
          extra: "",
        },
      }),
    );
    expect(stored).toMatchObject({
      method: "POST",
      requestHeaders: [{ name: "Content-Type", value: "application/json" }],
      requestBody: '{"a":1}',
      statusCode: 201,
      status: "Created",
      bodyClipped: true,
      bodySize: 90000,
    });
  });

  it("names copies uniquely", () => {
    expect(uniqueName("200 OK", [])).toBe("200 OK");
    expect(uniqueName("200 OK", ["200 OK", "200 OK (2)"])).toBe("200 OK (3)");
  });
});
