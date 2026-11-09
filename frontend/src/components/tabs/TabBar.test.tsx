import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";

// The request editor loads from Go and has its own tabs; RequestEditor.test.tsx covers it.
vi.mock("../editor/RequestEditor", () => ({
  RequestEditor: ({ id }: { id: string }) => <div>request editor {id}</div>,
}));
vi.mock("../editor/GraphqlEditor", () => ({
  GraphqlEditor: ({ id }: { id: string }) => <div>graphql editor {id}</div>,
}));
// The folder settings view (COL-03) loads from Go too; FolderEditor.test.tsx covers it.
vi.mock("../editor/FolderEditor", () => ({
  FolderEditor: ({ id }: { id: string }) => <div>folder editor {id}</div>,
}));
// TabPanel loads the resolved {{variables}} of HTTP and GraphQL requests.
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: {
    Variables: () => Object.assign(Promise.resolve({ variables: [], environment: "" }), { cancel: () => undefined }),
  },
}));
import { CollectionTree } from "../tree/CollectionTree";
import { TAB_PANEL_ID, TabBar, tabElementId } from "./TabBar";
import { TabPanel } from "./TabPanel";
import { IS_MAC } from "../../lib/shortcuts";
import { isCloseTabKey, isContextMenuKey, tabMoveDirection } from "./tabKeys";

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({
    kind: "request",
    parentId: null,
    name: over.id,
    path: `${over.id}.json`,
    fileKind: "http",
    readOnly: "",
    method: "GET",
    ...over,
  }) as TreeNode;

const NODES: TreeNode[] = [
  node({ id: "catalog", kind: "folder", name: "Catalog", path: "catalog", fileKind: "", childIds: ["list", "create", "gql"] } as Partial<TreeNode> & { id: string }),
  node({ id: "list", parentId: "catalog", name: "List products" }),
  node({ id: "create", parentId: "catalog", name: "Create product", method: "POST" }),
  node({ id: "gql", parentId: "catalog", name: "Products", method: "POST", fileKind: "graphql" }),
];

function setup(open: string[] = ["list", "gql", "catalog", "create"], active = open[0]) {
  const stores: Stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: NODES, rootIds: ["catalog"], expandedIds: ["catalog"] });
  stores.tabs.dispatch({ type: "tabs/restore", order: open, activeId: active ?? null });
  render(
    <StoreProvider stores={stores}>
      <TabBar />
      <TabPanel />
    </StoreProvider>,
  );
  return stores;
}

const tab = (id: string) => document.getElementById(tabElementId(id)) as HTMLElement;
const tabIds = () => screen.queryAllByRole("tab").map((el) => el.dataset.tabId);
const focusedTab = () => (document.activeElement as HTMLElement | null)?.dataset.tabId;
const key = (key: string, init: Partial<KeyboardEventInit> = {}) =>
  fireEvent.keyDown(document.activeElement as Element, { key, ...init });

