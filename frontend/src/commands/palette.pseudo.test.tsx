// The shortcut registry's text in the pseudo-locale (UI-10): the
// command palette and Settings → Shortcuts show action labels, groups,
// scopes, key names, the palette's reasons and the chord checks from the
// catalogue (namespaces lib and commands), so no English action label may
// be left. See docs/frontend.md#localization.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t, type Namespace } from "../i18n";
import { en } from "../i18n/catalog";
import { pseudoString } from "../i18n/pseudo";
import { localStorageSettings } from "../lib/persist";
import { IS_MAC, setShortcutOverrides, SHORTCUTS } from "../lib/shortcuts";
import { createStores, StoreProvider } from "../stores";
import { englishLeftovers, isPseudo, visibleTexts } from "../test/pseudoLocale";
import { ShortcutSettings } from "../components/settings/ShortcutSettings";
import { CommandPalette } from "../components/shell/CommandPalette";
import { useGlobalShortcuts } from "../components/shell/useShortcut";
import { useShortcutOverridesSync } from "../components/shell/useShortcutOverridesSync";
import { setSettingsPersistence } from "./settings";

/** The namespaces these screens show: the registry's (lib), the palette's reasons (commands) and the screens' own. */
const CONVERTED: readonly Namespace[] = ["lib", "commands", "shell", "settings", "common"];

const mod = IS_MAC ? { metaKey: true } : { ctrlKey: true };

/** Every action label of the registry, in English. */
const ENGLISH_LABELS = SHORTCUTS.map((def) => {
  const id = def.labelKey.slice("lib.".length) as keyof typeof en.lib;
  return en.lib[id] as string;
});

function Harness({ children }: { children: ReactNode }) {
  useGlobalShortcuts();
  useShortcutOverridesSync();
  return (
    <>
      <button type="button">outside</button>
      {children}
    </>
  );
}

function renderWith(children: ReactNode) {
  render(
    <StoreProvider stores={createStores()}>
      <Harness>{children}</Harness>
    </StoreProvider>,
  );
}

function expectNoEnglish(root: Element) {
  expect(englishLeftovers([root], CONVERTED)).toEqual([]);
  const texts = visibleTexts([root]);
  expect(texts.filter((text) => ENGLISH_LABELS.some((label) => text.includes(label)))).toEqual([]);
}

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
  setShortcutOverrides({});
});

describe("the shortcut registry in the pseudo-locale", () => {
  it("shows the command palette's actions, groups, scopes, keys and reasons from the catalogue", () => {
    renderWith(<CommandPalette />);
    screen.getByRole("button", { name: "outside" }).focus();
    fireEvent.keyDown(window, { key: "P", code: "KeyP", shiftKey: true, ...mod });
    const dialog = screen.getByRole("dialog", { name: pseudoString("Commands") });
    const options = within(dialog).getAllByRole("option");
    expect(options).toHaveLength(SHORTCUTS.length - 1);
    for (const option of options) expect(isPseudo(option.getAttribute("aria-label") ?? "")).toBe(true);
    // Scoped actions are unavailable here and say why, from the commands namespace.
    expect(dialog.textContent).toContain(t("commands.palette.onlyIn", { place: t("lib.shortcuts.place.tree") }));
    expectNoEnglish(dialog);

    // Searching by a translated label finds it.
    const input = within(dialog).getByRole("combobox");
    fireEvent.change(input, { target: { value: t("lib.shortcuts.action.sidebar.toggle") } });
    expect(within(dialog).getAllByRole("option")[0]?.getAttribute("data-action-id")).toBe("sidebar.toggle");
    expectNoEnglish(dialog);
  });

  it("shows Settings → Shortcuts and its chord checks from the catalogue", () => {
    renderWith(<ShortcutSettings />);
    const page = document.querySelector<HTMLElement>('[data-shortcut-id="sidebar.toggle"]')!.closest("section")!.parentElement!;
    expect(screen.getByRole("heading", { level: 3, name: pseudoString("General") })).toBeTruthy();
    const row = page.querySelector<HTMLElement>('[data-shortcut-id="sidebar.toggle"]')!;
    expect(row.textContent).toContain(pseudoString("Show or hide the sidebar"));
    expectNoEnglish(page);

    // A chord another shortcut has: the error names it in the language in use.
    fireEvent.click(within(row).getByRole("button", { name: t("settings.shortcuts.changeLabel", { shortcut: t("lib.shortcuts.action.sidebar.toggle") }) }));
    act(() => {
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: "w", code: "KeyW", ...mod });
    });
    const alert = within(row).getByRole("alert");
    expect(alert.textContent).toContain(t("lib.shortcuts.action.tab.close"));
    expect(isPseudo(alert.textContent ?? "")).toBe(true);
    expectNoEnglish(page);
  });
});
