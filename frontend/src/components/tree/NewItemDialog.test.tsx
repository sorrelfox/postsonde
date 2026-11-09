import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  CreateFolder: vi.fn<(input: unknown) => Promise<unknown>>(),
  CreateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { installShortcuts } from "../../commands/shortcuts";
import type { NodeDTO, TreeDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode, type WorkspaceInfo } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { Sidebar } from "../shell/Sidebar";

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

// orders/ { Get order, refunds/ { } }, Health
const NODES: NodeDTO[] = [
  dto({ id: "orders", name: "Orders", kind: "folder", fileKind: "folder", method: "", path: "orders", childIds: ["get", "refunds"] }),
  dto({ id: "get", parentId: "orders", name: "Get order", path: "orders/get-order.json" }),
  dto({ id: "refunds", parentId: "orders", name: "Refunds", kind: "folder", fileKind: "folder", method: "", path: "orders/refunds" }),
  dto({ id: "health", name: "Health", path: "health.json" }),
];

function treeWith(extra: NodeDTO[] = [], readOnly = ""): TreeDTO {
  const nodes = NODES.map((n) => {
    const children = extra.filter((e) => e.parentId === n.id).map((e) => e.id);
    return children.length > 0 ? { ...n, childIds: [...(n.childIds ?? []), ...children] } : n;
  });
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly,
    rootIds: ["orders", "health", ...extra.filter((e) => e.parentId === null).map((e) => e.id)],
    nodes: [...nodes, ...extra],
    problems: [],
    scratch: [],
  } as TreeDTO;
}

function toNodes(tree: TreeDTO): TreeNode[] {
  return (tree.nodes ?? []).map((n) =>
    n.kind === "folder"
      ? { kind: "folder", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, childIds: n.childIds ?? [] }
      : { kind: "request", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, method: n.method },
  );
}

function setup({ readOnly = "", selected = null as string | null } = {}) {
  const stores: Stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  const tree = treeWith([], readOnly);
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes: toNodes(tree),
    rootIds: tree.rootIds ?? [],
    readOnly,
    expandedIds: ["orders"],
  });
  if (selected !== null) stores.collection.dispatch({ type: "collection/select", id: selected });
  render(
    <StoreProvider stores={stores}>
      <Sidebar id="sidebar" view="collections" hidden={false} />
    </StoreProvider>,
  );
  return stores;
}

