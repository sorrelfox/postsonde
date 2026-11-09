import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { act, createEvent, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  Move: vi.fn<(input: unknown) => Promise<unknown>>(),
  Flush: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { toaster } from "../../commands/toasts";
import type { NodeDTO, TreeDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode, type WorkspaceInfo } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { ToastHost } from "../common/Toast";
import { Sidebar } from "../shell/Sidebar";
import { TREE_DRAG_TYPE } from "./move";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "shop-api",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

function dto(over: Partial<NodeDTO> & { id: string }): NodeDTO {
  return {
    parentId: null,
    kind: "request",
    name: over.id,
    path: `${over.id}.json`,
    fileKind: "http",
    method: "GET",
    childIds: [],
    readOnly: "",
    ...over,
  };
}

const folder = (id: string, name: string, childIds: string[], extra: Partial<NodeDTO> = {}) =>
  dto({ id, name, kind: "folder", fileKind: "folder", method: "", path: id, childIds, ...extra });

// Orders/ { Get order, Ship order }, Empty/, Locked/ (newer_version), Health
function nodes(): NodeDTO[] {
  return [
    folder("orders", "Orders", ["get", "ship"]),
    dto({ id: "get", parentId: "orders", name: "Get order", path: "orders/get-order.json" }),
    dto({ id: "ship", parentId: "orders", name: "Ship order", method: "POST", path: "orders/ship-order.json" }),
    folder("empty", "Empty", []),
    folder("locked", "Locked", [], { readOnly: "newer_version" }),
    dto({ id: "health", name: "Health", path: "health.json" }),
  ];
}

function tree(list: NodeDTO[] = nodes(), rootIds = ["orders", "empty", "locked", "health"]): TreeDTO {
  return { workspaceId: "ws_1", path: "/p/shop", readOnly: "", rootIds, nodes: list, problems: [], scratch: [] } as TreeDTO;
}

function toNodes(t: TreeDTO): TreeNode[] {
  return (t.nodes ?? []).map((n) =>
    n.kind === "folder"
      ? { kind: "folder", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, childIds: n.childIds ?? [] }
      : { kind: "request", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, method: n.method },
  );
}

/** The tree after moving Health into Empty. */
function healthInEmpty(): TreeDTO {
  const list = nodes().map((n) =>
    n.id === "empty" ? { ...n, childIds: ["health"] } : n.id === "health" ? { ...n, parentId: "empty", path: "empty/health.json" } : n,
  );
  return tree(list, ["orders", "empty", "locked"]);
}

function setup(): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  const t = tree();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: toNodes(t), rootIds: t.rootIds ?? [], readOnly: "", expandedIds: ["orders"] });
  render(
    <StoreProvider stores={stores}>
      <Sidebar id="sidebar" view="collections" hidden={false} />
      <ToastHost />
    </StoreProvider>,
  );
  return stores;
}

const row = (id: string) => document.querySelector(`[data-node-id="${id}"]`) as HTMLElement;
const rowIds = () => [...document.querySelectorAll("[data-node-id]")].map((el) => el.getAttribute("data-node-id"));
const menuItem = (id: string) => within(screen.getByRole("menu")).getAllByRole("menuitem").find((m) => m.dataset.menuItem === id) as HTMLElement;
const altKey = (el: Element, k: string) => fireEvent.keyDown(el, { key: k, altKey: true });

/** Fires a drag event at `clientY` (jsdom's plain Event takes no coordinates). Returns false when it was cancelled. */
function drag(type: "dragOver" | "drop", el: Element, dt: ReturnType<typeof dataTransfer>, clientY: number): boolean {
  const ev = createEvent[type](el, { dataTransfer: dt });
  Object.defineProperty(ev, "clientY", { value: clientY });
  return fireEvent(el, ev);
}

/** A DataTransfer stand-in (jsdom has none). */
function dataTransfer() {
  const data = new Map<string, string>();
  return {
    dropEffect: "none",
    effectAllowed: "all",
    get types() {
      return [...data.keys()];
    },
    setData: (type: string, value: string) => data.set(type, value),
    getData: (type: string) => data.get(type) ?? "",
  };
}

