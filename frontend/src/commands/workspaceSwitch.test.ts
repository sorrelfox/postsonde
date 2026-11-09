// Switching between projects (STO-06): each project's tabs, active tab,
// pinned tabs and expanded folders are saved in its local UI state (a fake
// of Go here) before it is left, and come back when it is opened again.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/** A fake of the Go side: two projects, their trees and their saved UI state. */
const go = vi.hoisted(() => {
  const trees: Record<string, { id: string; nodes: string[]; folders: string[] }> = {
    "/p/a": { id: "ws_a", nodes: ["a1", "a2", "a3"], folders: ["fa"] },
    "/p/b": { id: "ws_b", nodes: ["b1", "b2"], folders: ["fb"] },
  };
  const uiState: Record<string, Record<string, string>> = { "/p/a": {}, "/p/b": {} };
  const calls: string[] = [];
  let current: string | null = null;
  const dto = (path: string) => ({
    id: trees[path]!.id,
    name: path.slice(3).toUpperCase(),
    path,
    localDir: `/data/${trees[path]!.id}`,
    localStatus: "existing",
    previousPath: "",
    newerFormat: false,
    openedAt: "2026-09-18T09:00:00Z",
    uiState: { ...uiState[path] },
  });
  return {
    trees,
    uiState,
    calls,
    current: () => current,
    workspace: {
      Current: () => Promise.resolve({ workspace: current === null ? null : dto(current) }),
      Open: (path: string) => {
        calls.push(`Open ${path}`);
        current = path;
        return Promise.resolve(dto(path));
      },
      Close: () => {
        calls.push("Close");
        current = null;
        return Promise.resolve();
      },
      Recent: () => Promise.resolve([]),
      SaveUIState: (input: { workspaceId: string; path: string; key: string; value: string }) => {
        calls.push(`SaveUIState ${input.path} ${input.key}`);
        if (trees[input.path]?.id !== input.workspaceId) return Promise.reject(new Error("unknown workspace"));
        uiState[input.path]![input.key] = input.value;
        return Promise.resolve();
      },
    },
    collection: {
      Tree: () => {
        const t = trees[current!]!;
        const folder = t.folders[0]!;
        return Promise.resolve({
          workspaceId: t.id,
          path: current,
          readOnly: "",
          rootIds: [folder],
          problems: [],
          nodes: [
            { id: folder, parentId: null, kind: "folder", name: folder, path: folder, fileKind: "folder", method: "", childIds: t.nodes, readOnly: "" },
            ...t.nodes.map((id) => ({ id, parentId: folder, kind: "request", name: id, path: `${folder}/${id}.json`, fileKind: "http", method: "GET", childIds: [], readOnly: "" })),
          ],
        });
      },
      FlushAll: () => {
        calls.push("FlushAll");
        return Promise.resolve();
      },
    },
    reset() {
      current = null;
      calls.length = 0;
      uiState["/p/a"] = {};
      uiState["/p/b"] = {};
    },
  };
});

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: go.workspace,
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: go.collection,
}));

import { resetWorkspaceLocal } from "../lib/workspaceLocal";
import { createStores, type Stores } from "../stores";
import { resetFakeWails, settle } from "../test/fakeWails";
import { followAutoSave } from "./autosave";
import { followCollection } from "./collection";
import { activateTab, followTabs, openRequest, setTabPinned } from "./tabs";
import { closeWorkspace, loadWorkspace, openWorkspaceAt } from "./workspace";

let stops: (() => void)[] = [];

function start(): Stores {
  const stores = createStores();
  stops = [followCollection(stores), followTabs(stores), followAutoSave(stores)];
  return stores;
}

async function switchTo(stores: Stores, path: string): Promise<void> {
  await openWorkspaceAt(stores, path);
  await settle();
}

beforeEach(() => {
  resetFakeWails();
  resetWorkspaceLocal();
  go.reset();
});

afterEach(() => {
  for (const stop of stops) stop();
  stops = [];
});

describe("switching projects", () => {
  it("restores each project's tabs, active tab, pinned tabs and expanded folders", async () => {
    const stores = start();
    await loadWorkspace(stores);
    await switchTo(stores, "/p/a");
    expect(stores.collection.getState()).toMatchObject({ status: "ready", workspaceId: "ws_a" });

    openRequest(stores, "a1");
    openRequest(stores, "a2");
    openRequest(stores, "a3");
    setTabPinned(stores, "a3", true);
    activateTab(stores, "a2");
    stores.collection.dispatch({ type: "collection/setExpanded", id: "fa", expanded: false });

    await switchTo(stores, "/p/b");
    // A's state went to its local UI state before Go opened B.
    const openB = go.calls.indexOf("Open /p/b");
    expect(go.calls.slice(0, openB)).toEqual(
      expect.arrayContaining(["FlushAll", "SaveUIState /p/a tabs", "SaveUIState /p/a tree.expanded"]),
    );
    expect(JSON.parse(go.uiState["/p/a"]!.tabs!)).toMatchObject({ order: ["a3", "a1", "a2"], activeId: "a2", pinnedIds: ["a3"] });
    expect(stores.tabs.getState().order).toEqual([]);
    expect(stores.collection.getState().workspaceId).toBe("ws_b");
    expect(stores.collection.getState().expandedIds).toEqual({ fb: true });

    openRequest(stores, "b2");
    openRequest(stores, "b1");
    activateTab(stores, "b2");

    await switchTo(stores, "/p/a");
    expect(stores.tabs.getState()).toMatchObject({ order: ["a3", "a1", "a2"], activeId: "a2", pinnedIds: { a3: true } });
    expect(stores.collection.getState().expandedIds).toEqual({});

    await switchTo(stores, "/p/b");
    expect(stores.tabs.getState()).toMatchObject({ order: ["b2", "b1"], activeId: "b2", pinnedIds: {} });
    expect(stores.collection.getState().expandedIds).toEqual({ fb: true });
  });

  it("restores the tabs after a restart, from the state Go sends with the workspace", async () => {
    const first = start();
    await loadWorkspace(first);
    await switchTo(first, "/p/a");
    openRequest(first, "a2");
    openRequest(first, "a1");
    await closeWorkspace(first);
    await settle();
    expect(go.calls.indexOf("SaveUIState /p/a tabs")).toBeLessThan(go.calls.indexOf("Close"));
    for (const stop of stops) stop();

    // A new window: nothing in memory, only what Go kept.
    resetWorkspaceLocal();
    const second = start();
    await switchTo(second, "/p/a");
    expect(second.tabs.getState()).toMatchObject({ order: ["a2", "a1"], activeId: "a1" });
  });
});
