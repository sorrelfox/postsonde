import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { EditOptions } from "../../commands/autosave";
import { useExtractionJump } from "../../commands/extractionJump";
import { useHttpSend } from "../../commands/useHttpSend";
import {
  RUN_SCOPE,
  SCOPES,
  SOURCES,
  describeExtraction,
  newExtraction,
  scopeInfo,
  sourceInfo,
  withScope,
  withSource,
  type ExtractionDTO,
  type ExtractionResultDTO,
} from "../../lib/extractions";
import { useT } from "../../i18n/react";
import type { RequestData, RequestId } from "../../stores";
import { IconCheck, IconChevron, IconClose, IconInfo, IconLock, IconPin, IconPlus, IconTrash, IconWarning } from "../common/icons";
import { moveItem } from "./SavedMessages";
import styles from "./ExtractionsEditor.module.css";

let idSeq = 0;
function newRowId(): string {
  idSeq += 1;
  return `extraction-${idSeq}`;
}

/**
 * Stable row ids for the rules, so inputs keep focus while typing and move
 * with their row (as the assertions editor's). Rows from elsewhere (undo,
 * a reload) get ids by position.
 */
function useRowIds(list: readonly ExtractionDTO[]): [readonly string[], (next: readonly ExtractionDTO[], ids: string[]) => void] {
  const state = useRef<{ list: readonly ExtractionDTO[]; ids: string[] } | null>(null);
  if (state.current === null || state.current.list !== list) {
    const previous = state.current?.ids ?? [];
    state.current = { list, ids: list.map((_, i) => previous[i] ?? newRowId()) };
  }
  const expect = useCallback((next: readonly ExtractionDTO[], ids: string[]) => {
    state.current = { list: next, ids };
  }, []);
  return [state.current.ids, expect];
}

const EMPTY: readonly ExtractionDTO[] = [];

export interface ExtractionsEditorProps {
  id: RequestId;
  /** The request's extraction rules, in order (missing: none). */
  extractions: readonly ExtractionDTO[] | null | undefined;
  readOnly: boolean;
  /** The request editor's auto-saving edit. */
  edit: (patch: RequestData, options?: EditOptions) => void;
}

/**
 * The Extract sub-tab of the HTTP and GraphQL editors (TEST-05): rules
 * that take a value from every response into a variable, in the
 * assertions table's style: source (JSONPath, header, cookie, regex on the
 * body, XPath, status code) · expression · variable · scope (Runtime, or
 * Run only) · secret and keep toggles. Go applies the rules after each
 * send, in order; each row shows its last result and the response's
 * Variables tab jumps here (commands/extractionJump.ts). Rows can be
 * disabled, moved (the arrows, or Alt+↑/↓ inside the row) and deleted.
 * Only the rules are saved with the request, never the values.
 */
