import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { IS_MAC } from "../../lib/shortcuts";
import { createKeyValueRow, KeyValueTable, type KeyValueRow, type KeyValueTableProps } from "./KeyValueTable";

let seq = 0;
const nextId = () => `id${++seq}`;

const sample = (): KeyValueRow[] => [
  { id: "a", enabled: true, key: "limit", value: "20", description: "Page size" },
  { id: "b", enabled: false, key: "cursor", value: "", description: "" },
];

type Props = Partial<Omit<KeyValueTableProps, "rows" | "onChange">>;

/** A parent that owns the rows, like a request editor would. */
function renderTable(initial: KeyValueRow[] = sample(), props: Props = {}) {
  const onChange = vi.fn();
  let current = initial;
  function Parent() {
    const [rows, setRows] = useState(initial);
    current = rows;
    return (
      <KeyValueTable
        label="Query params"
        createId={nextId}
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
  const grid = screen.getByRole("grid", { name: "Query params" });
  return { grid, onChange, rows: () => current };
}

const primary = IS_MAC ? { metaKey: true } : { ctrlKey: true };
const focused = () => document.activeElement as HTMLElement;
const key = (name: string, opts: object = {}) => fireEvent.keyDown(focused(), { key: name, ...opts });
const input = (name: string) => screen.getByRole("textbox", { name }) as HTMLInputElement;
const type = (text: string) => fireEvent.change(focused(), { target: { value: text } });
const caret = (el: HTMLInputElement, pos: number) => el.setSelectionRange(pos, pos);

describe("KeyValueTable", () => {
  it("renders an ARIA grid with enable, key, value, description and delete columns", () => {
    const { grid } = renderTable();
    const headers = within(grid).getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toEqual(["Enabled", "Key", "Value", "Description", "Delete"]);
    // Two rows, the trailing empty row and the header row.
    expect(within(grid).getAllByRole("row")).toHaveLength(4);
    expect(grid.getAttribute("aria-rowcount")).toBe("4");
    expect(input("Key, row 1").value).toBe("limit");
    expect(input("Description, row 1").value).toBe("Page size");
    expect((screen.getByRole("checkbox", { name: "Enabled, row 1" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Enabled, row 2" }) as HTMLInputElement).checked).toBe(false);
    expect(input("Key, row 3").placeholder).toBe("key");
    expect(screen.queryByRole("checkbox", { name: "Enabled, row 3" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete row 3" })).toBeNull();
  });

  it("edits cells and adds a row when typing into the trailing empty row", () => {
    const { onChange, rows } = renderTable();
    input("Value, row 2").focus();
    type("abc");
    expect(rows()[1]).toEqual({ id: "b", enabled: false, key: "cursor", value: "abc", description: "" });

    const draft = input("Key, row 3");
    draft.focus();
    type("sort");
    expect(rows()).toHaveLength(3);
    expect(rows()[2]).toMatchObject({ enabled: true, key: "sort", value: "", description: "" });
    // Same element, still focused, and a new empty row after it.
    expect(input("Key, row 3")).toBe(draft);
    expect(focused()).toBe(draft);
    expect(input("Key, row 4").value).toBe("");
    expect(onChange).toHaveBeenCalledTimes(2);
    // Ids stay stable across edits.
    const id = rows()[2].id;
    type("sort2");
    expect(rows()[2].id).toBe(id);
    expect(rows().map((r) => r.id).slice(0, 2)).toEqual(["a", "b"]);
  });

  it("moves between cells with the arrow keys", () => {
    renderTable();
    const key1 = input("Key, row 1");
    key1.focus();
    caret(key1, 2);
    key("ArrowRight");
    expect(focused()).toBe(key1); // caret inside the text: not at the end yet
    caret(key1, key1.value.length);
    key("ArrowRight");
    expect(focused()).toBe(input("Value, row 1"));
    expect(input("Value, row 1").selectionStart).toBe(0);
    key("ArrowDown");
    expect(focused()).toBe(input("Value, row 2"));
    key("ArrowDown");
    expect(focused()).toBe(input("Value, row 3")); // the trailing empty row
    key("ArrowDown");
    expect(focused()).toBe(input("Value, row 3"));
    key("ArrowUp");
    key("ArrowUp");
    expect(focused()).toBe(input("Value, row 1"));
    key("ArrowUp");
    expect(focused()).toBe(input("Value, row 1"));

    caret(input("Value, row 1"), 0);
    key("ArrowLeft");
    expect(focused()).toBe(key1);
    expect(key1.selectionStart).toBe(key1.value.length);
    caret(key1, 0);
    key("ArrowLeft");
    expect(focused()).toBe(screen.getByRole("checkbox", { name: "Enabled, row 1" }));
    key("ArrowLeft");
    expect(focused()).toBe(screen.getByRole("checkbox", { name: "Enabled, row 1" }));

    input("Description, row 2").focus();
    key("ArrowRight");
    expect(focused()).toBe(screen.getByRole("button", { name: "Delete row 2" }));
    key("ArrowRight");
    expect(focused()).toBe(screen.getByRole("button", { name: "Delete row 2" }));
    // The empty row has no delete button: the nearest cell is the description.
    key("ArrowDown");
    expect(focused()).toBe(input("Description, row 3"));
  });

  it("keeps text cells in the tab order and reaches toggles and delete with the arrows", () => {
    renderTable();
    expect(input("Key, row 1").tabIndex).toBe(0);
    expect(input("Description, row 3").tabIndex).toBe(0);
    expect(screen.getByRole("checkbox", { name: "Enabled, row 1" }).tabIndex).toBe(-1);
    expect(screen.getByRole("button", { name: "Delete row 1" }).tabIndex).toBe(-1);
  });

  it("toggles, adds and deletes rows from the keyboard alone", () => {
    const { rows } = renderTable();
    // Space on the toggle cell (a native checkbox: Space fires a click).
    input("Key, row 2").focus();
    caret(input("Key, row 2"), 0);
    key("ArrowLeft");
    const toggle = screen.getByRole("checkbox", { name: "Enabled, row 2" });
    expect(focused()).toBe(toggle);
    fireEvent.click(toggle);
    expect(rows()[1].enabled).toBe(true);

    // Mod+Enter inserts a row below and focuses its key.
    input("Value, row 1").focus();
    key("Enter", primary);
    expect(rows().map((r) => r.key)).toEqual(["limit", "", "cursor"]);
    expect(focused()).toBe(input("Key, row 2"));
    type("page");
    expect(rows()[1]).toMatchObject({ key: "page", enabled: true });
    expect(rows()[1].id).toBe(`id${seq}`);

    // Mod+Shift+Backspace deletes the row and keeps focus in that column.
    key("Backspace", { ...primary, shiftKey: true });
    expect(rows().map((r) => r.key)).toEqual(["limit", "cursor"]);
    expect(focused()).toBe(input("Key, row 2"));
    expect(input("Key, row 2").value).toBe("cursor");

    // Enter on the delete button (native button activation is a click).
    input("Description, row 2").focus();
    key("ArrowRight");
    const del = screen.getByRole("button", { name: "Delete row 2" });
    expect(focused()).toBe(del);
    fireEvent.click(del);
    expect(rows().map((r) => r.key)).toEqual(["limit"]);
    // Focus moves to the trailing empty row, nearest column.
    expect(focused()).toBe(input("Description, row 2"));
  });

  it("does not treat plain Backspace or Enter as row commands", () => {
    const { onChange } = renderTable();
    input("Key, row 1").focus();
    key("Backspace");
    key("Enter");
    key("Backspace", primary);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("deleting the last row leaves only the empty row and focuses the grid when nothing is left", () => {
    const { grid, rows } = renderTable([createKeyValueRow({ id: "x", key: "only" })], { autoAppend: false });
    input("Key, row 1").focus();
    key("Delete", { ...primary, shiftKey: true });
    expect(rows()).toEqual([]);
    expect(focused()).toBe(grid);
  });

  it("shows an Add row button instead of the empty row when autoAppend is off", () => {
    const { rows } = renderTable(sample(), { autoAppend: false });
    expect(screen.queryByRole("textbox", { name: "Key, row 3" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "+ Add row" }));
    expect(rows()).toHaveLength(3);
    expect(focused()).toBe(input("Key, row 3"));
  });

  it("hides the description column and uses custom placeholders", () => {
    renderTable(sample(), { showDescription: false, placeholders: { key: "header", value: "text" } });
    expect(screen.queryByRole("textbox", { name: "Description, row 1" })).toBeNull();
    expect(screen.queryByRole("columnheader", { name: "Description" })).toBeNull();
    expect(input("Key, row 3").placeholder).toBe("header");
    expect(input("Value, row 3").placeholder).toBe("text");
    // Right from the value cell goes to delete, skipping the missing column.
    input("Value, row 1").focus();
    key("ArrowRight");
    expect(focused()).toBe(screen.getByRole("button", { name: "Delete row 1" }));
  });

  it("edits only values and descriptions of fixed keys", () => {
    const { grid, rows, onChange } = renderTable(sample(), { fixedKeys: true, autoAppend: false });
    expect(within(grid).getAllByRole("row")).toHaveLength(3); // no empty row
    expect(within(grid).queryAllByRole("checkbox")).toHaveLength(0);
    expect(screen.queryByRole("button")).toBeNull(); // no delete, no Add row
    expect(within(grid).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["", "Key", "Value", "Description", ""]);
    expect(input("Key, row 1").readOnly).toBe(true);
    expect(input("Value, row 1").readOnly).toBe(false);

    input("Value, row 1").focus();
    key("Enter", primary);
    key("Backspace", { ...primary, shiftKey: true });
    expect(onChange).not.toHaveBeenCalled();

    type("50");
    fireEvent.change(input("Description, row 2"), { target: { value: "Next page" } });
    expect(rows().map((r) => [r.key, r.value, r.description])).toEqual([
      ["limit", "50", "Page size"],
      ["cursor", "", "Next page"],
    ]);
    // Right from the description goes nowhere; left from the key does not reach a toggle.
    key("ArrowRight");
    input("Key, row 1").focus();
    caret(input("Key, row 1"), 0);
    key("ArrowLeft");
    expect(focused()).toBe(input("Key, row 1"));
  });

  it("is navigable but not editable when read-only", () => {
    const onChange = vi.fn();
    render(<KeyValueTable label="Metadata" rows={sample()} onChange={onChange} readOnly />);
    const grid = screen.getByRole("grid", { name: "Metadata" });
    expect(grid.getAttribute("aria-readonly")).toBe("true");
    expect(within(grid).getAllByRole("row")).toHaveLength(3); // no empty row
    expect(screen.queryByRole("button", { name: /Delete/ })).toBeNull();
    expect(input("Key, row 1").readOnly).toBe(true);

    input("Key, row 1").focus();
    key("ArrowDown");
    expect(focused()).toBe(input("Key, row 2"));
    fireEvent.click(screen.getByRole("checkbox", { name: "Enabled, row 2" }));
    key("Enter", primary);
    key("Backspace", { ...primary, shiftKey: true });
    expect(onChange).not.toHaveBeenCalled();
    expect((screen.getByRole("checkbox", { name: "Enabled, row 2" }) as HTMLInputElement).checked).toBe(false);
  });

  it("draws value cells with renderValue under the input", () => {
    const rows = [createKeyValueRow({ id: "h", key: "Authorization", value: "Bearer {{token}}" })];
    render(
      <KeyValueTable
        label="Headers"
        rows={rows}
        onChange={() => {}}
        renderValue={(value) => <mark>{value}</mark>}
      />,
    );
    const cell = input("Value, row 1").parentElement as HTMLElement;
    const mirror = cell.querySelector("[aria-hidden='true']");
    expect(mirror?.querySelector("mark")?.textContent).toBe("Bearer {{token}}");
    expect(input("Value, row 1").value).toBe("Bearer {{token}}");
    // The key cell has no mirror.
    expect((input("Key, row 1").parentElement as HTMLElement).querySelector("[aria-hidden='true']")).toBeNull();
  });

  it("masks values and shows value actions per row", () => {
    const { onChange } = renderTable(
      [
        { id: "s", enabled: true, key: "token", value: "s3cret", description: "" },
        { id: "p", enabled: true, key: "baseUrl", value: "{{host}}", description: "" },
      ],
      {
        isValueMasked: (row) => row.id === "s",
        renderValue: (value) => <mark>{value}</mark>,
        valueActions: (row, index) => <button type="button">{`Act ${row.key} ${index + 1}`}</button>,
      },
    );
    const grid = screen.getByRole("grid", { name: "Query params" });
    const secret = grid.querySelector<HTMLInputElement>('input[aria-label="Value, row 1"]');
    expect(secret?.type).toBe("password");
    expect(secret?.value).toBe("s3cret");
    // A masked value is not drawn by renderValue.
    expect(grid.textContent).not.toContain("s3cret");
    expect(input("Value, row 2").type).toBe("text");
    expect(screen.getByRole("button", { name: "Act token 1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Act baseUrl 2" })).toBeTruthy();
    // The empty row has no actions, and typing into it keeps its input.
    const draft = input("Value, row 3");
    draft.focus();
    type("x");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(draft);
    expect(screen.getByRole("button", { name: "Act 3" })).toBeTruthy();
    // Arrow keys still move out of a masked field.
    secret?.focus();
    caret(secret as HTMLInputElement, 0);
    key("ArrowLeft");
    expect(focused().getAttribute("aria-label")).toBe("Key, row 1");
  });
});

describe("KeyValueTable bulk edit (HTTP-08)", () => {
  const bulkSwitch = () => screen.getByRole("button", { name: "Bulk edit Query params" });
  const textArea = () => screen.getByRole("textbox", { name: "Query params, bulk edit" }) as HTMLTextAreaElement;

  it("is offered only with bulkEdit", () => {
    renderTable(sample());
    expect(screen.queryByRole("button", { name: "Bulk edit Query params" })).toBeNull();
  });

  it("switches to text and back without a change when the text is not edited", () => {
    const { onChange } = renderTable(sample(), { bulkEdit: true });
    expect(bulkSwitch().getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(bulkSwitch());
    expect(bulkSwitch().getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByRole("grid")).toBeNull();
    expect(textArea().value).toBe("limit: 20\n// cursor:");
    textArea().focus();
    fireEvent.blur(textArea());
    fireEvent.click(bulkSwitch());
    expect(screen.getByRole("grid", { name: "Query params" })).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
    expect(input("Description, row 1").value).toBe("Page size");
  });

  it("applies the edited text as one change when switching back, keeping descriptions", () => {
    const { onChange, rows } = renderTable(sample(), { bulkEdit: true });
    fireEvent.click(bulkSwitch());
    textArea().focus();
    fireEvent.change(textArea(), { target: { value: "limit: 50\n// cursor: abc\n# off: 1\nsort: name" } });
    fireEvent.change(textArea(), { target: { value: "limit: 50\n// cursor: abc\n# off: 1\nsort: name\n" } });
    expect(onChange).not.toHaveBeenCalled();
    // The switch keeps the focus in the text (mousedown), so unmounting applies it.
    fireEvent.mouseDown(bulkSwitch());
    fireEvent.click(bulkSwitch());
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(rows()).toEqual([
      { id: "a", enabled: true, key: "limit", value: "50", description: "Page size" },
      { id: "b", enabled: false, key: "cursor", value: "abc", description: "" },
      expect.objectContaining({ enabled: false, key: "off", value: "1", description: "" }),
      expect.objectContaining({ enabled: true, key: "sort", value: "name", description: "" }),
    ]);
    expect(input("Key, row 4").value).toBe("sort");
  });

  it("applies the text on blur and shows it as formatted", () => {
    const { onChange, rows } = renderTable(sample(), { bulkEdit: true });
    fireEvent.click(bulkSwitch());
    textArea().focus();
    fireEvent.change(textArea(), { target: { value: "limit:5" } });
    fireEvent.blur(textArea());
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(rows()).toEqual([{ id: "a", enabled: true, key: "limit", value: "5", description: "Page size" }]);
    expect(textArea().value).toBe("limit: 5");
  });

  it("follows rows changed from outside while the text is not being edited", () => {
    function Outside() {
      const [rows, setRows] = useState(sample());
      return (
        <>
          <button type="button" onClick={() => setRows([createKeyValueRow({ key: "q", value: "x" }, nextId)])}>
            reset
          </button>
          <KeyValueTable label="Query params" rows={rows} onChange={setRows} bulkEdit createId={nextId} />
        </>
      );
    }
    render(<Outside />);
    fireEvent.click(bulkSwitch());
    fireEvent.click(screen.getByRole("button", { name: "reset" }));
    expect(textArea().value).toBe("q: x");
  });

  it("is read-only in a read-only table", () => {
    const { onChange } = renderTable(sample(), { bulkEdit: true, readOnly: true });
    fireEvent.click(bulkSwitch());
    expect(textArea().readOnly).toBe(true);
    fireEvent.click(bulkSwitch());
    expect(onChange).not.toHaveBeenCalled();
  });
});
