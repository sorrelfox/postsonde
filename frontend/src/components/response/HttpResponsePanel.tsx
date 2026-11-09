import { useCallback, useEffect, useMemo, useState } from "react";
import { jumpToAssertion, TESTS_SUB_TAB } from "../../commands/assertionJump";
import { jumpToExtraction } from "../../commands/extractionJump";
import { addQuickAssertion, type QuickAssertion } from "../../commands/quickAssert";
import { jumpToQueryLocation } from "../../commands/graphqlJump";
import type { HttpSendState } from "../../commands/httpSends";
import { cancelRequest } from "../../commands/httpSends";
import { useGraphqlSubscription } from "../../commands/useGraphqlSubscription";
import { useHttpSend } from "../../commands/useHttpSend";
import { errorTitle } from "../../lib/errors";
import { formatDurationMs, type ResponseDTO } from "../../lib/http";
import { shortcutLabel } from "../../lib/shortcuts";
import type { QueryLocation } from "../../lib/graphqlResponse";
import { useCollection, useStores, type RequestId } from "../../stores";
import { useT } from "../../i18n/react";
import { EmptyState } from "../common/EmptyState";
import { IconSignal, IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { GraphqlSubscriptionView } from "./GraphqlSubscriptionView";
import styles from "./HttpResponsePanel.module.css";
import type { FilterPersistence } from "./ResponseFilter";
import { ResponseSource } from "./ResponseSourceBar";
import { ResponseView } from "./ResponseView";
import { RunResults } from "./RunResults";

export interface HttpResponsePanelProps {
  id: RequestId;
  /** The request is a GraphQL request: its response gets the GraphQL view (GQL-05). */
  graphql?: boolean;
}

/**
 * The response pane of an HTTP request (HTTP-20): the latest send's
 * progress with Cancel, its error (a timeout says which timeout stopped
 * it, HTTP-21), or the response in a ResponseView (HTTP-30…33). For a
 * GraphQL request the view gets its GraphQL mode, whose error locations
 * jump to the query (GQL-05, commands/graphqlJump.ts). A run of repeated
 * sends (HTTP-27) shows its results above its latest response (RunResults).
 * Above it, ResponseSource offers the request's earlier responses and its
 * response examples, and saves the response shown as an example (HTTP-38,
 * HTTP-39).
 */
export function HttpResponsePanel({ id, graphql = false }: HttpResponsePanelProps) {
  if (graphql) return <GraphqlPanel id={id} />;
  return <RestPanel id={id} />;
}

function RestPanel({ id }: { id: RequestId }) {
  const { state } = useHttpSend(id);
  const renderStored = useCallback((response: ResponseDTO) => <RestResponseView id={id} response={response} stored />, [id]);
  return (
    <div className={styles.panel} data-response-for={id}>
      <ResponseSource id={id} send={state} renderStored={renderStored}>
        <PanelBody id={id} state={state} graphql={false} />
      </ResponseSource>
    </div>
  );
}

/**
 * A GraphQL request's pane: the latest of its HTTP send and its
 * subscription (GQL-34), whichever started last, so running a query
 * after a subscription shows the query's response and subscribing again
 * shows the live log.
 */
function GraphqlPanel({ id }: { id: RequestId }) {
  const { state } = useHttpSend(id);
  const { subscription, state: sub } = useGraphqlSubscription(id);
  const showSubscription = sub.startedAt !== null && (state.startedAt === null || sub.startedAt > state.startedAt);
  const renderStored = useCallback((response: ResponseDTO) => <GraphqlResponseView id={id} response={response} stored />, [id]);
  return (
    <div className={styles.panel} data-response-for={id}>
      {showSubscription ? (
        <GraphqlSubscriptionView id={id} subscription={subscription} state={sub} />
      ) : (
        <ResponseSource id={id} send={state} renderStored={renderStored}>
          <PanelBody id={id} state={state} graphql />
        </ResponseSource>
      )}
    </div>
  );
}

function PanelBody({ id, state, graphql }: { id: RequestId; state: HttpSendState; graphql: boolean }) {
  const t = useT();
  if (state.run !== null) {
    // A run (HTTP-27): its results, and the latest response it carried.
    const response =
      state.response === null ? null : graphql ? (
        <GraphqlResponseView id={id} response={state.response} />
      ) : (
        <RestResponseView id={id} response={state.response} />
      );
    return <RunResults id={id} state={state} run={state.run} response={response} />;
  }
  const incremental = graphql && state.response?.incremental != null;
  if (incremental && state.response !== null && (state.status === "running" || state.status === "ok")) {
    // An incremental response (GQL-08) is shown while its parts arrive,
    // with Cancel above it. Both states render the same tree, so the view
    // keeps its switches when the final response replaces the partial ones.
    return (
      <>
        {state.status === "running" && <Receiving id={id} state={state} />}
        <GraphqlResponseView id={id} response={state.response} />
      </>
    );
  }
  switch (state.status) {
    case "idle":
      return (
        <EmptyState icon={<IconSignal size={40} strokeWidth={1.4} />} title={t("response.http.emptyTitle")}>
          {t("response.http.emptyBody", { shortcut: shortcutLabel("request.send") })}
        </EmptyState>
      );
    case "starting":
    case "running":
      return <Running id={id} state={state} />;
    case "cancelled":
      return (
        <EmptyState title={t("response.http.cancelledTitle")}>
          {t("response.http.cancelledBody", { shortcut: shortcutLabel("request.send") })}
        </EmptyState>
      );
    case "error":
      return <SendError state={state} />;
    default:
      if (state.response === null) return <SendError state={state} />;
      return graphql ? <GraphqlResponseView id={id} response={state.response} /> : <RestResponseView id={id} response={state.response} />;
  }
}

/** Where the request's body filter is remembered (HTTP-36): per request, in the open workspace. */
function useFilterPersistence(id: RequestId): FilterPersistence {
  const workspaceId = useCollection((s) => s.workspaceId);
  return useMemo(() => ({ workspaceId, requestId: id }), [workspaceId, id]);
}

/**
 * The Tests and Variables tabs of the request's response (TEST-06,
 * TEST-05): results jump to the request's Tests or Extract sub-tab.
 */
function useTestsJump(id: RequestId): {
  onJump: (index: number | null) => void;
  onExtractionJump: (index: number) => void;
  onQuickAssert?: (a: QuickAssertion) => void;
} {
  const stores = useStores();
  // A read-only request takes no new assertions (TEST-04).
  const readOnly = useCollection((s) => (s.nodes[id]?.readOnly ?? "") !== "" || s.readOnly !== "");
  return useMemo(
    () => ({
      onJump: (index: number | null) => {
        if (index === null) stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: TESTS_SUB_TAB });
        else jumpToAssertion(stores, id, index);
      },
      onExtractionJump: (index: number) => jumpToExtraction(stores, id, index),
      onQuickAssert: readOnly ? undefined : (a: QuickAssertion) => void addQuickAssertion(stores, id, a),
    }),
    [stores, id, readOnly],
  );
}

