import { useCallback, useEffect, useId, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { cancelRequest, repeatRequest, sendRequest, type HttpRunOptions } from "../../commands/httpSends";
import { connectStream, stopStream } from "../../commands/sseStreams";
import { useStreamTests } from "../../commands/streamTests";
import { setRequestMethod } from "../../commands/requestEditor";
import { useAutoSave } from "../../commands/useAutoSave";
import { useHttpSend } from "../../commands/useHttpSend";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { useSseStream } from "../../commands/useSseStream";
import { countAssertions } from "../../lib/assertions";
import { countExtractions } from "../../lib/extractions";
import type { KeyValueDTO, RequestDTO } from "../../lib/collection";
import { looksLikeCurl } from "../../lib/curl";
import { SHOW_RESOLUTION_EVENT, type ShowResolutionDetail } from "../../lib/resolution";
import { syncPathParams, type PathParamStash } from "../../lib/pathParams";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { useCollection, useDrafts, useStores, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconChevron, IconLock, IconPlay, IconWarning } from "../common/icons";
import { createKeyValueRow, KeyValueTable } from "../common/KeyValueTable";
import { useShortcut } from "../shell/useShortcut";
import { CodeEditor } from "./CodeEditor";
import { CopyCurlButton } from "./CopyCurlButton";
import { pasteCurlIntoRequest } from "./curlPaste";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "./headerSuggest";
import { AssertionsEditor } from "./AssertionsEditor";
import { ExtractionsEditor } from "./ExtractionsEditor";
import { AuthEditor } from "./AuthEditor";
import { AutoHeaders } from "./AutoHeaders";
import { InheritedHeaders } from "./InheritedRows";
import { BodyEditor } from "./BodyEditor";
import { useKeyValueRows } from "./keyValueRows";
import { MethodPicker } from "./MethodPicker";
import { NotesPanel } from "./NotesPanel";
import { installPathParamHighlight } from "./pathParamHighlight";
import styles from "./RequestEditor.module.css";
import { RepeatDialog } from "./RepeatDialog";
import { ResponseExamples } from "./ResponseExamples";
import { RelativeUrlHint } from "./RelativeUrlHint";
import { RequestSettings, TlsOffBadge } from "./RequestSettings";
import { ResolutionPanel } from "./ResolutionPanel";
import { SnippetPanel } from "./SnippetPanel";
import { useUrlParamsSync } from "./useUrlParamsSync";

export type RequestSubTab =
  | "params"
  | "headers"
  | "body"
  | "auth"
  | "tests"
  | "extract"
  | "examples"
  | "settings"
  | "notes"
  | "variables"
  | "code";

const SUB_TABS: readonly { id: RequestSubTab; labelKey: MessageKey }[] = [
  { id: "params", labelKey: "editor.request.tab.params" },
  { id: "headers", labelKey: "editor.request.tab.headers" },
  { id: "body", labelKey: "editor.request.tab.body" },
  { id: "auth", labelKey: "editor.request.tab.auth" },
  { id: "tests", labelKey: "editor.request.tab.tests" },
  // Extraction into variables (TEST-05, ExtractionsEditor).
  { id: "extract", labelKey: "editor.request.tab.extract" },
  { id: "examples", labelKey: "editor.request.tab.examples" },
  { id: "settings", labelKey: "editor.request.tab.settings" },
  { id: "notes", labelKey: "editor.request.tab.notes" },
  // Where each {{variable}} comes from (DBG-08, ResolutionPanel).
  { id: "variables", labelKey: "editor.request.tab.variables" },
  // The request as code (GEN-12, SnippetPanel): last, a view of the rest.
  { id: "code", labelKey: "editor.request.tab.code" },
];

/** Nothing is extracted from an SSE request's event stream and it has no history: no Extract or Examples sub-tab (TEST-05, HTTP-38). Its Tests check the events (TEST-08). */
const STREAM_SUB_TABS = SUB_TABS.filter((t) => t.id !== "extract" && t.id !== "examples");

function isSubTab(tabs: typeof SUB_TABS, value: string | undefined): value is RequestSubTab {
  return tabs.some((t) => t.id === value);
}

const ITEM_READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editor.request.readOnly.newerVersion",
  symlink: "editor.request.readOnly.symlink",
  unknown_kind: "editor.request.readOnly.unknownKind",
  invalid: "editor.request.readOnly.invalid",
  deleted: "editor.request.readOnly.deleted",
};

