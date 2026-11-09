import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Two windows of one project (APP-06, docs/windows.md): two sets of stores
// in one test, each with its own window ID, and a fake of Go's collection
// service that versions the request as internal/app/services/collection
// does (windows.go): an edit made from an older version is refused with
// collection.edited_elsewhere, and every edit it takes is announced with
// collection:edited, naming the window it came from.

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  Folder: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: Record<string, unknown>; revision: number; baseVersion?: number }) => Promise<unknown>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  FlushAll: vi.fn<() => Promise<void>>(),
  SetWatching: vi.fn<(active: boolean) => Promise<void>>(),
  ResolveConflict: vi.fn<(id: string) => Promise<void>>(),
}));

const windowService = vi.hoisted(() => ({
  Current: vi.fn<() => Promise<unknown>>(),
  NewWindow: vi.fn<() => Promise<unknown>>(),
  OpenInNewWindow: vi.fn<(path: string) => Promise<unknown>>(),
}));

const workspaceService = vi.hoisted(() => ({
  Open: vi.fn<(path: string) => Promise<unknown>>(),
  Recent: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/window", () => ({
  WindowService: windowService,
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: workspaceService,
}));

import { COLLECTION_CODES, COLLECTION_EDITED_EVENT, COLLECTION_SAVED_EVENT, type RequestDTO } from "../lib/collection";
import { WINDOW_OPEN_PROJECT_EVENT } from "../lib/windows";
import { createStores, selectConflictSource, selectIsDirty, selectRequestSaveStatus, type Stores, type WorkspaceInfo } from "../stores";
import { emit, resetFakeWails, settle } from "../test/fakeWails";
import { editRequest, followAutoSave, loadRequestDraft } from "./autosave";
import { followExternalChanges, keepMine, takeTheirs } from "./externalChanges";
import { followWindow, fromOtherWindow, newWindow, openInNewWindow, ownWindow, setOwnWindow } from "./windows";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

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

/** A Wails RuntimeError as the runtime rejects with. */
function runtimeError(cause: unknown): Error {
  const err = new Error("go error text");
  err.name = "RuntimeError";
  (err as Error & { cause: unknown }).cause = cause;
  return err;
}

/** Go's side: the request as the service holds it, its version, and the window a call comes from. */
const go = { content: request(), version: 0, caller: 0, conflict: false };

/** Runs `action` as window `id`: Go sees its calls as that window's, until everything has settled. */
async function asWindow(id: number, action: () => void | Promise<void>): Promise<void> {
  go.caller = id;
  await action();
  await settle();
  await settle();
}

const stops: (() => void)[] = [];

/** The stores of window `id`, with the project open and Go's events followed. */
async function openWindow(id: number, follow = true): Promise<Stores> {
  const stores = createStores();
  setOwnWindow(stores, id);
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  if (follow) stops.push(followExternalChanges(stores), followAutoSave(stores));
  await asWindow(id, () => loadRequestDraft(stores, "req_1"));
  return stores;
}

const draftOf = (stores: Stores) => stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO;