describe("TabBar", () => {
  it("renders an ARIA tab list with badges, and the panel of the active tab", () => {
    const stores = setup();
    const list = screen.getByRole("tablist", { name: "Open requests" });
    expect(within(list).getAllByRole("tab")).toHaveLength(4);
    expect(tabIds()).toEqual(["list", "gql", "catalog", "create"]);
    const badge = (id: string) => tab(id).querySelector("[data-tone]") as HTMLElement;
    expect([badge("list").textContent, badge("list").dataset.tone]).toEqual(["GET", "get"]);
    expect([badge("gql").textContent, badge("gql").dataset.tone]).toEqual(["GQL", "gql"]);
    expect([badge("catalog").textContent, badge("catalog").dataset.tone]).toEqual(["DIR", "dir"]);
    expect([badge("create").textContent, badge("create").dataset.tone]).toEqual(["POST", "post"]);

    expect(tab("list").getAttribute("aria-selected")).toBe("true");
    expect(tab("gql").getAttribute("aria-selected")).toBe("false");
    expect(tab("list").getAttribute("aria-controls")).toBe(TAB_PANEL_ID);
    expect(screen.getAllByRole("tab").filter((el) => el.tabIndex === 0)).toEqual([tab("list")]);
    expect(screen.getByRole("tab", { name: "GET List products" })).toBe(tab("list"));

    const panel = screen.getByRole("tabpanel");
    expect(panel.id).toBe(TAB_PANEL_ID);
    expect(panel.getAttribute("aria-labelledby")).toBe(tab("list").id);
    // An HTTP request shows its editor, a GraphQL request its own (GQL-01).
    expect(panel.textContent).toBe("request editor list");
    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "gql" }));
    expect(screen.getByRole("tabpanel").textContent).toBe("graphql editor gql");
  });

  it("switches tabs with the mouse and shows the folder settings", () => {
    const stores = setup();
    fireEvent.pointerDown(tab("catalog"), { pointerId: 1, button: 0, clientX: 10 });
    fireEvent.pointerUp(tab("catalog"), { pointerId: 1, button: 0, clientX: 10 });
    expect(stores.tabs.getState().activeId).toBe("catalog");
    expect(stores.collection.getState().selectedId).toBe("catalog");
    const panel = screen.getByRole("tabpanel");
    expect(panel.textContent).toBe("folder editor catalog");
    expect(panel.getAttribute("data-edit-history")).toBe("catalog");
    expect(panel.getAttribute("aria-labelledby")).toBe(tab("catalog").id);
  });

  it("moves focus and selection with the arrow keys, Home and End", () => {
    const stores = setup();
    act(() => tab("list").focus());
    key("ArrowRight");
    expect(focusedTab()).toBe("gql");
    expect(stores.tabs.getState().activeId).toBe("gql");
    expect(tab("gql").tabIndex).toBe(0);
    key("End");
    expect(focusedTab()).toBe("create");
    key("ArrowRight"); // wraps
    expect(focusedTab()).toBe("list");
    key("ArrowLeft"); // wraps back
    expect(focusedTab()).toBe("create");
    key("Home");
    expect(focusedTab()).toBe("list");
    expect(stores.tabs.getState().activeId).toBe("list");
  });

  it("closes with the close button, the middle button and Delete, activating the right neighbour, else the left", () => {
    const stores = setup(["list", "gql", "catalog", "create"], "gql");
    fireEvent.click(tab("gql").querySelector("[data-close-tab]") as Element);
    expect(tabIds()).toEqual(["list", "catalog", "create"]);
    expect(stores.tabs.getState().activeId).toBe("catalog");

    fireEvent(tab("list"), new MouseEvent("auxclick", { bubbles: true, button: 1 }));
    expect(tabIds()).toEqual(["catalog", "create"]);
    expect(stores.tabs.getState().activeId).toBe("catalog");

    act(() => tab("create").focus());
    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "create" }));
    key("Delete");
    expect(tabIds()).toEqual(["catalog"]);
    expect(stores.tabs.getState().activeId).toBe("catalog");
    expect(focusedTab()).toBe("catalog");

    key("Delete");
    expect(tabIds()).toEqual([]);
    expect(screen.queryByRole("tabpanel")).toBeNull();
  });

  it("offers close, pin and reopen items in a context menu", () => {
    const stores = setup(["list", "gql", "catalog", "create"], "list");
    fireEvent.contextMenu(tab("catalog"), { clientX: 40, clientY: 30 });
    const menu = screen.getByRole("menu", { name: "Tab" });
    const items = within(menu).getAllByRole("menuitem");
    const label = (el: HTMLElement) => el.getAttribute("data-menu-item");
    expect(items.map(label)).toEqual(["close", "closeOthers", "closeRight", "closeAll", "pin", "reopen"]);
    expect(items[0].textContent).toBe("Close");
    expect(items[4].textContent).toBe("Pin");
    expect(items[5].textContent).toContain("Reopen closed tab");
    expect(items[5].getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(items[0]);

    fireEvent.click(items[1]);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(tabIds()).toEqual(["catalog"]);
    expect(stores.tabs.getState().activeId).toBe("catalog");

    // With one tab, "Close others" is disabled.
    fireEvent.contextMenu(tab("catalog"), { clientX: 40, clientY: 30 });
    expect(screen.getByRole("menuitem", { name: "Close others" }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.keyDown(document.activeElement as Element, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();

    // From the keyboard: Shift+F10 on the focused tab, then "Close all".
    act(() => tab("catalog").focus());
    key("F10", { shiftKey: true });
    const closeAll = screen.getByRole("menuitem", { name: "Close all" });
    act(() => closeAll.focus());
    key("Enter");
    expect(tabIds()).toEqual([]);
    expect(stores.tabs.getState().activeId).toBeNull();
  });

  it("pins and unpins from the menu; pinned tabs come first, without a close button, and survive bulk closes", () => {
    const stores = setup(["list", "gql", "catalog", "create"], "list");
    const choose = (id: string, item: string) => {
      fireEvent.contextMenu(tab(id), { clientX: 40, clientY: 30 });
      fireEvent.click(document.querySelector(`[data-menu-item="${item}"]`) as Element);
    };
    choose("catalog", "pin");
    expect(tabIds()).toEqual(["catalog", "list", "gql", "create"]);
    expect(screen.getByRole("tab", { name: "DIR Catalog, pinned" })).toBe(tab("catalog"));
    expect(tab("catalog").querySelector("[data-close-tab]")).toBeNull();
    expect(tab("catalog").className).toMatch(/pinned/);

    // The middle button does not close a pinned tab; Delete does.
    fireEvent(tab("catalog"), new MouseEvent("auxclick", { bubbles: true, button: 1 }));
    expect(tabIds()).toContain("catalog");

    choose("gql", "closeRight");
    expect(tabIds()).toEqual(["catalog", "list", "gql"]);
    choose("gql", "closeOthers");
    expect(tabIds()).toEqual(["catalog", "gql"]);
    choose("gql", "closeAll");
    expect(tabIds()).toEqual(["catalog"]);

    fireEvent.contextMenu(tab("catalog"), { clientX: 40, clientY: 30 });
    expect(screen.getByRole("menuitem", { name: "Close all" }).getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByRole("menuitem", { name: /Reopen closed tab/ }).getAttribute("aria-disabled")).not.toBe("true");
    fireEvent.click(screen.getByRole("menuitem", { name: "Unpin" }));
    expect(tab("catalog").querySelector("[data-close-tab]")).not.toBeNull();

    // Reopen from the menu: the last closed tab comes back where it was.
    choose("catalog", "reopen");
    expect(tabIds()).toEqual(["catalog", "gql"]);
    expect(stores.tabs.getState().activeId).toBe("gql");

    act(() => tab("catalog").focus());
    act(() => stores.tabs.dispatch({ type: "tabs/setPinned", id: "catalog", pinned: true }));
    key("Delete");
    expect(tabIds()).toEqual(["gql"]);
  });

  it("shows the preview tab in italics and keeps it on double-click, from the menu or by dragging", () => {
    const stores = setup(["list", "create"], "list");
    act(() => stores.tabs.dispatch({ type: "tabs/open", id: "gql", preview: true }));
    expect(tab("gql").className).toMatch(/preview/);
    expect(screen.getByRole("tab", { name: "GQL Products, preview" })).toBe(tab("gql"));
    fireEvent.doubleClick(tab("gql"));
    expect(stores.tabs.getState().previewId).toBeNull();
    expect(tab("gql").className).not.toMatch(/preview/);

    act(() => stores.tabs.dispatch({ type: "tabs/open", id: "catalog", preview: true }));
    fireEvent.contextMenu(tab("catalog"), { clientX: 40, clientY: 30 });
    fireEvent.click(screen.getByRole("menuitem", { name: "Keep open" }));
    expect(stores.tabs.getState().previewId).toBeNull();

    act(() => stores.tabs.dispatch({ type: "tabs/close", ids: ["catalog"] }));
    act(() => stores.tabs.dispatch({ type: "tabs/open", id: "catalog", preview: true }));
    act(() => tab("catalog").focus());
    key("PageUp", { ctrlKey: true, shiftKey: true });
    expect(stores.tabs.getState()).toMatchObject({ order: ["list", "create", "catalog", "gql"], previewId: null });
  });

  it("keeps a dragged tab within its group", () => {
    const stores = setup(["list", "gql", "catalog"], "list");
    act(() => stores.tabs.dispatch({ type: "tabs/setPinned", id: "list", pinned: true }));
    stores.tabs.getState().order.forEach((id, i) => {
      const el = tab(id);
      el.getBoundingClientRect = () => ({ left: i * 100, right: i * 100 + 100, width: 100, top: 0, bottom: 40, height: 40, x: i * 100, y: 0, toJSON: () => ({}) });
      Object.defineProperty(el, "offsetLeft", { configurable: true, value: i * 100 });
      Object.defineProperty(el, "offsetWidth", { configurable: true, value: 100 });
    });
    const catalog = tab("catalog");
    fireEvent.pointerDown(catalog, { pointerId: 1, button: 0, clientX: 250 });
    fireEvent.pointerMove(catalog, { pointerId: 1, clientX: 10 }); // before the pinned tab: lands after it
    expect((document.querySelector("[class*='dropMarker']") as HTMLElement).style.left).toBe("100px");
    fireEvent.pointerUp(catalog, { pointerId: 1, clientX: 10 });
    expect(stores.tabs.getState().order).toEqual(["list", "catalog", "gql"]);
  });

  it("reorders the focused tab with the keyboard", () => {
    const stores = setup(["list", "gql", "catalog"], "list");
    act(() => tab("list").focus());
    key("PageDown", { ctrlKey: true, shiftKey: true });
    expect(stores.tabs.getState().order).toEqual(["gql", "list", "catalog"]);
    expect(focusedTab()).toBe("list");
    key("ArrowRight", { altKey: true, shiftKey: true });
    expect(tabIds()).toEqual(["gql", "catalog", "list"]);
    key("ArrowRight", { altKey: true, shiftKey: true }); // already last
    expect(tabIds()).toEqual(["gql", "catalog", "list"]);
    if (IS_MAC) key("PageUp", { metaKey: true, shiftKey: true });
    else key("ArrowLeft", { altKey: true, shiftKey: true });
    key("PageUp", { ctrlKey: true, shiftKey: true });
    expect(tabIds()).toEqual(["list", "gql", "catalog"]);
    expect(focusedTab()).toBe("list");
    expect(stores.tabs.getState().activeId).toBe("list");
  });

  it("reorders by dragging with the pointer, showing a drop marker until the drop", () => {
    const stores = setup(["list", "gql", "catalog", "create"], "list");
    // jsdom has no layout: put the tabs side by side, 100px wide each, in their current order.
    const layout = () =>
      stores.tabs.getState().order.forEach((id, i) => {
        const el = tab(id);
        el.getBoundingClientRect = () => ({ left: i * 100, right: i * 100 + 100, width: 100, top: 0, bottom: 40, height: 40, x: i * 100, y: 0, toJSON: () => ({}) });
        Object.defineProperty(el, "offsetLeft", { configurable: true, value: i * 100 });
        Object.defineProperty(el, "offsetWidth", { configurable: true, value: 100 });
      });
    layout(); // list 0–100, gql 100–200, catalog 200–300, create 300–400
    const list = tab("list");
    fireEvent.pointerDown(list, { pointerId: 1, button: 0, clientX: 50 });
    fireEvent.pointerMove(list, { pointerId: 1, clientX: 52 }); // under the threshold
    expect(list.className).not.toMatch(/dragging/);
    fireEvent.pointerMove(list, { pointerId: 1, clientX: 180 }); // past gql's middle (150)
    expect(list.className).toMatch(/dragging/);
    const marker = () => document.querySelector("[class*='dropMarker']") as HTMLElement | null;
    expect(marker()?.style.left).toBe("200px");
    expect(stores.tabs.getState().order).toEqual(["list", "gql", "catalog", "create"]);
    fireEvent.pointerMove(list, { pointerId: 2, clientX: 390 }); // another pointer: ignored
    fireEvent.pointerMove(list, { pointerId: 1, clientX: 360 }); // past create's middle (350): the end
    expect(marker()?.style.left).toBe("400px");
    fireEvent.pointerUp(list, { pointerId: 1, clientX: 360 });
    expect(stores.tabs.getState().order).toEqual(["gql", "catalog", "create", "list"]);
    expect(marker()).toBeNull();
    expect(list.className).not.toMatch(/dragging/);

    // A cancelled drag and one that returns to its place change nothing.
    layout(); // gql 0–100, catalog 100–200, create 200–300, list 300–400
    const gql = tab("gql");
    fireEvent.pointerDown(gql, { pointerId: 1, button: 0, clientX: 50 });
    fireEvent.pointerMove(gql, { pointerId: 1, clientX: 380 });
    expect(marker()?.style.left).toBe("400px");
    fireEvent.pointerCancel(gql, { pointerId: 1 });
    expect(marker()).toBeNull();
    fireEvent.pointerUp(gql, { pointerId: 1, clientX: 380 });
    fireEvent.pointerDown(gql, { pointerId: 1, button: 0, clientX: 50 });
    fireEvent.pointerMove(gql, { pointerId: 1, clientX: 380 });
    fireEvent.pointerMove(gql, { pointerId: 1, clientX: 60 });
    expect(marker()).toBeNull();
    fireEvent.pointerUp(gql, { pointerId: 1, clientX: 60 });
    expect(stores.tabs.getState().order).toEqual(["gql", "catalog", "create", "list"]);
  });

  it("updates names and badges when the tree reloads, and hides tabs whose item is gone", () => {
    const stores = setup(["list", "create"], "list");
    act(() =>
      stores.collection.dispatch({
        type: "collection/load",
        workspaceId: "ws_1",
        nodes: [node({ id: "list", name: "All products", method: "DELETE" })],
        rootIds: ["list"],
      }),
    );
    expect(tabIds()).toEqual(["list"]);
    expect(tab("list").textContent).toBe("DELAll products");
    expect(screen.getByRole("tabpanel").textContent).toBe("request editor list");
  });
});

describe("opening tabs from the tree", () => {
  it("opens requests on click and folders on Enter, into the tab bar", () => {
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: NODES, rootIds: ["catalog"], expandedIds: ["catalog"] });
    render(
      <StoreProvider stores={stores}>
        <CollectionTree />
        <TabBar />
      </StoreProvider>,
    );
    const row = (id: string) => document.querySelector(`[data-node-id="${id}"]`) as HTMLElement;
    // Single clicks share one preview tab, in italics.
    fireEvent.click(row("create"));
    expect(tabIds()).toEqual(["create"]);
    fireEvent.click(row("list"));
    expect(tabIds()).toEqual(["list"]);
    expect(tab("list").className).toMatch(/preview/);
    fireEvent.doubleClick(row("list"));
    expect(tab("list").className).not.toMatch(/preview/);
    act(() => row("list").focus());
    fireEvent.keyDown(row("list"), { key: "ArrowUp" });
    fireEvent.keyDown(row("catalog"), { key: "Enter" });
    expect(tabIds()).toEqual(["list", "catalog"]);
    expect(tab("catalog").getAttribute("aria-selected")).toBe("true");
    expect(tab("catalog").textContent).toBe("DIRCatalog");
  });
});

