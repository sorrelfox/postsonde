import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IS_MAC } from "../../lib/shortcuts";
import { filterByMatch, type Suggestion } from "../../lib/suggest";
import { KeyValueTable, type KeyValueRow, type KeyValueTableProps } from "./KeyValueTable";

let seq = 0;
const nextId = () => `id${++seq}`;

const NAMES = ["Accept", "Accept-Encoding", "Authorization", "Content-Type", "X-Request-ID"];
const VALUES: Record<string, string[]> = {
  "content-type": ["application/json", "application/xml", "text/plain"],
  accept: ["*/*", "application/json"],
};

const suggestName = vi.fn((text: string): Suggestion[] =>
  filterByMatch(NAMES, text, (n) => n).map((n) => ({ value: n, detail: `About ${n}`, ...(n === "Authorization" && { note: "Set from the URL" }) })),
);
const suggestValue = vi.fn((text: string, row: KeyValueRow): Suggestion[] =>
  filterByMatch(VALUES[row.key.toLowerCase()] ?? [], text, (v) => v).map((v) => ({ value: v })),
);

type Props = Partial<Omit<KeyValueTableProps, "rows" | "onChange">>;

function renderTable(initial: KeyValueRow[], props: Props = {}) {
  const onChange = vi.fn();
  let current = initial;
  function Parent() {
    const [rows, setRows] = useState(initial);
    current = rows;
    return (
      <KeyValueTable
        label="Headers"
        createId={nextId}
        suggest={{ key: suggestName, value: suggestValue }}
        suggestLabels={{ key: "Header names", value: "Header values" }}
        {...props}
        rows={rows}
        onChange={(next) => {
          onChange(next);
          setRows(next);
        }}
      />
    );
  }
  render(<Parent />);
  return { onChange, rows: () => current };
}

const row = (id: string, key: string, value = "", enabled = true): KeyValueRow => ({ id, enabled, key, value, description: "" });
const focused = () => document.activeElement as HTMLElement;
const combo = (name: string) => screen.getByRole("combobox", { name }) as HTMLInputElement;
const type = (el: HTMLInputElement, text: string) => {
  el.focus();
  fireEvent.change(el, { target: { value: text } });
};
const key = (name: string, opts: object = {}) => fireEvent.keyDown(focused(), { key: name, ...opts });
const listbox = () => screen.queryByRole("listbox");
const options = () => within(screen.getByRole("listbox")).getAllByRole("option");
const labels = () => options().map((o) => o.firstElementChild?.textContent);

afterEach(() => {
  suggestName.mockClear();
  suggestValue.mockClear();
});

