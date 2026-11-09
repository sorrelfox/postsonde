import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Current: vi.fn<() => Promise<unknown>>(),
  Open: vi.fn<(path: string) => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<() => Promise<unknown>>(),
  Recent: vi.fn<() => Promise<unknown>>(),
  RemoveRecent: vi.fn<(path: string) => Promise<unknown>>(),
  PickFolder: vi.fn<(purpose: string) => Promise<unknown>>(),
}));

const windowService = vi.hoisted(() => ({
  Current: vi.fn<() => Promise<unknown>>(),
  OpenInNewWindow: vi.fn<(path: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/window", () => ({
  WindowService: windowService,
}));

import { createStores, StoreProvider, type RecentWorkspace } from "../../stores";
import { FakeCancelError, resetFakeWails } from "../../test/fakeWails";
import { WorkspaceGate } from "./WorkspaceGate";

function dto(path: string, name: string) {
  return {
    id: "ws_1",
    name,
    path,
    localDir: "/data/workspaces/ws_1",
    localStatus: "new",
    previousPath: "",
    newerFormat: false,
    openedAt: "2026-09-16T09:00:00Z",
  };
}

function goError(kind: string, code: string, message: string, path: string): Error {
  const err = new Error("raw");
  err.name = "RuntimeError";
  (err as Error & { cause: unknown }).cause = { kind, code, message, hint: "A hint.", details: { path }, causes: [] };
  return err;
}

const recent: RecentWorkspace[] = [
  { id: "ws_1", name: "Shop API", path: "/p/shop", openedAt: "2026-09-16T09:00:00Z", available: true },
  { id: "ws_2", name: "Old", path: "/Volumes/usb/old", openedAt: "2026-09-15T09:00:00Z", available: false },
];

function renderGate() {
  const stores = createStores();
  render(
    <StoreProvider stores={stores}>
      <WorkspaceGate>
        <div>project shell</div>
      </WorkspaceGate>
    </StoreProvider>,
  );
  return stores;
}

beforeEach(() => {
  resetFakeWails();
  service.Current.mockReset().mockResolvedValue({ workspace: null });
  service.Open.mockReset().mockImplementation((path) => Promise.resolve(dto(path, "Opened")));
  service.Create.mockReset().mockImplementation((input) => {
    const { path, name } = input as { path: string; name: string };
    return Promise.resolve(dto(path, name));
  });
  service.Close.mockReset().mockResolvedValue(undefined);
  service.Recent.mockReset().mockResolvedValue(recent);
  service.RemoveRecent.mockReset().mockResolvedValue(undefined);
  service.PickFolder.mockReset().mockResolvedValue("");
  windowService.Current.mockReset().mockResolvedValue({ id: 1, openPath: "" });
  windowService.OpenInNewWindow.mockReset().mockResolvedValue(undefined);
});

describe("WorkspaceGate", () => {
  it("shows nothing while loading, then the shell when a project is open", async () => {
    service.Current.mockResolvedValue({ workspace: dto("/p/shop", "Shop API") });
    renderGate();
    expect(screen.queryByRole("button", { name: /Open folder/ })).toBeNull();
    expect(await screen.findByText("project shell")).toBeTruthy();
  });
});

describe("WelcomeScreen", () => {
  it("opens a folder chosen in the native dialog", async () => {
    service.PickFolder.mockResolvedValue("/p/shop");
    const stores = renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Open folder/ }));
    expect(await screen.findByText("project shell")).toBeTruthy();
    expect(service.PickFolder).toHaveBeenCalledWith("open");
    expect(service.Open).toHaveBeenCalledWith("/p/shop");
    expect(stores.workspace.getState().current?.path).toBe("/p/shop");
  });

  it("stays put when the dialog is cancelled", async () => {
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Open folder/ }));
    await waitFor(() => expect(service.PickFolder).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByRole("button", { name: /Open folder/ })).toHaveProperty("disabled", false));
    expect(service.Open).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("offers to create a project in a folder that is not one", async () => {
    service.PickFolder.mockResolvedValue("/p/plain-folder");
    service.Open.mockRejectedValue(
      goError("not_found", "workspace.not_a_project", "This folder is not a PostSonde project.", "/p/plain-folder"),
    );
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Open folder/ }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("This folder is not a PostSonde project.");
    expect(alert.textContent).toContain("A hint.");
    fireEvent.click(screen.getByRole("button", { name: "Create a project in this folder" }));

    const name = screen.getByLabelText("Project name") as HTMLInputElement;
    expect(name.value).toBe("plain-folder");
    expect(screen.getByText("/p/plain-folder")).toBeTruthy();
    fireEvent.change(name, { target: { value: "Plain API" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("project shell")).toBeTruthy();
    expect(service.Create).toHaveBeenCalledWith({ path: "/p/plain-folder", name: "Plain API" });
  });

  it("creates a project in a chosen folder, or opens it when it already is one", async () => {
    service.PickFolder.mockResolvedValue("/p/existing");
    service.Create.mockRejectedValue(
      goError("already_exists", "workspace.already_a_project", "This folder is already a PostSonde project.", "/p/existing"),
    );
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Create project/ }));
    expect(service.PickFolder).toHaveBeenCalledWith("create");

    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    expect((await screen.findByRole("alert")).textContent).toContain("already a PostSonde project");

    fireEvent.click(screen.getByRole("button", { name: "Open it instead" }));
    expect(await screen.findByText("project shell")).toBeTruthy();
    expect(service.Open).toHaveBeenCalledWith("/p/existing");
  });

  it("cancels the create form", async () => {
    service.PickFolder.mockResolvedValue("/p/new");
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Create project/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(screen.queryByLabelText("Project name")).toBeNull();
    expect(service.Create).not.toHaveBeenCalled();
  });

  it("lists recent projects, opens and removes them", async () => {
    renderGate();
    const list = await screen.findByRole("region", { name: "Recent projects" });
    expect(list.textContent).toContain("Shop API");
    expect(list.textContent).toContain("/Volumes/usb/old");
    expect(screen.getAllByText("Not found")).toHaveLength(1);

    service.Recent.mockResolvedValue([recent[0]]);
    fireEvent.click(screen.getByRole("button", { name: "Remove Old from recent projects" }));
    await waitFor(() => expect(screen.queryByText("/Volumes/usb/old")).toBeNull());
    expect(service.RemoveRecent).toHaveBeenCalledWith("/Volumes/usb/old");

    fireEvent.click(screen.getByRole("button", { name: /^Shop API/ }));
    expect(await screen.findByText("project shell")).toBeTruthy();
    expect(service.Open).toHaveBeenCalledWith("/p/shop");
  });

  it("opens a recent project in a new window (APP-06)", async () => {
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: "Open Shop API in a new window" }));
    await waitFor(() => expect(windowService.OpenInNewWindow).toHaveBeenCalledWith("/p/shop"));
    // A project that is not found has no such button.
    expect(screen.queryByRole("button", { name: "Open Old in a new window" })).toBeNull();
  });

  it("opens the project the window was opened for, and shows why when it does not open (APP-06)", async () => {
    windowService.Current.mockResolvedValue({ id: 2, openPath: "/p/plain" });
    service.Open.mockRejectedValue(goError("not_found", "workspace.not_a_project", "This folder is not a PostSonde project.", "/p/plain"));
    renderGate();
    expect(await screen.findByText("This folder is not a PostSonde project.")).toBeTruthy();
    expect(service.Open).toHaveBeenCalledWith("/p/plain");
    expect(screen.getByRole("button", { name: "Create a project in this folder" })).toBeTruthy();
  });

  it("ignores a cancelled call", async () => {
    service.PickFolder.mockRejectedValue(new FakeCancelError());
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: /Open folder/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: /Open folder/ })).toHaveProperty("disabled", false));
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
