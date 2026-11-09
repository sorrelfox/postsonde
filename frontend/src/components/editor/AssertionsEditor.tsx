import { Fragment, useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type UIEvent } from "react";
import { useAssertionJump } from "../../commands/assertionJump";
import type { EditOptions } from "../../commands/autosave";
import { showToast } from "../../commands/toasts";
import { useHttpSend } from "../../commands/useHttpSend";
import {
  MATCHES_SCHEMA,
  MESSAGE_SCOPES,
  OPERATORS,
  TARGETS,
  TYPE_NAMES,
  describeAssertion,
  expectedPlaceholder,
  formatInlineSchema,
  inlineSchemaProblem,
  isInlineSchema,
  newAssertion,
  operatorInfo,
  scopeOf,
  streamTargets,
  targetInfo,
  withOperator,
  withScope,
  withTarget,
  type AssertionDTO,
  type TestResultDTO,
} from "../../lib/assertions";
import { isCancelled, toAppError } from "../../lib/errors";
import { pickSchemaFile } from "../../lib/workspace";
import { useT } from "../../i18n/react";
import type { RequestData, RequestId } from "../../stores";
import { IconCheck, IconChevron, IconClose, IconCode, IconFile, IconPlus, IconTrash, IconWarning } from "../common/icons";
import { TemplateText, templateTitle, useTemplateVariables } from "../common/TemplateVariables";
import { FileAccessNotice, useFileAccessCheck } from "../common/FileAccessNotice";
import { CodeEditor } from "./CodeEditor";
import { moveItem } from "./SavedMessages";
import styles from "./AssertionsEditor.module.css";

let idSeq = 0;
function newRowId(): string {
  idSeq += 1;
  return `assertion-${idSeq}`;
}

/**
 * Stable row ids for the assertions, so inputs keep focus while typing and
 * move with their row. Ids follow the rows through the editor's own
 * changes; rows from elsewhere (undo, a reload) get ids by position.
 */
function useRowIds(assertions: readonly AssertionDTO[]): [readonly string[], (next: readonly AssertionDTO[], ids: string[]) => void] {
  const state = useRef<{ list: readonly AssertionDTO[]; ids: string[] } | null>(null);
  if (state.current === null || state.current.list !== assertions) {
    const previous = state.current?.ids ?? [];
    state.current = { list: assertions, ids: assertions.map((_, i) => previous[i] ?? newRowId()) };
  }
  const expect = useCallback((next: readonly AssertionDTO[], ids: string[]) => {
    state.current = { list: next, ids };
  }, []);
  return [state.current.ids, expect];
}

const EMPTY: readonly AssertionDTO[] = [];

export interface AssertionsEditorProps {
  id: RequestId;
  /** The request's assertions, in order (missing: none). */
  assertions: readonly AssertionDTO[] | null | undefined;
  readOnly: boolean;
  /** The request editor's auto-saving edit. */
  edit: (patch: RequestData, options?: EditOptions) => void;
  /**
   * The request is a stream (gRPC, WebSocket, Socket.IO, SSE; TEST-08):
   * each row also picks which messages it checks (the whole stream, any,
   * every, none, the first, the last or the nth message), and the message
   * count is a target.
   */
  stream?: boolean;
  /** The results the row marks show, when not the last HTTP send's: a stream's (commands/streamTests.ts). */
  results?: readonly RowResult[] | null;
}

/** What a row's result mark needs of a result. */
type RowResult = Pick<TestResultDTO, "index" | "target" | "op" | "outcome" | "message"> & { readonly messages?: string };

