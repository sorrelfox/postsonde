import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Current: vi.fn<() => Promise<unknown>>(),
  Open: vi.fn<(path: string) => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<() => Promise<unknown>>(),
  Recent: vi.fn<() => Promise<unknown>>(),
  RemoveRecent: vi.fn<(path: string) => Promise<unknown>>(),
  PickFolder: vi.fn<(purpose: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: service,
}));

import type { AppError } from "../lib/errors";
import { WORKSPACE_CHANGED_EVENT, WORKSPACE_CODES, type WorkspaceDTO } from "../lib/workspace";
import { createStores } from "../stores";
import { emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import {
  chooseAndOpenWorkspace,
  chooseProjectFolder,
  closeWorkspace,
  createWorkspaceAt,
  followWorkspaceChanges,
  forgetRecentWorkspace,
  loadWorkspace,
  openWorkspaceAt,
} from "./workspace";

function dto(over: Partial<WorkspaceDTO> = {}): WorkspaceDTO {
  return {
    id: "ws_1",
    name: "Shop API",
    path: "/p/shop",
    localDir: "/data/workspaces/ws_1",
    localStatus: "existing",
    previousPath: "",
    newerFormat: false,
    openedAt: "2026-09-16T09:00:00Z",
    uiState: {},
    ...over,
  };
}

const recentDTO = { id: "ws_1", name: "Shop API", path: "/p/shop", openedAt: "2026-09-16T09:00:00Z", available: true };

const notAProject = {
  kind: "not_found",
  code: WORKSPACE_CODES.notAProject,
  message: "This folder is not a PostSonde project.",
  hint: "Choose the folder that contains postsonde.json, or create a project in this folder.",
  details: { path: "/p/plain" },
  causes: [],
};

/** A Wails RuntimeError as the runtime rejects with. */
function runtimeError(cause: unknown): Error {
  const err = new Error("go error text");
  err.name = "RuntimeError";
  (err as Error & { cause: unknown }).cause = cause;
  return err;
}

beforeEach(() => {
  resetFakeWails();
  service.Current.mockReset().mockResolvedValue({ workspace: null });
  service.Open.mockReset().mockResolvedValue(dto());
  service.Create.mockReset().mockResolvedValue(dto({ localStatus: "new" }));
  service.Close.mockReset().mockResolvedValue(undefined);
  service.Recent.mockReset().mockResolvedValue([recentDTO]);
  service.RemoveRecent.mockReset().mockResolvedValue(undefined);
  service.PickFolder.mockReset().mockResolvedValue("/p/shop");
});

describe("workspace commands", () => {
  it("loads the open workspace and the recent list", async () => {
    const stores = createStores();
    service.Current.mockResolvedValue({ workspace: dto() });
    await loadWorkspace(stores);
    const state = stores.workspace.getState();
    expect(state.status).toBe("ready");
    expect(state.current).toEqual({
      id: "ws_1",
      name: "Shop API",
      path: "/p/shop",
      localDir: "/data/workspaces/ws_1",
      localStatus: "existing",
      previousPath: "",
      newerFormat: false,
    });
    expect(state.recent).toEqual([recentDTO]);
  });

  it("is ready with nothing open when Go cannot be asked", async () => {
    const stores = createStores();
    service.Current.mockRejectedValue(new Error("no backend"));
    service.Recent.mockResolvedValue(null);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await loadWorkspace(stores);
    expect(stores.workspace.getState()).toEqual({ status: "ready", current: null, recent: [] });
    warn.mockRestore();
  });

  it("follows workspace:changed events", async () => {
    const stores = createStores();
    const stop = followWorkspaceChanges(stores);
    emit(WORKSPACE_CHANGED_EVENT, { workspace: dto() });
    expect(stores.workspace.getState().current?.path).toBe("/p/shop");
    await settle();
    expect(stores.workspace.getState().recent).toHaveLength(1);
    emit(WORKSPACE_CHANGED_EVENT, { workspace: null });
    expect(stores.workspace.getState().current).toBeNull();
    stop();
    expect(listenerCount()).toBe(0);
  });

  it("opens a chosen folder, and does nothing when the dialog is cancelled", async () => {
    const stores = createStores();
    expect(await chooseAndOpenWorkspace(stores)).toBe(true);
    expect(service.PickFolder).toHaveBeenCalledWith("open");
    expect(service.Open).toHaveBeenCalledWith("/p/shop");
    expect(stores.workspace.getState().current?.name).toBe("Shop API");

    service.PickFolder.mockResolvedValue("");
    service.Open.mockClear();
    expect(await chooseAndOpenWorkspace(stores)).toBe(false);
    expect(service.Open).not.toHaveBeenCalled();
  });

  it("rejects with an AppError that keeps the code and path", async () => {
    const stores = createStores();
    service.Open.mockRejectedValue(runtimeError(notAProject));
    const err = (await openWorkspaceAt(stores, "/p/plain").catch((e: unknown) => e)) as AppError;
    expect(err.code).toBe(WORKSPACE_CODES.notAProject);
    expect(err.details.path).toBe("/p/plain");
    expect(stores.workspace.getState().current).toBeNull();
  });

  it("creates a project with a trimmed name", async () => {
    const stores = createStores();
    expect(await chooseProjectFolder()).toBe("/p/shop");
    expect(service.PickFolder).toHaveBeenCalledWith("create");
    await createWorkspaceAt(stores, "/p/new", "  New API ");
    expect(service.Create).toHaveBeenCalledWith({ path: "/p/new", name: "New API" });
    expect(stores.workspace.getState().current?.localStatus).toBe("new");
  });

  it("drops the tree and tabs of the previous project when another opens or it closes", async () => {
    const stores = createStores();
    await openWorkspaceAt(stores, "/p/shop");
    const seed = () => {
      stores.collection.dispatch({
        type: "collection/load",
        nodes: [{ kind: "request", id: "r1", parentId: null, name: "Get", method: "GET", path: "get.json", fileKind: "http", readOnly: "" }],
        rootIds: ["r1"],
      });
      stores.tabs.dispatch({ type: "tabs/open", id: "r1" });
    };
    seed();

    // Reopening the same project keeps them.
    await openWorkspaceAt(stores, "/p/shop");
    expect(stores.tabs.getState().order).toEqual(["r1"]);

    service.Open.mockResolvedValue(dto({ id: "ws_2", path: "/p/other", name: "Other" }));
    await openWorkspaceAt(stores, "/p/other");
    expect(stores.tabs.getState().order).toEqual([]);
    expect(stores.collection.getState().rootIds).toEqual([]);

    seed();
    await closeWorkspace(stores);
    expect(stores.workspace.getState().current).toBeNull();
    expect(stores.tabs.getState().order).toEqual([]);
  });

  it("removes a recent entry", async () => {
    const stores = createStores();
    service.Recent.mockResolvedValue([]);
    await forgetRecentWorkspace(stores, "/p/shop");
    expect(service.RemoveRecent).toHaveBeenCalledWith("/p/shop");
    expect(stores.workspace.getState().recent).toEqual([]);
  });
});
