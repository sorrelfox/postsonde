import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: { id: string; url: string }; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  FlushAll: vi.fn<() => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { COLLECTION_SAVE_FAILED_EVENT, COLLECTION_SAVED_EVENT, type RequestDTO } from "../lib/collection";
import { createStores, selectIsDirty, selectRequestSaveStatus, type Stores } from "../stores";
import { deferred, emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import { historyDepth } from "./editHistory";
import {
  applyRenamed,
  applySaved,
  editRequest,
  flushAllRequests,
  flushRequests,
  followAutoSave,
  loadRequestDraft,
} from "./autosave";
import { closeTabs } from "./tabs";

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req_1",
    kind: "http",
    name: "Health",
    description: "",
    method: "GET",
    url: "/health",
    params: [],
    pathParams: [],
    headers: [],
    body: { mode: "none", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "",
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: "",
    ...over,
  };
}

const saveFailed = {
  kind: "permission_denied",
  code: "projectfile.save_failed",
  message: "A project file could not be saved.",
  hint: "Check that the project folder is writable and the disk is not full.",
  details: { path: "/p/health.json" },
  causes: ["permission denied"],
};

/** A Wails RuntimeError as the runtime rejects with. */
function runtimeError(cause: unknown): Error {
  const err = new Error("go error text");
  err.name = "RuntimeError";
  (err as Error & { cause: unknown }).cause = cause;
  return err;
}

async function openDraft(): Promise<Stores> {
  const stores = createStores();
  service.Request.mockResolvedValueOnce(request());
  await loadRequestDraft(stores, "req_1");
  return stores;
}

const stops: (() => void)[] = [];
/** followAutoSave, stopped after the test. */
function follow(stores: Stores): () => void {
  const stop = followAutoSave(stores);
  stops.push(stop);
  return stop;
}

afterEach(() => {
  for (const stop of stops.splice(0)) stop();
});

const sentUrls = () => service.UpdateRequest.mock.calls.map(([input]) => `${input.request.url}#${input.revision}`);

beforeEach(() => {
  resetFakeWails();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.FlushAll.mockReset().mockResolvedValue(undefined);
});

