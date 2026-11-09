import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setSortPersistence } from "../../commands/collection";
import { toaster } from "../../commands/toasts";
import type { SortPersistence } from "../../lib/treeSort";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { Sidebar } from "../shell/Sidebar";
import { CollectionTree } from "./CollectionTree";
import type { TreeActions } from "./useTreeActions";

const folder = (id: string, parentId: string | null, name: string, childIds: string[]): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});
const request = (id: string, parentId: string | null, name: string, method = "GET"): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path: `${id}.json`,
  fileKind: "http",
  readOnly: "",
  method,
});

// Shop/ { Orders/ { Get order, Cancel order (DELETE) }, Users/ { List users, Create user (POST) } }, Health
const NODES: TreeNode[] = [
  folder("shop", null, "Shop", ["orders", "users"]),
  folder("orders", "shop", "Orders", ["get", "cancel"]),
  request("get", "orders", "Get order"),
  request("cancel", "orders", "Cancel order", "DELETE"),
  folder("users", "shop", "Users", ["list", "create"]),
  request("list", "users", "List users"),
  request("create", "users", "Create user", "POST"),
  request("health", null, "Health"),
];

function load(stores: Stores) {
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes: NODES,
    rootIds: ["shop", "health"],
    expandedIds: ["shop"],
  });
}

function setup(filter = "", actions?: TreeActions) {
  const stores = createStores();
  load(stores);
  const onClearFilter = vi.fn();
  const tree = (f: string) => (
    <StoreProvider stores={stores}>
      <CollectionTree label="Collections" filter={f} onClearFilter={onClearFilter} actions={actions} onNewItem={() => {}} />
    </StoreProvider>
  );
  const view = render(tree(filter));
  return { stores, onClearFilter, rerender: (f: string) => view.rerender(tree(f)) };
}

const names = () => screen.queryAllByRole("treeitem").map((el) => el.getAttribute("data-node-id"));
const item = (id: string) => document.querySelector(`[data-node-id="${id}"]`) as HTMLElement;
const lastToast = () => {
  const list = toaster.toasts();
  return list[list.length - 1];
};

const fakeActions = (): TreeActions => ({
  rename: vi.fn(),
  duplicate: vi.fn(),
  remove: vi.fn(() => Promise.resolve(true)),
  move: vi.fn(() => Promise.resolve(true)),
  moveTo: vi.fn(),
});

