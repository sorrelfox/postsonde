import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatChord, IS_MAC, SHORTCUTS, shortcutChords, shortcutName } from "../../lib/shortcuts";
import { createStores, StoreProvider } from "../../stores";
import { ShortcutHelp, type ShortcutHelpProps } from "./ShortcutHelp";
import { useGlobalShortcuts } from "./useShortcut";

function Harness(props: ShortcutHelpProps) {
  useGlobalShortcuts();
  return (
    <>
      <button type="button">outside</button>
      <input aria-label="URL" />
      <ShortcutHelp {...props} />
    </>
  );
}

function setup(props: ShortcutHelpProps = {}) {
  render(
    <StoreProvider stores={createStores()}>
      <Harness {...props} />
    </StoreProvider>,
  );
}

const mod = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const dialog = () => screen.queryByRole("dialog", { name: "Keyboard shortcuts" });
const pressHelp = (target: Element | Window = document.activeElement ?? document.body) =>
  fireEvent.keyDown(target, { key: "/", code: "Slash", ...mod });

describe("ShortcutHelp", () => {
  it("opens and closes with ⌘/ (Ctrl+/) and with ?", () => {
    setup();
    expect(dialog()).toBeNull();
    pressHelp();
    expect(dialog()).toBeTruthy();
    expect(dialog()?.getAttribute("aria-modal")).toBe("true");
    pressHelp();
    expect(dialog()).toBeNull();

    fireEvent.keyDown(screen.getByRole("button", { name: "outside" }), { key: "?", code: "Slash", shiftKey: true });
    expect(dialog()).toBeTruthy();
  });

  it("does not open with ? while typing in a text field", () => {
    setup();
    fireEvent.keyDown(screen.getByRole("textbox", { name: "URL" }), { key: "?", code: "Slash", shiftKey: true });
    expect(dialog()).toBeNull();
  });

  it.each([
    ["macOS", true],
    ["Windows and Linux", false],
  ] as const)("lists every registered shortcut with its keys on %s", (_name, mac) => {
    setup({ mac });
    pressHelp();
    const d = dialog()!;
    const rows = d.querySelectorAll("[data-shortcut-id]");
    expect([...rows].map((r) => r.getAttribute("data-shortcut-id")).sort()).toEqual(SHORTCUTS.map((s) => s.id).sort());
    for (const def of SHORTCUTS) {
      const row = d.querySelector(`[data-shortcut-id="${def.id}"]`) as HTMLElement;
      expect(row.textContent).toContain(shortcutName(def));
      const keys = [...row.querySelectorAll("kbd")].map((k) => k.textContent);
      expect(keys).toEqual(shortcutChords(def, mac).map((c) => formatChord(c, mac)));
      expect(row.textContent?.includes("Coming soon")).toBe(def.placeholder !== undefined);
    }
    expect(within(d).getByRole("region", { name: "Tables" }).textContent).toContain("In a key/value table");
    expect(d.textContent).toContain(mac ? "⌘W" : "Ctrl+W");
  });

  it("filters the list", () => {
    setup({ mac: false });
    pressHelp();
    const filter = screen.getByRole("searchbox", { name: "Filter shortcuts" });
    expect(document.activeElement).toBe(filter);
    fireEvent.change(filter, { target: { value: "tab" } });
    const ids = [...dialog()!.querySelectorAll("[data-shortcut-id]")].map((r) => r.getAttribute("data-shortcut-id"));
    expect(ids).toContain("tab.close");
    expect(ids).not.toContain("environment.switch");
    fireEvent.change(filter, { target: { value: "ctrl+," } });
    expect([...dialog()!.querySelectorAll("[data-shortcut-id]")].map((r) => r.getAttribute("data-shortcut-id"))).toEqual(["settings.open"]);
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(dialog()!.textContent).toContain("No shortcut matches");
  });

  it("closes with Escape, the close button or the backdrop, and gives focus back", () => {
    setup();
    const outside = screen.getByRole("button", { name: "outside" });
    act(() => outside.focus());
    pressHelp();
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(outside);

    pressHelp();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(dialog()).toBeNull();

    pressHelp();
    fireEvent.pointerDown(dialog()!.parentElement!);
    expect(dialog()).toBeNull();
  });

  it("keeps Tab focus inside and blocks other shortcuts while open", () => {
    setup();
    pressHelp();
    const filter = screen.getByRole("searchbox", { name: "Filter shortcuts" });
    const close = screen.getByRole("button", { name: "Close" });
    act(() => close.focus());
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(filter);
    fireEvent.keyDown(filter, { key: "Tab" });
    expect(document.activeElement).toBe(close);

    // Focus moving outside is pulled back into the dialog.
    act(() => screen.getByRole("button", { name: "outside" }).focus());
    expect(dialog()!.contains(document.activeElement)).toBe(true);

    // ⌘B is not run inside the dialog.
    const e = fireEvent.keyDown(close, { key: "b", code: "KeyB", ...mod });
    expect(e).toBe(true); // not prevented
  });
});
