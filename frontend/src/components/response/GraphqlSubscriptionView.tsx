import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { jumpToQueryLocation } from "../../commands/graphqlJump";
import { startSubscription, type GraphqlSubscription, type SubscriptionState } from "../../commands/graphqlSubscriptions";
import { closeText } from "../../lib/graphqlSubscription";
import { readGraphqlPayload, type QueryLocation } from "../../lib/graphqlResponse";
import { formatDurationMs } from "../../lib/http";
import type { LogMessage } from "../../lib/messageSource";
import { shortcutLabel } from "../../lib/shortcuts";
import { useStores, type RequestId } from "../../stores";
import { t } from "../../i18n";
import { useT } from "../../i18n/react";
import { EmptyState } from "../common/EmptyState";
import { IconPlay, IconStop } from "../common/icons";
import { onTabListKeyDown } from "../common/tabList";
import { formatSize } from "../messagelog/format";
import { MessageLog, type KindToggle } from "../messagelog/MessageLog";
import { GraphqlBody } from "./GraphqlBody";
import styles from "./GrpcResponseView.module.css";
import { Meta } from "./ResponseView";
import view from "./ResponseView.module.css";

/** Whether the slot ran a query or mutation (GQL-35), which ends with one result, not a subscription. */
export function isSingleResult(state: SubscriptionState): boolean {
  return state.operationType === "query" || state.operationType === "mutation";
}

/** The status name of a subscription and its tone (ResponseView's status colours). */
export function subscriptionHead(state: SubscriptionState): { text: string; tone: string } {
  const single = isSingleResult(state);
  switch (state.status) {
    case "idle":
      return { text: t("response.subscription.idle"), tone: "other" };
    case "starting":
      return { text: t("response.subscription.connecting"), tone: "busy" };
    case "running":
      if (!state.connected) return { text: t("response.subscription.connecting"), tone: "busy" };
      return single ? { text: t("response.subscription.waiting"), tone: "busy" } : { text: t("response.subscription.subscribed"), tone: "ok" };
    case "ok":
      return { text: t("response.subscription.completed"), tone: "ok" };
    case "cancelled":
      return { text: t("response.subscription.stopped"), tone: "client" };
    default:
      return { text: t("response.subscription.failed"), tone: "server" };
  }
}

/** How the transport is named in the status line. */
function transportName(transport: string): string {
  switch (transport) {
    case "graphql-ws":
      return t("response.subscription.legacyWebSocket");
    case "sse":
      return "SSE";
    case "":
      return "";
    default:
      return "WebSocket";
  }
}

/**
 * The subscription in one line, announced to screen readers as it
 * changes: "Subscribed · 3 events", "Completed · 5 events · 1.2 s",
 * "Failed: The server closed … · 0 events".
 */
