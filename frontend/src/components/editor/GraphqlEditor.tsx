import { useCallback, useEffect, useId, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import type { EditorView } from "@codemirror/view";
import { editRequest } from "../../commands/autosave";
import { formatGraphqlQuery, type GraphqlFormat } from "../../commands/graphql";
import { activeUploads, PREFLIGHT_HEADER, sendsPreflight } from "../../commands/graphqlFiles";
import { useQueryJump } from "../../commands/graphqlJump";
import { startSubscription, stopSubscription } from "../../commands/graphqlSubscriptions";
import { cancelRequest, sendRequest } from "../../commands/httpSends";
import { useAutoSave } from "../../commands/useAutoSave";
import { useGraphqlDiagnostics, type GraphqlDiagnostics } from "../../commands/useGraphqlDiagnostics";
import { useGraphqlSchema } from "../../commands/useGraphqlSchema";
import { useGraphqlSchemaRefresh } from "../../commands/useGraphqlSchemaRefresh";
import { useGraphqlSubscription } from "../../commands/useGraphqlSubscription";
import { useHttpSend } from "../../commands/useHttpSend";
import { useGraphqlVariablesCheck } from "../../commands/useGraphqlVariablesCheck";
import { useJsonCheck } from "../../commands/useJsonCheck";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { countAssertions } from "../../lib/assertions";
import { countExtractions } from "../../lib/extractions";
import type { KeyValueDTO, RequestDTO, RequestSettingsDTO } from "../../lib/collection";
import type { GraphQLVariableDTO } from "../../lib/format";
import { isCancelled, toAppError } from "../../lib/errors";
import { docsPageFor, type DocsPage } from "../../lib/graphqlDocs";
import { GRAPHQL_INCREMENTAL_ACCEPT, usesIncrementalDelivery } from "../../lib/graphqlIncremental";
import { schemaRefreshOf } from "../../lib/graphqlSchemaRefresh";
import {
  DEFAULT_GRAPHQL_DEPTH,
  GRAPHQL_DEPTHS,
  graphqlDocsTarget,
  graphqlFillFields,
  graphqlGenerate,
  pickGraphqlSchemaFile,
  type GenerateRequest,
  type OperationDTO,
} from "../../lib/graphqlSchema";
import {
  defaultTransport,
  GRAPHQL_OPERATION_KINDS,
  runningOperationType,
  runsOverSocket,
  TRANSPORT_LABELS,
  transportChoices,
  transportFor,
  transportPatch,
  type GraphqlOperationKind,
  type GraphqlTransport,
} from "../../lib/graphqlSubscription";
import { ariaShortcut, matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import { useDrafts, useEnvironments, useStores, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconChevron, IconInfo, IconLock, IconPlay, IconStop, IconWarning } from "../common/icons";
import { KeyValueTable } from "../common/KeyValueTable";
import { useShortcut } from "../shell/useShortcut";
import { AssertionsEditor } from "./AssertionsEditor";
import { ExtractionsEditor } from "./ExtractionsEditor";
import { AuthEditor } from "./AuthEditor";
import bodyStyles from "./BodyEditor.module.css";
import { CodeEditor, type CodeEditorHandle } from "./CodeEditor";
import { CopyCurlButton } from "./CopyCurlButton";
import { installErrorMarker, markerRange, setErrorMarker, type ErrorMarker } from "./errorMarker";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "./headerSuggest";
import gqlStyles from "./GraphqlEditor.module.css";
import { GraphqlDocs } from "./GraphqlDocs";
import { GraphqlFiles } from "./GraphqlFiles";
import { GraphqlProblemsBar, useDiagnosticMarks } from "./GraphqlProblemsBar";
import { GraphqlSchemaRefreshSettings } from "./GraphqlSchemaRefreshSettings";
import { GraphqlSchemaStrip } from "./GraphqlSchemaStrip";
import { FileAccessNotice, useFileAccessCheck } from "../common/FileAccessNotice";
import { installDiagnostics, setDiagnostics } from "./diagnostics";
import { completionSchema, installGraphqlCompletion, setCompletionSchema } from "./graphqlCompletion";
import { installGraphqlDocsLink, setDocsLinkHandler } from "./graphqlDocsLink";
import implicitStyles from "./ImplicitHeaders.module.css";
import settingsStyles from "./RequestSettings.module.css";
import { InheritedHeaders } from "./InheritedRows";
import { useKeyValueRows } from "./keyValueRows";
import { NotesPanel } from "./NotesPanel";
import { SnippetPanel } from "./SnippetPanel";
import { useReadOnlyNotice } from "./RequestEditor";
import styles from "./RequestEditor.module.css";
import { RelativeUrlHint } from "./RelativeUrlHint";
import { RequestSettings, TlsOffBadge } from "./RequestSettings";
import { ResponseExamples } from "./ResponseExamples";

// The GraphQL request editor (GQL-01, GQL-02). It follows the HTTP request
// editor: a URL bar with the mode (POST or GET) and Send, then the Query /
// Variables / Extensions / Files / Connection / Headers / Auth / Tests /
// Settings sub-tabs, every change auto-saved (Extensions: the request's
// extensions object, GQL-06; Files: uploads with the GraphQL multipart
// request spec, GQL-07, GraphqlFiles; Tests: the assertions, TEST-01,
// AssertionsEditor).
// Sending goes through the HTTP send slots (commands/httpSends.ts): Go
// builds the GraphQL-over-HTTP request (request.BuildGraphQL), and the
// response shows in the HTTP response pane. When the operation that runs
// is a subscription, Send becomes Subscribe / Stop: it runs over
// WebSocket (or SSE) with the Connection tab's params (GQL-33) through the
// subscription slots (commands/graphqlSubscriptions.ts), and the pane
// shows its live event log (GQL-34). The Connection tab also picks the
// transport of each operation type (GQL-35): a query or mutation over
// WebSocket runs through the slot too and the pane shows its result. See
// docs/graphql.md and docs/graphql-subscriptions.md.

export type GraphqlSubTab =
  | "query"
  | "variables"
  | "extensions"
  | "files"
  | "connection"
  | "headers"
  | "auth"
  | "tests"
  | "extract"
  | "examples"
  | "settings"
  | "notes"
  | "code";

const SUB_TABS: readonly { id: GraphqlSubTab; labelKey: MessageKey }[] = [
  { id: "query", labelKey: "editorProtocols.tab.query" },
  { id: "variables", labelKey: "editorProtocols.tab.variables" },
  { id: "extensions", labelKey: "editorProtocols.tab.extensions" },
  { id: "files", labelKey: "editorProtocols.tab.files" },
  { id: "connection", labelKey: "editorProtocols.tab.connection" },
  { id: "headers", labelKey: "editorProtocols.tab.headers" },
  { id: "auth", labelKey: "editorProtocols.tab.auth" },
  { id: "tests", labelKey: "editorProtocols.tab.tests" },
  // Extraction into variables (TEST-05, ExtractionsEditor).
  { id: "extract", labelKey: "editorProtocols.tab.extract" },
  { id: "examples", labelKey: "editorProtocols.tab.examples" },
  { id: "settings", labelKey: "editorProtocols.tab.settings" },
  { id: "notes", labelKey: "editorProtocols.tab.notes" },
  // The request as code (GEN-12, SnippetPanel).
  { id: "code", labelKey: "editorProtocols.tab.code" },
];

function isSubTab(value: string | undefined): value is GraphqlSubTab {
  return SUB_TABS.some((t) => t.id === value);
}

/** The Accept header Go sends unless a row sets one: request.GraphQLAccept. */
export const GRAPHQL_ACCEPT = "application/graphql-response+json, application/json;q=0.9";

/** The file's method for a mode: "" is POST with a JSON body (the default), "GET" the GET mode. */
export function graphqlMode(method: string): "POST" | "GET" {
  return method === "GET" ? "GET" : "POST";
}

/**
 * The operation picker's state for a document's operations (GQL-02): with
 * one operation or none, Go sends that one and there is nothing to pick;
 * with several, the named ones are offered and `missing` says the saved
 * name is not one of them.
 */
export function operationChoice(
  operations: readonly OperationDTO[],
  saved: string,
): { pick: false } | { pick: true; names: string[]; missing: boolean } {
  if (operations.length <= 1) return { pick: false };
  const names = operations.map((op) => op.name).filter((name) => name !== "");
  return { pick: true, names, missing: !names.includes(saved) };
}

// Whether the docs panel (GQL-14) is open, and the selection depth of the
// schema actions (GQL-16, GQL-17): one choice for every GraphQL editor
// while the app runs, so they stay across tabs.
let docsPanelOpen = false;
let selectionDepth = DEFAULT_GRAPHQL_DEPTH;

/** Closes the docs panel and resets the depth for the next editor (tests). */
export function resetGraphqlDocsPanel(): void {
  docsPanelOpen = false;
  selectionDepth = DEFAULT_GRAPHQL_DEPTH;
}

/** A message of a schema action, shown under the query while the query is still `query`. */
interface QueryNotice {
  readonly message: string;
  readonly query: string;
}

/** Rows that count in a sub-tab's badge: enabled, with a name. */
function countRows(rows: readonly KeyValueDTO[] | null): number {
  return (rows ?? []).filter((r) => r.enabled && r.name !== "").length;
}

function hasRow(rows: readonly KeyValueDTO[] | null, name: string): boolean {
  return (rows ?? []).some((r) => r.enabled && r.name.toLowerCase() === name.toLowerCase());
}

/** Shows a marker from a check when it is for the editor's current text. */
function useMarker(view: EditorView | null, text: string | null, error: { offset: number; message: string } | null): void {
  useEffect(() => {
    if (view === null || text === null) return;
    const marker: ErrorMarker | null = error !== null && text === view.state.doc.toString() ? { offset: error.offset, message: error.message } : null;
    view.dispatch({ effects: setErrorMarker.of(marker) });
  }, [view, text, error]);
}

export interface GraphqlEditorProps {
  id: RequestId;
}

/**
 * The editor of a GraphQL request (GQL-01). Mount it with `key={id}` so
 * each request gets fresh local state.
 */
export function GraphqlEditor({ id }: GraphqlEditorProps) {
  const t = useT();
  const draft = useRequestDraft(id);

  if (draft.status === "loading") {
    return <div className={styles.editor} aria-busy="true" />;
  }
  if (draft.status === "error") {
    return (
      <div className={styles.editor}>
        <EmptyState icon={<IconWarning size={20} />} title={t("editorProtocols.openFailed")}>
          {draft.error.message}{" "}
          <button type="button" className={styles.linkButton} onClick={draft.retry}>
            {t("editorProtocols.tryAgain")}
          </button>
        </EmptyState>
      </div>
    );
  }
  return <LoadedEditor id={id} request={draft.request} />;
}

function LoadedEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const stores = useStores();
  const { edit } = useAutoSave(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;

  const stored = useDrafts((s) => s.subTabs[id]);
  const subTab: GraphqlSubTab = isSubTab(stored) ? stored : "query";
  const selectSubTab = (next: GraphqlSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });

  const { state: sendState } = useHttpSend(id);
  const sending = sendState.status === "starting" || sendState.status === "running";
  // The endpoint's schema (GQL-10, GQL-21): fetched only from the strip's button.
  const environment = useEnvironments((s) => s.activeId);
  const schema = useGraphqlSchema(stores, id, request.url, environment, request.schemaFile);
  // A schema from a local SDL file instead of introspection (GQL-11);
  // one outside the allowed directories is refused (PRV-10).
  const schemaFileAccess = useFileAccessCheck();
  const checkSchemaFile = schemaFileAccess.check;
  const loadSchemaFile = useCallback(() => {
    void (async () => {
      try {
        const path = await pickGraphqlSchemaFile();
        if (path === "") return;
        editRequest(stores, id, { schemaFile: path }, { history: "separate" });
        checkSchemaFile(path);
      } catch (err: unknown) {
        if (!isCancelled(err)) console.warn("graphql: the schema file could not be chosen", toAppError(err));
      }
    })();
  }, [stores, id, checkSchemaFile]);
  const switchToIntrospection = useCallback(
    () => editRequest(stores, id, { schemaFile: "" }, { history: "separate" }),
    [stores, id],
  );
  // The document's operations (GQL-12): the Query tab's markers and
  // picker, and whether the operation that runs is a subscription, which
  // turns Send into Subscribe / Stop (GQL-34).
  const check = useGraphqlDiagnostics(request.query, schema.info?.key ?? "");
  // The opt-in schema auto-refresh (GQL-19): off unless the Settings tab
  // turned it on. A send's validation errors only signal a changed
  // schema when the cached one accepted the query.
  const schemaAccepts =
    schema.info?.cached !== true ||
    (check.schema && check.text === request.query && !check.diagnostics.some((d) => d.source === "schema"));
  useGraphqlSchemaRefresh({ schema, refresh: schemaRefreshOf(request.settings), send: sendState, schemaAccepts });
  const { state: subState } = useGraphqlSubscription(id);
  const subscribed = subState.status === "starting" || subState.status === "running";
  // The transport of the operation that runs (GQL-35): a subscription,
  // or a query or mutation set to WebSocket, runs through the
  // subscription slot; the rest (HTTP, and SSE, whose answer is read as
  // a normal response) is sent.
  const operationType = runningOperationType(check.operations, request.operationName);
  const isSubscription = operationType === "subscription";
  const overSocket = subscribed || runsOverSocket(request.settings, operationType);
  const transport = transportFor(request.settings, operationType);
  const transportLabel = TRANSPORT_LABELS[transport as keyof typeof TRANSPORT_LABELS] ?? transport;
  useShortcut("request.send", () => {
    if (subscribed) stopSubscription(id);
    else if (overSocket) startSubscription(stores, id);
    else sendRequest(stores, id);
  });

  const mode = graphqlMode(request.method);
  const onMode = (e: ChangeEvent<HTMLSelectElement>) => {
    const next = e.target.value === "GET" ? "GET" : "POST";
    // Its own undo step, like the HTTP method picker (NAV-07).
    editRequest(stores, id, { method: next === "GET" ? "GET" : "" }, { history: "separate" });
    stores.collection.dispatch({ type: "collection/setMethod", id, method: next });
  };
  const onUrl = useCallback((url: string) => edit({ url }), [edit]);
  const onHeaders = useCallback((headers: KeyValueDTO[]) => edit({ headers }), [edit]);
  const [headerRows, setHeaderRows] = useKeyValueRows(request.headers, onHeaders);

  // The docs explorer (GQL-14): a side panel toggled from the schema strip
  // or with the graphql.docs shortcut; its fields insert into the query
  // editor while the Query tab shows.
  const [docsOpen, setDocsOpen] = useState(docsPanelOpen);
  const [docsJump, setDocsJump] = useState<{ page: DocsPage; seq: number } | null>(null);
  const toggleDocs = useCallback(() => {
    // A jump is shown once: reopening starts at the start page.
    setDocsJump(null);
    setDocsOpen((open) => {
      docsPanelOpen = !open;
      return !open;
    });
  }, []);
  useShortcut("graphql.docs", () => {
    toggleDocs();
  });
  // A Ctrl/Cmd-click in the query (GQL-15): the docs open at its page.
  const openDocsAt = useCallback((page: DocsPage) => {
    docsPanelOpen = true;
    setDocsOpen(true);
    setDocsJump((j) => ({ page, seq: (j?.seq ?? 0) + 1 }));
  }, []);
  // The selection depth of operations, fragments and filled fields (GQL-16, GQL-17).
  const [depth, setDepthState] = useState(selectionDepth);
  const setDepth = useCallback((d: number) => {
    selectionDepth = d;
    setDepthState(d);
  }, []);
  const [queryNotice, setQueryNotice] = useState<QueryNotice | null>(null);
  const [queryView, setQueryView] = useState<EditorView | null>(null);
  const insertIntoQuery = useCallback(
    (text: string) => {
      if (queryView === null) return;
      queryView.dispatch(queryView.state.replaceSelection(text), { scrollIntoView: true });
      queryView.focus();
    },
    [queryView],
  );
  const canInsert = queryView !== null && !readOnly && subTab === "query";
  const schemaKey = schema.info?.key ?? "";
  // Writes an operation or fragment from the docs (GQL-16) at the end of the query.
  const generate = useCallback(
    (req: GenerateRequest) => {
      const view = queryView;
      if (view === null) return;
      const text = view.state.doc.toString();
      graphqlGenerate(schemaKey, req, depth, text).then(
        (res) => {
          if (res.insert === "") {
            setQueryNotice({ message: res.message, query: text });
            return;
          }
          // The query changed while Go wrote: append after it instead.
          const same = view.state.doc.toString() === text;
          const end = view.state.doc.length;
          const from = same ? res.from : end;
          const insert = same ? res.insert : `\n\n${res.text}\n`;
          const cursor = same ? res.cursor : end + 2;
          view.dispatch({ changes: { from, to: same ? res.to : end, insert }, selection: { anchor: cursor }, scrollIntoView: true, userEvent: "input" });
          view.focus();
          setQueryNotice(res.message === "" ? null : { message: res.message, query: view.state.doc.toString() });
        },
        (err: unknown) => {
          if (!isCancelled(err)) setQueryNotice({ message: toAppError(err).message, query: text });
        },
      );
    },
    [queryView, schemaKey, depth],
  );

  const baseId = useId();
  const tabId = (tab: GraphqlSubTab) => `${baseId}-tab-${tab}`;
  const panelId = `${baseId}-panel`;
  const tabRefs = useRef<Partial<Record<GraphqlSubTab, HTMLButtonElement | null>>>({});
  const counts: Partial<Record<GraphqlSubTab, number>> = {
    files: activeUploads(request.files).length,
    headers: countRows(request.headers),
    tests: countAssertions(request.assertions),
    extract: countExtractions(request.extractions),
    examples: request.responseExamples?.length ?? 0,
  };

  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const at = SUB_TABS.findIndex((tab) => tab.id === subTab);
    let next: number;
    if (e.key === "ArrowRight") next = (at + 1) % SUB_TABS.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + SUB_TABS.length) % SUB_TABS.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = SUB_TABS.length - 1;
    else return;
    e.preventDefault();
    const target = SUB_TABS[next];
    if (target === undefined) return;
    selectSubTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div className={styles.editor} data-request-id={id} data-request-kind="graphql">
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}

      <div className={styles.urlBar}>
        <div className={styles.urlGroup}>
          <label className={styles.method} data-tone={mode === "GET" ? "get" : "post"} data-disabled={readOnly || undefined}>
            <select
              className={styles.methodSelect}
              aria-label={t("editorProtocols.graphql.mode")}
              title={mode === "GET" ? t("editorProtocols.graphql.modeGet") : t("editorProtocols.graphql.modePost")}
              value={mode}
              disabled={readOnly}
              onChange={onMode}
            >
              <option value="POST">POST</option>
              <option value="GET">GET</option>
            </select>
            <IconChevron size={11} className={styles.methodChevron} />
          </label>
          <CodeEditor
            className={styles.url}
            singleLine
            ariaLabel="URL"
            placeholder={t("editorProtocols.graphql.urlPlaceholder")}
            value={request.url}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onUrl}
          />
          <TlsOffBadge settings={request.settings} className={styles.tlsBadge} onOpen={() => selectSubTab("settings")} />
        </div>
        {/* WebSocket and the subscription's live log are nothing curl can send. */}
        {!subscribed && !overSocket && <CopyCurlButton id={id} />}
        {subscribed ? (
          <button
            type="button"
            className={styles.cancel}
            title={t("editorProtocols.graphql.stopTitle", { shortcut: shortcutLabel("request.send") })}
            aria-keyshortcuts={ariaShortcut("request.send")}
            onClick={() => stopSubscription(id)}
          >
            <IconStop size={10} />
            {(subState.operationType === "" ? isSubscription : subState.operationType === "subscription")
              ? t("editorProtocols.graphql.stop")
              : t("editorProtocols.cancel")}
          </button>
        ) : overSocket ? (
          <button
            type="button"
            className={styles.send}
            title={
              isSubscription
                ? t("editorProtocols.graphql.runSubscription", { transport: transportLabel, shortcut: shortcutLabel("request.send") })
                : operationType === null || operationType === undefined
                  ? t("editorProtocols.graphql.runOperation", { transport: transportLabel, shortcut: shortcutLabel("request.send") })
                  : t("editorProtocols.graphql.runKind", {
                      kind: operationType,
                      transport: transportLabel,
                      shortcut: shortcutLabel("request.send"),
                    })
            }
            aria-keyshortcuts={ariaShortcut("request.send")}
            onClick={() => startSubscription(stores, id)}
          >
            <IconPlay size={12} />
            {isSubscription ? t("editorProtocols.graphql.subscribe") : t("editorProtocols.send")}
          </button>
        ) : sending ? (
          <button type="button" className={styles.cancel} onClick={() => cancelRequest(id)}>
            {t("editorProtocols.cancel")}
          </button>
        ) : (
          <button
            type="button"
            className={styles.send}
            title={t("editorProtocols.sendTitle", { shortcut: shortcutLabel("request.send") })}
            aria-keyshortcuts={ariaShortcut("request.send")}
            onClick={() => sendRequest(stores, id)}
          >
            <IconPlay size={12} />
            {t("editorProtocols.send")}
          </button>
        )}
      </div>
      <RelativeUrlHint url={request.url} />

      <GraphqlSchemaStrip
        schema={schema}
        docsOpen={docsOpen}
        onToggleDocs={toggleDocs}
        onLoadFile={loadSchemaFile}
        onUseIntrospection={switchToIntrospection}
        readOnly={readOnly}
        requestName={request.name}
      />
      <FileAccessNotice access={schemaFileAccess} className={gqlStyles.fileAccess} />

      <div className={gqlStyles.workArea}>
        <div className={gqlStyles.main}>
          <div className={styles.subTabs} role="tablist" aria-label={t("editorProtocols.tab.sections")}>
            {SUB_TABS.map((tab) => {
              const selected = tab.id === subTab;
              const count = counts[tab.id] ?? 0;
              return (
                <button
                  key={tab.id}
                  ref={(el) => {
                    tabRefs.current[tab.id] = el;
                  }}
                  type="button"
                  role="tab"
                  id={tabId(tab.id)}
                  className={styles.subTab}
                  aria-selected={selected}
                  aria-controls={selected ? panelId : undefined}
                  tabIndex={selected ? 0 : -1}
                  onClick={() => selectSubTab(tab.id)}
                  onKeyDown={onTabKeyDown}
                >
                  {t(tab.labelKey)}
                  {count > 0 && <span className={styles.count}>{count}</span>}
                </button>
              );
            })}
          </div>

          <div className={styles.section} role="tabpanel" id={panelId} aria-labelledby={tabId(subTab)}>
            {subTab === "query" && (
              <QueryTab
                id={id}
                request={request}
                readOnly={readOnly}
                edit={edit}
                schemaKey={schemaKey}
                check={check}
                onView={setQueryView}
                depth={depth}
                onDepth={setDepth}
                onOpenDocs={openDocsAt}
                notice={queryNotice}
                onNotice={setQueryNotice}
              />
            )}
            {subTab === "variables" && <VariablesTab request={request} readOnly={readOnly} edit={edit} />}
            {subTab === "extensions" && <ExtensionsTab request={request} readOnly={readOnly} edit={edit} />}
            {subTab === "files" && <GraphqlFiles id={id} request={request} readOnly={readOnly} />}
            {subTab === "connection" && <ConnectionTab request={request} readOnly={readOnly} edit={edit} />}
            {subTab === "headers" && (
              <GraphqlImplicitHeaders
                mode={mode}
                headers={request.headers}
                uploads={transport !== "sse" && activeUploads(request.files).length > 0}
                settings={request.settings}
                incremental={usesIncrementalDelivery(request.query)}
                eventStream={transport === "sse"}
              />
            )}
            {subTab === "headers" && <InheritedHeaders id={id} own={request.headers} />}
            {subTab === "headers" && (
              <KeyValueTable
                label={t("editorProtocols.tab.headers")}
                bulkEdit
                rows={headerRows}
                onChange={readOnly ? undefined : setHeaderRows}
                readOnly={readOnly}
                placeholders={{ key: "header" }}
                suggest={HEADER_SUGGEST}
                suggestLabels={HEADER_SUGGEST_LABELS}
              />
            )}
            {subTab === "auth" && <AuthEditor id={id} auth={request.auth} readOnly={readOnly} />}
            {subTab === "tests" && <AssertionsEditor id={id} assertions={request.assertions} readOnly={readOnly} edit={edit} />}
            {subTab === "extract" && <ExtractionsEditor id={id} extractions={request.extractions} readOnly={readOnly} edit={edit} />}
            {subTab === "examples" && <ResponseExamples id={id} examples={request.responseExamples ?? []} readOnly={readOnly} />}
            {subTab === "settings" && (
              <RequestSettings settings={request.settings} readOnly={readOnly} edit={edit}>
                <GraphqlSchemaRefreshSettings request={request} readOnly={readOnly} edit={edit} />
              </RequestSettings>
            )}
            {subTab === "notes" && (
              <NotesPanel
                value={request.description ?? ""}
                subject="request"
                readOnly={readOnly}
                onChange={(description) => edit({ description })}
              />
            )}
            {subTab === "code" && (
              <SnippetPanel id={id} request={request} unavailable={overSocket ? t("editorProtocols.graphql.snippetOverSocket") : null} />
            )}
          </div>
        </div>
        {docsOpen && (
          <GraphqlDocs
            schemaKey={schema.info?.key ?? ""}
            version={schema.info?.fetchedAt ?? ""}
            onFetch={schema.fetch}
            fetching={schema.fetching}
            onInsert={canInsert ? insertIntoQuery : undefined}
            onGenerate={canInsert && schemaKey !== "" ? generate : undefined}
            depth={depth}
            onDepth={setDepth}
            jump={docsJump}
            onClose={toggleDocs}
          />
        )}
      </div>
    </div>
  );
}

