import { describe, expect, it } from "vitest";
import { END } from "../../lib/collection";
import { collectionReducer, initialCollectionState, type CollectionState, type TreeNode } from "../../stores";
import {
  cannotMoveReason,
  dropAtEnd,
  dropPositionAt,
  dropTarget,
  isNoOpMove,
  isSortedByName,
  moveIntoError,
  movedPath,
  stepTarget,
} from "./move";

const folder = (id: string, parentId: string | null, childIds: string[], readOnly = ""): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name: id,
  path: parentId === null ? id : `${parentId}/${id}`,
  fileKind: "folder",
  readOnly,
  childIds,
});
const request = (id: string, parentId: string | null, readOnly = ""): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name: id,
  path: parentId === null ? `${id}.json` : `${parentId}/${id}.json`,
  fileKind: "http",
  readOnly,
  method: "GET",
});

// a/ { a1, b/ { b1 } , link (symlink) }, locked/ (newer_version) { l1 }, r1, r2
function state(readOnly = ""): CollectionState {
  return collectionReducer(initialCollectionState, {
    type: "collection/load",
    workspaceId: "ws",
    readOnly,
    rootIds: ["a", "locked", "r1", "r2"],
    nodes: [
      folder("a", null, ["a1", "b", "link"]),
      request("a1", "a"),
      folder("b", "a", ["b1"]),
      request("b1", "b"),
      request("link", "a", "symlink"),
      folder("locked", null, ["l1"], "newer_version"),
      request("l1", "locked"),
      request("r1", null),
      request("r2", null),
    ],
  });
}

describe("what can move where", () => {
  it("refuses a folder into itself or one of its folders", () => {
    const s = state();
    expect(moveIntoError(s, "a", "a")).toMatch(/itself/);
    expect(moveIntoError(s, "a", "b")).toMatch(/itself/);
    expect(moveIntoError(s, "b", "a")).toBeNull();
    expect(moveIntoError(s, "b", null)).toBeNull();
  });

  it("refuses read-only targets and sources, and a read-only project", () => {
    const s = state();
    expect(moveIntoError(s, "r1", "locked")).toBe("That folder is read-only.");
    expect(cannotMoveReason(s, "l1")).toMatch(/folder is read-only/);
    // The read-only folder itself can move.
    expect(moveIntoError(s, "locked", "a")).toBeNull();
    expect(cannotMoveReason(state("newer_version"), "r1")).toBe("This project is read-only.");
  });

  it("only reorders a symbolic link in its folder, and only puts items into folders", () => {
    const s = state();
    expect(moveIntoError(s, "link", "a")).toBeNull();
    expect(moveIntoError(s, "link", null)).toMatch(/symbolic link/);
    expect(moveIntoError(s, "r1", "r2")).toMatch(/only go into folders/);
  });
});

describe("stepTarget", () => {
  it("moves one place within the folder, nothing at the edges", () => {
    const s = state();
    expect(stepTarget(s, "b", -1)).toEqual({ parentId: "a", index: 0 });
    expect(stepTarget(s, "b", 1)).toEqual({ parentId: "a", index: 2 });
    expect(stepTarget(s, "a1", -1)).toBeNull();
    expect(stepTarget(s, "link", 1)).toBeNull();
    expect(stepTarget(s, "r2", -1)).toEqual({ parentId: null, index: 2 });
    expect(stepTarget(s, "l1", -1)).toBeNull();
  });
});

