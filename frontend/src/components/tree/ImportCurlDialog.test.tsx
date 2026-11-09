import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  ParseCurl: vi.fn<(text: string) => Promise<unknown>>(),
  ImportCurl: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { openImportCurlDialog } from "../../commands/curlImport";
import type { CurlImportDTO, NodeDTO, TreeDTO } from "../../lib/collection";
import { createStores, StoreProvider, type Stores, type TreeNode, type WorkspaceInfo } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { Sidebar } from "../shell/Sidebar";
import { PARSE_DELAY_MS } from "./ImportCurlDialog";

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
const commandInput = () => within(dialog()).getByRole("textbox", { name: "cURL command" }) as HTMLTextAreaElement;
const nameInput = () => within(dialog()).getByRole("textbox", { name: "Name" }) as HTMLInputElement;
const folderSelect = () => within(dialog()).getByRole("combobox", { name: "Folder" }) as HTMLSelectElement;
const importButton = () => within(dialog()).getByRole("button", { name: "Import request" }) as HTMLButtonElement;

function parsed(over: Partial<CurlImportDTO> = {}): CurlImportDTO {
  return {
    ok: true,
    name: "Get order",
    request: {
      id: "",
      kind: "http",
      name: "",
      description: "",
      method: "POST",
      url: "https://api.example.com/orders/get-order",
      params: [],
      pathParams: [],
      headers: [{ name: "content-type", value: "application/json", description: "", enabled: true, extra: "" }],
      body: { mode: "raw", type: "json", text: "{}", fields: [], parts: [], path: "", contentType: "", extra: "" },
      auth: { type: "basic", username: "a", password: "b", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    } as unknown as CurlImportDTO["request"],
    diagnostics: [{ code: "curl.not_imported", severity: "warning", from: 5, to: 12, message: "“--http2” is not imported." }],
    ...over,
  };
}

/** Waits for the delayed parse and its answer. */
async function parse() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, PARSE_DELAY_MS + 20));
    await settle();
  });
}

/** Types into the command field and waits for the parse. */
async function typeCommand(text: string) {
  fireEvent.change(commandInput(), { target: { value: text } });
  await parse();
}

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset().mockResolvedValue(treeWith());
  service.ParseCurl.mockReset().mockImplementation(() => Promise.resolve(parsed()));
  service.ImportCurl.mockReset();
  localStorage.clear();
});


describe("Import cURL dialog (IMP-01)", () => {
  it("shows what the command imports as, with its diagnostics, and creates the request in the selected folder", async () => {
    const stores = setup({ selected: "refunds" });
    fireEvent.click(screen.getByRole("button", { name: "Import cURL" }));
    expect(screen.getByRole("heading", { name: "Import cURL" })).toBeTruthy();
    expect(document.activeElement).toBe(commandInput());
    expect(folderSelect().value).toBe("refunds");
    expect(importButton().disabled).toBe(true);

    const command = "curl --http2 https://api.example.com/orders/get-order -u a:b --json '{}'";
    await typeCommand(command);
    expect(service.ParseCurl).toHaveBeenLastCalledWith(command);
    expect(within(dialog()).getByText(/POST https:\/\/api.example.com\/orders\/get-order · 1 header · JSON body · Basic auth/)).toBeTruthy();
    expect(within(dialog()).getByText("1 warning")).toBeTruthy();
    // The suggested name, the path it gets.
    expect(nameInput().value).toBe("Get order");
    expect(within(dialog()).getByText("orders/refunds/get-order.json")).toBeTruthy();

    // A diagnostic selects its text.
    fireEvent.click(within(dialog()).getByRole("button", { name: /--http2/ }));
    expect(document.activeElement).toBe(commandInput());
    expect([commandInput().selectionStart, commandInput().selectionEnd]).toEqual([5, 12]);

    const created = dto({ id: "imp", parentId: "refunds", name: "Get order", method: "POST", path: "orders/refunds/get-order.json" });
    service.ImportCurl.mockResolvedValueOnce({ tree: treeWith([created]), nodeId: "imp" });
    await act(async () => {
      fireEvent.click(importButton());
      await settle();
    });
    expect(service.ImportCurl).toHaveBeenCalledWith({ parentId: "refunds", name: "Get order", text: command, index: -1 });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(stores.tabs.getState()).toMatchObject({ order: ["imp"], activeId: "imp" });
  });

  it("does not import a command with an error, and picks a free name", async () => {
    setup({ selected: "get" });
    fireEvent.click(screen.getByRole("button", { name: "Import cURL" }));
    service.ParseCurl.mockImplementation(() =>
      Promise.resolve(parsed({ ok: false, diagnostics: [{ code: "curl.missing_url", severity: "error", from: 0, to: 4, message: "The command has no URL." }] })),
    );
    await typeCommand("curl -X POST");
    expect(within(dialog()).getByText("The command has no URL.")).toBeTruthy();
    expect(within(dialog()).getByText("1 error")).toBeTruthy();
    expect(commandInput().getAttribute("aria-invalid")).toBe("true");
    expect(importButton().disabled).toBe(true);

    service.ParseCurl.mockImplementation(() => Promise.resolve(parsed()));
    await typeCommand("curl https://api.example.com/orders/get-order");
    // "Get order" is taken in orders/.
    expect(nameInput().value).toBe("Get order 2");
    expect(importButton().disabled).toBe(false);
    fireEvent.change(nameInput(), { target: { value: "get ORDER" } });
    expect(within(dialog()).getByRole("alert").textContent).toMatch(/already in this folder/);
    expect(importButton().disabled).toBe(true);
  });

  it("opens with a command from openImportCurlDialog", async () => {
    setup();
    act(() => {
      expect(openImportCurlDialog("curl https://h/x")).toBe(true);
    });
    expect(commandInput().value).toBe("curl https://h/x");
    await parse();
    expect(service.ParseCurl).toHaveBeenCalledWith("curl https://h/x");
  });

  it("is offered in the tree's context menu", () => {
    setup();
    fireEvent.contextMenu(document.querySelector('[data-node-id="refunds"]') as Element);
    fireEvent.click(screen.getByRole("menuitem", { name: "Import cURL…" }));
    expect(folderSelect().value).toBe("refunds");
  });
});
