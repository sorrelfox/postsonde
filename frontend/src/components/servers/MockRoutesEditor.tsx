import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import type { EditorView } from "@codemirror/view";
import {
  editMockConfig,
  editMockRoute,
  MOCK_DEFAULT_RESPONSE,
  MOCK_MAX_DELAY_MS,
  MOCK_METHODS,
  MOCK_RULE_OPS,
  MOCK_RULE_SOURCES,
  MOCK_SELECT_HEADER,
  newMockResponse,
  newMockRoute,
} from "../../commands/mockRoutes";
import { useJsonCheck } from "../../commands/useJsonCheck";
import { formatNumber, t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { beautifyJson } from "../../lib/format";
import {
  selectMockConfig,
  useServers,
  useStores,
  type LocalServer,
  type MockConfig,
  type MockHeader,
  type MockResponse,
  type MockRoute,
  type MockRule,
  type RouteDiagnostic,
} from "../../stores";
import { pickOpenApiFile } from "../../lib/servers";
import {
  loadMockTemplateNames,
  mockTemplateSet,
  patternParams,
  type MockTemplateNameDTO,
} from "../../lib/mockTemplate";
import { IconChevron, IconClose, IconFileImport, IconPlus, IconWarning } from "../common/icons";
import { newKeyValueRowId, KeyValueTable, type KeyValueRow } from "../common/KeyValueTable";
import { onTabListKeyDown } from "../common/tabList";
import { TemplateVariablesContext } from "../common/TemplateVariables";
import { methodBadge } from "../tree/badges";
import { CodeEditor } from "../editor/CodeEditor";
import { installErrorMarker, setErrorMarker } from "../editor/errorMarker";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "../editor/headerSuggest";
import { installPathParamHighlight } from "../editor/pathParamHighlight";
import bodyStyles from "../editor/BodyEditor.module.css";
import { OpenApiRoutesDialog } from "./OpenApiRoutesDialog";
import { byteToIndex, installRangeMarker, setRangeMarker } from "./rangeMarker";
import styles from "./MockRoutes.module.css";

// base64: a binary body written in Base64 (mockhttp.BodyTypeBase64), sent decoded.
const RAW_TYPES: readonly string[] = ["json", "xml", "html", "text", "yaml", "base64"];

/** A raw body type's name in the menu: the format's own name, "Text" or "Base64 (binary)". */
function rawTypeLabel(type: string): string {
  if (type === "base64") return translate("servers.mock.bodyType.base64");
  return type === "text" ? translate("servers.mock.bodyType.text") : type.toUpperCase();
}

/** The implicit Content-Type of a raw body type: Go's mockhttp rule. */
const RAW_CONTENT_TYPES: Readonly<Record<string, string>> = {
  json: "application/json",
  xml: "application/xml",
  html: "text/html; charset=utf-8",
  text: "text/plain; charset=utf-8",
  yaml: "application/yaml",
  base64: "application/octet-stream",
};

// HTTP reason phrases are protocol text, not translated.
// i18n-ignore-start
const STATUS_TEXT: Readonly<Record<number, string>> = {
  200: "OK",
  201: "Created",
  202: "Accepted",
  204: "No Content",
  301: "Moved Permanently",
  302: "Found",
  304: "Not Modified",
  307: "Temporary Redirect",
  308: "Permanent Redirect",
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
  405: "Method Not Allowed",
  409: "Conflict",
  410: "Gone",
  415: "Unsupported Media Type",
  422: "Unprocessable Content",
  429: "Too Many Requests",
  500: "Internal Server Error",
  501: "Not Implemented",
  502: "Bad Gateway",
  503: "Service Unavailable",
  504: "Gateway Timeout",
};
// i18n-ignore-end

/** The method menu's value for "Custom…": never a method. */
const CUSTOM = "custom…"; // i18n-ignore: an option value, not shown

function statusTone(status: number): string {
  return status >= 500 ? "bad" : status >= 400 ? "warn" : status >= 300 ? "info" : "ok";
}

function contentTypeRow(headers: readonly MockHeader[]): boolean {
  return headers.some((h) => h.enabled && h.name.trim().toLowerCase() === "content-type");
}

/** Table rows for header rows, keeping row ids while the same array comes back. */
function useHeaderRows(
  headers: readonly MockHeader[],
  onChange: (next: MockHeader[]) => void,
): [readonly KeyValueRow[], (rows: KeyValueRow[]) => void] {
  const ref = useRef<{
    headers: readonly MockHeader[];
    rows: readonly KeyValueRow[];
  } | null>(null);
  if (ref.current === null || ref.current.headers !== headers) {
    const previous = ref.current?.rows ?? [];
    ref.current = {
      headers,
      rows: headers.map((h, i) => ({
        id: previous[i]?.id ?? newKeyValueRowId(),
        enabled: h.enabled,
        key: h.name,
        value: h.value,
        description: "",
      })),
    };
  }
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const handle = useCallback((rows: KeyValueRow[]) => {
    const next = rows.map((r) => ({
      enabled: r.enabled,
      name: r.key,
      value: r.value,
    }));
    ref.current = { headers: next, rows };
    onChangeRef.current(next);
  }, []);
  return [ref.current.rows, handle];
}

function Problems({ items, label }: { items: readonly RouteDiagnostic[]; label?: (d: RouteDiagnostic) => string }) {
  const t = useT();
  if (items.length === 0) return null;
  return (
    <ul className={styles.problems} aria-label={t("servers.mock.problems")}>
      {items.map((d, i) => (
        <li key={`${d.code}-${i}`} className={styles.problem}>
          <IconWarning size={12} />
          <span>
            {label?.(d)}
            {d.message}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** The status, headers and body of a response. */
interface ResponseContent {
  readonly status: number;
  readonly headers: readonly MockHeader[];
  readonly body: MockRoute["body"];
  readonly templated: boolean;
}

/** The names a templated response can use, loaded from Go once it is needed; null until then. */
function useMockTemplateNames(needed: boolean): readonly MockTemplateNameDTO[] | null {
  const [names, setNames] = useState<readonly MockTemplateNameDTO[] | null>(null);
  useEffect(() => {
    if (!needed || names !== null) return;
    let alive = true;
    loadMockTemplateNames().then(
      (loaded) => {
        if (alive) setNames(loaded);
      },
      () => {
        // Not bound (tests): references are highlighted, not checked.
      },
    );
    return () => {
      alive = false;
    };
  }, [needed, names]);
  return names;
}

function hasBraces(value: ResponseContent): boolean {
  return (
    (value.body.mode === "raw" && value.body.text.includes("{{")) ||
    value.headers.some((h) => h.enabled && h.value.includes("{{"))
  );
}

interface ResponseFieldsProps {
  value: ResponseContent;
  /** Unique per route and response: the editors' document key. */
  docKey: string;
  /** The route's path parameters, for `{{request.params.…}}` completion. */
  params: readonly string[];
  /** The problems of this response. */
  diagnostics: readonly RouteDiagnostic[];
  onChange: (patch: Partial<ResponseContent>) => void;
}

/** The status, headers and body of the default response or of another response. */
function ResponseFields({ value, docKey, params, diagnostics, onChange: set }: ResponseFieldsProps) {
  const t = useT();
  const baseId = useId();

  // A templated response (MOCK-06): its body and header values get chips,
  // checked against the request's values and the dynamic values, with
  // completion after `{{`.
  const names = useMockTemplateNames(value.templated);
  const paramsKey = params.join("/");
  const templateContext = useMemo(
    () => ({ variables: names === null ? null : mockTemplateSet(names, paramsKey === "" ? [] : paramsKey.split("/")) }),
    [names, paramsKey],
  );
  const templated = (children: ReactNode) =>
    value.templated ? (
      <TemplateVariablesContext.Provider value={templateContext}>{children}</TemplateVariablesContext.Provider>
    ) : (
      children
    );
  const of = (...fields: string[]) => diagnostics.filter((d) => fields.includes(d.field));

  // Body: JSON check marker and beautify, like the request body editor.
  const json = value.body.mode === "raw" && value.body.type === "json";
  const [bodyView, setBodyView] = useState<EditorView | null>(null);
  const onBodyReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setBodyView(v);
  }, []);
  const check = useJsonCheck(value.body.text, json);
  useEffect(() => {
    if (bodyView === null) return;
    const error = check.error !== null && check.text === bodyView.state.doc.toString() ? check.error : null;
    bodyView.dispatch({
      effects: setErrorMarker.of(error === null ? null : { offset: error.offset, message: error.message }),
    });
  }, [bodyView, check]);
  const [formatError, setFormatError] = useState<string | null>(null);
  const beautify = () => {
    const text = value.body.text;
    setFormatError(null);
    beautifyJson(text).then(
      (result) => {
        // Only when the text was not edited meanwhile.
        if (result.valid && bodyView?.state.doc.toString() === text)
          set({ body: { ...value.body, text: result.text } });
      },
      (err: unknown) => setFormatError(toAppError(err).message),
    );
  };

  const [headerRows, setHeaderRows] = useHeaderRows(value.headers, (headers) => set({ headers }));
  const implicitType =
    value.body.mode === "raw" && !contentTypeRow(value.headers) && value.status !== 204 && value.status !== 304
      ? (RAW_CONTENT_TYPES[value.body.type] ?? null)
      : null;
  const statusText = STATUS_TEXT[value.status] ?? "";

  return (
    <>
      <div className={styles.group}>
        <label className={styles.label} htmlFor={`${baseId}-status`}>
          {t("servers.mock.status")}
        </label>
        <div className={styles.inline}>
          <input
            id={`${baseId}-status`}
            className={`${styles.input} ${styles.number}`}
            inputMode="numeric"
            value={value.status === 0 ? "" : String(value.status)}
            aria-invalid={of("status").length > 0 || undefined}
            onChange={(e) => {
              if (/^\d{0,3}$/.test(e.target.value)) set({ status: num(e.target.value) });
            }}
          />
          <span className={styles.statusText} data-tone={statusTone(value.status)}>
            {statusText}
          </span>
        </div>
        <Problems items={of("status")} />
      </div>

      <div className={styles.group}>
        <span className={styles.label}>{t("servers.mock.headers")}</span>
        {templated(
          <KeyValueTable
            key={value.templated ? "templated" : "plain"}
            label={t("servers.mock.responseHeaders")}
            rows={headerRows}
            onChange={setHeaderRows}
            showDescription={false}
            placeholders={{ key: t("servers.mock.headerPlaceholder"), value: t("servers.mock.valuePlaceholder") }}
            suggest={HEADER_SUGGEST}
            suggestLabels={HEADER_SUGGEST_LABELS}
          />,
        )}
        {implicitType !== null && (
          <p className={styles.hint}>
            <Trans k="servers.mock.implicitType" values={{ header: <code>Content-Type: {implicitType}</code> }} />
          </p>
        )}
        <Problems items={of("header")} label={(d) => (d.headerIndex >= 0 ? t("servers.mock.row", { n: d.headerIndex + 1 }) : "")} />
      </div>

      <div className={styles.group}>
        <span className={styles.label}>{t("servers.mock.body")}</span>
        <div className={bodyStyles.toolbar}>
          <div className={bodyStyles.modes} role="radiogroup" aria-label={t("servers.mock.bodyType")}>
            {[
              { mode: "none", label: t("servers.mock.bodyMode.none") },
              { mode: "raw", label: t("servers.mock.bodyMode.raw") },
            ].map((m) => (
              <label key={m.mode} className={bodyStyles.mode}>
                <input
                  type="radio"
                  name={`${baseId}-body`}
                  value={m.mode}
                  checked={value.body.mode === m.mode}
                  onChange={() =>
                    set({
                      body: {
                        ...value.body,
                        mode: m.mode,
                        type: value.body.type || "json",
                      },
                    })
                  }
                />
                {m.label}
              </label>
            ))}
          </div>
          {value.body.mode === "raw" && (
            <select
              className={bodyStyles.type}
              aria-label={t("servers.mock.bodyLanguage")}
              value={value.body.type}
              onChange={(e) => set({ body: { ...value.body, type: e.target.value } })}
            >
              {RAW_TYPES.map((type) => (
                <option key={type} value={type}>
                  {rawTypeLabel(type)}
                </option>
              ))}
            </select>
          )}
          <label
            className={styles.toggle}
            title={t("servers.mock.templatedTitle")}
          >
            <input type="checkbox" checked={value.templated} onChange={(e) => set({ templated: e.target.checked })} />
            {t("servers.mock.templated")}
          </label>
          {json && (
            <div className={bodyStyles.tools}>
              <button
                type="button"
                className={bodyStyles.tool}
                disabled={value.body.text.trim() === ""}
                onClick={beautify}
              >
                {t("servers.mock.beautify")}
              </button>
            </div>
          )}
        </div>
        {value.body.mode === "raw" && (
          <div className={`${bodyStyles.editorFrame} ${styles.bodyFrame}`}>
            {templated(
              <CodeEditor
                key={value.templated ? "templated" : "plain"}
                docKey={value.templated ? `${docKey}:templated` : docKey}
                value={value.body.text}
                language={json ? "json" : "text"}
                ariaLabel={t("servers.mock.responseBody")}
                onChange={(text) => set({ body: { ...value.body, text } })}
                onReady={onBodyReady}
              />,
            )}
          </div>
        )}
        {value.body.mode === "raw" && value.body.type === "base64" && <p className={styles.hint}>{t("servers.mock.base64Hint")}</p>}
        {value.templated ? (
          <p className={styles.hint}>
            <Trans
              k="servers.mock.templatedHint"
              values={{
                params: <code>{"{{request.params.id}}"}</code>,
                query: <code>{"{{request.query.page}}"}</code>,
                headers: <code>{"{{request.headers.X-Name}}"}</code>,
                body: <code>{"{{request.body.user.name}}"}</code>,
                method: <code>{"{{request.method}}"}</code>,
                uuid: <code>{"{{$uuid}}"}</code>,
                escape: <code>{"\\{{"}</code>,
              }}
            />
          </p>
        ) : (
          hasBraces(value) && (
            <p className={styles.hint}>
              <Trans k="servers.mock.notTemplatedHint" values={{ braces: <code>{"{{…}}"}</code> }} />
            </p>
          )
        )}
        {json && check.error !== null && (
          <p className={bodyStyles.status} role="status">
            <IconWarning size={12} />
            <span>
              {t("servers.mock.jsonError", { line: check.error.line, column: check.error.column, message: check.error.message })}
            </span>
          </p>
        )}
        {formatError !== null && <p className={styles.error}>{formatError}</p>}
        <Problems items={of("body")} />
      </div>
    </>
  );
}

function num(text: string): number {
  return text.trim() === "" ? 0 : Math.trunc(Number(text));
}

interface RulesEditorProps {
  rules: readonly MockRule[];
  diagnostics: readonly RouteDiagnostic[];
  onChange: (rules: MockRule[]) => void;
}

/** The rules of a response: all must match (MOCK-05). */
function RulesEditor({ rules, diagnostics, onChange }: RulesEditorProps) {
  const t = useT();
  const setRule = (i: number, patch: Partial<MockRule>) =>
    onChange(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className={styles.group}>
      <span className={styles.label}>{t("servers.mock.when")}</span>
      {rules.length === 0 ? (
        <p className={styles.hint}>
          <Trans k="servers.mock.noRules" values={{ header: <code>{MOCK_SELECT_HEADER}</code> }} />
        </p>
      ) : (
        <ul className={styles.rules} aria-label={t("servers.mock.rules")}>
          {rules.map((rule, i) => {
            const source = MOCK_RULE_SOURCES.find((s) => s.source === rule.source);
            const op = MOCK_RULE_OPS.find((o) => o.op === rule.op);
            const problems = diagnostics.filter((d) => d.field === "rule" && d.ruleIndex === i);
            return (
              <li key={i} className={styles.ruleItem}>
                <div className={styles.rule} data-invalid={problems.length > 0 || undefined}>
                  {i > 0 && <span className={styles.ruleAnd}>{t("servers.mock.and")}</span>}
                  <select
                    className={styles.select}
                    aria-label={t("servers.mock.rule.source", { n: i + 1 })}
                    value={rule.source}
                    onChange={(e) => setRule(i, { source: e.target.value })}
                  >
                    {source === undefined && <option value={rule.source}>{rule.source}</option>}
                    {MOCK_RULE_SOURCES.map((s) => (
                      <option key={s.source} value={s.source}>
                        {s.label}
                      </option>
                    ))}
                  </select>
                  {rule.source !== "body" && (
                    <input
                      className={`${styles.input} ${styles.ruleName}`}
                      aria-label={t("servers.mock.rule.name", { n: i + 1 })}
                      placeholder={source?.placeholder ?? ""}
                      spellCheck={false}
                      autoComplete="off"
                      value={rule.name}
                      onChange={(e) => setRule(i, { name: e.target.value })}
                    />
                  )}
                  <select
                    className={styles.select}
                    aria-label={t("servers.mock.rule.operator", { n: i + 1 })}
                    value={rule.op}
                    onChange={(e) => setRule(i, { op: e.target.value })}
                  >
                    {op === undefined && <option value={rule.op}>{rule.op}</option>}
                    {MOCK_RULE_OPS.map((o) => (
                      <option key={o.op} value={o.op}>
                        {o.label}
                      </option>
                    ))}
                  </select>
                  {(op?.needsValue ?? true) && (
                    <input
                      className={`${styles.input} ${styles.ruleValue}`}
                      aria-label={t("servers.mock.rule.value", { n: i + 1 })}
                      placeholder={rule.op === "matches" ? t("servers.mock.rule.regexPlaceholder") : t("servers.mock.rule.valuePlaceholder")}
                      spellCheck={false}
                      autoComplete="off"
                      value={rule.value}
                      onChange={(e) => setRule(i, { value: e.target.value })}
                    />
                  )}
                  <button
                    type="button"
                    className={styles.iconButton}
                    aria-label={t("servers.mock.rule.remove", { n: i + 1 })}
                    title={t("servers.mock.rule.removeTitle")}
                    onClick={() => onChange(rules.filter((_, j) => j !== i))}
                  >
                    <IconClose size={11} />
                  </button>
                </div>
                <Problems items={problems} />
              </li>
            );
          })}
        </ul>
      )}
      <div className={styles.inline}>
        <button
          type="button"
          className={styles.button}
          onClick={() => onChange([...rules, { source: "query", name: "", op: "equals", value: "" }])}
        >
          <IconPlus size={11} /> {t("servers.mock.addRule")}
        </button>
      </div>
    </div>
  );
}

function responseLabel(r: MockResponse, i: number): string {
  return r.name.trim() === "" ? translate("servers.mock.responseN", { n: i + 1 }) : r.name;
}

interface RouteDetailProps {
  route: MockRoute;
  index: number;
  diagnostics: readonly RouteDiagnostic[];
  onChange: (route: MockRoute) => void;
  onDelete: () => void;
}

/**
 * One route: method, path, its responses (the default one and the others,
 * MOCK-05 and MOCK-07) and the delay.
 */
function RouteDetail({ route, index, diagnostics, onChange, onDelete }: RouteDetailProps) {
  const t = useT();
  const baseId = useId();
  const [customMethod, setCustomMethod] = useState(!MOCK_METHODS.includes(route.method));
  // The response shown: -1 is the default one, else an index in route.responses.
  const [shown, setShown] = useState(-1);
  const set = (patch: Partial<MockRoute>) => onChange({ ...route, ...patch });
  const routeLevel = diagnostics.filter((d) => d.responseIndex < 0);
  const of = (...fields: string[]) => routeLevel.filter((d) => fields.includes(d.field));

  // Path: parameter highlight and the diagnostic's range.
  const [pathView, setPathView] = useState<EditorView | null>(null);
  const onPathReady = useCallback((v: EditorView) => {
    installPathParamHighlight(v);
    installRangeMarker(v);
    setPathView(v);
  }, []);
  const pathProblem = diagnostics.find((d) => d.field === "pattern") ?? null;
  useEffect(() => {
    if (pathView === null) return;
    const text = pathView.state.doc.toString();
    const marker =
      pathProblem === null || text !== route.path
        ? null
        : {
            from: byteToIndex(text, pathProblem.start),
            to: byteToIndex(text, pathProblem.end),
            message: pathProblem.message,
          };
    pathView.dispatch({ effects: setRangeMarker.of(marker) });
  }, [pathView, pathProblem, route.path]);

  const methodOptions = MOCK_METHODS.includes(route.method) ? MOCK_METHODS : [...MOCK_METHODS, route.method];

  const responses = route.responses;
  const current = shown >= 0 && shown < responses.length ? shown : -1;
  const alt = current >= 0 ? responses[current] : null;
  const setResponses = (next: MockResponse[]) => set({ responses: next });
  const setAlt = (patch: Partial<MockResponse>) =>
    setResponses(responses.map((r, j) => (j === current ? { ...r, ...patch } : r)));
  const move = (by: -1 | 1) => {
    const to = current + by;
    if (current < 0 || to < 0 || to >= responses.length) return;
    const next = [...responses];
    [next[current], next[to]] = [next[to], next[current]];
    setResponses(next);
    setShown(to);
  };
  const problemsOf = (i: number) => diagnostics.filter((d) => d.responseIndex === i);
  const altProblems = alt === null ? [] : problemsOf(current);

  return (
    <section className={styles.detail} aria-label={t("servers.mock.route", { n: index + 1 })}>
      <div className={styles.requestLine}>
        <div className={styles.pathGroup} data-invalid={of("pattern", "method").length > 0 || undefined}>
          {customMethod ? (
            <input
              className={`${styles.input} ${styles.methodInput}`}
              aria-label={t("servers.mock.customMethod")}
              value={route.method}
              spellCheck={false}
              autoComplete="off"
              placeholder="METHOD" // i18n-ignore: stands for an HTTP method token
              onChange={(e) => set({ method: e.target.value })}
              onBlur={() => {
                if (MOCK_METHODS.includes(route.method)) setCustomMethod(false);
              }}
            />
          ) : (
            <select
              className={styles.methodSelect}
              aria-label={t("servers.mock.method")}
              data-tone={route.method === "ANY" ? "dir" : methodBadge(route.method).tone}
              value={route.method}
              onChange={(e) => {
                if (e.target.value === CUSTOM) setCustomMethod(true);
                else set({ method: e.target.value });
              }}
            >
              {methodOptions.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
              <option value={CUSTOM}>{t("servers.mock.custom")}</option>
            </select>
          )}
          <CodeEditor
            className={styles.path}
            singleLine
            ariaLabel={t("servers.mock.path")}
            placeholder="/users/:id"
            docKey={route.id}
            value={route.path}
            onChange={(path) => set({ path })}
            onReady={onPathReady}
          />
        </div>
        <label className={styles.toggle}>
          <input type="checkbox" checked={route.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          {t("servers.mock.enabled")}
        </label>
        <button type="button" className={`${styles.button} ${styles.danger}`} onClick={onDelete}>
          {t("servers.mock.delete")}
        </button>
      </div>
      <Problems items={of("pattern", "method", "id")} />
      <p className={styles.hint}>
        <Trans
          k="servers.mock.pathHint"
          values={{
            colon: <code>:name</code>,
            braces: <code>{"{name}"}</code>,
            pattern: <code>{"{id:[0-9]+}"}</code>,
            star: <code>*</code>,
            stars: <code>**</code>,
            rest: <code>*name</code>,
          }}
        />
      </p>

      <div className={styles.group}>
        <span className={styles.label}>{t("servers.mock.responses")}</span>
        <div className={styles.responseTabs}>
          <div className={styles.responseTabList} role="tablist" aria-label={t("servers.mock.responses")} onKeyDown={onTabListKeyDown}>
            {[-1, ...responses.map((_, i) => i)].map((i) => {
              const r = i < 0 ? null : responses[i];
              const status = r === null ? route.status : r.status;
              const bad = (i < 0 ? routeLevel.filter((d) => ["status", "header", "body"].includes(d.field)) : problemsOf(i))
                .length;
              return (
                <button
                  key={i}
                  type="button"
                  role="tab"
                  className={styles.responseTab}
                  aria-selected={i === current}
                  tabIndex={i === current ? 0 : -1}
                  onClick={() => setShown(i)}
                >
                  <span>{r === null ? t("servers.mock.default") : responseLabel(r, i)}</span>
                  <span className={styles.rowStatus} data-tone={statusTone(status)}>
                    {status}
                  </span>
                  {bad > 0 && (
                    <span className={styles.rowWarn} role="img" aria-label={t("servers.mock.hasProblems")}>
                      <IconWarning size={11} />
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          <button
            type="button"
            className={styles.button}
            onClick={() => {
              setResponses([...responses, newMockResponse(route)]);
              setShown(responses.length);
            }}
          >
            <IconPlus size={11} /> {t("servers.mock.addResponse")}
          </button>
        </div>
        <p className={styles.hint}>
          <Trans
            k={responses.length > 0 ? "servers.mock.responsesHintRules" : "servers.mock.responsesHint"}
            values={{
              header: <code>{MOCK_SELECT_HEADER}: name</code>,
              status: <code>500</code>,
              name: <code>{MOCK_DEFAULT_RESPONSE}</code>,
            }}
          />
        </p>
      </div>

      {alt !== null && (
        <>
          <div className={styles.group}>
            <label className={styles.label} htmlFor={`${baseId}-name`}>
              {t("servers.mock.name")}
            </label>
            <div className={styles.inline}>
              <input
                id={`${baseId}-name`}
                className={`${styles.input} ${styles.ruleName}`}
                spellCheck={false}
                autoComplete="off"
                placeholder="not-found" // i18n-ignore: an example response name
                value={alt.name}
                aria-invalid={altProblems.some((d) => d.field === "name") || undefined}
                onChange={(e) => setAlt({ name: e.target.value })}
              />
              <button
                type="button"
                className={styles.iconButton}
                aria-label={t("servers.mock.moveEarlier")}
                title={t("servers.mock.moveEarlierTitle")}
                disabled={current === 0}
                onClick={() => move(-1)}
              >
                <IconChevron direction="left" size={12} />
              </button>
              <button
                type="button"
                className={styles.iconButton}
                aria-label={t("servers.mock.moveLater")}
                title={t("servers.mock.moveLaterTitle")}
                disabled={current === responses.length - 1}
                onClick={() => move(1)}
              >
                <IconChevron direction="right" size={12} />
              </button>
              <span className={styles.spacer} />
              <button
                type="button"
                className={`${styles.button} ${styles.danger}`}
                onClick={() => {
                  setResponses(responses.filter((_, j) => j !== current));
                  setShown(current - 1);
                }}
              >
                {t("servers.mock.deleteResponse")}
              </button>
            </div>
            <Problems items={altProblems.filter((d) => d.field === "name")} />
          </div>
          <RulesEditor rules={alt.rules} diagnostics={altProblems} onChange={(rules) => setAlt({ rules })} />
        </>
      )}

      <ResponseFields
        key={current}
        value={alt ?? route}
        docKey={`${route.id}:${current}`}
        params={patternParams(route.path)}
        diagnostics={alt === null ? routeLevel : altProblems}
        onChange={(patch) => (alt === null ? set(patch) : setAlt(patch))}
      />

      <div className={styles.group}>
        <span className={styles.label}>{t("servers.mock.delay")}</span>
        <div className={styles.inline}>
          <div className={bodyStyles.modes} role="radiogroup" aria-label={t("servers.mock.delay")}>
            {[
              { mode: "none", label: t("servers.mock.delayMode.none") },
              { mode: "fixed", label: t("servers.mock.delayMode.fixed") },
              { mode: "random", label: t("servers.mock.delayMode.random") },
            ].map((m) => (
              <label key={m.mode} className={bodyStyles.mode}>
                <input
                  type="radio"
                  name={`${baseId}-delay`}
                  value={m.mode}
                  checked={route.delay.mode === m.mode}
                  onChange={() => set({ delay: { ...route.delay, mode: m.mode } })}
                />
                {m.label}
              </label>
            ))}
          </div>
          {route.delay.mode === "fixed" && (
            <label className={styles.inline}>
              <input
                className={`${styles.input} ${styles.number}`}
                inputMode="numeric"
                aria-label={t("servers.mock.delayMs")}
                value={String(route.delay.ms)}
                onChange={(e) => {
                  if (/^\d{0,6}$/.test(e.target.value)) set({ delay: { ...route.delay, ms: num(e.target.value) } });
                }}
              />
              <span className={styles.unit}>{t("servers.unit.ms")}</span>
            </label>
          )}
          {route.delay.mode === "random" && (
            <span className={styles.inline}>
              <input
                className={`${styles.input} ${styles.number}`}
                inputMode="numeric"
                aria-label={t("servers.mock.delayMin")}
                value={String(route.delay.minMs)}
                onChange={(e) => {
                  if (/^\d{0,6}$/.test(e.target.value))
                    set({
                      delay: { ...route.delay, minMs: num(e.target.value) },
                    });
                }}
              />
              <span className={styles.unit}>{t("servers.mock.delayTo")}</span>
              <input
                className={`${styles.input} ${styles.number}`}
                inputMode="numeric"
                aria-label={t("servers.mock.delayMax")}
                value={String(route.delay.maxMs)}
                onChange={(e) => {
                  if (/^\d{0,6}$/.test(e.target.value))
                    set({
                      delay: { ...route.delay, maxMs: num(e.target.value) },
                    });
                }}
              />
              <span className={styles.unit}>{t("servers.unit.ms")}</span>
            </span>
          )}
        </div>
        {route.delay.mode !== "none" && (
          <p className={styles.hint}>{t("servers.mock.delayHint", { count: MOCK_MAX_DELAY_MS / 60_000 })}</p>
        )}
        <Problems items={of("delay")} />
      </div>
    </section>
  );
}

function routeLabel(r: MockRoute): string {
  return `${r.method || "ANY"} ${r.path}`;
}

/**
 * The Routes editor of a mock server (MOCK-01, MOCK-02, MOCK-04 to
 * MOCK-07): the route list (method, path, status, enabled, a warning when a
 * route has a problem) and the selected route's method, path, responses
 * (the default one's and the others' status, headers and body, the others'
 * names and rules) and delay. Every edit applies to the running server once typing pauses
 * (commands/mockRoutes.ts); problems come back from Go per route.
 */
export function MockRoutesEditor({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const config = useServers((s) => selectMockConfig(s, server.id));
  const error = useServers((s) => s.mockErrors[server.id] ?? null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // Generate routes from OpenAPI (MOCK-09): the chosen file while its preview is open.
  const [openApiPath, setOpenApiPath] = useState<string | null>(null);
  const [pickError, setPickError] = useState<string | null>(null);

  if (config === null) return null;
  const routes = config.routes;
  const selectedIndex = Math.max(
    0,
    routes.findIndex((r) => r.id === selectedId),
  );
  const selected = routes[selectedIndex] ?? null;
  const diagnosticsOf = (id: string) => server.diagnostics.filter((d) => d.routeId === id);

  const edit = (next: MockConfig) => editMockConfig(stores, server.id, next);
  const fromOpenApi = async () => {
    setPickError(null);
    try {
      const path = await pickOpenApiFile();
      if (path !== "") setOpenApiPath(path);
    } catch (err: unknown) {
      const e = toAppError(err);
      setPickError(e.hint ? `${e.message} ${e.hint}` : e.message);
    }
  };
  const add = () => {
    const route = newMockRoute(routes);
    edit({ ...config, routes: [...routes, route] });
    setSelectedId(route.id);
  };
  const remove = (id: string) => {
    const i = routes.findIndex((r) => r.id === id);
    const next = routes.filter((r) => r.id !== id);
    edit({ ...config, routes: next });
    setSelectedId(next[Math.min(i, next.length - 1)]?.id ?? null);
  };

  const option = (key: "strictTrailingSlash" | "caseInsensitive", labelKey: MessageKey, titleKey: MessageKey) => (
    <label className={styles.toggle} title={t(titleKey)}>
      <input type="checkbox" checked={config[key]} onChange={(e) => edit({ ...config, [key]: e.target.checked })} />
      {t(labelKey)}
    </label>
  );

  return (
    <div className={styles.editor} data-testid="mock-routes">
      <div className={styles.toolbar}>
        <button type="button" className={`${styles.button} ${styles.primary}`} onClick={add}>
          <IconPlus size={11} /> {t("servers.mock.addRoute")}
        </button>
        <button
          type="button"
          className={styles.button}
          title={t("servers.mock.fromOpenApiTitle")}
          onClick={() => void fromOpenApi()}
        >
          <IconFileImport size={12} /> {t("servers.mock.fromOpenApi")}
        </button>
        <span className={styles.spacer} />
        {option("strictTrailingSlash", "servers.mock.strictSlash", "servers.mock.strictSlashTitle")}
        {option("caseInsensitive", "servers.mock.ignoreCase", "servers.mock.ignoreCaseTitle")}
      </div>
      <p className={styles.note}>{t("servers.mock.specificity")}</p>
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      {pickError !== null && (
        <p className={styles.error} role="alert">
          {pickError}
        </p>
      )}
      <OpenApiRoutesDialog
        path={openApiPath}
        config={config}
        onClose={() => setOpenApiPath(null)}
        onApply={(next, firstAdded) => {
          edit(next);
          setOpenApiPath(null);
          if (firstAdded !== null) setSelectedId(firstAdded);
        }}
      />

      {routes.length === 0 ? (
        <p className={styles.note}>{t("servers.mock.noRoutes")}</p>
      ) : (
        <ul className={styles.list} aria-label={t("servers.tab.routes")}>
          {routes.map((r) => {
            const problems = diagnosticsOf(r.id);
            const isSelected = r === selected;
            return (
              <li
                key={r.id}
                className={styles.row}
                data-selected={isSelected || undefined}
                data-disabled={!r.enabled || undefined}
              >
                <input
                  type="checkbox"
                  aria-label={t("servers.mock.enable", { route: routeLabel(r) })}
                  checked={r.enabled}
                  onChange={(e) =>
                    editMockRoute(stores, server.id, {
                      ...r,
                      enabled: e.target.checked,
                    })
                  }
                />
                <button
                  type="button"
                  className={styles.rowMain}
                  aria-current={isSelected || undefined}
                  onClick={() => setSelectedId(r.id)}
                >
                  <span className={styles.badge} data-tone={r.method === "ANY" ? "dir" : methodBadge(r.method).tone}>
                    {r.method === "ANY" ? "ANY" : methodBadge(r.method).label}
                  </span>
                  <span className={styles.rowPath}>{r.path || t("servers.mock.noPath")}</span>
                  {problems.length > 0 && (
                    <span
                      className={styles.rowWarn}
                      role="img"
                      aria-label={problems[0].message}
                      title={problems[0].message}
                    >
                      <IconWarning size={12} />
                    </span>
                  )}
                  <span className={styles.rowStatus} data-tone={statusTone(r.status)}>
                    {r.status}
                  </span>
                  {r.responses.length > 0 && (
                    <span className={styles.rowDelay} title={t("servers.mock.moreResponses")}>
                      {t("servers.mock.moreCount", { n: formatNumber(r.responses.length) })}
                    </span>
                  )}
                  {r.delay.mode !== "none" && <span className={styles.rowDelay}>{t("servers.mock.delayTag")}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      )}

      {selected !== null && (
        <RouteDetail
          key={selected.id}
          route={selected}
          index={selectedIndex}
          diagnostics={diagnosticsOf(selected.id)}
          onChange={(route) => editMockRoute(stores, server.id, route)}
          onDelete={() => remove(selected.id)}
        />
      )}
    </div>
  );
}