export function ExtractionsEditor({ id, extractions, readOnly, edit }: ExtractionsEditorProps) {
  const t = useT();
  const list = extractions ?? EMPTY;
  const [ids, expectRows] = useRowIds(list);
  const { state } = useHttpSend(id);
  const results = new Map<number, ExtractionResultDTO>();
  for (const r of state.response?.extractions ?? []) results.set(r.index, r);

  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const [pendingFocus, setPendingFocus] = useState<{ id: string; field: "source" | "delete" | "add" } | null>(null);
  const [highlight, setHighlight] = useState<string | null>(null);
  const addRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (pendingFocus === null) return;
    setPendingFocus(null);
    if (pendingFocus.field === "add") {
      addRef.current?.focus();
      return;
    }
    const row = rowRefs.current.get(pendingFocus.id);
    row?.querySelector<HTMLElement>(`[data-field="${pendingFocus.field}"]`)?.focus();
  }, [pendingFocus]);

  useEffect(() => {
    if (highlight === null) return;
    const timer = setTimeout(() => setHighlight(null), 1600);
    return () => clearTimeout(timer);
  }, [highlight]);

  useExtractionJump(id, (index) => {
    const rowId = ids[index];
    if (rowId === undefined) return;
    const row = rowRefs.current.get(rowId);
    row?.scrollIntoView?.({ block: "nearest" });
    row?.querySelector<HTMLElement>('[data-field="source"]')?.focus();
    setHighlight(rowId);
  });

  const change = (next: ExtractionDTO[], nextIds: string[], separate: boolean) => {
    expectRows(next, nextIds);
    edit({ extractions: next }, separate ? { history: "separate" } : undefined);
  };
  const update = (index: number, patch: (e: ExtractionDTO) => ExtractionDTO, separate = false) =>
    change(
      list.map((e, i) => (i === index ? patch(e) : e)),
      [...ids],
      separate,
    );
  const add = () => {
    const rowId = newRowId();
    change([...list, newExtraction()], [...ids, rowId], true);
    setPendingFocus({ id: rowId, field: "source" });
  };
  const remove = (index: number) => {
    const nextIds = ids.filter((_, i) => i !== index);
    change(
      list.filter((_, i) => i !== index),
      nextIds,
      true,
    );
    const neighbour = nextIds[Math.min(index, nextIds.length - 1)];
    setPendingFocus(neighbour !== undefined ? { id: neighbour, field: "delete" } : { id: "", field: "add" });
  };
  const move = (index: number, to: number) => {
    if (to < 0 || to >= list.length) return;
    change(moveItem(list, index, to), moveItem(ids, index, to), true);
  };

  const onRowKeyDown = (e: KeyboardEvent<HTMLDivElement>, index: number) => {
    if (readOnly || !e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    move(index, e.key === "ArrowUp" ? index - 1 : index + 1);
  };

  return (
    <div className={styles.extract}>
      {list.length === 0 ? (
        <p className={styles.empty}>{readOnly ? t("editor.extractions.noneReadOnly") : t("editor.extractions.none")}</p>
      ) : (
        <div className={styles.frame}>
          <div role="table" aria-label={t("editor.extractions.label")} className={styles.table}>
            <div role="row" className={`${styles.row} ${styles.head}`}>
              <div role="columnheader" className={styles.headCell}>
                <span className={styles.srOnly}>{t("editor.extractions.column.enabled")}</span>
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.extractions.column.source")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.extractions.column.expr")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.extractions.column.variable")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.extractions.column.scope")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                <span className={styles.srOnly}>{t("editor.extractions.column.flags")}</span>
              </div>
              <div role="columnheader" className={styles.headCell}>
                <span className={styles.srOnly}>{t("editor.extractions.column.actions")}</span>
              </div>
            </div>
            {list.map((e, i) => {
              const rowId = ids[i] ?? String(i);
              const n = i + 1;
              const source = sourceInfo(e.source);
              const scope = scopeInfo(e.scope);
              const hasExpr = source === undefined || source.expr !== null;
              const run = e.scope === RUN_SCOPE;
              // The last send's result, while the row still sets the same variable.
              const last = e.enabled ? results.get(i) : undefined;
              const result = last !== undefined && last.variable === e.variable && last.source === e.source ? last : undefined;
              const exprLabel =
                e.source === "header"
                  ? t("editor.extractions.expr.header", { n })
                  : e.source === "cookie"
                    ? t("editor.extractions.expr.cookie", { n })
                    : t("editor.extractions.expr.expression", { n });
              return (
                <div
                  key={rowId}
                  ref={(el) => {
                    if (el) rowRefs.current.set(rowId, el);
                    else rowRefs.current.delete(rowId);
                  }}
                  role="row"
                  className={[styles.row, e.enabled ? "" : styles.disabled].filter(Boolean).join(" ")}
                  data-highlight={highlight === rowId || undefined}
                  data-extraction-index={i}
                  onKeyDown={(ev) => onRowKeyDown(ev, i)}
                >
                  <div role="cell" className={`${styles.cell} ${styles.center}`}>
                    <input
                      type="checkbox"
                      className={styles.toggle}
                      checked={e.enabled}
                      aria-label={t("editor.extractions.enabled", { n })}
                      disabled={readOnly}
                      onChange={() => update(i, (x) => ({ ...x, enabled: !x.enabled }), true)}
                    />
                  </div>
                  <div role="cell" className={styles.cell}>
                    <select
                      data-field="source"
                      className={styles.select}
                      aria-label={t("editor.extractions.source", { n })}
                      value={e.source}
                      disabled={readOnly}
                      onChange={(ev) => update(i, (x) => withSource(x, ev.target.value), true)}
                    >
                      {source === undefined && <option value={e.source}>{e.source}</option>}
                      {SOURCES.map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.label}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div role="cell" className={`${styles.cell} ${styles.mono}`}>
                    {hasExpr && (
                      <input
                        type="text"
                        className={styles.input}
                        aria-label={exprLabel}
                        placeholder={source?.expr ?? t("editor.extractions.expr.placeholder")}
                        value={e.expr}
                        readOnly={readOnly}
                        spellCheck={false}
                        autoComplete="off"
                        autoCapitalize="off"
                        autoCorrect="off"
                        onChange={(ev) => update(i, (x) => ({ ...x, expr: ev.target.value }))}
                      />
                    )}
                  </div>
                  <div role="cell" className={`${styles.cell} ${styles.mono}`}>
                    <span className={styles.braces} aria-hidden="true">
                      {"{{"}
                    </span>
                    <input
                      type="text"
                      className={`${styles.input} ${styles.variable}`}
                      aria-label={t("editor.extractions.variable", { n })}
                      placeholder={t("editor.extractions.variable.placeholder")}
                      aria-invalid={e.enabled && e.variable.trim() === "" ? true : undefined}
                      value={e.variable}
                      readOnly={readOnly}
                      spellCheck={false}
                      autoComplete="off"
                      autoCapitalize="off"
                      autoCorrect="off"
                      onChange={(ev) => update(i, (x) => ({ ...x, variable: ev.target.value }))}
                    />
                    <span className={styles.braces} aria-hidden="true">
                      {"}}"}
                    </span>
                  </div>
                  <div role="cell" className={styles.cell}>
                    <select
                      className={styles.select}
                      aria-label={t("editor.extractions.scope", { n })}
                      title={scope?.title}
                      value={scope?.id ?? e.scope}
                      disabled={readOnly}
                      onChange={(ev) => update(i, (x) => withScope(x, ev.target.value), true)}
                    >
                      {scope === undefined && <option value={e.scope}>{e.scope}</option>}
                      {SCOPES.map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.label}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div role="cell" className={`${styles.cell} ${styles.flags}`}>
                    <button
                      type="button"
                      className={styles.flag}
                      aria-label={t("editor.extractions.secret", { n })}
                      aria-pressed={e.secret}
                      title={e.secret ? t("editor.extractions.secret.on") : t("editor.extractions.secret.off")}
                      disabled={readOnly}
                      onClick={() => update(i, (x) => ({ ...x, secret: !x.secret }), true)}
                    >
                      <IconLock size={12} />
                    </button>
                    <button
                      type="button"
                      className={styles.flag}
                      aria-label={t("editor.extractions.persist", { n })}
                      aria-pressed={e.persist && !run}
                      title={
                        run ? t("editor.extractions.persist.run") : e.persist ? t("editor.extractions.persist.on") : t("editor.extractions.persist.off")
                      }
                      disabled={readOnly || run}
                      onClick={() => update(i, (x) => ({ ...x, persist: !x.persist }), true)}
                    >
                      <IconPin size={12} />
                    </button>
                  </div>
                  <div role="cell" className={`${styles.cell} ${styles.actions}`}>
                    {result !== undefined && <ResultMark result={result} />}
                    {!readOnly && (
                      <>
                        <button
                          type="button"
                          className={styles.iconButton}
                          aria-label={t("editor.extractions.moveUp", { n })}
                          aria-keyshortcuts="Alt+ArrowUp"
                          disabled={i === 0}
                          onClick={() => move(i, i - 1)}
                        >
                          <IconChevron direction="up" size={11} />
                        </button>
                        <button
                          type="button"
                          className={styles.iconButton}
                          aria-label={t("editor.extractions.moveDown", { n })}
                          aria-keyshortcuts="Alt+ArrowDown"
                          disabled={i === list.length - 1}
                          onClick={() => move(i, i + 1)}
                        >
                          <IconChevron direction="down" size={11} />
                        </button>
                        <button
                          type="button"
                          data-field="delete"
                          className={`${styles.iconButton} ${styles.delete}`}
                          aria-label={t("editor.extractions.delete", { n, rule: describeExtraction(e) })}
                          onClick={() => remove(i)}
                        >
                          <IconTrash size={11} />
                        </button>
                      </>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
      {!readOnly && (
        <button ref={addRef} type="button" className={styles.add} onClick={add}>
          <IconPlus size={11} />
          {t("editor.extractions.add")}
        </button>
      )}
      <p className={styles.hint}>{t("editor.extractions.hint")}</p>
    </div>
  );
}

/** The last send's result of a row: a tick, a cross, a warning or an info mark (skipped), its message on hover. */
function ResultMark({ result }: { result: ExtractionResultDTO }) {
  const t = useT();
  const params = { message: result.message };
  const [title, label] =
    result.outcome === "set"
      ? [t("editor.extractions.result.set", params), t("editor.extractions.result.set.label", params)]
      : result.outcome === "skipped"
        ? [t("editor.extractions.result.skipped", params), t("editor.extractions.result.skipped.label", params)]
        : result.outcome === "invalid"
          ? [t("editor.extractions.result.invalid", params), t("editor.extractions.result.invalid.label", params)]
          : [t("editor.extractions.result.failed", params), t("editor.extractions.result.failed.label", params)];
  return (
    <span className={styles.result} data-outcome={result.outcome} title={title} role="img" aria-label={label}>
      {result.outcome === "set" ? (
        <IconCheck size={12} />
      ) : result.outcome === "skipped" ? (
        <IconInfo size={12} />
      ) : result.outcome === "invalid" ? (
        <IconWarning size={12} />
      ) : (
        <IconClose size={12} />
      )}
    </span>
  );
}
