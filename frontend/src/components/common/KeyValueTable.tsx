import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
  type ReactNode,
  type UIEvent,
} from "react";
import { ariaShortcut, matchesShortcut } from "../../lib/shortcuts";
import type { Suggestion } from "../../lib/suggest";
import { useAutocomplete } from "./Autocomplete";
import { formatBulkText, parseBulkText, sameRows } from "./keyValueBulk";
import { TemplateText, templateTitle, useTemplateVariables } from "./TemplateVariables";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import styles from "./KeyValueTable.module.css";

/** One row of a key/value table: a query param, header, form field, variable… */
export interface KeyValueRow {
  /** Stable id: the React key, kept across edits and reorders. */
  id: string;
  enabled: boolean;
  key: string;
  value: string;
  description: string;
}

/** A text column of the table. */
export type KeyValueColumn = "key" | "value" | "description";

/**
 * Suggestions for a cell: `text` is the cell's text, `row` the row as it is
 * with that text (for the trailing empty row, the row it would become), so
 * value suggestions can depend on the row's key.
 */
export type KeyValueSuggest = (text: string, row: KeyValueRow, column: KeyValueColumn) => readonly Suggestion[];

/** The data attributes that make an element a cell for the table's keyboard navigation. */
export interface KeyValueCellProps {
  "data-kv-row": number;
  "data-kv-col": number;
}

export interface KeyValuePlaceholders {
  key?: string;
  value?: string;
  description?: string;
}

export interface KeyValueTableProps {
  /** The rows, owned by the parent. */
  rows: readonly KeyValueRow[];
  /** Called with the complete next rows after every edit, add, delete or toggle. */
  onChange?: (rows: KeyValueRow[]) => void;
  /** Accessible name of the grid, e.g. "Query params". */
  label: string;
  /** Show the description column. Default true. */
  showDescription?: boolean;
  /** No editing, adding or deleting; cells stay focusable and selectable. */
  readOnly?: boolean;
  placeholders?: KeyValuePlaceholders;
  /**
   * Keep an empty row at the end: typing into it adds a row (Postman-like).
   * When false, an "Add row" button is shown instead. Default true.
   */
  autoAppend?: boolean;
  /**
   * Draws the value cell's text, e.g. to highlight {{variables}}. The input
   * stays the editor: its text is made transparent and the rendered text is
   * shown underneath, aligned to it. Inside a TemplateVariablesProvider (a
   * request's editor) values with {{variables}} are drawn as chips by
   * default, red when they do not resolve, and the cell's title lists their
   * values (ENV-11).
   */
  renderValue?: (value: string, row: KeyValueRow | null) => ReactNode;
  /**
   * Masks a row's value (a password field), e.g. for secret variables. A
   * masked value is not passed to renderValue.
   */
  isValueMasked?: (row: KeyValueRow) => boolean;
  /**
   * Small controls shown at the end of a row's value cell (not in the empty
   * row), e.g. a secret toggle. They are reached with Tab, not the arrows.
   */
  valueActions?: (row: KeyValueRow, index: number) => ReactNode;
  /**
   * Replaces a row's value input (not the empty row's) with other content,
   * e.g. a file chip for a multipart file part; return null to keep the
   * input. Spread `cell` on the content's first focusable element so the
   * arrow keys reach it.
   */
  valueCell?: (row: KeyValueRow, index: number, cell: KeyValueCellProps) => ReactNode | null;
  /**
   * Autocomplete per text column: typing into a cell of the column (or
   * Ctrl+Space / Alt+Down) shows what the function returns in a popup.
   * Only while editable.
   */
  suggest?: Partial<Record<KeyValueColumn, KeyValueSuggest>>;
  /** Accessible names of the suggestion lists per column. Default: "Suggestions". */
  suggestLabels?: Partial<Record<KeyValueColumn, string>>;
  /** Id factory for new rows. Default: newKeyValueRowId. */
  createId?: () => string;
  /**
   * The rows are given (path variables named in the URL): keys are
   * read-only, and there is no enable toggle, no empty row and no adding or
   * deleting. Values and descriptions stay editable. The toggle and delete
   * columns are kept empty, so the columns line up with a table above.
   */
  fixedKeys?: boolean;
  /**
   * Offer a "Bulk edit" switch above the table (HTTP-08): the rows become
   * `key: value` text, a disabled row `// key: value` (keyValueBulk.ts).
   * The text is applied as one change when it loses focus, when the
   * switch goes back to the table and when the table unmounts; rows keep
   * their ids and descriptions by key. Not with fixedKeys.
   */
  bulkEdit?: boolean;
  className?: string;
}

