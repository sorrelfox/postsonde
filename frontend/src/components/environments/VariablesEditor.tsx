import { useCallback, useEffect, useId, useRef, useState } from "react";
import { formatError } from "../../lib/errors";
import { formatRawVariables, parseRawVariables } from "../../lib/rawVariables";
import { SECRET_MASK } from "../../lib/templateVariables";
import { useT } from "../../i18n/react";
import type { EnvironmentVariable } from "../../stores";
import { CodeEditor } from "../editor/CodeEditor";
import { IconClose, IconEye, IconEyeOff, IconLock, IconMonitor } from "../common/icons";
import { createKeyValueRow, KeyValueTable, type KeyValueRow } from "../common/KeyValueTable";
import { inheritedVariables, keepRows, rowsToVariables, variablesToRows, type VariableRows } from "./variableRows";
import styles from "./EnvironmentsPanel.module.css";

/** Edits are saved this long after the last change, and when the editor closes. */
export const SAVE_DELAY_MS = 400;

export interface VariablesEditorProps {
  /** The table's accessible name, e.g. "Variables of Staging". */
  label: string;
  /** The variables to edit. The editor reads them once: remount it (key) for another set. */
  variables: readonly EnvironmentVariable[];
  /** Saves the variables (an environment's, or the global ones); rejects with the error to show. */
  onSave: (variables: EnvironmentVariable[]) => Promise<unknown>;
  readOnly: boolean;
  /**
   * The base environment's variables (the global ones, ENV-05) that an
   * environment inherits, listed under its table with what the environment
   * overrides; undefined for the global variables themselves, null while
   * they load.
   */
  base?: readonly EnvironmentVariable[] | null;
}

/** Shows {{variables}} in the variable colour. */
function highlightVariables(value: string) {
  return value.split(/(\{\{[^{}]*\}\})/).map((part, i) =>
    i % 2 === 1 ? (
      <span key={i} className={styles.variable}>
        {part}
      </span>
    ) : (
      part
    ),
  );
}

/**
 * The variables of one environment, or the project's global variables, in
 * the key/value table. Changes are saved automatically. A lock button per
 * row marks a variable secret: its value is masked (the eye shows it) and
 * kept in local storage, never in the project file. A monitor button gives
 * a variable that is not secret a local value (SEC-02): edited under the
 * table, kept on this computer only, it replaces the shared value here
 * (struck through in the table) while the project file keeps the shared
 * value for everyone else.
 *
 * The Table / JSON switch (ENV-07) shows the same variables as JSON text
 * (lib/rawVariables.ts): every edit that parses replaces the rows and is
 * saved like a table edit; secret values are masked in the text and the
 * mask keeps them. The table cannot be shown again while the text does not
 * parse. An environment's editor also lists the base environment's
 * variables it inherits (`base`), with a button to override one.
 */
