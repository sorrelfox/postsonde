// GraphQL subscriptions over WebSocket (GQL-30, GQL-33, GQL-34): the calls
// into GraphQLService.Subscribe / StopSubscription, the operation that
// runs (a subscription gets Subscribe instead of Send), and the events of
// the live log as messages of the message log. A subscription runs as a
// graphql.subscription operation (docs/events.md): every protocol message
// and connection change arrives as a data item. See
// docs/graphql-subscriptions.md.
import { t } from "../i18n";
import {
  GraphQLService,
  type SubscriptionCloseDTO,
  type SubscriptionEventDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql";
import type { GraphQLTransportDTO, RequestSettingsDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { CancellablePromise } from "./calls";
import type { OperationDTO } from "./graphqlSchema";
import type { LogMessageInput, MessageMetaValue } from "./messageSource";
import type { StartedDTO } from "./operations";

export type { SubscriptionCloseDTO, SubscriptionEventDTO };

/** The domain of subscriptions: their events are `graphql.subscription:op:data|progress|done`. */
export const SUBSCRIPTION_DOMAIN = "graphql.subscription";

/**
 * Starts running request `requestId` over WebSocket as Go has it (the
 * edits sent so far included). Resolves with the operation ID at once; a
 * request that cannot be built rejects instead, undefined {{variables}}
 * with template.unresolved unless `allowUnresolved`.
 */
export function subscribeGraphql(requestId: string, allowUnresolved = false): CancellablePromise<StartedDTO> {
  return GraphQLService.Subscribe({ requestId, allowUnresolved });
}

/** Stops subscription `opId`: Go sends complete and closes the connection. Resolves false when it was not running. */
export function stopGraphqlSubscription(opId: string): CancellablePromise<boolean> {
  return GraphQLService.StopSubscription({ opId });
}

/**
 * The type of the operation that runs: the only operation's, or the one
 * named `saved` when there are several; null when it cannot be told (no
 * operation, or several and none picked), as Go decides
 * (request.GraphQLOperationName).
 */
export function runningOperationType(operations: readonly OperationDTO[], saved: string): string | null {
  if (operations.length === 1) return operations[0]?.type ?? null;
  if (saved === "") return null;
  return operations.find((op) => op.name === saved)?.type ?? null;
}

/** Whether the operation that runs is a subscription, so the editor offers Subscribe (GQL-34). */
export function runsSubscription(operations: readonly OperationDTO[], saved: string): boolean {
  return runningOperationType(operations, saved) === "subscription";
}

/**
 * The transports an operation can run over (GQL-35), as stored in
 * settings.graphqlTransport: "" is the default (http for queries and
 * mutations, ws for subscriptions).
 */
export type GraphqlTransport = "" | "http" | "ws" | "ws-legacy" | "sse";

/** The operation types that each pick a transport. */
export type GraphqlOperationKind = "query" | "mutation" | "subscription";

export const GRAPHQL_OPERATION_KINDS: readonly GraphqlOperationKind[] = ["query", "mutation", "subscription"];

/** The name of each transport in the UI. */
export const TRANSPORT_LABELS: Readonly<Record<Exclude<GraphqlTransport, "">, string>> = {
  http: "HTTP",
  get ws() {
    return t("lib.graphqlSubscription.transport.ws");
  },
  get "ws-legacy"() {
    return t("lib.graphqlSubscription.transport.wsLegacy");
  },
  get sse() {
    return t("lib.graphqlSubscription.transport.sse");
  },
};

/** The transports each operation type offers; subscriptions cannot run over plain HTTP. */
export function transportChoices(kind: GraphqlOperationKind): readonly Exclude<GraphqlTransport, "">[] {
  return kind === "subscription" ? ["ws", "ws-legacy", "sse"] : ["http", "ws", "ws-legacy", "sse"];
}

/** The default transport of an operation type. */
export function defaultTransport(kind: GraphqlOperationKind): Exclude<GraphqlTransport, ""> {
  return kind === "subscription" ? "ws" : "http";
}

/** An operation type, from runningOperationType; anything else (null) counts as a query, as in Go. */
function kindOf(operationType: string | null): GraphqlOperationKind {
  return operationType === "mutation" || operationType === "subscription" ? operationType : "query";
}

/**
 * The transport the settings pick for an operation of this type, the
 * default when they pick none (request.GraphQLTransportFor). A value this
 * version does not know is returned as it is; Go refuses it.
 */
export function transportFor(settings: RequestSettingsDTO, operationType: string | null): string {
  const kind = kindOf(operationType);
  const picked = settings.graphqlTransport?.[kind] ?? "";
  return picked !== "" ? picked : defaultTransport(kind);
}

/**
 * Whether the operation runs through the subscription slot (Subscribe,
 * the live log): every subscription, and a query or mutation over
 * WebSocket. A query or mutation over HTTP or SSE is sent with Send: an
 * SSE answer is read as a normal (incremental) response.
 */
export function runsOverSocket(settings: RequestSettingsDTO, operationType: string | null): boolean {
  if (kindOf(operationType) === "subscription") return true;
  const transport = transportFor(settings, operationType);
  return transport === "ws" || transport === "ws-legacy";
}

/**
 * The stored transports after picking `value` for `kind` ("" for the
 * default); null when nothing is left to store, so the file leaves the
 * member out.
 */
export function transportPatch(current: GraphQLTransportDTO | null | undefined, kind: GraphqlOperationKind, value: GraphqlTransport): GraphQLTransportDTO | null {
  const next: GraphQLTransportDTO = { query: "", mutation: "", subscription: "", extra: "", ...current, [kind]: value };
  if (next.query === "" && next.mutation === "" && next.subscription === "" && next.extra === "") return null;
  return next;
}

function eventTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

/** "1000 (normal closure): bye", "4403 (forbidden)". */
export function closeText(close: SubscriptionCloseDTO): string {
  const code = close.codeName !== "" ? t("lib.graphqlSubscription.closeCode", { code: close.code, name: close.codeName }) : String(close.code);
  return close.reason !== "" ? t("lib.graphqlSubscription.closeReason", { close: code, reason: close.reason }) : code;
}

/**
 * The log kind of a protocol message: its type, or "invalid" for one that
 * could not be parsed. The GraphQL `error` message keeps the log's "error"
 * kind (red).
 */
function messageKind(ev: SubscriptionEventDTO): string {
  return ev.type !== "" ? ev.type : "invalid";
}

/** A subscription event as a message of the log (GQL-34). */
export function subscriptionLogMessage(ev: SubscriptionEventDTO): LogMessageInput {
  const time = eventTime(ev.at);
  switch (ev.kind) {
    case "connecting": {
      const meta: Record<string, MessageMetaValue> = { url: ev.url };
      if (ev.transport) meta.transport = ev.transport;
      if (ev.operationType) meta.operation = ev.operationType;
      if (ev.transport === "sse") return { time, direction: "system", kind: "connecting", text: t("lib.graphqlSubscription.sending", { method: ev.method ?? "POST", url: ev.url }), meta };
      return { time, direction: "system", kind: "connecting", text: t("lib.graphqlSubscription.connecting", { url: ev.url }), meta };
    }
    case "open":
      if (ev.status) {
        const type = ev.contentType ? ` · ${ev.contentType}` : "";
        return { time, direction: "system", kind: "open", text: `HTTP ${ev.status}${type}`, meta: { status: ev.status } };
      }
      return { time, direction: "system", kind: "open", text: t("lib.graphqlSubscription.connected", { protocol: ev.subprotocol || "graphql-transport-ws" }) };
    case "result":
      return { time, direction: "system", kind: "result", text: ev.payload, size: ev.size };
    case "closed": {
      const meta: Record<string, MessageMetaValue> = {};
      let text = t("lib.graphqlSubscription.connectionClosed");
      if (ev.close !== null) {
        text = t("lib.graphqlSubscription.closed", { close: closeText(ev.close) });
        meta.code = ev.close.code;
        if (ev.close.reason !== "") meta.reason = ev.close.reason;
        meta.clean = ev.close.wasClean;
        meta["initiated by"] = ev.close.initiatedBy;
      }
      if (ev.error !== null) {
        text = ev.close !== null && ev.close.code !== 1006 ? `${text} · ${ev.error.message}` : ev.error.message;
        meta.error = ev.error.message;
      }
      return { time, direction: "system", kind: "closed", text, meta };
    }
    default: {
      const meta: Record<string, MessageMetaValue> = {};
      if (ev.id !== "") meta.id = ev.id;
      if (ev.errorCount > 0) meta.errors = ev.errorCount;
      if (ev.note === "unknown_id") meta.note = t("lib.graphqlSubscription.note.unknownId");
      else if (ev.note === "ignored") meta.note = t("lib.graphqlSubscription.note.ignored");
      else if (ev.note === "not_event_stream") meta.note = t("lib.graphqlSubscription.note.notEventStream");
      if (ev.error !== null) meta.error = ev.error.message;
      if (ev.binary) meta.binary = true;
      let badge: string | undefined;
      if (ev.errorCount > 0 && ev.type !== "error") badge = t("lib.graphqlSubscription.errors", { count: ev.errorCount });
      else if (ev.id !== "") badge = t("lib.graphqlSubscription.id", { id: ev.id });
      return {
        time,
        direction: ev.direction === "sent" ? "sent" : "received",
        kind: messageKind(ev),
        badge,
        text: ev.payload,
        size: ev.size,
        meta: Object.keys(meta).length > 0 ? meta : undefined,
      };
    }
  }
}