/** Column indexes, in visual and keyboard order. */
const COL_TOGGLE = 0;
const COL_KEY = 1;
const COL_VALUE = 2;
const COL_DESC = 3;
const COL_DELETE = 4;
type TextField = KeyValueColumn;

const DEFAULT_PLACEHOLDERS: Readonly<Record<keyof KeyValuePlaceholders, MessageKey>> = {
  key: "common.keyValue.placeholder.key",
  value: "common.keyValue.placeholder.value",
  description: "common.keyValue.placeholder.description",
};

const COLUMN_NAMES: Readonly<Record<TextField, MessageKey>> = {
  key: "common.keyValue.key",
  value: "common.keyValue.value",
  description: "common.keyValue.description",
};

let idSeq = 0;

/** A new unique row id. */
export function newKeyValueRowId(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === "function") return c.randomUUID();
  idSeq += 1;
  return `kv-${Date.now().toString(36)}-${idSeq}`;
}

/** A row with defaults (enabled, empty strings) and a fresh id unless given. */
export function createKeyValueRow(init: Partial<KeyValueRow> = {}, createId = newKeyValueRowId): KeyValueRow {
  return { enabled: true, key: "", value: "", description: "", ...init, id: init.id ?? createId() };
}

/** A cell's text field: a text input, or a password input for a masked value. */
function isTextField(el: Element): el is HTMLInputElement {
  return el instanceof HTMLInputElement && (el.type === "text" || el.type === "password");
}

function fieldOfCol(col: number): TextField | null {
  return col === COL_KEY ? "key" : col === COL_VALUE ? "value" : col === COL_DESC ? "description" : null;
}

interface FocusTarget {
  row: number;
  col: number;
  /** Where to put the caret in a text cell. */
  caret: "start" | "end";
  /** A sideways move: search this way when the column is missing. */
  dir?: -1 | 1;
}

/**
 * The key/value table editor, shared by params, headers, form fields,
 * variables and metadata. Controlled: the parent owns `rows` and gets the
 * next array from `onChange`.
 *
 * ARIA grid. Keyboard:
 * - Up/Down: same column, previous/next row. Left/Right: previous/next cell
 *   (in a text cell only when the caret is at its start/end).
 * - Tab/Shift+Tab: through the text cells (toggles and delete buttons are
 *   reached with the arrows), then out of the table.
 * - Space on the toggle: enable or disable the row.
 * - ⌘Enter / Ctrl+Enter: insert a row below and focus its key.
 * - ⌘⇧Backspace / Ctrl+Shift+Backspace (or Delete): delete the row. Enter or
 *   Space on the delete button does the same.
 */
export function KeyValueTable({
  rows,
  onChange,
  label,
  showDescription = true,
  readOnly = false,
  placeholders,
  autoAppend = true,
  renderValue,
  isValueMasked,
  valueActions,
  valueCell,
  suggest,
  suggestLabels,
  createId = newKeyValueRowId,
  fixedKeys = false,
  bulkEdit = false,
  className,
}: KeyValueTableProps) {
  const offerBulk = bulkEdit && !fixedKeys;
  const [bulk, setBulk] = useState(false);
  if (bulk && offerBulk) {
    return (
      <>
        <BulkSwitch label={label} bulk onToggle={() => setBulk(false)} />
        <BulkEditor
          label={label}
          rows={rows}
          onChange={readOnly ? undefined : onChange}
          createId={createId}
        />
      </>
    );
  }
  const table = (
    <GridTable
      rows={rows}
      onChange={onChange}
      label={label}
      showDescription={showDescription}
      readOnly={readOnly}
      placeholders={placeholders}
      autoAppend={autoAppend}
      renderValue={renderValue}
      isValueMasked={isValueMasked}
      valueActions={valueActions}
      valueCell={valueCell}
      suggest={suggest}
      suggestLabels={suggestLabels}
      createId={createId}
      fixedKeys={fixedKeys}
      className={className}
    />
  );
  if (!offerBulk) return table;
  return (
    <>
      <BulkSwitch label={label} bulk={false} onToggle={() => setBulk(true)} />
      {table}
    </>
  );
}

