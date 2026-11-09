import { describe, expect, it } from "vitest";
import {
  initialTabsState,
  MAX_CLOSED_TABS,
  selectIsPinned,
  selectIsPreview,
  tabsReducer,
  type TabsAction,
  type TabsState,
} from "./tabs";

const tabs = (
  order: string[],
  pinned: string[] = [],
  activeId: string | null = order[0] ?? null,
  extra: Partial<TabsState> = {},
): TabsState => ({
  order,
  pinnedIds: Object.fromEntries(pinned.map((id) => [id, true as const])),
  activeId,
  previewId: null,
  closed: [],
  ...extra,
});

/** The state without the closed tabs, to compare the open tabs only. */
const open = (state: TabsState): TabsState => ({ ...state, closed: [] });

const reduce = (state: TabsState, action: TabsAction) => tabsReducer(state, action);

describe("tabsReducer", () => {
  it("opens a tab at the end and activates it", () => {
    const s = reduce(initialTabsState, { type: "tabs/open", id: "a" });
    expect(s).toEqual(tabs(["a"], [], "a"));
    expect(reduce(s, { type: "tabs/open", id: "b" })).toEqual(tabs(["a", "b"], [], "b"));
  });

  it("only activates a tab that is already open", () => {
    const s = tabs(["a", "b"], [], "b");
    expect(reduce(s, { type: "tabs/open", id: "a" })).toEqual(tabs(["a", "b"], [], "a"));
    expect(reduce(s, { type: "tabs/open", id: "b" })).toBe(s);
  });

  it("never opens a tab inside the pinned group", () => {
    const s = tabs(["p", "a"], ["p"], "p");
    expect(reduce(s, { type: "tabs/open", id: "c", index: 0 }).order).toEqual(["p", "c", "a"]);
  });

  it("activates only open tabs", () => {
    const s = tabs(["a", "b"]);
    expect(reduce(s, { type: "tabs/activate", id: "b" }).activeId).toBe("b");
    expect(reduce(s, { type: "tabs/activate", id: "x" })).toBe(s);
  });

  it("closing the active tab activates its right neighbour, else the left one", () => {
    const s = tabs(["a", "b", "c"], [], "b");
    expect(open(reduce(s, { type: "tabs/close", ids: ["b"] }))).toEqual(tabs(["a", "c"], [], "c"));
    expect(open(reduce(s, { type: "tabs/close", ids: ["b", "c"] }))).toEqual(tabs(["a"], [], "a"));
    expect(open(reduce(s, { type: "tabs/close", ids: ["a", "b", "c"] }))).toEqual(tabs([], [], null));
  });

  it("closing drops the pinned flag and ignores unknown ids", () => {
    const s = tabs(["p", "a"], ["p"], "a");
    expect(open(reduce(s, { type: "tabs/close", ids: ["p"] }))).toEqual(tabs(["a"], [], "a"));
    expect(reduce(s, { type: "tabs/close", ids: ["x"] })).toBe(s);
  });

  it("closes the others but keeps pinned tabs", () => {
    const s = tabs(["p", "a", "b", "c"], ["p"], "a");
    expect(open(reduce(s, { type: "tabs/closeOthers", id: "b" }))).toEqual(tabs(["p", "b"], ["p"], "b"));
    expect(reduce(s, { type: "tabs/closeOthers", id: "x" })).toBe(s);
  });

  it("moves a tab within its group", () => {
    const s = tabs(["p", "a", "b"], ["p"]);
    expect(reduce(s, { type: "tabs/move", id: "b", toIndex: 0 }).order).toEqual(["p", "b", "a"]);
    expect(reduce(s, { type: "tabs/move", id: "a", toIndex: 9 }).order).toEqual(["p", "b", "a"]);
    expect(reduce(s, { type: "tabs/move", id: "p", toIndex: 5 })).toBe(s);
    expect(reduce(s, { type: "tabs/move", id: "x", toIndex: 0 })).toBe(s);
  });

  it("pins to the end of the pinned group and unpins to the start of the rest", () => {
    const s = tabs(["p", "a", "b"], ["p"]);
    const pinned = reduce(s, { type: "tabs/setPinned", id: "b", pinned: true });
    expect(pinned.order).toEqual(["p", "b", "a"]);
    expect(selectIsPinned(pinned, "b")).toBe(true);

    const unpinned = reduce(pinned, { type: "tabs/setPinned", id: "p", pinned: false });
    expect(unpinned.order).toEqual(["b", "p", "a"]);
    expect(selectIsPinned(unpinned, "p")).toBe(false);

    expect(reduce(s, { type: "tabs/setPinned", id: "p", pinned: true })).toBe(s);
    expect(reduce(s, { type: "tabs/setPinned", id: "x", pinned: true })).toBe(s);
  });

  it("restores tabs, dropping duplicates and an active id that is not open", () => {
    expect(reduce(initialTabsState, { type: "tabs/restore", order: ["a", "b", "a"], activeId: "b" })).toEqual(tabs(["a", "b"], [], "b"));
    expect(reduce(initialTabsState, { type: "tabs/restore", order: ["a", "b"], activeId: "x" })).toEqual(tabs(["a", "b"], [], "a"));
    expect(reduce(tabs(["p"], ["p"]), { type: "tabs/restore", order: [], activeId: null })).toEqual(tabs([], [], null));
    const s = tabs(["a", "b"], [], "b");
    expect(reduce(s, { type: "tabs/restore", order: ["a", "b"], activeId: "b" })).toBe(s);
  });

  it("restores pinned tabs first, the preview tab and the closed tabs", () => {
    const closed = [{ id: "x", index: 1, pinned: false }];
    const s = reduce(initialTabsState, {
      type: "tabs/restore",
      order: ["a", "p", "b"],
      activeId: "b",
      pinnedIds: ["p", "gone"],
      previewId: "b",
      closed,
    });
    expect(s).toEqual(tabs(["p", "a", "b"], ["p"], "b", { previewId: "b", closed }));
    expect(reduce(s, { type: "tabs/restore", order: ["p", "a", "b"], activeId: "b", pinnedIds: ["p"], previewId: "b", closed })).toBe(s);
    // A pinned or closed tab is not a preview tab.
    expect(reduce(initialTabsState, { type: "tabs/restore", order: ["p"], activeId: "p", pinnedIds: ["p"], previewId: "p" }).previewId).toBeNull();
    expect(reduce(initialTabsState, { type: "tabs/restore", order: ["a"], activeId: "a", previewId: "x" }).previewId).toBeNull();
  });
});