const PROJECT_READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editor.request.readOnly.project.newerVersion",
  invalid: "editor.request.readOnly.project.invalid",
};

/** Why request `id` cannot be edited, or null when it can (also used by the GraphQL editor). */
export function useReadOnlyNotice(id: RequestId): string | null {
  const t = useT();
  const itemReason = useCollection((s) => s.nodes[id]?.readOnly ?? "");
  const projectReason = useCollection((s) => s.readOnly);
  if (itemReason !== "") return t(ITEM_READ_ONLY[itemReason] ?? "editor.request.readOnly.request");
  if (projectReason !== "") return t(PROJECT_READ_ONLY[projectReason] ?? "editor.request.readOnly.project");
  return null;
}

/** Rows that count in a sub-tab's badge: enabled, with a name (mockup). */
function countRows(rows: readonly KeyValueDTO[] | null): number {
  return (rows ?? []).filter((r) => r.enabled && r.name !== "").length;
}

export interface RequestEditorProps {
  id: RequestId;
}

/**
 * The HTTP request editor (HTTP-01), as in the mockup: method picker,
 * URL field and Send, then the Params / Headers / Body / Auth / Tests / Extract /
 * Examples / Settings / Notes / Variables / Code sub-tabs (Tests: the assertions, TEST-01,
 * AssertionsEditor; Extract: the rules that set variables from responses,
 * TEST-05, ExtractionsEditor; Examples: the saved responses, HTTP-38, ResponseExamples;
 * Code: the request as code, GEN-12, SnippetPanel). Bound to the request's draft: every change is auto-saved
 * (useAutoSave). Send and ⌘↩ send the request (HTTP-20); while it runs the
 * button cancels it. The arrow next to Send opens the Repeat dialog, which
 * sends it N times or at an interval (HTTP-27); Stop ends the run. The response shows in the response pane
 * (HttpResponsePanel). A curl command pasted into the URL field replaces
 * the request, with Undo (IMP-01, curlPaste.tsx). Mount it with `key={id}`
 * so each request gets fresh local state.
 *
 * An sse request (SSE-01) has the same members and is edited here too;
 * Send is Connect instead, which opens its event stream, and Stop while it
 * runs (commands/sseStreams.ts). Its events show in SseResponsePanel. It
 * has no Tests sub-tab: an event stream is not checked.
 */
