// The response filter (HTTP-36): the filter bar above the body, the
// state behind it, and the filtered result in a read-only editor.
// Evaluation is Go's (HTTPService.FilterBody, lib/responseFilter.ts), on
// the bytes received rather than the view's clipped text.
import { forwardRef, useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { isCancelled, toAppError } from "../../lib/errors";
import type { ResponseDTO } from "../../lib/http";
import {
  BODY_GONE_CODE,
  EMPTY_REQUEST_FILTER,
  FILTER_SYNTAXES,
  addRecentFilter,
  defaultFilterSyntax,
  filterPlaceholder,
  filterResponseBody,
  loadRequestFilter,
  saveRequestFilter,
  type FilterBodyResultDTO,
  type FilterErrorDTO,
  type FilterSyntax,
  type RequestFilter,
} from "../../lib/responseFilter";
import { formatNumber } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { IconClose, IconFilter, IconWarning } from "../common/icons";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import type { EditorLanguage } from "../editor/languages";
import { formatSize } from "../messagelog/format";
import styles from "./ResponseView.module.css";

/** How long typing must pause before the expression is evaluated. */
export const FILTER_DEBOUNCE_MS = 250;
/** How long the filter must stay unchanged before it is saved. */
const SAVE_DELAY_MS = 500;

/** Where a request's filter is remembered: its workspace and id. */
export interface FilterPersistence {
  workspaceId: string | null;
  requestId: string;
}

/** An evaluation of one expression on one response. */
type Evaluation =
  | { status: "done"; result: FilterBodyResultDTO; shown: boolean }
  | { status: "failed"; message: string; hint: string };

interface EvalFor {
  response: ResponseDTO;
  syntax: FilterSyntax;
  expression: string;
}

export interface ResponseFilterState {
  syntax: FilterSyntax;
  /** The syntax follows the content type (the user did not pick one). */
  autoSyntax: boolean;
  expression: string;
  /** A filter is set: the view shows its result instead of the body. */
  active: boolean;
  /** The expression is being evaluated (typing, or Go working). */
  pending: boolean;
  /** The latest evaluation for this response, possibly of an older expression while pending. */
  evaluation: Evaluation | null;
  /** The expression the evaluation is of. */
  evaluated: string;
  recent: string[];
  setSyntax: (syntax: FilterSyntax) => void;
  setExpression: (expression: string) => void;
  clear: () => void;
  /** Remembers the expression among the recent ones when it gave a result. */
  commit: () => void;
}

/**
 * The filter of a response: the syntax (picked, or from the content
 * type), the expression, and its evaluation, debounced and cancelled when
 * the expression changes. With `persist`, the filter is the request's: it
 * is loaded from and saved to the workspace's local UI state, so the next
 * response of the request (and the next session) is filtered the same way.
 */
export function useResponseFilter(response: ResponseDTO, persist?: FilterPersistence): ResponseFilterState {
  const key = persist !== undefined && persist.workspaceId !== null ? JSON.stringify([persist.workspaceId, persist.requestId]) : "";
  const load = (): RequestFilter => {
    if (persist === undefined || persist.workspaceId === null) return EMPTY_REQUEST_FILTER;
    return loadRequestFilter(persist.workspaceId, persist.requestId);
  };
  // `changed`: edited since it was loaded, so it is saved.
  const [state, setState] = useState<{ key: string; filter: RequestFilter; changed: boolean }>(() => ({ key, filter: load(), changed: false }));
  let filter = state.filter;
  if (state.key !== key) {
    // Another request: its own filter.
    filter = load();
    setState({ key, filter, changed: false });
  }

  // Saved once it stays unchanged for a moment, and when the view goes.
  const pendingSave = useRef<(() => void) | null>(null);
  useEffect(() => {
    if (persist === undefined || persist.workspaceId === null || state.key !== key || !state.changed) return;
    const { workspaceId, requestId } = persist;
    const f = state.filter;
    const save = () => {
      pendingSave.current = null;
      saveRequestFilter(workspaceId, requestId, f);
    };
    pendingSave.current = save;
    const timer = setTimeout(save, SAVE_DELAY_MS);
    return () => clearTimeout(timer);
    // Only a change of the filter is saved; `persist` and `key` are read with it.
  }, [state]);
  useEffect(() => () => pendingSave.current?.(), []);

  const update = (next: (f: RequestFilter) => RequestFilter) => setState((s) => ({ key: s.key, filter: next(s.filter), changed: true }));

  const syntax = filter.syntax ?? defaultFilterSyntax(response);
  const expression = filter.expression;
  const trimmed = expression.trim();

  const [evaluation, setEvaluation] = useState<{ for: EvalFor; evaluation: Evaluation } | null>(null);
  useEffect(() => {
    if (trimmed === "") return;
    const target: EvalFor = { response, syntax, expression: trimmed };
    let stopped = false;
    let call: { cancel?: () => void } | null = null;
    const run = (shown: boolean) => {
      const promise = filterResponseBody(response, syntax, trimmed, shown);
      call = promise;
      promise.then(
        (result) => {
          if (!stopped) setEvaluation({ for: target, evaluation: { status: "done", result, shown } });
        },
        (reason: unknown) => {
          if (stopped || isCancelled(reason)) return;
          const err = toAppError(reason);
          if (!shown && err.code === BODY_GONE_CODE) {
            // The bytes are no longer kept: filter the text the view shows.
            run(true);
            return;
          }
          setEvaluation({ for: target, evaluation: { status: "failed", message: err.message, hint: err.hint } });
        },
      );
    };
    const timer = setTimeout(() => run(false), FILTER_DEBOUNCE_MS);
    return () => {
      stopped = true;
      clearTimeout(timer);
      call?.cancel?.();
    };
  }, [response, syntax, trimmed]);

  const current = evaluation !== null && evaluation.for.response === response ? evaluation : null;
  const upToDate = current !== null && current.for.syntax === syntax && current.for.expression === trimmed;

  return {
    syntax,
    autoSyntax: filter.syntax === null,
    expression,
    active: trimmed !== "",
    pending: trimmed !== "" && !upToDate,
    // An evaluation in the other syntax would be read the wrong way.
    evaluation: current !== null && current.for.syntax === syntax ? current.evaluation : null,
    evaluated: current?.for.expression ?? "",
    recent: filter.recent.filter((r) => r.syntax === syntax).map((r) => r.expression),
    setSyntax: (s) => update((f) => ({ ...f, syntax: s === defaultFilterSyntax(response) ? null : s })),
    setExpression: (e) => update((f) => ({ ...f, expression: e })),
    clear: () => update((f) => ({ ...f, expression: "" })),
    commit: () => {
      if (!upToDate || current.evaluation.status !== "done" || !current.evaluation.result.valid) return;
      update((f) => ({ ...f, recent: addRecentFilter(f.recent, { syntax, expression: trimmed }) }));
    },
  };
}

export interface FilterBarProps {
  filter: ResponseFilterState;
  /** Opens the search panel of the body shown (Mod-F in the filter box). */
  onSearch: () => void;
}

/** The filter bar: JSONPath / XPath, the expression with its recent ones, the match count, clear. */
export function FilterBar({ filter, onSearch }: FilterBarProps) {
  const t = useT();
  const inputRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const { evaluation } = filter;
  const error = evaluation?.status === "done" ? evaluation.result.error : null;

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape" && filter.expression !== "") {
      e.preventDefault();
      e.stopPropagation();
      filter.clear();
    } else if (e.key === "Enter") {
      e.preventDefault();
      filter.commit();
    } else if (e.key.toLowerCase() === "f" && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey) {
      e.preventDefault();
      onSearch();
    }
  };

  const showPosition = (offset: number) => {
    const input = inputRef.current;
    if (input === null) return;
    input.focus();
    // The expression is evaluated trimmed: count the blank space before it.
    const start = offset + (input.value.length - input.value.trimStart().length);
    input.setSelectionRange(start, Math.min(start + 1, input.value.length));
  };

  return (
    <div className={styles.filterBar} data-response-filter="">
      <div className={styles.filterRow}>
        <div className={styles.segmented} role="group" aria-label={t("response.filter.syntax")}>
          {FILTER_SYNTAXES.map((o) => (
            <button
              key={o.value}
              type="button"
              className={styles.segment}
              aria-pressed={filter.syntax === o.value}
              title={
                filter.syntax === o.value && filter.autoSyntax
                  ? t("response.filter.syntaxAuto", { syntax: o.label })
                  : t("response.filter.syntaxPick", { syntax: o.label })
              }
              onClick={() => filter.setSyntax(o.value)}
            >
              {o.label}
            </button>
          ))}
        </div>
        <label className={styles.filterField} data-invalid={error != null || undefined}>
          <IconFilter size={12} />
          <input
            ref={inputRef}
            className={styles.filterInput}
            type="text"
            role="searchbox"
            aria-label={t("response.filter.label", { syntax: filter.syntax === "jsonpath" ? "JSONPath" : "XPath" })}
            aria-invalid={error != null || undefined}
            placeholder={filterPlaceholder(filter.syntax)}
            title={t("response.filter.inputTitle")}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            list={filter.recent.length > 0 ? listId : undefined}
            value={filter.expression}
            onChange={(e) => filter.setExpression(e.target.value)}
            onKeyDown={onKeyDown}
            onBlur={filter.commit}
          />
          {filter.recent.length > 0 && (
            <datalist id={listId}>
              {filter.recent.map((r) => (
                <option key={r} value={r} />
              ))}
            </datalist>
          )}
          {filter.expression !== "" && (
            <button type="button" className={styles.filterClear} aria-label={t("response.filter.clear")} title={t("response.filter.clearTitle")} onClick={filter.clear}>
              <IconClose size={11} />
            </button>
          )}
        </label>
        {filter.active && <FilterCount filter={filter} />}
      </div>
      {filter.active && error != null && <FilterError error={error} expression={filter.evaluated} onShowPosition={showPosition} />}
      {filter.active && evaluation?.status === "failed" && (
        <p className={styles.filterError} role="alert">
          <IconWarning size={12} />
          {evaluation.message}
          {evaluation.hint !== "" && ` ${evaluation.hint}`}
        </p>
      )}
    </div>
  );
}