/** The switch between the table and bulk edit, right-aligned above it. */
function BulkSwitch({ label, bulk, onToggle }: { label: string; bulk: boolean; onToggle: () => void }) {
  const t = useT();
  return (
    <div className={styles.bulkBar}>
      <button
        type="button"
        className={styles.bulkSwitch}
        aria-pressed={bulk}
        aria-label={t("common.keyValue.bulk.label", { table: label })}
        title={bulk ? t("common.keyValue.bulk.toTable") : t("common.keyValue.bulk.toText")}
        // The editor's text is applied on blur, before the switch unmounts it.
        onMouseDown={(e) => e.preventDefault()}
        onClick={onToggle}
      >
        {t("common.keyValue.bulk")}
      </button>
    </div>
  );
}

interface BulkEditorProps {
  label: string;
  rows: readonly KeyValueRow[];
  onChange?: (rows: KeyValueRow[]) => void;
  createId: () => string;
}

/**
 * The rows as text (HTTP-08). Edits stay local until the text is applied:
 * on blur, and when the editor unmounts (the switch back to the table,
 * another sub-tab), as one onChange, so undo takes it back in one step.
 * Rows changed from outside while the text is not being edited replace it.
 */
function BulkEditor({ label, rows, onChange, createId }: BulkEditorProps) {
  const t = useT();
  const [text, setText] = useState(() => formatBulkText(rows));
  // The text as last formatted from the rows: an unedited text applies nothing.
  const base = useRef(text);
  const latest = useRef({ text, rows, onChange, createId });
  latest.current = { text, rows, onChange, createId };
  const focused = useRef(false);

  const apply = () => {
    const { text: current, rows: currentRows, onChange: emit, createId: newId } = latest.current;
    if (emit === undefined || current === base.current) return;
    const next = parseBulkText(current, currentRows, newId);
    base.current = current;
    if (!sameRows(next, currentRows)) emit(next);
  };

  // Rows from outside (an undo, the URL's query) replace a text not being edited.
  useEffect(() => {
    if (focused.current) return;
    const formatted = formatBulkText(rows);
    base.current = formatted;
    setText(formatted);
  }, [rows]);

  // Unmounting applies the text: the switch back, or leaving the sub-tab.
  useEffect(() => () => apply(), []);

  return (
    <div className={styles.frame}>
      <textarea
        className={styles.bulk}
        aria-label={t("common.keyValue.bulk.editor", { table: label })}
        value={text}
        readOnly={onChange === undefined}
        rows={Math.max(4, text.split("\n").length + 1)}
        placeholder={t("common.keyValue.bulk.placeholder")}
        spellCheck={false}
        autoComplete="off"
        autoCapitalize="off"
        autoCorrect="off"
        onFocus={() => {
          focused.current = true;
        }}
        onBlur={() => {
          focused.current = false;
          apply();
        }}
        onChange={(e) => setText(e.target.value)}
      />
    </div>
  );
}