export function RequestEditor({ id }: RequestEditorProps) {
  const t = useT();
  const draft = useRequestDraft(id);

  if (draft.status === "loading") {
    return <div className={styles.editor} aria-busy="true" />;
  }
  if (draft.status === "error") {
    return (
      <div className={styles.editor}>
        <EmptyState icon={<IconWarning size={20} />} title={t("editor.request.openFailed")}>
          {draft.error.message}{" "}
          <button type="button" className={styles.linkButton} onClick={draft.retry}>
            {t("editor.request.tryAgain")}
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

  const stream = request.kind === "sse";
  const tabs = stream ? STREAM_SUB_TABS : SUB_TABS;
  const streamTests = useStreamTests(id);

  const stored = useDrafts((s) => s.subTabs[id]);
  const subTab: RequestSubTab = isSubTab(tabs, stored) ? stored : "params";
  const selectSubTab = (next: RequestSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });

  const onMethod = useCallback((method: string) => setRequestMethod(stores, id, method), [stores, id]);
  const { onUrlChange, onParamsChange, onPathParamsChange } = useUrlParamsSync(id, edit);
  const onHeaders = useCallback((headers: KeyValueDTO[]) => edit({ headers }), [edit]);
  const onNotes = useCallback((description: string) => edit({ description }), [edit]);
  const [paramRows, setParamRows] = useKeyValueRows(request.params, onParamsChange);
  // The path table follows the URL's placeholders; a file that disagrees is brought in step by the next edit.
  const pathStash = useDrafts((s) => s.pathParamStash[id]) as PathParamStash | undefined;
  const pathParams = useMemo(
    () => syncPathParams(request.url, request.pathParams, pathStash).rows ?? [],
    [request.url, request.pathParams, pathStash],
  );
  const [pathRows, setPathRows] = useKeyValueRows(pathParams, onPathParamsChange);
  const [headerRows, setHeaderRows] = useKeyValueRows(request.headers, onHeaders);

  // A curl command pasted into the URL field replaces the request (IMP-01).
  // Taken in the capture phase, before CodeMirror joins its lines into the URL.
  const onUrlPaste = (e: ClipboardEvent<HTMLDivElement>) => {
    if (readOnly || stream || !(e.target instanceof Element) || e.target.closest(".cm-editor") === null) return;
    const text = e.clipboardData.getData("text/plain");
    if (!looksLikeCurl(text)) return;
    e.preventDefault();
    e.stopPropagation();
    void pasteCurlIntoRequest(stores, id, text);
  };

  // A chip's hover asks for the resolution of one name (DBG-08): show the Variables sub-tab with it marked.
  const root = useRef<HTMLDivElement>(null);
  const [resolutionFocus, setResolutionFocus] = useState<string | null>(null);
  useEffect(() => {
    const el = root.current;
    if (el === null) return;
    const onShow = (e: Event) => {
      setResolutionFocus((e as CustomEvent<ShowResolutionDetail>).detail.name);
      stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: "variables" });
    };
    el.addEventListener(SHOW_RESOLUTION_EVENT, onShow);
    return () => el.removeEventListener(SHOW_RESOLUTION_EVENT, onShow);
  }, [stores, id]);

  const baseId = useId();
  const tabId = (tab: RequestSubTab) => `${baseId}-tab-${tab}`;
  const panelId = `${baseId}-panel`;
  const tabRefs = useRef<Partial<Record<RequestSubTab, HTMLButtonElement | null>>>({});

  const counts: Partial<Record<RequestSubTab, number>> = {
    params: countRows(request.params) + pathParams.length,
    headers: countRows(request.headers),
    tests: countAssertions(request.assertions),
    extract: countExtractions(request.extractions),
    examples: request.responseExamples?.length ?? 0,
  };

  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const at = tabs.findIndex((tab) => tab.id === subTab);
    let next: number;
    if (e.key === "ArrowRight") next = (at + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = tabs.length - 1;
    else return;
    e.preventDefault();
    const target = tabs[next];
    if (target === undefined) return;
    selectSubTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div ref={root} className={styles.editor} data-request-id={id} data-resolution-host="">
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}

      <div className={styles.urlBar}>
        <div className={styles.urlGroup} onPasteCapture={onUrlPaste}>
          <MethodPicker method={request.method} onChange={readOnly ? undefined : onMethod} disabled={readOnly} />
          <CodeEditor
            className={styles.url}
            singleLine
            ariaLabel="URL"
            placeholder={t("editor.request.urlPlaceholder")}
            value={request.url}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onUrlChange}
            onReady={installPathParamHighlight}
          />
          <TlsOffBadge settings={request.settings} className={styles.tlsBadge} onOpen={() => selectSubTab("settings")} />
        </div>
        <CopyCurlButton id={id} />
        {stream ? <ConnectButton id={id} /> : <SendButton id={id} />}
      </div>
      <RelativeUrlHint url={request.url} />

      <div className={styles.subTabs} role="tablist" aria-label={t("editor.request.sections")}>
        {tabs.map((tab) => {
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
        {subTab === "params" && pathRows.length > 0 && (
          <h3 className={styles.paramsHeading} aria-hidden="true">
            {t("editor.request.queryParams")}
          </h3>
        )}
        {subTab === "params" && (
          <KeyValueTable
            label={t("editor.request.queryParams")}
            bulkEdit
            rows={paramRows}
            onChange={readOnly ? undefined : setParamRows}
            readOnly={readOnly}
          />
        )}
        {subTab === "params" && pathRows.length > 0 && (
          <>
            <h3 className={styles.paramsHeading} aria-hidden="true">
              {t("editor.request.pathVariables")}
            </h3>
            <KeyValueTable
              label={t("editor.request.pathVariables")}
              rows={pathRows}
              onChange={readOnly ? undefined : setPathRows}
              readOnly={readOnly}
              fixedKeys
              placeholders={{ value: t("editor.request.placeholder.required") }}
            />
          </>
        )}
        {subTab === "headers" && (
          <AutoHeaders
            id={id}
            request={request}
            readOnly={readOnly}
            onSettings={(settings) => edit({ settings }, { history: "separate" })}
            onOverride={(key, value) => setHeaderRows([...headerRows, createKeyValueRow({ key, value })])}
          />
        )}
        {subTab === "headers" && <InheritedHeaders id={id} own={request.headers} />}
        {subTab === "headers" && (
          <KeyValueTable
            label={t("editor.request.headers")}
            bulkEdit
            rows={headerRows}
            onChange={readOnly ? undefined : setHeaderRows}
            readOnly={readOnly}
            placeholders={{ key: t("editor.request.placeholder.header") }}
            suggest={HEADER_SUGGEST}
            suggestLabels={HEADER_SUGGEST_LABELS}
          />
        )}
        {subTab === "body" && (
          <BodyEditor id={id} body={request.body} examples={request.bodyExamples ?? []} readOnly={readOnly} />
        )}
        {subTab === "auth" && <AuthEditor id={id} auth={request.auth} readOnly={readOnly} />}
        {subTab === "tests" && (
          <AssertionsEditor
            id={id}
            assertions={request.assertions}
            readOnly={readOnly}
            edit={edit}
            stream={stream}
            results={stream ? streamTests.tests : undefined}
          />
        )}
        {subTab === "extract" && <ExtractionsEditor id={id} extractions={request.extractions} readOnly={readOnly} edit={edit} />}
        {subTab === "examples" && <ResponseExamples id={id} examples={request.responseExamples ?? []} readOnly={readOnly} />}
        {subTab === "settings" && <RequestSettings settings={request.settings} readOnly={readOnly} edit={edit} stream={stream} />}
        {subTab === "notes" && (
          <NotesPanel value={request.description ?? ""} subject="request" readOnly={readOnly} onChange={onNotes} />
        )}
        {subTab === "variables" && <ResolutionPanel id={id} request={request} focus={resolutionFocus} />}
        {subTab === "code" && <SnippetPanel id={id} request={request} />}
      </div>
    </div>
  );
}

/**
 * Send, or Cancel while a send runs (HTTP-20); ⌘↩ sends. The arrow next to
 * it opens the Repeat dialog (HTTP-27); while a run goes on, the button is
 * Stop.
 */
function SendButton({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  const { send, state } = useHttpSend(id);
  const [repeatOpen, setRepeatOpen] = useState(false);
  const sending = state.status === "starting" || state.status === "running";
  // ⌘↩ while a send runs is taken and does nothing: the button cancels.
  useShortcut("request.send", () => {
    sendRequest(stores, id);
  });
  if (sending) {
    const run = state.run !== null;
    return (
      <button
        type="button"
        className={styles.cancel}
        title={run ? t("editor.request.stopRun") : undefined}
        onClick={() => cancelRequest(id)}
      >
        {run ? t("editor.request.stop") : t("common.cancel")}
      </button>
    );
  }
  const onStart = (options: HttpRunOptions) => {
    setRepeatOpen(false);
    repeatRequest(stores, id, options);
  };
  return (
    <div className={styles.sendGroup}>
      <button
        type="button"
        className={styles.send}
        title={t("editor.request.send.title", { shortcut: shortcutLabel("request.send") })}
        aria-keyshortcuts={ariaShortcut("request.send")}
        onClick={() => sendRequest(stores, id)}
      >
        <IconPlay size={12} />
        {t("editor.request.send")}
      </button>
      <button
        type="button"
        className={styles.sendMore}
        aria-label={t("editor.request.repeat")}
        aria-haspopup="dialog"
        title={t("editor.request.repeat.title")}
        onClick={() => setRepeatOpen(true)}
      >
        <IconChevron size={12} />
      </button>
      <RepeatDialog open={repeatOpen} initial={send.runOptions} onCancel={() => setRepeatOpen(false)} onStart={onStart} />
    </div>
  );
}

/** Connect, or Stop while the event stream runs (SSE-01); ⌘↩ connects. */
function ConnectButton({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  const { state } = useSseStream(id);
  const running = state.status === "starting" || state.status === "running";
  // ⌘↩ while the stream runs is taken and does nothing: the button stops it.
  useShortcut("request.send", () => {
    connectStream(stores, id);
  });
  if (running) {
    return (
      <button type="button" className={styles.cancel} onClick={() => stopStream(id)}>
        {t("editor.request.stop")}
      </button>
    );
  }
  return (
    <button
      type="button"
      className={styles.send}
      title={t("editor.request.connect.title", { shortcut: shortcutLabel("request.send") })}
      aria-keyshortcuts={ariaShortcut("request.send")}
      onClick={() => connectStream(stores, id)}
    >
      <IconPlay size={12} />
      {t("editor.request.connect")}
    </button>
  );
}