describe("the preview tab", () => {
  it("opens as the preview tab and replaces the previous preview in place", () => {
    let s = reduce(tabs(["a"]), { type: "tabs/open", id: "b", preview: true });
    expect(s).toEqual(tabs(["a", "b"], [], "b", { previewId: "b" }));
    expect(selectIsPreview(s, "b")).toBe(true);
    s = reduce(s, { type: "tabs/open", id: "c" });
    s = reduce(s, { type: "tabs/open", id: "d", preview: true });
    // d takes b's place; the replaced preview is not remembered as closed.
    expect(s).toEqual(tabs(["a", "d", "c"], [], "d", { previewId: "d" }));
  });

  it("does not turn an open kept tab into the preview", () => {
    const s = tabs(["a", "b"], [], "b", { previewId: "b" });
    expect(reduce(s, { type: "tabs/open", id: "a", preview: true })).toEqual(tabs(["a", "b"], [], "a", { previewId: "b" }));
  });

  it("is kept by tabs/keep, a move and pinning, and is gone when it closes", () => {
    const s = tabs(["a", "b", "c"], [], "b", { previewId: "b" });
    expect(reduce(s, { type: "tabs/keep", id: "b" }).previewId).toBeNull();
    expect(reduce(s, { type: "tabs/keep", id: "a" })).toBe(s);
    expect(reduce(s, { type: "tabs/move", id: "b", toIndex: 2 })).toMatchObject({ order: ["a", "c", "b"], previewId: null });
    expect(reduce(s, { type: "tabs/move", id: "a", toIndex: 2 }).previewId).toBe("b");
    expect(reduce(s, { type: "tabs/setPinned", id: "b", pinned: true })).toMatchObject({ order: ["b", "a", "c"], previewId: null });
    expect(reduce(s, { type: "tabs/close", ids: ["b"] }).previewId).toBeNull();
  });
});

