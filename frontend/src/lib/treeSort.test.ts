import { afterEach, describe, expect, it } from "vitest";
import { decodeTreeSort, encodeTreeSort, TREE_SORT_UI_STATE_KEY, workspaceLocalTreeSort } from "./treeSort";
import { resetWorkspaceLocal, seedWorkspaceLocal, setWorkspaceLocalWriter, workspaceLocalIdle } from "./workspaceLocal";

describe("tree sort persistence (COL-05)", () => {
  afterEach(() => resetWorkspaceLocal());

  it("encodes the folders sorted by name, the top level apart", () => {
    expect(encodeTreeSort(["", "fld_a", "fld_b"])).toEqual({ topLevel: "name", folders: { fld_a: "name", fld_b: "name" } });
    expect(encodeTreeSort([])).toEqual({});
    expect(decodeTreeSort({ topLevel: "name", folders: { fld_a: "name", fld_b: "manual", "": "name" } })).toEqual(["", "fld_a"]);
  });

  it("ignores what it cannot read", () => {
    expect(decodeTreeSort(null)).toBeNull();
    expect(decodeTreeSort(["fld_a"])).toBeNull();
    expect(decodeTreeSort({ topLevel: "manual", folders: ["x"] })).toEqual([]);
  });

  it("is saved in the workspace's local UI state under tree.sort", async () => {
    const writes: [string, string][] = [];
    const previous = setWorkspaceLocalWriter(async (_id, _path, key, value) => {
      writes.push([key, value]);
    });
    try {
      seedWorkspaceLocal({ id: "ws_1", path: "/p", uiState: { [TREE_SORT_UI_STATE_KEY]: '{"folders":{"fld_x":"name"}}' } });
      const store = workspaceLocalTreeSort();
      expect(store.load("ws_1")).toEqual(["fld_x"]);
      store.save("ws_1", ["", "fld_y"]);
      await workspaceLocalIdle();
      expect(writes).toEqual([[TREE_SORT_UI_STATE_KEY, '{"topLevel":"name","folders":{"fld_y":"name"}}']]);
      expect(store.load("ws_1")).toEqual(["", "fld_y"]);
      expect(store.load("ws_2")).toBeNull();
    } finally {
      setWorkspaceLocalWriter(previous);
    }
  });
});