describe("the sidebar filter in the tree (NAV-06)", () => {
  it("shows the matches and the folders above them, opened, with the matched letters marked", () => {
    const { stores } = setup("cancel");
    expect(names()).toEqual(["shop", "orders", "cancel"]);
    expect(item("orders").getAttribute("aria-expanded")).toBe("true");
    expect(item("cancel").querySelector("mark")?.textContent).toBe("Cancel");
    expect(screen.getByRole("tree").getAttribute("aria-label")).toBe("Collections, filtered");
    expect(screen.getByRole("status").textContent).toBe("1 match for “cancel”");
    // The store's expansion is untouched.
    expect(stores.collection.getState().expandedIds).toEqual({ shop: true });
  });

  it("filters by method, marking the badge", () => {
    setup("post");
    expect(names()).toEqual(["shop", "users", "create"]);
    expect(item("create").querySelector("[data-hit]")?.textContent).toBe("POST");
  });

  it("toggles folders only in the filtered view, and restores the expansion when cleared", () => {
    const { stores, rerender } = setup("order");
    expect(names()).toEqual(["shop", "orders", "get", "cancel"]);
    fireEvent.click(item("orders"));
    expect(names()).toEqual(["shop", "orders"]);
    expect(stores.collection.getState().expandedIds).toEqual({ shop: true });
    rerender("");
    expect(names()).toEqual(["shop", "orders", "users", "health"]);
    // A new query opens the folders above its matches again.
    rerender("orde");
    expect(names()).toEqual(["shop", "orders", "get", "cancel"]);
  });

  it("gives the Tab stop to the first match and moves between the shown rows", () => {
    setup("user");
    expect(names()).toEqual(["shop", "users", "list", "create"]);
    // "Users" (the folder) is the first match.
    const first = item("users");
    expect(first.tabIndex).toBe(0);
    act(() => first.focus());
    fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(item("list"));
  });

  it("shows an empty state with a way to clear the filter", () => {
    const { onClearFilter } = setup("zzz");
    expect(screen.getByText("No matches", { selector: "p" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Clear the filter" }));
    expect(onClearFilter).toHaveBeenCalled();
  });

  it("turns drag and drop and Alt+Up/Down off while filtering", () => {
    const actions = fakeActions();
    setup("order", actions);
    expect(item("get").getAttribute("draggable")).toBeNull();
    act(() => item("cancel").focus());
    fireEvent.keyDown(item("cancel"), { key: "ArrowUp", altKey: true });
    expect(actions.move).not.toHaveBeenCalled();
    expect(lastToast()?.message).toBe("Clear the filter to reorder items.");
    act(() => toaster.clear());
  });
});

describe("sorting by name (COL-05)", () => {
  let saved: [string, readonly string[]][] = [];
  let previous: SortPersistence;
  beforeEach(() => {
    saved = [];
    previous = setSortPersistence({ load: () => null, save: (ws, ids) => saved.push([ws, ids]) });
  });
  afterEach(() => {
    setSortPersistence(previous);
    act(() => toaster.clear());
  });

  const openMenu = (id: string | null) => {
    const target = id === null ? screen.getByRole("tree") : item(id);
    fireEvent.contextMenu(target, { clientX: 10, clientY: 10 });
  };

  it("sorts a folder by name from the context menu, and back to its manual order", () => {
    const { stores } = setup("", fakeActions());
    fireEvent.click(item("users"));
    expect(names()).toEqual(["shop", "orders", "users", "list", "create", "health"]);

    // On an item, the choices are about its folder.
    openMenu("list");
    const byName = screen.getByRole("menuitemradio", { name: "Sort “Users” by name" });
    expect(byName.getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("menuitemradio", { name: "Keep “Users” in manual order" }).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(byName);
    expect(names()).toEqual(["shop", "orders", "users", "create", "list", "health"]);
    expect(stores.collection.getState().sortedByName).toEqual({ users: true });
    expect(saved).toEqual([["ws_1", ["users"]]]);

    openMenu("users");
    expect(screen.getByRole("menuitemradio", { name: "Sort “Users” by name" }).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(screen.getByRole("menuitemradio", { name: "Keep “Users” in manual order" }));
    expect(names()).toEqual(["shop", "orders", "users", "list", "create", "health"]);
    expect(saved[saved.length - 1]).toEqual(["ws_1", []]);
  });

  it("sorts the top level from a top-level item's menu", () => {
    setup("", fakeActions());
    openMenu("health");
    fireEvent.click(screen.getByRole("menuitemradio", { name: "Sort the top level by name" }));
    expect(names()).toEqual(["shop", "orders", "users", "health"]);
    expect(saved).toEqual([["ws_1", [""]]]);
  });

  it("does not reorder a sorted folder by hand, and says why", () => {
    const actions = fakeActions();
    const { stores } = setup("", actions);
    act(() => stores.collection.dispatch({ type: "collection/setSort", parentId: "shop", mode: "name" }));
    act(() => item("users").focus());
    fireEvent.keyDown(item("users"), { key: "ArrowUp", altKey: true });
    expect(actions.move).not.toHaveBeenCalled();
    expect(String(lastToast()?.message)).toMatch(/“Shop” is sorted by name/);

    openMenu("users");
    const up = screen.getByRole("menuitem", { name: /Move up/ });
    expect(up.getAttribute("aria-disabled")).toBe("true");
    expect(up.getAttribute("title")).toMatch(/sorted by name/);
  });
});

describe("the Collections filter field", () => {
  it("filters the tree as you type, goes into it with Down and clears with Escape", async () => {
    const stores = createStores();
    load(stores);
    render(
      <StoreProvider stores={stores}>
        <Sidebar id="sb" view="collections" hidden={false} />
      </StoreProvider>,
    );
    const field = screen.getByRole("searchbox", { name: "Filter collections" }) as HTMLInputElement;
    expect(field.disabled).toBe(false);
    fireEvent.change(field, { target: { value: "list" } });
    expect(names()).toEqual(["shop", "users", "list"]);

    act(() => field.focus());
    fireEvent.keyDown(field, { key: "ArrowDown" });
    expect(document.activeElement).toBe(item("list"));

    fireEvent.keyDown(field, { key: "Escape" });
    expect(field.value).toBe("");
    expect(names()).toEqual(["shop", "orders", "users", "health"]);
  });

  it("is focused by the collection.filter shortcut through filterFocus", () => {
    const stores = createStores();
    load(stores);
    const ui = (n: number) => (
      <StoreProvider stores={stores}>
        <Sidebar id="sb" view="collections" hidden={false} filterFocus={n} />
      </StoreProvider>
    );
    const view = render(ui(0));
    const field = screen.getByRole("searchbox", { name: "Filter collections" });
    expect(document.activeElement).not.toBe(field);
    view.rerender(ui(1));
    expect(document.activeElement).toBe(field);
  });
});
