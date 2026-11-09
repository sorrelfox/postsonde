import { forwardRef, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { openSearchPanel } from "@codemirror/search";
import type { GrpcCall, GrpcCallState } from "../../commands/grpcCalls";
import { useGrpcCall } from "../../commands/useGrpcCall";
import { errorTitle, type AppError } from "../../lib/errors";
import { methodKindLabel, statusText, type MetadataDTO } from "../../lib/grpc";
import { formatDurationMs } from "../../lib/http";
import type { RequestDTO } from "../../lib/collection";
import { useStores, type RequestId } from "../../stores";
import { t } from "../../i18n";
import { useT } from "../../i18n/react";
import { EmptyState } from "../common/EmptyState";
import { IconCheck, IconCopy, IconSearch, IconSignal, IconWarning } from "../common/icons";
import { onTabListKeyDown } from "../common/tabList";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import { formatSize } from "../messagelog/format";
import { MessageLog } from "../messagelog/MessageLog";
import { GrpcStatusPanel } from "./GrpcStatusPanel";
import { GrpcStreamControls } from "./GrpcStreamControls";
import panelStyles from "./HttpResponsePanel.module.css";
import { summaryLabel, summaryTone } from "./ResponseTests";
import { Meta, Segmented } from "./ResponseView";
import { StreamTests, useStreamTestSummary } from "./StreamTests";
import view from "./ResponseView.module.css";
import styles from "./GrpcResponseView.module.css";

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** How many google.rpc.Status details the call's status carries. */
function detailCount(state: GrpcCallState): number {
  return state.grpcStatus?.rich?.details?.length ?? 0;
}

/**
 * The call in one line, announced to screen readers as it changes: "OK ·
 * 3 messages · 12 ms", "NOT_FOUND: no such order · 0 messages · 4 ms",
 * "Calling pkg.Service/Method… 2 messages received".
 */
export function callStatusText(state: GrpcCallState): string {
  switch (state.status) {
    case "idle":
      return t("response.grpc.notCalled");
    case "starting":
      return t("response.grpc.calling", { method: state.method });
    case "running": {
      const parts: string[] = [];
      if (state.clientStreaming) parts.push(t("response.grpc.sent", { n: state.sent }));
      if (state.messages > 0) parts.push(t("response.grpc.received", { count: state.messages }));
      if (state.halfClosed) parts.push(t("response.grpc.halfClosed"));
      return parts.length > 0
        ? t("response.grpc.callingParts", { method: state.method, parts: parts.join(", ") })
        : t("response.grpc.calling", { method: state.method });
    }
    default: {
      const parts: string[] = [];
      if (state.grpcStatus !== null) {
        parts.push(statusText(state.grpcStatus));
        const details = detailCount(state);
        if (details > 0) parts.push(t("response.grpc.errorDetails", { count: details }));
        parts.push(t("response.grpc.messages", { count: state.messages }));
        if (state.clientStreaming) parts.push(t("response.grpc.sent", { n: state.sent }));
      } else if (state.status === "cancelled") {
        parts.push(t("response.grpc.cancelled"));
      } else if (state.error !== null) {
        parts.push(errorText(state.error));
      }
      if (state.status === "cancelled" && state.grpcStatus !== null) parts[0] = t("response.grpc.cancelled");
      if (state.elapsedMs !== null && state.grpcStatus !== null) parts.push(t("response.duration.ms", { ms: state.elapsedMs }));
      return parts.join(" · ");
    }
  }
}

/** Unary and client-streaming calls end in one response message, shown as JSON (GRPC-11). */
export function singleResponse(kind: string): boolean {
  return kind === "unary" || kind === "client_stream";
}

/** Milliseconds since `start` while `running`, updated ten times a second. */
function useElapsed(start: number | null, running: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (start === null || !running) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(timer);
  }, [start, running]);
  return start === null ? 0 : Math.max(0, now - start);
}

export interface GrpcResponsePanelProps {
  id: RequestId;
}

/**
 * The response pane of a grpc request (GRPC-11, GRPC-12, GRPC-19,
 * GRPC-20): the latest call of its slot (commands/grpcCalls.ts). Before
 * the first call, "No response yet"; a call that failed before the server
 * answered, its error; otherwise the GrpcResponseView, live while the call
 * runs.
 */
