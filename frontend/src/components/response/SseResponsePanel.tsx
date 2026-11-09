import { useEffect, useId, useState } from "react";
import { resumeStream, attemptText, type SseStream, type SseStreamState } from "../../commands/sseStreams";
import { useSseStream } from "../../commands/useSseStream";
import { errorTitle, type AppError } from "../../lib/errors";
import { formatDurationMs, statusTone } from "../../lib/http";
import { useStores, type RequestId } from "../../stores";
import { t, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { EmptyState } from "../common/EmptyState";
import { IconSignal, IconWarning } from "../common/icons";
import { onTabListKeyDown } from "../common/tabList";
import { formatSize } from "../messagelog/format";
import { MessageLog } from "../messagelog/MessageLog";
import { summaryLabel, summaryTone } from "./ResponseTests";
import { StreamTests, useStreamTestSummary } from "./StreamTests";
import panelStyles from "./HttpResponsePanel.module.css";
import { ResponseHeaders } from "./ResponseHeaders";
import { Meta } from "./ResponseView";
import view from "./ResponseView.module.css";
import { SseMergedView } from "./SseMergedView";
import styles from "./SseResponseView.module.css";

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/**
 * The stream in one line, announced to screen readers as it changes:
 * "Connecting…", "Connected · 3 events", "Stopped · 12 events",
 * "The server ended the stream · 2 events", or the error.
 */
export function streamStatusText(state: SseStreamState): string {
  const events = t("response.sse.events", { count: state.events });
  switch (state.status) {
    case "idle":
      return t("response.sse.notConnectedYet");
    case "starting":
      return t("response.sse.connecting");
    case "running":
      if (state.reconnect !== null) {
        return t("response.sse.status.reconnecting", { attempt: attemptText(state.reconnect.attempt, state.reconnect.maxAttempts), events });
      }
      return state.head === null ? t("response.sse.connecting") : t("response.sse.status.connected", { events });
    case "cancelled":
      return t("response.sse.status.stopped", { events });
    case "ok":
      return t("response.sse.status.ended", { events });
    default:
      return state.error !== null ? t("response.sse.status.error", { error: errorText(state.error), events }) : t("response.sse.status.failed", { events });
  }
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

export interface SseResponsePanelProps {
  id: RequestId;
}

/**
 * The response pane of an sse request (SSE-01, SSE-02): the latest stream
 * of its slot (commands/sseStreams.ts). Before the first connect, "Not
 * connected"; a stream that failed before the server answered, its error;
 * otherwise the SseResponseView, live while the stream runs.
 */
export function SseResponsePanel({ id }: SseResponsePanelProps) {
  const t = useT();
  const { stream, state } = useSseStream(id);
  let body;
  if (state.status === "idle") {
    body = (
      <EmptyState icon={<IconSignal size={40} strokeWidth={1.4} />} title={t("response.sse.emptyTitle")}>
        {t("response.sse.emptyBody")}
      </EmptyState>
    );
  } else if (state.status === "error" && state.head === null && state.events === 0) {
    body = <StreamError error={state.error} />;
  } else {
    body = <SseResponseView id={id} stream={stream} state={state} />;
  }
  return (
    <div className={styles.panel} data-response-for={id} data-stream-for={id}>
      {body}
    </div>
  );
}

function StreamError({ error }: { error: AppError | null }) {
  const t = useT();
  const title = error === null ? t("response.sse.failed") : error.kind === "timeout" ? t("response.sse.timedOut") : errorTitle(error.kind);
  return (
    <div className={panelStyles.error} role="alert">
      <p className={panelStyles.errorTitle}>
        <IconWarning size={14} />
        {title}
      </p>
      <p className={panelStyles.errorMessage} data-stream-status="">
        {error?.message ?? t("response.sse.endedEarly")}
      </p>
      {error !== null && error.hint !== "" && <p className={panelStyles.errorHint}>{error.hint}</p>}
    </div>
  );
}

type StreamTab = "events" | "merged" | "headers" | "tests";

/**
 * A stream, in the HTTP response view's layout: the status line (status,
 * time, events and their size, last event ID, retry, reconnecting, Stop
 * while connected, Resume after), then the tabs Events, Merged and
 * Headers. Events is the stream's message log: every event with its event
 * type as the badge and its id, retry and data (pretty JSON in the detail
 * pane when it parses), the comments and retry fields, and the
 * connections' opens, reconnects and end; it is virtualized and follows
 * the stream live. Merged joins the events' data into one text (SSE-04,
 * SseMergedView).
 */
export function SseResponseView({ id, stream, state }: { id: RequestId; stream: SseStream; state: SseStreamState }) {
  const t = useT();
  const [tab, setTab] = useState<StreamTab>("events");
  const baseId = useId();
  const headers = state.head?.headers ?? [];
  const tabs: { id: StreamTab; label: MessageKey; count: number; countLabel?: string }[] = [
    { id: "events", label: "response.sse.tab.events", count: state.events, countLabel: t("response.sse.events", { count: state.events }) },
    { id: "merged", label: "response.sse.tab.merged", count: 0 },
    { id: "headers", label: "response.sse.tab.headers", count: headers.length, countLabel: t("response.tab.headersCount", { count: headers.length }) },
    { id: "tests", label: "response.streamTests.tab", count: 0 },
  ];
  const testSummary = useStreamTestSummary(id);
  const fills = tab === "events" || tab === "merged";
  return (
    <div className={view.view}>
      <StatusLine id={id} stream={stream} state={state} />
      <div className={view.tabs} role="tablist" aria-label={t("response.sse.tabs")} onKeyDown={onTabListKeyDown}>
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
            {t(info.label)}
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
      <div
        className={fills ? `${view.section} ${styles.events}` : view.section}
        role="tabpanel"
        id={`${baseId}-panel`}
        aria-labelledby={`${baseId}-tab-${tab}`}
      >
        {tab === "events" && <MessageLog source={stream.log} label={t("response.sse.log")} className={styles.log} />}
        {tab === "merged" && <SseMergedView stream={stream} />}
        {tab === "headers" &&
          (state.head === null ? <p className={view.empty}>{t("response.sse.noHeadersYet")}</p> : <ResponseHeaders headers={[...headers]} />)}
        {tab === "tests" && <StreamTests id={id} />}
      </div>
    </div>
  );
}

/** The status line's head: the HTTP status while it is known, else the connection state. */
function statusHead(state: SseStreamState): { text: string; tone: string } {
  const head = state.head;
  if (head === null) {
    if (state.status === "cancelled") return { text: t("response.sse.phase.stopped"), tone: "other" };
    if (state.status === "error" || state.status === "ok") return { text: t("response.sse.noResponse"), tone: "other" };
    return { text: t("response.sse.connecting"), tone: "busy" };
  }
  return { text: head.statusText || String(head.status), tone: statusTone(head.status) };
}

/**
 * The status line: 200 OK · Connected · 3 s · 12 events · 1.2 KB · id 42
 * · retry 3000 ms, and Stop while the stream runs. The one-line summary
 * (streamStatusText) is announced to screen readers.
 */
function StatusLine({ id, stream, state }: { id: RequestId; stream: SseStream; state: SseStreamState }) {
  const t = useT();
  const stores = useStores();
  const running = state.status === "starting" || state.status === "running";
  const elapsed = useElapsed(state.startedAt, running);
  const head = statusHead(state);
  const time = running ? elapsed : state.elapsedMs;
  const wait = state.reconnect;
  const phase = running
    ? wait !== null
      ? t("response.sse.phase.reconnecting")
      : state.head === null
        ? ""
        : t("response.sse.phase.connected")
    : state.status === "cancelled"
      ? t("response.sse.phase.stopped")
      : state.status === "ok"
        ? t("response.sse.phase.ended")
        : t("response.sse.phase.failed");
  // The clock of useElapsed ticks while running, so the countdown follows it.
  const left = wait === null || state.startedAt === null ? 0 : Math.max(0, wait.until - (state.startedAt + elapsed));
  const canResume = !running && state.lastEventId !== "";
  return (
    <div className={view.statusLine} data-response-status="">
      <span className={view.srOnly} aria-live="polite" data-stream-status="">
        {streamStatusText(state)}
      </span>
      <span className={view.status} data-tone={head.tone} aria-hidden="true">
        <span className={running ? `${view.dot} ${styles.busy}` : view.dot} />
        {head.text}
      </span>
      {phase !== "" && (
        <Meta sans title={t("response.sse.meta.connection")}>
          {phase}
        </Meta>
      )}
      {time !== null && <Meta title={running ? t("response.sse.meta.elapsed") : t("response.sse.meta.total")}>{formatDurationMs(time)}</Meta>}
      <Meta title={t("response.sse.meta.events")}>{t("response.sse.events", { count: state.events })}</Meta>
      <Meta title={t("response.sse.meta.dataBytes", { count: state.dataBytes })}>{formatSize(state.dataBytes)}</Meta>
      {state.lastEventId !== "" && (
        <Meta quiet title={t("response.sse.meta.lastIdTitle")}>
          {t("response.sse.meta.lastId", { id: state.lastEventId })}
        </Meta>
      )}
      {state.retryMs !== null && (
        <Meta quiet title={t("response.sse.meta.retryTitle")}>
          {t("response.sse.meta.retry", { ms: state.retryMs })}
        </Meta>
      )}
      {wait !== null && (
        <Meta sans title={t("response.sse.meta.waitTitle", { attempt: attemptText(wait.attempt, wait.maxAttempts), ms: wait.delayMs })}>
          <span data-reconnect-wait="">
            {t("response.sse.meta.wait", { left: formatDurationMs(left), attempt: attemptText(wait.attempt, wait.maxAttempts) })}
          </span>
        </Meta>
      )}
      {state.reconnects > 0 && (
        <Meta quiet title={t("response.sse.meta.reconnectsTitle")}>
          {t("response.sse.meta.reconnects", { count: state.reconnects })}
        </Meta>
      )}
      <span className={view.statusEnd}>
        {running && (
          <button type="button" className={styles.stop} onClick={() => stream.stop()}>
            {t("response.sse.stop")}
          </button>
        )}
        {canResume && (
          <button
            type="button"
            className={styles.stop}
            title={t("response.sse.resumeTitle", { id: state.lastEventId })}
            onClick={() => resumeStream(stores, id)}
          >
            {t("response.sse.resume")}
          </button>
        )}
      </span>
    </div>
  );
}
