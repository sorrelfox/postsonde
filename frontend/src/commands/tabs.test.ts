import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  localStorageTabs,
  TABS_FORMAT_VERSION,
  TABS_KEY_PREFIX,
  type SavedTabs,
  type TabsPersistence,
} from "../lib/tabsPersistence";
import { createStores, type Stores, type TreeNode } from "../stores";
import {
  activateTab,
  closeAllTabs,
  closeOtherTabs,
  closeTabs,
  closeTabsToTheRight,
  followTabs,
  keepTab,
  onTabClosed,
  openFolder,
  openRequest,
  reopenClosedTab,
  setTabPinned,
  setTabsPersistence,
  TABS_SAVE_DELAY_MS,
} from "./tabs";

const request = (id: string, name = id, parentId: string | null = null): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path: `${id}.json`,
  fileKind: "http",
  readOnly: "",
  method: "GET",
});
const folder = (id: string, childIds: string[]): TreeNode => ({
  kind: "folder",
  id,
  parentId: null,
  name: id,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});

function load(stores: Stores, workspaceId: string, nodes: TreeNode[]): void {
  stores.collection.dispatch({ type: "collection/loading", workspaceId });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId,
    nodes,
    rootIds: nodes.filter((n) => n.parentId === null).map((n) => n.id),
  });
}

const NODES = [folder("dir", ["a", "b"]), request("a", "A", "dir"), request("b", "B", "dir"), request("c"), request("d")];

/** Saved tabs with nothing pinned, no preview and no closed tabs, plus `extra`. */
const saved = (tabs: { order: string[]; activeId: string | null }, extra: Partial<SavedTabs> = {}): SavedTabs => ({
  pinnedIds: [],
  previewId: null,
  closed: [],
  ...tabs,
  ...extra,
});

function memoryTabs(initial: Record<string, SavedTabs> = {}): TabsPersistence & { saved: Record<string, SavedTabs> } {
  const store: Record<string, SavedTabs> = { ...initial };
  return {
    saved: store,
    load: (id) => store[id] ?? null,
    save: (id, tabs) => {
      store[id] = { ...tabs, order: [...tabs.order], pinnedIds: [...tabs.pinnedIds], closed: [...tabs.closed] };
    },
  };
}

let previous: TabsPersistence;

beforeEach(() => {
  previous = setTabsPersistence(memoryTabs());
});

afterEach(() => {
  setTabsPersistence(previous);
  vi.useRealTimers();
});

function loadedStores(nodes: TreeNode[] = NODES): Stores {
  const stores = createStores();
  load(stores, "ws_1", nodes);
  return stores;
}

describe("opening and switching tabs", () => {
  it("opens requests and folders as tabs and selects them in the tree", () => {
    const stores = loadedStores();
    openRequest(stores, "a");
    openFolder(stores, "dir");
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "dir"], activeId: "dir" });
    expect(stores.collection.getState().selectedId).toBe("dir");
    openRequest(stores, "a");
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "dir"], activeId: "a" });
  });

  it("activates only open tabs, selecting the item", () => {
    const stores = loadedStores();
    openRequest(stores, "a");
    openRequest(stores, "b");
    activateTab(stores, "a");
    expect(stores.tabs.getState().activeId).toBe("a");
    expect(stores.collection.getState().selectedId).toBe("a");
    activateTab(stores, "c");
    expect(stores.tabs.getState().activeId).toBe("a");
  });
});

