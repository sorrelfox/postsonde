import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const commands = vi.hoisted(() => ({
  createScratchRequest: vi.fn<(stores: unknown, kind?: string) => Promise<string>>(),
  deleteScratchRequest: vi.fn<(stores: unknown, id: string) => Promise<void>>(),
  saveScratchTo: vi.fn<(stores: unknown, id: string, parentId: string | null, name: string) => Promise<string>>(),
}));
vi.mock("../../commands/collection", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../commands/collection")>()),
  ...commands,
}));

import { ConfirmHost } from "../common/Dialog";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { NoTabState } from "./NoTabState";
import { TabBar, tabElementId } from "./TabBar";

const SCRATCH = "req_00000000000000aa";

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "http", readOnly: "", method: "GET", ...over }) as TreeNode;

function setup(ui: "tabs" | "empty", open: string[] = []): Stores {
  const stores = createStores();
  stores.workspace.dispatch({
    type: "workspace/setCurrent",
    workspace: { id: "ws_1", name: "Shop", path: "/p/shop", localDir: "/d", localStatus: "existing", previousPath: "", newerFormat: false },
  });
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes: [
      node({ id: "api", kind: "folder", name: "API", path: "api", fileKind: "", childIds: [] } as Partial<TreeNode> & { id: string }),
      node({ id: "health", name: "Health" }),
      node({ id: SCRATCH, name: "Scratch 1", path: "", method: "POST" }),
    ],
    rootIds: ["api", "health"],
    scratchIds: [SCRATCH],
  });
  stores.tabs.dispatch({ type: "tabs/restore", order: open, activeId: open[0] ?? null });
  render(
    <StoreProvider stores={stores}>
      {ui === "tabs" ? <TabBar /> : <NoTabState />}
      <ConfirmHost />
    </StoreProvider>,
  );
  return stores;
}

describe("scratch requests (COL-06)", () => {
  it("offers every request kind from the tab bar's +", () => {
    commands.createScratchRequest.mockResolvedValue(SCRATCH);
    setup("tabs");
    fireEvent.click(screen.getByRole("button", { name: "New scratch request" }));
    const menu = screen.getByRole("menu", { name: "New scratch request" });
    expect(within(menu).getAllByRole("menuitem").map((el) => el.textContent)).toEqual([
      "HTTP request",
      "GraphQL request",
      "WebSocket request",
      "TCP request",
      "UDP request",
      "gRPC request",
      "SSE request",
      "Socket.IO request",
    ]);
    fireEvent.click(within(menu).getByRole("menuitem", { name: "WebSocket request" }));
    expect(commands.createScratchRequest).toHaveBeenCalledWith(expect.anything(), "websocket");
  });

  it("marks a scratch tab and saves it to a folder from its menu", async () => {
    commands.saveScratchTo.mockResolvedValue(SCRATCH);
    setup("tabs", ["health", SCRATCH]);
    const tab = document.getElementById(tabElementId(SCRATCH)) as HTMLElement;
    expect(tab.getAttribute("aria-label")).toBe("POST Scratch 1, scratch request");
    expect(tab.textContent).toContain("scratch");
    fireEvent.contextMenu(document.getElementById(tabElementId("health")) as HTMLElement);
    expect(screen.queryByRole("menuitem", { name: "Save to…" })).toBeNull();
    fireEvent.keyDown(document.activeElement as Element, { key: "Escape" });

    fireEvent.contextMenu(tab);
    fireEvent.click(screen.getByRole("menuitem", { name: "Save to…" }));
    const dialog = screen.getByRole("dialog", { name: "Save to…" });
    const name = within(dialog).getByLabelText("Name") as HTMLInputElement;
    expect(name.value).toBe("Scratch 1");
    expect(within(dialog).queryByRole("radiogroup")).toBeNull();
    fireEvent.change(name, { target: { value: "Health" } });
    expect(within(dialog).getByRole("alert").textContent).toBe("An item with this name is already in this folder.");
    fireEvent.change(within(dialog).getByLabelText("Folder"), { target: { value: "api" } });
    expect(within(dialog).queryByRole("alert")).toBeNull();
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Save request" }));
    });
    expect(commands.saveScratchTo).toHaveBeenCalledWith(expect.anything(), SCRATCH, "api", "Health");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("lists scratch requests in the empty state, opens and deletes them", async () => {
    commands.createScratchRequest.mockResolvedValue(SCRATCH);
    commands.deleteScratchRequest.mockResolvedValue();
    const stores = setup("empty");
    fireEvent.click(screen.getByRole("button", { name: "New scratch request" }));
    expect(commands.createScratchRequest).toHaveBeenCalledWith(expect.anything(), "http");

    const list = screen.getByRole("region", { name: "Scratch requests" });
    fireEvent.click(within(list).getByRole("button", { name: "Open Scratch 1" }));
    expect(stores.tabs.getState()).toMatchObject({ order: [SCRATCH], activeId: SCRATCH });

    fireEvent.click(within(list).getByRole("button", { name: "Delete Scratch 1" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete scratch request “Scratch 1”?" });
    await act(async () => {
      fireEvent.click(within(confirm).getByRole("button", { name: "Delete" }));
    });
    expect(commands.deleteScratchRequest).toHaveBeenCalledWith(expect.anything(), SCRATCH);
  });
});
