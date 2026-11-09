import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { flushSettings, setSettingsPersistence } from "../../commands/settings";
import { localStorageSettings, UI_SETTINGS_STORAGE_KEY } from "../../lib/persist";
import { createStores, StoreProvider } from "../../stores";
import { AppShell } from "./AppShell";
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

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
});

afterEach(async () => {
  await flushSettings();
});

function renderShell() {
  const stores = createStores();
  const view = render(
    <StoreProvider stores={stores}>
      <WithAppEffects>
        <AppShell request={<div>request editor</div>} response={<div>response viewer</div>} />
      </WithAppEffects>
    </StoreProvider>,
  );
  return { stores, view };
}

const separator = () => screen.getByRole("separator", { name: "Resize request and response panes" });

describe("WorkArea split", () => {
  it("puts the request and response panes on either side of the splitter", () => {
    renderShell();
    const handle = separator();
    const controlled = document.getElementById(handle.getAttribute("aria-controls") ?? "");
    expect(controlled?.textContent).toBe("request editor");
    expect(handle.getAttribute("aria-valuemin")).toBe("20");
    expect(handle.getAttribute("aria-valuemax")).toBe("80");
  });

  it("resizes with the keyboard and saves the ratio", async () => {
    const { stores } = renderShell();
    fireEvent.keyDown(separator(), { key: "ArrowLeft", shiftKey: true });
    expect(stores.settings.getState().splitRatio).toBe(0.4);
    expect(separator().getAttribute("aria-valuenow")).toBe("40");
    await flushSettings();
    expect(JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "")).toEqual({ splitRatio: 0.4 });
  });

  it("restores the saved ratio at startup", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ splitRatio: 0.3 }));
    const { stores } = renderShell();
    await act(async () => {});
    expect(stores.settings.getState().splitRatio).toBe(0.3);
    expect(separator().getAttribute("aria-valuenow")).toBe("30");
  });

  it("writes pending changes when the shell unmounts", () => {
    const { view } = renderShell();
    fireEvent.keyDown(separator(), { key: "End" });
    expect(localStorage.getItem(UI_SETTINGS_STORAGE_KEY)).toBeNull();
    view.unmount();
    expect(JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "")).toEqual({ splitRatio: 0.8 });
  });
});

describe("WorkArea orientation (UI-02)", () => {
  it("stacks the response below the request, with its own split", async () => {
    localStorage.setItem(UI_SETTINGS_STORAGE_KEY, JSON.stringify({ responsePosition: "below", splitRatioBelow: 0.6 }));
    const { stores } = renderShell();
    await act(async () => {});
    const handle = separator();
    expect(handle.getAttribute("aria-orientation")).toBe("horizontal");
    expect(handle.getAttribute("aria-valuenow")).toBe("60");
    const controlled = document.getElementById(handle.getAttribute("aria-controls") ?? "");
    expect(controlled?.textContent).toBe("request editor");

    fireEvent.keyDown(handle, { key: "ArrowUp", shiftKey: true });
    expect(stores.settings.getState()).toMatchObject({ splitRatio: 0.5, splitRatioBelow: 0.5 });
    await flushSettings();
    expect(JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "")).toMatchObject({ splitRatioBelow: 0.5 });
  });

  it("switches layout live and keeps the panes mounted", () => {
    const { stores } = renderShell();
    const request = screen.getByText("request editor");
    expect(separator().getAttribute("aria-orientation")).toBe("vertical");

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { responsePosition: "below" } }));
    expect(separator().getAttribute("aria-orientation")).toBe("horizontal");
    expect(screen.getByText("request editor")).toBe(request);

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { responsePosition: "beside" } }));
    expect(separator().getAttribute("aria-orientation")).toBe("vertical");
    expect(screen.getByText("request editor")).toBe(request);
  });
});