/** "3 matches", "No matches", "Number"; "Filtering…" while the result is not up to date. */
const RESULT_TYPES = {
  string: "response.filter.type.string",
  number: "response.filter.type.number",
  boolean: "response.filter.type.boolean",
} as const;

function FilterCount({ filter }: { filter: ResponseFilterState }) {
  const t = useT();
  const { evaluation } = filter;
  let text = "";
  if (evaluation?.status === "done" && evaluation.result.valid) {
    const r = evaluation.result;
    if (r.resultType !== "nodes") {
      const key = RESULT_TYPES[r.resultType as keyof typeof RESULT_TYPES];
      text = key !== undefined ? t(key) : r.resultType.charAt(0).toUpperCase() + r.resultType.slice(1);
    } else if (r.count === 0) text = t("response.filter.noMatches");
    else text = t("response.filter.matches", { count: r.count, n: formatNumber(r.count) });
  }
  if (filter.pending) text = t("response.filter.filtering");
  if (text === "") return null;
  return (
    <span className={styles.filterCount} role="status" aria-live="polite" data-pending={filter.pending || undefined}>
      {text}
    </span>
  );
}

/** A filter error: the message, and for a bad expression the expression with the position marked. */
function FilterError({ error, expression, onShowPosition }: { error: FilterErrorDTO; expression: string; onShowPosition: (offset: number) => void }) {
  const t = useT();
  const message = error.message.replace(/\.$/, "");
  let sentence;
  if (error.where === "expression" && error.offset >= 0) {
    const position = (
      <button type="button" className={styles.filterPosition} title={t("response.filter.positionTitle")} onClick={() => onShowPosition(error.offset)}>
        {t("response.filter.position", { n: error.offset + 1 })}
      </button>
    );
    sentence = <Trans k="response.filter.errorAt" values={{ message, position }} />;
  } else if (error.where === "body" && error.line > 0) {
    sentence = t("response.filter.errorAtLine", { message, line: error.line, column: error.column });
  } else {
    sentence = t("response.filter.error", { message });
  }
  return (
    <div className={styles.filterError} role="alert">
      <IconWarning size={12} />
      <span>
        {sentence}
        {error.where === "expression" && error.offset >= 0 && (
          <code className={styles.filterExcerpt} aria-hidden="true">
            {expression.slice(0, error.offset)}
            <mark className={styles.filterMark}>{error.offset < expression.length ? expression.charAt(error.offset) : " "}</mark>
            {expression.slice(error.offset + 1)}
          </code>
        )}
      </span>
    </div>
  );
}