describe("tab keys", () => {
  const chord = (key: string, mods: Partial<Record<"metaKey" | "ctrlKey" | "altKey" | "shiftKey", boolean>> = {}) => ({
    key,
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    ...mods,
  });

  it("recognizes the move, close and menu keys", () => {
    expect(tabMoveDirection(chord("PageUp", { ctrlKey: true, shiftKey: true }))).toBe(-1);
    expect(tabMoveDirection(chord("PageDown", { metaKey: true, shiftKey: true }), true)).toBe(1);
    expect(tabMoveDirection(chord("PageDown", { ctrlKey: true, shiftKey: true }), true)).toBe(1);
    expect(tabMoveDirection(chord("PageDown", { metaKey: true, shiftKey: true }), false)).toBe(0);
    expect(tabMoveDirection(chord("ArrowLeft", { altKey: true, shiftKey: true }))).toBe(-1);
    expect(tabMoveDirection(chord("ArrowRight", { altKey: true, shiftKey: true }))).toBe(1);
    expect(tabMoveDirection(chord("PageDown", { ctrlKey: true }))).toBe(0);
    expect(tabMoveDirection(chord("ArrowRight", { shiftKey: true }))).toBe(0);
    expect(tabMoveDirection(chord("ArrowRight", { altKey: true, ctrlKey: true, shiftKey: true }))).toBe(0);
    expect(isCloseTabKey(chord("Delete"))).toBe(true);
    expect(isCloseTabKey(chord("Delete", { shiftKey: true }))).toBe(false);
    expect(isContextMenuKey(chord("ContextMenu"))).toBe(true);
    expect(isContextMenuKey(chord("F10", { shiftKey: true }))).toBe(true);
    expect(isContextMenuKey(chord("F10"))).toBe(false);
  });
});