beforeEach(() => {
  resetFakeWails();
  go.content = request();
  go.version = 0;
  go.caller = 0;
  go.conflict = false;
  service.Tree.mockReset().mockResolvedValue({ workspaceId: "ws_1", path: "/p/shop", readOnly: "", nodes: [], problems: [] });
  service.Request.mockReset().mockImplementation(() => Promise.resolve({ ...go.content, version: go.version }));
  service.UpdateRequest.mockReset().mockImplementation(({ request: req, baseVersion }) => {
    if (go.conflict || (baseVersion !== undefined && baseVersion !== go.version)) {
      return Promise.reject(
        runtimeError({
          kind: "io",
          code: COLLECTION_CODES.editedElsewhere,
          message: "This item was changed in another PostSonde window after this window loaded it.",
          hint: "Keep your version or take the other window's first.",
          details: { id: "req_1", version: String(go.version) },
          causes: [],
        }),
      );
    }
    go.content = req as unknown as RequestDTO;
    go.version++;
    emit(COLLECTION_EDITED_EVENT, { workspaceId: "ws_1", ids: ["req_1"], window: go.caller });
    return Promise.resolve({ version: go.version });
  });
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.FlushAll.mockReset().mockResolvedValue(undefined);
  service.SetWatching.mockReset().mockResolvedValue(undefined);
  service.ResolveConflict.mockReset().mockResolvedValue(undefined);
  windowService.Current.mockReset().mockResolvedValue({ id: 1, openPath: "" });
  windowService.NewWindow.mockReset().mockResolvedValue(undefined);
  windowService.OpenInNewWindow.mockReset().mockResolvedValue(undefined);
  workspaceService.Open.mockReset().mockImplementation((path) =>
    Promise.resolve({ ...shop, path, openedAt: "2026-09-22T09:00:00Z", uiState: {} }),
  );
  workspaceService.Recent.mockReset().mockResolvedValue([]);
});

afterEach(() => {
  for (const stop of stops.splice(0)) stop();
});

describe("window IDs", () => {
  it("tells another window's events from this window's", () => {
    const stores = createStores();
    // Before Go has told the ID, every event counts as this window's.
    expect(fromOtherWindow(stores, 2)).toBe(false);
    setOwnWindow(stores, 1);
    expect(ownWindow(stores)).toBe(1);
    expect(fromOtherWindow(stores, 2)).toBe(true);
    expect(fromOtherWindow(stores, 1)).toBe(false);
    expect(fromOtherWindow(stores, 0)).toBe(false);
    expect(fromOtherWindow(stores, undefined)).toBe(false);
  });

  it("opens new windows through Go and passes its refusal on", async () => {
    await newWindow();
    expect(windowService.NewWindow).toHaveBeenCalledTimes(1);
    windowService.OpenInNewWindow.mockRejectedValueOnce(
      runtimeError({ kind: "invalid_argument", code: "window.project_differs", message: "Every PostSonde window shows the same project, and another project is open.", hint: "", details: { path: "/p/billing" }, causes: [] }),
    );
    await expect(openInNewWindow("/p/billing")).rejects.toMatchObject({ code: "window.project_differs" });
  });
});

describe("followWindow", () => {
  it("learns the window's ID and opens the project it was opened for", async () => {
    windowService.Current.mockResolvedValue({ id: 3, openPath: "/p/billing" });
    const stores = createStores();
    const onError = vi.fn();
    stops.push(followWindow(stores, onError));
    await settle();
    await settle();
    expect(ownWindow(stores)).toBe(3);
    expect(workspaceService.Open).toHaveBeenCalledWith("/p/billing");
    expect(stores.workspace.getState().current?.path).toBe("/p/billing");
    expect(onError).not.toHaveBeenCalled();
  });

  it("opens the projects Go asks this window for, not another window's, and reports failures", async () => {
    const stores = createStores();
    const onError = vi.fn();
    stops.push(followWindow(stores, onError));
    await settle();
    emit(WINDOW_OPEN_PROJECT_EVENT, { window: 2, path: "/p/other" });
    await settle();
    expect(workspaceService.Open).not.toHaveBeenCalled();

    workspaceService.Open.mockRejectedValueOnce(
      runtimeError({ kind: "invalid_argument", code: "workspace.shared_by_windows", message: "Every PostSonde window shows the same project, so it cannot change while other windows are open.", hint: "", details: {}, causes: [] }),
    );
    emit(WINDOW_OPEN_PROJECT_EVENT, { window: 1, path: "/p/billing" });
    await settle();
    await settle();
    expect(workspaceService.Open).toHaveBeenCalledWith("/p/billing");
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ code: "workspace.shared_by_windows" }));
  });
});

