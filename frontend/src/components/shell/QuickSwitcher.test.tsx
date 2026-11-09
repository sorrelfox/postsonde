import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: service,
}));

import { onCommandPaletteRequest } from "../../commands/palette";
import { installShortcuts } from "../../commands/shortcuts";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, initialEnvironmentsState, StoreProvider, type Environment, type Stores, type TreeNode } from "../../stores";
import { QuickSwitcher } from "./QuickSwitcher";

const NODES: TreeNode[] = [
  { kind: "folder", id: "users", parentId: null, name: "Users", path: "users", fileKind: "", readOnly: "", childIds: ["admin", "get"] },
  { kind: "folder", id: "admin", parentId: "users", name: "Admin", path: "users/admin", fileKind: "", readOnly: "", childIds: ["del"] },
  { kind: "request", id: "del", parentId: "admin", name: "Delete user", path: "users/admin/del.json", fileKind: "http", readOnly: "", method: "DELETE" },
  { kind: "request", id: "get", parentId: "users", name: "Get user", path: "users/get.json", fileKind: "http", readOnly: "", method: "GET" },
];

const staging: Environment = { id: "env_s", name: "Staging", color: "", fileName: "staging.json", readOnly: false, variables: [] };

let stopShortcuts: (() => void) | null = null;

function setup(): Stores {
  const stores = createStores({
    environments: { ...initialEnvironmentsState, status: "ready", ids: ["env_s"], byId: { env_s: staging } },
  });
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: NODES, rootIds: ["users"] });
  stopShortcuts = installShortcuts(stores);
  render(
    <StoreProvider stores={stores}>
      <button type="button">elsewhere</button>
      <QuickSwitcher />
    </StoreProvider>,
  );
  return stores;
}

const pressOpen = () => fireEvent.keyDown(document.activeElement ?? window, { key: "p", code: "KeyP", metaKey: IS_MAC, ctrlKey: !IS_MAC });
const input = () => screen.getByRole("combobox", { name: "Jump to request, folder or environment" }) as HTMLInputElement;
const options = () => within(screen.getByRole("listbox")).getAllByRole("option");
const key = (k: string) => fireEvent.keyDown(input(), { key: k });

beforeEach(() => {
  service.SetActive.mockReset();
  service.SetActive.mockResolvedValue(undefined);
});

afterEach(() => {
  stopShortcuts?.();
  stopShortcuts = null;
});

describe("QuickSwitcher", () => {
  it("is disabled without a project", () => {
    render(
      <StoreProvider stores={createStores()}>
        <QuickSwitcher />
      </StoreProvider>,
    );
    expect(screen.getByRole("button", { name: "Jump to request, folder or environment" })).toHaveProperty("disabled", true);
  });

  it("opens with the shortcut, filters fuzzily and opens a request with Enter", () => {
    const stores = setup();
    const before = screen.getByRole("button", { name: "elsewhere" });
    before.focus();
    act(() => {
      pressOpen();
    });
    expect(document.activeElement).toBe(input());
    expect(input().getAttribute("aria-expanded")).toBe("true");
    // Everything is listed before typing.
    expect(options()).toHaveLength(5);

    fireEvent.change(input(), { target: { value: "dlu" } });
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual(["Delete user, DEL, in Users / Admin"]);
    const first = options()[0];
    expect(first.getAttribute("aria-selected")).toBe("true");
    expect(input().getAttribute("aria-activedescendant")).toBe(first.id);
    expect(Array.from(first.querySelectorAll("mark")).map((m) => m.textContent)).toEqual(["D", "l", "u"]);
    expect(first.textContent).toContain("Users / Admin");

    key("Enter");
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(stores.tabs.getState().activeId).toBe("del");
    expect(stores.tabs.getState().previewId).toBeNull();
    expect(stores.collection.getState().selectedId).toBe("del");
    expect(stores.collection.getState().expandedIds).toMatchObject({ users: true, admin: true });
    expect(document.activeElement).toBe(before);
  });

  it("moves with the arrows, opens a folder and ranks the recent tab first", () => {
    const stores = setup();
    fireEvent.click(screen.getByRole("button", { name: "Jump to request, folder or environment" }));
    fireEvent.change(input(), { target: { value: "user" } });
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual([
      "Users, Folder",
      "Get user, GET, in Users",
      "Delete user, DEL, in Users / Admin",
      "Admin, Folder, in Users",
    ]);
    key("ArrowUp");
    expect(options()[3].getAttribute("aria-selected")).toBe("true");
    key("ArrowDown");
    expect(options()[0].getAttribute("aria-selected")).toBe("true");
    key("Enter");
    expect(stores.tabs.getState().activeId).toBe("users");

    act(() => {
      stores.tabs.dispatch({ type: "tabs/open", id: "del" });
    });
    act(() => {
      pressOpen();
    });
    expect(options()[0].getAttribute("aria-label")).toBe("Delete user, DEL, in Users / Admin");
    // Users (opened before) and Delete user (opened last) now come before Get user.
    fireEvent.change(input(), { target: { value: "user" } });
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual([
      "Users, Folder",
      "Delete user, DEL, in Users / Admin",
      "Get user, GET, in Users",
      "Admin, Folder, in Users",
    ]);
  });

  it("makes an environment active", async () => {
    const stores = setup();
    act(() => {
      pressOpen();
    });
    fireEvent.change(input(), { target: { value: "stag" } });
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual(["Staging, Environment"]);
    await act(async () => {
      fireEvent.click(options()[0]);
    });
    expect(service.SetActive).toHaveBeenCalledWith("env_s");
    expect(stores.environments.getState().activeId).toBe("env_s");
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("closes with Escape and says when nothing matches", () => {
    setup();
    act(() => {
      pressOpen();
    });
    fireEvent.change(input(), { target: { value: "zzz" } });
    expect(within(screen.getByRole("listbox")).queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByRole("status").textContent).toBe("No matches");
    expect(input().hasAttribute("aria-activedescendant")).toBe(false);
    key("Enter");
    expect(screen.getByRole("combobox")).toBeTruthy();
    key("Escape");
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByRole("button", { name: "Jump to request, folder or environment" })).toBeTruthy();
  });

  it("hands over to the command palette when > is typed first (NAV-05)", () => {
    setup();
    const requests: string[] = [];
    const off = onCommandPaletteRequest((q) => requests.push(q));
    const elsewhere = screen.getByRole("button", { name: "elsewhere" });
    elsewhere.focus();
    act(() => {
      pressOpen();
    });
    fireEvent.change(input(), { target: { value: ">side" } });
    off();
    expect(requests).toEqual(["side"]);
    expect(screen.queryByRole("combobox")).toBeNull();
    // Focus went back where it was, so the palette's scoped actions apply there.
    expect(document.activeElement).toBe(elsewhere);
  });
});