describe("the preview tab", () => {
  it("replaces the preview tab on a single-click open, flushing it like a close", () => {
    const stores = loadedStores();
    openRequest(stores, "c");
    openRequest(stores, "a", { preview: true });
    stores.drafts.dispatch({ type: "drafts/open", id: "a", request: { url: "x" } });
    const seen: Array<{ ids: readonly string[]; hadDraft: boolean }> = [];
    const stop = onTabClosed((ids, s) => seen.push({ ids, hadDraft: "a" in s.drafts.getState().byId }));

    openRequest(stores, "b", { preview: true });
    expect(stores.tabs.getState()).toMatchObject({ order: ["c", "b"], activeId: "b", previewId: "b", closed: [] });
    expect(stores.collection.getState().selectedId).toBe("b");
    expect(seen).toEqual([{ ids: ["a"], hadDraft: true }]);
    expect(stores.drafts.getState().byId).not.toHaveProperty("a");

    // A preview open of a kept tab only activates it.
    openRequest(stores, "c", { preview: true });
    expect(stores.tabs.getState()).toMatchObject({ order: ["c", "b"], activeId: "c", previewId: "b" });
    expect(seen).toHaveLength(1);
    stop();
  });

  it("is kept by a normal open (double-click, Enter), keepTab and pinning", () => {
    const stores = loadedStores();
    openRequest(stores, "a", { preview: true });
    openRequest(stores, "a");
    expect(stores.tabs.getState().previewId).toBeNull();

    openFolder(stores, "dir", { preview: true });
    keepTab(stores, "dir");
    expect(stores.tabs.getState().previewId).toBeNull();

    openRequest(stores, "b", { preview: true });
    setTabPinned(stores, "b", true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["b", "a", "dir"], previewId: null });
  });

  it("is kept by the first edit of its request", () => {
    const stores = loadedStores();
    const stop = followTabs(stores);
    openRequest(stores, "a", { preview: true });
    stores.drafts.dispatch({ type: "drafts/open", id: "a", request: { url: "x" } });
    expect(stores.tabs.getState().previewId).toBe("a");
    stores.drafts.dispatch({ type: "drafts/edit", id: "a", patch: { url: "y" } });
    expect(stores.tabs.getState().previewId).toBeNull();
    // Another request's edit does not keep the preview.
    openRequest(stores, "b", { preview: true });
    stores.drafts.dispatch({ type: "drafts/open", id: "c", request: { url: "x" } });
    stores.drafts.dispatch({ type: "drafts/edit", id: "c", patch: { url: "z" } });
    expect(stores.tabs.getState().previewId).toBe("b");
    stop();
  });
});

describe("pinned tabs", () => {
  it("stay on the left and are not closed by close others, close to the right or close all", () => {
    const stores = loadedStores();
    for (const id of ["a", "b", "c", "d"]) openRequest(stores, id);
    setTabPinned(stores, "c", true);
    setTabPinned(stores, "d", true);
    expect(stores.tabs.getState().order).toEqual(["c", "d", "a", "b"]);

    closeTabsToTheRight(stores, "c");
    expect(stores.tabs.getState().order).toEqual(["c", "d"]);
    openRequest(stores, "a");
    openRequest(stores, "b");
    closeOtherTabs(stores, "a");
    expect(stores.tabs.getState()).toMatchObject({ order: ["c", "d", "a"], activeId: "a" });
    closeAllTabs(stores);
    expect(stores.tabs.getState().order).toEqual(["c", "d"]);
    // An explicit close (menu, Delete, ⌘W) closes a pinned tab.
    closeTabs(stores, ["c"]);
    expect(stores.tabs.getState().order).toEqual(["d"]);
  });
});