export function GrpcResponsePanel({ id }: GrpcResponsePanelProps) {
  const t = useT();
  const { call, state } = useGrpcCall(id);
  const stores = useStores();
  // Send message sends what the editor holds now.
  const getMessage = () => (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined)?.message ?? "";
  let body;
  if (state.status === "idle") {
    body = (
      <EmptyState icon={<IconSignal size={40} strokeWidth={1.4} />} title={t("response.grpc.emptyTitle")}>
        {t("response.grpc.emptyBody")}
      </EmptyState>
    );
  } else if (state.status === "error" && state.grpcStatus === null && state.headers === null && state.messages === 0) {
    body = <CallError error={state.error} />;
  } else {
    body = <GrpcResponseView call={call} state={state} getMessage={getMessage} />;
  }
  return (
    <div className={styles.panel} data-response-for={id} data-call-for={id}>
      {body}
    </div>
  );
}

function CallError({ error }: { error: AppError | null }) {
  const t = useT();
  const title = error === null ? t("response.grpc.failed") : error.kind === "timeout" ? t("response.grpc.timedOut") : errorTitle(error.kind);
  return (
    <div className={panelStyles.error} role="alert">
      <p className={panelStyles.errorTitle}>
        <IconWarning size={14} />
        {title}
      </p>
      <p className={panelStyles.errorMessage} data-call-status="">
        {error?.message ?? t("response.grpc.endedEarly")}
      </p>
      {error !== null && error.hint !== "" && <p className={panelStyles.errorHint}>{error.hint}</p>}
    </div>
  );
}

type ResponseTab = "messages" | "headers" | "trailers" | "status" | "tests";
type MessagesView = "message" | "log";

/**
 * A call's response, in the HTTP response view's layout: the status line
 * (status name and code, message, time, messages received and their size,
 * call type, Cancel while running), then the tabs Messages, Headers,
 * Trailers and Status with count badges. Messages shows a unary call's
 * response as pretty JSON in a read-only editor (search, folding, copy),
 * with a switch to the call's message log; a stream shows the log, the
 * streaming timeline (GRPC-22): sent and received messages, headers,
 * half-close and status in order, virtualized and following the call live.
 * A client-streaming call opens on the timeline, with the switch to its
 * one response. While a client-streaming or bidi call runs, the status
 * line has Send message and End stream next to Cancel; `getMessage` gives
 * the message to send (no Send message without it).
 */