describe("KeyValueTable suggestions", () => {
  it("marks cells with suggestions as ARIA comboboxes and shows a listbox while typing", () => {
    renderTable([row("a", "", "")]);
    const name = combo("Key, row 1");
    expect(name.getAttribute("aria-autocomplete")).toBe("list");
    expect(name.getAttribute("aria-expanded")).toBe("false");
    expect(name.hasAttribute("aria-controls")).toBe(false);
    // The description column has no suggest function: a plain text box.
    expect(screen.getByRole("textbox", { name: "Description, row 1" })).toBeTruthy();

    type(name, "acc");
    const list = screen.getByRole("listbox", { name: "Header names" });
    expect(labels()).toEqual(["Accept", "Accept-Encoding"]);
    expect(name.getAttribute("aria-expanded")).toBe("true");
    expect(name.getAttribute("aria-controls")).toBe(list.id);
    const [first, second] = options();
    expect(first?.getAttribute("aria-selected")).toBe("true");
    expect(second?.getAttribute("aria-selected")).toBe("false");
    expect(name.getAttribute("aria-activedescendant")).toBe(first?.id);
    // The popup is a portal outside the grid.
    expect(screen.getByRole("grid").contains(list)).toBe(false);
    expect(first?.textContent).toContain("About Accept");
    expect(suggestName).toHaveBeenLastCalledWith("acc", expect.objectContaining({ id: "a", key: "acc" }), "key");
  });

  it("filters fuzzily and closes when nothing matches or the cell is cleared", () => {
    renderTable([row("a", "")]);
    const name = combo("Key, row 1");
    type(name, "ctype");
    expect(labels()).toEqual(["Content-Type"]);
    type(name, "zzz");
    expect(listbox()).toBeNull();
    type(name, "x-");
    expect(labels()).toEqual(["X-Request-ID"]);
    type(name, "");
    expect(listbox()).toBeNull();
    expect(name.getAttribute("aria-expanded")).toBe("false");
  });

  it("does not show a lone suggestion equal to the text", () => {
    renderTable([row("a", "")]);
    type(combo("Key, row 1"), "Authorization");
    expect(listbox()).toBeNull();
  });

  it("moves the highlight with Up and Down instead of the grid, and accepts with Enter", () => {
    const { rows, onChange } = renderTable([row("a", ""), row("b", "Accept", "*/*")]);
    const name = combo("Key, row 1");
    type(name, "a");
    expect(labels()).toEqual(["Accept", "Accept-Encoding", "Authorization"]);

    key("ArrowDown");
    expect(focused()).toBe(name);
    expect(options()[1]?.getAttribute("aria-selected")).toBe("true");
    expect(name.getAttribute("aria-activedescendant")).toBe(options()[1]?.id);
    key("ArrowUp");
    key("ArrowUp");
    // Wraps to the last item.
    expect(options()[2]?.getAttribute("aria-selected")).toBe("true");
    key("ArrowDown");
    key("ArrowDown");
    onChange.mockClear();
    key("Enter");
    expect(listbox()).toBeNull();
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(rows()[0]).toMatchObject({ id: "a", key: "Accept-Encoding" });
    expect(focused()).toBe(name);
    expect(name.selectionStart).toBe("Accept-Encoding".length);

    // Closed: Down moves to the next row again.
    key("ArrowDown");
    expect(focused()).toBe(combo("Key, row 2"));
    // Enter with no popup changes nothing.
    onChange.mockClear();
    key("Enter");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("closes with Escape without changing the cell, and Tab closes and moves on", () => {
    const { onChange } = renderTable([row("a", "")]);
    const name = combo("Key, row 1");
    type(name, "acc");
    onChange.mockClear();
    const escape = fireEvent.keyDown(name, { key: "Escape" });
    expect(escape).toBe(false); // prevented
    expect(listbox()).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
    expect(name.value).toBe("acc");

    type(name, "acce");
    expect(listbox()).not.toBeNull();
    const tab = fireEvent.keyDown(name, { key: "Tab" });
    expect(tab).toBe(true); // not prevented: focus moves as usual
    expect(listbox()).toBeNull();
    expect(name.value).toBe("acce");
  });

  it("opens with Ctrl+Space or Alt+Down, also in an empty cell", () => {
    renderTable([row("a", "Content-Type", "")]);
    const value = combo("Value, row 1");
    value.focus();
    key(" ", { ctrlKey: true });
    expect(screen.getByRole("listbox", { name: "Header values" })).toBeTruthy();
    expect(labels()).toEqual(["application/json", "application/xml", "text/plain"]);
    key("Escape");
    expect(listbox()).toBeNull();
    key("ArrowDown", { altKey: true });
    expect(labels()).toHaveLength(3);
    // Alt+Down did not move to another row.
    expect(focused()).toBe(value);
  });

  it("suggests values for the row's header name", () => {
    renderTable([row("a", "accept", ""), row("b", "X-Other", "")]);
    type(combo("Value, row 1"), "app");
    expect(labels()).toEqual(["application/json"]);
    expect(suggestValue).toHaveBeenLastCalledWith("app", expect.objectContaining({ key: "accept", value: "app" }), "value");
    type(combo("Value, row 2"), "app");
    expect(listbox()).toBeNull();
  });

  it("accepts with the mouse without taking focus from the cell", () => {
    const { rows } = renderTable([row("a", "Content-Type", "")]);
    const value = combo("Value, row 1");
    type(value, "x");
    const xml = options().find((o) => o.textContent === "application/xml");
    expect(xml).toBeTruthy();
    fireEvent.pointerMove(xml as HTMLElement);
    expect(xml?.getAttribute("aria-selected")).toBe("true");
    const down = fireEvent.mouseDown(xml as HTMLElement);
    expect(down).toBe(false); // prevented, so the input keeps focus
    fireEvent.click(xml as HTMLElement);
    expect(rows()[0]?.value).toBe("application/xml");
    expect(listbox()).toBeNull();
    expect(focused()).toBe(value);
  });

  it("adds a row when a suggestion is accepted in the empty row", () => {
    const { rows } = renderTable([]);
    const draft = combo("Key, row 1");
    type(draft, "auth");
    // Typing added the row; the popup belongs to the same cell.
    expect(rows()).toHaveLength(1);
    expect(draft.getAttribute("aria-expanded")).toBe("true");
    key("Enter");
    expect(rows()).toEqual([expect.objectContaining({ key: "Authorization", enabled: true })]);
    expect(focused()).toBe(draft);
  });

  it("shows a note of the highlighted suggestion", () => {
    renderTable([row("a", "")]);
    type(combo("Key, row 1"), "auth");
    const popup = document.querySelector("[data-autocomplete-popup]") as HTMLElement;
    expect(popup.textContent).toContain("Set from the URL");
    const option = options()[0] as HTMLElement;
    expect(option.querySelector("[title]")?.getAttribute("title")).toBe("Set from the URL");
  });

  it("closes when the cell loses focus", () => {
    renderTable([row("a", "")]);
    const name = combo("Key, row 1");
    type(name, "acc");
    act(() => combo("Value, row 1").focus());
    expect(listbox()).toBeNull();
  });

  it("keeps ⌘Enter inserting a row while the popup is open", () => {
    const primary = IS_MAC ? { metaKey: true } : { ctrlKey: true };
    const { rows } = renderTable([row("a", "")]);
    type(combo("Key, row 1"), "acc");
    key("Enter", primary);
    expect(rows()).toHaveLength(2);
    expect(focused()).toBe(combo("Key, row 2"));
    expect(listbox()).toBeNull();
  });

  it("offers no suggestions when read-only", () => {
    renderTable([row("a", "Accept")], { readOnly: true });
    const name = screen.getByRole("textbox", { name: "Key, row 1" }) as HTMLInputElement;
    name.focus();
    key(" ", { ctrlKey: true });
    expect(listbox()).toBeNull();
  });

  it("places the popup under the cell, or above it near the bottom of the window", () => {
    renderTable([row("a", "")]);
    const name = combo("Key, row 1");
    const cell = name.closest('[role="gridcell"]') as HTMLElement;
    const rect = (top: number, height: number, left = 40, width = 200) =>
      ({ top, bottom: top + height, left, right: left + width, width, height, x: left, y: top, toJSON: () => ({}) }) as DOMRect;
    const popupRect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this === cell) return rect(cellTop, 34);
      if (this.hasAttribute("data-autocomplete-popup")) return rect(0, 120, 0, 180);
      return rect(0, 0);
    });
    let cellTop = 100;
    try {
      Object.defineProperty(window, "innerHeight", { configurable: true, value: 600 });
      type(name, "acc");
      const popup = () => document.querySelector("[data-autocomplete-popup]") as HTMLElement;
      expect(popup().style.top).toBe("136px");
      expect(popup().style.left).toBe("40px");
      expect(popup().style.minWidth).toBe("200px");

      fireEvent.keyDown(name, { key: "Escape" });
      cellTop = 500;
      type(name, "acce");
      expect(popup().style.top).toBe(`${500 - 2 - 120}px`);
    } finally {
      popupRect.mockRestore();
    }
  });
});
