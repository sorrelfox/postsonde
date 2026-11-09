import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { createStores, initialSettingsState, StoreProvider, type InitialStates, type TreeNode } from "../../stores";
import { TabBar } from "../tabs/TabBar";
import { AppShell, type AppShellProps } from "./AppShell";
import { IS_MAC } from "../../lib/shortcuts";
import { useSettingsPersistence } from "./useSettingsPersistence";
import { useThemeSync } from "./useThemeSync";

// The console button follows the app log (DBG-06); no Go backend under jsdom.
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: {
    List: () => Promise.resolve({ entries: [], capacity: 2000 }),
    Clear: () => Promise.resolve(),
  },
}));
// The network activity indicator follows the connection log (PRV-06).
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: {
    List: () => Promise.resolve({ conns: [], active: 0, capacity: 500 }),
    Clear: () => Promise.resolve(),
  },
}));

// App applies the theme and the settings file around the shell; do the same.
function WithAppEffects({ children }: { children: ReactNode }) {
  useThemeSync();
  useSettingsPersistence();
  return <>{children}</>;
}

function renderShell(props: AppShellProps = {}, initial: InitialStates = {}) {
  const stores = createStores(initial);
  render(
    <StoreProvider stores={stores}>
      <WithAppEffects>
        <AppShell {...props} />
      </WithAppEffects>
    </StoreProvider>,
  );
  return stores;
}

const primary = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const sidebar = () => screen.queryByRole("complementary");
const pressToggleShortcut = (extra: Partial<KeyboardEventInit> = {}) =>
  fireEvent.keyDown(window, { key: "b", code: "KeyB", metaKey: IS_MAC, ctrlKey: !IS_MAC, ...extra });

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

