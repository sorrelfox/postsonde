import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Open: vi.fn<(path: string) => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<() => Promise<unknown>>(),
  Recent: vi.fn<() => Promise<unknown>>(),
  PickFolder: vi.fn<(purpose: string) => Promise<unknown>>(),
  Reveal: vi.fn<(path: string) => Promise<unknown>>(),
}));

const windowService = vi.hoisted(() => ({
  NewWindow: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/window", () => ({
  WindowService: windowService,
}));

import { toaster } from "../../commands/toasts";
import { WORKSPACE_CODES } from "../../lib/workspace";
import { createStores, StoreProvider, type RecentWorkspace, type Stores } from "../../stores";
import { resetFakeWails } from "../../test/fakeWails";
import { revealLabel, WorkspaceMenu } from "./WorkspaceMenu";

function dto(path: string, name: string) {
  return {
    id: `ws_${name.toLowerCase()}`,
    name,
    path,
    localDir: "/data/x",
    localStatus: "existing",
    previousPath: "",
    newerFormat: false,
    openedAt: "2026-09-18T09:00:00Z",
    uiState: {},
  };
}

const recent: RecentWorkspace[] = [
  { id: "ws_shop", name: "Shop", path: "/p/shop", openedAt: "2026-09-18T09:00:00Z", available: true },
  { id: "ws_billing", name: "Billing", path: "/p/billing", openedAt: "2026-09-17T09:00:00Z", available: true },
  { id: "ws_old", name: "Old", path: "/Volumes/usb/old", openedAt: "2026-09-16T09:00:00Z", available: false },
];

function renderMenu(onManageRecent?: () => void): Stores {
  const stores = createStores();
  stores.workspace.dispatch({
    type: "workspace/setCurrent",
    workspace: {
      id: "ws_shop",
      name: "Shop",
      path: "/p/shop",
      localDir: "/data/x",
      localStatus: "existing",
      previousPath: "",
      newerFormat: false,
    },
  });
  stores.workspace.dispatch({ type: "workspace/setRecent", recent });
  render(
    <StoreProvider stores={stores}>
      <WorkspaceMenu onManageRecent={onManageRecent} />
    </StoreProvider>,
  );
  return stores;
}

const items = () => screen.getAllByRole("menuitem").map((el) => el.textContent);

beforeEach(() => {
  resetFakeWails();
  service.Open.mockReset().mockImplementation((path) => Promise.resolve(dto(path, "Billing")));
  service.Create.mockReset().mockImplementation((input) => {
    const { path, name } = input as { path: string; name: string };
    return Promise.resolve(dto(path, name));
  });
  service.Close.mockReset().mockResolvedValue(undefined);
  service.Recent.mockReset().mockResolvedValue(recent);
  service.PickFolder.mockReset().mockResolvedValue("");
  service.Reveal.mockReset().mockResolvedValue(undefined);
  windowService.NewWindow.mockReset().mockResolvedValue(undefined);
  toaster.clear();
});

describe("WorkspaceMenu", () => {
  it("shows the open project and lists the other recent projects", () => {
    const manage = vi.fn();
    renderMenu(manage);
    const button = screen.getByRole("button", { name: "Project: Shop. Switch project" });
    expect(button.getAttribute("title")).toBe("/p/shop");
    fireEvent.click(button);
    expect(items()).toEqual([
      "Billing",
      "Old (not found)",
      "Open folder…",
      "New project…",
      "Open in new window",
      revealLabel(),
      "Manage recent projects…",
      "Close project",
    ]);
    expect(screen.getByRole("menuitem", { name: "Old (not found)" }).getAttribute("aria-disabled")).toBe("true");

    fireEvent.click(screen.getByRole("menuitem", { name: "Manage recent projects…" }));
    expect(manage).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("switches to a recent project", async () => {
    const stores = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Billing" }));
    await waitFor(() => expect(stores.workspace.getState().current?.path).toBe("/p/billing"));
    expect(service.Open).toHaveBeenCalledWith("/p/billing");
  });

  it("reveals the project folder and closes the project", async () => {
    const stores = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: revealLabel() }));
    await waitFor(() => expect(service.Reveal).toHaveBeenCalledWith("/p/shop"));

    await waitFor(() => expect((screen.getByRole("button", { name: /Switch project/ }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Close project" }));
    await waitFor(() => expect(stores.workspace.getState().current).toBeNull());
  });

  it("opens another window on the project (APP-06)", async () => {
    renderMenu();
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Open in new window" }));
    await waitFor(() => expect(windowService.NewWindow).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("says why the project cannot switch while other windows show it (APP-06)", async () => {
    const message = "Every PostSonde window shows the same project, so it cannot change while other windows are open.";
    const err = new Error("go error text");
    err.name = "RuntimeError";
    (err as Error & { cause: unknown }).cause = {
      kind: "invalid_argument",
      code: WORKSPACE_CODES.sharedByWindows,
      message,
      hint: "Close the other PostSonde windows first, then switch or close the project in this one.",
      details: { path: "/p/billing", windows: "1" },
      causes: [],
    };
    service.Open.mockRejectedValue(err);
    const stores = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Billing" }));
    await waitFor(() => expect(toaster.toasts()).toHaveLength(1));
    expect(String(toaster.toasts()[0]?.message)).toContain(message);
    expect(stores.workspace.getState().current?.path).toBe("/p/shop");
  });

  it("creates a new project in a chosen folder with the typed name", async () => {
    service.PickFolder.mockResolvedValue("/p/new-api");
    const stores = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: /Switch project/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "New project…" }));
    const input = await screen.findByRole("textbox", { name: "Project name" });
    expect((input as HTMLInputElement).value).toBe("new-api");
    fireEvent.change(input, { target: { value: "New API" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(stores.workspace.getState().current?.name).toBe("New API"));
    expect(service.PickFolder).toHaveBeenCalledWith("create");
    expect(service.Create).toHaveBeenCalledWith({ path: "/p/new-api", name: "New API" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