describe("reopening closed tabs", () => {
  it("reopens the most recent first, at its position, pinned again, active and selected", () => {
    const stores = loadedStores();
    for (const id of ["a", "b", "c", "d"]) openRequest(stores, id);
    setTabPinned(stores, "d", true); // d a b c
    closeTabs(stores, ["a"]);
    closeOtherTabs(stores, "c"); // closes b; d stays
    closeTabs(stores, ["d"]);
    expect(stores.tabs.getState().order).toEqual(["c"]);

    expect(reopenClosedTab(stores)).toBe(true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["d", "c"], pinnedIds: { d: true }, activeId: "d" });
    expect(stores.collection.getState().selectedId).toBe("d");
    expect(reopenClosedTab(stores)).toBe(true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["d", "b", "c"], activeId: "b" });
    expect(reopenClosedTab(stores)).toBe(true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["d", "a", "b", "c"], activeId: "a" });
    expect(reopenClosedTab(stores)).toBe(false);
  });

  it("skips closed tabs whose item is gone or that are open again", () => {
    const stores = loadedStores();
    for (const id of ["a", "b", "c"]) openRequest(stores, id);
    closeTabs(stores, ["a"]);
    closeTabs(stores, ["b"]);
    closeTabs(stores, ["c"]);
    // c is deleted from the tree; b is open again by other means.
    load(stores, "ws_1", NODES.filter((n) => n.id !== "c"));
    stores.tabs.dispatch({ type: "tabs/open", id: "b" });
    expect(reopenClosedTab(stores)).toBe(true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "b"], activeId: "a", closed: [] });

    stores.tabs.dispatch({ type: "tabs/restore", order: [], activeId: null, closed: [{ id: "gone", index: 0, pinned: false }] });
    expect(reopenClosedTab(stores)).toBe(false);
    expect(stores.tabs.getState().closed).toEqual([]);
  });
});

describe("closing tabs", () => {
  function withTabs(active: string): Stores {
    const stores = loadedStores();
    for (const id of ["a", "b", "c", "d"]) openRequest(stores, id);
    activateTab(stores, active);
    return stores;
  }

  it("activates the right neighbour of a closed active tab, else the left one", () => {
    const stores = withTabs("b");
    closeTabs(stores, ["b"]);
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "c", "d"], activeId: "c" });
    activateTab(stores, "d");
    closeTabs(stores, ["d"]);
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "c"], activeId: "c" });
  });

  it("closes the others and all", () => {
    const stores = withTabs("a");
    closeOtherTabs(stores, "c");
    expect(stores.tabs.getState()).toMatchObject({ order: ["c"], activeId: "c" });
    closeAllTabs(stores);
    expect(stores.tabs.getState()).toMatchObject({ order: [], activeId: null });
  });

  it("drops the drafts, after telling onTabClosed listeners while the drafts are still there", () => {
    const stores = withTabs("a");
    stores.drafts.dispatch({ type: "drafts/open", id: "a", request: { url: "x" } });
    stores.drafts.dispatch({ type: "drafts/open", id: "b", request: { url: "y" } });
    const seen: Array<{ ids: readonly string[]; hadDraft: boolean; open: boolean }> = [];
    const stop = onTabClosed((ids, s) => {
      seen.push({ ids, hadDraft: "a" in s.drafts.getState().byId, open: s.tabs.getState().order.includes("a") });
    });
    const failing = onTabClosed(() => {
      throw new Error("boom");
    });
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    closeTabs(stores, ["a", "x", "a"]);
    expect(seen).toEqual([{ ids: ["a"], hadDraft: true, open: true }]);
    expect(stores.drafts.getState().byId).not.toHaveProperty("a");
    expect(stores.drafts.getState().byId).toHaveProperty("b");
    expect(warn).toHaveBeenCalled();

    closeTabs(stores, ["x"]); // nothing open: no call
    expect(seen).toHaveLength(1);
    stop();
    failing();
    closeTabs(stores, ["b"]);
    expect(seen).toHaveLength(1);
    warn.mockRestore();
  });
});

