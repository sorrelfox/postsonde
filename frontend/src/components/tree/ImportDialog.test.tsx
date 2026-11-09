import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  ImportFormats: vi.fn<() => Promise<unknown>>(),
  PickImportFile: vi.fn<() => Promise<string>>(),
  PreviewImport: vi.fn<(input: unknown) => Promise<unknown>>(),
  Import: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const envService = vi.hoisted(() => ({ List: vi.fn<() => Promise<unknown>>() }));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: envService,
}));

import type { ImportItemDTO, ImportReportDTO, NodeDTO, TreeDTO } from "../../lib/collection";
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

const NODES: NodeDTO[] = [
  dto({ id: "orders", name: "Orders", kind: "folder", fileKind: "folder", method: "", path: "orders", childIds: [] }),
  dto({ id: "api", name: "api", kind: "folder", fileKind: "folder", method: "", path: "api", childIds: [] }),
];

function treeWith(extra: NodeDTO[] = []): TreeDTO {
  return {
    workspaceId: "ws_1",
    path: "/p/shop",
    readOnly: "",
    rootIds: ["orders", "api", ...extra.filter((e) => e.parentId === null).map((e) => e.id)],
    nodes: [...NODES, ...extra],
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

function setup(): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  const tree = treeWith();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: toNodes(tree), rootIds: tree.rootIds ?? [], readOnly: "" });
  render(
    <StoreProvider stores={stores}>
      <Sidebar id="sidebar" view="collections" hidden={false} />
    </StoreProvider>,
  );
  return stores;
}

const loc = (line = 0, path = "") => ({ line, column: 0, path });

function item(over: Partial<ImportItemDTO>): ImportItemDTO {
  return {
    kind: "request",
    name: "",
    parent: 0,
    fileKind: "",
    skippedKind: "",
    method: "",
    detail: "",
    status: "new",
    nodeId: "",
    source: loc(),
    diagnostics: [],
    ...over,
  };
}

function preview(over: Partial<ImportReportDTO> = {}): ImportReportDTO {
  return {
    format: { id: "curl", name: "cURL commands", extensions: [".sh"] },
    path: "/home/ada/api.sh",
    fileName: "api.sh",
    digest: "d1",
    intoParent: false,
    items: [
      item({ kind: "folder", name: "api", parent: -1 }),
      item({ name: "List users", fileKind: "http", method: "GET", detail: "https://api.test/users", source: loc(5, "command 1") }),
      item({
        name: "Create user",
        fileKind: "http",
        method: "POST",
        detail: "https://api.test/users",
        diagnostics: [{ code: "curl.ignored_option", severity: "warning", message: "“--retry” is ignored.", source: { line: 12, column: 4, path: "command 2" } }],
      }),
      item({
        kind: "skipped",
        name: "Imported request",
        skippedKind: "curl command",
        status: "skipped",
        diagnostics: [{ code: "curl.missing_url", severity: "warning", message: "The command has no URL.", source: loc(14, "command 3") }],
      }),
    ],
    diagnostics: [{ code: "curl.not_command", severity: "info", message: "Lines before the first curl command were ignored.", source: loc(2) }],
    counts: { folders: 1, requests: 2, environments: 0, skipped: 1, failed: 0, lossy: 1, errors: 0, warnings: 2, infos: 1 },
    ...over,
  };
}

const dialog = () => screen.getByRole("dialog");
const button = (name: string | RegExp) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;

async function click(el: Element) {
  await act(async () => {
    fireEvent.click(el);
    await settle();
  });
}

beforeEach(() => {
  resetFakeWails();
  service.Tree.mockReset().mockResolvedValue(treeWith());
  service.ImportFormats.mockReset().mockResolvedValue([{ id: "curl", name: "cURL commands", extensions: [".sh", ".txt"] }]);
  service.PickImportFile.mockReset().mockResolvedValue("/home/ada/api.sh");
  service.PreviewImport.mockReset().mockResolvedValue(preview());
  service.Import.mockReset();
  envService.List.mockReset().mockResolvedValue({ environments: [], activeId: "", problems: [], readOnly: false });
  localStorage.clear();
});

