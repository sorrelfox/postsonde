import { describe, expect, it } from "vitest";
import { IS_MAC } from "../lib/shortcuts";
import { createStores, type RequestData } from "../stores";
import {
  clearEditHistory,
  COALESCE_MS,
  editHistoryGeneration,
  editorStateScope,
  historyDepth,
  MAX_HISTORY,
  recordEdit,
  subscribeEditHistory,
  takeRedo,
  takeUndo,
} from "./editHistory";
import { EDIT_HISTORY_ATTRIBUTE, handleUndoKeyDown } from "./undoRedo";

const r = (over: Record<string, unknown>): RequestData => ({ url: "/a", method: "GET", headers: [], ...over });

describe("edit history (NAV-07)", () => {
  it("records only the fields that changed, and undoes and redoes them", () => {
    const stores = createStores();
    recordEdit(stores, "q", r({}), r({ url: "/b" }), ["url", "method"], "coalesce", 0);
    expect(historyDepth(stores, "q")).toEqual({ undo: 1, redo: 0 });
    const step = takeUndo(stores, "q");
    expect(step).toEqual({ before: { url: "/a" }, after: { url: "/b" } });
    expect(historyDepth(stores, "q")).toEqual({ undo: 0, redo: 1 });
    expect(takeUndo(stores, "q")).toBeNull();
    expect(takeRedo(stores, "q")).toBe(step);
    expect(takeRedo(stores, "q")).toBeNull();
  });

  it("ignores edits that changed nothing", () => {
    const stores = createStores();
    const same = r({});
    recordEdit(stores, "q", same, { ...same }, ["url"], "coalesce", 0);
    expect(historyDepth(stores, "q").undo).toBe(0);
  });

  it("coalesces quick edits of overlapping fields, not slow or unrelated ones", () => {
    const stores = createStores();
    recordEdit(stores, "q", r({}), r({ url: "/ab" }), ["url"], "coalesce", 0);
    recordEdit(stores, "q", r({ url: "/ab" }), r({ url: "/abc", headers: ["x"] }), ["url", "headers"], "coalesce", 500);
    expect(historyDepth(stores, "q").undo).toBe(1);
    // Too late: a new step.
    recordEdit(stores, "q", r({ url: "/abc" }), r({ url: "/abcd" }), ["url"], "coalesce", 500 + COALESCE_MS + 1);
    // Other fields: a new step.
    recordEdit(stores, "q", r({}), r({ method: "PUT" }), ["method"], "coalesce", 500 + COALESCE_MS + 2);
    expect(historyDepth(stores, "q").undo).toBe(3);
    takeUndo(stores, "q");
    takeUndo(stores, "q");
    expect(takeUndo(stores, "q")).toEqual({
      before: { url: "/a", headers: [] },
      after: { url: "/abc", headers: ["x"] },
    });
  });

  it("keeps a separate edit apart on both sides, and starts a new step after undo", () => {
    const stores = createStores();
    recordEdit(stores, "q", r({}), r({ url: "/b" }), ["url"], "coalesce", 0);
    recordEdit(stores, "q", r({ url: "/b" }), r({ url: "/c" }), ["url"], "separate", 1);
    recordEdit(stores, "q", r({ url: "/c" }), r({ url: "/d" }), ["url"], "coalesce", 2);
    expect(historyDepth(stores, "q").undo).toBe(3);
    takeUndo(stores, "q");
    recordEdit(stores, "q", r({ url: "/c" }), r({ url: "/e" }), ["url"], "coalesce", 3);
    // A new edit drops the redo steps.
    expect(historyDepth(stores, "q")).toEqual({ undo: 3, redo: 0 });
  });

  it("keeps at most MAX_HISTORY steps", () => {
    const stores = createStores();
    for (let i = 0; i < MAX_HISTORY + 5; i++) {
      recordEdit(stores, "q", r({ url: `/${i}` }), r({ url: `/${i + 1}` }), ["url"], "separate", i);
    }
    expect(historyDepth(stores, "q").undo).toBe(MAX_HISTORY);
  });

  it("clears steps and editor states and bumps the generation", () => {
    const stores = createStores();
    const calls: number[] = [];
    const stop = subscribeEditHistory(stores, () => calls.push(editHistoryGeneration(stores, "q")));
    recordEdit(stores, "q", r({}), r({ url: "/b" }), ["url"], "coalesce", 0);
    const scope = editorStateScope(stores, "q");
    expect(scope.acquire("line:URL:")).toBe(true);
    expect(scope.acquire("line:URL:")).toBe(false); // held by a mounted editor
    scope.release("line:URL:", "state-1");
    expect(scope.get("line:URL:")).toBe("state-1");

    clearEditHistory(stores, ["q"]);
    stop();
    expect(calls).toEqual([1]);
    expect(historyDepth(stores, "q").undo).toBe(0);
    // The old scope keeps and gives nothing any more; a new one starts empty.
    scope.release("line:URL:", "state-2");
    expect(scope.acquire("line:URL:")).toBe(false);
    const fresh = editorStateScope(stores, "q");
    expect(fresh.get("line:URL:")).toBeUndefined();
    expect(fresh.acquire("line:URL:")).toBe(true);
  });

  it("keeps histories per stores and per request", () => {
    const a = createStores();
    const b = createStores();
    recordEdit(a, "q", r({}), r({ url: "/b" }), ["url"], "coalesce", 0);
    expect(historyDepth(a, "other").undo).toBe(0);
    expect(historyDepth(b, "q").undo).toBe(0);
  });
});

describe("handleUndoKeyDown", () => {
  function panel(): { input: HTMLInputElement; cm: HTMLElement; cleanup: () => void } {
    const root = document.createElement("div");
    root.setAttribute(EDIT_HISTORY_ATTRIBUTE, "q");
    const input = document.createElement("input");
    const cm = document.createElement("div");
    cm.className = "cm-editor";
    const content = document.createElement("div");
    cm.append(content);
    root.append(input, cm);
    document.body.append(root);
    return { input, cm: content, cleanup: () => root.remove() };
  }
  const key = (target: Element, init: KeyboardEventInit) => {
    const e = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
    Object.defineProperty(e, "target", { value: target });
    return e;
  };
  const ctrl = (k: string, shift = false) => ({ key: k, ctrlKey: !IS_MAC, metaKey: IS_MAC, shiftKey: shift });

  it("undoes and redoes outside code editors, only for open drafts", () => {
    const stores = createStores();
    const { input, cm, cleanup } = panel();
    try {
      // No draft: left alone.
      expect(handleUndoKeyDown(stores, key(input, ctrl("z")))).toBeNull();
      stores.drafts.dispatch({ type: "drafts/open", id: "q", request: r({}) });
      const undo = key(input, ctrl("z"));
      expect(handleUndoKeyDown(stores, undo)).toBe("undo");
      expect(undo.defaultPrevented).toBe(true);
      expect(handleUndoKeyDown(stores, key(input, ctrl("Z", true)))).toBe("redo");
      expect(handleUndoKeyDown(stores, key(input, ctrl("y")))).toBe("redo");
      // A code editor has its own history.
      expect(handleUndoKeyDown(stores, key(cm, ctrl("z")))).toBeNull();
      // Already handled (e.g. the Undo toast of a delete): left alone.
      const handled = key(input, ctrl("z"));
      handled.preventDefault();
      expect(handleUndoKeyDown(stores, handled)).toBeNull();
      // Outside a request editor: left alone.
      expect(handleUndoKeyDown(stores, key(document.body, ctrl("z")))).toBeNull();
    } finally {
      cleanup();
    }
  });
});