describe("closed tabs", () => {
  it("remembers closed tabs with their index and pinned flag, the leftmost on top", () => {
    const s = tabs(["p", "a", "b", "c"], ["p"], "a");
    const closed = reduce(s, { type: "tabs/close", ids: ["a", "p", "c"] });
    expect(closed.closed).toEqual([
      { id: "c", index: 3, pinned: false },
      { id: "a", index: 1, pinned: false },
      { id: "p", index: 0, pinned: true },
    ]);
    expect(reduce(s, { type: "tabs/close", ids: ["a"], remember: false }).closed).toEqual([]);
    // Closing a tab again moves it to the top; opening it forgets it.
    const again = reduce(reduce(reduce(closed, { type: "tabs/open", id: "c" }), { type: "tabs/close", ids: ["c"] }), {
      type: "tabs/open",
      id: "a",
    });
    expect(again.closed.map((e) => e.id)).toEqual(["p", "c"]);
  });

  it("keeps at most MAX_CLOSED_TABS", () => {
    let s = tabs([]);
    for (let i = 0; i < MAX_CLOSED_TABS + 5; i++) {
      s = reduce(reduce(s, { type: "tabs/open", id: `t${i}` }), { type: "tabs/close", ids: [`t${i}`] });
    }
    expect(s.closed).toHaveLength(MAX_CLOSED_TABS);
    expect(s.closed[MAX_CLOSED_TABS - 1].id).toBe(`t${MAX_CLOSED_TABS + 4}`);
  });

  it("reopens at the old index, pinned again, active, dropping the entries closed after it", () => {
    const s = reduce(tabs(["p", "a", "b", "c"], ["p"], "c"), { type: "tabs/close", ids: ["p", "b"] });
    expect(s.order).toEqual(["a", "c"]);
    let r = reduce(s, { type: "tabs/reopen", id: "p" });
    expect(r).toEqual(tabs(["p", "a", "c"], ["p"], "p", { closed: [{ id: "b", index: 2, pinned: false }] }));
    r = reduce(r, { type: "tabs/reopen", id: "b" });
    expect(r).toEqual(tabs(["p", "a", "b", "c"], ["p"], "b"));
    // Reopening the lower entry drops the one above it.
    expect(reduce(s, { type: "tabs/reopen", id: "b" })).toMatchObject({ order: ["a", "c", "b"], closed: [] });
    expect(reduce(s, { type: "tabs/reopen", id: "x" })).toBe(s);
    expect(reduce(s, { type: "tabs/forgetClosed" }).closed).toEqual([]);
    expect(reduce(tabs(["a"]), { type: "tabs/forgetClosed" }).closed).toEqual([]);
  });

  it("clamps a reopened tab to its group", () => {
    const s = tabs(["p", "q", "a"], ["p", "q"], "a", { closed: [{ id: "u", index: 0, pinned: false }, { id: "v", index: 9, pinned: true }] });
    expect(reduce(s, { type: "tabs/reopen", id: "v" }).order).toEqual(["p", "q", "v", "a"]);
    expect(reduce(s, { type: "tabs/reopen", id: "u" }).order).toEqual(["p", "q", "u", "a"]);
  });
});
