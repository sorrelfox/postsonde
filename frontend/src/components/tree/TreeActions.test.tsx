import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  Rename: vi.fn<(input: unknown) => Promise<unknown>>(),
  Duplicate: vi.fn<(id: string) => Promise<unknown>>(),
  Delete: vi.fn<(id: string) => Promise<unknown>>(),
  Restore: vi.fn<(trashId: string) => Promise<unknown>>(),
  ExportPostman: vi.fn<(input: unknown) => Promise<unknown>>(),
  ExportFolderZip: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

const httpService = vi.hoisted(() => ({
  CurlCommand: vi.fn<(input: unknown) => Promise<unknown>>(),
  ExportHAR: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const historyService = vi.hoisted(() => ({
  ExportHAR: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: httpService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({
  HistoryService: historyService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { installShortcuts } from "../../commands/shortcuts";
import { toaster } from "../../commands/toasts";
import type { NodeDTO, TreeDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode, type WorkspaceInfo } from "../../stores";
import { fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { ToastHost } from "../common/Toast";
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

// Orders/ { Get order (GET), Ship order (POST) }, Empty/, Health
const NODES: NodeDTO[] = [
  dto({ id: "orders", name: "Orders", kind: "folder", fileKind: "folder", method: "", path: "orders", childIds: ["get", "ship"] }),
  dto({ id: "get", parentId: "orders", name: "Get order", path: "orders/get-order.json" }),
  dto({ id: "ship", parentId: "orders", name: "Ship order", method: "POST", path: "orders/ship-order.json" }),
  dto({ id: "empty", name: "Empty", kind: "folder", fileKind: "folder", method: "", path: "empty" }),
  dto({ id: "health", name: "Health", path: "health.json" }),
];

function tree(nodes: NodeDTO[] = NODES): TreeDTO {
  const ids = new Set(nodes.map((n) => n.id));
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["orders", "empty", "health"].filter((id) => ids.has(id)),
    nodes,
    problems: [],
    scratch: [],
  } as TreeDTO;
}

const renamed = (id: string, name: string, path: string) => tree(NODES.map((n) => (n.id === id ? { ...n, name, path } : n)));
const without = (...ids: string[]) => tree(NODES.filter((n) => !ids.includes(n.id)));

function toNodes(t: TreeDTO): TreeNode[] {
  return (t.nodes ?? []).map((n) =>
    n.kind === "folder"
      ? { kind: "folder", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, childIds: n.childIds ?? [] }
      : { kind: "request", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, method: n.method },
  );
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
const menuItem = (id: string) => within(screen.getByRole("menu")).getAllByRole("menuitem").find((m) => m.dataset.menuItem === id) as HTMLElement;
const key = (el: Element, k: string, mod = false) =>
  fireEvent.keyDown(el, { key: k, metaKey: mod && IS_MAC, ctrlKey: mod && !IS_MAC });
const entry = (id: string, name: string) => ({ id: `trash_${id}`, nodeId: id, kind: "request", fileKind: "http", name, path: "", parentId: null, index: 0 });

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset();
  service.Tree.mockResolvedValue(tree());
  for (const fn of [service.Rename, service.Duplicate, service.Delete, service.Restore]) fn.mockReset();
  httpService.ExportHAR.mockReset();
  historyService.ExportHAR.mockReset();
  localStorage.clear();
});

afterEach(() => {
  act(() => toaster.clear());
});

describe("inline rename", () => {
  it("renames the focused item with F2 and Enter, and the open tab follows", async () => {
    const stores = setup();
    act(() => {
      stores.tabs.dispatch({ type: "tabs/open", id: "health" });
    });
    act(() => row("health").focus());
    key(row("health"), "F2");

    const input = within(row("health")).getByRole("textbox", { name: "Request name" }) as HTMLInputElement;
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, "Health".length]);

    fireEvent.change(input, { target: { value: "orders" } });
    expect(within(row("health")).getByRole("alert").textContent).toBe("An item with this name is already in this folder.");
    key(input, "Enter");
    expect(service.Rename).not.toHaveBeenCalled();

    service.Rename.mockResolvedValueOnce({ tree: renamed("health", "Status", "status.json"), nodeId: "health" });
    fireEvent.change(input, { target: { value: " Status " } });
    key(input, "Enter");
    await act(settle);

    expect(service.Rename).toHaveBeenCalledWith({ id: "health", name: "Status" });
    expect(within(row("health")).queryByRole("textbox")).toBeNull();
    expect(row("health").textContent).toContain("Status");
    expect(document.activeElement).toBe(row("health"));
    expect(stores.collection.getState().nodes.health.name).toBe("Status");
  });

  it("cancels with Escape and when leaving an unchanged name", async () => {
    setup();
    act(() => row("orders").focus());
    key(row("orders"), "F2");
    const input = within(row("orders")).getByRole("textbox", { name: "Folder name" });
    fireEvent.change(input, { target: { value: "Sales" } });
    key(input, "Escape");
    expect(within(row("orders")).queryByRole("textbox")).toBeNull();
    expect(document.activeElement).toBe(row("orders"));

    key(row("orders"), "F2");
    fireEvent.blur(within(row("orders")).getByRole("textbox"));
    expect(within(row("orders")).queryByRole("textbox")).toBeNull();
    expect(service.Rename).not.toHaveBeenCalled();
  });

  it("keeps the field open with Go's error", async () => {
    setup();
    act(() => row("health").focus());
    key(row("health"), "F2");
    const input = within(row("health")).getByRole("textbox");
    service.Rename.mockRejectedValueOnce({ kind: "io", code: "collection.read_only", message: "The file is read-only.", hint: "", details: {}, causes: [] });
    fireEvent.change(input, { target: { value: "Status" } });
    key(input, "Enter");
    await act(settle);
    expect(within(row("health")).getByRole("alert").textContent).toBe("The file is read-only.");
    expect(within(row("health")).getByRole("textbox")).toBeTruthy();
  });
});

describe("context menu", () => {
  it("renames in the dialog, showing where the file moves", async () => {
    setup();
    fireEvent.contextMenu(row("get"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("rename"));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Rename request" })).toBeTruthy();
    const input = within(dialog).getByRole("textbox", { name: "Name" }) as HTMLInputElement;
    expect(input.value).toBe("Get order");
    expect((within(dialog).getByRole("button", { name: "Rename" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(input, { target: { value: "Cancel an order" } });
    expect(within(dialog).getByText("The file is renamed too")).toBeTruthy();
    expect(dialog.textContent).toContain("orders/get-order.json → orders/cancel-an-order.json");

    service.Rename.mockResolvedValueOnce({ tree: renamed("get", "Cancel an order", "orders/cancel-an-order.json"), nodeId: "get" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    await act(settle);
    expect(service.Rename).toHaveBeenCalledWith({ id: "get", name: "Cancel an order" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("duplicates the item and selects the copy", async () => {
    const stores = setup();
    const copy = dto({ id: "health2", name: "Health copy", path: "health-copy.json" });
    service.Duplicate.mockResolvedValueOnce({ tree: { ...tree([...NODES, copy]), rootIds: ["orders", "empty", "health", "health2"] }, nodeId: "health2" });
    fireEvent.contextMenu(row("health"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("duplicate"));
    await act(settle);
    expect(service.Duplicate).toHaveBeenCalledWith("health");
    expect(stores.collection.getState().selectedId).toBe("health2");
  });

  it("copies a request as cURL, not a folder", async () => {
    setup();
    httpService.CurlCommand.mockResolvedValueOnce({ command: "curl https://api.test/orders/1", masked: false, unresolved: [] });
    fireEvent.contextMenu(row("get"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("copyCurl"));
    await act(settle);
    expect(httpService.CurlCommand).toHaveBeenCalledWith({ requestId: "get", includeSecrets: false });
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenCalledWith("curl https://api.test/orders/1");
    expect(toaster.toasts()[0]?.message).toBe("Copied as cURL.");

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    expect(menuItem("copyCurl")).toBeUndefined();
  });

  it("exports a folder, a request's history or all of it as HAR", async () => {
    setup();
    httpService.ExportHAR.mockResolvedValueOnce({ saved: true, path: "/o.har", entries: 1, masked: false, skipped: [] });
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    expect(menuItem("exportHistoryHar")).toBeUndefined();
    fireEvent.click(menuItem("exportHar"));
    await act(settle);
    expect(httpService.ExportHAR).toHaveBeenCalledWith(expect.objectContaining({ name: "Orders", includeSecrets: false }));
    expect(toaster.toasts()[0]?.message).toBe("Exported 1 request as HAR.");

    historyService.ExportHAR.mockResolvedValue({ saved: false, path: "", entries: 0, masked: false });
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(row("get"), { clientX: 10, clientY: 10 });
    expect(menuItem("exportHar")).toBeUndefined();
    fireEvent.click(menuItem("exportHistoryHar"));
    await act(settle);
    expect(historyService.ExportHAR).toHaveBeenLastCalledWith(expect.objectContaining({ requestId: "get" }));

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(screen.getByRole("tree"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("exportHistoryHar"));
    await act(settle);
    expect(historyService.ExportHAR).toHaveBeenLastCalledWith({ ids: [], requestId: "", title: "" });
  });

  it("exports a folder or the project as a Postman collection", async () => {
    setup();
    service.ExportPostman.mockResolvedValue({ saved: true, path: "/o.json", name: "Orders", folders: 0, requests: 1, skipped: 0, warnings: 0, notes: [] });
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("exportPostman"));
    await act(settle);
    expect(service.ExportPostman).toHaveBeenCalledWith({ id: "orders" });
    expect(toaster.toasts()[0]?.message).toBe("Exported 1 request as a Postman collection.");

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(row("get"), { clientX: 10, clientY: 10 });
    expect(menuItem("exportPostman")).toBeUndefined();
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(screen.getByRole("tree"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("exportPostmanProject"));
    await act(settle);
    expect(service.ExportPostman).toHaveBeenLastCalledWith({ id: "" });
  });

  it("exports a folder as a zip", async () => {
    setup();
    service.ExportFolderZip.mockResolvedValue({
      saved: true, path: "/o.zip", name: "Orders", folders: 1, requests: 1, skipped: 0, secretsRemoved: 0, warnings: 0, notes: [],
    });
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("exportZip"));
    await act(settle);
    expect(service.ExportFolderZip).toHaveBeenCalledWith({ id: "orders" });
    expect(toaster.toasts()[0]?.message).toBe("Exported 1 request as a zip, without secrets.");

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    fireEvent.contextMenu(row("get"), { clientX: 10, clientY: 10 });
    expect(menuItem("exportZip")).toBeUndefined();
  });

  it("offers no item actions on the empty area", () => {
    setup();
    fireEvent.contextMenu(screen.getByRole("tree"), { clientX: 10, clientY: 10 });
    expect(within(screen.getByRole("menu")).queryByText("Delete")).toBeNull();
  });
});

describe("delete with undo", () => {
  it("deletes a request with Delete, closes its tab, and Undo brings both back", async () => {
    const stores = setup();
    act(() => {
      stores.tabs.dispatch({ type: "tabs/open", id: "health" });
    });
    service.Delete.mockResolvedValueOnce({ tree: without("health"), entry: entry("health", "Health") });
    act(() => row("health").focus());
    key(row("health"), "Delete");
    await act(settle);

    expect(service.Delete).toHaveBeenCalledWith("health");
    expect(row("health")).toBeNull();
    expect(stores.tabs.getState().order).toEqual([]);
    // Focus moves to the row that took its place (the last one: Empty).
    expect(document.activeElement).toBe(row("empty"));

    const toast = screen.getByText("Deleted “Health”").closest("[data-toast-id]") as HTMLElement;
    expect(within(toast).getByText(IS_MAC ? "⌘Z" : "Ctrl+Z")).toBeTruthy();
    service.Restore.mockResolvedValueOnce({ tree: tree(), nodeId: "health", toRoot: false });
    fireEvent.click(within(toast).getByRole("button", { name: "Undo" }));
    await act(settle);

    expect(service.Restore).toHaveBeenCalledWith("trash_health");
    expect(row("health")).toBeTruthy();
    expect(stores.tabs.getState()).toMatchObject({ order: ["health"], activeId: "health" });
    expect(screen.queryByText("Deleted “Health”")).toBeNull();
  });

  it("deletes an empty folder without asking; ⌘Z undoes it outside text fields", async () => {
    const stores = setup();
    const uninstall = installShortcuts(stores);
    try {
      service.Delete.mockResolvedValueOnce({ tree: without("empty"), entry: entry("empty", "Empty") });
      act(() => row("empty").focus());
      key(row("empty"), "Backspace");
      await act(settle);
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(row("empty")).toBeNull();

      service.Restore.mockResolvedValueOnce({ tree: tree(), nodeId: "empty", toRoot: false });
      const field = screen.getByRole("searchbox", { hidden: true });
      key(field, "z", true);
      expect(service.Restore).not.toHaveBeenCalled();
      key(document.body, "z", true);
      await act(settle);
      expect(service.Restore).toHaveBeenCalledWith("trash_empty");
      expect(row("empty")).toBeTruthy();

      // The toast is gone: ⌘Z does nothing more.
      key(document.body, "z", true);
      expect(service.Restore).toHaveBeenCalledTimes(1);
    } finally {
      uninstall();
    }
  });

  it("asks before deleting a folder with contents, listing them", async () => {
    setup();
    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("delete"));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Delete folder “Orders”?" })).toBeTruthy();
    expect(dialog.textContent).toContain("The folder and the 2 requests in it move to this project's trash.");
    const items = within(within(dialog).getByRole("list")).getAllByRole("listitem").map((li) => li.textContent);
    expect(items).toEqual(["GETGet order", "POSTShip order"]);
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Cancel" }));

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await act(settle);
    expect(service.Delete).not.toHaveBeenCalled();

    fireEvent.contextMenu(row("orders"), { clientX: 10, clientY: 10 });
    fireEvent.click(menuItem("delete"));
    service.Delete.mockResolvedValueOnce({ tree: without("orders", "get", "ship"), entry: entry("orders", "Orders") });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete folder" }));
    await act(settle);
    expect(service.Delete).toHaveBeenCalledWith("orders");
    expect(screen.getByText("Deleted “Orders”")).toBeTruthy();
  });

  it("shows a failed delete as a notification", async () => {
    setup();
    service.Delete.mockRejectedValueOnce({ kind: "io", code: "x", message: "Permission denied.", hint: "", details: {}, causes: [] });
    act(() => row("health").focus());
    key(row("health"), "Delete");
    await act(settle);
    expect(screen.getByText("Could not delete “Health”. Permission denied.")).toBeTruthy();
    expect(row("health")).toBeTruthy();
  });
});