describe("Import dialog (IMP-10)", () => {
  it("previews the chosen file, imports it into a new folder and shows the report", async () => {
    const stores = setup();
    await click(screen.getByRole("button", { name: "Import file" }));
    expect(screen.getByRole("heading", { name: "Import" })).toBeTruthy();
    expect(within(dialog()).getByText(/PostSonde reads/).textContent).toMatch(/cURL commands \(.sh .txt\)/);

    await click(button("Choose file…"));
    expect(service.PickImportFile).toHaveBeenCalled();
    expect(service.PreviewImport).toHaveBeenCalledWith({ path: "/home/ada/api.sh", format: "" });

    // The preview: file, format, summary, the tree and the file's notes.
    expect(within(dialog()).getByText("api.sh")).toBeTruthy();
    expect(within(dialog()).getByText(/cURL commands \(detected\)/)).toBeTruthy();
    expect(within(dialog()).getByText("Creates 1 folder and 2 requests · 1 skipped · 1 with warnings")).toBeTruthy();
    expect(within(dialog()).getByText("Lines before the first curl command were ignored.")).toBeTruthy();
    const tree = within(dialog()).getByRole("list", { name: "What the import creates" });
    expect(within(tree).getByText("List users")).toBeTruthy();
    expect(within(tree).getByText("SKIP")).toBeTruthy();
    // "api" is taken at the top level: the folder gets a free name.
    const name = within(dialog()).getByRole("textbox", { name: "Folder name" }) as HTMLInputElement;
    expect(name.value).toBe("api 2");
    expect(within(tree).getByText("api 2")).toBeTruthy();

    // A row's diagnostics open with its toggle, with their place in the file.
    const toggle = within(tree).getAllByRole("button", { name: /1/ })[0];
    fireEvent.click(toggle);
    expect(within(tree).getByText("“--retry” is ignored.")).toBeTruthy();
    expect(within(tree).getByText(/line 12, column 4 · command 2/)).toBeTruthy();

    const created = dto({ id: "fld_new", name: "api 2", kind: "folder", fileKind: "folder", method: "", path: "api-2", childIds: ["r1"] });
    const req = dto({ id: "r1", parentId: "fld_new", name: "List users", path: "api-2/list-users.json" });
    const report = preview({
      items: (preview().items ?? []).map((i, index) => ({ ...i, status: i.status === "new" ? "imported" : i.status, nodeId: index === 0 ? "fld_new" : "" })),
    });
    service.Import.mockResolvedValueOnce({ tree: treeWith([created, req]), nodeId: "fld_new", report });
    await click(button("Import"));
    expect(service.Import).toHaveBeenCalledWith({
      path: "/home/ada/api.sh",
      format: "curl",
      digest: "d1",
      parentId: "",
      folderName: "api 2",
      intoParent: false,
      skipEnvironments: false,
      index: -1,
    });
    // The new folder is revealed, expanded and selected.
    expect(stores.collection.getState().selectedId).toBe("fld_new");
    expect(stores.collection.getState().expandedIds.fld_new).toBe(true);

    // The report.
    expect(screen.getByRole("heading", { name: "Import report" })).toBeTruthy();
    expect(within(dialog()).getByRole("status").textContent).toBe("Imported 1 folder and 2 requests. 1 skipped · 1 with warnings.");
    const problems = within(dialog()).getByRole("list", { name: "Skipped, failed and changed items" });
    expect(within(problems).getByText("Create user")).toBeTruthy();
    expect(within(problems).getByText("The command has no URL.")).toBeTruthy();
    expect(within(problems).queryByText("Lines before the first curl command were ignored.")).toBeNull();
    fireEvent.click(within(dialog()).getByRole("checkbox", { name: "Show notes too" }));
    expect(within(dialog()).getByText("Lines before the first curl command were ignored.")).toBeTruthy();
    expect(within(dialog()).queryByRole("button", { name: "Cancel" })).toBeNull();
    fireEvent.click(button("Done"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("imports straight into the chosen folder without a new one", async () => {
    setup();
    fireEvent.contextMenu(document.querySelector('[data-node-id="orders"]') as Element);
    await click(screen.getByRole("menuitem", { name: "Import file…" }));
    await click(button("Choose file…"));
    fireEvent.click(within(dialog()).getByRole("checkbox", { name: "Create a folder for the import" }));
    expect(within(dialog()).queryByRole("textbox", { name: "Folder name" })).toBeNull();
    service.Import.mockResolvedValueOnce({ tree: treeWith(), nodeId: "orders", report: preview({ intoParent: true }) });
    await click(button("Import"));
    expect(service.Import).toHaveBeenCalledWith(expect.objectContaining({ parentId: "orders", intoParent: true, folderName: "" }));
  });

  it("shows why a file cannot be read, and previews again when the file changed", async () => {
    setup();
    await click(screen.getByRole("button", { name: "Import file" }));
    service.PreviewImport.mockRejectedValueOnce({
      kind: "unsupported",
      code: "import.unknown_format",
      message: "PostSonde does not recognise the format of this file.",
      hint: "",
      details: {},
    });
    await click(button("Choose file…"));
    expect(within(dialog()).getByRole("alert").textContent).toMatch(/does not recognise/);

    // Cancelled picker: nothing happens.
    service.PickImportFile.mockResolvedValueOnce("");
    await click(button("Choose file…"));
    expect(service.PreviewImport).toHaveBeenCalledTimes(1);

    await click(button("Choose file…"));
    service.Import.mockRejectedValueOnce({ kind: "invalid_argument", code: "import.source_changed", message: "The file changed after it was previewed.", hint: "", details: {} });
    await click(button("Import"));
    expect(service.PreviewImport).toHaveBeenCalledTimes(3);
    expect(within(dialog()).getByRole("alert").textContent).toMatch(/changed since it was previewed/);
    expect(screen.getByRole("heading", { name: "Import" })).toBeTruthy();
  });

  it("offers to leave environments out and reloads them after an import", async () => {
    setup();
    service.PreviewImport.mockResolvedValue(
      preview({
        items: [item({ kind: "folder", name: "env", parent: -1 }), item({ kind: "environment", name: "Staging", parent: -1 })],
        diagnostics: [],
        counts: { folders: 1, requests: 0, environments: 1, skipped: 0, failed: 0, lossy: 0, errors: 0, warnings: 0, infos: 0 },
      }),
    );
    await click(screen.getByRole("button", { name: "Import file" }));
    await click(button("Choose file…"));
    // Only environments: no folder to name or place.
    expect(within(dialog()).queryByRole("textbox", { name: "Folder name" })).toBeNull();
    const envBox = within(dialog()).getByRole("checkbox", { name: "Import the environment" }) as HTMLInputElement;
    fireEvent.click(envBox);
    expect(button("Import").disabled).toBe(true);
    fireEvent.click(envBox);
    service.Import.mockResolvedValueOnce({
      tree: treeWith(),
      nodeId: "",
      report: preview({ intoParent: true, items: [], diagnostics: [], counts: { folders: 0, requests: 0, environments: 1, skipped: 0, failed: 0, lossy: 0, errors: 0, warnings: 0, infos: 0 } }),
    });
    await click(button("Import"));
    expect(service.Import).toHaveBeenCalledWith(expect.objectContaining({ skipEnvironments: false }));
    expect(envService.List).toHaveBeenCalled();
    expect(within(dialog()).getByText("Everything was imported as it was.")).toBeTruthy();
  });
});
