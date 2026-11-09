import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  decodeStringArray,
  localStorageWorkspaceStore,
  resetWorkspaceLocal,
  seedWorkspaceLocal,
  setWorkspaceLocalWriter,
  workspaceLocalIdle,
  workspaceLocalStore,
} from "./workspaceLocal";

type Write = [workspaceId: string, path: string, key: string, value: string];

let writes: Write[];
let previous: ReturnType<typeof setWorkspaceLocalWriter>;

beforeEach(() => {
  resetWorkspaceLocal();
  window.localStorage.clear();
  writes = [];
  previous = setWorkspaceLocalWriter((...args) => {
    writes.push(args);
    return Promise.resolve();
  });
});

afterEach(() => {
  setWorkspaceLocalWriter(previous);
});

const store = () =>
  workspaceLocalStore<string[]>("ids", decodeStringArray, (ids) => ids.slice(0, 2), localStorageWorkspaceStore("legacy.", decodeStringArray));

describe("workspaceLocalStore", () => {
  it("loads what Go sent with the workspace and saves changes back to it, in order", async () => {
    seedWorkspaceLocal({ id: "ws_1", path: "/p/a", uiState: { ids: '["x"]', other: "1" } });
    const s = store();
    expect(s.load("ws_1")).toEqual(["x"]);
    s.save("ws_1", ["y", "z", "dropped"]);
    s.save("ws_1", ["y", "z"]); // the same text: not sent again
    s.save("ws_1", ["w"]);
    expect(s.load("ws_1")).toEqual(["w"]);
    await workspaceLocalIdle();
    expect(writes).toEqual([
      ["ws_1", "/p/a", "ids", '["y","z"]'],
      ["ws_1", "/p/a", "ids", '["w"]'],
    ]);
  });

  it("keeps the newer values when the same workspace is seeded again, and replaces a copy's", () => {
    seedWorkspaceLocal({ id: "ws_1", path: "/p/a", uiState: { ids: '["old"]' } });
    const s = store();
    s.save("ws_1", ["new"]);
    seedWorkspaceLocal({ id: "ws_1", path: "/p/a", uiState: { ids: '["old"]' } });
    expect(s.load("ws_1")).toEqual(["new"]);
    // Same id, another folder: a copy with its own state.
    seedWorkspaceLocal({ id: "ws_1", path: "/p/copy", uiState: { ids: '["copy"]' } });
    expect(s.load("ws_1")).toEqual(["copy"]);
  });

  it("falls back to what older builds kept in localStorage, and ignores broken values", () => {
    window.localStorage.setItem("legacy.ws_1", '["from-local-storage"]');
    const s = store();
    expect(s.load("ws_1")).toEqual(["from-local-storage"]); // not seeded yet
    seedWorkspaceLocal({ id: "ws_1", path: "/p/a", uiState: null });
    expect(s.load("ws_1")).toEqual(["from-local-storage"]);
    seedWorkspaceLocal({ id: "ws_2", path: "/p/b", uiState: { ids: "{not json" } });
    expect(s.load("ws_2")).toBeNull();
  });

  it("drops saves for a workspace that was never opened, and logs failed saves", async () => {
    const s = store();
    s.save("ws_9", ["x"]);
    await workspaceLocalIdle();
    expect(writes).toEqual([]);

    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    setWorkspaceLocalWriter(() => Promise.reject(new Error("disk full")));
    seedWorkspaceLocal({ id: "ws_1", path: "/p/a" });
    s.save("ws_1", ["x"]);
    await workspaceLocalIdle();
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockRestore();
  });
});
