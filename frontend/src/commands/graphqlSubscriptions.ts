// The subscriptions of open GraphQL requests (GQL-30, GQL-33, GQL-34): one
// slot per request, holding the state of its latest subscription and its
// live event log. Subscriptions run as graphql.subscription operations
// (docs/events.md). The slots live outside React, so switching tabs keeps
// a running subscription and its log; closing the tab stops it and drops
// the log. See docs/graphql-subscriptions.md.
import { toAppError, type AppError } from "../lib/errors";
import {
  SUBSCRIPTION_DOMAIN,
  stopGraphqlSubscription,
  subscribeGraphql,
  subscriptionLogMessage,
  type SubscriptionCloseDTO,
  type SubscriptionEventDTO,
} from "../lib/graphqlSubscription";
import { UNRESOLVED_CODE } from "../lib/http";
import { MessageSource } from "../lib/messageSource";
import { startOperation, type OperationHandle, type StartedDTO } from "../lib/operations";
import type { RequestId, Stores } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { confirmUnresolved, type PrepareSend } from "./httpSends";
import { onTabClosed } from "./tabs";

/** Log entries kept per subscription; older ones are dropped. */
export const SUBSCRIPTION_LOG_MAX = 10_000;

/**
 * "idle" before the first subscription; "starting" until Go has accepted
 * it; "running" while it runs; then how it ended: "ok" (the server
 * completed it), "error" or "cancelled" (stopped).
 */
export type SubscriptionStatus = "idle" | "starting" | "running" | "ok" | "error" | "cancelled";

export interface SubscriptionState {
  readonly status: SubscriptionStatus;
  /** Whether the server acknowledged the connection (connection_ack) and it is still open. */
  readonly connected: boolean;
  /** `next` messages received so far. */
  readonly events: number;
  /** Of them, the ones with GraphQL errors, plus `error` messages. */
  readonly errors: number;
  /** Bytes of `next` payloads received. */
  readonly receivedBytes: number;
  /** How the connection closed, once it has. */
  readonly close: SubscriptionCloseDTO | null;
  /** Why the subscription failed or could not start; null otherwise. */
  readonly error: AppError | null;
  /** When the subscription was started (Date.now()), for a live timer; null before the first. */
  readonly startedAt: number | null;
  /** How long it ran, once it ended. */
  readonly elapsedMs: number | null;
  /** The transport it runs over (graphql-transport-ws, graphql-ws or sse); "" until it connects. */
  readonly transport: string;
  /** The type of the operation that runs (query, mutation, subscription); "" until it connects or when unknown. */
  readonly operationType: string;
  /**
   * The result of a query or mutation run through the slot (GQL-35): the
   * merged result once the server completed it, else the latest result
   * payload received; null before the first.
   */
  readonly result: string | null;
}

const INITIAL: SubscriptionState = {
  status: "idle",
  connected: false,
  events: 0,
  errors: 0,
  receivedBytes: 0,
  close: null,
  error: null,
  startedAt: null,
  elapsedMs: null,
  transport: "",
  operationType: "",
  result: null,
};

/** Received message types that carry a GraphQL result: next, data (legacy), response (SSE without a stream). */
const RESULT_TYPES = new Set(["next", "data", "response"]);

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** One GraphQL request's latest subscription and its event log. */
export class GraphqlSubscription {
  readonly requestId: string;
  readonly log: MessageSource;
  private state: SubscriptionState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Bumped by every start and stop, so a stale callback does nothing. */
  private generation = 0;

  constructor(requestId: string, options: { log?: MessageSource } = {}) {
    this.requestId = requestId;
    this.log = options.log ?? new MessageSource({ maxMessages: SUBSCRIPTION_LOG_MAX });
  }

  getState = (): SubscriptionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<SubscriptionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a subscription is starting or running. */
  get running(): boolean {
    return this.state.status === "starting" || this.state.status === "running";
  }

  /**
   * Starts the subscription, unless one is running (restart after Stop).
   * The log is cleared first. `prepare` runs first (it flushes the
   * editor's queued edits to Go). Undefined {{variables}} ask "Subscribe
   * anyway" (ENV-11); declining puts the state back.
   */
  start(prepare?: PrepareSend, options: { now?: number } = {}): void {
    if (this.running) return;
    const generation = ++this.generation;
    const before = this.state;
    this.log.clear();
    this.set({ ...INITIAL, status: "starting", startedAt: options.now ?? Date.now() });
    const mine = () => this.generation === generation;
    let declined = false;
    const start = async (): Promise<StartedDTO> => {
      try {
        return await subscribeGraphql(this.requestId);
      } catch (err: unknown) {
        const e = toAppError(err);
        if (e.code !== UNRESOLVED_CODE || !mine()) throw err;
        const names = (e.details.names ?? "").split(",").filter((n) => n !== "");
        if (!(await confirmUnresolved(names, "subscribe")) || !mine()) {
          declined = true;
          throw err;
        }
        return await subscribeGraphql(this.requestId, true);
      }
    };
    void (async () => {
      try {
        await prepare?.();
      } catch (err: unknown) {
        console.warn("graphql: the edits were not flushed before subscribing", toAppError(err));
      }
      if (!mine()) return;
      const handle = startOperation<SubscriptionEventDTO>(SUBSCRIPTION_DOMAIN, start, {
        onStarted: () => {
          if (mine()) this.set({ status: "running" });
        },
        onData: (items) => {
          if (mine()) this.handleItems(items);
        },
        onDone: (result) => {
          if (!mine()) return;
          this.handle = null;
          if (declined) {
            this.set(before);
            return;
          }
          if (result.opId === null && result.error !== null) {
            this.log.append({ direction: "system", kind: "error", text: errorText(result.error) });
          }
          this.set({
            status: result.outcome,
            connected: false,
            error: result.error,
            elapsedMs: result.opId !== null ? result.elapsedMs : null,
          });
        },
      });
      if (mine()) this.handle = handle;
      else {
        handle.cancel();
        handle.dispose();
      }
    })();
  }