describe("drops", () => {
  it("splits rows into before, into and after", () => {
    expect(dropPositionAt("folder", 0.1)).toBe("before");
    expect(dropPositionAt("folder", 0.5)).toBe("into");
    expect(dropPositionAt("folder", 0.9)).toBe("after");
    expect(dropPositionAt("request", 0.4)).toBe("before");
    expect(dropPositionAt("request", 0.6)).toBe("after");
  });

  it("maps a drop to a folder and an index not counting the item", () => {
    const s = state();
    // r2 before a: top level, first.
    expect(dropTarget(s, "r2", "a", "before", true)).toEqual({ parentId: null, index: 0 });
    // r1 after r2: last of the top level (index counts without r1).
    expect(dropTarget(s, "r1", "r2", "after", false)).toEqual({ parentId: null, index: 3 });
    // r1 into a: last in a.
    expect(dropTarget(s, "r1", "a", "into", true)).toEqual({ parentId: "a", index: END });
    // After an expanded folder with items: before its first item.
    expect(dropTarget(s, "r1", "a", "after", true)).toEqual({ parentId: "a", index: 0 });
    // After a collapsed one: next to it.
    expect(dropTarget(s, "r2", "a", "after", false)).toEqual({ parentId: null, index: 1 });
    // Into another folder's item list.
    expect(dropTarget(s, "r1", "b1", "before", false)).toEqual({ parentId: "b", index: 0 });
  });

  it("refuses invalid drops and drops that change nothing", () => {
    const s = state();
    expect(dropTarget(s, "a", "b", "into", false)).toBeNull();
    expect(dropTarget(s, "a", "b1", "after", false)).toBeNull();
    expect(dropTarget(s, "r1", "locked", "into", false)).toBeNull();
    expect(dropTarget(s, "r1", "l1", "before", false)).toBeNull();
    expect(dropTarget(s, "r1", "r1", "after", false)).toBeNull();
    expect(dropTarget(s, "r1", "r2", "before", false)).toBeNull();
    expect(dropTarget(s, "r1", "locked", "after", false)).toBeNull();
    expect(dropTarget(s, "b", "a", "into", true)).toEqual({ parentId: "a", index: END });
    expect(dropTarget(s, "link", "a", "into", true)).toBeNull();
    expect(dropTarget(s, "a1", "a", "into", true)).toEqual({ parentId: "a", index: END });
    expect(dropTarget(s, "link", "r1", "before", false)).toBeNull();
  });

  it("drops on the empty area at the end of the top level", () => {
    const s = state();
    expect(dropAtEnd(s, "b1")).toEqual({ parentId: null, index: END });
    expect(dropAtEnd(s, "r2")).toBeNull();
    expect(isNoOpMove(s, "r2", { parentId: null, index: 99 })).toBe(true);
  });
});

describe("folders sorted by name (COL-05)", () => {
  const sorted = (...folders: (string | null)[]) =>
    folders.reduce<CollectionState>(
      (s, parentId) => collectionReducer(s, { type: "collection/setSort", parentId, mode: "name" }),
      state(),
    );

  it("cannot be reordered with Alt+Up/Down", () => {
    const s = sorted("a");
    expect(isSortedByName(s, "a")).toBe(true);
    expect(stepTarget(s, "b", -1)).toBeNull();
    expect(stepTarget(s, "b", 1)).toBeNull();
    // Other folders still can.
    expect(stepTarget(s, "r2", -1)).toEqual({ parentId: null, index: 2 });
  });

  it("take drops at the end of their manual order, and refuse drops from their own items", () => {
    const s = sorted("a", null);
    // Between two items of a: into a (last in its manual order).
    expect(dropTarget(s, "r1", "a1", "before", false)).toEqual({ parentId: "a", index: END });
    expect(dropTarget(s, "r1", "a", "after", true)).toEqual({ parentId: "a", index: END });
    // An item of a dropped anywhere in a changes nothing.
    expect(dropTarget(s, "a1", "b", "after", false)).toBeNull();
    expect(dropTarget(s, "a1", "a", "into", true)).toBeNull();
    // The sorted top level: b1 goes last; r1 stays.
    expect(dropTarget(s, "b1", "r1", "before", false)).toEqual({ parentId: null, index: END });
    expect(dropAtEnd(s, "b1")).toEqual({ parentId: null, index: END });
    expect(dropTarget(s, "r2", "r1", "before", false)).toBeNull();
    expect(dropAtEnd(s, "r1")).toBeNull();
  });
});

describe("movedPath", () => {
  it("keeps the base name", () => {
    expect(movedPath("orders/get-order.json", "")).toBe("get-order.json");
    expect(movedPath("get-order.json", "shop/orders")).toBe("shop/orders/get-order.json");
  });
});