const dialog = () => screen.getByRole("dialog");
const nameInput = () => within(dialog()).getByRole("textbox", { name: "Name" }) as HTMLInputElement;
const folderSelect = () => within(dialog()).getByRole("combobox", { name: "Folder" }) as HTMLSelectElement;
const confirmButton = (label: string) => within(dialog()).getByRole("button", { name: label }) as HTMLButtonElement;

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset();
  service.Tree.mockResolvedValue(treeWith());
  service.CreateFolder.mockReset();
  service.CreateRequest.mockReset();
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("New request dialog", () => {
  it("creates a GraphQL request in the selected folder and opens it in a tab", async () => {
    const stores = setup({ selected: "refunds" });
    fireEvent.click(screen.getByRole("button", { name: "New request" }));

    expect(dialog().getAttribute("aria-labelledby")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "New request" })).toBeTruthy();
    const kinds = within(dialog()).getAllByRole("radio");
    expect(kinds.map((r) => r.parentElement?.textContent)).toEqual(["HTTP", "GraphQL", "WebSocket", "TCP", "UDP", "gRPC", "SSE", "Socket.IO"]);
    expect((kinds[0] as HTMLInputElement).checked).toBe(true);
    expect(document.activeElement).toBe(nameInput());
    expect(folderSelect().value).toBe("refunds");
    expect(confirmButton("Create request").disabled).toBe(true);

    fireEvent.click(kinds[1]);
    expect(within(dialog()).getByText(/A GraphQL query or mutation/)).toBeTruthy();
    fireEvent.change(nameInput(), { target: { value: "Refund order" } });
    expect(within(dialog()).getByText("orders/refunds/refund-order.json")).toBeTruthy();

    const created = dto({ id: "refund", parentId: "refunds", name: "Refund order", fileKind: "graphql", method: "POST", path: "orders/refunds/refund-order.json" });
    service.CreateRequest.mockResolvedValueOnce({ tree: treeWith([created]), nodeId: "refund" });
    await act(async () => {
      fireEvent.click(confirmButton("Create request"));
      await settle();
    });

    expect(service.CreateRequest).toHaveBeenCalledWith({ parentId: "refunds", name: "Refund order", kind: "graphql", index: -1 });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(stores.tabs.getState()).toMatchObject({ order: ["refund"], activeId: "refund" });
    expect(stores.collection.getState().expandedIds).toMatchObject({ orders: true, refunds: true });
    expect(document.querySelector('[data-node-id="refund"]')).toBeTruthy();
  });

  it("validates the name against the chosen folder", () => {
    setup({ selected: "get" });
    fireEvent.click(screen.getByRole("button", { name: "New request" }));
    expect(folderSelect().value).toBe("orders");

    fireEvent.change(nameInput(), { target: { value: " get ORDER " } });
    expect(within(dialog()).getByRole("alert").textContent).toMatch(/already in this folder/);
    expect(nameInput().getAttribute("aria-invalid")).toBe("true");
    expect(confirmButton("Create request").disabled).toBe(true);
    // Enter does not create either.
    fireEvent.keyDown(nameInput(), { key: "Enter" });
    expect(service.CreateRequest).not.toHaveBeenCalled();

    // The same name is free at the top level.
    fireEvent.change(folderSelect(), { target: { value: "" } });
    expect(within(dialog()).queryByRole("alert")).toBeNull();
    expect(confirmButton("Create request").disabled).toBe(false);
    expect(within(dialog()).getByText("top level")).toBeTruthy();
    expect(within(dialog()).getByText("get-order.json")).toBeTruthy();
  });

  it("shows a failure in the dialog and keeps it open", async () => {
    setup();
    fireEvent.click(screen.getByRole("button", { name: "New request" }));
    fireEvent.change(nameInput(), { target: { value: "Ping" } });
    service.CreateRequest.mockRejectedValueOnce({
      kind: "permission",
      code: "collection.write_failed",
      message: "The file could not be written.",
      hint: "Check the folder's permissions.",
      details: {},
      causes: [],
    });
    await act(async () => {
      fireEvent.keyDown(nameInput(), { key: "Enter" });
      await settle();
    });
    expect(service.CreateRequest).toHaveBeenCalledWith({ parentId: "", name: "Ping", kind: "http", index: -1 });
    expect(within(dialog()).getByRole("alert").textContent).toBe("The file could not be written. Check the folder's permissions.");
    expect(confirmButton("Create request").disabled).toBe(false);

    fireEvent.change(nameInput(), { target: { value: "Ping 2" } });
    expect(within(dialog()).queryByRole("alert")).toBeNull();
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens with the New request shortcut", () => {
    const stores = setup();
    const uninstall = installShortcuts(stores);
    try {
      fireEvent.keyDown(document.body, { key: "n", code: "KeyN", metaKey: IS_MAC, ctrlKey: !IS_MAC });
      expect(screen.getByRole("heading", { name: "New request" })).toBeTruthy();
    } finally {
      uninstall();
    }
  });

  it("is not offered in a read-only project", () => {
    setup({ readOnly: "newer_version" });
    const button = screen.getByRole("button", { name: "New request" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("This project is read-only.");
    expect((screen.getByRole("button", { name: "New folder" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("New folder dialog", () => {
  it("nests a folder in the folder of the row the context menu was opened on", async () => {
    const stores = setup();
    fireEvent.contextMenu(document.querySelector('[data-node-id="refunds"]') as Element, { clientX: 20, clientY: 40 });
    const menu = screen.getByRole("menu", { name: "Collection item" });
    expect(within(menu).getAllByRole("menuitem").map((m) => m.textContent)).toEqual([
      expect.stringContaining("New request"),
      "New folder",
      "Import cURL…",
      "Import file…",
      "Rename…",
      expect.stringContaining("Duplicate"),
      "Export as HAR…",
      "Export as Postman collection…",
      "Export as zip…",
      "Run folder…",
      expect.stringContaining("Move to folder…"),
      expect.stringContaining("Move up"),
      expect.stringContaining("Move down"),
      expect.stringContaining("Delete"),
    ]);
    fireEvent.click(within(menu).getByText("New folder"));

    expect(screen.getByRole("heading", { name: "New folder" })).toBeTruthy();
    expect(within(dialog()).queryAllByRole("radio")).toHaveLength(0);
    expect(folderSelect().value).toBe("refunds");
    expect(Array.from(folderSelect().options).map((o) => o.textContent?.trim())).toEqual([
      "shop-api (top level)",
      "Orders",
      "Refunds",
    ]);
    fireEvent.change(nameInput(), { target: { value: "Partial" } });
    expect(within(dialog()).getByText("orders/refunds/partial/_folder.json")).toBeTruthy();

    const created = dto({ id: "partial", parentId: "refunds", name: "Partial", kind: "folder", fileKind: "folder", method: "", path: "orders/refunds/partial" });
    service.CreateFolder.mockResolvedValueOnce({ tree: treeWith([created]), nodeId: "partial" });
    await act(async () => {
      fireEvent.click(confirmButton("Create folder"));
      await settle();
    });

    expect(service.CreateFolder).toHaveBeenCalledWith({ parentId: "refunds", name: "Partial", index: -1 });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(stores.collection.getState()).toMatchObject({ selectedId: "partial" });
    expect(stores.collection.getState().nodes.partial?.parentId).toBe("refunds");
    expect(stores.tabs.getState().activeId).toBe("partial");
  });

  it("targets the top level from the tree's empty area and the header button without a selection", () => {
    setup();
    fireEvent.contextMenu(screen.getByRole("tree"), { clientX: 20, clientY: 400 });
    fireEvent.click(within(screen.getByRole("menu")).getByText("New folder"));
    expect(folderSelect().value).toBe("");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));

    fireEvent.click(screen.getByRole("button", { name: "New folder" }));
    expect(folderSelect().value).toBe("");
  });

  it("opens the context menu from the keyboard on the focused row", () => {
    setup();
    const row = document.querySelector('[data-node-id="get"]') as HTMLElement;
    row.focus();
    fireEvent.keyDown(row, { key: "F10", code: "F10", shiftKey: true });
    fireEvent.click(within(screen.getByRole("menu")).getByText("New folder"));
    expect(folderSelect().value).toBe("orders");
  });
});