export function subscriptionStatusText(state: SubscriptionState): string {
  const head = subscriptionHead(state);
  const parts = [head.text];
  if (state.status === "error" && state.error !== null) parts[0] = t("response.subscription.failedWith", { message: state.error.message });
  if (state.status !== "starting") parts.push(t("response.sse.events", { count: state.events }));
  if (state.errors > 0) parts.push(t("response.subscription.withErrors", { count: state.errors }));
  if (state.elapsedMs !== null) parts.push(formatDurationMs(state.elapsedMs));
  return parts.join(" · ");
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

/**
 * The GraphQL result of a log message, for the detail pane: a received
 * `next` or `data` payload (or an SSE `response`), or an `error` payload
 * (an array, or one error object in the legacy protocol) read as
 * `{"errors": …}`; null for anything else, which the log shows as JSON or
 * text.
 */
export function graphqlPayloadOf(message: LogMessage): ReturnType<typeof readGraphqlPayload> {
  if (message.direction !== "received" || message.text === undefined || message.text === "") return null;
  if (message.kind === "next" || message.kind === "data" || message.kind === "response") return readGraphqlPayload(message.text);
  if (message.kind === "error") {
    const text = message.text.trim();
    return readGraphqlPayload(text.startsWith("{") ? `{"errors":[${text}]}` : `{"errors":${text}}`);
  }
  return null;
}

/** The kinds of keep-alive messages (ping, pong, ka, SSE comments), which the log's toggle hides. */
const KEEP_ALIVE_KINDS: readonly string[] = ["ping", "pong", "ka", "keep-alive"];

type SingleTab = "response" | "messages";

export interface GraphqlSubscriptionViewProps {
  id: RequestId;
  subscription: GraphqlSubscription;
  state: SubscriptionState;
}

/**
 * A GraphQL request's subscription in the response pane (GQL-34): the
 * status line (state, time, events, errors, bytes, how the connection
 * closed; Stop while it runs, Subscribe again after) above the live event
 * log, a MessageLog of every protocol message with timestamps. A `next`
 * or `error` message opens in the detail pane as the GraphQL response
 * view does: its data and errors marked, the errors listed with their
 * locations jumping to the query (GraphqlBody).
 *
 * A query or mutation run over WebSocket (GQL-35) shows its result as a
 * GraphQL response instead, with the messages on a second tab.
 */
export function GraphqlSubscriptionView({ id, subscription, state }: GraphqlSubscriptionViewProps) {
  const t = useT();
  const stores = useStores();
  const running = state.status === "starting" || state.status === "running";
  const elapsed = useElapsed(state.startedAt, running);
  const time = running ? elapsed : state.elapsedMs;
  const head = subscriptionHead(state);
  const single = isSingleResult(state);
  const [tab, setTab] = useState<SingleTab>("response");
  const baseId = useId();
  const onLocation = useCallback((location: QueryLocation) => jumpToQueryLocation(stores, id, location), [stores, id]);
  const renderPayload = useCallback(
    (message: LogMessage) => {
      const graphql = graphqlPayloadOf(message);
      return graphql === null ? null : <GraphqlBody graphql={graphql} hideExtensions={false} onLocation={onLocation} />;
    },
    [onLocation],
  );
  const result = useMemo(() => (state.result === null ? null : readGraphqlPayload(state.result)), [state.result]);
  const transport = transportName(state.transport);
  const again = single ? t("response.subscription.sendAgain") : t("response.subscription.subscribeAgain");
  const kindToggles = useMemo<readonly KindToggle[]>(
    () => [{ label: t("response.subscription.keepAlives"), kinds: KEEP_ALIVE_KINDS, title: t("response.subscription.keepAlivesTitle") }],
    [t],
  );

  const log = (
    <MessageLog
      source={subscription.log}
      label={single ? t("response.subscription.messages") : t("response.subscription.log")}
      className={styles.log}
      renderPayload={renderPayload}
      kindToggles={kindToggles}
    />
  );

  return (
    <div className={view.view} data-subscription-for={id}>
      <div className={view.statusLine} data-response-status="">
        <span className={view.srOnly} aria-live="polite" data-subscription-status="">
          {subscriptionStatusText(state)}
        </span>
        <span className={view.status} data-tone={head.tone} aria-hidden="true">
          <span className={head.tone === "busy" || (running && state.connected) ? `${view.dot} ${styles.busy}` : view.dot} />
          {head.text}
        </span>
        {state.status === "error" && state.error !== null && (
          <span className={styles.message} title={state.error.hint !== "" ? `${state.error.message} ${state.error.hint}` : state.error.message}>
            {state.error.message}
          </span>
        )}
        {time !== null && (
          <Meta title={running ? t("response.subscription.meta.elapsed") : t("response.subscription.meta.total")}>{formatDurationMs(time)}</Meta>
        )}
        {transport !== "" && (
          <Meta quiet title={t("response.subscription.meta.transport", { transport: state.transport })}>
            {transport}
          </Meta>
        )}
        <Meta title={t("response.subscription.meta.events")}>{t("response.sse.events", { count: state.events })}</Meta>
        {state.errors > 0 && (
          <Meta title={t("response.subscription.meta.errors")}>{t("response.subscription.withErrors", { count: state.errors })}</Meta>
        )}
        <Meta title={t("response.subscription.meta.bytes", { count: state.receivedBytes })}>{formatSize(state.receivedBytes)}</Meta>
        {state.close !== null && !running && (
          <Meta quiet title={t("response.subscription.meta.close")}>
            {closeText(state.close)}
          </Meta>
        )}
        <span className={view.statusEnd}>
          {running ? (
            <button
              type="button"
              className={styles.cancel}
              onClick={() => subscription.stop()}
              title={t("response.subscription.stopTitle", { shortcut: shortcutLabel("request.send") })}
            >
              <IconStop size={10} /> {single ? t("common.cancel") : t("response.subscription.stop")}
            </button>
          ) : (
            <button type="button" className={styles.cancel} onClick={() => startSubscription(stores, id)} title={t("response.subscription.againTitle")}>
              <IconPlay size={10} /> {again}
            </button>
          )}
        </span>
      </div>
      {single ? (
        <>
          <div className={view.tabs} role="tablist" aria-label={t("response.sections")} onKeyDown={onTabListKeyDown}>
            {(["response", "messages"] as const).map((which) => (
              <button
                key={which}
                type="button"
                role="tab"
                id={`${baseId}-tab-${which}`}
                className={view.tab}
                aria-selected={which === tab}
                aria-controls={which === tab ? `${baseId}-panel` : undefined}
                tabIndex={which === tab ? 0 : -1}
                onClick={() => setTab(which)}
              >
                {which === "response" ? t("response.subscription.tab.response") : t("response.subscription.tab.messages")}
              </button>
            ))}
          </div>
          <div id={`${baseId}-panel`} role="tabpanel" aria-labelledby={`${baseId}-tab-${tab}`} className={tab === "response" ? view.section : `${view.section} ${styles.messages}`}>
            {tab === "messages" ? (
              log
            ) : result !== null ? (
              <GraphqlBody graphql={result} hideExtensions={false} onLocation={onLocation} />
            ) : (
              <EmptyState title={running ? t("response.subscription.waitingTitle") : t("response.subscription.noResult")}>
                {running ? t("response.subscription.waitingBody") : t("response.subscription.noResultBody")}
              </EmptyState>
            )}
          </div>
        </>
      ) : (
        <div className={`${view.section} ${styles.messages}`}>{log}</div>
      )}
    </div>
  );
}