// jsdom lays nothing out: every row's rect is at 0 with no height, so the
// tree takes the middle of the row. Give rows a 30px box at the top instead.
function withRowBoxes() {
  const original = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function (this: Element) {
    if (this.hasAttribute("data-node-id")) return { top: 0, bottom: 30, left: 0, right: 200, height: 30, width: 200, x: 0, y: 0, toJSON() {} } as DOMRect;
    return original.call(this);
  };
  return () => {
    Element.prototype.getBoundingClientRect = original;
  };
}

let restoreRects: () => void;

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset();
  service.Tree.mockResolvedValue(tree());
  service.Move.mockReset();
  service.Flush.mockReset();
  service.Flush.mockResolvedValue(undefined);
  localStorage.clear();
  restoreRects = withRowBoxes();
});

afterEach(() => {
  restoreRects();
  act(() => toaster.clear());
  vi.useRealTimers();
});

describe("keyboard", () => {
  it("reorders with Alt+Up and Alt+Down, keeping focus on the item", async () => {
    setup();
    service.Move.mockResolvedValueOnce({ tree: tree(nodes().map((n) => (n.id === "orders" ? { ...n, childIds: ["ship", "get"] } : n))), nodeId: "ship" });
    act(() => row("ship").focus());
    altKey(row("ship"), "ArrowUp");
    await act(settle);
    expect(service.Move).toHaveBeenCalledWith({ id: "ship", parentId: "orders", index: 0 });
    expect(rowIds().slice(0, 3)).toEqual(["orders", "ship", "get"]);
    expect(document.activeElement).toBe(row("ship"));

    // At the top of its folder: nothing happens, and focus does not move either.
    altKey(row("ship"), "ArrowUp");
    await act(settle);
    expect(service.Move).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(row("ship"));
  });

  it("moves to a folder with the Move to folder dialog", async () => {
    const stores = setup();
    act(() => {
      stores.tabs.dispatch({ type: "tabs/open", id: "health" });
    });
    act(() => row("health").focus());
    fireEvent.keyDown(row("health"), { key: "M", shiftKey: true, metaKey: IS_MAC, ctrlKey: !IS_MAC });

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Move request" })).toBeTruthy();
    const select = within(dialog).getByRole("combobox") as HTMLSelectElement;
    const move = within(dialog).getByRole("button", { name: "Move" }) as HTMLButtonElement;
    expect(move.disabled).toBe(true);
    // The read-only folder cannot be chosen.
    const locked = [...select.options].find((o) => o.value === "locked") as HTMLOptionElement;
    expect(locked.disabled).toBe(true);

    fireEvent.change(select, { target: { value: "empty" } });
    expect(dialog.textContent).toContain("health.json → empty/health.json");
    service.Move.mockResolvedValueOnce({ tree: healthInEmpty(), nodeId: "health" });
    fireEvent.click(move);
    await act(settle);

    expect(service.Move).toHaveBeenCalledWith({ id: "health", parentId: "empty", index: -1 });
    expect(screen.queryByRole("dialog")).toBeNull();
    const state = stores.collection.getState();
    expect(state.nodes.health.parentId).toBe("empty");
    expect(state.selectedId).toBe("health");
    expect("empty" in state.expandedIds).toBe(true);
    // The open tab keeps working: the id did not change.
    expect(stores.tabs.getState().order).toEqual(["health"]);
  });

  it("does not offer a folder's own subfolders", () => {
    setup();
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("moveTo"));
    const select = within(screen.getByRole("dialog")).getByRole("combobox") as HTMLSelectElement;
    const disabled = [...select.options].filter((o) => o.disabled).map((o) => o.value);
    // Itself (its subfolders would follow) and the read-only folder.
    expect(disabled).toEqual(["orders", "locked"]);
  });
});