const LANGUAGES: Record<string, EditorLanguage> = { json: "json", xml: "xml" };

export interface FilteredBodyProps {
  filter: ResponseFilterState;
}

/**
 * The filter's result in a read-only editor (search works as in the body):
 * the values (pretty JSON, indented XML, or a scalar), or their paths.
 * While the result is not up to date the previous one stays, dimmed.
 */
export const FilteredBody = forwardRef<CodeEditorHandle, FilteredBodyProps>(function FilteredBody({ filter }, ref) {
  const t = useT();
  const [showPaths, setShowPaths] = useState(false);
  const { evaluation } = filter;
  const result = evaluation?.status === "done" && evaluation.result.valid ? evaluation.result : null;

  if (result === null) {
    let text = "";
    if (filter.pending) text = t("response.filter.filtering");
    else if (evaluation?.status === "done") text = t("response.filter.fix");
    return (
      <div className={styles.bodyPane} data-filtered-body="">
        {text !== "" && <p className={styles.empty}>{text}</p>}
      </div>
    );
  }

  const notes: string[] = [];
  if (evaluation?.status === "done" && evaluation.shown) {
    notes.push(t("response.filter.shownText"));
  }
  if (result.clipped) notes.push(t("response.filter.clipped", { size: formatSize(result.text.length) }));
  const hasPaths = result.resultType === "nodes" && result.count > 0;
  const paths = showPaths && hasPaths;
  const pathList = result.paths ?? [];
  let text = paths ? pathList.join("\n") : result.text;
  if (paths && pathList.length < result.count) text += `\n${t("response.filter.morePaths", { n: result.count - pathList.length })}`;

  return (
    <div className={styles.bodyPane} data-filtered-body="" data-pending={filter.pending || undefined}>
      {(notes.length > 0 || hasPaths) && (
        <div className={styles.bodyNotes}>
          {notes.map((n) => (
            <p key={n} className={styles.bodyNote}>
              {n}
            </p>
          ))}
          {hasPaths && (
            <div className={styles.filterShow}>
              <span className={styles.bodyNote}>{t("response.filter.show")}</span>
              <div className={styles.segmented} role="group" aria-label={t("response.filter.result")}>
                <button type="button" className={styles.segment} aria-pressed={!paths} onClick={() => setShowPaths(false)}>
                  {t("response.filter.values")}
                </button>
                <button type="button" className={styles.segment} aria-pressed={paths} onClick={() => setShowPaths(true)}>
                  {t("response.filter.paths")}
                </button>
              </div>
            </div>
          )}
        </div>
      )}
      {result.resultType === "nodes" && result.count === 0 ? (
        <p className={styles.empty}>{t("response.filter.nothing")}</p>
      ) : (
        <CodeEditor
          ref={ref}
          className={styles.bodyEditor}
          value={text}
          readOnly
          language={paths ? "text" : (LANGUAGES[result.language] ?? "text")}
          ariaLabel={t("response.filter.bodyLabel")}
        />
      )}
    </div>
  );
});
