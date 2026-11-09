import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadSettings, setSettingsPersistence } from "../../commands/settings";
import { installShortcuts, shortcutBindings } from "../../commands/shortcuts";
import { localStorageSettings, UI_SETTINGS_STORAGE_KEY } from "../../lib/persist";
import { getShortcutOverrides, IS_MAC, matchesShortcut, setShortcutOverrides, shortcutLabel } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { useShortcutOverridesSync } from "../shell/useShortcutOverridesSync";
import { ShortcutSettings } from "./ShortcutSettings";

const mod = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const saved = () => JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "{}") as Record<string, unknown>;

// App hands the settings store's shortcuts to the registry; do the same.
function WithSync({ children }: { children: ReactNode }) {
  useShortcutOverridesSync();
  return <>{children}</>;
}

function renderPage(stores: Stores = createStores()) {
  render(
    <StoreProvider stores={stores}>
      <WithSync>
        <ShortcutSettings />
      </WithSync>
    </StoreProvider>,
  );
  return stores;
}

const row = (id: string) => document.querySelector<HTMLElement>(`[data-shortcut-id="${id}"]`) as HTMLElement;
const change = (id: string) => fireEvent.click(within(row(id)).getByRole("button", { name: /^Change the shortcut of/ }));

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
});

afterEach(() => {
  setShortcutOverrides({});
});