/** The props of a request's response view; `stored` for an earlier response or an example (HTTP-38, HTTP-39), which has no test results. */
interface RequestResponseProps {
  id: RequestId;
  response: NonNullable<HttpSendState["response"]>;
  stored?: boolean;
}

function RestResponseView({ id, response, stored = false }: RequestResponseProps) {
  const filter = useFilterPersistence(id);
  const tests = useTestsJump(id);
  const name = useCollection((s) => s.nodes[id]?.name ?? "");
  return <ResponseView response={response} filter={filter} tests={stored ? undefined : tests} name={name} />;
}

/** A GraphQL request's response: error locations jump to its Query editor (commands/graphqlJump.ts). */
function GraphqlResponseView({ id, response, stored = false }: RequestResponseProps) {
  const stores = useStores();
  const onLocation = useCallback((location: QueryLocation) => jumpToQueryLocation(stores, id, location), [stores, id]);
  const graphql = useMemo(() => ({ onLocation }), [onLocation]);
  const filter = useFilterPersistence(id);
  const tests = useTestsJump(id);
  const name = useCollection((s) => s.nodes[id]?.name ?? "");
  return <ResponseView response={response} graphql={graphql} filter={filter} tests={stored ? undefined : tests} name={name} />;
}

/** Milliseconds since `start`, updated ten times a second. */
function useElapsed(start: number | null): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (start === null) return;
    const timer = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(timer);
  }, [start]);
  return start === null ? 0 : Math.max(0, now - start);
}

function Running({ id, state }: { id: RequestId; state: HttpSendState }) {
  const t = useT();
  const elapsed = useElapsed(state.startedAt);
  return (
    <div className={styles.running} role="status" aria-live="polite">
      <span className={styles.spinner} aria-hidden="true" />
      <p className={styles.stage}>{state.stage !== "" ? t("response.http.stage", { stage: state.stage }) : t("response.http.sending")}</p>
      <Received received={state.received} total={state.total} />
      <p className={styles.elapsed}>{formatDurationMs(elapsed)}</p>
      <button type="button" className={styles.cancel} onClick={() => cancelRequest(id)}>
        {t("common.cancel")}
      </button>
    </div>
  );
}

/** The strip above an incremental response while its parts arrive (GQL-08): the time so far and Cancel. */
function Receiving({ id, state }: { id: RequestId; state: HttpSendState }) {
  const t = useT();
  const elapsed = useElapsed(state.startedAt);
  return (
    <div className={styles.receiving} role="status" aria-live="polite">
      <span className={styles.spinner} aria-hidden="true" />
      <span>{t("response.http.receivingParts")}</span>
      <span className={styles.elapsed}>{formatDurationMs(elapsed)}</span>
      <button type="button" className={styles.cancel} onClick={() => cancelRequest(id)}>
        {t("common.cancel")}
      </button>
    </div>
  );
}

/**
 * How much of the body has arrived (HTTP-37): "12.4 MB of 50 MB" with a
 * progress bar when the server announced the size, "12.4 MB received"
 * otherwise. Nothing before the first byte.
 */
function Received({ received, total }: { received: number; total: number }) {
  const t = useT();
  if (received <= 0 && total <= 0) return null;
  if (total > 0) {
    return (
      <div className={styles.received}>
        <progress className={styles.progress} max={total} value={Math.min(received, total)} aria-label={t("response.http.progress")} />
        <p className={styles.bytes}>{t("response.http.receivedOf", { received: formatSize(received), total: formatSize(total) })}</p>
      </div>
    );
  }
  return <p className={styles.bytes}>{t("response.http.received", { received: formatSize(received) })}</p>;
}

function SendError({ state }: { state: HttpSendState }) {
  const t = useT();
  const error = state.error;
  const title = error === null ? t("response.http.failed") : error.kind === "timeout" ? t("response.http.timedOut") : errorTitle(error.kind);
  return (
    <div className={styles.error} role="alert">
      <p className={styles.errorTitle}>
        <IconWarning size={14} />
        {title}
      </p>
      <p className={styles.errorMessage}>{error?.message ?? t("response.http.noResponse")}</p>
      {error !== null && error.hint !== "" && <p className={styles.errorHint}>{error.hint}</p>}
    </div>
  );
}