/**
 * The Tests sub-tab of the HTTP and GraphQL editors (TEST-01): rows of
 * target · operator · expected in the key/value table's style. The target
 * picker (TEST-02) shows a name or path field for header, cookie, JSONPath,
 * XPath and regex; the operator (TEST-03) hides the expected value when it
 * takes none; the expected value shows {{variables}} as chips and is
 * rendered by Go when the response is checked. Each row can be disabled,
 * moved (the arrows, or Alt+↑/↓ inside the row) and deleted. After a send
 * each row shows its last result; the response's Tests tab jumps here
 * (commands/assertionJump.ts). Changes are auto-saved with the request.
 *
 * A "matches schema" row (TEST-07) takes a JSON Schema file path, chosen
 * with the file button, or an inline schema, edited in a JSON editor that
 * opens under the row.
 *
 * With `stream` (the gRPC, WebSocket, Socket.IO and SSE editors, TEST-08)
 * each row first picks the messages it checks, and the row marks are the
 * latest stream's results (`results`).
 */
export function AssertionsEditor({ id, assertions, readOnly, edit, stream = false, results: given }: AssertionsEditorProps) {
  const t = useT();
  const list = assertions ?? EMPTY;
  const [ids, expectRows] = useRowIds(list);
  const baseId = useId();
  const templates = useTemplateVariables();
  const { state } = useHttpSend(id);
  const results = new Map<number, RowResult>();
  for (const r of given !== undefined ? (given ?? []) : (state.response?.tests ?? [])) results.set(r.index, r);

  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const [pendingFocus, setPendingFocus] = useState<{ id: string; field: "target" | "delete" | "add" } | null>(null);
  const [highlight, setHighlight] = useState<string | null>(null);
  // Rows whose inline schema editor is open.
  const [schemaOpen, setSchemaOpen] = useState<ReadonlySet<string>>(() => new Set());
  const addRef = useRef<HTMLButtonElement>(null);
  const access = useFileAccessCheck();

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

  useAssertionJump(id, (index) => {
    const rowId = ids[index];
    if (rowId === undefined) return;
    const row = rowRefs.current.get(rowId);
    row?.scrollIntoView?.({ block: "nearest" });
    row?.querySelector<HTMLElement>('[data-field="target"]')?.focus();
    setHighlight(rowId);
  });

  const change = (next: AssertionDTO[], nextIds: string[], separate: boolean) => {
    expectRows(next, nextIds);
    edit({ assertions: next }, separate ? { history: "separate" } : undefined);
  };
  const update = (index: number, patch: (a: AssertionDTO) => AssertionDTO, separate = false) =>
    change(
      list.map((a, i) => (i === index ? patch(a) : a)),
      [...ids],
      separate,
    );
  const add = () => {
    const rowId = newRowId();
    // A stream's first check is how many messages came.
    const fresh = stream ? newAssertion({ target: "message_count", op: "gte", expected: "1" }) : newAssertion();
    change([...list, fresh], [...ids, rowId], true);
    setPendingFocus({ id: rowId, field: "target" });
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

  const toggleSchema = (index: number, rowId: string) => {
    const open = !schemaOpen.has(rowId);
    const next = new Set(schemaOpen);
    if (open) next.add(rowId);
    else next.delete(rowId);
    setSchemaOpen(next);
    // An empty expected value starts as an empty inline schema.
    if (open && !readOnly && (list[index]?.expected ?? "").trim() === "") update(index, (x) => ({ ...x, expected: "{}" }), true);
  };

  const chooseSchemaFile = async (index: number) => {
    try {
      const file = await pickSchemaFile();
      if (file.path === "") return;
      update(index, (x) => ({ ...x, expected: file.path }), true);
      // A schema file outside the allowed directories is refused (PRV-10).
      access.check(file.path);
    } catch (err) {
      if (isCancelled(err)) return;
      showToast({
        message: t("editor.assertions.schemaFileFailed", { error: toAppError(err).message }),
        icon: <IconWarning size={14} strokeWidth={1.9} />,
      });
    }
  };

  const syncScroll = (ev: UIEvent<HTMLInputElement>) => {
    const mirror = ev.currentTarget.previousElementSibling as HTMLElement | null;
    if (mirror) mirror.scrollLeft = ev.currentTarget.scrollLeft;
  };

  const typesId = `${baseId}-types`;

  return (
    <div className={styles.tests}>
      {list.length === 0 ? (
        <p className={styles.empty}>
          {readOnly ? t("editor.assertions.noneReadOnly") : stream ? t("editor.assertions.noneStream") : t("editor.assertions.none")}
        </p>
      ) : (
        <div className={styles.frame}>
          <div role="table" aria-label={t("editor.assertions.label")} className={stream ? `${styles.table} ${styles.streamTable}` : styles.table}>
            <div role="row" className={`${styles.row} ${styles.head}`}>
              <div role="columnheader" className={styles.headCell}>
                <span className={styles.srOnly}>{t("editor.assertions.column.enabled")}</span>
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.assertions.column.target")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.assertions.column.expr")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.assertions.column.operator")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                {t("editor.assertions.column.expected")}
              </div>
              <div role="columnheader" className={styles.headCell}>
                <span className={styles.srOnly}>{t("editor.assertions.column.actions")}</span>
              </div>
            </div>
            {list.map((a, i) => {
              const rowId = ids[i] ?? String(i);
              const n = i + 1;
              const target = targetInfo(a.target);
              const op = operatorInfo(a.op);
              const hasExpr = target === undefined || target.expr !== null;
              const hasExpected = op === undefined || op.expected;
              const templated = templates !== null && a.expected.includes("{{");
              const scope = scopeOf(a);
              // The last send's result, while the row still checks the same thing.
              const last = a.enabled ? results.get(i) : undefined;
              const result =
                last !== undefined && last.target === a.target && last.op === a.op && (last.messages ?? "") === scope ? last : undefined;
              const targets = stream ? streamTargets(scope) : TARGETS;
              const exprLabel =
                a.target === "header"
                  ? t("editor.assertions.expr.header", { n })
                  : a.target === "cookie"
                    ? t("editor.assertions.expr.cookie", { n })
                    : t("editor.assertions.expr.expression", { n });
              const isSchema = a.op === MATCHES_SCHEMA;
              const schemaShown = isSchema && schemaOpen.has(rowId);
              const schemaPanelId = `${baseId}-schema-${rowId}`;
              return (
                <Fragment key={rowId}>
                  <div
                    ref={(el) => {
                      if (el) rowRefs.current.set(rowId, el);
                      else rowRefs.current.delete(rowId);
                    }}
                    role="row"
                    className={[styles.row, a.enabled ? "" : styles.disabled].filter(Boolean).join(" ")}
                    data-highlight={highlight === rowId || undefined}
                    data-assertion-index={i}
                    onKeyDown={(e) => onRowKeyDown(e, i)}
                  >
                    <div role="cell" className={`${styles.cell} ${styles.center}`}>
                      <input
                        type="checkbox"
                        className={styles.toggle}
                        checked={a.enabled}
                        aria-label={t("editor.assertions.enabled", { n })}
                        disabled={readOnly}
                        onChange={() => update(i, (x) => ({ ...x, enabled: !x.enabled }), true)}
                      />
                    </div>
                    <div role="cell" className={stream ? `${styles.cell} ${styles.targetPick}` : styles.cell}>
                      {stream && (
                        <select
                          data-field="scope"
                          className={styles.select}
                          aria-label={t("editor.assertions.scope", { n })}
                          value={scope}
                          disabled={readOnly}
                          onChange={(e) => update(i, (x) => withScope(x, e.target.value), true)}
                        >
                          {!MESSAGE_SCOPES.some((s) => s.id === scope) && <option value={scope}>{scope}</option>}
                          {MESSAGE_SCOPES.map((s) => (
                            <option key={s.id} value={s.id}>
                              {s.label}
                            </option>
                          ))}
                        </select>
                      )}
                      {stream && scope === "nth" && (
                        <input
                          type="number"
                          min={1}
                          className={`${styles.input} ${styles.index}`}
                          aria-label={t("editor.assertions.index", { n })}
                          value={a.index ?? 1}
                          readOnly={readOnly}
                          onChange={(e) => update(i, (x) => ({ ...x, index: Math.max(1, Math.trunc(Number(e.target.value)) || 1) }))}
                        />
                      )}
                      <select
                        data-field="target"
                        className={styles.select}
                        aria-label={t("editor.assertions.target", { n })}
                        value={a.target}
                        disabled={readOnly}
                        onChange={(e) => update(i, (x) => withTarget(x, e.target.value), true)}
                      >
                        {!targets.some((x) => x.id === a.target) && <option value={a.target}>{target?.label ?? a.target}</option>}
                        {targets.map((target) => (
                          <option key={target.id} value={target.id}>
                            {target.label}
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
                          placeholder={target?.expr ?? t("editor.assertions.expr.placeholder")}
                          value={a.expr}
                          readOnly={readOnly}
                          spellCheck={false}
                          autoComplete="off"
                          autoCapitalize="off"
                          autoCorrect="off"
                          onChange={(e) => update(i, (x) => ({ ...x, expr: e.target.value }))}
                        />
                      )}
                    </div>
                    <div role="cell" className={styles.cell}>
                      <select
                        className={styles.select}
                        aria-label={t("editor.assertions.operator", { n })}
                        value={a.op}
                        disabled={readOnly}
                        onChange={(e) => update(i, (x) => withOperator(x, e.target.value), true)}
                      >
                        {op === undefined && <option value={a.op}>{a.op}</option>}
                        {OPERATORS.map((o) => (
                          <option key={o.id} value={o.id}>
                            {o.label}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div role="cell" className={`${styles.cell} ${styles.mono}`}>
                      {hasExpected && (
                        <div className={styles.field}>
                          {templated && (
                            <div className={styles.mirror} aria-hidden="true">
                              <TemplateText text={a.expected} variables={templates.variables} />
                            </div>
                          )}
                          <input
                            type="text"
                            className={templated ? `${styles.input} ${styles.overlay}` : styles.input}
                            aria-label={t("editor.assertions.expected", { n })}
                            placeholder={expectedPlaceholder(a)}
                            title={templated ? templateTitle(a.expected, templates.variables) : undefined}
                            list={a.op === "is_type" ? typesId : undefined}
                            value={a.expected}
                            readOnly={readOnly}
                            spellCheck={false}
                            autoComplete="off"
                            autoCapitalize="off"
                            autoCorrect="off"
                            onChange={(e) => update(i, (x) => ({ ...x, expected: e.target.value }))}
                            onScroll={templated ? syncScroll : undefined}
                            onSelect={templated ? syncScroll : undefined}
                          />
                        </div>
                      )}
                      {isSchema && (
                        <span className={styles.schemaButtons}>
                          {!readOnly && (
                            <button
                              type="button"
                              className={styles.iconButton}
                              aria-label={t("editor.assertions.chooseSchema", { n })}
                              title={t("editor.assertions.chooseSchema.title")}
                              onClick={() => void chooseSchemaFile(i)}
                            >
                              <IconFile size={12} />
                            </button>
                          )}
                          <button
                            type="button"
                            className={styles.iconButton}
                            aria-label={t("editor.assertions.editSchema", { n })}
                            title={schemaShown ? t("editor.assertions.editSchema.close") : t("editor.assertions.editSchema.open")}
                            aria-expanded={schemaShown}
                            aria-controls={schemaShown ? schemaPanelId : undefined}
                            data-active={schemaShown || undefined}
                            onClick={() => toggleSchema(i, rowId)}
                          >
                            <IconCode size={12} />
                          </button>
                        </span>
                      )}
                    </div>
                    <div role="cell" className={`${styles.cell} ${styles.actions}`}>
                      {result !== undefined && <ResultMark result={result} />}
                      {!readOnly && (
                        <>
                          <button
                            type="button"
                            className={styles.iconButton}
                            aria-label={t("editor.assertions.moveUp", { n })}
                            aria-keyshortcuts="Alt+ArrowUp"
                            disabled={i === 0}
                            onClick={() => move(i, i - 1)}
                          >
                            <IconChevron direction="up" size={11} />
                          </button>
                          <button
                            type="button"
                            className={styles.iconButton}
                            aria-label={t("editor.assertions.moveDown", { n })}
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
                            aria-label={t("editor.assertions.delete", { n, assertion: describeAssertion(a) })}
                            onClick={() => remove(i)}
                          >
                            <IconTrash size={11} />
                          </button>
                        </>
                      )}
                    </div>
                  </div>
                  {schemaShown && (
                    <SchemaPanel
                      id={schemaPanelId}
                      rowId={rowId}
                      n={n}
                      expected={a.expected}
                      readOnly={readOnly}
                      onChange={(text, separate) => update(i, (x) => ({ ...x, expected: text }), separate)}
                    />
                  )}
                </Fragment>
              );
            })}
          </div>
        </div>
      )}
      <FileAccessNotice access={access} />
      {!readOnly && (
        <button ref={addRef} type="button" className={styles.add} onClick={add}>
          <IconPlus size={11} />
          {t("editor.assertions.add")}
        </button>
      )}
      <datalist id={typesId}>
        {TYPE_NAMES.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
    </div>
  );
}

interface SchemaPanelProps {
  id: string;
  rowId: string;
  n: number;
  expected: string;
  readOnly: boolean;
  onChange: (text: string, separate: boolean) => void;
}

/**
 * The inline JSON Schema editor of a "matches schema" row, a full-width
 * row under it: a JSON code editor on the expected value, whether it
 * parses, and a Format button. A file path in the expected value is shown
 * as it is, with a note.
 */
function SchemaPanel({ id, rowId, n, expected, readOnly, onChange }: SchemaPanelProps) {
  const t = useT();
  const inline = isInlineSchema(expected);
  const problem = inline ? inlineSchemaProblem(expected) : null;
  return (
    <div role="row" className={styles.schemaRow} id={id}>
      <div role="cell" className={styles.schemaCell}>
        <CodeEditor
          className={styles.schemaEditor}
          value={expected}
          docKey={`${rowId}-schema`}
          language="json"
          readOnly={readOnly}
          ariaLabel={t("editor.assertions.schema", { n })}
          onChange={(text) => onChange(text, false)}
        />
        <div className={styles.schemaStatus}>
          {!inline ? (
            <span className={styles.schemaNote}>{t("editor.assertions.schema.filePath")}</span>
          ) : problem !== null ? (
            <span className={styles.schemaProblem} role="status">
              {t("editor.assertions.schema.invalid", { problem })}
            </span>
          ) : (
            <span className={styles.schemaNote} role="status">
              {t("editor.assertions.schema.valid")}
            </span>
          )}
          {inline && problem === null && !readOnly && (
            <button type="button" className={styles.schemaFormat} onClick={() => onChange(formatInlineSchema(expected), true)}>
              {t("editor.assertions.schema.format")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/** The last send's result of a row: a tick, a cross or a warning, its message on hover. */
function ResultMark({ result }: { result: RowResult }) {
  const t = useT();
  const params = { message: result.message };
  const [title, label] =
    result.outcome === "passed"
      ? [t("editor.assertions.result.passed", params), t("editor.assertions.result.passed.label", params)]
      : result.outcome === "invalid"
        ? [t("editor.assertions.result.invalid", params), t("editor.assertions.result.invalid.label", params)]
        : [t("editor.assertions.result.failed", params), t("editor.assertions.result.failed.label", params)];
  return (
    <span className={styles.result} data-outcome={result.outcome} title={title} role="img" aria-label={label}>
      {result.outcome === "passed" ? <IconCheck size={12} /> : result.outcome === "invalid" ? <IconWarning size={12} /> : <IconClose size={12} />}
    </span>
  );
}