describe("followTabs", () => {
  it("restores the saved tabs of the workspace once its tree is loaded, dropping stale ids", () => {
    const persistence = memoryTabs({
      ws_1: saved({ order: ["gone", "b", "dir"], activeId: "dir" }),
      ws_2: saved({ order: ["c"], activeId: "c" }),
    });
    setTabsPersistence(persistence);
    const stores = createStores();
    const stop = followTabs(stores);
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    expect(stores.tabs.getState().order).toEqual([]);
    load(stores, "ws_1", NODES);
    expect(stores.tabs.getState()).toMatchObject({ order: ["b", "dir"], activeId: "dir" });
    stop();
  });

  it("falls back to the first tab when the saved active tab is gone", () => {
    setTabsPersistence(memoryTabs({ ws_1: saved({ order: ["gone", "c"], activeId: "gone" }) }));
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    expect(stores.tabs.getState()).toMatchObject({ order: ["c"], activeId: "c" });
    stop();
  });

  it("saves open tabs and the active tab per workspace, debounced, and flushes on stop", () => {
    vi.useFakeTimers();
    const persistence = memoryTabs();
    setTabsPersistence(persistence);
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    openRequest(stores, "a");
    openFolder(stores, "dir");
    expect(persistence.saved).toEqual({});
    vi.advanceTimersByTime(TABS_SAVE_DELAY_MS);
    expect(persistence.saved.ws_1).toEqual(saved({ order: ["a", "dir"], activeId: "dir" }));

    activateTab(stores, "a");
    stop();
    expect(persistence.saved.ws_1).toEqual(saved({ order: ["a", "dir"], activeId: "a" }));
  });

  it("does not save before the tree is loaded, and keeps the saved tabs when the project closes", () => {
    vi.useFakeTimers();
    const persistence = memoryTabs({ ws_1: saved({ order: ["c", "d"], activeId: "d" }) });
    setTabsPersistence(persistence);
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    closeTabs(stores, ["c"]);
    // Closing the project (commands/workspace.ts): the tree is cleared, then the tabs are closed.
    stores.collection.dispatch({ type: "collection/clear" });
    expect(persistence.saved.ws_1).toMatchObject({ order: ["d"], activeId: "d" });
    closeTabs(stores, stores.tabs.getState().order);
    vi.advanceTimersByTime(TABS_SAVE_DELAY_MS * 2);
    expect(persistence.saved.ws_1).toMatchObject({ order: ["d"], activeId: "d" });

    // Reopening it restores them.
    load(stores, "ws_1", NODES);
    expect(stores.tabs.getState()).toMatchObject({ order: ["d"], activeId: "d" });
    stop();
  });

  it("restores the tabs of another workspace when it opens", () => {
    const persistence = memoryTabs({ ws_2: saved({ order: ["x"], activeId: "x" }) });
    setTabsPersistence(persistence);
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    openRequest(stores, "a");
    stores.collection.dispatch({ type: "collection/clear" });
    closeTabs(stores, ["a"]);
    load(stores, "ws_2", [request("x")]);
    expect(stores.tabs.getState()).toMatchObject({ order: ["x"], activeId: "x" });
    expect(persistence.saved.ws_1).toEqual(saved({ order: ["a"], activeId: "a" }));
    stop();
  });

  it("restores pinned, preview and closed tabs after a restart", () => {
    vi.useFakeTimers();
    const persistence = memoryTabs();
    setTabsPersistence(persistence);
    let stores = createStores();
    let stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    openRequest(stores, "a");
    openRequest(stores, "b");
    openRequest(stores, "dir");
    setTabPinned(stores, "dir", true);
    closeTabs(stores, ["a"]);
    openRequest(stores, "c", { preview: true });
    vi.advanceTimersByTime(TABS_SAVE_DELAY_MS);
    expect(persistence.saved.ws_1).toEqual({
      order: ["dir", "b", "c"],
      activeId: "c",
      pinnedIds: ["dir"],
      previewId: "c",
      closed: [{ id: "a", index: 1, pinned: false }],
    });
    stop();

    stores = createStores();
    stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    expect(stores.tabs.getState()).toEqual({
      order: ["dir", "b", "c"],
      pinnedIds: { dir: true },
      activeId: "c",
      previewId: "c",
      closed: [{ id: "a", index: 1, pinned: false }],
    });
    expect(reopenClosedTab(stores)).toBe(true);
    expect(stores.tabs.getState().order).toEqual(["dir", "a", "b", "c"]);
    stop();
  });

  it("forgets the closed tabs of another project", () => {
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    openRequest(stores, "a");
    stores.collection.dispatch({ type: "collection/clear" });
    closeTabs(stores, ["a"]);
    expect(stores.tabs.getState().closed).toHaveLength(1);
    load(stores, "ws_2", NODES);
    expect(stores.tabs.getState().closed).toEqual([]);
    stop();
  });

  it("closes tabs whose item is gone when the tree reloads, and keeps the rest", () => {
    const stores = createStores();
    const stop = followTabs(stores);
    load(stores, "ws_1", NODES);
    openRequest(stores, "a");
    openRequest(stores, "c");
    const closed: string[] = [];
    const stopListening = onTabClosed((ids) => closed.push(...ids));
    stores.collection.dispatch({ type: "collection/select", id: "a" }); // no node change: nothing
    load(stores, "ws_1", [request("a", "A renamed"), request("d")]);
    expect(stores.tabs.getState()).toMatchObject({ order: ["a"], activeId: "a" });
    expect(closed).toEqual(["c"]);
    // A deleted item's tab is not offered for reopening.
    expect(stores.tabs.getState().closed).toEqual([]);
    expect(stores.collection.getState().nodes.a?.name).toBe("A renamed");
    stopListening();
    stop();
  });
});