type Edit = ReturnType<typeof useAutoSave>["edit"];

/** Puts the cursor on an error position and focuses the editor. */
function goTo(view: EditorView | null | undefined, offset: number): void {
  if (!view) return;
  const range = markerRange(view.state.doc, offset);
  view.dispatch({ selection: { anchor: range?.from ?? 0 }, scrollIntoView: true });
  view.focus();
}

/**
 * The Query tab: the GraphQL document (highlighted by the in-house mode),
 * Go's live diagnostics against the cached schema `schemaKey` (markers and
 * the problems bar, GQL-12) and autocomplete from it (GQL-13), the
 * operation picker when the document has
 * several operations, and Prettify (⇧⌘F inside the query editor, GQL-18)
 * and Minify, formatted by Go (commands/graphql.ts). Fill fields (GQL-17)
 * writes the missing fields of the selection set at the cursor, and a
 * Ctrl/Cmd-click on a name (or the graphql.docsAtCursor chord) opens its
 * docs (GQL-15); both ask Go (GraphQLService.FillFields, DocsTarget).
 */
function QueryTab({
  id,
  request,
  readOnly,
  edit,
  schemaKey,
  check,
  onView,
  depth = DEFAULT_GRAPHQL_DEPTH,
  onDepth,
  onOpenDocs,
  notice = null,
  onNotice,
}: {
  id: RequestId;
  request: RequestDTO;
  readOnly: boolean;
  edit: Edit;
  schemaKey: string;
  /** Go's diagnostics of the query (the editor runs them: they also decide Send or Subscribe). */
  check: GraphqlDiagnostics;
  /** Receives the query editor's view once it exists, and null when the tab goes (the docs panel inserts into it). */
  onView?: (view: EditorView | null) => void;
  /** The selection depth of Fill fields, and its setter. */
  depth?: number;
  onDepth?: (depth: number) => void;
  /** Opens the docs at a page (a Ctrl/Cmd-click on a name). */
  onOpenDocs?: (page: DocsPage) => void;
  /** The message of the last schema action, and its setter. */
  notice?: QueryNotice | null;
  onNotice?: (notice: QueryNotice | null) => void;
}) {
  const t = useT();
  const stores = useStores();
  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installDiagnostics(v);
    installGraphqlCompletion(v);
    installGraphqlDocsLink(v);
    setView(v);
  }, []);
  // Autocomplete (GQL-13) reads the schema key from the editor state.
  useEffect(() => {
    if (view !== null && completionSchema(view.state) !== schemaKey) {
      view.dispatch({ effects: setCompletionSchema.of(schemaKey) });
    }
  }, [view, schemaKey]);
  useEffect(() => {
    if (view === null || onView === undefined) return;
    onView(view);
    return () => onView(null);
  }, [view, onView]);
  const onQuery = useCallback((query: string) => edit({ query }), [edit]);
  // "Line 3, column 5" on a GraphQL error in the response pane lands here (GQL-05).
  useQueryJump(id, view);

  useDiagnosticMarks(view, check);

  const choice = operationChoice(check.operations, request.operationName);
  const onOperation = (e: ChangeEvent<HTMLSelectElement>) => edit({ operationName: e.target.value });

  const [busy, setBusy] = useState<GraphqlFormat | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const runFormat = useCallback(
    async (format: GraphqlFormat) => {
      setBusy(format);
      setFailure(null);
      const outcome = await formatGraphqlQuery(
        {
          get: () => (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined)?.query,
          set: (query) => edit({ query }),
        },
        format,
      );
      if (!mounted.current) return;
      setBusy(null);
      if (outcome.status === "failed") setFailure(outcome.message);
      if (outcome.status === "invalid") {
        const v = editorRef.current?.view;
        const { offset, endOffset, message } = outcome.error;
        v?.dispatch({ effects: setDiagnostics.of([{ from: offset, to: endOffset, severity: "error", message }]) });
        goTo(v, outcome.error.offset);
      }
    },
    [stores, id, edit],
  );

  const say = useCallback((message: string, v: EditorView) => onNotice?.({ message, query: v.state.doc.toString() }), [onNotice]);

  // Jump to the docs (GQL-15): what the name at `pos` refers to, opened in the docs panel.
  const showDocsAt = useCallback(
    (v: EditorView, pos: number, fromClick: boolean) => {
      if (onOpenDocs === undefined) return;
      if (schemaKey === "") {
        say(t("editorProtocols.graphql.docsNeedSchema"), v);
        return;
      }
      const text = v.state.doc.toString();
      graphqlDocsTarget(schemaKey, text, pos).then(
        (target) => {
          if (!mounted.current) return;
          const page = docsPageFor(target);
          if (page !== null) {
            onNotice?.(null);
            onOpenDocs(page);
          } else if (fromClick) {
            v.dispatch({ selection: { anchor: pos } });
          } else {
            say(t("editorProtocols.graphql.noDocsAtCursor"), v);
          }
        },
        (err: unknown) => {
          if (!isCancelled(err)) console.warn("graphql: the docs target could not be found", toAppError(err));
        },
      );
    },
    [schemaKey, onOpenDocs, onNotice, say, t],
  );
  // The Ctrl/Cmd-click handler reads the component of now (see graphqlDocsLink.ts).
  // Not cleared on unmount: the view may be gone by then, and the next
  // mount sets its own at once.
  useEffect(() => {
    if (view === null) return;
    view.dispatch({ effects: setDocsLinkHandler.of(onOpenDocs === undefined ? null : (pos) => showDocsAt(view, pos, true)) });
  }, [view, showDocsAt, onOpenDocs]);

  // Fill fields (GQL-17): the missing fields of the selection set at the cursor.
  const [filling, setFilling] = useState(false);
  const runFill = useCallback(() => {
    const v = editorRef.current?.view;
    if (!v) return;
    if (schemaKey === "") {
      say(t("editorProtocols.graphql.fillNeedsSchema"), v);
      return;
    }
    const text = v.state.doc.toString();
    setFilling(true);
    graphqlFillFields(schemaKey, text, v.state.selection.main.head, depth).then(
      (res) => {
        if (!mounted.current) return;
        setFilling(false);
        if (res.reason !== "" || res.text === "") {
          say(res.message, v);
          return;
        }
        if (v.state.doc.toString() !== text) return; // typed on meanwhile: the range is stale
        v.dispatch({ changes: { from: res.from, to: res.to, insert: res.text }, selection: { anchor: res.cursor }, scrollIntoView: true, userEvent: "input" });
        v.focus();
        if (res.message !== "") say(res.message, v);
        else onNotice?.(null);
      },
      (err: unknown) => {
        if (!mounted.current) return;
        setFilling(false);
        if (!isCancelled(err)) say(toAppError(err).message, v);
      },
    );
  }, [schemaKey, depth, say, onNotice, t]);

  const canFormat = !readOnly && request.query.trim() !== "" && busy === null;
  const canFill = !readOnly && !filling;
  // ⇧⌘F inside the query editor: its scope takes the chord from the global Beautify JSON body.
  // ⌥⌘F fills in fields, ⌥⌘D shows the docs of the name at the cursor.
  const onKeyDownCapture = (e: KeyboardEvent<HTMLDivElement>) => {
    if (matchesShortcut(e, "graphql.prettify")) {
      e.preventDefault();
      e.stopPropagation();
      if (canFormat) void runFormat("prettify");
    } else if (matchesShortcut(e, "graphql.fillFields")) {
      e.preventDefault();
      e.stopPropagation();
      if (canFill) runFill();
    } else if (matchesShortcut(e, "graphql.docsAtCursor")) {
      e.preventDefault();
      e.stopPropagation();
      const v = editorRef.current?.view;
      if (v) showDocsAt(v, v.state.selection.main.head, false);
    }
  };

  return (
    <div className={bodyStyles.body}>
      <div className={bodyStyles.toolbar}>
        {choice.pick && (
          <select
            className={bodyStyles.type}
            aria-label={t("editorProtocols.graphql.operation")}
            value={choice.missing ? "" : request.operationName}
            disabled={readOnly}
            onChange={onOperation}
          >
            {choice.missing && (
              <option value="" disabled>
                {t("editorProtocols.graphql.chooseOperation")}
              </option>
            )}
            {choice.names.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        )}
        <div className={bodyStyles.tools}>
          <button
            type="button"
            className={bodyStyles.tool}
            disabled={!canFormat}
            title={t("editorProtocols.graphql.prettifyTitle", { shortcut: shortcutLabel("graphql.prettify") })}
            aria-keyshortcuts={ariaShortcut("graphql.prettify")}
            onClick={() => void runFormat("prettify")}
          >
            {t("editorProtocols.graphql.prettify")}
          </button>
          <button
            type="button"
            className={bodyStyles.tool}
            disabled={!canFormat}
            title={t("editorProtocols.graphql.minifyTitle")}
            onClick={() => void runFormat("minify")}
          >
            {t("editorProtocols.graphql.minify")}
          </button>
          <button
            type="button"
            className={bodyStyles.tool}
            disabled={!canFill}
            title={t("editorProtocols.graphql.fillTitle", { shortcut: shortcutLabel("graphql.fillFields") })}
            aria-keyshortcuts={ariaShortcut("graphql.fillFields")}
            onClick={runFill}
          >
            {t("editorProtocols.graphql.fill")}
          </button>
          {onDepth !== undefined && (
            <label className={gqlStyles.depth} title={t("editorProtocols.graphql.depthTitle")}>
              {t("editorProtocols.graphql.depth")}
              <select className={bodyStyles.type} value={depth} disabled={readOnly} onChange={(e) => onDepth(Number(e.target.value))}>
                {GRAPHQL_DEPTHS.map((d) => (
                  <option key={d} value={d}>
                    {d}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
      </div>
      <div className={bodyStyles.editorFrame} data-shortcut-scope="graphql" onKeyDownCapture={onKeyDownCapture}>
        <CodeEditor
          ref={editorRef}
          value={request.query}
          language="graphql"
          ariaLabel={t("editorProtocols.graphql.queryLabel")}
          readOnly={readOnly}
          onChange={readOnly ? undefined : onQuery}
          onReady={onReady}
        />
      </div>
      {failure !== null && (
        <p className={bodyStyles.status} role="alert">
          <IconWarning size={12} />
          {failure}
        </p>
      )}
      {notice !== null && notice.message !== "" && notice.query === request.query && (
        <p className={bodyStyles.status} role="status">
          <IconInfo size={12} />
          {notice.message}
        </p>
      )}
      <GraphqlProblemsBar check={check} onJump={(offset) => goTo(editorRef.current?.view, offset)} />
    </div>
  );
}

/** A variable's name and type for a message: `$id: ID!`. */
function variableLabel(v: GraphQLVariableDTO): string {
  return v.type === "" ? `$${v.name}` : `$${v.name}: ${v.type}`;
}

/**
 * The Variables tab (GQL-03, GQL-04): the variables' JSON text, checked by
 * Go: a syntax error gets the marker and a status line; then the variables
 * the operation that runs declares but the object lacks, and the members
 * it does not declare, are listed as warnings (members jump to their
 * name).
 */
function VariablesTab({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: Edit }) {
  const t = useT();
  const text = request.variables;
  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);
  const onVariables = useCallback((variables: string) => edit({ variables }), [edit]);
  const check = useGraphqlVariablesCheck(request.query, text, request.operationName);
  const result = check.text === text ? check.result : null;
  useMarker(view, check.text, check.result?.error ?? null);
  const error = result?.error ?? null;
  const operation = result?.operation ?? "";
  const missing = result?.missing ?? [];
  const unknown = result?.unknown ?? [];
  const missingText = (v: GraphQLVariableDTO): string => {
    const variable = variableLabel(v);
    if (v.required) {
      return operation === ""
        ? t("editorProtocols.graphql.variableRequiredAnon", { variable })
        : t("editorProtocols.graphql.variableRequired", { variable, operation });
    }
    return operation === ""
      ? t("editorProtocols.graphql.variableDeclaredAnon", { variable })
      : t("editorProtocols.graphql.variableDeclared", { variable, operation });
  };

  return (
    <div className={bodyStyles.body}>
      <div className={bodyStyles.editorFrame}>
        <CodeEditor
          ref={editorRef}
          value={text}
          language="json"
          ariaLabel={t("editorProtocols.graphql.variablesLabel")}
          readOnly={readOnly}
          onChange={readOnly ? undefined : onVariables}
          onReady={onReady}
        />
      </div>
      {error !== null && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          <button type="button" className={bodyStyles.position} onClick={() => goTo(editorRef.current?.view, error.offset)}>
            {t("editorProtocols.position", { line: error.line, column: error.column })}
          </button>
          <span>{error.message}</span>
        </p>
      )}
      {result !== null && result.notObject && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          {/* i18n-ignore: a JSON example */}
          <span>{t("editorProtocols.graphql.variablesNotObject", { example: '{"id": 1}' })}</span>
        </p>
      )}
      {(missing.length > 0 || unknown.length > 0) && (
        <ul className={gqlStyles.problems} aria-label={t("editorProtocols.graphql.variableWarnings")}>
          {missing.map((v) => (
            <li key={`missing-${v.name}`} className={gqlStyles.problem} data-severity={v.required ? "error" : "warning"}>
              <IconWarning size={12} />
              <span>{missingText(v)}</span>
            </li>
          ))}
          {unknown.map((k) => (
            <li key={`unknown-${k.offset}`} className={gqlStyles.problem} data-severity="warning">
              <IconWarning size={12} />
              <button type="button" className={gqlStyles.position} onClick={() => goTo(editorRef.current?.view, k.offset)}>
                {t("editorProtocols.position", { line: k.line, column: k.column })}
              </button>
              <span>
                {operation === ""
                  ? t("editorProtocols.graphql.notAVariableAnon", { name: k.name })
                  : t("editorProtocols.graphql.notAVariable", { name: k.name, operation })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Whether connection params text is blank, null or starts an object (what Go accepts once it parses). */
export function paramsLookLikeObject(text: string): boolean {
  const t = text.trim();
  return t === "" || t === "null" || t.startsWith("{");
}

/** What each transport does, under the pickers (message keys). */
const TRANSPORT_HELP: Record<Exclude<GraphqlTransport, "">, MessageKey> = {
  http: "editorProtocols.graphql.transport.http",
  ws: "editorProtocols.graphql.transport.ws",
  "ws-legacy": "editorProtocols.graphql.transport.wsLegacy",
  sse: "editorProtocols.graphql.transport.sse",
};

const OPERATION_LABELS: Record<GraphqlOperationKind, MessageKey> = {
  query: "editorProtocols.graphql.queries",
  mutation: "editorProtocols.graphql.mutations",
  subscription: "editorProtocols.graphql.subscriptions",
};

/**
 * The transport of each operation type (GQL-35): queries, mutations and
 * subscriptions each run over HTTP, WebSocket (either protocol) or SSE.
 * The defaults (HTTP, and graphql-transport-ws for subscriptions) are
 * stored as nothing, so files without a choice load unchanged.
 */
function TransportSettings({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: Edit }) {
  const t = useT();
  const baseId = useId();
  const current = request.settings.graphqlTransport ?? null;
  const onPick = (kind: GraphqlOperationKind, value: string) => {
    const next = value === defaultTransport(kind) ? "" : (value as GraphqlTransport);
    edit({ settings: { ...request.settings, graphqlTransport: transportPatch(current, kind, next) } });
  };
  return (
    <section className={settingsStyles.group} aria-label={t("editorProtocols.graphql.transport")}>
      <h3 className={settingsStyles.heading}>{t("editorProtocols.graphql.transport")}</h3>
      {GRAPHQL_OPERATION_KINDS.map((kind) => {
        const picked = transportFor(request.settings, kind);
        const choices = transportChoices(kind);
        const known = (choices as readonly string[]).includes(picked);
        const selectId = `${baseId}-${kind}`;
        return (
          <div key={kind} className={settingsStyles.row}>
            <label className={settingsStyles.label} htmlFor={selectId}>
              {t(OPERATION_LABELS[kind])}
            </label>
            <select id={selectId} className={settingsStyles.select} value={picked} disabled={readOnly} onChange={(e) => onPick(kind, e.currentTarget.value)}>
              {choices.map((choice) => (
                <option key={choice} value={choice}>
                  {choice === defaultTransport(kind)
                    ? t("editorProtocols.graphql.transportDefault", { transport: TRANSPORT_LABELS[choice] })
                    : TRANSPORT_LABELS[choice]}
                </option>
              ))}
              {!known && <option value={picked}>{t("editorProtocols.graphql.transportUnknown", { transport: picked })}</option>}
            </select>
          </div>
        );
      })}
      <p className={settingsStyles.help}>
        {(["ws", "ws-legacy", "sse"] as const)
          .filter((tr) => GRAPHQL_OPERATION_KINDS.some((kind) => transportFor(request.settings, kind) === tr))
          .map((tr) => t(TRANSPORT_HELP[tr]))
          .join(" ") || t(TRANSPORT_HELP.http)}
      </p>
    </section>
  );
}

/**
 * The Connection tab (GQL-33): the connection params, the JSON object Go
 * sends as the connection_init payload when a subscription runs over
 * WebSocket (an auth token, say). Saved as text like the variables; a
 * syntax error gets the marker and a status line, a value that is not an
 * object a warning. {{variables}} are rendered on every subscribe.
 */
function ConnectionTab({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: Edit }) {
  const t = useT();
  const text = request.connectionParams;
  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);
  const onParams = useCallback((connectionParams: string) => edit({ connectionParams }), [edit]);
  const check = useJsonCheck(text, true);
  useMarker(view, check.text, check.error);
  const error = check.text === text ? check.error : null;

  return (
    <div className={bodyStyles.body}>
      <TransportSettings request={request} readOnly={readOnly} edit={edit} />
      <p className={gqlStyles.hint}>
        <Trans
          k="editorProtocols.graphql.connectionHint"
          values={{ message: <code>connection_init</code>, example: <code>{'{"authToken": "{{token}}"}'}</code> }}
        />
      </p>
      <div className={bodyStyles.editorFrame}>
        <CodeEditor
          ref={editorRef}
          value={text}
          language="json"
          ariaLabel={t("editorProtocols.graphql.connectionLabel")}
          // i18n-ignore: a JSON example
          placeholder='{"authToken": "…"}'
          readOnly={readOnly}
          onChange={readOnly ? undefined : onParams}
          onReady={onReady}
        />
      </div>
      {error !== null && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          <button type="button" className={bodyStyles.position} onClick={() => goTo(editorRef.current?.view, error.offset)}>
            {t("editorProtocols.position", { line: error.line, column: error.column })}
          </button>
          <span>{error.message}</span>
        </p>
      )}
      {error === null && !paramsLookLikeObject(text) && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          {/* i18n-ignore: a JSON example */}
          <span>{t("editorProtocols.graphql.connectionNotObject", { example: '{"authToken": "…"}' })}</span>
        </p>
      )}
    </div>
  );
}

/**
 * The Extensions tab (GQL-06): the request's `extensions` object, sent
 * with the query (in the JSON body, the GET query string, the multipart
 * operations or the subscribe payload), e.g. a persisted query's hash.
 * Saved as text like the variables; a syntax error gets the marker and a
 * status line, a value that is not an object a warning. {{variables}} are
 * rendered on every send.
 */
function ExtensionsTab({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: Edit }) {
  const t = useT();
  const text = request.extensions ?? "";
  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);
  const onExtensions = useCallback((extensions: string) => edit({ extensions }), [edit]);
  const check = useJsonCheck(text, true);
  useMarker(view, check.text, check.error);
  const error = check.text === text ? check.error : null;

  return (
    <div className={bodyStyles.body}>
      <p className={gqlStyles.hint}>
        <Trans
          k="editorProtocols.graphql.extensionsHint"
          values={{
            name: <code>extensions</code>,
            example: <code>{'{"persistedQuery": {"version": 1, "sha256Hash": "…"}}'}</code>,
          }}
        />
      </p>
      <div className={bodyStyles.editorFrame}>
        <CodeEditor
          ref={editorRef}
          value={text}
          language="json"
          ariaLabel={t("editorProtocols.graphql.extensionsLabel")}
          // i18n-ignore: a JSON example
          placeholder='{"persistedQuery": {"version": 1, "sha256Hash": "…"}}'
          readOnly={readOnly}
          onChange={readOnly ? undefined : onExtensions}
          onReady={onReady}
        />
      </div>
      {error !== null && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          <button type="button" className={bodyStyles.position} onClick={() => goTo(editorRef.current?.view, error.offset)}>
            {t("editorProtocols.position", { line: error.line, column: error.column })}
          </button>
          <span>{error.message}</span>
        </p>
      )}
      {error === null && !paramsLookLikeObject(text) && (
        <p className={bodyStyles.status} role="status">
          <IconWarning size={12} />
          <span>{t("editorProtocols.graphql.extensionsNotObject")}</span>
        </p>
      )}
    </div>
  );
}

/**
 * The headers Go adds to a GraphQL request, shown read-only above the
 * Headers table: Content-Type for POST (multipart/form-data with files,
 * GQL-07) and Accept, and with files the Apollo-Require-Preflight header
 * unless the Files tab turns it off; each is overridden by an enabled row
 * of the same name.
 */
function GraphqlImplicitHeaders({
  mode,
  headers,
  uploads,
  settings,
  incremental,
  eventStream = false,
}: {
  mode: "POST" | "GET";
  headers: readonly KeyValueDTO[] | null;
  /** The request has files to upload. */
  uploads: boolean;
  settings: RequestSettingsDTO;
  /** The query uses @defer or @stream (GQL-08): Go asks for incremental delivery. */
  incremental: boolean;
  /** The operation runs over SSE (GQL-32): Go asks for an event stream. */
  eventStream?: boolean;
}) {
  const t = useT();
  const multipart = mode === "POST" && uploads;
  const accept = eventStream ? "text/event-stream" : incremental ? GRAPHQL_INCREMENTAL_ACCEPT : GRAPHQL_ACCEPT;
  const rows = [
    ...(mode === "POST"
      ? [{ name: "Content-Type", value: multipart ? "multipart/form-data; boundary=…" : "application/json" }]
      : []),
    { name: "Accept", value: accept }, // i18n-ignore: a header name
    ...(multipart && sendsPreflight(settings) ? [{ name: PREFLIGHT_HEADER, value: "true" }] : []),
  ];
  return (
    <ul className={implicitStyles.implicit} aria-label={t("editorProtocols.implicit.label")}>
      {rows.map((row) => {
        const overridden = hasRow(headers, row.name);
        const title = overridden
          ? t("editorProtocols.implicit.overriddenTitle", { name: row.name })
          : t("editorProtocols.implicit.graphqlTitle", { name: row.name });
        return (
          <li key={row.name} className={implicitStyles.row} data-overridden={overridden || undefined} title={title}>
            <span className={implicitStyles.badge}>
              {overridden ? t("editorProtocols.implicit.overridden") : t("editorProtocols.implicit.auto")}
            </span>
            <span className={implicitStyles.name}>{row.name}</span>
            <span className={implicitStyles.value}>{overridden ? <s>{row.value}</s> : row.value}</span>
            <span className={implicitStyles.srOnly}>{title}</span>
          </li>
        );
      })}
    </ul>
  );
}