  /** Applies events of the running subscription: the log and the counters. */
  handleItems(items: readonly SubscriptionEventDTO[]): void {
    const patch: { -readonly [K in keyof SubscriptionState]?: SubscriptionState[K] } = {};
    let events = this.state.events;
    let errors = this.state.errors;
    let received = this.state.receivedBytes;
    let merged = false;
    const logged: SubscriptionEventDTO[] = [];
    for (const ev of items) {
      if (ev.kind === "result") {
        // The merged result of a query or mutation: the response view shows it.
        patch.result = ev.payload;
        merged = true;
        continue;
      }
      logged.push(ev);
      if (ev.kind === "connecting") {
        patch.transport = ev.transport ?? "";
        patch.operationType = ev.operationType ?? "";
      } else if (ev.kind === "open" && (ev.status ?? 0) > 0) {
        // An SSE response: there is no acknowledgement to wait for.
        patch.connected = true;
      } else if (ev.kind === "closed") {
        patch.connected = false;
        patch.close = ev.close;
      } else if (ev.kind === "message" && ev.direction === "received") {
        if (ev.type === "connection_ack") patch.connected = true;
        else if (RESULT_TYPES.has(ev.type) && ev.payload !== "" && ev.note !== "unknown_id") {
          events++;
          received += ev.size;
          if (ev.errorCount > 0) errors++;
          if (!merged) patch.result = ev.payload;
        } else if (ev.type === "error" && ev.note !== "unknown_id") errors++;
      }
    }
    patch.events = events;
    patch.errors = errors;
    patch.receivedBytes = received;
    this.log.append(logged.map(subscriptionLogMessage));
    this.set(patch);
  }

  /**
   * Stops the running subscription: Go sends complete and closes the
   * connection. The state follows when Go has stopped it.
   */
  stop(): void {
    if (!this.running) return;
    const handle = this.handle;
    if (handle === null) {
      // Still flushing the edits: nothing was started yet.
      this.generation++;
      this.set({ status: "cancelled" });
      return;
    }
    const opId = handle.opId();
    if (opId === null) {
      handle.cancel();
      return;
    }
    // false means it was already over: the done event says how. If the
    // call fails, cancelling the operation stops it too.
    stopGraphqlSubscription(opId).then(
      () => undefined,
      () => handle.cancel(),
    );
  }

  /** Stops a running subscription and releases the log. */
  dispose(): void {
    this.generation++;
    const handle = this.handle;
    this.handle = null;
    handle?.cancel();
    handle?.dispose();
    this.listeners.clear();
    this.log.dispose();
  }
}

/** The subscription slots of every open GraphQL request. */
export class GraphqlSubscriptions {
  private readonly slots = new Map<string, GraphqlSubscription>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a request's tab stops its subscription and drops its log.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The slot of request `requestId`, created on first use. */
  get(requestId: string): GraphqlSubscription {
    let slot = this.slots.get(requestId);
    if (slot === undefined) {
      slot = new GraphqlSubscription(requestId);
      this.slots.set(requestId, slot);
    }
    return slot;
  }

  /** The slot of `requestId` if it has one. */
  peek(requestId: string): GraphqlSubscription | undefined {
    return this.slots.get(requestId);
  }

  /** Stops and forgets the subscriptions of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const slot = this.slots.get(id);
      if (slot === undefined) continue;
      this.slots.delete(id);
      slot.dispose();
    }
  }

  /** Stops every subscription and stops following tab closes. */
  dispose(): void {
    this.drop([...this.slots.keys()]);
    this.unsubscribe();
  }
}

let registry: GraphqlSubscriptions | null = null;

/** The app's subscription slots, created on first use. */
export function graphqlSubscriptions(): GraphqlSubscriptions {
  registry ??= new GraphqlSubscriptions();
  return registry;
}

/** Drops the registry, for tests. */
export function resetGraphqlSubscriptions(): void {
  registry?.dispose();
  registry = null;
}

/**
 * Starts (or restarts) the subscription of GraphQL request `id` (the
 * Subscribe button and the request.send shortcut on a subscription): its
 * queued edits reach Go first. Does nothing while one runs.
 */
export function startSubscription(stores: Stores, id: RequestId): void {
  graphqlSubscriptions()
    .get(id)
    .start(() => waitForQueuedEdits(stores, id));
}

/** Stops the running subscription of request `id`, if any. */
export function stopSubscription(id: RequestId): void {
  graphqlSubscriptions().peek(id)?.stop();
}