describe("localStorageTabs", () => {
  it("saves per workspace id and reads back only valid data", () => {
    window.localStorage.clear();
    const persistence = localStorageTabs();
    expect(persistence.load("ws_1")).toBeNull();
    persistence.save("ws_1", saved({ order: ["a", "b"], activeId: "b" }));
    expect(persistence.load("ws_1")).toEqual(saved({ order: ["a", "b"], activeId: "b" }));
    expect(persistence.load("ws_2")).toBeNull();

    const put = (id: string, value: string) => window.localStorage.setItem(TABS_KEY_PREFIX + id, value);
    put("ws_3", "{not json");
    expect(persistence.load("ws_3")).toBeNull();
    put("ws_4", JSON.stringify({ order: ["a", 3, "a", "b"], activeId: "z" }));
    expect(persistence.load("ws_4")).toEqual(saved({ order: ["a", "b"], activeId: null }));
    put("ws_5", JSON.stringify(["a"]));
    expect(persistence.load("ws_5")).toBeNull();

    // Version 2: pinned, preview and closed tabs; broken members are dropped.
    const full = saved(
      { order: ["p", "a", "b"], activeId: "a" },
      { pinnedIds: ["p"], previewId: "b", closed: [{ id: "x", index: 2, pinned: true }] },
    );
    persistence.save("ws_6", full);
    expect(JSON.parse(window.localStorage.getItem(TABS_KEY_PREFIX + "ws_6") ?? "null")).toMatchObject({ version: TABS_FORMAT_VERSION });
    expect(persistence.load("ws_6")).toEqual(full);
    put(
      "ws_7",
      JSON.stringify({
        version: 2,
        order: ["a", "b"],
        activeId: "a",
        pinnedIds: ["b", "gone", 1],
        previewId: "b",
        closed: [{ id: "x", index: 0 }, { id: "y", index: -1 }, { index: 1 }, "z", { id: "w", index: 1.5 }],
      }),
    );
    expect(persistence.load("ws_7")).toEqual(
      saved({ order: ["a", "b"], activeId: "a" }, { pinnedIds: ["b"], closed: [{ id: "x", index: 0, pinned: false }] }),
    );
    // A newer format is not read.
    put("ws_8", JSON.stringify({ version: 3, order: ["a"], activeId: "a" }));
    expect(persistence.load("ws_8")).toBeNull();

    const broken = localStorageTabs(() => {
      throw new Error("no storage");
    });
    expect(broken.load("ws_1")).toBeNull();
    expect(() => broken.save("ws_1", saved({ order: [], activeId: null }))).not.toThrow();
    window.localStorage.clear();
  });
});