describe("drag and drop", () => {
  it("drops a request into a folder, highlighting it", async () => {
    const stores = setup();
    const dt = dataTransfer();
    fireEvent.dragStart(row("health"), { dataTransfer: dt });
    expect(dt.getData(TREE_DRAG_TYPE)).toBe("health");
    expect(row("health").className).toMatch(/dragging/);

    drag("dragOver", row("empty"), dt, 15);
    expect(dt.dropEffect).toBe("move");
    expect(row("empty").className).toMatch(/dropInto/);

    service.Move.mockResolvedValueOnce({ tree: healthInEmpty(), nodeId: "health" });
    drag("drop", row("empty"), dt, 15);
    fireEvent.dragEnd(row("health"), { dataTransfer: dt });
    await act(settle);
    expect(service.Move).toHaveBeenCalledWith({ id: "health", parentId: "empty", index: -1 });
    expect(stores.collection.getState().nodes.health.parentId).toBe("empty");
    expect(row("empty").className).not.toMatch(/dropInto/);
  });

  it("shows a line between items and drops there", async () => {
    setup();
    const dt = dataTransfer();
    fireEvent.dragStart(row("health"), { dataTransfer: dt });
    // Top half of Ship order: before it, inside Orders.
    drag("dragOver", row("ship"), dt, 5);
    const line = document.querySelector('[class*="dropLine"]') as HTMLElement;
    expect(line).toBeTruthy();
    // Rows: Orders 0, Get 1, Ship 2: the line is at the top of row 2, indented one level.
    expect(line.style.top).toBe("60px");
    expect(line.style.left).toBe("22px");

    service.Move.mockResolvedValueOnce({ tree: tree(), nodeId: "health" });
    drag("drop", row("ship"), dt, 5);
    await act(settle);
    expect(service.Move).toHaveBeenCalledWith({ id: "health", parentId: "orders", index: 1 });
    expect(document.querySelector('[class*="dropLine"]')).toBeNull();
  });

  it("refuses a folder into its own subfolder and into a read-only folder", () => {
    setup();
    const dt = dataTransfer();
    fireEvent.dragStart(row("orders"), { dataTransfer: dt });
    drag("dragOver", row("get"), dt, 5);
    expect(dt.dropEffect).toBe("none");
    expect(document.querySelector('[class*="dropLine"]')).toBeNull();
    drag("dragOver", row("locked"), dt, 15);
    expect(dt.dropEffect).toBe("none");
    expect(row("locked").className).not.toMatch(/dropInto/);
    drag("drop", row("get"), dt, 5);
    expect(service.Move).not.toHaveBeenCalled();
  });

  it("ignores drags that did not start in the tree, such as files", () => {
    setup();
    const dt = dataTransfer();
    dt.setData("Files", "");
    const over = drag("dragOver", row("empty"), dt, 15);
    // Not cancelled: the tree is no drop target for it.
    expect(over).toBe(true);
    expect(row("empty").className).not.toMatch(/dropInto/);
  });

  it("expands a collapsed folder the pointer rests on", () => {
    vi.useFakeTimers();
    const stores = setup();
    act(() => {
      stores.collection.dispatch({ type: "collection/setExpanded", id: "orders", expanded: false });
    });
    const dt = dataTransfer();
    fireEvent.dragStart(row("health"), { dataTransfer: dt });
    drag("dragOver", row("orders"), dt, 15);
    expect("orders" in stores.collection.getState().expandedIds).toBe(false);
    act(() => vi.advanceTimersByTime(700));
    expect("orders" in stores.collection.getState().expandedIds).toBe(true);
  });

  it("does not drag in a read-only folder", () => {
    const stores = setup();
    const locked = folder("locked", "Locked", ["old"], { readOnly: "newer_version" });
    const old = dto({ id: "old", parentId: "locked", name: "Old", path: "locked/old.json" });
    const t = tree([...nodes().filter((n) => n.id !== "locked"), locked, old]);
    act(() => {
      stores.collection.dispatch({ type: "collection/load", nodes: toNodes(t), rootIds: t.rootIds ?? [], expandedIds: ["locked"] });
    });
    expect(row("old").getAttribute("draggable")).toBeNull();
    expect(row("health").getAttribute("draggable")).toBe("true");
  });
});
