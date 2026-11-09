import { describe, expect, it } from "vitest";
import type { RunItemDTO } from "./runner";
import {
  EMPTY_RUN_SELECTION,
  isDefaultRunSelection,
  mergeRunOrder,
  moveRunItem,
  moveRunItemTo,
  orderedRunItems,
  selectedRunIds,
} from "./runSelection";

const item = (id: string, skip = ""): RunItemDTO => ({ position: 0, requestId: id, name: id, kind: "http", method: "GET", path: [], skip });

describe("mergeRunOrder", () => {
  it("keeps the order chosen and puts new requests at their tree place", () => {
    expect(mergeRunOrder(["a", "b", "c"], [])).toEqual(["a", "b", "c"]);
    const order = ["c", "a", "b"];
    expect(mergeRunOrder(["a", "b", "c"], order)).toBe(order);
    // "x" is after "a" in the tree: it goes right after "a" in the run.
    expect(mergeRunOrder(["a", "x", "b", "c"], order)).toEqual(["c", "a", "x", "b"]);
    // A new first request goes first; a removed one drops out.
    expect(mergeRunOrder(["y", "a", "c"], order)).toEqual(["y", "c", "a"]);
  });
});

describe("moving", () => {
  it("moves one place, or before or after another request", () => {
    const order = ["a", "b", "c"];
    expect(moveRunItem(order, "a", 1)).toEqual(["b", "a", "c"]);
    expect(moveRunItem(order, "c", -1)).toEqual(["a", "c", "b"]);
    expect(moveRunItem(order, "a", -1)).toBe(order);
    expect(moveRunItem(order, "c", 1)).toBe(order);
    expect(moveRunItem(order, "gone", 1)).toBe(order);
    expect(moveRunItemTo(order, "a", "c", true)).toEqual(["b", "c", "a"]);
    expect(moveRunItemTo(order, "c", "a", false)).toEqual(["c", "a", "b"]);
    expect(moveRunItemTo(order, "a", "b", false)).toBe(order);
    expect(moveRunItemTo(order, "a", "a", true)).toBe(order);
  });
});

describe("the requests a run sends", () => {
  const plan = [item("a"), item("ws", "kind"), item("b"), item("c")];

  it("are the chosen ones that can be sent, in the order of the run", () => {
    expect(selectedRunIds(plan, EMPTY_RUN_SELECTION)).toEqual(["a", "b", "c"]);
    const selection = { order: ["c", "ws", "a", "b"], excluded: { b: true as const } };
    expect(orderedRunItems(plan, selection.order).map((it) => it.requestId)).toEqual(["c", "ws", "a", "b"]);
    expect(selectedRunIds(plan, selection)).toEqual(["c", "a"]);
    expect(selectedRunIds(plan, { order: [], excluded: { a: true, b: true, c: true } })).toEqual([]);
  });

  it("are the folder's own until changed", () => {
    expect(isDefaultRunSelection(plan, EMPTY_RUN_SELECTION)).toBe(true);
    expect(isDefaultRunSelection(plan, { order: ["a", "ws", "b", "c"], excluded: {} })).toBe(true);
    expect(isDefaultRunSelection(plan, { order: ["b", "a"], excluded: {} })).toBe(false);
    expect(isDefaultRunSelection(plan, { order: [], excluded: { c: true } })).toBe(false);
  });
});