/** The table itself: KeyValueTable without the bulk edit switch. */
function GridTable({
  rows,
  onChange,
  label,
  showDescription = true,
  readOnly = false,
  placeholders,
  autoAppend = true,
  renderValue,
  isValueMasked,
  valueActions,
  valueCell,
  suggest,
  suggestLabels,
  createId = newKeyValueRowId,
  fixedKeys = false,
  className,
}: KeyValueTableProps) {
  const t = useT();
  const gridRef = useRef<HTMLDivElement>(null);
  const pendingFocus = useRef<FocusTarget | null>(null);
  // Id the trailing empty row will get once something is typed into it. The
  // new row keeps that React key, so the input being typed into keeps focus.
  const [draftId, setDraftId] = useState(createId);
  const editable = !readOnly && onChange !== undefined;
  // Rows can be added, deleted and toggled.
  const growable = editable && !fixedKeys;
  const hasDraft = growable && autoAppend;
  const ph: Required<KeyValuePlaceholders> = {
    key: placeholders?.key ?? t(DEFAULT_PLACEHOLDERS.key),
    value: placeholders?.value ?? t(DEFAULT_PLACEHOLDERS.value),
    description: placeholders?.description ?? t(DEFAULT_PLACEHOLDERS.description),
  };
  const lastCol = growable ? COL_DELETE : showDescription ? COL_DESC : COL_VALUE;
  // The field whose suggestion popup is open, for the list's label, and the caret to restore after accepting.
  const [suggestColumn, setSuggestColumn] = useState<TextField>("key");
  const pendingCaret = useRef<HTMLInputElement | null>(null);
  const templates = useTemplateVariables();

  useLayoutEffect(() => {
    const caretInput = pendingCaret.current;
    pendingCaret.current = null;
    if (caretInput?.isConnected) {
      const end = caretInput.value.length;
      caretInput.setSelectionRange(end, end);
    }
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    focusCell(target);
  });

  const cellsOfRow = (row: number) =>
    Array.from(gridRef.current?.querySelectorAll<HTMLElement>(`[data-kv-row="${row}"][data-kv-col]`) ?? []);

  /**
   * Focuses the cell. When that row has no such cell (the empty row has no
   * toggle or delete button, the description may be hidden), takes the next
   * one in `dir` for a sideways move, or the nearest one for a row change.
   */
  function focusCell({ row, col, caret, dir }: FocusTarget): boolean {
    const cells = cellsOfRow(row);
    const colOf = (el: HTMLElement) => Number(el.dataset.kvCol);
    let best: HTMLElement | undefined;
    if (dir === 1) best = cells.find((el) => colOf(el) >= col);
    else if (dir === -1) best = [...cells].reverse().find((el) => colOf(el) <= col);
    else {
      for (const el of cells) {
        if (!best || Math.abs(colOf(el) - col) < Math.abs(colOf(best) - col)) best = el;
      }
    }
    if (!best) return false;
    best.focus();
    if (isTextField(best)) {
      const pos = caret === "start" ? 0 : best.value.length;
      best.setSelectionRange(pos, pos);
    }
    return true;
  }

  const emit = (next: KeyValueRow[]) => onChange?.(next);

  const setField = (index: number, field: TextField, text: string) => {
    if (index === rows.length) {
      // Typing into the trailing empty row adds it.
      emit([...rows, createKeyValueRow({ id: draftId, [field]: text })]);
      setDraftId(createId());
      return;
    }
    emit(rows.map((r, i) => (i === index ? { ...r, [field]: text } : r)));
  };

  const autocomplete = useAutocomplete({
    label: suggestLabels?.[suggestColumn] ?? t("common.keyValue.suggestions"),
    onAccept: (suggestion, input) => {
      const field = fieldOfCol(Number(input.dataset.kvCol));
      if (field === null || !editable) return;
      if (input.value === suggestion.value) {
        const end = suggestion.value.length;
        input.setSelectionRange(end, end);
        return;
      }
      setField(Number(input.dataset.kvRow), field, suggestion.value);
      pendingCaret.current = input;
    },
  });

  /** Shows the column's suggestions for a cell; typing into an empty cell closes them, Ctrl+Space shows them anyway. */
  const showSuggestions = (input: HTMLInputElement, index: number, field: TextField, explicit: boolean) => {
    const fn = editable ? suggest?.[field] : undefined;
    if (fn === undefined) return false;
    const text = input.value;
    if (text === "" && !explicit) {
      autocomplete.close();
      return true;
    }
    const base = rows[index] ?? { id: draftId, enabled: true, key: "", value: "", description: "" };
    const row = { ...base, [field]: text };
    setSuggestColumn(field);
    const anchor = input.closest<HTMLElement>('[role="gridcell"]') ?? input;
    autocomplete.show({ owner: `${row.id}:${field}`, input, anchor }, fn(text, row, field));
    return true;
  };

  const toggle = (index: number) => {
    if (index >= rows.length) return;
    emit(rows.map((r, i) => (i === index ? { ...r, enabled: !r.enabled } : r)));
  };

  const insertBelow = (index: number) => {
    const at = Math.min(index + 1, rows.length);
    emit([...rows.slice(0, at), createKeyValueRow({}, createId), ...rows.slice(at)]);
    pendingFocus.current = { row: at, col: COL_KEY, caret: "start" };
  };

  const remove = (index: number, col: number) => {
    if (index >= rows.length) return;
    emit(rows.filter((_, i) => i !== index));
    const remaining = rows.length - 1 + (hasDraft ? 1 : 0);
    if (remaining > 0) pendingFocus.current = { row: Math.min(index, remaining - 1), col, caret: "end" };
    else gridRef.current?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    // An open suggestion popup takes Up, Down, Enter and Escape first.
    if (autocomplete.onKeyDown(e)) return;
    const el = e.target as HTMLElement;
    if (el.dataset.kvRow === undefined || el.dataset.kvCol === undefined) return;
    const row = Number(el.dataset.kvRow);
    const col = Number(el.dataset.kvCol);
    const text = isTextField(el) ? el : null;

    if (text && matchesShortcut(e, "table.suggest")) {
      const field = fieldOfCol(col);
      if (field !== null && showSuggestions(text, row, field, true)) {
        e.preventDefault();
        return;
      }
    }

    // "Add row below" and "delete row" come from the shortcut registry (scope "table").
    if (growable && matchesShortcut(e, "table.insertRow")) {
      e.preventDefault();
      insertBelow(row);
      return;
    }
    if (growable && matchesShortcut(e, "table.deleteRow")) {
      e.preventDefault();
      remove(row, col);
      return;
    }
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;

    let target: FocusTarget | null = null;
    switch (e.key) {
      case "ArrowUp":
        target = { row: row - 1, col, caret: "end" };
        break;
      case "ArrowDown":
        target = { row: row + 1, col, caret: "end" };
        break;
      case "ArrowLeft":
        if (text && (text.selectionStart !== 0 || text.selectionEnd !== 0)) return;
        target = col > COL_TOGGLE ? { row, col: col - 1, caret: "end", dir: -1 } : null;
        break;
      case "ArrowRight":
        if (text && (text.selectionStart !== text.value.length || text.selectionEnd !== text.value.length)) return;
        target = col < lastCol ? { row, col: col + 1, caret: "start", dir: 1 } : null;
        break;
      default:
        return;
    }
    e.preventDefault();
    if (target && target.row >= 0) focusCell(target);
  };

  const rowCount = rows.length + (hasDraft ? 1 : 0);
  const gridClass = [styles.grid, showDescription ? styles.withDescription : "", editable ? styles.editable : "", className]
    .filter(Boolean)
    .join(" ");

  const renderRow = (row: KeyValueRow | null, index: number) => {
    const isDraft = row === null;
    const n = index + 1;
    const name = (col: string) => t("common.keyValue.cell", { column: col, row: n });
    const data = (col: number) => ({ "data-kv-row": index, "data-kv-col": col });
    const textCell = (col: number, field: TextField, mono: boolean) => {
      const value = row ? row[field] : "";
      const masked = field === "value" && row !== null && isValueMasked !== undefined && isValueMasked(row);
      const templated = field === "value" && !masked && renderValue === undefined && templates !== null && value.includes("{{");
      const rendered =
        field === "value" && renderValue && !masked
          ? renderValue(value, row)
          : templated
            ? <TemplateText text={value} variables={templates.variables} />
            : null;
      const actions = field === "value" && row !== null && valueActions ? valueActions(row, index) : null;
      const onInput = (ev: ChangeEvent<HTMLInputElement>) => {
        setField(index, field, ev.target.value);
        showSuggestions(ev.target, index, field, false);
      };
      const combobox =
        editable && suggest?.[field] !== undefined ? autocomplete.comboboxProps(`${row?.id ?? draftId}:${field}`) : null;
      const syncScroll = (ev: UIEvent<HTMLInputElement>) => {
        const mirror = ev.currentTarget.previousElementSibling as HTMLElement | null;
        if (mirror) mirror.scrollLeft = ev.currentTarget.scrollLeft;
      };
      // Wrapped for the empty row too, so its input keeps focus when it becomes a row.
      const hasActions = field === "value" && valueActions !== undefined;
      const custom = field === "value" && row !== null && valueCell ? valueCell(row, index, data(col)) : null;
      const fieldEl = custom !== null ? (
        <div className={styles.custom}>{custom}</div>
      ) : (
        <>
          {rendered !== null && (
            <div className={styles.mirror} aria-hidden="true">
              {rendered}
            </div>
          )}
          <input
            {...data(col)}
            {...combobox}
            type={masked ? "password" : "text"}
            className={rendered !== null ? `${styles.input} ${styles.overlay}` : styles.input}
            value={value}
            title={templated ? templateTitle(value, templates.variables) : undefined}
            placeholder={ph[field]}
            aria-label={name(t(COLUMN_NAMES[field]))}
            readOnly={!editable || (fixedKeys && field === "key")}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            onChange={onInput}
            onScroll={rendered !== null ? syncScroll : undefined}
            onSelect={rendered !== null ? syncScroll : undefined}
          />
        </>
      );
      return (
        <div role="gridcell" className={`${styles.cell} ${mono ? styles.mono : styles.desc}`}>
          {hasActions ? (
            <>
              {/* The mirror must stop where the actions start. */}
              <div className={styles.field}>{fieldEl}</div>
              <div className={styles.valueActions}>{actions}</div>
            </>
          ) : (
            fieldEl
          )}
        </div>
      );
    };

    return (
      <div
        key={row?.id ?? draftId}
        role="row"
        aria-rowindex={index + 2}
        className={[styles.row, row && !row.enabled ? styles.disabled : "", isDraft ? styles.draft : ""]
          .filter(Boolean)
          .join(" ")}
        data-draft={isDraft || undefined}
      >
        <div role="gridcell" className={`${styles.cell} ${styles.center}`}>
          {row && !fixedKeys && (
            <input
              {...data(COL_TOGGLE)}
              type="checkbox"
              className={styles.toggle}
              tabIndex={-1}
              checked={row.enabled}
              aria-label={name(t("common.keyValue.enabled"))}
              aria-readonly={!editable || undefined}
              onChange={() => editable && toggle(index)}
            />
          )}
        </div>
        {textCell(COL_KEY, "key", true)}
        {textCell(COL_VALUE, "value", true)}
        {showDescription && textCell(COL_DESC, "description", false)}
        {editable && (
          <div role="gridcell" className={`${styles.cell} ${styles.center}`}>
            {row && growable && (
              <button
                {...data(COL_DELETE)}
                type="button"
                className={styles.delete}
                tabIndex={-1}
                aria-label={t("common.keyValue.deleteRow", { row: n })}
                aria-keyshortcuts={ariaShortcut("table.deleteRow")}
                onClick={() => remove(index, COL_DELETE)}
              >
                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" aria-hidden="true" focusable="false">
                  <path d="M6 6l12 12M18 6 6 18" />
                </svg>
              </button>
            )}
          </div>
        )}
      </div>
    );
  };

  return (
    <div className={styles.frame}>
      <div
        ref={gridRef}
        role="grid"
        aria-label={label}
        aria-rowcount={rowCount + 1}
        aria-colcount={lastCol + 1}
        aria-readonly={!editable || undefined}
        tabIndex={-1}
        className={gridClass}
        data-shortcut-scope={editable ? "table" : undefined}
        onKeyDown={onKeyDown}
      >
        <div role="row" aria-rowindex={1} className={`${styles.row} ${styles.head}`}>
          <div role="columnheader" className={styles.headCell}>
            {!fixedKeys && <span className={styles.srOnly}>{t("common.keyValue.enabled")}</span>}
          </div>
          <div role="columnheader" className={styles.headCell}>
            {t("common.keyValue.key")}
          </div>
          <div role="columnheader" className={styles.headCell}>
            {t("common.keyValue.value")}
          </div>
          {showDescription && (
            <div role="columnheader" className={styles.headCell}>
              {t("common.keyValue.description")}
            </div>
          )}
          {editable && (
            <div role="columnheader" className={styles.headCell}>
              {growable && <span className={styles.srOnly}>{t("common.keyValue.delete")}</span>}
            </div>
          )}
        </div>
        {/* One keyed list, so a draft row that becomes a real row keeps its DOM (and focus). */}
        {hasDraft ? [...rows, null].map((row, i) => renderRow(row, i)) : rows.map((row, i) => renderRow(row, i))}
      </div>
      {growable && !autoAppend && (
        <button type="button" className={styles.add} onClick={() => insertBelow(rows.length - 1)}>
          {t("common.keyValue.addRow")}
        </button>
      )}
      {autocomplete.popup}
    </div>
  );
}