describe("two windows editing one request", () => {
  it("keeps the other window's draft in step with every edit", async () => {
    const one = await openWindow(1);
    const two = await openWindow(2);

    await asWindow(1, () => editRequest(one, "req_1", { url: "/health/live" }));
    expect(go.content.url).toBe("/health/live");
    expect(draftOf(two).url).toBe("/health/live");
    expect(selectIsDirty(two.drafts.getState(), "req_1")).toBe(false);
    expect(draftOf(two)).not.toHaveProperty("version");

    // Window 2 edits on top of window 1's edit: it is taken, and window 1 follows.
    await asWindow(2, () => editRequest(two, "req_1", { method: "HEAD" }));
    expect(go.content).toMatchObject({ url: "/health/live", method: "HEAD" });
    expect(draftOf(one)).toMatchObject({ url: "/health/live", method: "HEAD" });
    expect(service.UpdateRequest.mock.calls.map(([input]) => input.baseVersion)).toEqual([0, 1]);
  });

  it("refuses an edit made before the other window's reached it, and shows the conflict instead of losing either", async () => {
    const one = await openWindow(1);
    // Window 2 has not heard of window 1's edit yet (its events are late).
    const two = await openWindow(2, false);

    await asWindow(1, () => editRequest(one, "req_1", { url: "/health/live" }));
    await asWindow(2, () => editRequest(two, "req_1", { method: "HEAD" }));

    // Go still holds window 1's edit; window 2's is held in its draft, in conflict.
    expect(go.content).toMatchObject({ url: "/health/live", method: "GET" });
    expect(go.version).toBe(1);
    expect(draftOf(two)).toMatchObject({ url: "/health", method: "HEAD" });
    expect(selectConflictSource(two.drafts.getState(), "req_1")).toBe("window");
    expect(selectRequestSaveStatus(two.autosave.getState(), "req_1")).not.toBe("error");

    // Held: further edits are not sent until the user chooses.
    const calls = service.UpdateRequest.mock.calls.length;
    await asWindow(2, () => editRequest(two, "req_1", { name: "Health check" }));
    expect(service.UpdateRequest.mock.calls.length).toBe(calls);

    // Take theirs: window 1's version, and the next edit is one of it.
    await asWindow(2, () => takeTheirs(two, "req_1"));
    expect(draftOf(two)).toMatchObject({ url: "/health/live", method: "GET", name: "Health" });
    expect(selectConflictSource(two.drafts.getState(), "req_1")).toBeNull();
    await asWindow(2, () => editRequest(two, "req_1", { method: "PUT" }));
    expect(go.content).toMatchObject({ url: "/health/live", method: "PUT" });
  });

  it("keeps mine: written over the other window's version on purpose", async () => {
    const one = await openWindow(1);
    const two = await openWindow(2, false);
    await asWindow(1, () => editRequest(one, "req_1", { url: "/health/live" }));
    await asWindow(2, () => editRequest(two, "req_1", { method: "HEAD" }));
    expect(selectConflictSource(two.drafts.getState(), "req_1")).toBe("window");

    await asWindow(2, () => keepMine(two, "req_1"));
    expect(go.content).toMatchObject({ url: "/health", method: "HEAD" });
    expect(selectConflictSource(two.drafts.getState(), "req_1")).toBeNull();
  });

  it("does not take another window's save events as its own", async () => {
    const two = await openWindow(2);
    service.UpdateRequest.mockImplementationOnce(() => Promise.resolve({ version: 1 }));
    await asWindow(2, () => editRequest(two, "req_1", { method: "HEAD" }));
    expect(selectIsDirty(two.drafts.getState(), "req_1")).toBe(true);

    // Window 1's save, with a revision number window 2 also used.
    emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, window: 1, savedAt: "2026-09-22T10:00:00Z" });
    expect(selectIsDirty(two.drafts.getState(), "req_1")).toBe(true);
    emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, window: 2, savedAt: "2026-09-22T10:00:00Z" });
    expect(selectIsDirty(two.drafts.getState(), "req_1")).toBe(false);
  });
});