describe("ShortcutSettings (UI-07)", () => {
  it("lists every shortcut with its chords; the code editor's own cannot be changed", () => {
    renderPage();
    expect(within(row("sidebar.toggle")).getByText(IS_MAC ? "⌘B" : "Ctrl+B")).toBeTruthy();
    expect(within(row("sidebar.toggle")).getByRole("button", { name: "Change the shortcut of Show or hide the sidebar" })).toBeTruthy();
    expect(within(row("editor.find")).queryByRole("button", { name: /^Change/ })).toBeNull();
    expect(row("editor.find").textContent).toContain("The code editor's own");
    expect(screen.getByRole("button", { name: "Reset all" })).toHaveProperty("disabled", true);
  });

  it("records a new chord, saves it in the settings and applies it at once", async () => {
    const stores = renderPage();
    const toggled = vi.fn();
    const stop = installShortcuts(stores);
    const unbind = shortcutBindings.bind("sidebar.toggle", toggled);
    try {
      change("sidebar.toggle");
      const rec = document.activeElement as HTMLElement;
      expect(rec.getAttribute("data-shortcut-scope")).toBe("record");
      expect(rec.textContent).toContain("Press the new keys");
      // While recording, the current chord is recorded, not run.
      fireEvent.keyDown(rec, { key: "b", code: "KeyB", ...mod });
      expect(toggled).not.toHaveBeenCalled();
      // ⌘B is its own default: nothing to override.
      await waitFor(() => expect(within(row("sidebar.toggle")).queryByText("Press the new keys… Esc cancels")).toBeNull());
      expect(stores.settings.getState().shortcuts).toEqual({});

      change("sidebar.toggle");
      // Only a modifier: keeps waiting.
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: IS_MAC ? "Meta" : "Control", ...mod });
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: "K", code: "KeyK", shiftKey: true, ...mod });
      await waitFor(() => expect(stores.settings.getState().shortcuts).toEqual({ "sidebar.toggle": ["Mod+Shift+K"] }));
      expect(saved().shortcuts).toEqual({ "sidebar.toggle": ["Mod+Shift+K"] });
      expect(getShortcutOverrides()).toEqual({ "sidebar.toggle": ["Mod+Shift+K"] });
      expect(shortcutLabel("sidebar.toggle")).toBe(IS_MAC ? "⇧⌘K" : "Ctrl+Shift+K");
      expect(within(row("sidebar.toggle")).getByText(IS_MAC ? "⇧⌘K" : "Ctrl+Shift+K")).toBeTruthy();
      // The default is still shown next to it.
      expect(row("sidebar.toggle").textContent).toContain(`Default: ${IS_MAC ? "⌘B" : "Ctrl+B"}`);

      // The new chord runs it; the old one no longer does.
      fireEvent.keyDown(window, { key: "K", code: "KeyK", shiftKey: true, ...mod });
      expect(toggled).toHaveBeenCalledTimes(1);
      fireEvent.keyDown(window, { key: "b", code: "KeyB", ...mod });
      expect(toggled).toHaveBeenCalledTimes(1);
      expect(screen.getByText("1 changed")).toBeTruthy();
    } finally {
      unbind();
      stop();
    }
  });

  it("refuses a chord another shortcut of the same scope has, or one the OS keeps, and cancels with Escape", async () => {
    const stores = renderPage();
    change("sidebar.toggle");
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "w", code: "KeyW", ...mod });
    expect(within(row("sidebar.toggle")).getByRole("alert").textContent).toContain("is already “Close tab”");
    // Still recording: try again.
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "c", code: "KeyC", ...mod });
    expect(within(row("sidebar.toggle")).getByRole("alert").textContent).toContain("copies in text fields");
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Escape" });
    expect(within(row("sidebar.toggle")).queryByRole("alert")).toBeNull();
    expect(within(row("sidebar.toggle")).getByRole("button", { name: /^Change/ })).toBeTruthy();
    await act(async () => {
      await Promise.resolve();
    });
    expect(stores.settings.getState().shortcuts).toEqual({});
    expect(saved().shortcuts).toBeUndefined();
  });

  it("saves a chord another scope takes, with a warning", async () => {
    const stores = renderPage();
    change("sidebar.toggle");
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "f", code: "KeyF", ...mod });
    await waitFor(() => expect(stores.settings.getState().shortcuts).toEqual({ "sidebar.toggle": ["Mod+F"] }));
    expect(within(row("sidebar.toggle")).getByRole("note").textContent).toContain("“Find and replace” in a code editor");
  });

  it("resets one shortcut and all of them", async () => {
    const stores = createStores();
    stores.settings.dispatch({ type: "settings/load", settings: { shortcuts: { "sidebar.toggle": ["Mod+Shift+K"], "tab.close": ["Mod+Alt+W"] } } });
    renderPage(stores);
    fireEvent.click(within(row("sidebar.toggle")).getByRole("button", { name: "Reset Show or hide the sidebar to its default" }));
    await waitFor(() => expect(stores.settings.getState().shortcuts).toEqual({ "tab.close": ["Mod+Alt+W"] }));
    expect(getShortcutOverrides()).toEqual({ "tab.close": ["Mod+Alt+W"] });

    fireEvent.click(screen.getByRole("button", { name: "Reset all" }));
    await waitFor(() => expect(stores.settings.getState().shortcuts).toEqual({}));
    expect(saved().shortcuts).toEqual({});
    expect(getShortcutOverrides()).toEqual({});
    expect(screen.getByText("All defaults")).toBeTruthy();
  });

  it("filters by label, group or keys", () => {
    renderPage();
    fireEvent.change(screen.getByRole("searchbox", { name: "Filter shortcuts" }), { target: { value: "focus mode" } });
    expect(row("view.focusMode")).toBeTruthy();
    expect(row("sidebar.toggle")).toBeNull();
  });
});

describe("applying custom shortcuts at startup and live (UI-07)", () => {
  it("applies the saved overrides once the settings load, and hand edits as they arrive", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ shortcuts: { "request.send": ["Mod+Alt+Enter"], "no.such": ["Mod+K"] } }));
    const stores = createStores();
    render(
      <StoreProvider stores={stores}>
        <WithSync>
          <span />
        </WithSync>
      </StoreProvider>,
    );
    const enter = { key: "Enter", code: "Enter", altKey: false, shiftKey: false, metaKey: IS_MAC, ctrlKey: !IS_MAC };
    expect(matchesShortcut(enter, "request.send")).toBe(true);

    await act(async () => {
      await loadSettings(stores);
    });
    expect(matchesShortcut(enter, "request.send")).toBe(false);
    expect(matchesShortcut({ ...enter, altKey: true }, "request.send")).toBe(true);
    // Unknown ids are kept in the file but not applied.
    expect(getShortcutOverrides()).toEqual({ "request.send": ["Mod+Alt+Enter"] });

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { shortcuts: {} } }));
    expect(matchesShortcut(enter, "request.send")).toBe(true);
  });
});