describe("auto-save commands", () => {
  it("loads a draft from Go", async () => {
    const stores = await openDraft();
    expect(service.Request).toHaveBeenCalledWith("req_1");
    expect(stores.drafts.getState().byId.req_1).toMatchObject({ dirty: false, revision: 0, request: request() });
  });

  it("goes dirty → saving → saved", async () => {
    const stores = await openDraft();
    const stop = follow(stores);

    editRequest(stores, "req_1", { url: "/health/live" });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(true);
    expect(selectRequestSaveStatus(stores.autosave.getState(), "req_1")).toBe("saving");
    expect(service.UpdateRequest).toHaveBeenCalledWith({ request: request({ url: "/health/live" }), revision: 1, baseVersion: 0 });

    await settle();
    emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, savedAt: "2026-09-16T10:00:00Z" });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(false);
    expect(selectRequestSaveStatus(stores.autosave.getState(), "req_1")).toBe("saved");
    expect(stores.autosave.getState().lastSavedAt).toBe("2026-09-16T10:00:00Z");

    stop();
    expect(listenerCount()).toBe(0);
  });

  it("sends one update at a time and collapses edits made meanwhile", async () => {
    const stores = await openDraft();
    const first = deferred<void>();
    service.UpdateRequest.mockReturnValueOnce(first.promise);

    editRequest(stores, "req_1", { url: "/a" });
    editRequest(stores, "req_1", { url: "/ab" });
    editRequest(stores, "req_1", { url: "/abc" });
    expect(sentUrls()).toEqual(["/a#1"]);

    first.resolve();
    await settle();
    expect(sentUrls()).toEqual(["/a#1", "/abc#3"]);

    // A saved event for an older revision does not mark the draft saved.
    follow(stores);
    emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, savedAt: "t1" });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(true);
    expect(selectRequestSaveStatus(stores.autosave.getState(), "req_1")).toBe("saving");
    emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 3, savedAt: "t2" });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(false);
  });

  it("puts a rename into the draft and the queued edit, not into the one in flight", async () => {
    const stores = await openDraft();
    const first = deferred<void>();
    service.UpdateRequest.mockReturnValueOnce(first.promise);
    editRequest(stores, "req_1", { url: "/a" });
    editRequest(stores, "req_1", { url: "/ab" });
    const before = stores.drafts.getState().byId.req_1;

    applyRenamed(stores, "req_1", "Liveness");
    const after = stores.drafts.getState().byId.req_1;
    expect(after).toMatchObject({ dirty: true, revision: before?.revision, request: { name: "Liveness", url: "/ab" } });
    // Renaming to the current name changes nothing.
    applyRenamed(stores, "req_1", "Liveness");
    expect(stores.drafts.getState().byId.req_1).toBe(after);

    first.resolve();
    await settle();
    const names = service.UpdateRequest.mock.calls.map(([input]) => (input.request as unknown as RequestDTO).name);
    expect(names).toEqual(["Health", "Liveness"]);

    // Without a draft, nothing happens.
    applyRenamed(stores, "req_other", "X");
    expect(stores.drafts.getState().byId.req_other).toBeUndefined();
  });

  it("shows a failed write and keeps the draft dirty", async () => {
    const stores = await openDraft();
    follow(stores);
    editRequest(stores, "req_1", { name: "Me" });
    await settle();
    emit(COLLECTION_SAVE_FAILED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, error: saveFailed });

    expect(selectRequestSaveStatus(stores.autosave.getState(), "req_1")).toBe("error");
    expect(stores.autosave.getState().failed.req_1).toMatchObject({
      revision: 1,
      code: "projectfile.save_failed",
      message: saveFailed.message,
      hint: saveFailed.hint,
    });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(true);
  });

  it("reports an update Go refuses at once", async () => {
    const stores = await openDraft();
    service.UpdateRequest.mockRejectedValueOnce(
      runtimeError({
        kind: "unsupported",
        code: "collection.read_only",
        message: "The file is a symbolic link, which saving would replace with a copy.",
        hint: "",
        details: { id: "req_1", reason: "symlink" },
        causes: [],
      }),
    );
    editRequest(stores, "req_1", { url: "/x" });
    await settle();
    expect(stores.autosave.getState().failed.req_1).toMatchObject({ code: "collection.read_only", revision: 1 });
    expect(stores.autosave.getState().pending).toEqual({});
  });

  it("flushes on tab close after sending what is queued", async () => {
    const stores = await openDraft();
    follow(stores);
    stores.tabs.dispatch({ type: "tabs/open", id: "req_1" });
    const first = deferred<void>();
    service.UpdateRequest.mockReturnValueOnce(first.promise);
    editRequest(stores, "req_1", { url: "/a" });
    editRequest(stores, "req_1", { url: "/latest" });

    closeTabs(stores, ["req_1"]);
    // The tab and draft go at once; the flush waits for the queue.
    expect(stores.tabs.getState().order).toEqual([]);
    expect(stores.drafts.getState().byId.req_1).toBeUndefined();
    expect(service.Flush).not.toHaveBeenCalled();

    first.resolve();
    await settle();
    await settle();
    expect(sentUrls()).toEqual(["/a#1", "/latest#2"]);
    expect(service.Flush).toHaveBeenCalledWith("req_1");
    const flushOrder = service.Flush.mock.invocationCallOrder[0];
    expect(flushOrder).toBeGreaterThan(service.UpdateRequest.mock.invocationCallOrder[1]);
  });

  it("does not call Go when closing tabs with nothing to save", async () => {
    const stores = await openDraft();
    follow(stores);
    await flushRequests(stores, ["req_1", "req_other"]);
    stores.tabs.dispatch({ type: "tabs/open", id: "req_1" });
    stores.tabs.dispatch({ type: "tabs/open", id: "fld_users" });
    closeTabs(stores, ["req_1", "fld_users"]);
    await settle();
    expect(service.Flush).not.toHaveBeenCalled();
  });

  it("flushes closing tabs only through the onTabClosed listener of its own stores, until stopped", async () => {
    const stores = await openDraft();
    const other = createStores();
    const stop = follow(stores);
    editRequest(stores, "req_1", { url: "/a" });
    await settle();

    // Closing a tab of other stores does not flush these.
    other.tabs.dispatch({ type: "tabs/open", id: "req_1" });
    closeTabs(other, ["req_1"]);
    await settle();
    expect(service.Flush).not.toHaveBeenCalled();

    // After stop, closing a tab no longer flushes.
    stop();
    stores.tabs.dispatch({ type: "tabs/open", id: "req_1" });
    closeTabs(stores, ["req_1"]);
    await settle();
    expect(service.Flush).not.toHaveBeenCalled();

    // Following again flushes the closed request.
    follow(stores);
    stores.tabs.dispatch({ type: "tabs/open", id: "req_1" });
    closeTabs(stores, ["req_1"]);
    await settle();
    expect(service.Flush).toHaveBeenCalledTimes(1);
    expect(service.Flush).toHaveBeenCalledWith("req_1");
  });

  it("never rejects a flush", async () => {
    const stores = await openDraft();
    editRequest(stores, "req_1", { url: "/a" });
    service.Flush.mockRejectedValueOnce(runtimeError(saveFailed));
    service.FlushAll.mockRejectedValueOnce(runtimeError(saveFailed));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(flushRequests(stores, ["req_1"])).resolves.toBeUndefined();
    await expect(flushAllRequests(stores)).resolves.toBeUndefined();
    warn.mockRestore();
  });

  it("flushes everything when the page is hidden", async () => {
    const stores = await openDraft();
    const stop = follow(stores);
    editRequest(stores, "req_1", { url: "/a" });
    window.dispatchEvent(new Event("pagehide"));
    await settle();
    expect(service.FlushAll).toHaveBeenCalledTimes(1);
    stop();
    window.dispatchEvent(new Event("pagehide"));
    await settle();
    expect(service.FlushAll).toHaveBeenCalledTimes(1);
  });

  it("ignores edits of requests without a draft", () => {
    const stores = createStores();
    editRequest(stores, "req_1", { url: "/a" });
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    expect(stores.autosave.getState().pending).toEqual({});
  });

  it("records edits for undo, and drops them when a clean draft is reloaded (NAV-07)", async () => {
    const stores = await openDraft();
    editRequest(stores, "req_1", { url: "/a" });
    editRequest(stores, "req_1", { method: "PUT" }, { history: "separate" });
    expect(historyDepth(stores, "req_1").undo).toBe(2);

    await settle();
    applySaved(stores, { workspaceId: "ws_1", id: "req_1", revision: 2, window: 0, savedAt: "2026-09-16T10:00:00Z" });
    expect(selectIsDirty(stores.drafts.getState(), "req_1")).toBe(false);
    service.Request.mockResolvedValueOnce(request({ url: "/from-disk" }));
    await loadRequestDraft(stores, "req_1");
    expect(historyDepth(stores, "req_1").undo).toBe(0);
  });
});