describe("AppShell", () => {
  it("renders the frame", () => {
    renderShell();
    expect(screen.getByRole("banner")).toBeTruthy();
    expect(screen.getByText("PostSonde")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Jump to request, folder or environment" })).toHaveProperty(
      "disabled",
      true,
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("navigation", { name: "Sidebar views" })).toBeTruthy();
    expect(screen.getByRole("complementary", { name: "Collections" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Request" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Response" })).toBeTruthy();
    expect(screen.getByText("Ready")).toBeTruthy();
  });

  it("is the window's file drop target and shows the drop overlay under a file drag (IMP-11)", async () => {
    renderShell();
    const shell = document.querySelector("[data-file-drop-target]");
    expect(shell?.getAttribute("data-file-drop-target")).toBe("import");
    // The modal hosts are outside it (portals), so a drop over an open dialog is ignored.
    expect(shell?.contains(screen.getByRole("banner"))).toBe(true);
    // The Collections view is not loaded here: the overlay says why a drop would not import.
    await act(async () => {
      shell?.classList.add("file-drop-target-active");
      await Promise.resolve();
    });
    expect(screen.getByRole("status").textContent).toBe("Open a project to import files into it.");
    await act(async () => {
      shell?.classList.remove("file-drop-target-active");
      await Promise.resolve();
    });
    expect(screen.queryByText("Open a project to import files into it.")).toBeNull();
  });

  it("mounts content into its slots", () => {
    renderShell({
      projectPath: "~/work/api",
      tabBar: <div>tabs here</div>,
      request: <div>request editor</div>,
      response: <div>response viewer</div>,
      statusStart: <span>busy</span>,
      statusEnd: <span>v1.2.3</span>,
    });
    for (const text of ["~/work/api", "tabs here", "request editor", "response viewer", "busy", "v1.2.3"]) {
      expect(screen.getByText(text)).toBeTruthy();
    }
    expect(screen.queryByText("Ready")).toBeNull();
    expect(screen.queryByRole("region", { name: "Design token preview" })).toBeNull();
  });

  it("shows the project name with its path as tooltip, and a close button", () => {
    let closed = 0;
    renderShell({ projectName: "Shop API", projectPath: "/Users/zoe/shop-api", onCloseProject: () => closed++ });
    expect(screen.getByText("Shop API").closest("[title]")?.getAttribute("title")).toBe("/Users/zoe/shop-api");
    fireEvent.click(screen.getByRole("button", { name: "Close project" }));
    expect(closed).toBe(1);
  });

  it("has no close button without a project", () => {
    renderShell();
    expect(screen.queryByRole("button", { name: "Close project" })).toBeNull();
  });

  it("collapses and expands the sidebar with the rail button", () => {
    const stores = renderShell();
    fireEvent.click(screen.getByRole("button", { name: "Hide sidebar" }));
    expect(sidebar()).toBeNull();
    expect(stores.settings.getState().sidebarCollapsed).toBe(true);

    const show = screen.getByRole("button", { name: "Show sidebar" });
    expect(show.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(show);
    expect(sidebar()).toBeTruthy();
    expect(stores.settings.getState().sidebarCollapsed).toBe(false);
  });

  it("collapses and expands the sidebar with the keyboard shortcut", () => {
    const stores = renderShell();
    pressToggleShortcut();
    expect(sidebar()).toBeNull();
    pressToggleShortcut();
    expect(sidebar()).toBeTruthy();

    pressToggleShortcut({ shiftKey: true });
    expect(stores.settings.getState().sidebarCollapsed).toBe(false);
  });

  it("starts collapsed when the settings say so", () => {
    renderShell({}, { settings: { ...initialSettingsState, sidebarCollapsed: true } });
    expect(sidebar()).toBeNull();
    expect(screen.getByRole("button", { name: "Show sidebar" })).toBeTruthy();
  });

  it("switches the sidebar view from the rail", () => {
    renderShell();
    const collections = screen.getByRole("button", { name: "Collections" });
    expect(collections.getAttribute("aria-pressed")).toBe("true");

    for (const [name, empty] of [
      ["History", "No history yet"],
      ["Environments", "No environments"],
      ["Collections", "No project open"],
    ]) {
      const button = screen.getByRole("button", { name });
      fireEvent.click(button);
      expect(button.getAttribute("aria-pressed")).toBe("true");
      expect(screen.getByRole("complementary", { name })).toBeTruthy();
      expect(screen.getByRole("heading", { name })).toBeTruthy();
      expect(screen.getByText(empty)).toBeTruthy();
    }
    expect(screen.getByRole("button", { name: "History" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("toggles the sidebar when the open view is clicked, and opens it for another view", () => {
    renderShell();
    fireEvent.click(screen.getByRole("button", { name: "Collections" }));
    expect(sidebar()).toBeNull();
    expect(screen.getByRole("button", { name: "Collections" }).getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(screen.getByRole("button", { name: "History" }));
    expect(screen.getByRole("complementary", { name: "History" })).toBeTruthy();
  });

  it("cycles the theme and updates data-theme on <html>", () => {
    const stores = renderShell({}, { settings: { ...initialSettingsState, theme: "light" } });
    expect(document.documentElement.dataset.theme).toBe("light");

    fireEvent.click(screen.getByRole("button", { name: /^Theme: Light/ }));
    expect(stores.settings.getState().theme).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("postsonde.theme")).toBe("dark");

    fireEvent.click(screen.getByRole("button", { name: /^Theme: Dark/ }));
    expect(stores.settings.getState().theme).toBe("system");
    // No prefers-color-scheme in jsdom: 'system' resolves to light.
    expect(screen.getByRole("button", { name: "Theme: System (Light). Switch to Light" })).toBeTruthy();
  });

  it("closes the active tab with ⌘W (Ctrl+W), also from a text field", () => {
    const stores = renderShell({ tabBar: <TabBar />, request: <input aria-label="URL" /> });
    const node = (id: string) =>
      ({ id, kind: "request", parentId: null, name: id, path: `${id}.json`, fileKind: "http", readOnly: "", method: "GET" }) as TreeNode;
    act(() => {
      stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws" });
      stores.collection.dispatch({ type: "collection/load", workspaceId: "ws", nodes: [node("a"), node("b")], rootIds: ["a", "b"], expandedIds: [] });
      stores.tabs.dispatch({ type: "tabs/restore", order: ["a", "b"], activeId: "a" });
    });
    expect(screen.getAllByRole("tab")).toHaveLength(2);

    fireEvent.keyDown(screen.getByRole("textbox", { name: "URL" }), { key: "w", code: "KeyW", ...primary });
    expect(screen.getAllByRole("tab")).toHaveLength(1);
    expect(stores.tabs.getState().activeId).toBe("b");
    fireEvent.keyDown(screen.getByRole("tab"), { key: "w", code: "KeyW", ...primary });
    expect(screen.queryAllByRole("tab")).toHaveLength(0);
  });

  it("opens the settings screen with ⌘, (Ctrl+,)", () => {
    const stores = renderShell({}, { settings: { ...initialSettingsState, sidebarCollapsed: true } });
    fireEvent.keyDown(window, { key: ",", code: "Comma", ...primary });
    expect(screen.getByRole("navigation", { name: "Settings" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "General" })).toBeTruthy();
    expect(stores.settings.getState().sidebarCollapsed).toBe(true); // untouched
    // Again: stays open (it is not a toggle).
    fireEvent.keyDown(window, { key: ",", code: "Comma", ...primary });
    expect(screen.getByRole("navigation", { name: "Settings" })).toBeTruthy();
  });

  it("shows and hides the console from the status bar and with ⇧⌘Y (Ctrl+Shift+Y)", () => {
    renderShell();
    const panel = () => screen.queryByRole("region", { name: "Console" });
    expect(panel()).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /^Console/ }));
    expect(panel()).toBeTruthy();
    fireEvent.keyDown(window, { key: "Y", code: "KeyY", shiftKey: true, ...primary });
    expect(panel()).toBeNull();

    // From the settings screen the shortcut goes back to the work area with the console shown.
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    fireEvent.keyDown(window, { key: "Y", code: "KeyY", shiftKey: true, ...primary });
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
    expect(panel()).toBeTruthy();
  });

  it("opens the connection log from the network activity indicator (PRV-06)", () => {
    renderShell();
    const indicator = screen.getByRole("button", { name: /^Network: no open connections/ });
    expect(indicator.textContent).toContain("Idle");
    const log = () => screen.queryByRole("region", { name: "Connections" });

    fireEvent.click(indicator);
    expect(log()).toBeTruthy();
    expect(screen.getByText(/^No connections\./)).toBeTruthy();
    expect(indicator.getAttribute("aria-pressed")).toBe("true");

    // The view switch goes to the app log and back.
    fireEvent.click(screen.getByRole("button", { name: "Log" }));
    expect(log()).toBeNull();
    expect(screen.getByRole("log", { name: "Console records" })).toBeTruthy();
    fireEvent.click(indicator);
    expect(log()).toBeTruthy();
    fireEvent.click(indicator);
    expect(screen.queryByRole("region", { name: "Console" })).toBeNull();
  });

  it("shows the settings screen in place of the sidebar and the work area while the gear is pressed", () => {
    renderShell();
    const gear = screen.getByRole("button", { name: "Settings" });
    expect(gear.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(gear);
    expect(gear.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("navigation", { name: "Settings" })).toBeTruthy();
    expect(sidebar()).toBeNull();
    expect(screen.queryByRole("region", { name: "Request" })).toBeNull();
    expect(screen.getByRole("button", { name: "Collections" }).getAttribute("aria-pressed")).toBe("false");

    // The gear again goes back to where you were.
    fireEvent.click(gear);
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
    expect(screen.getByRole("complementary", { name: "Collections" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Request" })).toBeTruthy();

    // A view button closes it and opens that view.
    fireEvent.click(gear);
    fireEvent.click(screen.getByRole("button", { name: "Collections" }));
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
    expect(screen.getByRole("complementary", { name: "Collections" })).toBeTruthy();

    // So does the sidebar toggle, showing the sidebar.
    fireEvent.click(gear);
    pressToggleShortcut();
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
    expect(sidebar()).toBeTruthy();
  });

  it("keeps the work area mounted behind the settings screen", () => {
    renderShell({ request: <input aria-label="URL" defaultValue="typed" /> });
    const input = screen.getByRole("textbox", { name: "URL" });
    fireEvent.change(input, { target: { value: "edited" } });
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    expect(screen.getByRole("textbox", { name: "URL" })).toBe(input);
    expect((input as HTMLInputElement).value).toBe("edited");
  });

  it("focuses the environment switcher with ⌘E (Ctrl+E) when there are environments", () => {
    const stores = renderShell();
    const select = screen.getByRole("combobox", { name: "Environment" });
    const noEnvs = fireEvent.keyDown(window, { key: "e", code: "KeyE", ...primary });
    expect(noEnvs).toBe(true); // not taken
    expect(document.activeElement).not.toBe(select);

    act(() => {
      stores.environments.dispatch({ type: "environments/loading", workspaceKey: "ws" });
      stores.environments.dispatch({
        type: "environments/loaded",
        workspaceKey: "ws",
        environments: [{ id: "dev", name: "Dev", color: "", fileName: "dev.json", readOnly: false, variables: [] }],
        activeId: null,
        problems: [],
        readOnly: false,
      });
    });
    fireEvent.keyDown(window, { key: "e", code: "KeyE", ...primary });
    expect(document.activeElement).toBe(select);
  });

  it("shows the shortcut help with ⌘/ (Ctrl+/)", () => {
    renderShell();
    fireEvent.keyDown(window, { key: "/", code: "Slash", ...primary });
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeTruthy();
  });

  it("runs an action from the command palette with ⇧⌘P (Ctrl+Shift+P) (NAV-05)", () => {
    const stores = renderShell();
    fireEvent.keyDown(window, { key: "P", code: "KeyP", shiftKey: true, ...primary });
    const dialog = screen.getByRole("dialog", { name: "Commands" });
    const field = screen.getByRole("combobox", { name: "Run a command" });
    expect(document.activeElement).toBe(field);
    fireEvent.change(field, { target: { value: "hide the sidebar" } });
    expect(dialog.querySelector('[role="option"][aria-selected="true"]')?.getAttribute("data-action-id")).toBe("sidebar.toggle");
    fireEvent.keyDown(field, { key: "Enter" });
    expect(screen.queryByRole("dialog", { name: "Commands" })).toBeNull();
    expect(stores.settings.getState().sidebarCollapsed).toBe(true);
    expect(sidebar()).toBeNull();
  });

  describe("focus mode (UI-09)", () => {
    const pressFocusMode = () => fireEvent.keyDown(window, { key: "Enter", code: "Enter", shiftKey: true, ...primary });
    const rail = () => screen.queryByRole("navigation", { name: "Sidebar views" });
    const consolePanel = () => screen.queryByRole("region", { name: "Console" });
    const exitButton = () => screen.queryByRole("button", { name: "Focus mode" });

    it("hides the rail, the sidebar and the console, and brings back exactly what was shown", () => {
      const stores = renderShell();
      fireEvent.click(screen.getByRole("button", { name: /^Console/ }));
      expect(consolePanel()).toBeTruthy();

      pressFocusMode();
      expect(rail()).toBeNull();
      expect(sidebar()).toBeNull();
      expect(consolePanel()).toBeNull();
      expect(screen.getByRole("region", { name: "Request" })).toBeTruthy();
      expect(screen.getByRole("region", { name: "Response" })).toBeTruthy();
      expect(exitButton()?.getAttribute("aria-pressed")).toBe("true");
      // A flag on top of the layout: the saved sidebar setting is untouched.
      expect(stores.settings.getState().sidebarCollapsed).toBe(false);

      pressFocusMode();
      expect(rail()).toBeTruthy();
      expect(sidebar()).toBeTruthy();
      expect(consolePanel()).toBeTruthy();
      expect(exitButton()).toBeNull();
    });

    it("keeps a collapsed sidebar collapsed when it ends, and ends from the status bar", () => {
      renderShell({}, { settings: { ...initialSettingsState, sidebarCollapsed: true } });
      pressFocusMode();
      expect(rail()).toBeNull();
      fireEvent.click(exitButton() as HTMLElement);
      expect(rail()).toBeTruthy();
      expect(sidebar()).toBeNull();
      expect(screen.getByRole("button", { name: "Show sidebar" })).toBeTruthy();
    });

    it("ends when the sidebar, the console or the settings are asked for", () => {
      const stores = renderShell({}, { settings: { ...initialSettingsState, sidebarCollapsed: true } });
      pressFocusMode();
      pressToggleShortcut();
      expect(rail()).toBeTruthy();
      expect(sidebar()).toBeTruthy();
      expect(stores.settings.getState().sidebarCollapsed).toBe(false);

      pressFocusMode();
      fireEvent.keyDown(window, { key: "Y", code: "KeyY", shiftKey: true, ...primary });
      expect(rail()).toBeTruthy();
      expect(consolePanel()).toBeTruthy();

      pressFocusMode();
      fireEvent.keyDown(window, { key: ",", code: "Comma", ...primary });
      expect(screen.getByRole("navigation", { name: "Settings" })).toBeTruthy();
      expect(rail()).toBeTruthy();
    });

    it("moves focus out of the parts it hides, and runs from the palette", () => {
      renderShell();
      const railButton = screen.getByRole("button", { name: "Hide sidebar" });
      railButton.focus();
      pressFocusMode();
      expect(document.activeElement?.tagName).toBe("MAIN");

      pressFocusMode();
      fireEvent.keyDown(window, { key: "P", code: "KeyP", shiftKey: true, ...primary });
      const field = screen.getByRole("combobox", { name: "Run a command" });
      fireEvent.change(field, { target: { value: "focus mode" } });
      fireEvent.keyDown(field, { key: "Enter" });
      expect(rail()).toBeNull();
      expect(exitButton()).toBeTruthy();
    });
  });

  it("follows theme changes made elsewhere", () => {
    const stores = renderShell({}, { settings: { ...initialSettingsState, theme: "dark" } });
    expect(document.documentElement.dataset.theme).toBe("dark");
    act(() => stores.settings.dispatch({ type: "settings/load", settings: { theme: "light" } }));
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});