export function VariablesEditor({ label, variables: initial, onSave, readOnly, base }: VariablesEditorProps) {
  const t = useT();
  const baseId = useId();
  const [state, setState] = useState<VariableRows>(() => variablesToRows(initial));
  const [mode, setMode] = useState<"table" | "json">("table");
  const [rawText, setRawText] = useState("");
  const [rawError, setRawError] = useState<string | null>(null);
  // The latest onSave, so flush keeps its identity across renders.
  const save = useRef(onSave);
  useEffect(() => {
    save.current = onSave;
  });
  const [revealed, setRevealed] = useState<Record<string, true>>({});
  const [error, setError] = useState<string | null>(null);

  // The latest unsaved rows, the debounce timer and the chain of saves (one at a time, in order).
  const pending = useRef<VariableRows | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const chain = useRef<Promise<unknown>>(Promise.resolve());
  const mounted = useRef(true);

  const flush = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current);
      timer.current = null;
    }
    const next = pending.current;
    if (next === null) return;
    pending.current = null;
    const variables = rowsToVariables(next);
    chain.current = chain.current.then(() =>
      save.current(variables).then(
        () => {
          if (mounted.current) setError(null);
        },
        (err: unknown) => {
          console.warn(`environments: could not save the variables: ${formatError(err)}`);
          if (mounted.current) setError(formatError(err));
        },
      ),
    );
  }, []);

  useEffect(() => {
    mounted.current = true;
    const onHide = () => flush();
    window.addEventListener("pagehide", onHide);
    return () => {
      mounted.current = false;
      window.removeEventListener("pagehide", onHide);
      flush();
    };
  }, [flush]);

  const update = (next: VariableRows) => {
    setState(next);
    if (readOnly) return;
    pending.current = next;
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = setTimeout(flush, SAVE_DELAY_MS);
  };

  // Drop the flags and local values of deleted rows.
  const onRowsChange = (rows: KeyValueRow[]) => update(keepRows(state, rows));

  const toggleSecret = (row: KeyValueRow) => {
    const secret = { ...state.secret };
    if (secret[row.id]) delete secret[row.id];
    else secret[row.id] = true;
    update({ ...state, secret });
  };

  // The row whose local value field should take focus once it is shown.
  const focusLocal = useRef<string | null>(null);
  useEffect(() => {
    const rowId = focusLocal.current;
    if (rowId === null) return;
    focusLocal.current = null;
    document.getElementById(`${baseId}-local-${rowId}`)?.focus();
  });

  /** Gives the row a local value (starting from its shared value), or removes it. */
  const toggleLocal = (row: KeyValueRow) => {
    const local = { ...state.local };
    if (row.id in local) delete local[row.id];
    else {
      local[row.id] = row.value;
      focusLocal.current = row.id;
    }
    update({ ...state, local });
  };

  const setLocal = (rowId: string, value: string) => update({ ...state, local: { ...state.local, [rowId]: value } });

  const toggleRevealed = (row: KeyValueRow) => {
    setRevealed((r) => {
      const next = { ...r };
      if (next[row.id]) delete next[row.id];
      else next[row.id] = true;
      return next;
    });
  };

  const valueActions = (row: KeyValueRow, index: number) => {
    const n = index + 1;
    const isSecret = state.secret[row.id] === true;
    const hasLocal = row.id in state.local;
    const shown = revealed[row.id] === true;
    return (
      <>
        {!isSecret && (
          <button
            type="button"
            className={hasLocal ? `${styles.rowButton} ${styles.rowButtonLocal}` : styles.rowButton}
            aria-label={t("environments.variables.localRow", { row: n })}
            aria-pressed={hasLocal}
            title={hasLocal ? t("environments.variables.localOn") : t("environments.variables.localOff")}
            disabled={readOnly}
            onClick={() => toggleLocal(row)}
          >
            <IconMonitor size={12} />
          </button>
        )}
        {isSecret && (
          <button
            type="button"
            className={styles.rowButton}
            aria-label={
              shown
                ? t("environments.variables.hideValueRow", { row: n })
                : t("environments.variables.showValueRow", { row: n })
            }
            title={shown ? t("environments.variables.hideValue") : t("environments.variables.showValue")}
            onClick={() => toggleRevealed(row)}
          >
            {shown ? <IconEyeOff size={12} /> : <IconEye size={12} />}
          </button>
        )}
        <button
          type="button"
          className={isSecret ? `${styles.rowButton} ${styles.rowButtonOn}` : styles.rowButton}
          aria-label={t("environments.variables.secretRow", { row: n })}
          aria-pressed={isSecret}
          title={
            hasLocal
              ? t("environments.variables.secretLocal")
              : isSecret
                ? t("environments.variables.secretOn")
                : t("environments.variables.secretOff")
          }
          // A local value is not a secret: remove it first (docs/environments.md#local-values).
          disabled={readOnly || hasLocal}
          onClick={() => toggleSecret(row)}
        >
          <IconLock size={12} />
        </button>
      </>
    );
  };

  // The shared value of a row with a local value is drawn struck through: this computer sends the local one.
  const renderValue = (value: string, row: KeyValueRow | null) =>
    row !== null && row.id in state.local ? (
      <span className={styles.overridden}>{highlightVariables(value)}</span>
    ) : (
      highlightVariables(value)
    );

  const localRows = state.rows.filter((r) => r.id in state.local && state.secret[r.id] !== true);

  const showJson = () => {
    setRawText(formatRawVariables(rowsToVariables(state)));
    setRawError(null);
    setMode("json");
  };

  // Each edit of the text that parses replaces the rows (and is saved); one that does not is only shown.
  const onRawChange = (text: string) => {
    setRawText(text);
    const parsed = parseRawVariables(text, rowsToVariables(state));
    if (!parsed.ok) {
      setRawError(parsed.error);
      return;
    }
    setRawError(null);
    update(variablesToRows(parsed.variables));
  };

  /** Adds a row that overrides base variable `v` in this environment, starting from its shared value. */
  const override = (v: EnvironmentVariable) => {
    const row = createKeyValueRow({ key: v.name, value: v.secret ? "" : v.value, description: v.description });
    update({
      rows: [...state.rows, row],
      secret: v.secret ? { ...state.secret, [row.id]: true } : state.secret,
      local: state.local,
    });
  };

  const inherited = base === undefined || base === null ? null : inheritedVariables(base, state);

  return (
    <div className={styles.variables}>
      <div className={styles.modes} role="group" aria-label={t("environments.variables.mode.label", { label })}>
        <button
          type="button"
          className={styles.modeButton}
          aria-pressed={mode === "table"}
          disabled={mode === "json" && rawError !== null}
          title={mode === "json" && rawError !== null ? t("environments.variables.mode.tableBlocked") : undefined}
          onClick={() => setMode("table")}
        >
          {t("environments.variables.mode.table")}
        </button>
        <button
          type="button"
          className={styles.modeButton}
          aria-pressed={mode === "json"}
          onClick={() => {
            if (mode !== "json") showJson();
          }}
        >
          {t("environments.variables.mode.json")}
        </button>
      </div>
      {mode === "json" ? (
        <>
          <CodeEditor
            className={styles.rawEditor}
            language="json"
            ariaLabel={t("environments.variables.raw.label", { label })}
            value={rawText}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onRawChange}
            historyKey={null}
          />
          {rawError !== null && (
            <p className={styles.error} role="status">
              {rawError}
            </p>
          )}
          <p className={styles.note}>{t("environments.variables.raw.note", { mask: SECRET_MASK })}</p>
        </>
      ) : (
        <KeyValueTable
          label={label}
          rows={state.rows}
          onChange={readOnly ? undefined : onRowsChange}
          readOnly={readOnly}
          showDescription={false}
          placeholders={{
            key: t("environments.variables.placeholder.name"),
            value: t("common.keyValue.placeholder.value"),
          }}
          renderValue={renderValue}
          isValueMasked={(row) => state.secret[row.id] === true && revealed[row.id] !== true}
          valueActions={valueActions}
        />
      )}
      {mode === "table" && inherited !== null && inherited.length > 0 && (
        <div className={styles.localValues} role="group" aria-labelledby={`${baseId}-base-title`}>
          <p id={`${baseId}-base-title`} className={styles.localTitle}>
            {t("environments.variables.base.title")}
          </p>
          <ul className={styles.baseList}>
            {inherited.map(({ variable: v, overridden }, i) => (
              <li key={`${i}-${v.name}`} className={styles.baseRow} data-overridden={overridden || undefined}>
                <span className={styles.localName} title={v.name}>
                  {v.name}
                </span>
                <span className={overridden ? `${styles.baseValue} ${styles.overridden}` : styles.baseValue}>
                  {v.secret
                    ? SECRET_MASK
                    : (v.localValue ?? v.value) === ""
                      ? t("environments.variables.base.empty")
                      : highlightVariables(v.localValue ?? v.value)}
                </span>
                {overridden ? (
                  <span className={styles.baseState}>{t("environments.variables.base.overridden")}</span>
                ) : (
                  <button
                    type="button"
                    className={styles.smallButton}
                    aria-label={t("environments.variables.base.overrideName", { name: v.name })}
                    title={t("environments.variables.base.overrideTitle")}
                    disabled={readOnly}
                    onClick={() => override(v)}
                  >
                    {t("environments.variables.base.override")}
                  </button>
                )}
              </li>
            ))}
          </ul>
          <p className={styles.localNote}>{t("environments.variables.base.note")}</p>
        </div>
      )}
      {mode === "table" && localRows.length > 0 && (
        <div className={styles.localValues} role="group" aria-labelledby={`${baseId}-local-title`}>
          <p id={`${baseId}-local-title`} className={styles.localTitle}>
            {t("environments.variables.local.title")}
          </p>
          {localRows.map((row) => {
            const name = row.key === "" ? t("environments.variables.local.unnamed") : row.key;
            const inputId = `${baseId}-local-${row.id}`;
            return (
              <div key={row.id} className={styles.localRow}>
                <label htmlFor={inputId} className={styles.localName} title={name}>
                  {name}
                </label>
                <input
                  id={inputId}
                  className={styles.localInput}
                  value={state.local[row.id]}
                  placeholder={t("environments.variables.local.placeholder")}
                  aria-describedby={`${inputId}-shared`}
                  readOnly={readOnly}
                  spellCheck={false}
                  autoComplete="off"
                  autoCapitalize="off"
                  autoCorrect="off"
                  onChange={(e) => setLocal(row.id, e.target.value)}
                />
                <button
                  type="button"
                  className={styles.rowButton}
                  aria-label={t("environments.variables.local.remove", { name })}
                  title={t("environments.variables.local.removeTitle")}
                  disabled={readOnly}
                  onClick={() => toggleLocal(row)}
                >
                  <IconClose size={11} />
                </button>
                <span id={`${inputId}-shared`} className={styles.localShared}>
                  {row.value === ""
                    ? t("environments.variables.local.sharedEmpty")
                    : t("environments.variables.local.shared", { value: row.value })}
                </span>
              </div>
            );
          })}
          <p className={styles.localNote}>{t("environments.variables.local.note")}</p>
        </div>
      )}
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