export function GrpcResponseView({ call, state, getMessage }: { call: GrpcCall; state: GrpcCallState; getMessage?: () => string }) {
  const t = useT();
  const [tab, setTab] = useState<ResponseTab>("messages");
  const single = singleResponse(state.kind);
  const initialView = (): MessagesView => (single && !state.clientStreaming ? "message" : "log");
  // The view is chosen per call: a new call opens in its initial view.
  const [chosen, setChosen] = useState<{ for: number | null; view: MessagesView }>(() => ({ for: state.startedAt, view: initialView() }));
  const messagesView = chosen.for === state.startedAt ? chosen.view : initialView();
  const [searchWanted, setSearchWanted] = useState(0);
  const [copied, setCopied] = useState(false);
  const editorRef = useRef<CodeEditorHandle>(null);
  const baseId = useId();
  const json = state.firstMessage?.json ?? "";
  const showsMessage = tab === "messages" && messagesView === "message" && json !== "";

  useEffect(() => {
    if (searchWanted === 0) return;
    const editor = editorRef.current?.view;
    if (editor) openSearchPanel(editor);
  }, [searchWanted]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = () => {
    if (!navigator.clipboard) return;
    navigator.clipboard.writeText(json).then(
      () => setCopied(true),
      () => setCopied(false),
    );
  };

  const headerCount = state.headers?.length ?? 0;
  const trailerCount = state.trailers?.length ?? 0;
  const details = detailCount(state);
  const tabs: { id: ResponseTab; label: string; count: number; countLabel: string }[] = [
    { id: "messages", label: t("response.grpc.tab.messages"), count: state.messages, countLabel: t("response.grpc.messages", { count: state.messages }) },
    { id: "headers", label: t("response.grpc.tab.headers"), count: headerCount, countLabel: t("response.tab.headersCount", { count: headerCount }) },
    { id: "trailers", label: t("response.grpc.tab.trailers"), count: trailerCount, countLabel: t("response.grpc.trailersCount", { count: trailerCount }) },
    { id: "status", label: t("response.grpc.tab.status"), count: details, countLabel: t("response.grpc.errorDetails", { count: details }) },
    // The results of the request's assertions (TEST-08).
    { id: "tests", label: t("response.streamTests.tab"), count: 0, countLabel: "" },
  ];
  const testSummary = useStreamTestSummary(call.requestId);

  return (
    <div className={view.view}>
      <StatusLine call={call} state={state} getMessage={getMessage}>
        {showsMessage && (
          <>
            <button
              type="button"
              className={view.iconButton}
              title={copied ? t("response.grpc.copied") : t("response.grpc.copy")}
              aria-label={copied ? t("response.grpc.copied") : t("response.grpc.copy")}
              onClick={copy}
            >
              {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
            </button>
            <button
              type="button"
              className={view.iconButton}
              title={t("response.grpc.searchTitle")}
              aria-label={t("response.grpc.search")}
              onClick={() => setSearchWanted((n) => n + 1)}
            >
              <IconSearch size={13} />
            </button>
          </>
        )}
      </StatusLine>

      <div className={view.tabs}>
        <div className={view.tabList} role="tablist" aria-label={t("response.grpc.tabs")} onKeyDown={onTabListKeyDown}>
          {tabs.map((info) => (
            <button
              key={info.id}
              type="button"
              role="tab"
              id={`${baseId}-tab-${info.id}`}
              className={view.tab}
              aria-selected={info.id === tab}
              aria-controls={info.id === tab ? `${baseId}-panel` : undefined}
              tabIndex={info.id === tab ? 0 : -1}
              onClick={() => setTab(info.id)}
            >
              {info.label}
              {info.count > 0 && (
                <span className={view.count} aria-label={info.countLabel}>
                  {info.count}
                </span>
              )}
              {info.id === "tests" && testSummary !== null && testSummary.total > 0 && (
                <span className={view.count} data-tone={summaryTone(testSummary)} aria-label={summaryLabel(testSummary)} data-tests-badge="">
                  {testSummary.passed}/{testSummary.total}
                </span>
              )}
            </button>
          ))}
        </div>
        {tab === "messages" && single && (
          <div className={view.tools}>
            <Segmented<MessagesView>
              label={t("response.grpc.messagesView")}
              value={messagesView}
              options={[
                { value: "message", label: t("response.grpc.view.response") },
                { value: "log", label: t("response.grpc.view.log") },
              ]}
              onChange={(v) => setChosen({ for: state.startedAt, view: v })}
            />
          </div>
        )}
      </div>

      <div
        className={tab === "messages" ? `${view.section} ${styles.messages}` : view.section}
        role="tabpanel"
        id={`${baseId}-panel`}
        aria-labelledby={`${baseId}-tab-${tab}`}
      >
        {tab === "messages" && messagesView === "log" && (
          <MessageLog source={call.log} label={t("response.grpc.log")} className={styles.log} initialBinaryView="base64" />
        )}
        {tab === "messages" && messagesView === "message" && <ResponseMessage ref={editorRef} state={state} />}
        {tab === "headers" && (
          <MetadataTable metadata={state.headers} label={t("response.grpc.responseHeaders")} empty={metadataEmpty(state, "headers")} />
        )}
        {tab === "trailers" && <MetadataTable metadata={state.trailers} label={t("response.grpc.trailers")} empty={metadataEmpty(state, "trailers")} />}
        {tab === "status" && <GrpcStatusPanel status={state.grpcStatus} />}
        {tab === "tests" && <StreamTests id={call.requestId} />}
      </div>
    </div>
  );
}

function metadataEmpty(state: GrpcCallState, which: "headers" | "trailers"): string {
  const running = state.status === "starting" || state.status === "running";
  if (which === "headers") return running && state.headers === null ? t("response.grpc.noHeadersYet") : t("response.grpc.noHeaders");
  return running && state.trailers === null ? t("response.grpc.trailersAtEnd") : t("response.grpc.noTrailers");
}

/** The status name as the status line shows it, and its tone (ResponseView's status colours). */
function statusHead(state: GrpcCallState): { text: string; tone: string } {
  if (state.status === "starting" || state.status === "running") return { text: t("response.grpc.state.calling"), tone: "busy" };
  if (state.status === "cancelled") return { text: t("response.grpc.cancelled"), tone: "client" };
  const status = state.grpcStatus;
  if (status === null) {
    return state.status === "ok" ? { text: t("response.grpc.state.ended"), tone: "other" } : { text: t("response.grpc.state.failed"), tone: "server" };
  }
  return { text: status.name, tone: status.code === 0 ? "ok" : "server" };
}

/**
 * The status line: OK 0 · 12 ms · 3 messages · 1.2 KB · unary ·
 * pkg.Service/Method, with the status message after a failing code, and
 * Cancel while the call runs. The one-line summary (callStatusText) is
 * announced to screen readers.
 */
function StatusLine({ call, state, getMessage, children }: { call: GrpcCall; state: GrpcCallState; getMessage?: () => string; children?: ReactNode }) {
  const t = useT();
  const running = state.status === "starting" || state.status === "running";
  const elapsed = useElapsed(state.startedAt, running);
  const head = statusHead(state);
  const status = state.grpcStatus;
  const time = running ? elapsed : state.elapsedMs;
  return (
    <div className={view.statusLine} data-response-status="">
      <span className={view.srOnly} aria-live="polite" data-call-status="">
        {callStatusText(state)}
      </span>
      <span className={view.status} data-tone={head.tone} data-call-tone={head.tone} aria-hidden="true">
        <span className={running ? `${view.dot} ${styles.busy}` : view.dot} />
        {head.text}
        {status !== null && state.status !== "cancelled" && <span className={styles.code}>{status.code}</span>}
      </span>
      {status !== null && status.message !== "" && (
        <span className={styles.message} title={status.message}>
          {status.message}
        </span>
      )}
      {time !== null && <Meta title={running ? t("response.grpc.meta.elapsed") : t("response.grpc.meta.total")}>{formatDurationMs(time)}</Meta>}
      {state.clientStreaming && (
        <Meta title={t("response.grpc.meta.sentBytes", { count: state.sentBytes })}>{t("response.grpc.sent", { n: state.sent })}</Meta>
      )}
      <Meta title={t("response.grpc.meta.received")}>{t("response.grpc.messages", { count: state.messages })}</Meta>
      <Meta title={t("response.grpc.meta.receivedBytes", { count: state.receivedBytes })}>{formatSize(state.receivedBytes)}</Meta>
      {state.kind !== "" && (
        <Meta quiet sans title={t("response.grpc.meta.kind")}>
          {methodKindLabel(state.kind)}
        </Meta>
      )}
      <Meta quiet sans title={state.method}>
        {state.method.slice(state.method.lastIndexOf("/") + 1)}
      </Meta>
      <span className={view.statusEnd}>
        {children}
        {running && getMessage !== undefined && (
          <GrpcStreamControls call={call} state={state} getMessage={getMessage} sendClassName={styles.cancel} endClassName={styles.cancel} />
        )}
        {running && (
          <button type="button" className={styles.cancel} onClick={() => call.cancel()}>
            {t("common.cancel")}
          </button>
        )}
      </span>
    </div>
  );
}

/** A unary call's response message as pretty JSON, or why there is none. */
const ResponseMessage = forwardRef<CodeEditorHandle, { state: GrpcCallState }>(function ResponseMessage({ state }, ref) {
  const t = useT();
  const message = state.firstMessage;
  const running = state.status === "starting" || state.status === "running";
  if (message === null) {
    let text = t("response.grpc.noMessage");
    if (running) text = t("response.grpc.waiting");
    else if (state.grpcStatus !== null && state.grpcStatus.code !== 0) text = t("response.grpc.noMessageStatus");
    return <p className={styles.empty}>{text}</p>;
  }
  const extra = state.messages > 1 ? t("response.grpc.firstOfMany", { count: state.messages }) : "";
  if (message.json === "") {
    return (
      <div className={view.bodyPane}>
        <div className={view.bodyNotes}>
          <p className={view.bodyNote}>{t("response.grpc.base64Note", { error: message.decodeError })}</p>
          {extra !== "" && <p className={view.bodyNote}>{extra}</p>}
        </div>
        <pre className={styles.raw}>
          {message.raw}
          <span className={styles.binaryTag}>Base64</span>
        </pre>
      </div>
    );
  }
  return (
    <div className={view.bodyPane}>
      {extra !== "" && (
        <div className={view.bodyNotes}>
          <p className={view.bodyNote}>{extra}</p>
        </div>
      )}
      <CodeEditor ref={ref} className={view.bodyEditor} value={message.json} readOnly language="json" ariaLabel={t("response.grpc.responseMessage")} />
    </div>
  );
});

/**
 * Response headers or trailers (GRPC-19) as a two-column table in the
 * HTTP Headers tab's style, in the order received; binary ("-bin") values
 * are Base64 and marked so.
 */
function MetadataTable({ metadata, label, empty }: { metadata: readonly MetadataDTO[] | null; label: string; empty: string }) {
  const t = useT();
  if (metadata === null || metadata.length === 0) return <p className={view.empty}>{empty}</p>;
  return (
    <div className={view.headersBox}>
      <table className={view.headers} aria-label={label}>
        <thead className={view.srOnly}>
          <tr>
            <th scope="col">{t("common.keyValue.key")}</th>
            <th scope="col">{t("common.keyValue.value")}</th>
          </tr>
        </thead>
        <tbody>
          {metadata.map((e, i) => (
            <tr key={i}>
              <th scope="row" className={view.headerName}>
                {e.key}
              </th>
              <td className={view.headerValue}>
                {e.value}
                {e.binary && <span className={styles.binaryTag}>Base64</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
