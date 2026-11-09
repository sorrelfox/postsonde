import { search, searchKeymap } from "@codemirror/search";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useEffect, useRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { openCommandPalette } from "../../commands/palette";
import { IS_MAC, matchesShortcut, setShortcutOverrides, SHORTCUTS } from "../../lib/shortcuts";
import { createStores, StoreProvider } from "../../stores";
import { CommandPalette } from "./CommandPalette";
import { useGlobalShortcuts, useShortcut } from "./useShortcut";

const primary = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const openPalette = () => fireEvent.keyDown(window, { key: "P", code: "KeyP", shiftKey: true, ...primary });
const field = () => screen.getByRole("combobox", { name: "Run a command" });
const option = (id: string) => document.querySelector<HTMLElement>(`[role="option"][data-action-id="${id}"]`);
const selected = () => document.querySelector('[role="option"][aria-selected="true"]')?.getAttribute("data-action-id");

/** A GraphQL query editor stand-in: its frame is the "graphql" scope and it records the fill-fields chord. */
function Harness({ onFill, onToggle }: { onFill: () => void; onToggle: () => void }) {
  useGlobalShortcuts();
  useShortcut("sidebar.toggle", onToggle);
  const cm = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (cm.current === null) return;
    // A CodeMirror editor with the app's search keymap: the "editor" scope.
    const v = new EditorView({
      state: EditorState.create({ doc: "hello", extensions: [search(), keymap.of(searchKeymap)] }),
      parent: cm.current,
    });
    return () => v.destroy();
  }, []);
  return (
    <>
      <button type="button">outside</button>
      <div
        data-shortcut-scope="graphql"
        onKeyDown={(e) => {
          if (matchesShortcut(e, "graphql.fillFields")) onFill();
        }}
      >
        <textarea aria-label="Query" />
      </div>
      <div data-shortcut-scope="editor" ref={cm} />
      <CommandPalette />
    </>
  );
}

function renderHarness() {
  const calls = { fill: 0, toggle: 0 };
  render(
    <StoreProvider stores={createStores()}>
      <Harness onFill={() => calls.fill++} onToggle={() => calls.toggle++} />
    </StoreProvider>,
  );
  return calls;
}

afterEach(() => {
  setShortcutOverrides({});
});

describe("CommandPalette (NAV-05)", () => {
  it("opens with ⇧⌘P (Ctrl+Shift+P) as an accessible combobox over a listbox, and closes with Escape", () => {
    renderHarness();
    const outside = screen.getByRole("button", { name: "outside" });
    outside.focus();
    openPalette();
    const dialog = screen.getByRole("dialog", { name: "Commands" });
    const input = field();
    expect(document.activeElement).toBe(input);
    const listbox = within(dialog).getByRole("listbox", { name: "Commands" });
    expect(input.getAttribute("aria-controls")).toBe(listbox.id);
    expect(input.getAttribute("aria-expanded")).toBe("true");
    const active = input.getAttribute("aria-activedescendant");
    expect(active && document.getElementById(active)?.getAttribute("aria-selected")).toBe("true");
    // Every action but the palette itself, with its keys and where it applies.
    expect(within(listbox).getAllByRole("option")).toHaveLength(SHORTCUTS.length - 1);
    expect(option("sidebar.toggle")?.textContent).toContain(IS_MAC ? "⌘B" : "Ctrl+B");
    expect(option("table.insertRow")?.textContent).toContain("Only in a key/value table");

    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(outside);
  });

  it("moves with the arrows and runs the highlighted action with Enter", () => {
    const calls = renderHarness();
    openPalette();
    const first = selected();
    fireEvent.keyDown(field(), { key: "ArrowDown" });
    expect(selected()).not.toBe(first);
    fireEvent.keyDown(field(), { key: "ArrowUp" });
    expect(selected()).toBe(first);
    // Wraps from the first to the last.
    fireEvent.keyDown(field(), { key: "ArrowUp" });
    const options = screen.getAllByRole("option");
    expect(selected()).toBe(options[options.length - 1]?.getAttribute("data-action-id"));

    fireEvent.change(field(), { target: { value: "hide the sidebar" } });
    expect(selected()).toBe("sidebar.toggle");
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls.toggle).toBe(1);
  });

  it("lists Fill fields disabled with the reason outside a GraphQL query editor", () => {
    const calls = renderHarness();
    screen.getByRole("button", { name: "outside" }).focus();
    openPalette();
    fireEvent.change(field(), { target: { value: "fill in the fields" } });
    const fill = option("graphql.fillFields") as HTMLElement;
    expect(fill.getAttribute("aria-disabled")).toBe("true");
    expect(fill.textContent).toContain("Only in a GraphQL query editor");
    expect(screen.getByRole("status").textContent).toContain("Only in a GraphQL query editor");
    fireEvent.keyDown(field(), { key: "Enter" });
    fireEvent.click(fill);
    expect(screen.getByRole("dialog", { name: "Commands" })).toBeTruthy();
    expect(calls.fill).toBe(0);
  });

  it("runs Fill fields in the GraphQL query editor that had focus, with its custom chord", () => {
    setShortcutOverrides({ "graphql.fillFields": ["Mod+Shift+L"] });
    const calls = renderHarness();
    const query = screen.getByRole("textbox", { name: "Query" });
    query.focus();
    openPalette();
    fireEvent.change(field(), { target: { value: "fill" } });
    const fill = option("graphql.fillFields") as HTMLElement;
    expect(fill.getAttribute("aria-disabled")).toBeNull();
    expect(fill.textContent).toContain(IS_MAC ? "⇧⌘L" : "Ctrl+Shift+L");
    fireEvent.click(fill);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(query);
    expect(calls.fill).toBe(1);
  });

  it("runs CodeMirror's own Find in the editor that had focus", () => {
    renderHarness();
    const content = document.querySelector<HTMLElement>(".cm-content");
    expect(content).not.toBeNull();
    content?.focus();
    openPalette();
    fireEvent.change(field(), { target: { value: "find and replace" } });
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(document.querySelector(".cm-search")).not.toBeNull();
  });

  it("opens with a query from the quick switcher's > prefix", () => {
    renderHarness();
    act(() => {
      openCommandPalette("sidebar");
    });
    expect((field() as HTMLInputElement).value).toBe("sidebar");
    expect(selected()).toBe("sidebar.toggle");
  });
});
