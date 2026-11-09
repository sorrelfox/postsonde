import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const themesService = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  OpenFolder: vi.fn<() => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/themes", () => ({
  ThemesService: themesService,
}));

import { setSettingsPersistence } from "../../commands/settings";
import { resetThemesForTests } from "../../commands/themes";
import { localStorageSettings, UI_SETTINGS_STORAGE_KEY } from "../../lib/persist";
import { createStores, initialSettingsState, StoreProvider, type Stores } from "../../stores";
import { applyCustomTheme, resetCustomThemeForTests } from "../../styles/customTheme";
import { applyTheme, currentTheme, resetThemeForTests } from "../../styles/theme";
import { useCustomThemeSync } from "../shell/useCustomThemeSync";
import { CustomThemeRows } from "./CustomThemes";

// Built at run time: colour literals belong in tokens.css only.
const hex = (digits: string) => `#${digits}`;
const root = () => document.documentElement;

const ocean = {
  id: "ocean.json",
  name: "Ocean",
  base: "light",
  vars: [
    { name: "--bg0", value: hex("001122") },
    { name: "--tk-kw", value: hex("c792ea") },
  ],
  usable: true,
  problems: [{ severity: "warning", code: "themes.unknown_key", key: "author", message: 'Unknown member "author" is ignored.' }],
};
const broken = {
  id: "broken.json",
  name: "Broken",
  base: "dark",
  vars: [],
  usable: false,
  problems: [
    { severity: "error", code: "themes.invalid_color", key: "colors.bg0", message: '"colors.bg0" is not a colour: not a hex colour.' },
  ],
};

function Sync() {
  useCustomThemeSync();
  return null;
}

function renderRows(customTheme = ""): Stores {
  const stores = createStores({ settings: { ...initialSettingsState, customTheme } });
  render(
    <StoreProvider stores={stores}>
      <Sync />
      <CustomThemeRows />
    </StoreProvider>,
  );
  return stores;
}

const saved = () => JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "{}") as Record<string, unknown>;

beforeEach(() => {
  localStorage.clear();
  root().removeAttribute("style");
  resetThemeForTests();
  resetCustomThemeForTests();
  resetThemesForTests();
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  applyTheme("dark");
  setSettingsPersistence(localStorageSettings());
  themesService.List.mockReset().mockResolvedValue({ dir: "/data/themes", themes: [broken, ocean] });
  themesService.OpenFolder.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
  applyCustomTheme(null);
});

describe("custom themes (UI-05)", () => {
  it("loads the theme files and sets the chosen theme's CSS variables on :root", async () => {
    renderRows("ocean.json");
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(hex("001122")));
    expect(root().style.getPropertyValue("--tk-kw")).toBe(hex("c792ea"));
    expect(currentTheme()).toBe("light");
    expect(themesService.List).toHaveBeenCalledTimes(1);
  });

  it("chooses a theme on the settings screen and saves ui.customTheme", async () => {
    const stores = renderRows();
    const select = screen.getByRole("combobox", { name: "Custom theme" }) as HTMLSelectElement;
    await waitFor(() => expect(within(select).getByRole("option", { name: "Ocean" })).toBeTruthy());
    // A file with errors is listed but cannot be chosen.
    expect((within(select).getByRole("option", { name: "Broken (has errors)" }) as HTMLOptionElement).disabled).toBe(true);

    fireEvent.change(select, { target: { value: "ocean.json" } });
    await waitFor(() => expect(stores.settings.getState().customTheme).toBe("ocean.json"));
    expect(saved().customTheme).toBe("ocean.json");
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(hex("001122")));

    fireEvent.change(select, { target: { value: "" } });
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(""));
    expect(currentTheme()).toBe("dark");
    expect(saved().customTheme).toBe("");
  });

  it("shows the problems of the theme files", async () => {
    renderRows();
    const list = await screen.findByRole("list", { name: "Problems in theme files" });
    expect(list.textContent).toContain("broken.json");
    expect(list.textContent).toContain('Error: "colors.bg0" is not a colour');
    expect(list.textContent).toContain('Warning: Unknown member "author" is ignored.');
  });

  it("says why a chosen theme with errors or a missing file is not applied", async () => {
    renderRows("broken.json");
    expect(await screen.findByText("broken.json has errors, so the built-in theme is shown.")).toBeTruthy();
    expect(root().style.getPropertyValue("--bg0")).toBe("");
    expect(currentTheme()).toBe("dark");
  });

  it("reloads the folder and applies the edited file", async () => {
    renderRows("ocean.json");
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(hex("001122")));
    themesService.List.mockResolvedValue({
      dir: "/data/themes",
      themes: [{ ...ocean, vars: [{ name: "--bg0", value: hex("334455") }] }],
    });
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(hex("334455")));
    expect(root().style.getPropertyValue("--tk-kw")).toBe("");
    expect(themesService.List).toHaveBeenCalledTimes(2);
  });

  it("opens the themes folder, and shows why it could not", async () => {
    renderRows();
    fireEvent.click(screen.getByRole("button", { name: "Open themes folder" }));
    await waitFor(() => expect(themesService.OpenFolder).toHaveBeenCalledTimes(1));

    themesService.OpenFolder.mockRejectedValueOnce({
      kind: "io",
      code: "themes.open_failed",
      message: "The themes folder could not be opened.",
      hint: "Open it yourself.",
      details: {},
      causes: [],
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Open themes folder" }));
    });
    expect((await screen.findByRole("alert")).textContent).toContain("The themes folder could not be opened.");
  });

  it("warns, without refusing it, when the chosen theme's colours fall below WCAG AA (UI-11)", async () => {
    // Ocean is a light theme with a near-black window background: the light theme's dark text on it.
    renderRows("ocean.json");
    await waitFor(() => expect(root().style.getPropertyValue("--bg0")).toBe(hex("001122")));
    const warning = await screen.findByText(/^Low contrast in this theme:/);
    expect(warning.textContent).toContain("text on bg0");
    expect(warning.textContent).toContain("needs 4.5:1");
    expect(warning.closest("[role='status']")).not.toBeNull();
  });

  it("has no contrast warning without a custom theme", async () => {
    renderRows();
    await screen.findByRole("list", { name: "Problems in theme files" });
    expect(screen.queryByText(/^Low contrast in this theme:/)).toBeNull();
  });
});
